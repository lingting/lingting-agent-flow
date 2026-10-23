// 任务级工作区初始化协调器的单元测试。
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
	workspaceContainerID = "container-workspace"
	workspaceDockerID    = "docker-container-workspace"
	workspaceBaseSHA     = "1111111111111111111111111111111111111111"
	workspaceHeadSHA     = "2222222222222222222222222222222222222222"
)

// workspaceContainers 是容器能力的受控替身。
type workspaceContainers struct {
	mu sync.Mutex
	// inspectErr 使容器核验失败。
	inspectErr error
	// running 表示容器是否在运行。
	running bool
	// detached 表示容器已脱离管理。
	detached bool
	// removed 表示容器已被删除。
	removed bool
	// fetchErr 是代码获取返回的错误。
	fetchErr error
	// fetched 是代码获取返回的结果；零值时使用成功默认值。
	fetched source.FetchResult
	// requests 记录收到的代码获取请求。
	requests []source.FetchRequest
	// calls 统计代码获取次数。
	calls int
}

// InspectContainer 返回受控的容器状态。
func (c *workspaceContainers) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	if c.inspectErr != nil {
		return app.ContainerDetail{}, c.inspectErr
	}
	container := domain.Container{
		ID:       domain.ID(request.ContainerID),
		DockerID: workspaceDockerID,
		Detached: c.detached,
		State:    domain.ContainerStateRunning,
	}
	if c.detached {
		container.State = domain.ContainerStateDetached
	}
	if c.removed {
		container.State = domain.ContainerStateRemoved
	}
	return app.ContainerDetail{
		Container: container,
		Inspect:   &docker.ContainerInspect{Running: c.running},
	}, nil
}

// FetchSourceWith 记录请求并返回受控结果。
func (c *workspaceContainers) FetchSourceWith(
	ctx context.Context,
	containerID string,
	request source.FetchRequest,
) (source.FetchResult, error) {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.calls++
	fetched, fetchErr := c.fetched, c.fetchErr
	c.mu.Unlock()
	if fetchErr != nil {
		return fetchResultOf(fetchErr, request), fetchErr
	}
	if fetched.Commit == "" {
		fetched = source.FetchResult{
			ProjectID:        request.ProjectID,
			TaskID:           request.TaskID,
			AttemptID:        request.AttemptID,
			SourceType:       domain.CodeSourceGit,
			Repository:       "https://git.example.com/group/repo.git",
			ContainerID:      containerID,
			DockerID:         workspaceDockerID,
			WorkDir:          request.WorkDir,
			BaseBranch:       "main",
			BaseSelection:    domain.GitBranchSelectionOriginHead,
			BaseCommit:       workspaceBaseSHA,
			TaskBranch:       "agent/" + request.TaskID,
			TaskBranchSource: domain.TaskBranchSourceNew,
			Commit:           workspaceHeadSHA,
			Stage:            domain.CodeFetchStageVerify,
		}
	}
	now := time.Now().UTC()
	fetched.StartedAt = now
	fetched.FinishedAt = now
	return fetched, nil
}

// workspaceFixture 是一套完整的任务级工作区初始化测试数据。
type workspaceFixture struct {
	db         *sqlite.DB
	ctx        context.Context
	service    *app.TaskWorkspaceService
	containers *workspaceContainers
	task       domain.Task
	host       domain.ExecutionHost
}

// newWorkspaceFixture 构造任务级工作区初始化测试数据。
//
// 任务此时没有任何 Step / Attempt：这正是任务级初始化与尝试级初始化的区别。
func newWorkspaceFixture(t *testing.T) *workspaceFixture {
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
	task.ContainerID = workspaceContainerID
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
	containers := &workspaceContainers{running: true}
	service, err := app.NewTaskWorkspaceService(app.TaskWorkspaceOptions{
		Store:      db,
		Containers: containers,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造工作区初始化服务失败: %v", err)
	}
	return &workspaceFixture{
		db:         db,
		ctx:        ctx,
		service:    service,
		containers: containers,
		task:       task,
		host:       host,
	}
}

// request 返回一次指向当前测试任务的初始化请求。
func (f *workspaceFixture) request() app.TaskWorkspaceRequest {
	return app.TaskWorkspaceRequest{TaskID: f.task.ID.String()}
}

// TestInitializeTaskWorkspaceSucceeds 校验成功的初始化写入完整记录。
func TestInitializeTaskWorkspaceSucceeds(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	result, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("初始化失败: %v", err)
	}
	if result.Status != domain.TaskWorkspaceInitStatusSucceeded {
		t.Fatalf("状态应为 succeeded，实际 %s", result.Status)
	}
	if !result.Succeeded {
		t.Fatal("成功初始化必须报告工作区已就绪")
	}
	if result.Commit != workspaceHeadSHA {
		t.Fatalf("commit 不符: %s", result.Commit)
	}
	if !result.Persisted {
		t.Fatalf("记录应已写入: %v", result.PersistenceErrors)
	}
	if fixture.containers.calls != 1 {
		t.Fatalf("代码获取应调用 1 次，实际 %d", fixture.containers.calls)
	}
	// 任务级初始化不得伪造尝试归属：请求里的 AttemptID 必须为空。
	if attemptID := fixture.containers.requests[0].AttemptID; attemptID != "" {
		t.Fatalf("任务级初始化不得携带尝试归属，实际 %q", attemptID)
	}
	if fixture.containers.requests[0].TaskID != fixture.task.ID.String() {
		t.Fatalf("任务标识不符: %s", fixture.containers.requests[0].TaskID)
	}
	stored, err := fixture.db.ListTaskWorkspaceInitsByTask(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("读取记录失败: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("记录数应为 1，实际 %d", len(stored))
	}
	if stored[0].ContainerID != workspaceContainerID {
		t.Fatalf("容器代次判据不符: %s", stored[0].ContainerID)
	}
}

// TestInitializeTaskWorkspaceSkipsSameContainer 校验同容器重复初始化只记录 skipped。
func TestInitializeTaskWorkspaceSkipsSameContainer(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	if _, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request()); err != nil {
		t.Fatalf("首次初始化失败: %v", err)
	}
	second, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("重复初始化失败: %v", err)
	}
	if second.Status != domain.TaskWorkspaceInitStatusSkipped {
		t.Fatalf("状态应为 skipped，实际 %s", second.Status)
	}
	if fixture.containers.calls != 1 {
		t.Fatalf("已就绪时不得重新 Clone，实际调用 %d 次", fixture.containers.calls)
	}
	if second.Commit != workspaceHeadSHA {
		t.Fatalf("skipped 记录应沿用上次核验的 commit: %s", second.Commit)
	}
}

// TestInitializeTaskWorkspaceReinitializesAfterContainerChange 校验换容器后重新初始化。
func TestInitializeTaskWorkspaceReinitializesAfterContainerChange(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	if _, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request()); err != nil {
		t.Fatalf("首次初始化失败: %v", err)
	}
	// 模拟任务重新开始：任务关联到新容器。
	stored, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if setErr := stored.SetContainer("container-new"); setErr != nil {
		t.Fatalf("写入容器关联失败: %v", setErr)
	}
	if updateErr := fixture.db.UpdateTaskIfStatus(
		fixture.ctx,
		*stored,
		[]domain.TaskStatus{stored.Status},
	); updateErr != nil {
		t.Fatalf("更新任务失败: %v", updateErr)
	}
	second, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("换容器后初始化失败: %v", err)
	}
	if second.Status != domain.TaskWorkspaceInitStatusSucceeded {
		t.Fatalf("换容器后应重新初始化，实际状态 %s", second.Status)
	}
	if fixture.containers.calls != 2 {
		t.Fatalf("换容器后必须重新获取代码，实际调用 %d 次", fixture.containers.calls)
	}
	records, listErr := fixture.db.ListTaskWorkspaceInitsByTask(fixture.ctx, fixture.task.ID.String())
	if listErr != nil {
		t.Fatalf("读取记录失败: %v", listErr)
	}
	if len(records) != 2 {
		t.Fatalf("历史记录必须保留，实际 %d 条", len(records))
	}
}

// TestInitializeTaskWorkspaceRejectsUncertainWithoutReplay 校验不确定记录阻止重放。
func TestInitializeTaskWorkspaceRejectsUncertainWithoutReplay(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	fixture.containers.fetchErr = &workspaceFetchError{code: source.CodeStateUncertain}
	first, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request())
	if !errors.Is(err, app.ErrTaskWorkspaceUncertain) {
		t.Fatalf("应返回结果不确定，实际 %v", err)
	}
	if first.Status != domain.TaskWorkspaceInitStatusUncertain {
		t.Fatalf("状态应为 uncertain，实际 %s", first.Status)
	}
	// 现场被占用：再次初始化必须被数据库唯一索引拒绝，不能重放 Clone。
	fixture.containers.fetchErr = nil
	_, secondErr := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request())
	if !errors.Is(secondErr, app.ErrTaskWorkspaceNotPersisted) {
		t.Fatalf("不确定记录必须阻止重复初始化，实际 %v", secondErr)
	}
	if fixture.containers.calls != 1 {
		t.Fatalf("不得重放代码获取，实际调用 %d 次", fixture.containers.calls)
	}
}

// TestInitializeTaskWorkspaceRequiresRunningContainer 校验容器未运行时拒绝执行。
func TestInitializeTaskWorkspaceRequiresRunningContainer(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	fixture.containers.running = false
	_, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request())
	if !errors.Is(err, app.ErrTaskWorkspacePrerequisite) {
		t.Fatalf("应返回前置条件缺失，实际 %v", err)
	}
	if fixture.containers.calls != 0 {
		t.Fatalf("容器未运行时不得执行代码获取，实际 %d 次", fixture.containers.calls)
	}
}

// TestInitializeTaskWorkspaceRejectsDetachedContainer 校验已脱离管理的容器不被接管。
func TestInitializeTaskWorkspaceRejectsDetachedContainer(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	fixture.containers.detached = true
	_, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request())
	if !errors.Is(err, app.ErrTaskWorkspacePrerequisite) {
		t.Fatalf("应返回前置条件缺失，实际 %v", err)
	}
	if fixture.containers.calls != 0 {
		t.Fatalf("不得在已 detached 容器上执行，实际 %d 次", fixture.containers.calls)
	}
}

// TestInitializeTaskWorkspaceRejectsRemovedContainer 校验已删除的容器被拒绝。
func TestInitializeTaskWorkspaceRejectsRemovedContainer(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	fixture.containers.removed = true
	_, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request())
	if !errors.Is(err, app.ErrTaskWorkspacePrerequisite) {
		t.Fatalf("应返回前置条件缺失，实际 %v", err)
	}
}

// TestInitializeTaskWorkspaceRequiresContainer 校验任务缺少容器时返回前置条件错误。
func TestInitializeTaskWorkspaceRequiresContainer(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	stored, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if setErr := stored.SetContainer(""); setErr != nil {
		t.Fatalf("清空容器关联失败: %v", setErr)
	}
	if updateErr := fixture.db.UpdateTaskIfStatus(
		fixture.ctx,
		*stored,
		[]domain.TaskStatus{stored.Status},
	); updateErr != nil {
		t.Fatalf("更新任务失败: %v", updateErr)
	}
	_, initErr := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request())
	if !errors.Is(initErr, app.ErrTaskWorkspacePrerequisite) {
		t.Fatalf("应返回前置条件缺失，实际 %v", initErr)
	}
}

// TestInitializeTaskWorkspaceRecordsFailureStage 校验失败记录带阶段与原因。
func TestInitializeTaskWorkspaceRecordsFailureStage(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	fixture.containers.fetchErr = &workspaceFetchError{
		code:    source.CodeTargetDirNotEmpty,
		stage:   domain.CodeFetchStageProbe,
		summary: "目标目录非空",
	}
	result, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request())
	if !errors.Is(err, app.ErrTaskWorkspaceFailed) {
		t.Fatalf("应返回执行失败，实际 %v", err)
	}
	if result.Status != domain.TaskWorkspaceInitStatusFailed {
		t.Fatalf("状态应为 failed，实际 %s", result.Status)
	}
	if result.Stage != domain.CodeFetchStageProbe {
		t.Fatalf("失败阶段不符: %s", result.Stage)
	}
	if result.Failure == "" {
		t.Fatal("失败记录必须带原因")
	}
	if !result.Persisted {
		t.Fatalf("失败记录同样必须落库: %v", result.PersistenceErrors)
	}
}

// TestInitializeTaskWorkspaceMapsTimeout 校验超时映射为 timed_out。
func TestInitializeTaskWorkspaceMapsTimeout(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	fixture.containers.fetchErr = &workspaceFetchError{
		code:  source.CodeCloneTimeout,
		stage: domain.CodeFetchStageClone,
	}
	result, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request())
	if !errors.Is(err, app.ErrTaskWorkspaceFailed) {
		t.Fatalf("应返回执行失败，实际 %v", err)
	}
	if result.Status != domain.TaskWorkspaceInitStatusTimedOut {
		t.Fatalf("状态应为 timed_out，实际 %s", result.Status)
	}
}

// TestInitializeTaskWorkspaceMapsCancel 校验取消映射为 cancelled。
func TestInitializeTaskWorkspaceMapsCancel(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	fixture.containers.fetchErr = &workspaceFetchError{
		code:  source.CodeCloneInterrupted,
		stage: domain.CodeFetchStageClone,
	}
	result, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request())
	if !errors.Is(err, app.ErrTaskWorkspaceFailed) {
		t.Fatalf("应返回执行失败，实际 %v", err)
	}
	if result.Status != domain.TaskWorkspaceInitStatusCancelled {
		t.Fatalf("状态应为 cancelled，实际 %s", result.Status)
	}
}

// TestInitializeTaskWorkspaceRejectsInvalidInput 校验参数校验。
func TestInitializeTaskWorkspaceRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	if _, err := fixture.service.InitializeTaskWorkspace(
		fixture.ctx,
		app.TaskWorkspaceRequest{TaskID: ""},
	); !errors.Is(err, app.ErrTaskWorkspaceInvalidInput) {
		t.Fatalf("空任务标识应被拒绝，实际 %v", err)
	}
	if _, err := fixture.service.InitializeTaskWorkspace(
		fixture.ctx,
		app.TaskWorkspaceRequest{TaskID: "a/b"},
	); !errors.Is(err, app.ErrTaskWorkspaceInvalidInput) {
		t.Fatalf("含斜线的任务标识应被拒绝，实际 %v", err)
	}
	if _, err := fixture.service.InitializeTaskWorkspace(
		fixture.ctx,
		app.TaskWorkspaceRequest{TaskID: fixture.task.ID.String(), Timeout: 2 * time.Hour},
	); !errors.Is(err, app.ErrTaskWorkspaceInvalidInput) {
		t.Fatalf("超上限的超时应被拒绝，实际 %v", err)
	}
}

// TestInitializeTaskWorkspaceRejectsUnknownTask 校验未知任务返回不存在。
func TestInitializeTaskWorkspaceRejectsUnknownTask(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	_, err := fixture.service.InitializeTaskWorkspace(
		fixture.ctx,
		app.TaskWorkspaceRequest{TaskID: domain.NewID().String()},
	)
	if !errors.Is(err, app.ErrTaskWorkspaceNotFound) {
		t.Fatalf("应返回目标不存在，实际 %v", err)
	}
}

// TestInitializeTaskWorkspaceKeepsWorkDirUnderWorkspace 校验工作目录边界。
func TestInitializeTaskWorkspaceKeepsWorkDirUnderWorkspace(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	_, err := fixture.service.InitializeTaskWorkspace(
		fixture.ctx,
		app.TaskWorkspaceRequest{TaskID: fixture.task.ID.String(), WorkDir: "/tmp/outside"},
	)
	if !errors.Is(err, app.ErrTaskWorkspaceInvalidInput) {
		t.Fatalf("工作区之外的目录应被拒绝，实际 %v", err)
	}
	if fixture.containers.calls != 0 {
		t.Fatalf("参数非法时不得执行代码获取，实际 %d 次", fixture.containers.calls)
	}
}

// TestInitializeTaskWorkspacePersistsEvent 校验初始化写入时间线事件。
func TestInitializeTaskWorkspacePersistsEvent(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	if _, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request()); err != nil {
		t.Fatalf("初始化失败: %v", err)
	}
	events, err := fixture.db.ListEventsByTask(
		fixture.ctx,
		fixture.task.ID,
		storage.EventFilter{},
	)
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type == domain.EventTypeSourceFetched {
			found = true
			if !strings.Contains(string(event.Payload), "task_workspace") {
				t.Fatalf("事件负载应标注任务级范围: %s", event.Payload)
			}
		}
	}
	if !found {
		t.Fatal("必须写入代码获取事件")
	}
}

// TestRecoverInterruptedConvergesToUncertain 校验中断记录收敛为不确定。
func TestRecoverInterruptedConvergesToUncertain(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	// 直接写入一条停留在 running 的记录，模拟进程在 Git 操作期间退出。
	record, err := domain.NewTaskWorkspaceInitRecord(
		"",
		fixture.task.ID.String(),
		workspaceContainerID,
		workspaceDockerID,
		domain.CodeSourceGit,
		domain.DefaultContainerWorkDir,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造记录失败: %v", err)
	}
	if markErr := record.MarkRunning(domain.CodeFetchStageClone, time.Now().UTC()); markErr != nil {
		t.Fatalf("标记运行失败: %v", markErr)
	}
	if createErr := fixture.db.CreateTaskWorkspaceInit(fixture.ctx, record); createErr != nil {
		t.Fatalf("写入记录失败: %v", createErr)
	}
	if recoverErr := fixture.service.RecoverInterrupted(fixture.ctx); recoverErr != nil {
		t.Fatalf("收敛失败: %v", recoverErr)
	}
	stored, err := fixture.db.GetTaskWorkspaceInit(fixture.ctx, record.ID)
	if err != nil {
		t.Fatalf("读取记录失败: %v", err)
	}
	if stored.Status != domain.TaskWorkspaceInitStatusUncertain {
		t.Fatalf("应收敛为 uncertain，实际 %s", stored.Status)
	}
	if stored.ErrorSummary == "" {
		t.Fatal("收敛后必须写入可读摘要")
	}
	if fixture.containers.calls != 0 {
		t.Fatalf("收敛不得重放代码获取，实际调用 %d 次", fixture.containers.calls)
	}
}

// TestRecoverInterruptedKeepsTerminalRecords 校验终态记录不被改写。
func TestRecoverInterruptedKeepsTerminalRecords(t *testing.T) {
	t.Parallel()
	fixture := newWorkspaceFixture(t)
	if _, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, fixture.request()); err != nil {
		t.Fatalf("初始化失败: %v", err)
	}
	if err := fixture.service.RecoverInterrupted(fixture.ctx); err != nil {
		t.Fatalf("收敛失败: %v", err)
	}
	records, err := fixture.db.ListTaskWorkspaceInitsByTask(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("读取记录失败: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("记录数应为 1，实际 %d", len(records))
	}
	if records[0].Status != domain.TaskWorkspaceInitStatusSucceeded {
		t.Fatalf("终态记录不得被改写，实际 %s", records[0].Status)
	}
}

// workspaceFetchError 是带源码错误类别的受控错误。
//
// 它直接构造 source.Error，使状态映射走真实分支而不是字符串匹配。
type workspaceFetchError struct {
	code    source.ErrorCode
	stage   domain.CodeFetchStage
	summary string
}

// Error 返回错误信息。
func (e *workspaceFetchError) Error() string {
	if e.summary != "" {
		return e.summary
	}
	return "代码获取失败: " + e.code.String()
}

// Unwrap 返回 source 层错误，供 source.ErrorCodeOf 识别。
func (e *workspaceFetchError) Unwrap() error {
	return &source.Error{Code: e.code, Op: "fetch", Detail: e.Error()}
}

// fetchResultOf 把受控错误转换为代码获取结果。
//
// 真实的 source 实现在失败时同样返回带阶段与错误类别填充的结果，替身必须保持一致，
// 否则状态映射与失败阶段断言会偏离产品行为。
func fetchResultOf(err error, request source.FetchRequest) source.FetchResult {
	controlled, ok := err.(*workspaceFetchError)
	if !ok {
		return source.FetchResult{}
	}
	return source.FetchResult{
		TaskID:       request.TaskID,
		SourceType:   domain.CodeSourceGit,
		ContainerID:  request.ContainerID,
		WorkDir:      request.WorkDir,
		Stage:        controlled.stage,
		ErrorCode:    controlled.code.String(),
		ErrorSummary: controlled.Error(),
	}
}

// 编译期确认替身满足端口契约。
var (
	_ app.TaskWorkspaceContainerPort = (*workspaceContainers)(nil)
	_ app.TaskWorkspaceStore         = (*sqlite.DB)(nil)
)
