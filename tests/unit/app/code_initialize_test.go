// 代码初始化协调器的单元测试。
//
// 用例只依赖数据库与受控替身：不访问真实 Git、SSH 或 Docker，代码获取结果由
// 替身按预设返回，因此验证的是归属校验、幂等边界、状态映射与持久化行为。
package app_test

import (
	"context"
	"errors"
	"strings"
	"sync"
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

// 测试使用的固定值。
const (
	codeInitContainerID = "container-1"
	codeInitDockerID    = "docker-container-1"
	codeInitBaseSHA     = "1111111111111111111111111111111111111111"
	codeInitHeadSHA     = "2222222222222222222222222222222222222222"
)

// codeInitContainers 是容器能力的受控替身。
type codeInitContainers struct {
	mu sync.Mutex
	// inspectErr 使容器核验失败。
	inspectErr error
	// running 表示容器是否在运行。
	running bool
	// detached 表示容器已脱离管理。
	detached bool
	// fetchErr 是代码获取返回的错误。
	fetchErr error
	// fetched 是代码获取返回的结果；零值时使用成功默认值。
	fetched source.FetchResult
	// requests 记录收到的代码获取请求。
	requests []source.FetchRequest
	// calls 统计代码获取次数。
	calls int
	// block 使代码获取阻塞直到 ctx 结束。
	block bool
	// attemptID 是容器记录登记的尝试归属。
	attemptID domain.ID
	// omitAttemptID 表示容器记录没有 attempt_id。
	omitAttemptID bool
}

// InspectContainer 返回受控的容器状态。
//
// 容器记录的尝试归属来自 attemptID：用例需要它指向真实尝试，才能验证绑定规则。
func (c *codeInitContainers) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	if c.inspectErr != nil {
		return app.ContainerDetail{}, c.inspectErr
	}
	attemptID := domain.ID("attempt-1")
	if c.attemptID != "" {
		attemptID = c.attemptID
	}
	var attemptPtr *domain.ID
	if !c.omitAttemptID {
		attemptPtr = &attemptID
	}
	container := domain.Container{
		ID:        domain.ID(request.ContainerID),
		DockerID:  codeInitDockerID,
		TaskID:    ptrToID(domain.ID("task-placeholder")),
		AttemptID: attemptPtr,
		Detached:  c.detached,
		State:     domain.ContainerStateRunning,
	}
	if c.detached {
		container.State = domain.ContainerStateDetached
	}
	return app.ContainerDetail{
		Container: container,
		Inspect:   &docker.ContainerInspect{Running: c.running},
	}, nil
}

// FetchSourceWith 记录请求并返回受控结果。
func (c *codeInitContainers) FetchSourceWith(
	ctx context.Context,
	containerID string,
	request source.FetchRequest,
) (source.FetchResult, error) {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.calls++
	fetched, fetchErr, block := c.fetched, c.fetchErr, c.block
	c.mu.Unlock()
	if block {
		<-ctx.Done()
		return source.FetchResult{}, ctx.Err()
	}
	if fetchErr != nil {
		return fetched, fetchErr
	}
	if fetched.Commit == "" {
		fetched = source.FetchResult{
			ProjectID:        request.ProjectID,
			TaskID:           request.TaskID,
			AttemptID:        request.AttemptID,
			SourceType:       domain.CodeSourceGit,
			Repository:       "https://git.example.com/group/repo.git",
			ContainerID:      containerID,
			DockerID:         codeInitDockerID,
			WorkDir:          request.WorkDir,
			BaseBranch:       "main",
			BaseSelection:    domain.GitBranchSelectionOriginHead,
			BaseCommit:       codeInitBaseSHA,
			TaskBranch:       "agent/" + request.TaskID,
			TaskBranchSource: domain.TaskBranchSourceNew,
			Commit:           codeInitHeadSHA,
			Stage:            domain.CodeFetchStageVerify,
		}
	}
	now := time.Now().UTC()
	fetched.StartedAt = now
	fetched.FinishedAt = now
	return fetched, nil
}

// codeInitFixture 是一套完整的代码初始化测试数据。
type codeInitFixture struct {
	db         *sqlite.DB
	service    *app.CodeInitializeService
	containers *codeInitContainers
	task       domain.Task
	step       domain.Step
	attempt    domain.Attempt
	host       domain.ExecutionHost
}

// newCodeInitFixture 构造代码初始化测试数据。
func newCodeInitFixture(t *testing.T) *codeInitFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	role := createManagementRole(t, db, ctx, domain.RoleTypeDeveloper, "开发角色", now)
	host := markHostNormal(t, db, ctx, createManagementHost(t, db, ctx, now))
	requirement, err := domain.NewRequirement("需求标题", "需求正文", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "ssh://git.example.com/team/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &host.ID
	task.ContainerID = codeInitContainerID
	task.Config = domain.TaskConfig{
		Goal:            "完成本次需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		ProjectID:       "demo",
		PlannerRoleID:   role.ID,
		DeveloperRoleID: role.ID,
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	step, err := newFixtureStep(task.ID, domain.StepTypeDevelop, role, now)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := db.CreateStep(ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	attempt, err := db.StartAttempt(ctx, storageAttemptRequest(task.ID, step.ID))
	if err != nil {
		t.Fatalf("创建执行尝试失败: %v", err)
	}
	containers := &codeInitContainers{running: true, attemptID: attempt.ID}
	service, err := app.NewCodeInitializeService(app.CodeInitializeOptions{
		Store:      db,
		Containers: containers,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造代码初始化服务失败: %v", err)
	}
	return &codeInitFixture{
		db:         db,
		service:    service,
		containers: containers,
		task:       task,
		step:       step,
		attempt:    attempt,
		host:       host,
	}
}

// request 返回一次指向当前测试数据的初始化请求。
func (f *codeInitFixture) request() app.CodeInitRequest {
	return app.CodeInitRequest{
		TaskID:    f.task.ID.String(),
		StepID:    f.step.ID.String(),
		AttemptID: f.attempt.ID.String(),
	}
}

// TestInitializeSucceeds 校验成功的初始化写入完整记录。
func TestInitializeSucceeds(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	ctx := context.Background()
	result, err := fixture.service.Initialize(ctx, fixture.request())
	if err != nil {
		t.Fatalf("初始化代码失败: %v", err)
	}
	if !result.Succeeded || result.Status != domain.CodeFetchStatusSucceeded {
		t.Fatalf("初始化应当成功: %+v", result)
	}
	if result.Commit != codeInitHeadSHA {
		t.Fatalf("commit 不符: %s", result.Commit)
	}
	if result.TaskBranch != "agent/"+fixture.task.ID.String() {
		t.Fatalf("任务分支不符: %s", result.TaskBranch)
	}
	if result.WorkDir != domain.DefaultContainerWorkDir {
		t.Fatalf("目标目录不符: %s", result.WorkDir)
	}
	if !result.Persisted {
		t.Fatalf("记录必须写入成功: %v", result.PersistenceErrors)
	}
	records, err := fixture.db.ListCodeFetchRecordsByAttempt(ctx, fixture.attempt.ID.String())
	if err != nil {
		t.Fatalf("读取代码初始化记录失败: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("记录数不符: %d", len(records))
	}
	if records[0].Status != domain.CodeFetchStatusSucceeded {
		t.Fatalf("记录状态不符: %s", records[0].Status)
	}
	if records[0].BaseBranch != "main" || records[0].BaseCommit != codeInitBaseSHA {
		t.Fatalf("基础分支信息不符: %+v", records[0])
	}
	if records[0].SourceType != domain.CodeSourceGit {
		t.Fatalf("获取方式不符: %s", records[0].SourceType)
	}
}

// TestInitializeRejectsForeignAttempt 校验跨任务链的尝试被拒绝。
func TestInitializeRejectsForeignAttempt(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	ctx := context.Background()
	request := fixture.request()
	request.AttemptID = domain.NewID().String()
	_, err := fixture.service.Initialize(ctx, request)
	if !errors.Is(err, app.ErrCodeInitNotFound) {
		t.Fatalf("不存在的尝试必须返回不存在错误: %v", err)
	}
	if fixture.containers.calls != 0 {
		t.Fatal("归属校验失败时不得执行任何代码获取")
	}
}

// TestInitializeRejectsUnknownStep 校验步骤归属不符被拒绝。
func TestInitializeRejectsUnknownStep(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	ctx := context.Background()
	request := fixture.request()
	request.StepID = domain.NewID().String()
	_, err := fixture.service.Initialize(ctx, request)
	if !errors.Is(err, app.ErrCodeInitNotFound) {
		t.Fatalf("不存在的步骤必须返回不存在错误: %v", err)
	}
}

// TestInitializeRequiresRunningContainer 校验容器未运行时的前置条件错误。
func TestInitializeRequiresRunningContainer(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	fixture.containers.running = false
	_, err := fixture.service.Initialize(context.Background(), fixture.request())
	if !errors.Is(err, app.ErrCodeInitPrerequisite) {
		t.Fatalf("容器未运行必须返回前置条件错误: %v", err)
	}
	if fixture.containers.calls != 0 {
		t.Fatal("容器不可用时不得执行代码获取")
	}
}

// TestInitializeRejectsDetachedContainer 校验已脱离管理的容器被拒绝。
func TestInitializeRejectsDetachedContainer(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	fixture.containers.detached = true
	_, err := fixture.service.Initialize(context.Background(), fixture.request())
	if !errors.Is(err, app.ErrCodeInitPrerequisite) {
		t.Fatalf("已脱离管理的容器必须返回前置条件错误: %v", err)
	}
	if fixture.containers.calls != 0 {
		t.Fatal("容器不可用时不得执行代码获取")
	}
}

// TestInitializeRequiresContainerAttemptBinding 校验容器必须登记本次尝试。
func TestInitializeRequiresContainerAttemptBinding(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	// 把容器记录的尝试归属改成其他尝试，模拟准备阶段写入了不同尝试。
	fixture.containers.attemptID = domain.NewID()
	_, err := fixture.service.Initialize(context.Background(), fixture.request())
	if !errors.Is(err, app.ErrCodeInitPrerequisite) {
		t.Fatalf("容器尝试归属不符必须返回前置条件错误: %v", err)
	}
	if fixture.containers.calls != 0 {
		t.Fatal("容器尝试归属不符时不得执行代码获取")
	}
}

// TestInitializeRequiresContainerAttemptRegistered 校验容器未登记尝试时被拒绝。
func TestInitializeRequiresContainerAttemptRegistered(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	// 空值表示容器记录没有 attempt_id：无法证明它属于本次执行。
	fixture.containers.attemptID = "-"
	fixture.containers.omitAttemptID = true
	_, err := fixture.service.Initialize(context.Background(), fixture.request())
	if !errors.Is(err, app.ErrCodeInitPrerequisite) {
		t.Fatalf("容器未登记尝试归属必须返回前置条件错误: %v", err)
	}
	if fixture.containers.calls != 0 {
		t.Fatal("容器未登记尝试归属时不得执行代码获取")
	}
}

// TestInitializeSkipsAlreadyInitializedAttempt 校验重复初始化不会重新 Clone。
func TestInitializeSkipsAlreadyInitializedAttempt(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	ctx := context.Background()
	first, err := fixture.service.Initialize(ctx, fixture.request())
	if err != nil {
		t.Fatalf("首次初始化失败: %v", err)
	}
	second, err := fixture.service.Initialize(ctx, fixture.request())
	if err != nil {
		t.Fatalf("重复初始化失败: %v", err)
	}
	if second.Status != domain.CodeFetchStatusSkipped {
		t.Fatalf("重复初始化必须记录 skipped，实际 %s", second.Status)
	}
	if !second.Succeeded {
		t.Fatal("skipped 必须报告代码已就绪")
	}
	if second.Commit != first.Commit {
		t.Fatalf("跳过的记录必须沿用上次 commit: %s", second.Commit)
	}
	if fixture.containers.calls != 1 {
		t.Fatalf("重复初始化不得再次执行代码获取，实际调用 %d 次", fixture.containers.calls)
	}
}

// TestInitializeFailureRecordsStage 校验失败记录保留阶段与原因。
func TestInitializeFailureRecordsStage(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	fixture.containers.fetchErr = sourceError(source.CodeRepositoryAccessFailed, "clone", "无法访问仓库")
	fixture.containers.fetched = source.FetchResult{
		Stage:        domain.CodeFetchStageClone,
		ErrorCode:    source.CodeRepositoryAccessFailed.String(),
		ErrorSummary: "无法访问仓库",
	}
	ctx := context.Background()
	result, err := fixture.service.Initialize(ctx, fixture.request())
	if err == nil {
		t.Fatal("获取失败必须返回错误")
	}
	if result.Succeeded {
		t.Fatal("失败的初始化不得报告代码已就绪")
	}
	if result.Status != domain.CodeFetchStatusFailed {
		t.Fatalf("状态不符: %s", result.Status)
	}
	records, listErr := fixture.db.ListCodeFetchRecordsByAttempt(ctx, fixture.attempt.ID.String())
	if listErr != nil {
		t.Fatalf("读取代码初始化记录失败: %v", listErr)
	}
	if len(records) != 1 {
		t.Fatalf("失败也必须留下记录，实际 %d 条", len(records))
	}
	if records[0].Status != domain.CodeFetchStatusFailed {
		t.Fatalf("记录状态不符: %s", records[0].Status)
	}
	if records[0].Stage == "" {
		t.Fatal("失败记录必须保留失败阶段")
	}
	if records[0].ErrorCode != source.CodeRepositoryAccessFailed.String() {
		t.Fatalf("错误类别不符: %s", records[0].ErrorCode)
	}
}

// TestInitializeTimeoutMapsToTimedOut 校验超时映射为 timed_out。
func TestInitializeTimeoutMapsToTimedOut(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	fixture.containers.fetchErr = sourceError(source.CodeCloneTimeout, "git clone", "Clone 超时")
	fixture.containers.fetched = source.FetchResult{
		Stage:        domain.CodeFetchStageClone,
		ErrorCode:    source.CodeCloneTimeout.String(),
		ErrorSummary: "Clone 超时",
	}
	result, err := fixture.service.Initialize(context.Background(), fixture.request())
	if err == nil {
		t.Fatal("超时必须返回错误")
	}
	if result.Status != domain.CodeFetchStatusTimedOut {
		t.Fatalf("状态不符: %s", result.Status)
	}
}

// TestInitializeCancelMapsToCancelled 校验取消映射为 cancelled。
func TestInitializeCancelMapsToCancelled(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	fixture.containers.fetchErr = sourceError(source.CodeCloneInterrupted, "git clone", "Clone 被取消")
	fixture.containers.fetched = source.FetchResult{
		Stage:        domain.CodeFetchStageClone,
		ErrorCode:    source.CodeCloneInterrupted.String(),
		ErrorSummary: "Clone 被取消",
	}
	result, err := fixture.service.Initialize(context.Background(), fixture.request())
	if err == nil {
		t.Fatal("取消必须返回错误")
	}
	if result.Status != domain.CodeFetchStatusCancelled {
		t.Fatalf("状态不符: %s", result.Status)
	}
}

// TestInitializeUncertainMapsToUncertain 校验结果不确定映射为 uncertain。
func TestInitializeUncertainMapsToUncertain(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	fixture.containers.fetchErr = sourceError(source.CodeStateUncertain, "git clone", "结果无法确认")
	fixture.containers.fetched = source.FetchResult{
		Stage:        domain.CodeFetchStageClone,
		ErrorCode:    source.CodeStateUncertain.String(),
		ErrorSummary: "结果无法确认",
	}
	result, err := fixture.service.Initialize(context.Background(), fixture.request())
	if err == nil {
		t.Fatal("结果不确定必须返回错误")
	}
	if result.Status != domain.CodeFetchStatusUncertain {
		t.Fatalf("状态不符: %s", result.Status)
	}
}

// TestInitializeRejectsInvalidInput 校验参数校验。
func TestInitializeRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	cases := []app.CodeInitRequest{
		{TaskID: "", StepID: "step", AttemptID: "attempt"},
		{TaskID: "task", StepID: "", AttemptID: "attempt"},
		{TaskID: "task", StepID: "step", AttemptID: ""},
		{TaskID: "task/1", StepID: "step", AttemptID: "attempt"},
	}
	for _, request := range cases {
		_, err := fixture.service.Initialize(context.Background(), request)
		if !errors.Is(err, app.ErrCodeInitInvalidInput) {
			t.Fatalf("请求 %+v 应当被拒绝: %v", request, err)
		}
	}
	if fixture.containers.calls != 0 {
		t.Fatal("参数校验失败时不得执行代码获取")
	}
}

// TestInitializeRejectsInvalidWorkDir 校验非法目标目录被拒绝。
func TestInitializeRejectsInvalidWorkDir(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	request := fixture.request()
	request.WorkDir = "/root/project"
	_, err := fixture.service.Initialize(context.Background(), request)
	if !errors.Is(err, app.ErrCodeInitInvalidInput) {
		t.Fatalf("非法目标目录必须返回参数错误: %v", err)
	}
	if fixture.containers.calls != 0 {
		t.Fatal("目标目录非法时不得执行代码获取")
	}
}

// TestInitializeRejectsMissingContainer 校验任务没有容器时的前置条件错误。
func TestInitializeRejectsMissingContainer(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	ctx := context.Background()
	task, err := fixture.db.GetTask(ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	task.ContainerID = ""
	if err := fixture.db.UpdateTask(ctx, *task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	_, initErr := fixture.service.Initialize(ctx, fixture.request())
	if !errors.Is(initErr, app.ErrCodeInitPrerequisite) {
		t.Fatalf("缺少容器必须返回前置条件错误: %v", initErr)
	}
	if fixture.containers.calls != 0 {
		t.Fatal("缺少容器时不得执行代码获取")
	}
}

// TestInitializePersistsEvent 校验初始化结果写入任务时间线。
func TestInitializePersistsEvent(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	ctx := context.Background()
	if _, err := fixture.service.Initialize(ctx, fixture.request()); err != nil {
		t.Fatalf("初始化代码失败: %v", err)
	}
	events, err := fixture.db.ListEventsByTask(ctx, fixture.task.ID, storageEventFilter())
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type == domain.EventTypeSourceFetched {
			found = true
		}
	}
	if !found {
		t.Fatal("代码初始化必须写入 source.fetched 事件")
	}
}

// TestInitializeRejectsCancelledTask 校验已请求取消的任务被拒绝。
func TestInitializeRejectsCancelledTask(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	ctx := context.Background()
	task, err := fixture.db.GetTask(ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if err := task.RequestCancel(time.Now().UTC()); err != nil {
		t.Fatalf("记录取消请求失败: %v", err)
	}
	if err := fixture.db.UpdateTask(ctx, *task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	_, initErr := fixture.service.Initialize(ctx, fixture.request())
	if !errors.Is(initErr, app.ErrCodeInitConflict) {
		t.Fatalf("已请求取消的任务必须返回状态冲突: %v", initErr)
	}
	if fixture.containers.calls != 0 {
		t.Fatal("状态冲突时不得执行代码获取")
	}
}

// TestInitializeKeepsWorkDirUnderWorkspace 校验目标目录始终位于容器工作区之下。
func TestInitializeKeepsWorkDirUnderWorkspace(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitFixture(t)
	request := fixture.request()
	request.WorkDir = "/workspace/project/custom"
	if _, err := fixture.service.Initialize(context.Background(), request); err != nil {
		t.Fatalf("初始化代码失败: %v", err)
	}
	if len(fixture.containers.requests) != 1 {
		t.Fatalf("请求数不符: %d", len(fixture.containers.requests))
	}
	if fixture.containers.requests[0].WorkDir != "/workspace/project/custom" {
		t.Fatalf("目标目录不符: %s", fixture.containers.requests[0].WorkDir)
	}
	if !strings.HasPrefix(fixture.containers.requests[0].WorkDir, domain.WorkspaceContainerRoot) {
		t.Fatal("目标目录必须位于容器工作区之下")
	}
}

// ptrToID 返回标识的指针。
func ptrToID(id domain.ID) *domain.ID {
	return &id
}

// storageAttemptRequest 构造一次创建执行尝试的请求。
func storageAttemptRequest(taskID, stepID domain.ID) storage.StartAttemptRequest {
	return storage.StartAttemptRequest{
		TaskID: taskID,
		StepID: stepID,
		At:     time.Now().UTC(),
	}
}

// storageEventFilter 返回读取任务全部事件的筛选条件。
func storageEventFilter() storage.EventFilter {
	return storage.EventFilter{}
}

// sourceError 构造带类别的代码获取错误。
//
// 直接使用 source.Error 的导出字段：测试必须与真实执行层使用同一个错误类型，
// 否则类别映射逻辑会被一个只在测试中存在的实现绕过。
func sourceError(code source.ErrorCode, op, detail string) error {
	return &source.Error{Code: code, Op: op, Detail: detail}
}
