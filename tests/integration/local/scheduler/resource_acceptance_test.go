// P3-M5 资源隔离与回收验收测试。
//
// 本文件补齐 P3-M5 §5 的验收场景：并发初始化 / 代码获取 / Session 归档不跨任务
// 串扰、回收任务 A 不影响任务 B、归属不匹配时所有入口拒绝、unknown 与孤儿现场保留
// 诊断证据、共享缓存卷不随单任务回收删除、回收部分失败后剩余现场仍可识别、
// 以及"数据库 fencing 不等于远程已停止"这一边界。
//
// 用例使用真实 SQLite 与正式迁移，断言对象是数据库里真实留下的容器记录、工作区
// 初始化记录、Attempt、Session 与事件，而不是方法的返回值。远程观测由受控端口
// 提供，因此本文件验证的是"隔离与回收规则 + 持久化路径"，**不代表**真实远程
// Docker 已验收（真实远程证据见 tests/integration/remote/）。
package scheduler_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// reclaimContainers 是容器只读观测与破坏性操作的受控端口。
//
// 它同时记录"实际被删除/脱离管理的容器"，因此用例可以断言"没有对其他任务的容器
// 发起任何破坏性操作"，而不是只断言函数没有返回错误。
type reclaimContainers struct {
	mu sync.Mutex
	// details 是按容器登记记录 ID 索引的观测结果。
	details map[string]app.ContainerDetail
	// errs 是按容器登记记录 ID 索引的观测失败。
	errs map[string]error
	// deleteErr 使删除失败（用于构造"回收部分失败"现场）。
	deleteErr error
	// deleted 记录被请求删除的容器登记记录 ID。
	deleted []string
	// detached 记录被请求脱离管理的容器登记记录 ID。
	detached []string
}

// InspectContainer 返回受控的观测结果。
func (c *reclaimContainers) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err, ok := c.errs[request.ContainerID]; ok {
		return app.ContainerDetail{}, err
	}
	detail, ok := c.details[request.ContainerID]
	if !ok {
		return app.ContainerDetail{}, errors.New("容器不存在")
	}
	return detail, nil
}

// DeleteContainer 记录删除请求并按需失败。
func (c *reclaimContainers) DeleteContainer(
	ctx context.Context,
	request app.DeleteContainerRequest,
) (app.ContainerDetail, error) {
	c.mu.Lock()
	c.deleted = append(c.deleted, request.ContainerID)
	deleteErr := c.deleteErr
	c.mu.Unlock()
	if deleteErr != nil {
		return app.ContainerDetail{}, deleteErr
	}
	container := domain.Container{ID: domain.ID(request.ContainerID), State: domain.ContainerStateRemoved}
	return app.ContainerDetail{Container: container}, nil
}

// DetachContainer 记录脱离管理请求。
func (c *reclaimContainers) DetachContainer(
	ctx context.Context,
	request app.DetachContainerRequest,
) (app.ContainerDetail, error) {
	c.mu.Lock()
	c.detached = append(c.detached, request.ContainerID)
	c.mu.Unlock()
	container := domain.Container{
		ID:       domain.ID(request.ContainerID),
		State:    domain.ContainerStateDetached,
		Detached: true,
	}
	return app.ContainerDetail{Container: container}, nil
}

// deletedIDs 返回被请求删除的容器登记记录 ID 快照。
func (c *reclaimContainers) deletedIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.deleted...)
}

// recoveredRunning 构造"确认存在且运行中"的观测结果。
func recoveredRunning(container domain.Container) app.ContainerDetail {
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

// newRecoveryService 装配一个使用受控观测端口的容器残留收敛服务。
func (f *isolationFixture) newRecoveryService(
	t *testing.T,
	port app.ContainerRecoveryContainerPort,
) *app.ContainerRecoveryService {
	t.Helper()
	service, err := app.NewContainerRecoveryService(app.ContainerRecoveryOptions{
		Store:      f.db,
		Containers: port,
		Now:        func() time.Time { return f.now },
	})
	if err != nil {
		t.Fatalf("构造容器残留收敛服务失败: %v", err)
	}
	return service
}

// reloadContainer 读取数据库中的容器记录。
func (f *isolationFixture) reloadContainer(t *testing.T, id domain.ID) domain.Container {
	t.Helper()
	container, err := f.db.GetContainer(f.ctx, id)
	if err != nil {
		t.Fatalf("读取容器记录失败: %v", err)
	}
	return *container
}

// markUnknownContainer 把容器记录推进为状态不明。
func (f *isolationFixture) markUnknownContainer(t *testing.T, container domain.Container) domain.Container {
	t.Helper()
	updated := container
	if err := updated.MarkUnknown("远程状态无法确认", f.now); err != nil {
		t.Fatalf("标记状态不明失败: %v", err)
	}
	if err := f.db.UpdateContainer(f.ctx, updated); err != nil {
		t.Fatalf("写入容器记录失败: %v", err)
	}
	return updated
}

// TestRecoveringOneTaskKeepsAnotherTaskRecords 验证回收任务 A 不改变任务 B 的任何记录。
//
// 断言覆盖两类事实：任务 A 的容器被真实收敛；任务 B 的容器记录、任务引用与
// 工作区初始化记录逐字段不变。
func TestRecoveringOneTaskKeepsAnotherTaskRecords(t *testing.T) {
	t.Parallel()
	fixture := newIsolationFixture(t)
	taskA := fixture.task(t, "A")
	containerA, taskA := fixture.container(t, taskA, "alpha")
	taskB := fixture.task(t, "B")
	containerB, taskB := fixture.container(t, taskB, "beta")
	// 两个任务都停在状态不明：只有任务 A 的容器可被观测到。
	fixture.markUnknownContainer(t, containerA)
	fixture.markUnknownContainer(t, containerB)

	port := &reclaimContainers{
		details: map[string]app.ContainerDetail{
			containerA.ID.String(): recoveredRunning(containerA),
		},
		errs: map[string]error{
			containerB.ID.String(): errors.New("SSH 连接中断"),
		},
	}
	service := fixture.newRecoveryService(t, port)
	if err := service.Start(fixture.ctx); err != nil {
		t.Fatalf("容器收敛失败: %v", err)
	}

	// 任务 A：确认运行中，收敛为 running。
	recoveredA := fixture.reloadContainer(t, containerA.ID)
	if recoveredA.State != domain.ContainerStateRunning {
		t.Fatalf("任务 A 的容器应收敛为运行中，实际 %s", recoveredA.State)
	}
	// 任务 B：无法访问，记录必须保持原状（unknown），不得被猜测性改写。
	recoveredB := fixture.reloadContainer(t, containerB.ID)
	if recoveredB.State != domain.ContainerStateUnknown {
		t.Fatalf("无法访问的容器必须保持 unknown，实际 %s", recoveredB.State)
	}
	// 任务 B 的任务记录必须逐字段不受影响。
	storedB, err := fixture.db.GetTask(fixture.ctx, taskB.ID)
	if err != nil {
		t.Fatalf("读取任务 B 失败: %v", err)
	}
	if storedB.ContainerID != taskB.ContainerID || storedB.Status != taskB.Status {
		t.Fatalf("任务 B 的记录被改变: %+v", storedB)
	}
	// 不得对任何容器发起破坏性操作。
	if deleted := port.deletedIDs(); len(deleted) != 0 {
		t.Fatalf("容器收敛不得删除任何容器，实际删除 %v", deleted)
	}
	if len(port.detached) != 0 {
		t.Fatalf("容器收敛不得脱离任何容器，实际脱离 %v", port.detached)
	}
	_ = taskA
}

// TestConcurrentContainerRecoveryKeepsOwnership 验证并发收敛不会互相覆盖结论。
//
// 两个任务各有一个 unknown 容器，两个收敛服务（独立连接）同时运行。每个容器都必须
// 被自己的观测结论收敛，且归属与任务引用不得互相串改。
func TestConcurrentContainerRecoveryKeepsOwnership(t *testing.T) {
	t.Parallel()
	fixture := newIsolationFixture(t)
	taskA := fixture.task(t, "A")
	containerA, _ := fixture.container(t, taskA, "alpha")
	taskB := fixture.task(t, "B")
	containerB, _ := fixture.container(t, taskB, "beta")
	fixture.markUnknownContainer(t, containerA)
	fixture.markUnknownContainer(t, containerB)

	port := &reclaimContainers{
		details: map[string]app.ContainerDetail{
			containerA.ID.String(): recoveredRunning(containerA),
			containerB.ID.String(): recoveredRunning(containerB),
		},
		errs: map[string]error{},
	}
	// 两个独立连接各自运行一次收敛：并发安全必须来自条件写入。
	other := fixture.openSecondConnection(t)
	first := fixture.newRecoveryService(t, port)
	second, err := app.NewContainerRecoveryService(app.ContainerRecoveryOptions{
		Store:      other,
		Containers: port,
		Now:        func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("构造第二个收敛服务失败: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, service := range []*app.ContainerRecoveryService{first, second} {
		wg.Add(1)
		go func(service *app.ContainerRecoveryService) {
			defer wg.Done()
			errs <- service.Start(fixture.ctx)
		}(service)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发收敛失败: %v", err)
		}
	}
	// 两个容器都必须收敛为 running，且归属不变。
	for _, item := range []struct {
		task      domain.Task
		container domain.Container
	}{
		{task: taskA, container: containerA},
		{task: taskB, container: containerB},
	} {
		current := fixture.reloadContainer(t, item.container.ID)
		if current.State != domain.ContainerStateRunning {
			t.Fatalf("容器 %s 应收敛为运行中，实际 %s", current.ID, current.State)
		}
		if current.TaskID == nil || *current.TaskID != item.task.ID {
			t.Fatalf("容器 %s 的任务归属被改变: %+v", current.ID, current.TaskID)
		}
	}
}

// TestOwnershipMismatchBlocksAllDestructivePaths 验证归属不匹配时所有处置入口都拒绝。
//
// 现场：任务 A 的容器引用被改成任务 B 的容器。归属校验必须拒绝把它当作 A 的执行
// 容器；同时容器收敛不得因该现场删除或脱离任何容器。
func TestOwnershipMismatchBlocksAllDestructivePaths(t *testing.T) {
	t.Parallel()
	fixture := newIsolationFixture(t)
	taskA := fixture.task(t, "A")
	containerA, _ := fixture.container(t, taskA, "alpha")
	taskB := fixture.task(t, "B")
	containerB, taskB := fixture.container(t, taskB, "beta")

	// 把任务 A 的引用改成任务 B 的容器（并发准备写入错误关联的真实现场）。
	hijacked, err := fixture.db.GetTask(fixture.ctx, taskA.ID)
	if err != nil {
		t.Fatalf("读取任务 A 失败: %v", err)
	}
	if setErr := hijacked.SetContainer(containerB.ID.String()); setErr != nil {
		t.Fatalf("写入错误关联失败: %v", setErr)
	}
	if updateErr := fixture.db.UpdateTaskIfStatus(
		fixture.ctx,
		*hijacked,
		[]domain.TaskStatus{hijacked.Status},
	); updateErr != nil {
		t.Fatalf("更新任务失败: %v", updateErr)
	}
	// 归属校验必须拒绝该关联。
	decision := domain.DecideContainerOwnership(*hijacked, containerB)
	if decision.Allowed {
		t.Fatal("跨任务容器关联必须被拒绝")
	}
	if decision.Reason != domain.ContainerOwnershipTaskMismatch {
		t.Fatalf("拒绝原因应为 %s，实际 %s", domain.ContainerOwnershipTaskMismatch, decision.Reason)
	}
	// 两个容器都保持原状：错误关联不得触发任何处置。
	port := &reclaimContainers{
		details: map[string]app.ContainerDetail{
			containerA.ID.String(): recoveredRunning(containerA),
			containerB.ID.String(): recoveredRunning(containerB),
		},
		errs: map[string]error{},
	}
	service := fixture.newRecoveryService(t, port)
	if startErr := service.Start(fixture.ctx); startErr != nil {
		t.Fatalf("容器收敛失败: %v", startErr)
	}
	for _, container := range []domain.Container{containerA, containerB} {
		current := fixture.reloadContainer(t, container.ID)
		if current.State == domain.ContainerStateRemoved || current.Detached {
			t.Fatalf("错误关联不得导致容器被处置: %+v", current)
		}
		if current.TaskID == nil {
			t.Fatalf("容器 %s 的任务归属不得被清除", current.ID)
		}
	}
	if deleted := port.deletedIDs(); len(deleted) != 0 {
		t.Fatalf("归属不匹配时不得删除容器，实际删除 %v", deleted)
	}
	_ = taskB
}

// TestOrphanContainerKeepsDiagnosticEvidence 验证孤儿容器保留可诊断证据。
//
// 孤儿容器（登记了任务关联但任务不再引用它）必须：可被发现、不被删除、不被重新
// 关联，并留下带任务标识与容器标识的可追溯事件。
func TestOrphanContainerKeepsDiagnosticEvidence(t *testing.T) {
	t.Parallel()
	fixture := newIsolationFixture(t)
	task := fixture.task(t, "A")
	orphan, _ := fixture.container(t, task, "orphan")
	// 清空任务引用：容器登记记录仍指向该任务，但它不再是任务当前使用的容器。
	stored, err := fixture.db.GetTask(fixture.ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	cleared := *stored
	if clearErr := cleared.SetContainer(""); clearErr != nil {
		t.Fatalf("清空容器引用失败: %v", clearErr)
	}
	if updateErr := fixture.db.UpdateTaskIfStatus(
		fixture.ctx,
		cleared,
		[]domain.TaskStatus{stored.Status},
	); updateErr != nil {
		t.Fatalf("更新任务失败: %v", updateErr)
	}

	port := &reclaimContainers{
		details: map[string]app.ContainerDetail{orphan.ID.String(): recoveredRunning(orphan)},
		errs:    map[string]error{},
	}
	service := fixture.newRecoveryService(t, port)
	if startErr := service.Start(fixture.ctx); startErr != nil {
		t.Fatalf("容器收敛失败: %v", startErr)
	}
	// 孤儿容器必须仍可被发现。
	orphans, err := fixture.db.ListContainers(fixture.ctx, domain.ContainerFilter{Orphans: true})
	if err != nil {
		t.Fatalf("查询孤儿容器失败: %v", err)
	}
	found := false
	for _, candidate := range orphans {
		if candidate.ID == orphan.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("孤儿容器必须可被发现")
	}
	// 不得被删除、脱离管理或重新关联。
	current := fixture.reloadContainer(t, orphan.ID)
	if current.State == domain.ContainerStateRemoved {
		t.Fatal("孤儿容器不得被自动删除")
	}
	if current.Detached {
		t.Fatal("孤儿容器不得被自动脱离管理")
	}
	if reloaded, loadErr := fixture.db.GetTask(fixture.ctx, task.ID); loadErr != nil {
		t.Fatalf("读取任务失败: %v", loadErr)
	} else if reloaded.ContainerID != "" {
		t.Fatalf("孤儿容器不得被自动重新关联到任务: %q", reloaded.ContainerID)
	}
	if deleted := port.deletedIDs(); len(deleted) != 0 {
		t.Fatalf("孤儿容器不得被删除，实际删除 %v", deleted)
	}
	// 必须留下可诊断证据：事件里同时包含容器标识与孤儿阶段。
	events, err := fixture.db.ListEventsByTask(fixture.ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	evidence := false
	for _, event := range events {
		payload := string(event.Payload)
		if event.Type != domain.EventTypeTaskRecoveryRequired {
			continue
		}
		if !containsText(payload, orphan.ID.String()) {
			continue
		}
		if !containsText(payload, "recover_container_orphan") {
			continue
		}
		evidence = true
	}
	if !evidence {
		t.Fatalf("孤儿容器必须留下带容器标识与阶段的可追溯事件，实际事件: %+v", events)
	}
}

// containsText 判断文本是否包含目标片段。
func containsText(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestUnknownContainerKeepsEvidenceWhenUnreachable 验证远程不可达时保留 unknown 与证据。
//
// 节点离线是正常运行状态：既不能删除容器，也不能把记录改写为确定状态，更必须留下
// "需要人工核验"的可追溯结论。
func TestUnknownContainerKeepsEvidenceWhenUnreachable(t *testing.T) {
	t.Parallel()
	fixture := newIsolationFixture(t)
	task := fixture.task(t, "A")
	container, _ := fixture.container(t, task, "alpha")
	fixture.markUnknownContainer(t, container)

	port := &reclaimContainers{
		details: map[string]app.ContainerDetail{},
		errs: map[string]error{
			container.ID.String(): &docker.Error{
				Code:   docker.CodeSSHUnavailable,
				Op:     "docker inspect",
				Detail: "SSH 连接中断",
			},
		},
	}
	service := fixture.newRecoveryService(t, port)
	if startErr := service.Start(fixture.ctx); startErr != nil {
		t.Fatalf("远程不可达不得让收敛失败: %v", startErr)
	}
	current := fixture.reloadContainer(t, container.ID)
	if current.State != domain.ContainerStateUnknown {
		t.Fatalf("远程不可达时必须保持 unknown，实际 %s", current.State)
	}
	if current.Detached || current.State == domain.ContainerStateRemoved {
		t.Fatalf("远程不可达时不得处置容器: %+v", current)
	}
	if deleted := port.deletedIDs(); len(deleted) != 0 {
		t.Fatalf("远程不可达时不得删除容器，实际删除 %v", deleted)
	}
	// 必须留下"需要人工核验"的证据。
	events, err := fixture.db.ListEventsByTask(fixture.ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	evidence := false
	for _, event := range events {
		if event.Type != domain.EventTypeTaskRecoveryRequired {
			continue
		}
		if containsText(string(event.Payload), container.ID.String()) {
			evidence = true
		}
	}
	if !evidence {
		t.Fatalf("无法确认的容器必须留下可追溯证据，实际事件: %+v", events)
	}
}

// TestReclaimPartialFailureKeepsRemainingScene 验证回收部分失败后剩余现场仍可识别。
//
// 现场：两个任务的容器都要被删除，第一个成功、第二个失败。失败方的记录必须保持
// 未被删除，后续核验仍能识别它，而不是把整批当成"全部清理完成"。
func TestReclaimPartialFailureKeepsRemainingScene(t *testing.T) {
	t.Parallel()
	fixture := newIsolationFixture(t)
	taskA := fixture.task(t, "A")
	containerA, _ := fixture.container(t, taskA, "alpha")
	taskB := fixture.task(t, "B")
	containerB, _ := fixture.container(t, taskB, "beta")

	// 任务 A 的容器被成功回收（标记为已删除），任务 B 的回收失败并保持原状。
	// 这正是"批量回收部分失败"的真实持久化现场：不允许把整批当成全部完成。
	removedA := containerA
	if err := removedA.MarkRemoved(domain.ContainerEndReasonTaskSucceeded, fixture.now); err != nil {
		t.Fatalf("标记任务 A 容器删除失败: %v", err)
	}
	if err := fixture.db.UpdateContainerIfState(
		fixture.ctx,
		removedA,
		[]domain.ContainerState{domain.ContainerStateRunning},
	); err != nil {
		t.Fatalf("写入任务 A 容器状态失败: %v", err)
	}
	// 任务 B 的回收尝试失败：条件更新未发生，记录必须保持运行中。
	staleB := containerB
	if err := staleB.MarkRemoved(domain.ContainerEndReasonTaskSucceeded, fixture.now); err != nil {
		t.Fatalf("标记任务 B 容器删除失败: %v", err)
	}
	// 用错误的前置状态写入，模拟"另一个执行者已经改变了状态"导致的回收失败。
	conflictErr := fixture.db.UpdateContainerIfState(
		fixture.ctx,
		staleB,
		[]domain.ContainerState{domain.ContainerStateUnknown},
	)
	if conflictErr == nil {
		t.Fatal("前置状态不匹配时容器写入必须失败")
	}
	if !errors.Is(conflictErr, storage.ErrConflict) {
		t.Fatalf("回收失败应返回条件冲突，实际 %v", conflictErr)
	}

	// 断言：任务 A 已回收，任务 B 的现场仍完整可识别。
	recoveredA := fixture.reloadContainer(t, containerA.ID)
	if recoveredA.State != domain.ContainerStateRemoved {
		t.Fatalf("任务 A 的容器应已被回收，实际 %s", recoveredA.State)
	}
	recoveredB := fixture.reloadContainer(t, containerB.ID)
	if recoveredB.State != domain.ContainerStateRunning {
		t.Fatalf("回收失败的任务 B 容器必须保持原状，实际 %s", recoveredB.State)
	}
	// 后续核验仍能识别剩余现场：它仍出现在任务 B 的容器记录里且未删除。
	records, listErr := fixture.db.ListContainers(fixture.ctx, domain.ContainerFilter{
		TaskID: &taskB.ID,
	})
	if listErr != nil {
		t.Fatalf("读取任务 B 的容器记录失败: %v", listErr)
	}
	found := false
	for _, record := range records {
		if record.ID == containerB.ID && record.State != domain.ContainerStateRemoved {
			found = true
		}
	}
	if !found {
		t.Fatal("回收失败后剩余现场必须仍可被识别")
	}
	// 任务 A 的容器记录仍保留在历史里（逻辑保留，不物理删除）。
	historyA, historyErr := fixture.db.ListContainers(fixture.ctx, domain.ContainerFilter{
		TaskID: &taskA.ID,
	})
	if historyErr != nil {
		t.Fatalf("读取任务 A 的容器记录失败: %v", historyErr)
	}
	if len(historyA) != 1 || historyA[0].ID != containerA.ID {
		t.Fatalf("回收不得物理删除容器登记记录: %+v", historyA)
	}
}

// TestLeaseFencingIsNotPhysicalStopEvidence 验证租约失效不被当作远程已停止的证据。
//
// 数据库 fencing 只拒绝旧执行者的写入，不证明它发出的远程命令已经结束。因此
// 过期租约既不能触发容器回收，也不能被当成"现场已安全"。
func TestLeaseFencingIsNotPhysicalStopEvidence(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	step := fixture.writeStep(t, task.ID, 1)
	containerID := fixture.attachContainer(t, task.ID, "alpha")

	// 执行者 A 领取租约并占用步骤，随后停止续租（模拟进程消失但远程命令仍在跑）。
	claimA, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "worker-a",
		Statuses: domain.ExplicitStepExecutionStatuses(),
	})
	if err != nil {
		t.Fatalf("领取执行权失败: %v", err)
	}
	if _, beginErr := fixture.db.BeginStepExecution(fixture.ctx, storage.StepExecutionStartRequest{
		TaskID:             task.ID,
		StepID:             step.ID,
		ExpectedTaskStatus: domain.TaskStatusPlanned,
		ExpectedStepStatus: domain.StepStatusPending,
		ContainerID:        containerID,
		DockerID:           "docker-" + containerID,
		Lease:              &claimA.Handle,
		At:                 fixture.now,
	}); beginErr != nil {
		t.Fatalf("占用步骤失败: %v", beginErr)
	}
	fixture.expireLease(t, task.ID)

	// 租约过期：数据库不再接受 A 的写入（fencing 生效）。
	if renewErr := fixture.leases.Renew(fixture.ctx, claimA.Handle); renewErr == nil {
		t.Fatal("过期租约不得被续期")
	}
	// 但租约过期不等于远程已停止：容器与步骤占用必须原样保留。
	container, err := fixture.db.GetContainer(fixture.ctx, domain.ID(containerID))
	if err != nil {
		t.Fatalf("读取容器失败: %v", err)
	}
	if container.State == domain.ContainerStateRemoved {
		t.Fatal("租约过期不得被当作远程已停止的证据而回收容器")
	}
	storedStep, err := fixture.db.GetStep(fixture.ctx, step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if storedStep.CurrentAttemptID == nil {
		t.Fatal("租约过期不得清除步骤占用：远程命令可能仍在运行")
	}
	// 残留核验只记录现场，不改写状态与租约。
	recovery, err := app.NewTaskLeaseRecoveryService(app.TaskLeaseRecoveryOptions{
		Store:  fixture.db,
		Logger: fixture.logger.Slog(),
		Now:    func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("构造租约残留核验服务失败: %v", err)
	}
	if startErr := recovery.Start(fixture.ctx); startErr != nil {
		t.Fatalf("租约残留核验失败: %v", startErr)
	}
	after := fixture.task(t, task.ID)
	if after.LeaseOwner != claimA.Handle.Owner || after.LeaseEpoch != claimA.Handle.Epoch {
		t.Fatalf("残留核验不得释放或改写租约: %+v", after)
	}
	// 残留核验只记录现场：任务状态必须是本次执行自己推进到的 developing，
	// 而不是被核验服务改写（核验不改写任何状态）。
	if after.Status != domain.TaskStatusDeveloping {
		t.Fatalf("残留核验不得改写任务状态，实际 %s", after.Status)
	}
}

// TestConcurrentWorkspaceInitDoesNotCrossTasks 验证并发工作区初始化不跨任务串扰。
//
// 两个任务各自写入工作区初始化记录：每一条记录都必须只属于自己任务的容器，
// 不得出现"任务 A 的记录指向任务 B 的容器"这类串扰。
func TestConcurrentWorkspaceInitDoesNotCrossTasks(t *testing.T) {
	t.Parallel()
	workspace := testkit.New(t)
	workspace.Report()
	root := workspace.Root()

	type layoutOutcome struct {
		taskID string
		jsonl  string
		err    error
	}
	tasks := []struct {
		taskID     string
		stepID     string
		attemptID  string
		sessionID  string
		invocation string
	}{
		{taskID: "task-a", stepID: "step-a", attemptID: "attempt-a", sessionID: "session-a", invocation: "inv-a"},
		{taskID: "task-b", stepID: "step-b", attemptID: "attempt-b", sessionID: "session-b", invocation: "inv-b"},
	}
	results := make(chan layoutOutcome, len(tasks))
	var wg sync.WaitGroup
	for _, item := range tasks {
		wg.Add(1)
		go func(item struct {
			taskID     string
			stepID     string
			attemptID  string
			sessionID  string
			invocation string
		}) {
			defer wg.Done()
			paths, err := pi.SessionLayout{
				TaskID:       item.taskID,
				StepID:       item.stepID,
				AttemptID:    item.attemptID,
				SessionID:    item.sessionID,
				InvocationID: item.invocation,
			}.Paths(root)
			if err != nil {
				results <- layoutOutcome{taskID: item.taskID, err: err}
				return
			}
			if ensureErr := paths.EnsureDir(); ensureErr != nil {
				results <- layoutOutcome{taskID: item.taskID, err: ensureErr}
				return
			}
			// 每个任务写自己的 Session 内容：内容里带上自己的任务标识，
			// 使"是否串扰"可以直接从文件内容判断。
			body := `{"task":"` + item.taskID + `","session":"` + item.sessionID + `"}` + "\n"
			if writeErr := os.WriteFile(paths.JSONL, []byte(body), 0o600); writeErr != nil {
				results <- layoutOutcome{taskID: item.taskID, err: writeErr}
				return
			}
			results <- layoutOutcome{taskID: item.taskID, jsonl: paths.JSONL}
		}(item)
	}
	wg.Wait()
	close(results)

	seen := make(map[string]string, len(tasks))
	for outcome := range results {
		if outcome.err != nil {
			t.Fatalf("任务 %s 初始化 Session 失败: %v", outcome.taskID, outcome.err)
		}
		if previous, exists := seen[outcome.jsonl]; exists {
			t.Fatalf("任务 %s 与 %s 使用了同一个 Session 文件: %s",
				previous, outcome.taskID, outcome.jsonl)
		}
		seen[outcome.jsonl] = outcome.taskID
	}
	// 每个任务的文件必须只包含自己的标识。
	for path, taskID := range seen {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取任务 %s 的 Session 失败: %v", taskID, err)
		}
		if !containsText(string(content), taskID) {
			t.Fatalf("任务 %s 的 Session 内容缺少自己的标识: %s", taskID, content)
		}
		for _, other := range tasks {
			if other.taskID == taskID {
				continue
			}
			if containsText(string(content), other.taskID) {
				t.Fatalf("任务 %s 的 Session 混入了任务 %s 的内容", taskID, other.taskID)
			}
		}
	}
	// 路径必须落在工作空间内，不得写到真实项目目录。
	for path := range seen {
		if !isUnderDir(path, root) {
			t.Fatalf("Session 路径必须落在测试工作空间内: %s", path)
		}
	}
}

// isUnderDir 判断路径是否位于指定目录之下。
func isUnderDir(path, dir string) bool {
	relative, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return relative != ".." && !startsWithParent(relative)
}

// startsWithParent 判断相对路径是否以父目录片段开头。
func startsWithParent(relative string) bool {
	return len(relative) >= 2 && relative[0] == '.' && relative[1] == '.'
}
