package app_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 规划运行时现场核验、准备容器清理、快照恢复、快照废弃与释放恢复的用例。
//
// 所有用例都使用真实 SQLite、真实容器服务外壳与受控远程替身：数据库事实由重新读取证明，
// 远程事实由替身注入，任何"远程结果不确定"的现场都必须显式构造，不允许由替身默认给出
// 成功或不存在。

// 规划运行时用例使用的固定测试数据。
const (
	// runtimeImageID 是容器登记记录与快照核验容器使用的镜像 ID。
	runtimeImageID = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	// runtimeBuildHash 是容器登记记录使用的构建配置哈希。
	runtimeBuildHash = "2222222222222222222222222222222222222222222222222222222222222222"
	// runtimeCommit 是快照固定的 commit。
	runtimeCommit = "1111111111111111111111111111111111111111"
	// runtimeOtherCommit 是另一个 commit，用于验证不一致时拒绝覆盖。
	runtimeOtherCommit = "2222222222222222222222222222222222222222"
	// runtimeEnvironmentName 是项目与容器使用的语言环境名称。
	runtimeEnvironmentName = "go"
	// runtimeVersionLabel 是项目锁定的环境版本标签。
	runtimeVersionLabel = "v1"
	// runtimeProjectID 是规划运行时用例使用的项目标识。
	runtimeProjectID = "runtime-project"
)

// 替身记录的远程动作名称。
const (
	runtimeCallInspectManaged = "inspect_managed_container"
	runtimeCallDeleteManaged  = "delete_managed_container"
	runtimeCallInspectPrep    = "inspect_prep_container"
	runtimeCallRemovePrep     = "remove_prep_container"
	runtimeCallInspectVolume  = "inspect_snapshot_volume"
	runtimeCallRemoveVolume   = "remove_snapshot_volume"
	runtimeCallVerifyCommit   = "verify_snapshot_commit"
	runtimeCallRunRole        = "run_role"
	runtimeCallInspectAgent   = "inspect_agent_container"
)

// runtimeFixture 是规划运行时用例的测试现场。
type runtimeFixture struct {
	db           *sqlite.DB
	containers   *app.ContainerService
	roles        *app.RoleService
	coordination *app.PlanningSessionCoordinator
	service      *app.PlanningRuntimeService
	remote       *fakeRuntimeRemote
	log          *runtimeEventLog
	project      domain.Project
	host         domain.ExecutionHost
	ctx          context.Context
	sessionRoot  string
	// containerSeq 是容器名称序号，保证同一用例内的多份记录名称互不相同。
	containerSeq int
}

// runtimeSeededSession 是一套已写入数据库的规划会话现场。
type runtimeSeededSession struct {
	session   domain.PlanningSession
	container domain.Container
}

// runtimeSeed 描述一个待写入的规划会话现场。
type runtimeSeed struct {
	// hostBound 表示把会话绑定到测试执行节点。
	hostBound bool
	// analyzing 表示会话处于 analyzing 状态。
	analyzing bool
	// containerBound 表示写入并绑定一个规划容器登记记录。
	containerBound bool
	// sourceCommit 是写入会话的固定 commit，空值表示快照尚未固定。
	sourceCommit string
	// containerHost 覆盖容器记录的执行节点，用于构造归属不成立的现场。
	containerHost domain.ID
}

// newRuntimeFixture 在测试工作空间中构造规划运行时服务与受控远程替身。
func newRuntimeFixture(t *testing.T) *runtimeFixture {
	t.Helper()
	workspace := testkit.New(t)
	workspace.Report()
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
	sshService, err := app.NewSSHService(db, logger, app.SSHOptions{
		KnownHostsPath: filepath.Join(workspace.Dir("ssh"), "known_hosts"),
	})
	if err != nil {
		t.Fatalf("构造 SSH 服务失败: %v", err)
	}
	containers, err := app.NewContainerService(app.DockerServiceOptions{
		SSH:    sshService,
		Store:  db,
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("构造容器服务失败: %v", err)
	}
	if _, err := db.SeedRoles(ctx, app.RoleSeeds()); err != nil {
		t.Fatalf("初始化固定角色失败: %v", err)
	}
	roles, err := app.NewRoleService(db)
	if err != nil {
		t.Fatalf("构造角色服务失败: %v", err)
	}
	now := time.Now().UTC()
	environment := domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: runtimeEnvironmentName}
	project, err := domain.NewProject(domain.ProjectInput{
		ID:                 runtimeProjectID,
		Name:               "规划运行时项目",
		RepositoryURL:      "ssh://git.example.com/team/runtime-repo",
		BaseBranch:         "main",
		Environment:        environment,
		EnvironmentVersion: runtimeVersionLabel,
	}, now)
	if err != nil {
		t.Fatalf("构造项目失败: %v", err)
	}
	if err := db.CreateProject(ctx, project); err != nil {
		t.Fatalf("写入项目失败: %v", err)
	}
	host, err := domain.NewExecutionHost(
		"runtime-host",
		"规划运行时节点",
		"10.0.0.21",
		22,
		"deploy",
		"/srv/agentflow",
		now,
		domain.WithPrivateKey(testkit.PrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	// 只有通过真实预检的节点才允许用于远程操作：用例直接写入 normal 现场。
	host.ValidationStatus = domain.ValidationStatusNormal
	if err := db.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	version := domain.EnvironmentVersion{
		HostID:            host.ID,
		Ref:               environment,
		Number:            1,
		DockerfileSHA256:  runtimeHash("dockerfile"),
		BuildConfigSHA256: runtimeHash("config"),
		ContextSHA256:     runtimeHash("context"),
		ParentVersion:     "v1",
		ImageRef:          "agentflow-language-go:v0001-abcdefabcdef",
		ImageID:           runtimeImageID,
		Status:            domain.BuildStatusSucceeded,
		CreatedAt:         now,
	}
	builtAt := now
	version.BuiltAt = &builtAt
	if err := db.CreateEnvironmentVersion(ctx, version); err != nil {
		t.Fatalf("写入环境版本失败: %v", err)
	}
	eventLog := &runtimeEventLog{}
	remote := newFakeRuntimeRemote(eventLog)
	coordination := app.NewPlanningSessionCoordinator()
	service, err := app.NewPlanningRuntimeService(app.PlanningRuntimeServiceOptions{
		Store:        db,
		Containers:   containers,
		RemotePort:   remote,
		Coordination: coordination,
	})
	if err != nil {
		t.Fatalf("构造规划运行时服务失败: %v", err)
	}
	return &runtimeFixture{
		db:           db,
		containers:   containers,
		roles:        roles,
		coordination: coordination,
		service:      service,
		remote:       remote,
		log:          eventLog,
		project:      project,
		host:         host,
		ctx:          ctx,
		sessionRoot:  workspace.SessionRoot(),
	}
}

// runtimeHash 返回测试用的 64 位十六进制摘要。
func runtimeHash(seed string) string {
	digits := []byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	for index, char := range []byte(seed) {
		digits[index] = "0123456789abcdef"[int(char)%16]
	}
	return string(digits)
}

// seedSession 写入一个规划会话现场。
func (f *runtimeFixture) seedSession(t *testing.T, seed runtimeSeed) runtimeSeededSession {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	seeded := runtimeSeededSession{}
	if seed.containerBound {
		hostID := f.host.ID
		if seed.containerHost != "" {
			hostID = seed.containerHost
		}
		f.containerSeq++
		container, err := domain.NewContainer(
			"",
			hostID,
			fmt.Sprintf("agentflow-runtime-container-%d", f.containerSeq),
			f.project.Environment,
			runtimeVersionLabel,
			runtimeBuildHash,
			runtimeImageID,
			now,
		)
		if err != nil {
			t.Fatalf("构造规划容器记录失败: %v", err)
		}
		container.DockerID = "docker-" + container.ID.String()
		container.State = domain.ContainerStateRunning
		container.DockerState = domain.DockerStateRunning
		if err := f.db.CreateContainer(f.ctx, container); err != nil {
			t.Fatalf("写入规划容器记录失败: %v", err)
		}
		seeded.container = container
	}
	session, err := domain.NewPlanningSession(f.project.ID, now)
	if err != nil {
		t.Fatalf("构造规划会话失败: %v", err)
	}
	if seed.analyzing {
		if err := session.BeginAnalysis(now); err != nil {
			t.Fatalf("规划会话进入 analyzing 失败: %v", err)
		}
	}
	var hostID *domain.ID
	if seed.hostBound {
		hostID = &f.host.ID
	}
	containerID := ""
	if seed.containerBound {
		containerID = seeded.container.ID.String()
	}
	if err := session.BindRuntime(hostID, containerID, seed.sourceCommit, now); err != nil {
		t.Fatalf("绑定规划运行环境失败: %v", err)
	}
	if err := f.db.CreatePlanningSession(f.ctx, session); err != nil {
		t.Fatalf("写入规划会话失败: %v", err)
	}
	seeded.session = session
	return seeded
}

// loadSession 重新读取规划会话的持久化事实。
func (f *runtimeFixture) loadSession(t *testing.T, sessionID domain.ID) domain.PlanningSession {
	t.Helper()
	session, err := f.db.GetPlanningSession(f.ctx, sessionID)
	if err != nil {
		t.Fatalf("读取规划会话失败: %v", err)
	}
	return *session
}

// prepContainerName 返回会话与测试节点推导出的准备容器名称。
func (f *runtimeFixture) prepContainerName(session domain.PlanningSession) string {
	return "agentflow-planprep-" + session.ID.String() + "-" + f.host.ID.String()
}

// volumeName 返回会话与测试节点推导出的快照卷名称。
func (f *runtimeFixture) volumeName(session domain.PlanningSession) string {
	return "agentflow-plan-" + session.ID.String() + "-" + f.host.ID.String()
}

// runtimeEventLog 按发生顺序记录远程与本地事件。
//
// 它用于断言临界区进入顺序：先进入的事件一定排在前面，不依赖固定等待推断锁顺序。
type runtimeEventLog struct {
	mu     sync.Mutex
	events []string
}

// add 追加一个事件。
func (l *runtimeEventLog) add(event string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

// snapshot 返回当前事件序列的副本。
func (l *runtimeEventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// count 返回某个事件出现的次数。
func (l *runtimeEventLog) count(event string) int {
	total := 0
	for _, item := range l.snapshot() {
		if item == event {
			total++
		}
	}
	return total
}

// fakeRuntimeRemote 是规划运行时远程能力的受控替身。
//
// 替身只返回用例显式写入的事实：未登记的资源一律"不存在"，失败必须由用例注入，避免默认
// 成功掩盖生产代码的缺陷。
type fakeRuntimeRemote struct {
	mu  sync.Mutex
	log *runtimeEventLog

	containers map[string]app.ContainerDetail
	preps      map[string]docker.ContainerInspect
	volumes    map[string]docker.VolumeInspect
	commits    map[string]string

	inspectContainerErr error
	deleteContainerErr  error
	inspectPrepErr      error
	removePrepErr       error
	inspectVolumeErr    error
	removeVolumeErr     error
	verifyCommitErr     error

	// deleteContainerState 是删除失败时返回的登记记录状态。
	deleteContainerState domain.ContainerState
	// removeKeepsResource 为 true 时删除命令报成功但不移除资源，用于验证删除后的复核。
	removeKeepsResource bool

	// hook 在每次动作进入时被调用，用例用它模拟并发写入。
	hook func(operation string)
	// gateOperation 触发同步阻塞，gateEntered 在进入时收到信号，gateRelease 关闭后放行。
	gateOperation string
	gateEntered   chan string
	gateRelease   chan struct{}
}

// newFakeRuntimeRemote 构造远程能力替身。
func newFakeRuntimeRemote(log *runtimeEventLog) *fakeRuntimeRemote {
	return &fakeRuntimeRemote{
		log:        log,
		containers: make(map[string]app.ContainerDetail),
		preps:      make(map[string]docker.ContainerInspect),
		volumes:    make(map[string]docker.VolumeInspect),
		commits:    make(map[string]string),
	}
}

// enter 记录一次动作，并按需进入同步阻塞。
func (f *fakeRuntimeRemote) enter(operation string) {
	f.mu.Lock()
	f.log.add(operation)
	hook := f.hook
	gated := f.gateOperation == operation && f.gateRelease != nil
	entered, release := f.gateEntered, f.gateRelease
	f.mu.Unlock()
	if hook != nil {
		hook(operation)
	}
	if !gated {
		return
	}
	entered <- operation
	<-release
}

// blockOn 让指定动作在 release 之前阻塞，并在每次进入时向 entered 发送信号。
func (f *fakeRuntimeRemote) blockOn(operation string) (<-chan string, func()) {
	f.mu.Lock()
	f.gateOperation = operation
	f.gateEntered = make(chan string, 16)
	f.gateRelease = make(chan struct{})
	entered := f.gateEntered
	release := f.gateRelease
	f.mu.Unlock()
	var once sync.Once
	return entered, func() { once.Do(func() { close(release) }) }
}

// setHook 设置动作进入时的回调。
func (f *fakeRuntimeRemote) setHook(hook func(operation string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hook = hook
}

// trackContainer 登记一个受管理规划容器的远端现场。
func (f *fakeRuntimeRemote) trackContainer(record domain.Container, planningSessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers[record.ID.String()] = app.ContainerDetail{
		Container: record,
		Inspect: &docker.ContainerInspect{
			ID:      record.DockerID,
			Name:    record.Name,
			State:   record.DockerState,
			Running: record.State == domain.ContainerStateRunning,
			Labels: map[string]string{
				docker.OwnershipLabelKey:       "true",
				docker.HostLabelKey:            record.HostID.String(),
				docker.ContainerLabelKey:       record.ID.String(),
				docker.PlanningSessionLabelKey: planningSessionID,
			},
		},
	}
}

// trackPrepContainer 登记一个一次性准备容器现场。
func (f *fakeRuntimeRemote) trackPrepContainer(
	name string,
	sessionID, hostID domain.ID,
	state domain.DockerContainerState,
	running bool,
) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preps[name] = docker.ContainerInspect{
		ID:      "prep-" + name,
		Name:    name,
		State:   state,
		Running: running,
		Labels: map[string]string{
			docker.OwnershipLabelKey:       "true",
			docker.HostLabelKey:            hostID.String(),
			docker.PlanningSessionLabelKey: sessionID.String(),
		},
	}
}

// trackVolume 登记一个快照卷现场与它的实际 commit。
func (f *fakeRuntimeRemote) trackVolume(name string, sessionID, hostID domain.ID, commit string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumes[name] = docker.VolumeInspect{
		Name: name,
		Labels: map[string]string{
			docker.OwnershipLabelKey:       "true",
			docker.HostLabelKey:            hostID.String(),
			docker.PlanningSessionLabelKey: sessionID.String(),
		},
	}
	f.commits[name] = commit
}

// setVolumeLabels 覆盖快照卷标签，用于构造归属不成立的现场。
func (f *fakeRuntimeRemote) setVolumeLabels(name string, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	volume := f.volumes[name]
	volume.Name = name
	volume.Labels = labels
	f.volumes[name] = volume
}

// setPrepLabels 覆盖准备容器标签，用于构造归属不成立的现场。
func (f *fakeRuntimeRemote) setPrepLabels(name string, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inspect := f.preps[name]
	inspect.Labels = labels
	f.preps[name] = inspect
}

// hasPrepHolder 判断准备容器现场是否仍然存在。
func (f *fakeRuntimeRemote) hasPrepHolder(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.preps[name]
	return ok
}

// hasVolumeHolder 判断快照卷现场是否仍然存在。
func (f *fakeRuntimeRemote) hasVolumeHolder(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.volumes[name]
	return ok
}

// InspectManagedContainer 返回受控的规划容器现场。
func (f *fakeRuntimeRemote) InspectManagedContainer(
	ctx context.Context,
	containerID string,
) (app.ContainerDetail, error) {
	f.enter(runtimeCallInspectManaged)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspectContainerErr != nil {
		return app.ContainerDetail{}, f.inspectContainerErr
	}
	detail, ok := f.containers[containerID]
	if !ok {
		return app.ContainerDetail{}, runtimeDockerError(docker.CodeContainerNotFound, "docker inspect")
	}
	return detail, nil
}

// DeleteManagedContainer 删除受控的规划容器现场。
func (f *fakeRuntimeRemote) DeleteManagedContainer(
	ctx context.Context,
	containerID string,
) (app.ContainerDetail, error) {
	f.enter(runtimeCallDeleteManaged)
	f.mu.Lock()
	defer f.mu.Unlock()
	detail, ok := f.containers[containerID]
	if f.deleteContainerErr != nil {
		if f.deleteContainerState != "" {
			record := detail.Container
			record.State = f.deleteContainerState
			detail.Container = record
		}
		return detail, f.deleteContainerErr
	}
	if !ok {
		// docker rm 对已不存在的容器是幂等的：这里按"已删除"返回，与容器服务的
		// 删除语义一致，避免替身把"已确认不存在"当成失败。
		return app.ContainerDetail{
			Container: domain.Container{
				ID:    domain.ID(containerID),
				State: domain.ContainerStateRemoved,
			},
		}, nil
	}
	record := detail.Container
	record.State = domain.ContainerStateRemoved
	detail.Container = record
	delete(f.containers, containerID)
	return detail, nil
}

// InspectPrepContainer 返回受控的一次性准备容器现场。
func (f *fakeRuntimeRemote) InspectPrepContainer(
	ctx context.Context,
	hostID domain.ID,
	name string,
) (docker.ContainerInspect, bool, error) {
	f.enter(runtimeCallInspectPrep)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspectPrepErr != nil {
		return docker.ContainerInspect{}, false, f.inspectPrepErr
	}
	inspect, ok := f.preps[name]
	return inspect, ok, nil
}

// RemovePrepContainer 按 Docker 容器 ID 删除准备容器现场。
func (f *fakeRuntimeRemote) RemovePrepContainer(
	ctx context.Context,
	hostID domain.ID,
	dockerID string,
) (bool, error) {
	f.enter(runtimeCallRemovePrep)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removePrepErr != nil {
		return false, f.removePrepErr
	}
	for name, inspect := range f.preps {
		if inspect.ID != dockerID {
			continue
		}
		if !f.removeKeepsResource {
			delete(f.preps, name)
		}
		return true, nil
	}
	return false, nil
}

// InspectSnapshotVolume 返回受控的快照卷现场。
func (f *fakeRuntimeRemote) InspectSnapshotVolume(
	ctx context.Context,
	hostID domain.ID,
	volumeName string,
) (docker.VolumeInspect, bool, error) {
	f.enter(runtimeCallInspectVolume)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspectVolumeErr != nil {
		return docker.VolumeInspect{}, false, f.inspectVolumeErr
	}
	volume, ok := f.volumes[volumeName]
	return volume, ok, nil
}

// RemoveSnapshotVolume 删除快照卷现场。
func (f *fakeRuntimeRemote) RemoveSnapshotVolume(
	ctx context.Context,
	hostID domain.ID,
	volumeName string,
) (bool, error) {
	f.enter(runtimeCallRemoveVolume)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removeVolumeErr != nil {
		return false, f.removeVolumeErr
	}
	if _, ok := f.volumes[volumeName]; !ok {
		return false, nil
	}
	if !f.removeKeepsResource {
		delete(f.volumes, volumeName)
	}
	return true, nil
}

// VerifySnapshotCommit 返回受控的远端实际 commit。
func (f *fakeRuntimeRemote) VerifySnapshotCommit(
	ctx context.Context,
	target app.PlanningRuntimeSnapshotTarget,
) (string, error) {
	f.enter(runtimeCallVerifyCommit)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.verifyCommitErr != nil {
		return "", f.verifyCommitErr
	}
	commit, ok := f.commits[target.VolumeName]
	if !ok {
		return "", runtimeDockerError(docker.CodeCommandFailed, "docker run")
	}
	return commit, nil
}

// runtimeDockerError 构造带类别的远程 Docker 错误。
func runtimeDockerError(code docker.ErrorCode, op string) error {
	return &docker.Error{Code: code, Op: op, Detail: "受控替身注入的远程失败"}
}

// assertNoOperation 断言指定远程动作没有被调用。
func assertNoOperation(t *testing.T, log *runtimeEventLog, operations ...string) {
	t.Helper()
	for _, operation := range operations {
		if count := log.count(operation); count != 0 {
			t.Fatalf("动作 %s 不应被调用，实际调用了 %d 次：%v", operation, count, log.snapshot())
		}
	}
}

// assertOperation 断言指定远程动作被调用过。
func assertOperation(t *testing.T, log *runtimeEventLog, operation string) {
	t.Helper()
	if count := log.count(operation); count == 0 {
		t.Fatalf("动作 %s 应当被调用，实际调用序列为 %v", operation, log.snapshot())
	}
}

// requireIs 断言错误链上包含目标错误类别。
func requireIs(t *testing.T, err error, target error, message string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s：期望错误类别 %v，实际 %v", message, target, err)
	}
}

// requireNoError 断言没有错误。
func requireNoError(t *testing.T, err error, message string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s：%v", message, err)
	}
}

// 通知用例中"确认没有进入临界区"的观察窗口。
const runtimeNotEnteredProbeTimeout = 200 * time.Millisecond

// awaitRuntimeSignal 等待一次同步信号，超时即失败。
func awaitRuntimeSignal(t *testing.T, entered <-chan string, message string) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s（等待 5s 仍无信号）", message)
	}
}

// assertRuntimeNotEntered 断言在观察窗口内没有新的进入信号。
func assertRuntimeNotEntered(t *testing.T, entered <-chan string, message string) {
	t.Helper()
	select {
	case operation := <-entered:
		t.Fatalf("%s（实际收到 %s）", message, operation)
	case <-time.After(runtimeNotEnteredProbeTimeout):
	}
}
