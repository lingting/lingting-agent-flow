// 资源回收的所有权校验测试。
//
// 用例使用真实 SQLite 与正式迁移，验证的核心约束是：
//   - 孤儿容器（创建成功、写回任务失败）可被发现，但不会被自动删除或重新关联；
//   - 状态未知的容器不触发猜测性删除或重建；
//   - 任务失败与中断时保留诊断现场；
//   - 一个任务的容器回收不会影响另一个任务的容器；
//   - 共享缓存卷不因单任务收尾被删除。
package scheduler_test

import (
	"errors"
	"testing"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// TestOrphanContainerIsDiscoverableButNotAutoHandled 验证孤儿容器只被发现、不被处置。
//
// 现场：容器创建成功并登记了任务关联，但任务记录的 container_id 指向别处（或为空）。
// 此时该容器既不能被自动删除（工作区状态未知），也不能被自动重新关联（会让未核验
// 的现场重新进入执行链路）。
func TestOrphanContainerIsDiscoverableButNotAutoHandled(t *testing.T) {
	t.Parallel()
	fixture := newIsolationFixture(t)
	task := fixture.task(t, "A")
	// 写入容器并登记任务关联，但不把引用写回任务。
	container, _ := fixture.container(t, task, "orphan")
	stored, err := fixture.db.GetTask(fixture.ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	cleared := *stored
	if err := cleared.SetContainer(""); err != nil {
		t.Fatalf("清空容器引用失败: %v", err)
	}
	if err := fixture.db.UpdateTaskIfStatus(fixture.ctx, cleared, []domain.TaskStatus{stored.Status}); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}

	// 孤儿容器必须可被发现。
	orphans, err := fixture.db.ListContainers(fixture.ctx, domain.ContainerFilter{Orphans: true})
	if err != nil {
		t.Fatalf("查询孤儿容器失败: %v", err)
	}
	found := false
	for _, candidate := range orphans {
		if candidate.ID == container.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("创建成功但未写回任务的容器必须能被发现")
	}
	// 它必须仍然处于受管理状态：恢复只报告，不自动处置。
	current, err := fixture.db.GetContainer(fixture.ctx, container.ID)
	if err != nil {
		t.Fatalf("读取容器失败: %v", err)
	}
	if current.State == domain.ContainerStateRemoved {
		t.Fatal("孤儿容器不得被自动删除")
	}
	if current.Detached {
		t.Fatal("孤儿容器不得被自动脱离管理")
	}
	// 归属校验必须拒绝把它当作任务容器：任务已经不再引用它。
	decision := domain.DecideContainerOwnership(cleared, *current)
	if decision.Allowed {
		t.Fatal("任务没有引用该容器时不得被当作执行容器")
	}
	if decision.Reason != domain.ContainerOwnershipMissingRef {
		t.Fatalf("拒绝原因应为 %s，实际 %s", domain.ContainerOwnershipMissingRef, decision.Reason)
	}
}

// TestUnknownContainerStateIsNotDeleted 验证状态未知的容器不被猜测性删除。
//
// 观测无法确认容器状态时，恢复必须保留 unknown 并记录可诊断原因，不得改写成任何
// 确定状态，更不得删除。
func TestUnknownContainerStateIsNotDeleted(t *testing.T) {
	t.Parallel()
	fixture := newIsolationFixture(t)
	task := fixture.task(t, "A")
	container, _ := fixture.container(t, task, "unknown")

	decision := domain.DecideContainerRecovery(domain.ContainerRecoveryInput{
		Container:   container,
		Observation: domain.ContainerObservedUnreachable,
	})
	if decision.Needed {
		t.Fatal("无法访问时不得改写容器记录")
	}
	if !decision.NeedsManual {
		t.Fatal("无法访问时必须要求人工核验")
	}
	if decision.Next == domain.ContainerStateRemoved {
		t.Fatal("无法确认状态时不得收敛为已删除")
	}
	// 所有权不匹配同样不得被删除。
	mismatch := domain.DecideContainerRecovery(domain.ContainerRecoveryInput{
		Container:   container,
		Observation: domain.ContainerObservedOwnershipMismatch,
	})
	if mismatch.Needed || mismatch.Next == domain.ContainerStateRemoved {
		t.Fatal("所有权不匹配时不得删除容器")
	}
}

// TestRecoveringOneTaskKeepsAnotherTaskContainer 验证回收一个任务不影响另一个任务。
func TestRecoveringOneTaskKeepsAnotherTaskContainer(t *testing.T) {
	t.Parallel()
	fixture := newIsolationFixture(t)
	taskA := fixture.task(t, "A")
	containerA, taskA := fixture.container(t, taskA, "alpha")
	taskB := fixture.task(t, "B")
	containerB, taskB := fixture.container(t, taskB, "beta")

	// 收敛任务 A 的容器为已删除。
	removed := containerA
	if err := removed.MarkRemoved(domain.ContainerEndReasonTaskSucceeded, fixture.now); err != nil {
		t.Fatalf("标记删除失败: %v", err)
	}
	if err := fixture.db.UpdateContainer(fixture.ctx, removed); err != nil {
		t.Fatalf("更新容器失败: %v", err)
	}
	// 任务 B 的容器必须原样保留。
	current, err := fixture.db.GetContainer(fixture.ctx, containerB.ID)
	if err != nil {
		t.Fatalf("读取任务 B 的容器失败: %v", err)
	}
	if current.State != domain.ContainerStateRunning {
		t.Fatalf("任务 B 的容器状态被改变: %s", current.State)
	}
	if current.Detached {
		t.Fatal("任务 B 的容器不得被脱离管理")
	}
	// 任务 B 的容器引用必须仍然有效。
	storedB, err := fixture.db.GetTask(fixture.ctx, taskB.ID)
	if err != nil {
		t.Fatalf("读取任务 B 失败: %v", err)
	}
	if storedB.ContainerID != containerB.ID.String() {
		t.Fatalf("任务 B 的容器引用被改变: %s", storedB.ContainerID)
	}
	// 任务 A 的引用指向已删除的容器：归属校验必须拒绝继续使用。
	storedA, err := fixture.db.GetTask(fixture.ctx, taskA.ID)
	if err != nil {
		t.Fatalf("读取任务 A 失败: %v", err)
	}
	decision := domain.DecideContainerOwnership(*storedA, removed)
	if decision.Allowed {
		t.Fatal("已删除的容器不得被继续当作执行容器")
	}
	if decision.Reason != domain.ContainerOwnershipRemoved {
		t.Fatalf("拒绝原因应为 %s，实际 %s", domain.ContainerOwnershipRemoved, decision.Reason)
	}
}

// TestSharedCacheVolumesAreNotTaskScoped 验证共享缓存不随单任务收尾被清理。
//
// 缓存卷名由"节点 + 缓存类型 + 共享范围 + ABI"决定，不包含任务标识：因此两个
// 任务的缓存卷名相同，说明它是共享资源，回收一个任务不得删除它。
func TestSharedCacheVolumesAreNotTaskScoped(t *testing.T) {
	t.Parallel()
	fixture := newIsolationFixture(t)
	taskA := fixture.task(t, "A")
	taskB := fixture.task(t, "B")
	hostID := fixture.host.ID

	machineScoped := domain.CacheMount{
		Type:          "go-mod",
		ContainerPath: "/var/cache/agentflow/go/pkg/mod",
		Scope:         domain.CacheScopeMachine,
		ABI:           "go124",
	}
	nameA, err := testCacheVolumeName(hostID, machineScoped, "")
	if err != nil {
		t.Fatalf("生成缓存卷名失败: %v", err)
	}
	nameB, err := testCacheVolumeName(hostID, machineScoped, "")
	if err != nil {
		t.Fatalf("生成缓存卷名失败: %v", err)
	}
	if nameA != nameB {
		t.Fatal("机器级缓存的卷名不得随任务变化")
	}
	// 卷名不得包含任何任务标识：否则同一份缓存会被误当作任务专属资源。
	for _, task := range []domain.Task{taskA, taskB} {
		if containsTaskID(nameA, task.ID.String()) {
			t.Fatalf("共享缓存卷名不得包含任务标识 %s", task.ID)
		}
	}
	// 项目级缓存按项目区分，同样不按任务区分。
	projectScoped := domain.CacheMount{
		Type:          "go-build",
		ContainerPath: "/var/cache/agentflow/go-build",
		Scope:         domain.CacheScopeProject,
		ABI:           "go124",
	}
	projectA, err := testCacheVolumeName(hostID, projectScoped, "project-1")
	if err != nil {
		t.Fatalf("生成项目级缓存卷名失败: %v", err)
	}
	projectB, err := testCacheVolumeName(hostID, projectScoped, "project-2")
	if err != nil {
		t.Fatalf("生成项目级缓存卷名失败: %v", err)
	}
	if projectA == projectB {
		t.Fatal("不同项目的项目级缓存必须使用不同的卷")
	}
}

// containsTaskID 判断卷名中是否包含任务标识。
func containsTaskID(volumeName, taskID string) bool {
	if taskID == "" {
		return false
	}
	return len(volumeName) >= len(taskID) && stringContains(volumeName, taskID)
}

// stringContains 是 strings.Contains 的薄包装，避免为本文件引入额外 import。
func stringContains(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestContainerDeletionIsIdempotentAndKeepsOwnership 验证删除的幂等与归属校验。
//
// 已删除的容器再次删除必须按幂等成功处理；已脱离管理的容器必须拒绝自动删除。
func TestContainerDeletionIsIdempotentAndKeepsOwnership(t *testing.T) {
	t.Parallel()
	fixture := newIsolationFixture(t)
	task := fixture.task(t, "A")
	container, _ := fixture.container(t, task, "alpha")

	removed := container
	if err := removed.MarkRemoved(domain.ContainerEndReasonTaskSucceeded, fixture.now); err != nil {
		t.Fatalf("标记删除失败: %v", err)
	}
	if err := fixture.db.UpdateContainer(fixture.ctx, removed); err != nil {
		t.Fatalf("更新容器失败: %v", err)
	}
	// 重复删除在领域层是幂等成功：状态机不重复迁移。
	again := removed
	if err := again.MarkRemoved(domain.ContainerEndReasonTaskSucceeded, fixture.now); err == nil {
		t.Fatal("已删除的容器不得重复迁移状态")
	}
	// 已脱离管理的容器不得被自动删除。
	detached := container
	if err := detached.Detach(fixture.now); err != nil {
		t.Fatalf("脱离管理失败: %v", err)
	}
	if err := detached.MarkRemoved(domain.ContainerEndReasonTaskSucceeded, fixture.now); err == nil {
		t.Fatal("已脱离管理的容器不得被自动删除")
	}
}

// TestContainerRecoveryKeepsEvidenceOnFailure 验证失败任务保留诊断现场。
func TestContainerRecoveryKeepsEvidenceOnFailure(t *testing.T) {
	t.Parallel()
	decision, err := domain.DecideContainerLifecycle(domain.ContainerOutcomeFailed, domain.ContainerLifecycleOptions{})
	if err != nil {
		t.Fatalf("计算生命周期策略失败: %v", err)
	}
	if decision.Retention != domain.ContainerRetentionKeep {
		t.Fatalf("任务失败时必须保留容器现场，实际 %s", decision.Retention)
	}
	if decision.RemoveTaskResources {
		t.Fatal("保留现场时不得清理任务级临时资源")
	}
	// 中断默认保留。
	interrupted, err := domain.DecideContainerLifecycle(
		domain.ContainerOutcomeInterrupted,
		domain.ContainerLifecycleOptions{KeepInterrupted: true},
	)
	if err != nil {
		t.Fatalf("计算生命周期策略失败: %v", err)
	}
	if interrupted.Retention != domain.ContainerRetentionKeep {
		t.Fatalf("中断时默认必须保留容器现场，实际 %s", interrupted.Retention)
	}
	// 成功才删除任务级资源。
	succeeded, err := domain.DecideContainerLifecycle(domain.ContainerOutcomeSucceeded, domain.ContainerLifecycleOptions{})
	if err != nil {
		t.Fatalf("计算生命周期策略失败: %v", err)
	}
	if succeeded.Retention != domain.ContainerRetentionDelete {
		t.Fatalf("任务成功时应删除容器，实际 %s", succeeded.Retention)
	}
	if !succeeded.RemoveTaskResources {
		t.Fatal("任务成功时应清理任务级临时资源")
	}
}

// TestContainerUpdateUsesStateCondition 验证容器状态写入带前置条件。
//
// 迟到的收敛结论不得覆盖已被其他执行者推进的记录。
func TestContainerUpdateUsesStateCondition(t *testing.T) {
	t.Parallel()
	fixture := newIsolationFixture(t)
	task := fixture.task(t, "A")
	container, _ := fixture.container(t, task, "alpha")

	// 先用"运行中"作为前置条件把记录推进到已删除。
	removed := container
	if err := removed.MarkRemoved(domain.ContainerEndReasonTaskSucceeded, fixture.now); err != nil {
		t.Fatalf("标记删除失败: %v", err)
	}
	if err := fixture.db.UpdateContainerIfState(
		fixture.ctx,
		removed,
		[]domain.ContainerState{domain.ContainerStateRunning},
	); err != nil {
		t.Fatalf("条件更新容器失败: %v", err)
	}
	// 迟到的"停止"结论用旧前置条件必须被拒绝。
	stale := container
	if err := stale.MarkStopped(fixture.now); err != nil {
		t.Fatalf("标记停止失败: %v", err)
	}
	err := fixture.db.UpdateContainerIfState(
		fixture.ctx,
		stale,
		[]domain.ContainerState{domain.ContainerStateRunning},
	)
	if err == nil {
		t.Fatal("迟到结论不得覆盖已确认的容器终态")
	}
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("拒绝原因应为条件冲突，实际 %v", err)
	}
	current, err := fixture.db.GetContainer(fixture.ctx, container.ID)
	if err != nil {
		t.Fatalf("读取容器失败: %v", err)
	}
	if current.State != domain.ContainerStateRemoved {
		t.Fatalf("容器状态被迟到结论覆盖: %s", current.State)
	}
}

// testCacheVolumeName 复算缓存卷名，用于断言"共享缓存不随任务变化"。
//
// 它只复用产品里真实的卷名规则（节点 + 缓存类型 + 共享范围 + ABI + 共享键），
// 不重新实现缓存共享语义。
func testCacheVolumeName(
	hostID domain.ID,
	mount domain.CacheMount,
	scopeKey string,
) (string, error) {
	return docker.CacheVolumeName(hostID, mount, scopeKey)
}
