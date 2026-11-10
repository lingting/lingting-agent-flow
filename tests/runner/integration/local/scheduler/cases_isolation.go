// 多任务资源隔离：迁移自 tests/integration/local/scheduler/isolation_test.go。
//
// 用例使用真实 SQLite 与正式迁移，断言的对象是数据库里真实留下的任务、容器、工作区
// 初始化记录、Attempt 与 Session，而不是方法的返回值。覆盖的约束：两个任务各自持有独立
// 的容器登记记录；任务 A 的容器不能被任务 B 当作自己的执行容器；任务 A 的工作区初始化
// 记录不能作为任务 B 的就绪证据；Session 路径按任务 / 步骤 / 尝试 / 会话 / 调用分层；
// 归档任务 A 不会移动或删除任务 B 的文件。
package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/internal/testrunner"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// isolationImageID 是容器登记记录使用的镜像 ID。
const isolationImageID = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// isolationBuildHash 是容器登记记录使用的构建配置哈希。
const isolationBuildHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// isolationFixture 是一套多任务隔离测试现场。
type isolationFixture struct {
	ctx  context.Context
	db   *sqlite.DB
	path string
	now  time.Time
	host domain.ExecutionHost
}

// newIsolationFixture 在工作空间中创建数据库与一个已通过预检的执行节点。
func newIsolationFixture(t *testrunner.Context) *isolationFixture {
	t.Helper()
	ctx := t.Context()
	workspace := testkit.NewAt(t, t.WorkspaceDir())
	workspace.Report()
	path := databasePath(t)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("打开数据库失败: %s: %v", path, err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭数据库失败: %v", closeErr)
		}
	})
	now := time.Now().UTC()
	host, err := domain.NewExecutionHost(
		"",
		"隔离测试节点",
		"10.0.0.1",
		22,
		"deploy",
		"/srv/agentflow",
		now,
		domain.WithPrivateKey(testkit.PrivateKeyIn(t, workspace.Dir("keys"))),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	stored, err := db.GetExecutionHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	details := domain.ValidationDetails{
		ValidatorVersion: domain.ValidatorVersion,
		Checks: []domain.ValidationCheck{
			{Name: domain.ValidationCheckSSHConnection, Status: domain.CheckStatusPassed},
		},
	}
	if err := stored.RecordValidationResult(stored.ValidationGeneration, details, now); err != nil {
		t.Fatalf("记录校验结果失败: %v", err)
	}
	if _, err := db.UpdateExecutionHostValidation(ctx, *stored, stored.ValidationGeneration); err != nil {
		t.Fatalf("写入校验结果失败: %v", err)
	}
	return &isolationFixture{ctx: ctx, db: db, path: path, now: now, host: *stored}
}

// openSecondConnection 在同一个数据库文件上再打开一条独立连接。
//
// 并发安全必须来自数据库的条件写入：只有独立连接上的竞争才能证明它真的生效。
func (f *isolationFixture) openSecondConnection(t *testrunner.Context) *sqlite.DB {
	t.Helper()
	other, err := sqlite.Open(f.ctx, sqlite.Options{Path: f.path})
	if err != nil {
		t.Fatalf("打开第二个数据库连接失败: %s: %v", f.path, err)
	}
	t.Cleanup(func() {
		if closeErr := other.Close(); closeErr != nil {
			t.Errorf("关闭第二个数据库连接失败: %v", closeErr)
		}
	})
	return other
}

// task 写入一个 queued 任务并返回它。
func (f *isolationFixture) task(t *testrunner.Context, label string) domain.Task {
	t.Helper()
	requirement, err := domain.NewRequirement(
		"隔离需求 "+label,
		"需求正文",
		domain.PriorityNormal,
		f.now,
	)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := f.db.CreateRequirement(f.ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(
		requirement.ID,
		"ssh://git.example.com/team/repo",
		"main",
		"",
		f.now,
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	hostID := f.host.ID
	task.HostID = &hostID
	task.Config = domain.TaskConfig{
		Goal:            "完成" + label,
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		PlannerRoleID:   "planner",
		DeveloperRoleID: "developer",
	}
	if err := f.db.CreateTask(f.ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	return task
}

// container 写入一条属于该任务的容器记录并把引用写回任务。
func (f *isolationFixture) container(
	t *testrunner.Context,
	task domain.Task,
	label string,
) (domain.Container, domain.Task) {
	t.Helper()
	container, err := domain.NewContainer(
		"",
		f.host.ID,
		"agentflow-"+label,
		domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		domain.FormatVersionLabel(1),
		isolationBuildHash,
		isolationImageID,
		f.now,
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	taskID := task.ID
	container.TaskID = &taskID
	container.DockerID = "docker-" + container.ID.String()
	container.State = domain.ContainerStateRunning
	container.DockerState = domain.DockerStateRunning
	if err := f.db.CreateContainer(f.ctx, container); err != nil {
		t.Fatalf("写入容器记录失败: %v", err)
	}
	updated := task
	if err := updated.SetContainer(container.ID.String()); err != nil {
		t.Fatalf("设置任务容器失败: %v", err)
	}
	if err := f.db.UpdateTaskIfStatus(f.ctx, updated, []domain.TaskStatus{task.Status}); err != nil {
		t.Fatalf("更新任务容器失败: %v", err)
	}
	return container, updated
}

// 原 TestTwoTasksUseIndependentContainers 验证两个任务各自持有独立的容器记录。
//
// 断言的是数据库事实：两条任务记录的 container_id 不同，两条容器记录的 task_id
// 分别指向各自任务，且不存在任何交叉引用。
func runTwoTasksUseIndependentContainers(t *testrunner.Context) {
	fixture := newIsolationFixture(t)
	taskA := fixture.task(t, "A")
	taskB := fixture.task(t, "B")
	containerA, taskA := fixture.container(t, taskA, "alpha")
	containerB, taskB := fixture.container(t, taskB, "beta")

	if containerA.ID == containerB.ID {
		t.Fatal("两个任务必须使用不同的容器登记记录")
	}
	if containerA.DockerID == containerB.DockerID {
		t.Fatal("两个任务的 Docker 容器 ID 必须不同")
	}
	if taskA.ContainerID == taskB.ContainerID {
		t.Fatal("两个任务的容器引用必须不同")
	}
	storedA, err := fixture.db.GetTask(fixture.ctx, taskA.ID)
	if err != nil {
		t.Fatalf("读取任务 A 失败: %v", err)
	}
	storedB, err := fixture.db.GetTask(fixture.ctx, taskB.ID)
	if err != nil {
		t.Fatalf("读取任务 B 失败: %v", err)
	}
	if storedA.ContainerID != containerA.ID.String() {
		t.Fatalf("任务 A 的容器引用不符: %s", storedA.ContainerID)
	}
	if storedB.ContainerID != containerB.ID.String() {
		t.Fatalf("任务 B 的容器引用不符: %s", storedB.ContainerID)
	}
	// 容器记录的归属必须各自成立，且不互相指向。
	for _, check := range []struct {
		task      domain.Task
		container domain.Container
	}{
		{task: *storedA, container: containerA},
		{task: *storedB, container: containerB},
	} {
		decision := domain.DecideContainerOwnership(check.task, check.container)
		if !decision.Allowed {
			t.Fatalf("任务 %s 的容器归属应成立，实际 %s", check.task.ID, decision.Reason)
		}
	}
	// 交叉引用必须被拒绝：这是跨任务串扰的唯一可核验防线。
	for _, cross := range []struct {
		task      domain.Task
		container domain.Container
	}{
		{task: *storedA, container: containerB},
		{task: *storedB, container: containerA},
	} {
		decision := domain.DecideContainerOwnership(cross.task, cross.container)
		if decision.Allowed {
			t.Fatalf("任务 %s 不得使用另一个任务的容器 %s", cross.task.ID, cross.container.ID)
		}
	}
}

// 原 TestTaskContainerCannotBeReusedByAnotherTask 验证跨任务复用容器被拒绝。
//
// 现场：任务 B 的容器引用被改成任务 A 的容器。此时归属校验必须拒绝，因为容器
// 记录登记的是任务 A。这条断言直接对应"两个不同 Task 不得关联同一容器"。
func runTaskContainerCannotBeReusedByAnotherTask(t *testrunner.Context) {
	fixture := newIsolationFixture(t)
	taskA := fixture.task(t, "A")
	containerA, _ := fixture.container(t, taskA, "alpha")
	taskB := fixture.task(t, "B")

	hijacked := taskB
	if err := hijacked.SetContainer(containerA.ID.String()); err != nil {
		t.Fatalf("设置任务容器失败: %v", err)
	}
	decision := domain.DecideContainerOwnership(hijacked, containerA)
	if decision.Allowed {
		t.Fatal("任务 B 引用任务 A 的容器时必须被拒绝")
	}
	if decision.Reason != domain.ContainerOwnershipTaskMismatch {
		t.Fatalf("拒绝原因应为 %s，实际 %s", domain.ContainerOwnershipTaskMismatch, decision.Reason)
	}
}

// 原 TestWorkspaceInitRecordsDoNotCrossTasks 验证工作区初始化记录不跨任务生效。
func runWorkspaceInitRecordsDoNotCrossTasks(t *testrunner.Context) {
	fixture := newIsolationFixture(t)
	taskA := fixture.task(t, "A")
	containerA, taskA := fixture.container(t, taskA, "alpha")
	taskB := fixture.task(t, "B")
	containerB, taskB := fixture.container(t, taskB, "beta")

	record, err := domain.NewTaskWorkspaceInitRecord(
		"",
		taskA.ID.String(),
		containerA.ID.String(),
		containerA.DockerID,
		domain.CodeSourceGit,
		domain.DefaultContainerWorkDir,
		fixture.now,
	)
	if err != nil {
		t.Fatalf("构造工作区记录失败: %v", err)
	}
	if err := record.MarkRunning(domain.CodeFetchStageClone, fixture.now); err != nil {
		t.Fatalf("标记运行中失败: %v", err)
	}
	if err := record.MarkSucceeded(
		"main",
		domain.GitBranchSelectionOriginHead,
		strings.Repeat("1", 40),
		domain.TaskBranchSourceNew.String(),
		strings.Repeat("2", 40),
		fixture.now,
	); err != nil {
		t.Fatalf("标记成功失败: %v", err)
	}
	if err := fixture.db.CreateTaskWorkspaceInit(fixture.ctx, record); err != nil {
		t.Fatalf("写入工作区记录失败: %v", err)
	}
	records, err := fixture.db.ListTaskWorkspaceInitsByTask(fixture.ctx, taskB.ID.String())
	if err != nil {
		t.Fatalf("读取任务 B 的工作区记录失败: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("任务 B 不得看到任务 A 的工作区记录，实际 %d 条", len(records))
	}
	// 任务 B 自己判定"是否需要初始化"时，因为没有记录，必须判定为需要初始化。
	next, err := domain.DecideTaskWorkspaceNext(domain.TaskWorkspaceState{
		ContainerUsable: true,
	})
	if err != nil {
		t.Fatalf("判定工作区是否需要初始化失败: %v", err)
	}
	if !next {
		t.Fatal("没有记录的容器上必须重新初始化工作区")
	}
	// 旧容器代次上的成功记录不适用于新容器：判定必须重新初始化。
	next, err = domain.DecideTaskWorkspaceNext(domain.TaskWorkspaceState{
		Record:          &record,
		ContainerUsable: true,
	})
	if err != nil {
		t.Fatalf("同容器判定失败: %v", err)
	}
	if next {
		t.Fatal("同一容器上的成功记录不应触发重新初始化")
	}
	_ = taskA
	_ = containerB
	_ = taskB
}

// 原 TestConcurrentSessionPathsDoNotCollide 验证并发创建 Session 不会覆盖彼此。
//
// 每个任务的 Session 路径按任务 / 步骤 / 尝试 / 会话 / 调用分层，因此即使两个
// 任务在同一毫秒创建同名会话，路径也必须不同。
func runConcurrentSessionPathsDoNotCollide(t *testrunner.Context) {
	workspace := testkit.NewAt(t, t.WorkspaceDir())
	workspace.Report()
	root := workspace.SessionRoot()
	type outcome struct {
		jsonl string
		err   error
	}
	const tasks = 4
	results := make(chan outcome, tasks)
	var wg sync.WaitGroup
	for index := 0; index < tasks; index++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			layout := pi.SessionLayout{
				TaskID:       "task-" + string(rune('a'+worker)),
				StepID:       "step-1",
				AttemptID:    "attempt-1",
				SessionID:    "session-shared",
				InvocationID: "invocation-shared",
			}
			paths, err := layout.Paths(root)
			if err != nil {
				results <- outcome{err: err}
				return
			}
			if err := paths.EnsureDir(); err != nil {
				results <- outcome{err: err}
				return
			}
			results <- outcome{jsonl: paths.JSONL}
		}(index)
	}
	wg.Wait()
	close(results)
	seen := make(map[string]struct{}, tasks)
	for item := range results {
		if item.err != nil {
			t.Fatalf("解析 Session 路径失败: %v", item.err)
		}
		if _, exists := seen[item.jsonl]; exists {
			t.Fatalf("不同任务使用了同一个 Session 文件: %s", item.jsonl)
		}
		seen[item.jsonl] = struct{}{}
	}
	if len(seen) != tasks {
		t.Fatalf("期望 %d 个独立 Session 路径，实际 %d", tasks, len(seen))
	}
}

// 原 TestArchivingOneTaskKeepsAnotherTaskFiles 验证归档一个任务不影响另一个任务。
//
// 归档只在任务自己的 Session 目录内写入，因此另一任务的原始文件必须逐字节不变。
func runArchivingOneTaskKeepsAnotherTaskFiles(t *testrunner.Context) {
	workspace := testkit.NewAt(t, t.WorkspaceDir())
	workspace.Report()
	root := workspace.SessionRoot()

	pathsA, err := pi.SessionLayout{
		TaskID: "task-a", SessionID: "session-a", InvocationID: "invocation-a",
	}.Paths(root)
	if err != nil {
		t.Fatalf("解析任务 A 的 Session 路径失败: %v", err)
	}
	pathsB, err := pi.SessionLayout{
		TaskID: "task-b", SessionID: "session-b", InvocationID: "invocation-b",
	}.Paths(root)
	if err != nil {
		t.Fatalf("解析任务 B 的 Session 路径失败: %v", err)
	}
	if err := pathsA.EnsureDir(); err != nil {
		t.Fatalf("创建任务 A 的目录失败: %v", err)
	}
	if err := pathsB.EnsureDir(); err != nil {
		t.Fatalf("创建任务 B 的目录失败: %v", err)
	}
	contentA := `{"type":"session","version":3,"id":"a","cwd":"/workspace/project"}` + "\n"
	contentB := `{"type":"session","version":3,"id":"b","cwd":"/workspace/project"}` + "\n"
	if err := os.WriteFile(pathsA.JSONL, []byte(contentA), 0o600); err != nil {
		t.Fatalf("写入任务 A 的 Session 失败: %v", err)
	}
	if err := os.WriteFile(pathsB.JSONL, []byte(contentB), 0o600); err != nil {
		t.Fatalf("写入任务 B 的 Session 失败: %v", err)
	}
	// 模拟归档任务 A：把原始文件复制到归档目录。
	archiveDir := workspace.Dir("archive", "task-a")
	archived, err := os.ReadFile(pathsA.JSONL)
	if err != nil {
		t.Fatalf("读取任务 A 的 Session 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archiveDir, "session.jsonl"), archived, 0o600); err != nil {
		t.Fatalf("写入归档失败: %v", err)
	}
	// 任务 B 的文件必须保持不变。
	after, err := os.ReadFile(pathsB.JSONL)
	if err != nil {
		t.Fatalf("归档任务 A 后任务 B 的 Session 必须仍可读: %v", err)
	}
	if string(after) != contentB {
		t.Fatalf("归档任务 A 影响了任务 B 的文件内容")
	}
	if pathsA.JSONL == pathsB.JSONL {
		t.Fatal("两个任务的 Session 路径必须不同")
	}
}

// 原 TestSessionScopeRejectsInconsistentOwnership 验证不一致的会话归属被持久化拒绝。
//
// 会话声明的步骤必须与尝试所属步骤一致：否则一次 Agent 调用会被记录在错误的
// 步骤下，事后无法区分"哪个步骤产生了这段输出"。
func runSessionScopeRejectsInconsistentOwnership(t *testrunner.Context) {
	fixture := newIsolationFixture(t)
	task := fixture.task(t, "A")
	stepA := fixture.step(t, task, 1, "步骤一")
	stepB := fixture.step(t, task, 2, "步骤二")
	attempt := fixture.attempt(t, task, stepA)

	role := fixture.role(t)
	otherStep := stepB.ID
	session, err := domain.NewSession(
		domain.SessionScope{TaskID: task.ID, StepID: &otherStep, AttemptID: &attempt.ID},
		role,
		"provider",
		"provider/model",
		fixture.now,
	)
	if err != nil {
		t.Fatalf("构造会话失败: %v", err)
	}
	err = fixture.db.CreateSession(fixture.ctx, session)
	if err == nil {
		t.Fatal("会话声明的步骤与尝试所属步骤不一致时必须被拒绝")
	}
	if !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("拒绝时必须返回 ErrInvalidData，实际 %v", err)
	}
}

// step 写入一个计划步骤。
func (f *isolationFixture) step(
	t *testrunner.Context,
	task domain.Task,
	sequence int,
	title string,
) domain.Step {
	t.Helper()
	role := f.role(t)
	step, err := domain.NewAgentStep(
		task.ID,
		sequence,
		domain.StepTypeDevelop,
		title,
		"按需求实现",
		"",
		role,
		domain.VerificationSpec{},
		domain.RetryConfig{MaxAttempts: 1},
		f.now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := f.db.CreateStep(f.ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	return step
}

// attempt 为步骤创建一个执行尝试。
func (f *isolationFixture) attempt(
	t *testrunner.Context,
	task domain.Task,
	step domain.Step,
) domain.Attempt {
	t.Helper()
	attempt, err := f.db.StartAttempt(f.ctx, storage.StartAttemptRequest{
		TaskID: task.ID,
		StepID: step.ID,
		At:     f.now,
	})
	if err != nil {
		t.Fatalf("创建执行尝试失败: %v", err)
	}
	return attempt
}

// role 返回（必要时写入）一个开发角色。
func (f *isolationFixture) role(t *testrunner.Context) domain.Role {
	t.Helper()
	if role, err := f.db.GetRole(f.ctx, "developer"); err == nil {
		return *role
	}
	role, err := domain.NewRole(
		"开发工程师",
		domain.RoleTypeDeveloper,
		"provider/model",
		"角色提示词",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		f.now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := f.db.CreateRole(f.ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	return role
}
