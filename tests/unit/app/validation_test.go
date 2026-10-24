// Package app_test 提供确定性验证引擎的单元测试。
//
// 用例只依赖数据库与受控替身：检查命令由 fakeCheckContainer 返回受控结果，
// 不访问真实容器、SSH、Docker 或模型，因此默认测试路径完全在测试工作空间内完成。
package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// checkReply 是一条受控的检查执行结果。
type checkReply struct {
	exitCode  int
	stdout    string
	stderr    string
	started   bool
	certain   bool
	timedOut  bool
	cancelled bool
	err       error
}

// fakeCheckContainer 是容器能力的受控替身。
type fakeCheckContainer struct {
	mu sync.Mutex
	// inspectErr 使容器核验失败。
	inspectErr error
	// running 表示核验结果中的容器是否在运行。
	running bool
	// detached 表示容器已脱离管理。
	detached bool
	// replies 按命令名返回受控结果。
	replies map[string]checkReply
	// execErr 使命令执行失败（用于模拟执行层不可用）。
	execErr error
	// block 使执行阻塞直到 ctx 结束。
	block bool
	// started 在开始执行时关闭，用于同步取消用例。
	started chan struct{}
	// requests 记录收到的执行请求。
	requests []app.ExecRequest
	// onExec 在返回结果前执行，用于注入副作用。
	onExec func(request app.ExecRequest)
}

// InspectContainer 返回受控的容器状态。
func (f *fakeCheckContainer) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	if f.inspectErr != nil {
		return app.ContainerDetail{}, f.inspectErr
	}
	return app.ContainerDetail{
		Container: domain.Container{
			ID:       domain.ID(request.ContainerID),
			HostID:   "host-1",
			Name:     "agentflow-abcdef123456",
			DockerID: "docker-" + request.ContainerID,
			Detached: f.detached,
		},
		Inspect: &docker.ContainerInspect{Running: f.running},
	}, nil
}

// Exec 返回受控的命令执行结果。
func (f *fakeCheckContainer) Exec(
	ctx context.Context,
	request app.ExecRequest,
) (app.ExecResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	onExec := f.onExec
	block := f.block
	started := f.started
	name := ""
	if len(request.Command) > 0 {
		name = request.Command[0]
	}
	reply := f.replies[name]
	execErr := f.execErr
	if reply.err != nil {
		execErr = reply.err
	}
	f.mu.Unlock()
	if onExec != nil {
		onExec(request)
	}
	if started != nil {
		select {
		case <-started:
		default:
			close(started)
		}
	}
	if block {
		<-ctx.Done()
		status := sshclient.CommandStatusCancelled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = sshclient.CommandStatusTimeout
		}
		return app.ExecResult{Result: docker.CommandResult{
			ExitCode: -1,
			Status:   status,
			Started:  true,
			Certain:  false,
		}}, ctx.Err()
	}
	status := sshclient.CommandStatusSucceeded
	switch {
	case execErr != nil && !reply.started:
		// 执行层无法启动命令：退出码与输出都没有意义。
		status = sshclient.CommandStatusConnectionError
	case reply.cancelled:
		status = sshclient.CommandStatusCancelled
	case reply.timedOut:
		status = sshclient.CommandStatusTimeout
	case reply.exitCode != 0:
		status = sshclient.CommandStatusExitError
	}
	certain := reply.certain
	startedFlag := reply.started
	if status == sshclient.CommandStatusSucceeded ||
		status == sshclient.CommandStatusExitError {
		certain = true
		startedFlag = true
	}
	return app.ExecResult{Result: docker.CommandResult{
		ExitCode: reply.exitCode,
		Stdout:   reply.stdout,
		Stderr:   reply.stderr,
		Status:   status,
		Certain:  certain,
		Started:  startedFlag,
		Duration: 10 * time.Millisecond,
	}}, execErr
}

// validationFixture 是一套可直接发起验证的测试数据。
type validationFixture struct {
	db         *sqlite.DB
	containers *fakeCheckContainer
	service    *app.ValidationService
	task       domain.Task
	step       domain.Step
	attempt    domain.Attempt
	outputRoot string
	ctx        context.Context
	workspace  *testkit.Workspace
	logger     *logging.Logger
}

// newValidationFixture 在工作空间中准备完整的验证测试数据。
//
// 任务的执行节点被显式置为 normal：验证前置条件要求节点已通过环境预检，
// 未通过的节点会让验证在命令执行前就被拒绝。
func newValidationFixture(t *testing.T, spec domain.ValidationSpec) *validationFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.SQLitePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	now := time.Now().UTC()
	developer := mustValidationRole(t, db, ctx, domain.RoleTypeDeveloper, now)
	planner := mustValidationRole(t, db, ctx, domain.RoleTypePlanner, now)
	host := mustValidationHost(t, db, ctx, now)
	requirement := mustValidationRequirement(t, db, ctx, now)
	task := mustValidationTask(t, db, ctx, requirement, host, planner, developer, now)
	step := mustValidationStep(t, db, ctx, task, developer, spec, now)
	attempt := mustValidationAttempt(t, db, ctx, step, now)
	// 尝试必须已经结束才允许验证：running 表示执行尚未收敛。
	if err := attempt.Complete(now); err != nil {
		t.Fatalf("标记尝试完成失败: %v", err)
	}
	if err := db.UpdateAttemptIfStatus(
		ctx,
		attempt,
		[]domain.AttemptStatus{domain.AttemptStatusRunning},
	); err != nil {
		t.Fatalf("写入尝试终态失败: %v", err)
	}
	stored, err := db.GetAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	containers := &fakeCheckContainer{
		running: true,
		replies: map[string]checkReply{},
	}
	service, serviceErr := app.NewValidationService(app.ValidationOptions{
		Store:      db,
		Containers: containers,
		OutputRoot: workspace.Dir("validations"),
		Logger:     logger.Slog(),
		Now:        func() time.Time { return time.Now().UTC() },
	})
	if serviceErr != nil {
		t.Fatalf("构造验证服务失败: %v", serviceErr)
	}
	if err := service.Start(ctx); err != nil {
		t.Fatalf("启动验证服务失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		if stopErr := service.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止验证服务失败: %v", stopErr)
		}
	})
	return &validationFixture{
		db:         db,
		containers: containers,
		service:    service,
		task:       task,
		step:       step,
		attempt:    *stored,
		outputRoot: workspace.Path("validations"),
		ctx:        ctx,
		workspace:  workspace,
		logger:     logger,
	}
}

// request 构造一次验证请求。
func (f *validationFixture) request() app.ValidationExecuteRequest {
	return app.ValidationExecuteRequest{
		TaskID:    f.task.ID.String(),
		StepID:    f.step.ID.String(),
		AttemptID: f.attempt.ID.String(),
	}
}

// reply 为指定命令登记受控结果。
func (f *validationFixture) reply(command string, reply checkReply) {
	f.containers.mu.Lock()
	defer f.containers.mu.Unlock()
	f.containers.replies[command] = reply
}

// mustValidationRole 创建并写入测试角色。
func mustValidationRole(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	roleType domain.RoleType,
	now time.Time,
) domain.Role {
	t.Helper()
	role, err := domain.NewRole(
		"验证测试角色",
		roleType,
		"provider/model",
		"角色提示词",
		domain.DefaultToolsPolicy(roleType),
		now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := db.CreateRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	return role
}

// mustValidationHost 创建并写入一个已通过环境预检的执行节点。
func mustValidationHost(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	now time.Time,
) domain.ExecutionHost {
	t.Helper()
	host, err := domain.NewExecutionHost(
		"", "验证测试节点", "127.0.0.1", 0, "deploy", "", now,
		domain.WithPrivateKey(testkit.PrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	// 只有 normal 状态允许执行真实远程操作，否则验证会在命令执行前被拒绝。
	details := domain.ValidationDetails{
		ValidatorVersion: domain.ValidatorVersion,
		Checks: []domain.ValidationCheck{
			{Name: domain.ValidationCheckSSHConnection, Status: domain.CheckStatusPassed},
		},
	}
	if err := host.RecordValidationResult(host.ValidationGeneration, details, now); err != nil {
		t.Fatalf("标记节点校验结果失败: %v", err)
	}
	ok, err := db.UpdateExecutionHostValidation(ctx, host, host.ValidationGeneration)
	if err != nil {
		t.Fatalf("写入节点校验结果失败: %v", err)
	}
	if !ok {
		t.Fatal("节点校验结果未写入")
	}
	loaded, err := db.GetExecutionHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	if loaded.ValidationStatus != domain.ValidationStatusNormal {
		t.Fatalf("节点校验状态不符: %s", loaded.ValidationStatus)
	}
	return *loaded
}

// mustValidationRequirement 创建并写入测试需求。
func mustValidationRequirement(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	now time.Time,
) domain.Requirement {
	t.Helper()
	requirement, err := domain.NewRequirement("验证测试需求", "验证测试正文", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	return requirement
}

// mustValidationTask 创建并写入一个已绑定容器与执行节点的任务。
func mustValidationTask(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	requirement domain.Requirement,
	host domain.ExecutionHost,
	planner domain.Role,
	developer domain.Role,
	now time.Time,
) domain.Task {
	t.Helper()
	task, err := domain.NewTask(requirement.ID, "ssh://git.example.com/team/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &host.ID
	task.Config = domain.TaskConfig{
		Goal:            "验证测试任务",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		PlannerRoleID:   planner.ID,
		DeveloperRoleID: developer.ID,
	}
	if err := task.Validate(); err != nil {
		t.Fatalf("任务配置非法: %v", err)
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	// 任务必须处于可验证状态并绑定受管理的运行中容器。
	if err := task.SetContainer("container-under-test"); err != nil {
		t.Fatalf("绑定任务容器失败: %v", err)
	}
	for _, status := range []domain.TaskStatus{
		domain.TaskStatusPreparing,
		domain.TaskStatusDeveloping,
	} {
		if err := task.Transition(status, now); err != nil {
			t.Fatalf("推进任务状态到 %s 失败: %v", status, err)
		}
	}
	if err := db.UpdateTaskIfStatus(ctx, task, []domain.TaskStatus{domain.TaskStatusQueued}); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	loaded, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	return *loaded
}

// mustValidationStep 创建并写入一个带验证规格的步骤。
func mustValidationStep(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	task domain.Task,
	role domain.Role,
	spec domain.ValidationSpec,
	now time.Time,
) domain.Step {
	t.Helper()
	commands := make([]domain.VerificationCommand, 0, len(spec.Checks))
	for _, check := range spec.Checks {
		commands = append(commands, domain.VerificationCommand{
			Name:             check.ID,
			Command:          check.Command,
			Args:             check.Args,
			WorkDir:          check.WorkDir,
			Required:         check.Required,
			Optional:         !check.Required,
			TimeoutSeconds:   check.TimeoutSeconds,
			AllowedExitCodes: check.ExpectedExitCodes,
			MaxOutputBytes:   check.MaxOutputBytes,
		})
	}
	step, err := domain.NewAgentStep(
		task.ID,
		1,
		domain.StepTypeDevelop,
		"验证测试步骤",
		"实现可验证的功能",
		"按验收条件实现",
		role,
		domain.VerificationSpec{Commands: commands},
		domain.RetryConfig{MaxAttempts: 3},
		now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := db.CreateStep(ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	return step
}

// mustValidationAttempt 创建并写入一次执行尝试。
func mustValidationAttempt(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	step domain.Step,
	now time.Time,
) domain.Attempt {
	t.Helper()
	return mustValidationAttemptNumber(t, db, ctx, step, 1, now)
}

// mustValidationAttemptNumber 按指定序号创建并写入一次执行尝试。
func mustValidationAttemptNumber(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	step domain.Step,
	number int,
	now time.Time,
) domain.Attempt {
	t.Helper()
	attempt, err := domain.NewAttempt(step.TaskID, step.ID, number, now)
	if err != nil {
		t.Fatalf("构造执行尝试失败: %v", err)
	}
	if err := db.CreateAttempt(ctx, attempt); err != nil {
		t.Fatalf("写入执行尝试失败: %v", err)
	}
	return attempt
}

// requiredSpec 构造一份只有必需检查的规格。
func requiredSpec(commands ...string) domain.ValidationSpec {
	checks := make([]domain.ValidationCheckDefinition, 0, len(commands))
	for index, command := range commands {
		checks = append(checks, domain.ValidationCheckDefinition{
			ID:       command,
			Name:     command,
			Type:     domain.ValidationCheckTypeCommand,
			Enabled:  true,
			Required: true,
			Sequence: index + 1,
			Command:  command,
		})
	}
	return domain.ValidationSpec{Checks: checks}
}

// TestValidationAllRequiredPassed 验证全部必需检查通过时结论为 passed。
func TestValidationAllRequiredPassed(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build", "test"))
	fixture.reply("build", checkReply{exitCode: 0, stdout: "构建成功"})
	fixture.reply("test", checkReply{exitCode: 0, stdout: "ok"})
	result, err := fixture.service.Validate(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	if !result.Passed || result.Verdict != domain.VerificationVerdictPassed {
		t.Fatalf("结论不符: %s (%s)", result.Verdict, result.FailureSummary)
	}
	if result.Status != domain.ValidationRunStatusCompleted {
		t.Fatalf("验证状态不符: %s", result.Status)
	}
	if result.PassedChecks != 2 || result.FailedChecks != 0 {
		t.Fatalf("统计计数不符: %+v", result)
	}
	if !result.Persisted {
		t.Fatalf("结果应已持久化: %v", result.PersistenceErrors)
	}
	// 结论必须写回执行尝试，且关联到本次验证记录。
	attempt, err := fixture.db.GetAttempt(fixture.ctx, fixture.attempt.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	if attempt.VerificationVerdict != domain.VerificationVerdictPassed {
		t.Fatalf("尝试的验证判定未写入: %s", attempt.VerificationVerdict)
	}
	if attempt.VerificationID == nil || *attempt.VerificationID != result.ValidationRunID {
		t.Fatalf("尝试未关联验证记录: %+v", attempt.VerificationID)
	}
	// 检查结果与验证记录正确关联。
	results, err := fixture.db.ListValidationCheckResults(fixture.ctx, result.ValidationRunID)
	if err != nil {
		t.Fatalf("读取检查结果失败: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("检查结果数量不符: %d", len(results))
	}
	if results[0].CheckID != "build" || results[1].CheckID != "test" {
		t.Fatalf("检查顺序不符: %s, %s", results[0].CheckID, results[1].CheckID)
	}
	for _, check := range results {
		if check.ValidationRunID != result.ValidationRunID {
			t.Fatalf("检查结果未关联验证记录: %s", check.ValidationRunID)
		}
	}
}

// TestValidationRequiredCheckFails 验证必需检查非零退出码判为失败。
func TestValidationRequiredCheckFails(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	fixture.reply("build", checkReply{exitCode: 2, stderr: "编译错误"})
	result, err := fixture.service.Validate(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	if result.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("结论不符: %s", result.Verdict)
	}
	if result.FailureSummary == "" {
		t.Fatal("失败时必须给出原因")
	}
	results, err := fixture.db.ListValidationCheckResults(fixture.ctx, result.ValidationRunID)
	if err != nil {
		t.Fatalf("读取检查结果失败: %v", err)
	}
	if len(results) != 1 || results[0].ExitCode != 2 {
		t.Fatalf("退出码未保存: %+v", results)
	}
	if results[0].StderrSummary == "" {
		t.Fatal("标准错误摘要未保存")
	}
}

// TestValidationOptionalCheckFails 验证可选检查失败不影响结论。
func TestValidationOptionalCheckFails(t *testing.T) {
	t.Parallel()
	spec := requiredSpec("build")
	optional := domain.ValidationCheckDefinition{
		ID:       "lint",
		Name:     "lint",
		Type:     domain.ValidationCheckTypeCommand,
		Enabled:  true,
		Required: false,
		Sequence: 2,
		Command:  "lint",
	}
	spec.Checks = append(spec.Checks, optional)
	fixture := newValidationFixture(t, spec)
	fixture.reply("build", checkReply{exitCode: 0})
	fixture.reply("lint", checkReply{exitCode: 1, stderr: "风格问题"})
	result, err := fixture.service.Validate(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	if !result.Passed {
		t.Fatalf("可选检查失败不应阻止通过，实际 %s: %s", result.Verdict, result.FailureSummary)
	}
	if result.FailedChecks != 1 {
		t.Fatalf("失败计数不符: %+v", result)
	}
}

// TestValidationCheckTimedOut 验证必需检查超时判为失败。
func TestValidationCheckTimedOut(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	fixture.reply("build", checkReply{exitCode: -1, timedOut: true})
	result, err := fixture.service.Validate(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	if result.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("超时应判为失败，实际 %s", result.Verdict)
	}
	if result.Checks[0].Status != domain.ValidationCheckStatusTimedOut {
		t.Fatalf("检查状态不符: %s", result.Checks[0].Status)
	}
	if !result.Checks[0].TimedOut {
		t.Fatal("超时标记未保存")
	}
}

// TestValidationCheckStartFailed 验证命令无法启动时判为失败且记录原因。
func TestValidationCheckStartFailed(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	fixture.reply("build", checkReply{
		exitCode: -1,
		started:  false,
		certain:  false,
		err:      errors.New("容器内命令未能启动"),
	})
	result, err := fixture.service.Validate(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	if result.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("启动失败应判为失败，实际 %s", result.Verdict)
	}
	if result.Checks[0].Status != domain.ValidationCheckStatusError {
		t.Fatalf("检查状态不符: %s", result.Checks[0].Status)
	}
	if result.Checks[0].FailureReason == "" {
		t.Fatal("启动失败必须记录原因")
	}
}

// TestValidationCancelledByCaller 验证调用方取消时结论为失败且状态为 cancelled。
func TestValidationCancelledByCaller(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build", "test"))
	fixture.containers.block = true
	fixture.containers.started = make(chan struct{})
	ctx, cancel := context.WithCancel(fixture.ctx)
	go func() {
		<-fixture.containers.started
		cancel()
	}()
	result, err := fixture.service.Validate(ctx, fixture.request())
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	if result.Passed {
		t.Fatal("被取消的验证不能判定通过")
	}
	if result.Status != domain.ValidationRunStatusCancelled {
		t.Fatalf("验证状态不符: %s", result.Status)
	}
	if result.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("被取消的验证结论必须是失败，实际 %s", result.Verdict)
	}
	// 未执行的必需检查不得被当作通过：它们必须以 skipped 记录。
	if len(result.Checks) != 2 {
		t.Fatalf("未执行的检查也必须留下记录，实际 %d", len(result.Checks))
	}
	if result.Checks[1].Status != domain.ValidationCheckStatusSkipped {
		t.Fatalf("未执行的检查状态不符: %s", result.Checks[1].Status)
	}
}

// TestValidationRequiresRunningContainer 验证缺少可用容器时明确报告前置条件。
func TestValidationRequiresRunningContainer(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	fixture.containers.running = false
	_, err := fixture.service.Validate(fixture.ctx, fixture.request())
	if !errors.Is(err, app.ErrValidationPrerequisite) {
		t.Fatalf("容器未运行时应返回前置条件错误，实际 %v", err)
	}
	if len(fixture.containers.requests) != 0 {
		t.Fatal("前置条件不满足时不得执行任何命令")
	}
}

// TestValidationRequiresContainerBinding 验证任务没有容器时不得执行命令。
func TestValidationRequiresContainerBinding(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	task := fixture.task
	if err := task.SetContainer(""); err != nil {
		t.Fatalf("清除容器绑定失败: %v", err)
	}
	if err := fixture.db.UpdateTaskIfStatus(
		fixture.ctx,
		task,
		[]domain.TaskStatus{domain.TaskStatusDeveloping},
	); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	_, err := fixture.service.Validate(fixture.ctx, fixture.request())
	if !errors.Is(err, app.ErrValidationPrerequisite) {
		t.Fatalf("缺少容器时应返回前置条件错误，实际 %v", err)
	}
	if len(fixture.containers.requests) != 0 {
		t.Fatal("前置条件不满足时不得在宿主机执行命令")
	}
}

// TestValidationRejectsUnfinishedAttempt 验证未结束的尝试不允许验证。
func TestValidationRejectsUnfinishedAttempt(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	// 重新创建一条仍在运行的尝试：序号必须与已有尝试不同。
	running := mustValidationAttemptNumber(t, fixture.db, fixture.ctx, fixture.step, 2, time.Now().UTC())
	request := fixture.request()
	request.AttemptID = running.ID.String()
	_, err := fixture.service.Validate(fixture.ctx, request)
	if !errors.Is(err, app.ErrValidationConflict) {
		t.Fatalf("未结束的尝试应返回状态冲突，实际 %v", err)
	}
}

// TestValidationRejectsForeignScope 验证跨任务的归属被拒绝且不泄露存在性。
func TestValidationRejectsForeignScope(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	other := newValidationFixture(t, requiredSpec("build"))
	request := fixture.request()
	request.AttemptID = other.attempt.ID.String()
	_, err := fixture.service.Validate(fixture.ctx, request)
	if !errors.Is(err, app.ErrValidationNotFound) {
		t.Fatalf("跨任务尝试应返回不存在，实际 %v", err)
	}
}

// TestValidationDuplicateRequestConflicts 验证同一尝试的并发验证被拒绝。
func TestValidationDuplicateRequestConflicts(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	fixture.containers.block = true
	fixture.containers.started = make(chan struct{})
	var (
		waitGroup sync.WaitGroup
		mu        sync.Mutex
		conflicts int
		completed int
	)
	waitGroup.Add(2)
	for index := 0; index < 2; index++ {
		go func() {
			defer waitGroup.Done()
			ctx, cancel := context.WithTimeout(fixture.ctx, 2*time.Second)
			defer cancel()
			_, err := fixture.service.Validate(ctx, fixture.request())
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, app.ErrValidationConflict):
				conflicts++
			case err == nil:
				completed++
			}
		}()
	}
	<-fixture.containers.started
	waitGroup.Wait()
	if conflicts == 0 {
		t.Fatalf("并发验证应至少有一个被拒绝，实际 conflicts=%d completed=%d", conflicts, completed)
	}
	// 数据库中同一尝试最多只有一条运行中的验证记录。
	runs, err := fixture.db.ListValidationRunsByAttempt(fixture.ctx, fixture.attempt.ID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	active := 0
	for _, run := range runs {
		if run.Status == domain.ValidationRunStatusRunning {
			active++
		}
	}
	if active > 1 {
		t.Fatalf("同一尝试存在多条运行中的验证: %d", active)
	}
}

// TestValidationRecordsPartialResults 验证部分检查完成后失败仍可追溯。
func TestValidationRecordsPartialResults(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build", "test"))
	fixture.reply("build", checkReply{exitCode: 0})
	fixture.reply("test", checkReply{exitCode: 1, stderr: "测试失败"})
	result, err := fixture.service.Validate(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	if result.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("结论不符: %s", result.Verdict)
	}
	// 第一条检查的结果不能被后面的失败吞掉。
	results, err := fixture.db.ListValidationCheckResults(fixture.ctx, result.ValidationRunID)
	if err != nil {
		t.Fatalf("读取检查结果失败: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("检查结果数量不符: %d", len(results))
	}
	if results[0].Status != domain.ValidationCheckStatusPassed {
		t.Fatalf("先前通过的检查结果丢失: %+v", results[0])
	}
}

// TestValidationRejectsMissingRequiredCheck 验证没有必需检查的配置被拒绝。
func TestValidationRejectsMissingRequiredCheck(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, domain.ValidationSpec{})
	_, err := fixture.service.Validate(fixture.ctx, fixture.request())
	if err == nil {
		t.Fatal("没有检查配置时不应返回成功")
	}
	if len(fixture.containers.requests) != 0 {
		t.Fatal("配置非法时不得执行任何命令")
	}
}

// TestValidationCheckOverrideCannotWeakenGate 验证请求覆盖不能放宽必需检查门槛。
func TestValidationCheckOverrideCannotWeakenGate(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	request := fixture.request()
	// 只提交可选检查：覆盖后没有启用的必需检查，必须判为失败。
	request.Checks = []domain.ValidationCheckDefinition{{
		ID:       "lint",
		Name:     "lint",
		Type:     domain.ValidationCheckTypeCommand,
		Enabled:  true,
		Required: false,
		Sequence: 1,
		Command:  "lint",
	}}
	fixture.reply("lint", checkReply{exitCode: 0})
	_, err := fixture.service.Validate(fixture.ctx, request)
	// 覆盖后没有启用的必需检查：直接以参数错误拒绝，而不是创建一条注定失败的记录。
	if !errors.Is(err, app.ErrValidationInvalidInput) {
		t.Fatalf("缺少必需检查的覆盖配置应被拒绝，实际 %v", err)
	}
	if len(fixture.containers.requests) != 0 {
		t.Fatal("配置不可用时不得执行任何命令")
	}
}

// TestValidationRejectsUnsafeWorkDir 验证检查工作目录必须位于任务工作区之内。
func TestValidationRejectsUnsafeWorkDir(t *testing.T) {
	t.Parallel()
	spec := requiredSpec("build")
	spec.Checks[0].WorkDir = "/tmp"
	fixture := newValidationFixture(t, requiredSpec("build"))
	request := fixture.request()
	request.Checks = spec.Checks
	_, err := fixture.service.Validate(fixture.ctx, request)
	if !errors.Is(err, app.ErrValidationInvalidInput) {
		t.Fatalf("容器外工作目录应返回参数非法，实际 %v", err)
	}
	if len(fixture.containers.requests) != 0 {
		t.Fatal("工作目录非法时不得执行命令")
	}
}

// TestValidationStoppedServiceRejectsNewRuns 验证服务停止后不再接受验证。
func TestValidationStoppedServiceRejectsNewRuns(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := fixture.service.Stop(stopCtx); err != nil {
		t.Fatalf("停止验证服务失败: %v", err)
	}
	_, err := fixture.service.Validate(fixture.ctx, fixture.request())
	if !errors.Is(err, app.ErrValidationEngineStopped) {
		t.Fatalf("服务已停止时应拒绝验证，实际 %v", err)
	}
}

// TestValidationPersistFailureIsNotSuccess 验证结论写入失败时不得报告成功。
func TestValidationPersistFailureIsNotSuccess(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	fixture.reply("build", checkReply{exitCode: 0})
	// 用可控失败的存储包装真实存储：业务代码不变，失败来自存储层。
	failing := &failingValidationStore{
		ValidationStore: fixture.db,
		finishErr:       errors.New("数据库写入失败"),
	}
	service, err := app.NewValidationService(app.ValidationOptions{
		Store:      failing,
		Containers: fixture.containers,
		OutputRoot: fixture.outputRoot,
	})
	if err != nil {
		t.Fatalf("构造验证服务失败: %v", err)
	}
	if err := service.Start(fixture.ctx); err != nil {
		t.Fatalf("启动验证服务失败: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := service.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止验证服务失败: %v", stopErr)
		}
	}()
	result, validateErr := service.Validate(fixture.ctx, fixture.request())
	if !errors.Is(validateErr, app.ErrValidationNotPersisted) {
		t.Fatalf("结论写入失败时应返回持久化错误，实际 %v", validateErr)
	}
	if result.Persisted {
		t.Fatal("写入失败时 Persisted 不能为 true")
	}
	if len(result.PersistenceErrors) == 0 {
		t.Fatal("必须记录写入失败原因")
	}
}

// failingValidationStore 包装真实存储，只把指定写入替换为可控失败。
type failingValidationStore struct {
	app.ValidationStore
	finishErr error
	appendErr error
}

// FinishValidationRun 在配置了错误时返回该错误。
func (s *failingValidationStore) FinishValidationRun(
	ctx context.Context,
	request storage.ValidationRunFinishRequest,
) error {
	if s.finishErr != nil {
		return s.finishErr
	}
	return s.ValidationStore.FinishValidationRun(ctx, request)
}

// AppendValidationCheckResult 在配置了错误时返回该错误。
func (s *failingValidationStore) AppendValidationCheckResult(
	ctx context.Context,
	result domain.ValidationCheckResult,
) error {
	if s.appendErr != nil {
		return s.appendErr
	}
	return s.ValidationStore.AppendValidationCheckResult(ctx, result)
}

// TestValidationOverallTimeout 验证整次验证超时后收敛为 timed_out 且不判通过。
func TestValidationOverallTimeout(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build", "test"))
	fixture.containers.block = true
	fixture.containers.started = make(chan struct{})
	request := fixture.request()
	request.Timeout = 100 * time.Millisecond
	result, err := fixture.service.Validate(fixture.ctx, request)
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	if result.Status != domain.ValidationRunStatusTimedOut {
		t.Fatalf("验证状态不符: %s", result.Status)
	}
	if result.Passed {
		t.Fatal("整体超时的验证不能判定通过")
	}
	if result.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("整体超时的验证结论必须是失败，实际 %s", result.Verdict)
	}
	// 超时后未执行的检查必须以 skipped 记录，不能被当作通过。
	skipped := 0
	for _, check := range result.Checks {
		if check.Status == domain.ValidationCheckStatusSkipped {
			skipped++
		}
	}
	if skipped == 0 {
		t.Fatalf("超时后未执行的检查必须记录为 skipped: %+v", result.Checks)
	}
	run, err := fixture.db.GetValidationRun(fixture.ctx, result.ValidationRunID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	if run.Status != domain.ValidationRunStatusTimedOut {
		t.Fatalf("持久化的验证状态不符: %s", run.Status)
	}
}

// TestValidationServiceStopCancelsInFlight 验证服务停止会中断在途验证并收敛记录。
func TestValidationServiceStopCancelsInFlight(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	fixture.containers.block = true
	fixture.containers.started = make(chan struct{})
	type outcome struct {
		result app.ValidationExecuteResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := fixture.service.Validate(fixture.ctx, fixture.request())
		done <- outcome{result: result, err: err}
	}()
	<-fixture.containers.started
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := fixture.service.Stop(stopCtx); err != nil {
		t.Fatalf("停止验证服务失败: %v", err)
	}
	finished := <-done
	if finished.err != nil {
		t.Fatalf("服务停止导致的验证应返回结果而不是错误: %v", finished.err)
	}
	if finished.result.Passed {
		t.Fatal("被中断的验证不能判定通过")
	}
	if finished.result.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("被中断的验证结论必须是失败，实际 %s", finished.result.Verdict)
	}
	// 验证记录必须收敛为终态，不能留下 running 残留。
	run, err := fixture.db.GetValidationRun(fixture.ctx, finished.result.ValidationRunID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	if run.Status == domain.ValidationRunStatusRunning {
		t.Fatal("服务停止后验证记录不得停留在 running")
	}
	if run.Status.IsTerminal() != true {
		t.Fatalf("验证记录应进入终态，实际 %s", run.Status)
	}
	if fixture.service.ActiveValidations() != 0 {
		t.Fatalf("停止后不应有在途验证: %d", fixture.service.ActiveValidations())
	}
}

// TestValidationArchivesOutputs 验证检查输出分别落盘且摘要写入数据库。
func TestValidationArchivesOutputs(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	fixture.reply("build", checkReply{exitCode: 0, stdout: "标准输出内容", stderr: "标准错误内容"})
	result, err := fixture.service.Validate(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	if len(result.Checks) != 1 {
		t.Fatalf("检查结果数量不符: %d", len(result.Checks))
	}
	check := result.Checks[0]
	if check.StdoutPath == "" || check.StderrPath == "" {
		t.Fatalf("输出未落盘: %+v", check)
	}
	if check.StdoutPath == check.StderrPath {
		t.Fatal("标准输出与标准错误必须分别落盘")
	}
	if check.OutputArchiveError != "" {
		t.Fatalf("归档不应失败: %s", check.OutputArchiveError)
	}
	if check.StdoutSummary != "标准输出内容" || check.StderrSummary != "标准错误内容" {
		t.Fatalf("输出摘要不符: %+v", check)
	}
}

// validationRun 读取一条验证记录。
func (f *validationFixture) validationRun(t *testing.T, runID domain.ID) domain.ValidationRun {
	t.Helper()
	run, err := f.db.GetValidationRun(f.ctx, runID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	return *run
}

// startValidationService 启动验证服务并在用例结束时停止。
func startValidationService(t *testing.T, service *app.ValidationService) {
	t.Helper()
	startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := service.Start(startCtx); err != nil {
		t.Fatalf("启动验证服务失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		if err := service.Stop(stopCtx); err != nil {
			t.Errorf("停止验证服务失败: %v", err)
		}
	})
}
