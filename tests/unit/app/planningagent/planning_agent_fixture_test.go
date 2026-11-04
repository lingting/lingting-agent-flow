package planningagent_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 规划会话测试数据。
//
// 容器登记记录必须真实写入数据库：规划 Agent 服务会读取记录并核验归属与标签，
// 只在会话上填一个容器标识验证的不是业务规则，而是"记录缺失时被拒绝"。
const (
	// testImageID 是容器登记记录使用的镜像 ID。
	testImageID = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	// testBuildHash 是容器登记记录使用的构建配置哈希。
	testBuildHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	// testVersionLabel 是容器与项目使用的环境版本标签。
	testVersionLabel = "v1"
	// testSourceCommit 是规划会话固定的代码快照 commit。
	testSourceCommit = "1111111111111111111111111111111111111111"
	// testEnvironmentName 是项目与容器使用的语言环境名称。
	testEnvironmentName = "go"
)

// planningTarget 是一套已经准备好运行环境的规划会话。
type planningTarget struct {
	session   domain.PlanningSession
	container domain.Container
}

// planningFixture 是一套规划 Agent 服务测试现场。
type planningFixture struct {
	service *app.PlanningAgentService
	port    *fakeContainerPort
	targets []planningTarget
	// db 是本次现场使用的测试数据库，供"第二个实例"与直接读取租约事实的用例复用。
	db *sqlite.DB
	// roles 是固定角色配置读取入口，供第二个实例复用。
	roles *app.RoleService
	// sessionRoot 是本机 Session 根目录，两个实例必须指向同一份归档目录。
	sessionRoot string
	// leases 是本现场使用的规划会话租约服务。
	leases *app.PlanningSessionLeaseService
}

// fixtureOptions 允许用例调整构造参数。
type fixtureOptions struct {
	// sessions 是准备运行环境的规划会话数量，至少 1。
	sessions int
	// start 表示构造后立即启动服务。
	start bool
	// hold 是替身收到取消后继续收敛的时长，用于验证停止等待完整收敛。
	hold time.Duration
	// ignoreCancel 表示替身忽略取消信号，用于验证停止超时返回真实错误。
	ignoreCancel bool
	// leaseTTL 覆盖租约有效期，零值使用生产默认值；用例用较短 TTL 验证续期与失效。
	leaseTTL time.Duration
	// leaseStore 允许包裹租约存储（故障注入），为空时直接使用测试数据库。
	leaseStore func(store storage.PlanningSessionLeaseStore) storage.PlanningSessionLeaseStore
}

// newPlanningFixture 在测试工作空间中准备规划会话并构造规划 Agent 服务。
func newPlanningFixture(t *testing.T, options fixtureOptions) *planningFixture {
	t.Helper()
	if options.sessions <= 0 {
		options.sessions = 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	workspace := testkit.New(t)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	// 规划角色配置来自统一角色服务：这里用生产同一套初始化补齐四个固定角色。
	if _, err := db.SeedRoles(ctx, app.RoleSeeds()); err != nil {
		t.Fatalf("初始化固定角色失败: %v", err)
	}
	roles, err := app.NewRoleService(db)
	if err != nil {
		t.Fatalf("构造角色服务失败: %v", err)
	}
	now := time.Now().UTC()
	environment := domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: testEnvironmentName}
	project, err := domain.NewProject(domain.ProjectInput{
		ID:                 "planning-project",
		Name:               "规划项目",
		RepositoryURL:      "ssh://git.example.com/team/repo",
		BaseBranch:         "main",
		Environment:        environment,
		EnvironmentVersion: testVersionLabel,
	}, now)
	if err != nil {
		t.Fatalf("构造项目失败: %v", err)
	}
	if err := db.CreateProject(ctx, project); err != nil {
		t.Fatalf("写入项目失败: %v", err)
	}
	host, err := domain.NewExecutionHost(
		"",
		"规划节点",
		"10.0.0.9",
		22,
		"deploy",
		"/srv/agentflow",
		now,
		domain.WithPrivateKey(testkit.PrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	port := newFakeContainerPort(options.hold, options.ignoreCancel)
	targets := make([]planningTarget, 0, options.sessions)
	for index := 0; index < options.sessions; index++ {
		container, err := domain.NewContainer(
			"",
			host.ID,
			fmt.Sprintf("agentflow-plan-test-%d", index),
			environment,
			testVersionLabel,
			testBuildHash,
			testImageID,
			now,
		)
		if err != nil {
			t.Fatalf("构造规划容器记录失败: %v", err)
		}
		container.DockerID = "docker-" + container.ID.String()
		container.State = domain.ContainerStateRunning
		container.DockerState = domain.DockerStateRunning
		if err := db.CreateContainer(ctx, container); err != nil {
			t.Fatalf("写入规划容器记录失败: %v", err)
		}
		session, err := domain.NewPlanningSession(project.ID, now)
		if err != nil {
			t.Fatalf("构造规划会话失败: %v", err)
		}
		if err := session.BeginAnalysis(now); err != nil {
			t.Fatalf("规划会话进入 analyzing 失败: %v", err)
		}
		if err := session.BindRuntime(&host.ID, container.ID.String(), testSourceCommit, now); err != nil {
			t.Fatalf("绑定规划运行环境失败: %v", err)
		}
		if err := db.CreatePlanningSession(ctx, session); err != nil {
			t.Fatalf("写入规划会话失败: %v", err)
		}
		port.track(container, session.ID.String())
		targets = append(targets, planningTarget{session: session, container: container})
	}
	leaseStore := storage.PlanningSessionLeaseStore(db)
	if options.leaseStore != nil {
		leaseStore = options.leaseStore(db)
	}
	leases := newLeaseService(t, leaseStore, options.leaseTTL)
	service, err := app.NewPlanningAgentService(app.PlanningAgentServiceOptions{
		Store:       db,
		Roles:       roles,
		Containers:  port,
		Leases:      leases,
		SessionRoot: workspace.SessionRoot(),
	})
	if err != nil {
		t.Fatalf("构造规划 Agent 服务失败: %v", err)
	}
	if options.start {
		if startErr := service.Start(ctx); startErr != nil {
			t.Fatalf("启动规划 Agent 服务失败: %v", startErr)
		}
		t.Cleanup(func() {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer stopCancel()
			if stopErr := service.Stop(stopCtx); stopErr != nil {
				t.Errorf("停止规划 Agent 服务失败: %v", stopErr)
			}
		})
	}
	return &planningFixture{
		service:     service,
		port:        port,
		targets:     targets,
		db:          db,
		roles:       roles,
		sessionRoot: workspace.SessionRoot(),
		leases:      leases,
	}
}

// newLeaseService 构造规划会话租约服务，ttl 为零时使用生产默认值。
func newLeaseService(
	t *testing.T,
	store storage.PlanningSessionLeaseStore,
	ttl time.Duration,
) *app.PlanningSessionLeaseService {
	t.Helper()
	leases, err := app.NewPlanningSessionLeaseService(app.PlanningLeaseOptions{Store: store, TTL: ttl})
	if err != nil {
		t.Fatalf("构造规划会话租约服务失败: %v", err)
	}
	return leases
}

// newInstance 在同一个测试数据库上构造另一个独立的规划 Agent 服务实例。
//
// 两个实例各自持有进程内互斥，只有数据库租约共享：这正是"两个 Server 连接同一个数据库"
// 的现场，进程内 mutex 无法阻止竞争。
func (f *planningFixture) newInstance(
	t *testing.T,
	port app.PlanningAgentContainerPort,
	leases *app.PlanningSessionLeaseService,
) *app.PlanningAgentService {
	t.Helper()
	if leases == nil {
		leases = f.leases
	}
	service, err := app.NewPlanningAgentService(app.PlanningAgentServiceOptions{
		Store:       f.db,
		Roles:       f.roles,
		Containers:  port,
		Leases:      leases,
		SessionRoot: f.sessionRoot,
	})
	if err != nil {
		t.Fatalf("构造第二个规划 Agent 服务实例失败: %v", err)
	}
	startCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if startErr := service.Start(startCtx); startErr != nil {
		t.Fatalf("启动第二个规划 Agent 服务实例失败: %v", startErr)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if stopErr := service.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止第二个规划 Agent 服务实例失败: %v", stopErr)
		}
	})
	return service
}

// newRuntime 在同一测试数据库上构造规划运行时服务。
//
// 只注入释放能力：这些用例验证的是 Prepare / Release 与 RunRole 的租约边界，
// 不创建真实容器，也不建立通用容器模拟框架。
func (f *planningFixture) newRuntime(
	t *testing.T,
	port app.PlanningRuntimeReleasePort,
) *app.PlanningRuntimeService {
	t.Helper()
	runtime, err := app.NewPlanningRuntimeService(app.PlanningRuntimeServiceOptions{
		Store:       f.db,
		Leases:      f.leases,
		ReleasePort: port,
	})
	if err != nil {
		t.Fatalf("构造规划运行时服务失败: %v", err)
	}
	return runtime
}

// newPort 构造一个与现场一致、已登记全部规划容器标签的容器能力替身。
//
// 规划调用会核验容器归属与规划会话标签，第二个实例必须看到同一批容器记录；
// 只构造空替身验证的不是租约，而是"记录缺失时被拒绝"。
func (f *planningFixture) newPort(hold time.Duration, ignoreCancel bool) *fakeContainerPort {
	port := newFakeContainerPort(hold, ignoreCancel)
	for _, target := range f.targets {
		port.track(target.container, target.session.ID.String())
	}
	return port
}

// callService 在后台对指定服务实例发起一次规划角色调用。
func (f *planningFixture) callService(
	ctx context.Context,
	service *app.PlanningAgentService,
	index int,
) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := service.RunRole(ctx, f.request(index))
		done <- err
	}()
	return done
}

// request 构造一次指向指定规划会话的规划角色调用请求。
func (f *planningFixture) request(index int) app.RunPlanningRoleRequest {
	return app.RunPlanningRoleRequest{
		PlanningSessionID: f.targets[index].session.ID.String(),
		RoleType:          string(domain.RoleTypeRequirementAnalyst),
		Prompt:            "分析这次需求",
	}
}

// stop 在限定时间内停止服务并返回错误。
func (f *planningFixture) stop(t *testing.T) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return f.service.Stop(ctx)
}

// call 在后台发起一次规划角色调用，返回错误通道。
func (f *planningFixture) call(ctx context.Context, index int) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := f.service.RunRole(ctx, f.request(index))
		done <- err
	}()
	return done
}

// waitCall 等待一次后台调用结束并返回其错误。
func waitCall(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("等待规划调用结束超时")
		return nil
	}
}

// fakeContainerPort 是容器核验与容器内 Agent 调用的受控替身。
//
// 它只实现规划 Agent 服务实际使用的两项能力：核验容器状态与在容器内执行一次调用。
// RunAgent 在收到取消后按配置继续"收敛"一段时间，用于验证停止等待的是完整调用而不是
// 取消信号本身。
type fakeContainerPort struct {
	mu         sync.Mutex
	containers map[string]domain.Container
	labels     map[string]string
	requests   []app.ContainerAgentRequest
	contexts   []context.Context
	hold       time.Duration
	// ignoreCancel 为 true 时忽略取消信号，直到 release 关闭。
	ignoreCancel bool
	release      chan struct{}
	// entered 每次进入 RunAgent 时写入一个令牌，供用例同步。
	entered chan struct{}
	// archiving 在收到取消后、RunAgent 返回前发出信号；为空表示不模拟归档阶段。
	archiving chan struct{}
	// archiveRelease 关闭后 RunAgent 才完成归档并返回；与 archiving 成对使用。
	archiveRelease chan struct{}
	// calls 统计进入 RunAgent 的次数。
	calls int32
	// finished 统计 RunAgent 返回的次数。
	finished int32
}

// newFakeContainerPort 构造容器能力替身。
func newFakeContainerPort(hold time.Duration, ignoreCancel bool) *fakeContainerPort {
	return &fakeContainerPort{
		containers:   make(map[string]domain.Container),
		labels:       make(map[string]string),
		hold:         hold,
		ignoreCancel: ignoreCancel,
		release:      make(chan struct{}),
		entered:      make(chan struct{}, 32),
	}
}

// track 登记一条容器记录及其规划会话标签。
func (f *fakeContainerPort) track(container domain.Container, planningSessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers[container.ID.String()] = container
	f.labels[container.ID.String()] = planningSessionID
}

// InspectContainer 返回受控的容器状态。
func (f *fakeContainerPort) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	f.mu.Lock()
	container, ok := f.containers[request.ContainerID]
	label := f.labels[request.ContainerID]
	f.mu.Unlock()
	if !ok {
		return app.ContainerDetail{}, fmt.Errorf("容器 %s 不存在", request.ContainerID)
	}
	return app.ContainerDetail{
		Container: container,
		Inspect: &docker.ContainerInspect{
			ID:      container.DockerID,
			Running: true,
			Labels:  map[string]string{docker.PlanningSessionLabelKey: label},
		},
	}, nil
}

// RunAgent 在收到取消信号（或显式释放）后返回。
func (f *fakeContainerPort) RunAgent(
	ctx context.Context,
	request app.ContainerAgentRequest,
) (app.ContainerAgentResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.contexts = append(f.contexts, ctx)
	hold := f.hold
	ignore := f.ignoreCancel
	release := f.release
	archiving := f.archiving
	archiveRelease := f.archiveRelease
	f.mu.Unlock()
	atomic.AddInt32(&f.calls, 1)
	select {
	case f.entered <- struct{}{}:
	default:
	}
	defer atomic.AddInt32(&f.finished, 1)
	if ignore {
		<-release
		return app.ContainerAgentResult{}, fmt.Errorf("模拟规划调用被显式释放")
	}
	<-ctx.Done()
	if hold > 0 {
		// 模拟 Session 提取与归档：取消之后仍需要一段收敛时间。
		time.Sleep(hold)
	}
	if archiving != nil {
		// 模拟"Pi 已取消、归档仍在收尾"：RunAgent 尚未返回，因此租约不能提前释放。
		select {
		case archiving <- struct{}{}:
		default:
		}
		<-archiveRelease
	}
	return app.ContainerAgentResult{}, ctx.Err()
}

// waitEntered 等待指定数量的调用进入 RunAgent。
func (f *fakeContainerPort) waitEntered(t *testing.T, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		select {
		case <-f.entered:
		case <-time.After(10 * time.Second):
			t.Fatalf("等待第 %d 次规划调用进入执行超时", index+1)
		}
	}
}

// callCount 返回进入 RunAgent 的次数。
func (f *fakeContainerPort) callCount() int {
	return int(atomic.LoadInt32(&f.calls))
}

// finishCount 返回 RunAgent 返回的次数。
func (f *fakeContainerPort) finishCount() int {
	return int(atomic.LoadInt32(&f.finished))
}

// requestAt 返回第 index 次调用收到的请求。
func (f *fakeContainerPort) requestAt(t *testing.T, index int) app.ContainerAgentRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if index >= len(f.requests) {
		t.Fatalf("第 %d 次调用的请求不存在，实际记录 %d 次", index+1, len(f.requests))
	}
	return f.requests[index]
}

// contextAt 返回第 index 次调用收到的上下文。
func (f *fakeContainerPort) contextAt(t *testing.T, index int) context.Context {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if index >= len(f.contexts) {
		t.Fatalf("第 %d 次调用的上下文不存在，实际记录 %d 次", index+1, len(f.contexts))
	}
	return f.contexts[index]
}

// releaseAll 释放被 ignoreCancel 阻塞的调用。
func (f *fakeContainerPort) releaseAll() {
	close(f.release)
}

// holdArchive 让 RunAgent 在收到取消后停在"归档收尾"阶段。
//
// 返回的 archiving 在进入归档阶段时收到信号，调用 closeArchive 之后 RunAgent 才真正返回。
// 用例据此断言：Pi 已被取消、但归档尚未完成时，租约仍然被持有，RunRole 也没有返回。
func (f *fakeContainerPort) holdArchive(t *testing.T) (<-chan struct{}, func()) {
	t.Helper()
	archiving := make(chan struct{}, 1)
	archiveRelease := make(chan struct{})
	f.mu.Lock()
	f.archiving = archiving
	f.archiveRelease = archiveRelease
	f.mu.Unlock()
	var once sync.Once
	return archiving, func() {
		once.Do(func() { close(archiveRelease) })
	}
}
