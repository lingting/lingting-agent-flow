package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// 本文件覆盖容器处置契约（delete / detach）。
//
// 处置校验与归属校验回答的是两个不同问题：归属校验要求容器是任务当前引用的那条
// 并且仍可执行；处置校验允许处置历史代次，但保留"终态容器不再接受自动操作"的
// 既有约束。这些规则是 Web 操作入口唯一的跨任务防线，因此必须直接断言。

// disposalOwned 构造一套允许处置的任务与容器记录。
func disposalOwned(t *testing.T) ownershipFixture {
	t.Helper()
	fixture := newOwnershipFixture(t)
	// 处置允许历史代次：把任务引用指向另一条记录，容器本身仍登记在该任务名下。
	fixture.task.ContainerID = ""
	return fixture
}

// TestDecideContainerDisposalAllowsManagedContainer 验证受管理容器可被删除与脱离。
func TestDecideContainerDisposalAllowsManagedContainer(t *testing.T) {
	t.Parallel()
	fixture := disposalOwned(t)
	for _, action := range []domain.RestartDisposition{
		domain.RestartDispositionDelete,
		domain.RestartDispositionDetach,
	} {
		decision := domain.DecideContainerDisposal(fixture.task, fixture.container, action)
		if !decision.Allowed {
			t.Fatalf("%s 应被允许，实际拒绝 %s: %s", action, decision.Reason, decision.Detail)
		}
		if err := domain.CheckContainerDisposal(fixture.task, fixture.container, action); err != nil {
			t.Fatalf("%s 校验不应失败: %v", action, err)
		}
	}
}

// TestDecideContainerDisposalAllowsHistoryGeneration 验证历史代次可被处置。
//
// 这与归属校验的关键差异：任务记录里的 container_id 只保存当前代次，旧代次只能通过
// "登记了任务归属但不是当前引用"被识别。
func TestDecideContainerDisposalAllowsHistoryGeneration(t *testing.T) {
	t.Parallel()
	fixture := disposalOwned(t)
	fixture.task.ContainerID = "another-container"
	decision := domain.DecideContainerDisposal(
		fixture.task,
		fixture.container,
		domain.RestartDispositionDelete,
	)
	if !decision.Allowed {
		t.Fatalf("历史代次应可被处置，实际拒绝 %s: %s", decision.Reason, decision.Detail)
	}
	// 同一份记录在归属校验下必须被拒绝：两者不能合并。
	ownership := domain.DecideContainerOwnership(fixture.task, fixture.container)
	if ownership.Allowed {
		t.Fatal("非任务当前引用的容器不得被当作执行容器使用")
	}
}

// TestDecideContainerDisposalRejectsCrossTask 验证跨任务处置被拒绝。
func TestDecideContainerDisposalRejectsCrossTask(t *testing.T) {
	t.Parallel()
	fixture := disposalOwned(t)
	other := fixture.task
	other.ID = domain.NewID()
	for _, action := range []domain.RestartDisposition{
		domain.RestartDispositionDelete,
		domain.RestartDispositionDetach,
	} {
		decision := domain.DecideContainerDisposal(other, fixture.container, action)
		if decision.Allowed {
			t.Fatalf("%s 不得跨任务执行", action)
		}
		if decision.Reason != domain.ContainerOwnershipTaskMismatch {
			t.Fatalf("拒绝原因应为 %s，实际 %s", domain.ContainerOwnershipTaskMismatch, decision.Reason)
		}
	}
}

// TestDecideContainerDisposalRejectsUnassignedContainer 验证未登记任务归属的容器被拒绝。
func TestDecideContainerDisposalRejectsUnassignedContainer(t *testing.T) {
	t.Parallel()
	fixture := disposalOwned(t)
	container := fixture.container
	container.TaskID = nil
	decision := domain.DecideContainerDisposal(
		fixture.task,
		container,
		domain.RestartDispositionDelete,
	)
	if decision.Allowed || decision.Reason != domain.ContainerOwnershipUnassigned {
		t.Fatalf("未登记归属必须拒绝，实际 %s", decision.Reason)
	}
}

// TestDecideContainerDisposalRejectsHostMismatch 验证节点不一致时被拒绝。
func TestDecideContainerDisposalRejectsHostMismatch(t *testing.T) {
	t.Parallel()
	fixture := disposalOwned(t)
	otherHost := domain.NewID()
	task := fixture.task
	task.HostID = &otherHost
	decision := domain.DecideContainerDisposal(
		task,
		fixture.container,
		domain.RestartDispositionDetach,
	)
	if decision.Allowed || decision.Reason != domain.ContainerOwnershipHostMismatch {
		t.Fatalf("节点不一致必须拒绝，实际 %s", decision.Reason)
	}
}

// TestDecideContainerDisposalRejectsDetachedContainer 验证已脱离管理的容器不接受任何处置。
func TestDecideContainerDisposalRejectsDetachedContainer(t *testing.T) {
	t.Parallel()
	fixture := disposalOwned(t)
	detached := fixture.container
	if err := detached.Detach(time.Now().UTC()); err != nil {
		t.Fatalf("构造已脱离容器失败: %v", err)
	}
	for _, action := range []domain.RestartDisposition{
		domain.RestartDispositionDelete,
		domain.RestartDispositionDetach,
	} {
		decision := domain.DecideContainerDisposal(fixture.task, detached, action)
		if decision.Allowed {
			t.Fatalf("%s 不得作用于已脱离管理的容器", action)
		}
		if decision.Reason != domain.ContainerOwnershipDetached {
			t.Fatalf("拒绝原因应为 %s，实际 %s", domain.ContainerOwnershipDetached, decision.Reason)
		}
		err := domain.CheckContainerDisposal(fixture.task, detached, action)
		if !errors.Is(err, domain.ErrInvalidState) {
			t.Fatalf("拒绝必须包装 ErrInvalidState，实际 %v", err)
		}
	}
}

// TestDecideContainerDisposalRejectsRemovedDetach 验证已删除的容器不能被报告为脱离。
func TestDecideContainerDisposalRejectsRemovedDetach(t *testing.T) {
	t.Parallel()
	fixture := disposalOwned(t)
	removed := fixture.container
	if err := removed.MarkRemoved(domain.ContainerEndReasonManual, time.Now().UTC()); err != nil {
		t.Fatalf("构造已删除容器失败: %v", err)
	}
	decision := domain.DecideContainerDisposal(
		fixture.task,
		removed,
		domain.RestartDispositionDetach,
	)
	if decision.Allowed || decision.Reason != domain.ContainerOwnershipRemoved {
		t.Fatalf("已删除容器不得脱离管理，实际 %s", decision.Reason)
	}
	// 重复删除仍然允许：容器服务把重复删除视为幂等成功，不产生新的远程副作用。
	repeat := domain.DecideContainerDisposal(
		fixture.task,
		removed,
		domain.RestartDispositionDelete,
	)
	if !repeat.Allowed {
		t.Fatalf("重复删除应交给容器服务判定幂等，实际拒绝 %s: %s", repeat.Reason, repeat.Detail)
	}
}

// TestDecideContainerDisposalRejectsContradictoryDetachedRecord 验证自相矛盾的记录被拒绝。
func TestDecideContainerDisposalRejectsContradictoryDetachedRecord(t *testing.T) {
	t.Parallel()
	fixture := disposalOwned(t)
	broken := fixture.container
	broken.State = domain.ContainerStateDetached
	broken.Detached = false
	decision := domain.DecideContainerDisposal(
		fixture.task,
		broken,
		domain.RestartDispositionDelete,
	)
	if decision.Allowed || decision.Reason != domain.ContainerOwnershipDetached {
		t.Fatalf("状态与标记不一致必须拒绝，实际 %s", decision.Reason)
	}
}

// TestDecideContainerDisposalRejectsInvalidAction 验证未知动作被拒绝。
func TestDecideContainerDisposalRejectsInvalidAction(t *testing.T) {
	t.Parallel()
	fixture := disposalOwned(t)
	decision := domain.DecideContainerDisposal(
		fixture.task,
		fixture.container,
		domain.RestartDisposition("stop"),
	)
	if decision.Allowed {
		t.Fatal("未知动作不得被接受")
	}
}
