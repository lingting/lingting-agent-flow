// 真实环境 M6 验收：项目级顺序执行、跨项目并行、同项目失败阻塞与需求删除。
//
// 与 unit / local 用例的分工：
//   - 单元用例用受控替身覆盖判定分支；
//   - 本文件只用真实依赖（真实 SSH、真实远程 Docker、容器内真实 Git 与真实 Pi 模型），
//     验证项目级调度与需求删除在真实环境下确实成立。
//
// 断言只依赖持久化事实与领域规则，不假设模型输出特定文本：模型可以生成不同的计划与
// 代码，但"状态迁移合法""失败必须阻塞后续任务""删除不改写业务状态"这些结论与模型输出无关。
package workflow_remote

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

const (
	// m6PollInterval 是轮询项目与任务状态的间隔。
	m6PollInterval = 2 * time.Second
	// m6SettleTimeout 是等待单个任务真实收敛（完成、失败或取消）的时限。
	//
	// 真实链路包含容器准备、容器内 Git Clone、规划、开发、验证与代码保存，实测单任务
	// 在数分钟内收敛；这里给出足够余量，超时即视为未取得证据，不用更长的等待掩盖卡死。
	m6SettleTimeout = 15 * time.Minute
	// m6CancelSettleTimeout 是停止请求之后等待真实收敛的时限。
	//
	// 取消只记录意图，收敛由执行者完成。收敛通常在一秒内发生；一旦超过这个窗口仍然停在
	// cancelling，说明没有任何执行者会收口它，必须快速失败而不是等到通用超时。
	m6CancelSettleTimeout = 5 * time.Minute

	// m6ProjectIdleTimeout 是等待项目执行状态回到空闲的时限。
	//
	// 最后一个任务完成后项目仍会短暂持有执行权，直到收敛逻辑释放它；超时说明项目没有
	// 回到空闲，必须如实报错，而不是用更长的等待掩盖收敛问题。
	m6ProjectIdleTimeout = 3 * time.Minute
	// m6AnalysisJSON 是确认生成需求时使用的需求分析快照。
	m6AnalysisJSON = `{"summary":"真实环境项目级验收","goals":["验证顺序执行与删除"],` +
		`"current_state":["已有真实远程执行链路"],"scope":["测试"],"out_of_scope":["产品功能扩展"],` +
		`"constraints":["只用真实依赖"],"risks":["远程环境波动"],"acceptance_criteria":["可复核"]}`
)

// m6Fixture 是项目级真实验收的现场。
//
// 它在公共现场之上补齐项目、规划会话、需求确认与项目执行协调：所有服务都由生产装配
// 方式构造，执行入口共享同一个 TaskExecutionCoordinator。
type m6Fixture struct {
	*e2eFixture
	projects         *app.ProjectService
	planningSessions *app.PlanningSessionService
	requirements     *app.RequirementService
	tasks            *app.TaskService
	projectExecution *app.ProjectExecutionService
	scheduler        *app.TaskScheduler
	keeper           *app.ProjectExecutionKeeper
}

// newM6Fixture 装配项目级真实验收现场（不播种任务）。
func newM6Fixture(t *testing.T) *m6Fixture {
	t.Helper()
	base := newE2EBase(t)
	fixture := &m6Fixture{e2eFixture: base}
	if _, err := base.db.SeedRoles(base.ctx, app.RoleSeeds()); err != nil {
		t.Fatalf("补齐固定角色失败: %v", err)
	}
	fixture.applyRoleModels(t)
	fixture.assembleM6(t)
	return fixture
}

// applyRoleModels 把固定角色的模型改成真实测试模型。
//
// 内置角色默认使用占位模型标识（产品不代用户选择模型），真实调用必须经生产角色服务
// 显式写入，不绕过角色配置校验。
func (f *m6Fixture) applyRoleModels(t *testing.T) {
	t.Helper()
	roles, err := app.NewRoleService(f.db)
	if err != nil {
		t.Fatalf("构造角色服务失败: %v", err)
	}
	for _, roleID := range []domain.ID{
		domain.RoleIDPlanner,
		domain.RoleIDDeveloper,
	} {
		model := f.model
		if _, err := roles.Update(f.ctx, roleID.String(), app.RoleUpdate{Model: &model}); err != nil {
			t.Fatalf("更新角色 %s 的模型失败: %v", roleID, err)
		}
	}
	// 本组用例验收的是项目级调度与删除语义，不是语义审查本身：禁用可选的验证者角色，
	// 让"任务是否通过"只由真实执行的确定性验证命令决定，避免模型对同一份成果给出不同
	// 判断而让用例随机失败。语义审查链路由既有真实全链路用例（V1）覆盖。
	disabled := false
	if _, err := roles.Update(f.ctx, domain.RoleIDValidator.String(), app.RoleUpdate{
		Enabled: &disabled,
	}); err != nil {
		t.Fatalf("禁用验证者角色失败: %v", err)
	}
	t.Logf("ROLE_MODEL=%s VALIDATOR=disabled", f.model)
}

// assembleM6 在公共装配之上补齐项目、规划、需求与项目执行协调服务。
func (f *m6Fixture) assembleM6(t *testing.T) {
	t.Helper()
	projects, err := app.NewProjectService(app.ProjectServiceOptions{Store: f.db})
	if err != nil {
		t.Fatalf("构造项目服务失败: %v", err)
	}
	planningSessions, err := app.NewPlanningSessionService(app.PlanningSessionServiceOptions{Store: f.db})
	if err != nil {
		t.Fatalf("构造规划会话服务失败: %v", err)
	}
	requirements, err := app.NewRequirementService(app.RequirementServiceOptions{
		Store:        f.db,
		Coordination: f.coordination,
	})
	if err != nil {
		t.Fatalf("构造需求服务失败: %v", err)
	}
	tasks, err := app.NewTaskService(app.TaskServiceOptions{Store: f.db, Logger: f.logger.Slog()})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	projectExecution, err := app.NewProjectExecutionService(app.ProjectExecutionServiceOptions{
		Store:    f.db,
		Canceler: tasks,
		Logger:   f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造项目执行服务失败: %v", err)
	}
	keeper, err := app.NewProjectExecutionKeeper(app.ProjectExecutionKeeperOptions{
		Service: projectExecution,
		Logger:  f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造执行权续租服务失败: %v", err)
	}
	scheduler, err := app.NewTaskScheduler(app.TaskSchedulerOptions{
		Store:              f.db,
		Coordination:       f.coordination,
		Launch:             f.launch,
		MaxConcurrentTasks: 3,
		ProjectGate:        projectExecution,
		Logger:             f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造调度器失败: %v", err)
	}
	projectExecution.AttachScanTrigger(scheduler)
	startE2EServices(t, scheduler, keeper)
	f.projects = projects
	f.planningSessions = planningSessions
	f.requirements = requirements
	f.tasks = tasks
	f.projectExecution = projectExecution
	f.scheduler = scheduler
	f.keeper = keeper
}

// createProject 用生产项目服务登记一个带默认执行节点的项目。
func (f *m6Fixture) createProject(t *testing.T, projectID, name string) domain.Project {
	t.Helper()
	project, err := f.projects.Create(f.ctx, app.CreateProjectRequest{
		BaseBranch:         "main",
		DefaultHostID:      f.host.ID.String(),
		Environment:        "node",
		EnvironmentVersion: "v1",
		ID:                 projectID,
		Name:               name,
		RepositoryURL:      f.repo.ResolvedRepository(),
	})
	if err != nil {
		t.Fatalf("创建项目 %s 失败: %v", projectID, err)
	}
	t.Logf("M6_PROJECT=%s REPOSITORY=%s", project.ID, project.RepositoryURL)
	return project
}

// m6TaskSpec 是确认生成任务时给出的任务草案内容。
type m6TaskSpec struct {
	// title 是任务标题，会成为触发模型行为的 Goal 文本的前半部分。
	title string
	// description 是任务描述，说明本任务要产出的具体文件。
	description string
	// check 是确定性验证命令；为空时不追加命令，只保留方案中的验收条件。
	check *domain.VerificationCommand
}

// seedRequirement 用真实的规划会话与确认流程生成 1 个需求与 N 个任务。
//
// 需求与任务由生产 RequirementService.Confirm 在单个事务内创建：这里只提供结构化方案
// 内容（真实模型产出的方案由 planning 包的真实用例覆盖），不直接写需求或任务行。
func (f *m6Fixture) seedRequirement(
	t *testing.T,
	project domain.Project,
	specs []m6TaskSpec,
) app.ConfirmRequirementResult {
	t.Helper()
	session, err := f.planningSessions.Create(f.ctx, app.CreatePlanningSessionRequest{
		ProjectID: project.ID,
	})
	if err != nil {
		t.Fatalf("创建规划会话失败: %v", err)
	}
	if _, err := f.planningSessions.SubmitMessage(
		f.ctx,
		session.ID.String(),
		app.SubmitPlanningMessageRequest{Content: "真实环境验收：生成正式需求与任务"},
	); err != nil {
		t.Fatalf("提交规划消息失败: %v", err)
	}
	completed, err := f.planningSessions.CompleteAnalysis(
		f.ctx,
		session.ID.String(),
		app.CompletePlanningAnalysisRequest{
			RequirementAnalysisJSON: []byte(m6AnalysisJSON),
			ImplementationPlanJSON:  []byte(m6PlanJSON(specs)),
			Messages: []app.PlanningRoleMessage{
				{SenderType: domain.PlanningSenderRequirementAnalyst, Content: "需求分析师：结论"},
				{SenderType: domain.PlanningSenderPlanner, Content: "方案规划师：方案"},
			},
		},
	)
	if err != nil {
		t.Fatalf("完成规划分析失败: %v", err)
	}
	confirmed, err := f.requirements.Confirm(f.ctx, app.ConfirmRequirementRequest{
		PlanningSessionID: session.ID.String(),
		RevisionID:        completed.Revision.ID.String(),
		HostID:            f.host.ID.String(),
	})
	if err != nil {
		t.Fatalf("确认方案失败: %v", err)
	}
	if len(confirmed.Tasks) != len(specs) {
		t.Fatalf("确认生成的任务数量不符: 期望 %d，实际 %d", len(specs), len(confirmed.Tasks))
	}
	// 确认流程只为任务写入验收条件（方案中没有命令），这里把"可执行的确定性验证命令"
	// 补进任务配置快照：任务仍处于 queued，未产生任何执行记录。
	for index, spec := range specs {
		if spec.check == nil {
			continue
		}
		task := confirmed.Tasks[index]
		commands := []domain.VerificationCommand{*spec.check}
		task.Config.Verification.Commands = commands
		if err := f.db.UpdateTask(f.ctx, task); err != nil {
			t.Fatalf("写入任务验证命令失败: %v", err)
		}
		confirmed.Tasks[index] = task
	}
	t.Logf("M6_REQUIREMENT=%s PROJECT=%s TASKS=%d",
		confirmed.Requirement.ID, project.ID, len(confirmed.Tasks))
	return confirmed
}

// fileExistsCheck 返回一条"文件存在于仓库根目录"的确定性验证命令。
func fileExistsCheck(fileName string) *domain.VerificationCommand {
	return &domain.VerificationCommand{
		Name:     "file-exists",
		Command:  "sh",
		Args:     []string{"-c", "test -f " + fileName},
		Required: true,
	}
}

// m6PlanJSON 构造与 specs 一一对应的实施方案快照。
func m6PlanJSON(specs []m6TaskSpec) string {
	tasks := make([]string, 0, len(specs))
	for index, spec := range specs {
		dependencies := "[]"
		if index > 0 {
			dependencies = fmt.Sprintf(`["task-%d"]`, index)
		}
		tasks = append(tasks, fmt.Sprintf(
			`{"id":"task-%d","title":%q,"description":%q,"depends_on":%s,`+
				`"acceptance_criteria":["验证命令通过"]}`,
			index+1, spec.title, spec.description, dependencies,
		))
	}
	return `{"summary":"按顺序完成真实交付","tasks":[` + strings.Join(tasks, ",") + `]}`
}

// task 读取任务真实记录。
func (f *m6Fixture) task(t *testing.T, taskID domain.ID) domain.Task {
	t.Helper()
	task, err := f.db.GetTask(f.ctx, taskID)
	if err != nil {
		t.Fatalf("读取任务 %s 失败: %v", taskID, err)
	}
	return *task
}

// waitTaskSettled 等待任务进入终态或失败类状态。
//
// 判定只看持久化状态：不推进状态、不重放执行，也不把等待超时当作成功。
func (f *m6Fixture) waitTaskSettled(t *testing.T, taskID domain.ID) domain.Task {
	t.Helper()
	return f.waitTaskSettledWithin(t, taskID, m6SettleTimeout)
}

// waitTaskSettledWithin 在给定时限内等待任务收敛。
func (f *m6Fixture) waitTaskSettledWithin(
	t *testing.T,
	taskID domain.ID,
	timeout time.Duration,
) domain.Task {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		task := f.task(t, taskID)
		switch task.Status {
		case domain.TaskStatusCompleted,
			domain.TaskStatusFailed,
			domain.TaskStatusCancelled,
			domain.TaskStatusRecoveryRequired:
			return task
		}
		time.Sleep(m6PollInterval)
	}
	task := f.task(t, taskID)
	t.Fatalf("等待任务 %s 收敛超时，当前状态 %s", taskID, task.Status)
	return task
}

// waitTaskStatus 等待任务到达指定状态之一。
func (f *m6Fixture) waitTaskStatus(
	t *testing.T,
	taskID domain.ID,
	want ...domain.TaskStatus,
) domain.Task {
	t.Helper()
	deadline := time.Now().Add(m6SettleTimeout)
	for time.Now().Before(deadline) {
		task := f.task(t, taskID)
		for _, status := range want {
			if task.Status == status {
				return task
			}
		}
		time.Sleep(m6PollInterval)
	}
	task := f.task(t, taskID)
	t.Fatalf("等待任务 %s 到达 %v 超时，当前状态 %s", taskID, want, task.Status)
	return task
}

// projectExecutionSnapshot 读取项目执行状态投影。
func (f *m6Fixture) projectExecutionSnapshot(t *testing.T, projectID string) app.ProjectExecution {
	t.Helper()
	detail, err := f.projectExecution.Execution(f.ctx, projectID)
	if err != nil {
		t.Fatalf("查询项目 %s 执行状态失败: %v", projectID, err)
	}
	return detail
}

// concurrencySample 是一次采样的项目内与全局在途任务数。
type concurrencySample struct {
	byProject map[string]int
}

// watchConcurrency 在指定项目上周期采样在途任务数，返回的采样函数停止监视并给出峰值。
//
// 采样只读接口返回的投影，不推进状态；它记录每个项目的最大值，用于核验"同项目串行、
// 跨项目并行、全局不超限"。停止与取峰值合成一步：监视协程只在收到停止信号后才汇报
// 峰值，调用方若先等峰值再停止就会永久阻塞。
func (f *m6Fixture) watchConcurrency(
	t *testing.T,
	projectIDs []string,
) func() concurrencySample {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan concurrencySample, 1)
	peak := concurrencySample{byProject: map[string]int{}}
	go func() {
		for {
			for _, projectID := range projectIDs {
				detail, err := f.projectExecution.Execution(f.ctx, projectID)
				if err != nil {
					continue
				}
				running := detail.Progress.Counts.Running
				if running > peak.byProject[projectID] {
					peak.byProject[projectID] = running
				}
			}
			select {
			case <-stop:
				done <- peak
				return
			case <-time.After(m6PollInterval):
			}
		}
	}()
	var once sync.Once
	var result concurrencySample
	sample := func() concurrencySample {
		once.Do(func() {
			close(stop)
			result = <-done
		})
		return result
	}
	// 用例中途失败时也要停掉监视协程，避免它在剩余等待时间内继续查询数据库。
	t.Cleanup(func() {
		sample()
	})
	return sample
}

// TestRemoteM6RequirementSequentialTasks 在真实环境验收场景 A 的执行半段。
//
// 一个项目、一个需求、两个带确定性验证命令的任务：`Start` 之后第一个任务真实完成，
// 第二个任务被自动领取并真实完成。核验同一项目在途任务始终不超过 1 个（严格串行）、
// 需求进度为 completed、项目回到空闲，且两个任务都留下步骤、尝试、会话与代码保存记录。
func TestRemoteM6RequirementSequentialTasks(t *testing.T) {
	t.Parallel()
	f := newM6Fixture(t)
	f.prepareEnvironmentVersion(t)
	project := f.createProject(t, "m6-sequence-"+f.suffix, "M6 顺序执行项目")
	confirmed := f.seedRequirement(t, project, []m6TaskSpec{
		{
			title:       "第一步：创建说明文件",
			description: "在仓库根目录创建 M6-SEQ-1.md，内容为一行说明。",
			check:       fileExistsCheck("M6-SEQ-1.md"),
		},
		{
			title:       "第二步：创建收尾文件",
			description: "在仓库根目录创建 M6-SEQ-2.md，内容为一行说明。",
			check:       fileExistsCheck("M6-SEQ-2.md"),
		},
	})
	stopWatching := f.watchConcurrency(t, []string{project.ID})
	if _, err := f.projectExecution.Start(f.ctx, project.ID); err != nil {
		t.Fatalf("开始项目执行失败: %v", err)
	}

	first := f.waitTaskSettled(t, confirmed.Tasks[0].ID)
	if first.Status != domain.TaskStatusCompleted {
		t.Fatalf("第一个任务应真实完成，实际 %s（失败摘要 %q）",
			first.Status, first.FailureSummary)
	}
	second := f.waitTaskSettled(t, confirmed.Tasks[1].ID)
	if second.Status != domain.TaskStatusCompleted {
		t.Fatalf("第二个任务应被自动领取并真实完成，实际 %s（失败摘要 %q）",
			second.Status, second.FailureSummary)
	}
	// 顺序证据：第二个任务只在第一个任务结束之后才开始。
	if first.FinishedAt == nil || second.StartedAt == nil {
		t.Fatalf("两个任务都必须有真实结束与开始时间: finished=%v started=%v",
			first.FinishedAt, second.StartedAt)
	}
	if second.StartedAt.Before(*first.FinishedAt) {
		t.Fatalf("第二个任务在第一个任务结束前就已开始：started=%s finished=%s",
			second.StartedAt.Format(time.RFC3339), first.FinishedAt.Format(time.RFC3339))
	}
	peak := stopWatching()
	if peak.byProject[project.ID] == 0 {
		t.Fatalf("未观测到项目内执行中的任务，串行证据不成立")
	}
	if peak.byProject[project.ID] > 1 {
		t.Fatalf("项目 %s 同时有 %d 个任务在途，违反同项目串行",
			project.ID, peak.byProject[project.ID])
	}

	progress, err := f.requirements.Progress(f.ctx, confirmed.Requirement.ID.String())
	if err != nil {
		t.Fatalf("查询需求进度失败: %v", err)
	}
	if progress.Status != domain.RequirementProgressStatusCompleted {
		t.Fatalf("需求进度应为 completed，实际 %s（计数 %+v，阻塞 %s）",
			progress.Status, progress.Counts, progress.BlockedReason)
	}
	if progress.Current != len(confirmed.Tasks) ||
		progress.Counts.Completed != len(confirmed.Tasks) {
		t.Fatalf("需求进度应记录 %d 个已完成任务，实际 current=%d counts=%+v",
			len(confirmed.Tasks), progress.Current, progress.Counts)
	}
	if progress.Blocked {
		t.Fatalf("全部任务完成后需求不应显示阻塞：%s", progress.BlockedReason)
	}
	execution := f.waitProjectIdle(t, project.ID, len(confirmed.Tasks))
	if execution.Progress.Blocked {
		t.Fatalf("全部任务完成后项目不应显示阻塞：%s", execution.Progress.BlockedReason)
	}
	if execution.Progress.Counts.Running != 0 {
		t.Fatalf("项目空闲时不应有在途任务，实际 %d", execution.Progress.Counts.Running)
	}
	for _, task := range confirmed.Tasks {
		assertM6TaskExecutionHistory(t, f, task.ID)
	}
	t.Logf("M6_A2_OK project=%s requirement=%s peak=%d requirement_status=%s project_status=%s",
		project.ID, confirmed.Requirement.ID, peak.byProject[project.ID],
		progress.Status, execution.Progress.Status)
}

// waitProjectIdle 等待项目执行状态为空闲且完成计数达到期望值。
func (f *m6Fixture) waitProjectIdle(
	t *testing.T,
	projectID string,
	wantCompleted int,
) app.ProjectExecution {
	t.Helper()
	deadline := time.Now().Add(m6ProjectIdleTimeout)
	var latest app.ProjectExecution
	for time.Now().Before(deadline) {
		latest = f.projectExecutionSnapshot(t, projectID)
		if latest.Progress.Status == domain.ProjectExecutionStatusIdle &&
			latest.Progress.Counts.Completed == wantCompleted {
			return latest
		}
		time.Sleep(m6PollInterval)
	}
	t.Fatalf("等待项目 %s 回到空闲超时：状态 %s，计数 %+v",
		projectID, latest.Progress.Status, latest.Progress.Counts)
	return latest
}

// assertM6TaskExecutionHistory 核验任务留下了步骤、尝试、会话与代码保存记录。
//
// 真实完成的链路必须留下可复核的执行历史，且这些记录不因任何后续操作被清理。
func assertM6TaskExecutionHistory(t *testing.T, f *m6Fixture, taskID domain.ID) {
	t.Helper()
	steps, err := f.db.ListStepsByTask(f.ctx, taskID)
	if err != nil {
		t.Fatalf("读取任务 %s 的步骤失败: %v", taskID, err)
	}
	if len(steps) == 0 {
		t.Fatalf("任务 %s 应留下步骤记录", taskID)
	}
	attemptsTotal := 0
	for _, step := range steps {
		attempts, attemptErr := f.db.ListAttemptsByStep(f.ctx, step.ID)
		if attemptErr != nil {
			t.Fatalf("读取步骤 %s 的执行尝试失败: %v", step.ID, attemptErr)
		}
		attemptsTotal += len(attempts)
	}
	if attemptsTotal == 0 {
		t.Fatalf("任务 %s 应留下执行尝试记录", taskID)
	}
	sessions, err := f.db.ListSessionsByTask(f.ctx, taskID)
	if err != nil {
		t.Fatalf("读取任务 %s 的会话失败: %v", taskID, err)
	}
	if len(sessions) == 0 {
		t.Fatalf("任务 %s 应留下角色会话记录", taskID)
	}
	records, err := f.db.ListCodeSaveRecordsByTask(f.ctx, taskID.String())
	if err != nil {
		t.Fatalf("读取任务 %s 的代码保存记录失败: %v", taskID, err)
	}
	if len(records) == 0 {
		t.Fatalf("任务 %s 真实完成时必须留下代码保存记录", taskID)
	}
	last := records[len(records)-1]
	if last.CommitSHA == "" {
		t.Fatalf("任务 %s 的代码保存记录必须带有真实 commit SHA", taskID)
	}
	if last.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("任务 %s 的代码保存必须推送成功，实际 %s（%s）",
			taskID, last.PushStatus, last.ErrorSummary)
	}
	t.Logf("M6_TASK_HISTORY task=%s steps=%d attempts=%d sessions=%d code_saves=%d commit=%s",
		taskID, len(steps), attemptsTotal, len(sessions), len(records), last.CommitSHA)
}

// TestRemoteM6ProjectSerialParallelAndFailureBlocking 在真实环境验收场景 C 与 D。
//
// 项目 A 有两个顺序任务，其中 A1 的确定性验证必然失败；项目 B 有一个正常任务。
// 两个项目同时开始执行，核验：
//   - 同一项目最多一个任务在途（严格串行），两个项目可以并行；
//   - 全局在途任务数不超过配置上限；
//   - A1 真实失败后 A2 不会自动开始，项目 A 显示阻塞且原因为 task_recovery_required；
//   - 项目 B 正常完成，不受项目 A 阻塞影响。
func TestRemoteM6ProjectSerialParallelAndFailureBlocking(t *testing.T) {
	t.Parallel()
	f := newM6Fixture(t)
	f.prepareEnvironmentVersion(t)

	projectA := f.createProject(t, "m6-fail-"+f.suffix, "M6 失败阻塞项目")
	projectB := f.createProject(t, "m6-parallel-"+f.suffix, "M6 并行项目")
	failing := &domain.VerificationCommand{
		Name:     "always-fails",
		Command:  "sh",
		Args:     []string{"-c", "exit 1"},
		Required: true,
	}
	requirementA := f.seedRequirement(t, projectA, []m6TaskSpec{
		{
			title:       "第一步：创建说明文件",
			description: "在仓库根目录创建 M6-A1.md，内容为一行说明。",
			check:       failing,
		},
		{
			title:       "第二步：创建收尾文件",
			description: "在仓库根目录创建 M6-A2.md，内容为一行说明。",
			check:       fileExistsCheck("M6-A2.md"),
		},
	})
	requirementB := f.seedRequirement(t, projectB, []m6TaskSpec{
		{
			title:       "独立任务：创建说明文件",
			description: "在仓库根目录创建 M6-B1.md，内容为一行说明。",
			check:       fileExistsCheck("M6-B1.md"),
		},
	})

	stopWatching := f.watchConcurrency(t, []string{projectA.ID, projectB.ID})
	if _, err := f.projectExecution.Start(f.ctx, projectA.ID); err != nil {
	}
	if _, err := f.projectExecution.Start(f.ctx, projectB.ID); err != nil {
		t.Fatalf("开始项目 B 执行失败: %v", err)
	}

	// A1 的确定性验证必然失败：执行记录以 blocked 结论停止，任务收敛为待人工处置。
	taskA1 := f.waitTaskSettled(t, requirementA.Tasks[0].ID)
	if taskA1.Status != domain.TaskStatusRecoveryRequired {
		t.Fatalf("A1 的验证命令必然失败，任务应收敛为 recovery_required，实际 %s", taskA1.Status)
	}
	// blocked 执行记录按设计保留（可被显式恢复），因此它仍属于"未结束记录"；这里必须
	// 核验的是它已经停止自动推进，而不是它不存在。
	runsA1, err := f.db.ListWorkflowRunsByTask(f.ctx, requirementA.Tasks[0].ID)
	if err != nil {
		t.Fatalf("读取工作流执行记录失败: %v", err)
	}
	if len(runsA1) != 1 || runsA1[0].Status != domain.WorkflowRunBlocked {
		t.Fatalf("失败任务应留下且只留下一条 blocked 执行记录，实际 %+v", runsA1)
	}
	if strings.TrimSpace(runsA1[0].ErrorSummary) == "" {
		t.Fatalf("blocked 执行记录必须给出可读的停止原因")
	}
	if runsA1[0].ErrorClass == "" {
		t.Fatalf("blocked 执行记录必须给出可诊断的错误分类")
	}
	taskB1 := f.waitTaskSettled(t, requirementB.Tasks[0].ID)
	if taskB1.Status != domain.TaskStatusCompleted {
		t.Fatalf("项目 B 的任务应真实完成，实际 %s", taskB1.Status)
	}
	peak := stopWatching()

	// A2 不得越过失败任务自动启动。
	taskA2 := f.task(t, requirementA.Tasks[1].ID)
	if taskA2.Status != domain.TaskStatusQueued {
		t.Fatalf("A1 失败后 A2 不应自动开始，实际状态 %s", taskA2.Status)
	}
	executionA := f.projectExecutionSnapshot(t, projectA.ID)
	if !executionA.Progress.Blocked {
		t.Fatalf("项目 A 应显示阻塞，实际状态 %s", executionA.Progress.Status)
	}
	if executionA.Progress.BlockedReason != domain.ProjectBlockReasonRecoveryRequired {
		t.Fatalf("项目 A 的阻塞原因应为 task_recovery_required，实际 %s",
			executionA.Progress.BlockedReason)
	}
	executionB := f.projectExecutionSnapshot(t, projectB.ID)
	if executionB.Progress.Counts.Completed != 1 {
		t.Fatalf("项目 B 应有一个已完成任务，实际计数 %+v", executionB.Progress.Counts)
	}

	// 同项目严格串行：任一项目在途任务数不超过 1；全局不超过配置上限。
	for projectID, value := range peak.byProject {
		if value > 1 {
			t.Fatalf("项目 %s 同时有 %d 个任务在途，违反同项目串行", projectID, value)
		}
	}
	globalPeak := peak.byProject[projectA.ID] + peak.byProject[projectB.ID]
	if globalPeak > 3 {
		t.Fatalf("全局在途任务数 %d 超过配置上限 3", globalPeak)
	}
	if peak.byProject[projectA.ID] == 0 || peak.byProject[projectB.ID] == 0 {
		t.Fatalf("未观测到两个项目各自执行过任务，并行性未取得证据: %+v", peak.byProject)
	}

	// 需求进度真实反映阻塞，而不是"完成"。
	progressA, err := f.requirements.Progress(f.ctx, requirementA.Requirement.ID.String())
	if err != nil {
		t.Fatalf("查询需求 A 进度失败: %v", err)
	}
	if progressA.Status != domain.RequirementProgressStatusBlocked {
		t.Fatalf("需求 A 应显示阻塞，实际 %s", progressA.Status)
	}
	progressB, err := f.requirements.Progress(f.ctx, requirementB.Requirement.ID.String())
	if err != nil {
		t.Fatalf("查询需求 B 进度失败: %v", err)
	}
	if progressB.Status != domain.RequirementProgressStatusCompleted {
		t.Fatalf("需求 B 应显示完成，实际 %s", progressB.Status)
	}
	t.Logf(
		"M6_C_D_OK a1=%s a1_run=%s/%s a2=%s b1=%s a_blocked=%s b_status=%s peak_a=%d peak_b=%d",
		taskA1.Status, runsA1[0].Status, runsA1[0].ErrorClass,
		taskA2.Status, taskB1.Status,
		executionA.Progress.BlockedReason, executionB.Progress.Status,
		peak.byProject[projectA.ID], peak.byProject[projectB.ID],
	)
}

// TestRemoteM6DeleteRequirementWhileRunning 在真实环境验收场景 E。
//
// 流程与产品语义一致：先显式停止任务并等待真实收敛，再显式删除需求。
// 核验：未收敛时删除返回冲突且没有任何部分软删除；收敛后删除成功；任务状态与最终结论
// 不被改写；执行历史仍可查询；项目调度不再领取该需求的任务；容器不被自动删除。
func TestRemoteM6DeleteRequirementWhileRunning(t *testing.T) {
	t.Parallel()
	f := newM6Fixture(t)
	f.prepareEnvironmentVersion(t)
	project := f.createProject(t, "m6-delete-"+f.suffix, "M6 删除验收项目")
	confirmed := f.seedRequirement(t, project, []m6TaskSpec{
		{
			title:       "运行中的任务",
			description: "在仓库根目录创建 M6-DELETE.md，内容为一行说明。",
		},
		{
			title:       "尚未启动的任务",
			description: "在仓库根目录创建 M6-DELETE-NEXT.md，内容为一行说明。",
		},
	})
	requirementID := confirmed.Requirement.ID
	runningTaskID := confirmed.Tasks[0].ID

	if _, err := f.projectExecution.Start(f.ctx, project.ID); err != nil {
		t.Fatalf("开始项目执行失败: %v", err)
	}
	// 等待任务真正进入执行阶段：容器与执行记录都已存在。
	running := f.waitTaskStatus(
		t,
		runningTaskID,
		domain.TaskStatusPreparing,
		domain.TaskStatusPlanning,
		domain.TaskStatusPlanned,
		domain.TaskStatusDeveloping,
		domain.TaskStatusVerifying,
	)
	containers, err := f.db.ListContainers(f.ctx, domain.ContainerFilter{
		HostID: f.host.ID,
	})
	if err != nil {
		t.Fatalf("读取容器记录失败: %v", err)
	}
	if len(containers) == 0 {
		t.Fatalf("运行中的任务应已创建容器记录")
	}
	t.Logf("M6_RUNNING_TASK=%s status=%s containers=%d",
		runningTaskID, running.Status, len(containers))

	// 未收敛：删除必须返回冲突，且没有任何部分软删除。
	if _, err := f.requirements.Delete(f.ctx, requirementID.String()); !errors.Is(
		err,
		app.ErrRequirementStateConflict,
	) {
		t.Fatalf("运行中的需求应拒绝删除，实际: %v", err)
	}
	assertM6NothingDeleted(t, f, requirementID, confirmed.Tasks)

	// 显式停止：只记录取消请求并推进到 cancelling，真实停止由执行者确认。
	if _, err := f.tasks.CancelTaskForAPI(f.ctx, runningTaskID.String()); err != nil {
		t.Fatalf("请求取消任务失败: %v", err)
	}
	cancelling := f.task(t, runningTaskID)
	if cancelling.Status != domain.TaskStatusCancelling && !cancelling.CancelRequested {
		t.Fatalf("取消请求应被记录，实际状态 %s", cancelling.Status)
	}
	// 停止中同样不能删除。
	if _, err := f.requirements.Delete(f.ctx, requirementID.String()); !errors.Is(
		err,
		app.ErrRequirementStateConflict,
	) {
		t.Fatalf("停止中的需求应拒绝删除，实际: %v", err)
	}

	// 停止后等待真实收敛：收敛必须在给定时限内发生，超时说明没有执行者收口。
	settled := f.waitTaskSettledWithin(t, runningTaskID, m6CancelSettleTimeout)
	if settled.Status != domain.TaskStatusCancelled {
		t.Fatalf("取消的任务应真实收敛为 cancelled，实际 %s（失败摘要 %q）",
			settled.Status, settled.FailureSummary)
	}

	// 收敛后删除成功，并返回真实任务数量。
	deleted, err := f.requirements.Delete(f.ctx, requirementID.String())
	if err != nil {
		t.Fatalf("收敛后删除需求失败: %v", err)
	}
	if deleted.DeletedTaskCount != len(confirmed.Tasks) {
		t.Fatalf("删除结果的任务数量不符: 期望 %d，实际 %d",
			len(confirmed.Tasks), deleted.DeletedTaskCount)
	}
	persistedRequirement, err := f.db.GetRequirement(f.ctx, requirementID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if persistedRequirement.DeletedAt == nil {
		t.Fatalf("需求应写入删除时间")
	}
	// 任务状态与最终结论不被改写。
	deletedRunning := f.task(t, runningTaskID)
	if deletedRunning.Status != domain.TaskStatusCancelled {
		t.Fatalf("软删除不得改写任务状态: %s", deletedRunning.Status)
	}
	if deletedRunning.DeletedAt == nil {
		t.Fatalf("运行过的任务应写入删除时间")
	}
	deletedNext := f.task(t, confirmed.Tasks[1].ID)
	if deletedNext.Status != domain.TaskStatusQueued {
		t.Fatalf("未启动任务的状态不得被改写: %s", deletedNext.Status)
	}
	if deletedNext.DeletedAt == nil {
		t.Fatalf("未启动任务也应随需求软删除")
	}

	// 执行历史仍可按任务读取。
	//
	// 取消发生在计划阶段时计划没有落库，因此这里不假设一定有步骤或尝试：能证明"历史
	// 保留"的是任务自身的事件时间线、角色会话与容器记录，它们与任务状态无关。
	steps, err := f.db.ListStepsByTask(f.ctx, runningTaskID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	attempts := make([]domain.Attempt, 0, len(steps))
	for _, step := range steps {
		stepAttempts, attemptErr := f.db.ListAttemptsByStep(f.ctx, step.ID)
		if attemptErr != nil {
			t.Fatalf("读取执行尝试失败: %v", attemptErr)
		}
		attempts = append(attempts, stepAttempts...)
	}
	sessions, err := f.db.ListSessionsByTask(f.ctx, runningTaskID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatalf("任务应保留角色会话记录")
	}
	events, err := f.db.ListEventsByTask(f.ctx, runningTaskID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("任务事件时间线应保留")
	}
	hasDeleteEvent := false
	for _, event := range events {
		if event.Type == domain.EventTypeRequirementDeleted {
			hasDeleteEvent = true
		}
	}
	if !hasDeleteEvent {
		t.Fatalf("任务时间线应包含 requirement.deleted 事件")
	}

	// 项目调度不再领取该需求的任务。
	facts, err := f.db.ListProjectTaskFacts(f.ctx, domain.ID(project.ID))
	if err != nil {
		t.Fatalf("查询项目任务事实失败: %v", err)
	}
	if len(facts) != 0 {
		t.Fatalf("删除后项目不应再看到该需求的任务: %d", len(facts))
	}
	if _, err := f.projectExecution.Start(f.ctx, project.ID); !errors.Is(
		err,
		app.ErrProjectExecutionNoTasks,
	) {
		t.Fatalf("删除后开始项目执行应因没有任务而失败，实际: %v", err)
	}

	// 容器不被自动删除：登记记录仍在，真实容器仍存在于远端。
	afterContainers, err := f.db.ListContainers(f.ctx, domain.ContainerFilter{HostID: f.host.ID})
	if err != nil {
		t.Fatalf("读取容器记录失败: %v", err)
	}
	if len(afterContainers) != len(containers) {
		t.Fatalf("删除需求不得删除容器记录: %d → %d", len(containers), len(afterContainers))
	}
	for _, record := range afterContainers {
		if strings.TrimSpace(record.DockerID) == "" {
			continue
		}
		inspect, inspectErr := f.runner.InspectContainer(f.ctx, record.DockerID)
		if inspectErr != nil {
			t.Fatalf("容器 %s 应仍存在于远端: %v", record.DockerID, inspectErr)
		}
		t.Logf("M6_CONTAINER_RETAINED docker_id=%s condition=%s",
			record.DockerID, inspect.RuntimeCondition())
	}

	// 代码保存记录不存在：取消发生在保存之前，因此没有任何 Git 副作用被重放。
	codeSaves, err := f.db.ListCodeSaveRecordsByTask(f.ctx, runningTaskID.String())
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	t.Logf("M6_E_OK requirement=%s running=%s next=%s steps=%d attempts=%d sessions=%d events=%d code_saves=%d",
		requirementID, deletedRunning.Status, deletedNext.Status,
		len(steps), len(attempts), len(sessions), len(events), len(codeSaves))
}

// assertM6NothingDeleted 断言需求与全部任务都没有被部分软删除。
func assertM6NothingDeleted(
	t *testing.T,
	f *m6Fixture,
	requirementID domain.ID,
	tasks []domain.Task,
) {
	t.Helper()
	requirement, err := f.db.GetRequirement(f.ctx, requirementID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if requirement.DeletedAt != nil {
		t.Fatalf("被拒绝时需求不得被软删除")
	}
	for _, task := range tasks {
		persisted := f.task(t, task.ID)
		if persisted.DeletedAt != nil {
			t.Fatalf("被拒绝时任务 %s 不得被软删除", task.ID)
		}
	}
	visible, err := f.db.ListTasks(f.ctx, storage.TaskFilter{RequirementID: requirementID})
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if len(visible) != len(tasks) {
		t.Fatalf("被拒绝时全部任务都应可见: %d/%d", len(visible), len(tasks))
	}
}
