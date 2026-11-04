package planningagent_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// fakeReleasePort 是规划运行时释放路径的受控替身。
//
// 它只实现释放真正使用的四项能力，并记录每项被调用的次数：用例据此断言"租约冲突时
// 根本没有进入远程删除或绑定清理"，而不是只看返回的错误。它不模拟容器创建、复用与
// 核验，因此不是通用容器模拟框架，也不改变生产执行路径。
type fakeReleasePort struct {
	mu         sync.Mutex
	host       domain.ExecutionHost
	containers map[string]domain.Container
	// deleteErr 让删除容器失败，用于验证"释放失败不得报告成功"。
	deleteErr error
	// volumeReleased 是删除快照卷的返回值。
	volumeReleased bool
	// entered 每次进入 LoadExecutionHost 时发信号，供用例同步。
	entered chan struct{}
	// gate 关闭后 LoadExecutionHost 才返回，用于把释放停留在"已取得租约"的状态。
	gate chan struct{}
	// 各项能力的调用次数。
	loadHostCalls int
	loadContainer int
	deleteCalls   int
	volumeCalls   int
}

// newFakeReleasePort 构造释放能力替身，容器记录取自本次测试现场。
func newFakeReleasePort(t *testing.T, fixture *planningFixture, index int) *fakeReleasePort {
	t.Helper()
	target := fixture.targets[index]
	host, err := fixture.db.GetExecutionHost(testContextForLease(t), *target.session.HostID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	port := &fakeReleasePort{
		host:       *host,
		containers: map[string]domain.Container{target.container.ID.String(): target.container},
		entered:    make(chan struct{}, 4),
	}
	return port
}

// blockInLoadHost 让登出流程停在取得租约之后的第一个远程步骤。
func (p *fakeReleasePort) blockInLoadHost() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gate = make(chan struct{})
}

// openGate 放行被阻塞的释放流程。
func (p *fakeReleasePort) openGate() {
	p.mu.Lock()
	gate := p.gate
	p.gate = nil
	p.mu.Unlock()
	if gate != nil {
		close(gate)
	}
}

// waitEntered 等待指定数量的释放流程进入远程步骤。
func (p *fakeReleasePort) waitEntered(t *testing.T, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		select {
		case <-p.entered:
		case <-time.After(10 * time.Second):
			t.Fatalf("等待第 %d 次释放进入远程步骤超时", index+1)
		}
	}
}

// calls 返回读取执行节点、读取容器、删除容器与删除快照卷的调用次数。
func (p *fakeReleasePort) calls() (int, int, int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.loadHostCalls, p.loadContainer, p.deleteCalls, p.volumeCalls
}

// LoadExecutionHost 返回现场的执行节点，并按需阻塞。
func (p *fakeReleasePort) LoadExecutionHost(
	ctx context.Context,
	hostID string,
) (domain.ExecutionHost, error) {
	p.mu.Lock()
	p.loadHostCalls++
	host := p.host
	gate := p.gate
	p.mu.Unlock()
	select {
	case p.entered <- struct{}{}:
	default:
	}
	if gate != nil {
		<-gate
	}
	if err := ctx.Err(); err != nil {
		return domain.ExecutionHost{}, err
	}
	if host.ID.String() != hostID {
		return domain.ExecutionHost{}, errors.New("执行节点标识不一致")
	}
	return host, nil
}

// LoadContainer 返回现场的规划容器记录。
func (p *fakeReleasePort) LoadContainer(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loadContainer++
	container, ok := p.containers[containerID]
	if !ok {
		return domain.Container{}, errors.New("容器记录不存在")
	}
	return container, nil
}

// DeleteContainer 按配置返回成功或失败。
func (p *fakeReleasePort) DeleteContainer(
	ctx context.Context,
	request app.DeleteContainerRequest,
) (app.ContainerDetail, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deleteCalls++
	container, ok := p.containers[request.ContainerID]
	if !ok {
		return app.ContainerDetail{}, errors.New("容器记录不存在")
	}
	if p.deleteErr != nil {
		return app.ContainerDetail{Container: container}, p.deleteErr
	}
	container.State = domain.ContainerStateRemoved
	p.containers[request.ContainerID] = container
	return app.ContainerDetail{Container: container}, nil
}

// RemoveSnapshotVolume 按配置返回删除结果。
func (p *fakeReleasePort) RemoveSnapshotVolume(
	ctx context.Context,
	session domain.PlanningSession,
	host domain.ExecutionHost,
) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.volumeCalls++
	return p.volumeReleased, nil
}

// TestPlanningRuntimeServiceLeaseGuardRejectsWhileRunRoleHolds 校验在途规划调用期间禁止释放。
func TestPlanningRuntimeServiceLeaseGuardRejectsWhileRunRoleHolds(t *testing.T) {
	t.Parallel()
	fixture := newPlanningFixture(t, fixtureOptions{start: true})
	port := newFakeReleasePort(t, fixture, 0)
	runtime := fixture.newRuntime(t, port)
	target := fixture.targets[0]

	callCtx, cancelCall := context.WithCancel(context.Background())
	done := fixture.call(callCtx, 0)
	fixture.port.waitEntered(t, 1)

	result, err := runtime.Release(context.Background(), app.ReleasePlanningRuntimeRequest{
		PlanningSessionID: target.session.ID.String(),
	})
	if !errors.Is(err, app.ErrPlanningRuntimeBusy) {
		t.Fatalf("RunRole 持有租约期间 Release 应返回冲突，实际: %v", err)
	}
	if result.ContainerReleased || result.SnapshotReleased {
		t.Fatal("冲突时不得报告任何资源已释放")
	}
	loadHost, loadContainer, deleteCalls, volumeCalls := port.calls()
	if loadHost+loadContainer+deleteCalls+volumeCalls != 0 {
		t.Fatalf(
			"租约冲突时不得进入容器删除、快照卷删除或运行时绑定清理，实际调用 %d/%d/%d/%d",
			loadHost,
			loadContainer,
			deleteCalls,
			volumeCalls,
		)
	}
	current := readPlanningSession(t, fixture.db, target.session.ID)
	if current.HostID == nil || current.ContainerID != target.container.ID.String() {
		t.Fatalf("冲突时不得清空运行时绑定，实际执行节点 %v、容器 %q", current.HostID, current.ContainerID)
	}

	cancelCall()
	if err := waitCall(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("在途调用应按取消返回，实际: %v", err)
	}
}

// TestPlanningRuntimeServiceLeaseGuardReleasesAfterRunRole 校验调用完整返回后 Release 可以进入。
func TestPlanningRuntimeServiceLeaseGuardReleasesAfterRunRole(t *testing.T) {
	t.Parallel()
	fixture := newPlanningFixture(t, fixtureOptions{start: true})
	port := newFakeReleasePort(t, fixture, 0)
	runtime := fixture.newRuntime(t, port)
	target := fixture.targets[0]

	callCtx, cancelCall := context.WithCancel(context.Background())
	done := fixture.call(callCtx, 0)
	fixture.port.waitEntered(t, 1)
	cancelCall()
	if err := waitCall(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("在途调用应按取消返回，实际: %v", err)
	}

	result, err := runtime.Release(context.Background(), app.ReleasePlanningRuntimeRequest{
		PlanningSessionID: target.session.ID.String(),
	})
	if err != nil {
		t.Fatalf("RunRole 释放租约后 Release 应能取得租约: %v", err)
	}
	if !result.ContainerReleased {
		t.Fatal("容器已确认删除时应报告 ContainerReleased")
	}
	if _, _, deleteCalls, volumeCalls := port.calls(); deleteCalls != 1 || volumeCalls != 1 {
		t.Fatalf("释放应依次删除容器与快照卷，实际 %d 次 / %d 次", deleteCalls, volumeCalls)
	}
	current := readPlanningSession(t, fixture.db, target.session.ID)
	if current.HostID != nil || current.ContainerID != "" || current.SourceCommit != "" {
		t.Fatalf("释放成功后应清空运行时绑定，实际 %+v", current)
	}
}

// TestPlanningRuntimeServiceLeaseGuardBlocksDuringRelease 校验释放期间其他实例不能进入。
func TestPlanningRuntimeServiceLeaseGuardBlocksDuringRelease(t *testing.T) {
	t.Parallel()
	fixture := newPlanningFixture(t, fixtureOptions{start: true})
	port := newFakeReleasePort(t, fixture, 0)
	port.blockInLoadHost()
	runtime := fixture.newRuntime(t, port)
	target := fixture.targets[0]

	releaseDone := make(chan error, 1)
	go func() {
		_, err := runtime.Release(context.Background(), app.ReleasePlanningRuntimeRequest{
			PlanningSessionID: target.session.ID.String(),
		})
		releaseDone <- err
	}()
	port.waitEntered(t, 1)

	// 释放已经取得租约：另一个实例不能发起 RunRole。
	secondPort := fixture.newPort(0, false)
	second := fixture.newInstance(t, secondPort, nil)
	if _, err := second.RunRole(context.Background(), fixture.request(0)); !errors.Is(
		err,
		app.ErrPlanningAgentBusy,
	) {
		t.Fatalf("释放期间 RunRole 应被租约拒绝，实际: %v", err)
	}
	if calls := secondPort.callCount(); calls != 0 {
		t.Fatalf("被拒绝的 RunRole 不得进入执行，实际 %d 次", calls)
	}

	// 另一个运行时实例同样不能发起 Prepare。
	otherRuntime := fixture.newRuntime(t, newFakeReleasePort(t, fixture, 0))
	_, prepareErr := otherRuntime.Prepare(
		context.Background(),
		app.PreparePlanningRuntimeRequest{
			PlanningSessionID: target.session.ID.String(),
			HostID:            target.session.HostID.String(),
		},
	)
	if !errors.Is(prepareErr, app.ErrPlanningRuntimeBusy) {
		t.Fatalf("释放期间 Prepare 应被租约拒绝，实际: %v", prepareErr)
	}

	port.openGate()
	if err := <-releaseDone; err != nil {
		t.Fatalf("释放应正常完成: %v", err)
	}
}

// TestPlanningRuntimeServiceLeaseGuardReportsReleaseFailure 校验远程删除失败不报告为已释放。
func TestPlanningRuntimeServiceLeaseGuardReportsReleaseFailure(t *testing.T) {
	t.Parallel()
	fixture := newPlanningFixture(t, fixtureOptions{start: true})
	port := newFakeReleasePort(t, fixture, 0)
	port.deleteErr = errors.New("模拟远程删除失败")
	runtime := fixture.newRuntime(t, port)
	target := fixture.targets[0]

	result, err := runtime.Release(context.Background(), app.ReleasePlanningRuntimeRequest{
		PlanningSessionID: target.session.ID.String(),
	})
	if err == nil {
		t.Fatal("远程删除失败时 Release 必须返回错误")
	}
	if !errors.Is(err, app.ErrPlanningRuntimeFailed) {
		t.Fatalf("删除失败应归入 ErrPlanningRuntimeFailed，实际: %v", err)
	}
	if result.ContainerReleased || result.SnapshotReleased {
		t.Fatal("删除失败时不得报告资源已释放")
	}
	if _, _, _, volumeCalls := port.calls(); volumeCalls != 0 {
		t.Fatalf("容器未确认删除时不得删除快照卷，实际 %d 次", volumeCalls)
	}
	current := readPlanningSession(t, fixture.db, target.session.ID)
	if current.HostID == nil || current.ContainerID != target.container.ID.String() {
		t.Fatalf("释放失败时必须保留运行时定位信息，实际 %+v", current)
	}
}

// TestPlanningRuntimeServiceLeaseGuardReportsLeaseLoss 校验租约被接管时不报告释放成功。
func TestPlanningRuntimeServiceLeaseGuardReportsLeaseLoss(t *testing.T) {
	t.Parallel()
	fixture := newPlanningFixture(t, fixtureOptions{start: true})
	port := newFakeReleasePort(t, fixture, 0)
	port.blockInLoadHost()
	runtime := fixture.newRuntime(t, port)
	target := fixture.targets[0]

	releaseDone := make(chan error, 1)
	go func() {
		_, err := runtime.Release(context.Background(), app.ReleasePlanningRuntimeRequest{
			PlanningSessionID: target.session.ID.String(),
		})
		releaseDone <- err
	}()
	port.waitEntered(t, 1)

	// 模拟"释放期间租约过期并被新执行者接管"：新领取发生在旧租约到期之后。
	takeoverAt := time.Now().UTC().Add(fixture.leases.TTL() + time.Second)
	if _, err := tryAcquireLease(
		t,
		fixture.db,
		target.session.ID,
		"new-owner",
		takeoverAt,
		time.Minute,
	); err != nil {
		t.Fatalf("过期租约应允许被接管: %v", err)
	}
	port.openGate()
	err := <-releaseDone
	if !errors.Is(err, app.ErrPlanningRuntimeLeaseLost) {
		t.Fatalf("租约被接管后 Release 必须以租约丢失上报，实际: %v", err)
	}
}
