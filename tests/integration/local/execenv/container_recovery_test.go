// 容器残留收敛的集成测试。
//
// 用例使用真实 SQLite 与正式的 app.ContainerRecoveryService：断言对象是数据库中的
// 真实容器记录，而不是方法的返回值。远程观测由受控端口提供，因此本文件验证的是
// "收敛规则 + 持久化路径"，**不代表**真实远程 Docker 已验收（真实远程用例在
// tests/integration/remote/ 下，需要 ssh.enable 与 docker.remote_enabled）。
package execenv_local

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
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// containerRecoveryFixture 是一套容器残留收敛的持久化现场。
type containerRecoveryFixture struct {
	ctx        context.Context
	db         *sqlite.DB
	host       domain.ExecutionHost
	logger     *logging.Logger
	containers *recoveryContainers
	service    *app.ContainerRecoveryService
	now        time.Time
}

// newContainerRecoveryFixture 打开真实数据库并装配收敛服务。
func newContainerRecoveryFixture(t *testing.T) *containerRecoveryFixture {
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
	fixture := &containerRecoveryFixture{
		ctx:    ctx,
		db:     db,
		logger: logger,
		now:    time.Now().UTC(),
	}
	fixture.host = fixture.createHost(t)
	fixture.containers = &recoveryContainers{details: make(map[string]app.ContainerDetail)}
	fixture.service = fixture.buildService(t, fixture.containers)
	return fixture
}

// createHost 写入一个执行节点记录。
func (f *containerRecoveryFixture) createHost(t *testing.T) domain.ExecutionHost {
	t.Helper()
	host, err := domain.NewExecutionHost(
		"",
		"容器恢复节点",
		"10.0.0.60",
		22,
		"deploy",
		"/srv/agentflow",
		f.now,
		domain.WithPrivateKey(testkit.PrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := f.db.CreateExecutionHost(f.ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	return host
}

// buildService 构造收敛服务。
func (f *containerRecoveryFixture) buildService(
	t *testing.T,
	port app.ContainerRecoveryContainerPort,
) *app.ContainerRecoveryService {
	t.Helper()
	service, err := app.NewContainerRecoveryService(app.ContainerRecoveryOptions{
		Store:      f.db,
		Containers: port,
		Logger:     f.logger.Slog(),
		Now:        func() time.Time { return f.now },
	})
	if err != nil {
		t.Fatalf("构造容器残留收敛服务失败: %v", err)
	}
	return service
}

// createTask 写入一个带容器关联的任务。
func (f *containerRecoveryFixture) createTask(t *testing.T) domain.Task {
	t.Helper()
	requirement, err := domain.NewRequirement("容器恢复需求", "正文", domain.PriorityNormal, f.now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := f.db.CreateRequirement(f.ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "ssh://git.example.com/team/repo", "main", "", f.now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := f.db.CreateTask(f.ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	return task
}

// createContainer 写入一条状态不明的容器记录。
func (f *containerRecoveryFixture) createContainer(
	t *testing.T,
	taskID *domain.ID,
	name string,
) domain.Container {
	t.Helper()
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	container, err := domain.NewContainer(
		domain.NewID(),
		f.host.ID,
		name,
		ref,
		"v0001",
		strings.Repeat("a", 64),
		"sha256:"+strings.Repeat("b", 64),
		f.now,
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	container.TaskID = taskID
	container.DockerID = "docker-" + name
	if err := f.db.CreateContainer(f.ctx, container); err != nil {
		t.Fatalf("写入容器记录失败: %v", err)
	}
	if err := container.MarkUnknown("远程状态无法确认", f.now); err != nil {
		t.Fatalf("标记状态不明失败: %v", err)
	}
	if err := f.db.UpdateContainer(f.ctx, container); err != nil {
		t.Fatalf("更新容器记录失败: %v", err)
	}
	return container
}

// reloadContainer 读取数据库中的容器记录。
func (f *containerRecoveryFixture) reloadContainer(
	t *testing.T,
	id domain.ID,
) domain.Container {
	t.Helper()
	container, err := f.db.GetContainer(f.ctx, id)
	if err != nil {
		t.Fatalf("读取容器记录失败: %v", err)
	}
	return *container
}

// linkTaskContainer 让任务引用指定容器，避免该容器同时被判定为孤儿。
func (f *containerRecoveryFixture) linkTaskContainer(
	t *testing.T,
	task domain.Task,
	containerID domain.ID,
) {
	t.Helper()
	updated := task
	updated.ContainerID = containerID.String()
	if err := f.db.UpdateTask(f.ctx, updated); err != nil {
		t.Fatalf("更新任务容器关联失败: %v", err)
	}
}

// recoveryContainers 是容器只读观测的受控替身。
type recoveryContainers struct {
	details map[string]app.ContainerDetail
	errs    map[string]error
}

// InspectContainer 返回受控的观测结果。
func (c *recoveryContainers) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	if err, ok := c.errs[request.ContainerID]; ok {
		return app.ContainerDetail{}, err
	}
	detail, ok := c.details[request.ContainerID]
	if !ok {
		return app.ContainerDetail{}, errors.New("容器不存在")
	}
	return detail, nil
}

// runningDetail 构造"确认存在且运行中"的观测结果。
func runningDetail(container domain.Container) app.ContainerDetail {
	return app.ContainerDetail{
		Container: container,
		Inspect: &docker.ContainerInspect{
			ID:      container.DockerID,
			Name:    container.Name,
			State:   "running",
			Running: true,
		},
	}
}

// TestContainerRecoveryConvergesUnknownToRunning 验证状态不明收敛为运行中。
func TestContainerRecoveryConvergesUnknownToRunning(t *testing.T) {
	t.Parallel()
	fixture := newContainerRecoveryFixture(t)
	task := fixture.createTask(t)
	container := fixture.createContainer(t, &task.ID, "agentflow-recover1")
	fixture.containers.details[container.ID.String()] = runningDetail(container)

	if err := fixture.service.Start(fixture.ctx); err != nil {
		t.Fatalf("启动收敛服务失败: %v", err)
	}
	stored := fixture.reloadContainer(t, container.ID)
	if stored.State != domain.ContainerStateRunning {
		t.Fatalf("容器应收敛为 running，实际 %s", stored.State)
	}
	if stored.ErrorSummary != "" {
		t.Fatalf("收敛成功后应清除状态不明摘要，实际 %q", stored.ErrorSummary)
	}
}

// TestContainerRecoveryConvergesAbsentToRemoved 验证确认不存在收敛为已删除。
func TestContainerRecoveryConvergesAbsentToRemoved(t *testing.T) {
	t.Parallel()
	fixture := newContainerRecoveryFixture(t)
	task := fixture.createTask(t)
	container := fixture.createContainer(t, &task.ID, "agentflow-recover2")
	fixture.containers.errs = map[string]error{
		container.ID.String(): &docker.Error{
			Code: docker.CodeContainerNotFound,
			Op:   "docker inspect",
		},
	}
	if err := fixture.service.Start(fixture.ctx); err != nil {
		t.Fatalf("启动收敛服务失败: %v", err)
	}
	stored := fixture.reloadContainer(t, container.ID)
	if stored.State != domain.ContainerStateRemoved {
		t.Fatalf("容器应收敛为 removed，实际 %s", stored.State)
	}
}

// TestContainerRecoveryKeepsUnknownWhenUnreachable 验证无法访问时保留不确定。
func TestContainerRecoveryKeepsUnknownWhenUnreachable(t *testing.T) {
	t.Parallel()
	fixture := newContainerRecoveryFixture(t)
	task := fixture.createTask(t)
	container := fixture.createContainer(t, &task.ID, "agentflow-recover3")
	fixture.containers.errs = map[string]error{
		container.ID.String(): &docker.Error{
			Code: docker.CodeSSHUnavailable,
			Op:   "docker inspect",
		},
	}
	if err := fixture.service.Start(fixture.ctx); err != nil {
		t.Fatalf("节点不可达不应让启动失败: %v", err)
	}
	stored := fixture.reloadContainer(t, container.ID)
	if stored.State != domain.ContainerStateUnknown {
		t.Fatalf("无法访问时必须保留 unknown，实际 %s", stored.State)
	}
}

// TestContainerRecoveryOwnershipMismatchNeedsManual 验证所有权不匹配不处置。
func TestContainerRecoveryOwnershipMismatchNeedsManual(t *testing.T) {
	t.Parallel()
	fixture := newContainerRecoveryFixture(t)
	task := fixture.createTask(t)
	container := fixture.createContainer(t, &task.ID, "agentflow-recover4")
	fixture.containers.errs = map[string]error{
		container.ID.String(): &docker.Error{
			Code: docker.CodeOwnershipMismatch,
			Op:   "docker inspect",
		},
	}
	if err := fixture.service.Start(fixture.ctx); err != nil {
		t.Fatalf("所有权不匹配不应让启动失败: %v", err)
	}
	stored := fixture.reloadContainer(t, container.ID)
	if stored.State != domain.ContainerStateUnknown {
		t.Fatalf("所有权不匹配时不得改写管理状态，实际 %s", stored.State)
	}
	if stored.State == domain.ContainerStateRemoved {
		t.Fatal("所有权不匹配不得被当作容器不存在而标记删除")
	}
}

// TestContainerRecoveryKeepsDetachedUntouched 验证已脱离管理的容器不被改写。
func TestContainerRecoveryKeepsDetachedUntouched(t *testing.T) {
	t.Parallel()
	fixture := newContainerRecoveryFixture(t)
	task := fixture.createTask(t)
	container := fixture.createContainer(t, &task.ID, "agentflow-recover5")
	if err := container.Detach(fixture.now); err != nil {
		t.Fatalf("脱离管理失败: %v", err)
	}
	if err := fixture.db.UpdateContainer(fixture.ctx, container); err != nil {
		t.Fatalf("更新容器记录失败: %v", err)
	}
	fixture.containers.details[container.ID.String()] = runningDetail(container)

	if err := fixture.service.Start(fixture.ctx); err != nil {
		t.Fatalf("启动收敛服务失败: %v", err)
	}
	stored := fixture.reloadContainer(t, container.ID)
	if stored.State != domain.ContainerStateDetached {
		t.Fatalf("已脱离管理的容器状态不得被改写，实际 %s", stored.State)
	}
	if !stored.Detached {
		t.Fatal("已脱离管理的标记不得被恢复清除")
	}
}

// TestContainerRecoveryIsIdempotentAcrossRestarts 验证重复收敛幂等。
func TestContainerRecoveryIsIdempotentAcrossRestarts(t *testing.T) {
	t.Parallel()
	fixture := newContainerRecoveryFixture(t)
	task := fixture.createTask(t)
	container := fixture.createContainer(t, &task.ID, "agentflow-recover6")
	fixture.containers.details[container.ID.String()] = runningDetail(container)
	// 让任务真正引用该容器：否则容器同时会被当作孤儿容器报告，事件数量无法用于
	// 判断"重复收敛是否重复写事件"。
	fixture.linkTaskContainer(t, task, container.ID)

	for attempt := 0; attempt < 2; attempt++ {
		service := fixture.buildService(t, fixture.containers)
		if err := service.Start(fixture.ctx); err != nil {
			t.Fatalf("第 %d 次启动收敛服务失败: %v", attempt+1, err)
		}
	}
	stored := fixture.reloadContainer(t, container.ID)
	if stored.State != domain.ContainerStateRunning {
		t.Fatalf("重复收敛后状态应保持 running，实际 %s", stored.State)
	}
	// 只应留下一条收敛事件：第二次启动时记录已不是 unknown，不在扫描范围内。
	events, err := fixture.db.ListEventsByTask(fixture.ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	recoverEvents := 0
	for _, event := range events {
		if strings.Contains(string(event.Payload), "recover_container") {
			recoverEvents++
		}
	}
	if recoverEvents != 1 {
		t.Fatalf("重复收敛不应重复写事件，实际 %d 条", recoverEvents)
	}
}

// TestContainerRecoveryReportsOrphanWithoutDeleting 验证孤儿容器只被报告。
func TestContainerRecoveryReportsOrphanWithoutDeleting(t *testing.T) {
	t.Parallel()
	fixture := newContainerRecoveryFixture(t)
	task := fixture.createTask(t)
	// 孤儿容器：登记了任务关联，但任务当前引用的不是它。
	orphan := fixture.createContainer(t, &task.ID, "agentflow-orphan9")
	current := fixture.createContainer(t, &task.ID, "agentflow-current9")
	updated := task
	updated.ContainerID = current.ID.String()
	if err := fixture.db.UpdateTask(fixture.ctx, updated); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	fixture.containers.details[current.ID.String()] = runningDetail(current)

	if err := fixture.service.Start(fixture.ctx); err != nil {
		t.Fatalf("启动收敛服务失败: %v", err)
	}
	stored := fixture.reloadContainer(t, orphan.ID)
	if stored.State == domain.ContainerStateRemoved {
		t.Fatal("孤儿容器不得被自动删除")
	}
	if stored.State != domain.ContainerStateUnknown {
		t.Fatalf("孤儿容器的管理状态不应被其它规则改写，实际 %s", stored.State)
	}
	events, err := fixture.db.ListEventsByTask(fixture.ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	found := false
	for _, event := range events {
		if strings.Contains(string(event.Payload), "recover_container_orphan") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("孤儿容器必须被报告")
	}
}

// TestContainerRecoveryWriteFailureBlocksStartup 验证写入失败阻止启动。
func TestContainerRecoveryWriteFailureBlocksStartup(t *testing.T) {
	t.Parallel()
	fixture := newContainerRecoveryFixture(t)
	task := fixture.createTask(t)
	container := fixture.createContainer(t, &task.ID, "agentflow-recover7")
	fixture.containers.details[container.ID.String()] = runningDetail(container)

	failing := &failingRecoveryStore{DB: fixture.db}
	service, err := app.NewContainerRecoveryService(app.ContainerRecoveryOptions{
		Store:      failing,
		Containers: fixture.containers,
		Logger:     fixture.logger.Slog(),
		Now:        func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("构造收敛服务失败: %v", err)
	}
	if startErr := service.Start(fixture.ctx); startErr == nil {
		t.Fatal("收敛写入失败时必须让启动失败")
	}
	stored := fixture.reloadContainer(t, container.ID)
	if stored.State != domain.ContainerStateUnknown {
		t.Fatalf("写入失败时记录应保持原状，实际 %s", stored.State)
	}
}

// TestContainerRecoveryConflictKeepsNewerRecord 验证并发收敛不覆盖新结论。
func TestContainerRecoveryConflictKeepsNewerRecord(t *testing.T) {
	t.Parallel()
	fixture := newContainerRecoveryFixture(t)
	task := fixture.createTask(t)
	container := fixture.createContainer(t, &task.ID, "agentflow-recover8")
	fixture.containers.details[container.ID.String()] = runningDetail(container)

	// 模拟另一执行者在本服务写入之前已经把记录推进为 running。
	service, err := app.NewContainerRecoveryService(app.ContainerRecoveryOptions{
		Store: &conflictRecoveryStore{
			DB: fixture.db,
			onList: func() {
				newer := container
				if markErr := newer.MarkRunning(fixture.now); markErr == nil {
					_ = fixture.db.UpdateContainer(context.Background(), newer)
				}
			},
		},
		Containers: fixture.containers,
		Logger:     fixture.logger.Slog(),
		Now:        func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("构造收敛服务失败: %v", err)
	}
	if startErr := service.Start(fixture.ctx); startErr != nil {
		t.Fatalf("并发收敛冲突不应让启动失败: %v", startErr)
	}
	stored := fixture.reloadContainer(t, container.ID)
	if stored.State != domain.ContainerStateRunning {
		t.Fatalf("并发写入的结论必须被保留，实际 %s", stored.State)
	}
}

// failingRecoveryStore 让容器条件写入失败。
type failingRecoveryStore struct {
	*sqlite.DB
}

// UpdateContainerIfState 始终返回错误。
func (s *failingRecoveryStore) UpdateContainerIfState(
	ctx context.Context,
	container domain.Container,
	expected []domain.ContainerState,
) error {
	return errors.New("数据库写入失败")
}

// conflictRecoveryStore 在列出记录后立刻让另一个执行者推进记录。
type conflictRecoveryStore struct {
	*sqlite.DB
	onList func()
}

// ListContainers 在返回结果前触发外部推进。
func (s *conflictRecoveryStore) ListContainers(
	ctx context.Context,
	filter domain.ContainerFilter,
) ([]domain.Container, error) {
	containers, err := s.DB.ListContainers(ctx, filter)
	if err != nil {
		return nil, err
	}
	if s.onList != nil {
		callback := s.onList
		s.onList = nil
		callback()
	}
	return containers, nil
}
