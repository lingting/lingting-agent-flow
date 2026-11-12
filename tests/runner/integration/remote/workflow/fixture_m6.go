// 本文件是 tests/integration/remote/workflow/acceptance_m6_test.go 中基础设施的迁移结果：
// 项目级真实验收现场、需求播种与状态轮询辅助。
//
// 改造点：参数类型由 *testing.T 换成 *testrunner.Context；其余语义保持原样。
package workflow

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/testrunner"
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
	// m6ExecutionReleaseTimeout 是等待项目执行权释放的时限。
	//
	// 执行权有效期默认 2 分钟：后台收敛没能在续租周期内释放时，执行权要等到过期才算失效。
	// 这里留出一个周期以上的余量，超时仍判"没有释放"，不放宽后续断言。
	m6ExecutionReleaseTimeout = 5 * time.Minute
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
func newM6Fixture(t *testrunner.Context) *m6Fixture {
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
func (f *m6Fixture) applyRoleModels(t *testrunner.Context) {
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
func (f *m6Fixture) assembleM6(t *testrunner.Context) {
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
func (f *m6Fixture) createProject(t *testrunner.Context, projectID, name string) domain.Project {
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
	t *testrunner.Context,
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
func (f *m6Fixture) task(t *testrunner.Context, taskID domain.ID) domain.Task {
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
func (f *m6Fixture) waitTaskSettled(t *testrunner.Context, taskID domain.ID) domain.Task {
	t.Helper()
	return f.waitTaskSettledWithin(t, taskID, m6SettleTimeout)
}

// waitTaskSettledWithin 在给定时限内等待任务收敛。
func (f *m6Fixture) waitTaskSettledWithin(
	t *testrunner.Context,
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
	t *testrunner.Context,
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
func (f *m6Fixture) projectExecutionSnapshot(t *testrunner.Context, projectID string) app.ProjectExecution {
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
	t *testrunner.Context,
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

// waitProjectIdle 等待项目执行状态为空闲且完成计数达到期望值。
func (f *m6Fixture) waitProjectIdle(
	t *testrunner.Context,
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

// waitProjectExecutionReleased 只读等待项目执行权收敛释放。
//
// 需求删除后执行权由后台收敛回收，回收完成之前同一持有者的 Start 会走幂等分支返回既有
// 执行而不是报错。这里先只读等待投影离开 running，再让调用方断言"拒绝重新开始"：等待
// 不放宽断言，超时仍未释放即判失败。
func (f *m6Fixture) waitProjectExecutionReleased(
	t *testrunner.Context,
	projectID string,
) app.ProjectExecution {
	t.Helper()
	deadline := time.Now().Add(m6ExecutionReleaseTimeout)
	var latest app.ProjectExecution
	for time.Now().Before(deadline) {
		latest = f.projectExecutionSnapshot(t, projectID)
		if latest.Progress.Status != domain.ProjectExecutionStatusRunning {
			return latest
		}
		time.Sleep(m6PollInterval)
	}
	t.Fatalf("等待项目 %s 释放执行权超时：状态 %s，在途 %d",
		projectID, latest.Progress.Status, latest.Progress.Counts.Running)
	return latest
}

// assertM6TaskExecutionHistory 核验任务留下了步骤、尝试、会话与代码保存记录。
//
// 真实完成的链路必须留下可复核的执行历史，且这些记录不因任何后续操作被清理。
func assertM6TaskExecutionHistory(t *testrunner.Context, f *m6Fixture, taskID domain.ID) {
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
