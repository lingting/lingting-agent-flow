// 尝试级代码获取残留收敛与只读核验的集成测试。
//
// 用例使用真实 SQLite、正式迁移与真实的 app.CodeFetchRecoveryService /
// app.CodeFetchVerifyService：断言对象是数据库中的真实 code_fetches 记录。
// 容器内命令由把"容器文件系统"映射到测试目录的受控执行器提供，因此本文件验证
// "收敛规则 + 持久化路径 + 只读探测"，不启动真实容器。
package fetcher_local

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/source"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// codeFetchRecoveryContainerID 是残留收敛用例使用的容器登记记录标识。
const codeFetchRecoveryContainerID = "container-code-fetch-recovery"

// codeFetchRecoveryFixture 是一套尝试级代码获取残留收敛的持久化现场。
type codeFetchRecoveryFixture struct {
	ctx    context.Context
	db     *sqlite.DB
	logger *logging.Logger
	probe  *codeFetchProbe
	task   domain.Task
	step   domain.Step
	now    time.Time
	// eligibleContainer 是满足代码初始化前置条件的容器记录。
	eligibleContainer domain.Container
}

// codeFetchProbe 是只读探测的受控替身。
type codeFetchProbe struct {
	container domain.Container
	// replies 按命令首参数返回受控结果。
	replies map[string]docker.CommandResult
	// execErr 让命令执行本身失败。
	execErr error
	// commands 记录收到的命令，用于断言"只读"。
	commands [][]string
}

// GetContainerRecord 返回受控容器记录。
func (p *codeFetchProbe) GetContainerRecord(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	return p.container, nil
}

// InspectContainer 返回运行中的容器。
func (p *codeFetchProbe) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	return app.ContainerDetail{
		Container: p.container,
		Inspect:   &docker.ContainerInspect{Running: true, State: domain.DockerStateRunning},
	}, nil
}

// Exec 返回受控的命令结果。
func (p *codeFetchProbe) Exec(
	ctx context.Context,
	request app.ExecRequest,
) (app.ExecResult, error) {
	p.commands = append(p.commands, append([]string(nil), request.Command...))
	if p.execErr != nil {
		return app.ExecResult{}, p.execErr
	}
	key := strings.Join(request.Command, " ")
	if result, ok := p.replies[key]; ok {
		return app.ExecResult{Result: result}, nil
	}
	return app.ExecResult{Result: docker.CommandResult{
		ExitCode: 1,
		Status:   "exit_error",
		Certain:  true,
	}}, nil
}

// newCodeFetchRecoveryFixture 打开真实数据库并创建任务、步骤与尝试归属。
func newCodeFetchRecoveryFixture(t *testing.T) *codeFetchRecoveryFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	now := time.Now().UTC()
	host := newNormalHost(t, db, ctx, now)
	developer := newWorkspaceRole(t, db, ctx, domain.RoleTypeDeveloper, "开发工程师", now)
	requirement, err := domain.NewRequirement("代码获取需求", "正文", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "https://git.example.com/group/repo.git", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &host.ID
	task.ContainerID = codeFetchRecoveryContainerID
	task.Config = domain.TaskConfig{
		Goal:            "完成本次需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		DeveloperRoleID: developer.ID,
		PlannerRoleID:   developer.ID,
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	step, err := domain.NewAgentStep(
		task.ID,
		1,
		domain.StepTypeDevelop,
		"实现步骤",
		"按需求实现",
		"",
		developer,
		domain.VerificationSpec{
			Commands: []domain.VerificationCommand{
				{Name: "build", Command: "go", Args: []string{"build", "./..."}, Required: true},
			},
		},
		domain.RetryConfig{MaxAttempts: 1},
		now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := db.CreateSteps(ctx, []domain.Step{step}); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	container := workspaceContainer()
	container.ID = domain.ID(codeFetchRecoveryContainerID)
	return &codeFetchRecoveryFixture{
		ctx:    ctx,
		db:     db,
		logger: logger,
		probe:  &codeFetchProbe{container: container, replies: make(map[string]docker.CommandResult)},
		task:   task,
		step:   step,
		now:    now,
	}
}

// createRecord 写入一条指定状态的代码获取记录。
func (f *codeFetchRecoveryFixture) createRecord(
	t *testing.T,
	attemptID string,
	status domain.CodeFetchStatus,
) domain.CodeFetchRecord {
	t.Helper()
	record, err := domain.NewCodeFetchRecord(
		domain.NewID(),
		f.task.Config.ProjectID,
		f.task.ID.String(),
		f.step.ID.String(),
		attemptID,
		domain.CodeSourceGit,
		domain.DefaultContainerWorkDir,
		codeFetchRecoveryContainerID,
		testDockerID,
		f.now,
	)
	if err != nil {
		t.Fatalf("构造代码获取记录失败: %v", err)
	}
	if err := f.db.CreateCodeFetchRecord(f.ctx, record); err != nil {
		t.Fatalf("写入代码获取记录失败: %v", err)
	}
	switch status {
	case domain.CodeFetchStatusPending:
		return record
	case domain.CodeFetchStatusRunning:
		if err := record.MarkRunning(domain.CodeFetchStageClone, f.now); err != nil {
			t.Fatalf("标记运行失败: %v", err)
		}
		if err := f.db.UpdateCodeFetchRecord(f.ctx, record); err != nil {
			t.Fatalf("更新代码获取记录失败: %v", err)
		}
		return record
	case domain.CodeFetchStatusUncertain:
		if err := record.MarkFailed(
			domain.CodeFetchStatusUncertain,
			domain.CodeFetchStagePersist,
			"interrupted",
			"不确定",
			f.now,
		); err != nil {
			t.Fatalf("标记不确定失败: %v", err)
		}
		if err := f.db.UpdateCodeFetchRecord(f.ctx, record); err != nil {
			t.Fatalf("更新代码获取记录失败: %v", err)
		}
		return record
	default:
		t.Fatalf("未支持的初始状态: %s", status)
		return domain.CodeFetchRecord{}
	}
}

// recoveryService 构造残留收敛服务。
func (f *codeFetchRecoveryFixture) recoveryService(
	t *testing.T,
	store app.CodeFetchRecoveryStore,
) *app.CodeFetchRecoveryService {
	t.Helper()
	service, err := app.NewCodeFetchRecoveryService(app.CodeFetchRecoveryOptions{
		Store:  store,
		Logger: f.logger.Slog(),
		Now:    func() time.Time { return f.now },
	})
	if err != nil {
		t.Fatalf("构造残留收敛服务失败: %v", err)
	}
	return service
}

// reload 读取数据库中的记录。
func (f *codeFetchRecoveryFixture) reload(t *testing.T, id domain.ID) domain.CodeFetchRecord {
	t.Helper()
	record, err := f.db.GetCodeFetchRecord(f.ctx, id)
	if err != nil {
		t.Fatalf("读取代码获取记录失败: %v", err)
	}
	return *record
}

// TestCodeFetchRecoveryConvergesPendingToUncertain 验证 pending 被收敛为不确定。
func TestCodeFetchRecoveryConvergesPendingToUncertain(t *testing.T) {
	t.Parallel()
	fixture := newCodeFetchRecoveryFixture(t)
	record := fixture.createRecord(t, "attempt-pending", domain.CodeFetchStatusPending)

	service := fixture.recoveryService(t, fixture.db)
	if err := service.Start(fixture.ctx); err != nil {
		t.Fatalf("启动残留收敛失败: %v", err)
	}
	stored := fixture.reload(t, record.ID)
	if stored.Status != domain.CodeFetchStatusUncertain {
		t.Fatalf("pending 应收敛为 uncertain，实际 %s", stored.Status)
	}
	if !strings.Contains(stored.ErrorSummary, "进程已退出") {
		t.Fatalf("收敛摘要必须说明来源: %q", stored.ErrorSummary)
	}
	// 已有记录不得被重放为成功：commit_sha 必须仍然为空。
	if stored.CommitSHA != "" {
		t.Fatalf("收敛不得臆造 commit，实际 %q", stored.CommitSHA)
	}
}

// TestCodeFetchRecoveryConvergesRunningToUncertain 验证 running 同样被收敛。
func TestCodeFetchRecoveryConvergesRunningToUncertain(t *testing.T) {
	t.Parallel()
	fixture := newCodeFetchRecoveryFixture(t)
	record := fixture.createRecord(t, "attempt-running", domain.CodeFetchStatusRunning)
	service := fixture.recoveryService(t, fixture.db)
	if err := service.Start(fixture.ctx); err != nil {
		t.Fatalf("启动残留收敛失败: %v", err)
	}
	stored := fixture.reload(t, record.ID)
	if stored.Status != domain.CodeFetchStatusUncertain {
		t.Fatalf("running 应收敛为 uncertain，实际 %s", stored.Status)
	}
}

// TestCodeFetchRecoveryKeepsRecordWithLiveLease 验证有执行者时不抢占。
func TestCodeFetchRecoveryKeepsRecordWithLiveLease(t *testing.T) {
	t.Parallel()
	fixture := newCodeFetchRecoveryFixture(t)
	record := fixture.createRecord(t, "attempt-live", domain.CodeFetchStatusRunning)
	// 为任务登记一条仍持有有效执行权的执行记录。
	run, err := domain.NewWorkflowRun(fixture.task.ID, []domain.Step{fixture.step}, fixture.now)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := run.Start("worker", time.Hour, fixture.now); err != nil {
		t.Fatalf("登记执行权失败: %v", err)
	}
	if err := fixture.db.CreateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}

	service := fixture.recoveryService(t, fixture.db)
	if err := service.Start(fixture.ctx); err != nil {
		t.Fatalf("启动残留收敛失败: %v", err)
	}
	stored := fixture.reload(t, record.ID)
	if stored.Status != domain.CodeFetchStatusRunning {
		t.Fatalf("仍持有执行权时不得收敛，实际 %s", stored.Status)
	}
}

// TestCodeFetchRecoveryIsIdempotentAcrossRestart 验证重复收敛幂等。
func TestCodeFetchRecoveryIsIdempotentAcrossRestart(t *testing.T) {
	t.Parallel()
	fixture := newCodeFetchRecoveryFixture(t)
	record := fixture.createRecord(t, "attempt-idempotent", domain.CodeFetchStatusPending)
	for attempt := 0; attempt < 2; attempt++ {
		service := fixture.recoveryService(t, fixture.db)
		if err := service.Start(fixture.ctx); err != nil {
			t.Fatalf("第 %d 次收敛失败: %v", attempt+1, err)
		}
	}
	stored := fixture.reload(t, record.ID)
	if stored.Status != domain.CodeFetchStatusUncertain {
		t.Fatalf("重复收敛后状态应保持 uncertain，实际 %s", stored.Status)
	}
	events, err := fixture.db.ListEventsByTask(fixture.ctx, fixture.task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	count := 0
	for _, event := range events {
		if strings.Contains(string(event.Payload), "recover_code_fetch") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("重复收敛不应重复写事件，实际 %d 条", count)
	}
}

// TestCodeFetchRecoveryFailureBlocksStartup 验证写入失败阻止启动。
func TestCodeFetchRecoveryFailureBlocksStartup(t *testing.T) {
	t.Parallel()
	fixture := newCodeFetchRecoveryFixture(t)
	record := fixture.createRecord(t, "attempt-writefail", domain.CodeFetchStatusPending)
	service := fixture.recoveryService(t, &failingCodeFetchStore{DB: fixture.db})
	if err := service.Start(fixture.ctx); err == nil {
		t.Fatal("收敛写入失败时必须让启动失败")
	}
	stored := fixture.reload(t, record.ID)
	if stored.Status != domain.CodeFetchStatusPending {
		t.Fatalf("写入失败时记录应保持原状，实际 %s", stored.Status)
	}
}

// TestCodeFetchRecoveryConcurrentInstancesKeepNewerRecord 验证并发不覆盖新结论。
func TestCodeFetchRecoveryConcurrentInstancesKeepNewerRecord(t *testing.T) {
	t.Parallel()
	fixture := newCodeFetchRecoveryFixture(t)
	record := fixture.createRecord(t, "attempt-concurrent", domain.CodeFetchStatusRunning)
	// 列出记录之后让另一个执行者把记录推进为 succeeded。
	service := fixture.recoveryService(t, &newerCodeFetchStore{
		DB: fixture.db,
		advance: func() {
			newer := record
			if err := newer.MarkSucceeded(
				"main",
				domain.GitBranchSelectionExplicit,
				strings.Repeat("1", 40),
				domain.TaskBranchSourceNew.String(),
				strings.Repeat("2", 40),
				fixture.now,
			); err == nil {
				_ = fixture.db.UpdateCodeFetchRecord(context.Background(), newer)
			}
		},
	})
	if err := service.Start(fixture.ctx); err != nil {
		t.Fatalf("并发收敛冲突不应让启动失败: %v", err)
	}
	stored := fixture.reload(t, record.ID)
	if stored.Status != domain.CodeFetchStatusSucceeded {
		t.Fatalf("并发写入的成功结论必须被保留，实际 %s", stored.Status)
	}
}

// TestCodeFetchInitRejectedWhileRecordUncertain 验证不确定现场阻止重新初始化。
func TestCodeFetchInitRejectedWhileRecordUncertain(t *testing.T) {
	t.Parallel()
	fixture := newCodeFetchRecoveryFixture(t)
	attempt, err := fixture.db.StartAttempt(fixture.ctx, storage.StartAttemptRequest{
		TaskID: fixture.task.ID,
		StepID: fixture.step.ID,
		At:     fixture.now,
	})
	if err != nil {
		t.Fatalf("创建执行尝试失败: %v", err)
	}
	record := fixture.createRecord(t, attempt.ID.String(), domain.CodeFetchStatusUncertain)
	// 让容器与任务级工作区都满足前置条件：本用例要验证的正是"不确定现场"这一条
	// 拒绝原因，其他前置条件必须已成立。
	fixture.prepareEligibleContainer(t, attempt.ID)

	initializer, err := app.NewCodeInitializeService(app.CodeInitializeOptions{
		Store:      fixture.db,
		Containers: &rejectingCodeInitContainers{container: fixture.eligibleContainer},
		Logger:     fixture.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造代码初始化服务失败: %v", err)
	}
	_, initErr := initializer.Initialize(fixture.ctx, app.CodeInitRequest{
		TaskID:    fixture.task.ID.String(),
		StepID:    fixture.step.ID.String(),
		AttemptID: attempt.ID.String(),
	})
	if !errors.Is(initErr, app.ErrCodeInitRecoveryRequired) {
		t.Fatalf("不确定现场必须阻止重新初始化，实际错误: %v", initErr)
	}
	// 不得插入新记录：同一尝试的活动记录数量应保持不变。
	records, err := fixture.db.ListCodeFetchRecordsByAttempt(fixture.ctx, attempt.ID.String())
	if err != nil {
		t.Fatalf("读取记录失败: %v", err)
	}
	if len(records) != 1 || records[0].ID != record.ID {
		t.Fatalf("拒绝重放时不得新增记录，实际 %d 条", len(records))
	}
}

// TestCodeFetchVerifyConvergesOnlyWithConclusiveFacts 验证只读核验的收敛边界。
func TestCodeFetchVerifyConvergesOnlyWithConclusiveFacts(t *testing.T) {
	t.Parallel()
	// 任务分支名由 taskID 派生：核验用例必须用真实分支名构造探测结果，
	// 否则断言的是"分支不匹配"而不是目标场景。
	cases := []struct {
		name       string
		replies    func(taskBranch string) map[string]docker.CommandResult
		execErr    error
		converged  bool
		wantStatus domain.CodeFetchStatus
	}{
		{
			name: "目录不存在收敛为 failed",
			replies: func(taskBranch string) map[string]docker.CommandResult {
				return map[string]docker.CommandResult{
					"test -e /workspace/project": {ExitCode: 1, Status: "exit_error", Certain: true},
				}
			},
			converged:  true,
			wantStatus: domain.CodeFetchStatusFailed,
		},
		{
			name: "分支与 HEAD 齐备收敛为 succeeded",
			replies: func(taskBranch string) map[string]docker.CommandResult {
				return map[string]docker.CommandResult{
					"test -e /workspace/project": {ExitCode: 0, Status: "succeeded", Certain: true},
					"git rev-parse --abbrev-ref HEAD": {
						ExitCode: 0,
						Stdout:   taskBranch + "\n",
						Status:   "succeeded",
						Certain:  true,
					},
					"git rev-parse HEAD": {
						ExitCode: 0,
						Stdout:   strings.Repeat("2", 40) + "\n",
						Status:   "succeeded",
						Certain:  true,
					},
				}
			},
			converged:  true,
			wantStatus: domain.CodeFetchStatusSucceeded,
		},
		{
			name:    "核验无法完成时保持不确定",
			execErr: errors.New("执行节点不可达"),
		},
		{
			name: "不是 Git 仓库时保持不确定",
			replies: func(taskBranch string) map[string]docker.CommandResult {
				return map[string]docker.CommandResult{
					"test -e /workspace/project": {ExitCode: 0, Status: "succeeded", Certain: true},
				}
			},
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newCodeFetchRecoveryFixture(t)
			record := fixture.createRecord(t, "attempt-verify", domain.CodeFetchStatusUncertain)
			if testCase.replies != nil {
				fixture.probe.replies = testCase.replies(record.TaskBranch)
			}
			fixture.probe.execErr = testCase.execErr
			verifier, err := app.NewCodeFetchVerifyService(
				fixture.db,
				fixture.probe,
				fixture.logger.Slog(),
			)
			if err != nil {
				t.Fatalf("构造核验服务失败: %v", err)
			}
			result, verifyErr := verifier.Verify(fixture.ctx, app.CodeFetchVerifyRequest{
				RecordID: record.ID.String(),
			})
			if testCase.execErr != nil && verifyErr != nil {
				t.Fatalf("核验失败不应返回错误（应降级保留不确定）: %v", verifyErr)
			}
			if result.Decision.Converged != testCase.converged {
				t.Fatalf("收敛结论不正确: %+v", result.Decision)
			}
			stored := fixture.reload(t, record.ID)
			if testCase.converged {
				if stored.Status != testCase.wantStatus {
					t.Fatalf("收敛状态不正确: 期望 %s，实际 %s", testCase.wantStatus, stored.Status)
				}
			} else if stored.Status != domain.CodeFetchStatusUncertain {
				t.Fatalf("事实不充分时必须保持 uncertain，实际 %s", stored.Status)
			}
			// 核验只能执行只读命令。
			for _, command := range fixture.probe.commands {
				if len(command) == 0 {
					continue
				}
				for _, forbidden := range []string{"clone", "checkout", "reset", "rm", "clean"} {
					if command[0] == "git" && len(command) > 1 && command[1] == forbidden {
						t.Fatalf("核验不得执行写操作: %v", command)
					}
				}
			}
		})
	}
}

// prepareEligibleContainer 写入满足代码初始化前置条件的容器与工作区记录。
// mustRecoveryContainer 构造一条完整合法的容器登记记录。
//
// 容器记录必须带完整的环境版本与镜像信息：缺字段会在写入时被领域校验拒绝，
// 而用例要验证的是"不确定现场"这一条拒绝原因，不是容器字段缺失。
func mustRecoveryContainer(
	t *testing.T,
	f *codeFetchRecoveryFixture,
	attemptID domain.ID,
) domain.Container {
	t.Helper()
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	hostID := domain.NewID()
	if f.task.HostID != nil {
		hostID = *f.task.HostID
	}
	container, err := domain.NewContainer(
		domain.ID(codeFetchRecoveryContainerID),
		hostID,
		"agentflow-code-fetch-recovery",
		ref,
		"v0001",
		strings.Repeat("a", 64),
		"sha256:"+strings.Repeat("b", 64),
		f.now,
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	container.TaskID = &f.task.ID
	container.AttemptID = &attemptID
	return container
}

func (f *codeFetchRecoveryFixture) prepareEligibleContainer(
	t *testing.T,
	attemptID domain.ID,
) {
	t.Helper()
	container := mustRecoveryContainer(t, f, attemptID)
	if err := f.db.CreateContainer(f.ctx, container); err != nil {
		t.Fatalf("写入容器记录失败: %v", err)
	}
	// 容器必须真实进入 running 并带上 Docker 容器 ID：代码初始化会核验这两点。
	if err := container.MarkCreated("docker-code-fetch-recovery", domain.DockerStateCreated, f.now); err != nil {
		t.Fatalf("标记容器已创建失败: %v", err)
	}
	if err := container.MarkRunning(f.now); err != nil {
		t.Fatalf("标记容器运行失败: %v", err)
	}
	if err := f.db.UpdateContainer(f.ctx, container); err != nil {
		t.Fatalf("更新容器记录失败: %v", err)
	}
	f.eligibleContainer = container
	workspace, err := domain.NewTaskWorkspaceInitRecord(
		domain.NewID(),
		f.task.ID.String(),
		container.ID.String(),
		container.DockerID,
		domain.CodeSourceGit,
		domain.DefaultContainerWorkDir,
		f.now,
	)
	if err != nil {
		t.Fatalf("构造工作区初始化记录失败: %v", err)
	}
	if err := f.db.CreateTaskWorkspaceInit(f.ctx, workspace); err != nil {
		t.Fatalf("写入工作区初始化记录失败: %v", err)
	}
	if err := workspace.MarkRunning(domain.CodeFetchStageClone, f.now); err != nil {
		t.Fatalf("标记工作区运行失败: %v", err)
	}
	if err := workspace.MarkSucceeded(
		"main",
		domain.GitBranchSelectionExplicit,
		strings.Repeat("1", 40),
		domain.TaskBranchSourceNew.String(),
		strings.Repeat("2", 40),
		f.now,
	); err != nil {
		t.Fatalf("标记工作区成功失败: %v", err)
	}
	if err := f.db.UpdateTaskWorkspaceInit(f.ctx, workspace); err != nil {
		t.Fatalf("更新工作区初始化记录失败: %v", err)
	}
}

// failingCodeFetchStore 让代码获取记录的条件写入失败。
type failingCodeFetchStore struct {
	*sqlite.DB
}

// UpdateCodeFetchRecordIfStatus 始终返回错误。
func (s *failingCodeFetchStore) UpdateCodeFetchRecordIfStatus(
	ctx context.Context,
	record domain.CodeFetchRecord,
	expected []domain.CodeFetchStatus,
) error {
	return errors.New("数据库写入失败")
}

// newerCodeFetchStore 在列出记录后立刻让另一个执行者推进记录。
type newerCodeFetchStore struct {
	*sqlite.DB
	advance func()
}

// ListInterruptedCodeFetchRecords 在返回结果前触发外部推进。
func (s *newerCodeFetchStore) ListInterruptedCodeFetchRecords(
	ctx context.Context,
) ([]domain.CodeFetchRecord, error) {
	records, err := s.DB.ListInterruptedCodeFetchRecords(ctx)
	if err != nil {
		return nil, err
	}
	if s.advance != nil {
		advance := s.advance
		s.advance = nil
		advance()
	}
	return records, nil
}

// rejectingCodeInitContainers 是代码初始化用例使用的容器端口。
//
// 用例只验证"拒绝发生在执行之前"，因此这里的任何代码获取调用都视为越界。
type rejectingCodeInitContainers struct {
	container domain.Container
}

// InspectContainer 返回运行中的容器记录。
func (c *rejectingCodeInitContainers) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	return app.ContainerDetail{
		Container: c.container,
		Inspect:   &docker.ContainerInspect{Running: true, State: domain.DockerStateRunning},
	}, nil
}

// FetchSourceWith 表示代码获取被调用。
func (c *rejectingCodeInitContainers) FetchSourceWith(
	ctx context.Context,
	containerID string,
	request source.FetchRequest,
) (source.FetchResult, error) {
	return source.FetchResult{}, errors.New("拒绝路径不应执行代码获取")
}
