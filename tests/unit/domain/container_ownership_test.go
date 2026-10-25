package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// 本文件覆盖容器归属契约：任务记录的 container_id 只是普通文本列，必须由容器
// 登记记录本身证明归属，才能被当作任务的执行容器使用。

// ownershipFixture 是一套归属校验用的任务与容器记录。
type ownershipFixture struct {
	task      domain.Task
	container domain.Container
}

// newOwnershipFixture 构造一套归属成立的任务与容器记录。
func newOwnershipFixture(t *testing.T) ownershipFixture {
	t.Helper()
	hostID := domain.NewID()
	task, err := domain.NewTask(domain.NewID(), "ssh://git.example.com/team/repo", "main", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &hostID
	container, err := domain.NewContainer(
		domain.NewID(),
		hostID,
		"agentflow-abcdef123456",
		domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		domain.FormatVersionLabel(1),
		testContainerHash(t),
		testContainerImageID(),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	taskID := task.ID
	container.TaskID = &taskID
	container.DockerID = "docker-" + container.ID.String()
	container.State = domain.ContainerStateRunning
	container.DockerState = domain.DockerStateRunning
	task.ContainerID = container.ID.String()
	return ownershipFixture{task: task, container: container}
}

// testContainerHash 返回合法的构建配置哈希。
func testContainerHash(t *testing.T) string {
	t.Helper()
	return "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}

// testContainerImageID 返回合法的镜像 ID。
func testContainerImageID() string {
	return "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}

// TestDecideContainerOwnershipAcceptsOwnedContainer 验证归属成立的记录被接受。
func TestDecideContainerOwnershipAcceptsOwnedContainer(t *testing.T) {
	t.Parallel()
	fixture := newOwnershipFixture(t)
	decision := domain.DecideContainerOwnership(fixture.task, fixture.container)
	if !decision.Allowed {
		t.Fatalf("归属成立时必须接受，实际拒绝原因 %s: %s", decision.Reason, decision.Detail)
	}
	if decision.Reason != domain.ContainerOwnershipAllowed {
		t.Fatalf("接受时不应给出拒绝原因，实际 %s", decision.Reason)
	}
	if err := domain.CheckContainerOwnership(fixture.task, fixture.container); err != nil {
		t.Fatalf("归属成立时不应返回错误: %v", err)
	}
}

// TestDecideContainerOwnershipRejectsForeignTask 验证跨任务串扰被拒绝。
//
// 这是最重要的防线：容器名称唯一、容器里有一个工作区，都不能替代"登记的是哪个
// 任务"。两条任务引用同一条容器记录时，必须至少有一方被拒绝。
func TestDecideContainerOwnershipRejectsForeignTask(t *testing.T) {
	t.Parallel()
	fixture := newOwnershipFixture(t)
	otherHost := domain.NewID()
	other, err := domain.NewTask(domain.NewID(), "ssh://git.example.com/team/repo", "main", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造另一个任务失败: %v", err)
	}
	other.HostID = &otherHost
	other.ContainerID = fixture.container.ID.String()

	decision := domain.DecideContainerOwnership(other, fixture.container)
	if decision.Allowed {
		t.Fatal("容器登记的是其他任务，不得被当作本任务的执行容器")
	}
	if decision.Reason != domain.ContainerOwnershipTaskMismatch {
		t.Fatalf("拒绝原因应为 %s，实际 %s", domain.ContainerOwnershipTaskMismatch, decision.Reason)
	}
	if err := domain.CheckContainerOwnership(other, fixture.container); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("拒绝时必须返回 ErrInvalidState，实际 %v", err)
	}
}

// TestDecideContainerOwnershipRejectsUnassignedContainer 验证未登记归属被拒绝。
func TestDecideContainerOwnershipRejectsUnassignedContainer(t *testing.T) {
	t.Parallel()
	fixture := newOwnershipFixture(t)
	unassigned := fixture.container
	unassigned.TaskID = nil
	decision := domain.DecideContainerOwnership(fixture.task, unassigned)
	if decision.Allowed {
		t.Fatal("没有任务归属的容器不得被当作任务容器")
	}
	if decision.Reason != domain.ContainerOwnershipUnassigned {
		t.Fatalf("拒绝原因应为 %s，实际 %s", domain.ContainerOwnershipUnassigned, decision.Reason)
	}
}

// TestDecideContainerOwnershipRejectsMismatchedRecord 验证记录标识不一致被拒绝。
//
// 任务引用 A、读取到的却是 B，说明数据库里存在两条互相矛盾的关联：此时无法判断
// 哪一条代表真实现场，必须拒绝而不是任选其一。
func TestDecideContainerOwnershipRejectsMismatchedRecord(t *testing.T) {
	t.Parallel()
	fixture := newOwnershipFixture(t)
	other := fixture.container
	other.ID = domain.NewID()
	decision := domain.DecideContainerOwnership(fixture.task, other)
	if decision.Allowed {
		t.Fatal("记录标识与任务引用不一致时必须拒绝")
	}
	if decision.Reason != domain.ContainerOwnershipRecordMismatch {
		t.Fatalf("拒绝原因应为 %s，实际 %s", domain.ContainerOwnershipRecordMismatch, decision.Reason)
	}
}

// TestDecideContainerOwnershipRejectsMissingReference 验证任务没有容器引用被拒绝。
func TestDecideContainerOwnershipRejectsMissingReference(t *testing.T) {
	t.Parallel()
	fixture := newOwnershipFixture(t)
	task := fixture.task
	task.ContainerID = ""
	decision := domain.DecideContainerOwnership(task, fixture.container)
	if decision.Allowed {
		t.Fatal("任务没有容器引用时必须拒绝")
	}
	if decision.Reason != domain.ContainerOwnershipMissingRef {
		t.Fatalf("拒绝原因应为 %s，实际 %s", domain.ContainerOwnershipMissingRef, decision.Reason)
	}
}

// TestDecideContainerOwnershipRejectsHostMismatch 验证节点不一致被拒绝。
func TestDecideContainerOwnershipRejectsHostMismatch(t *testing.T) {
	t.Parallel()
	fixture := newOwnershipFixture(t)
	task := fixture.task
	otherHost := domain.NewID()
	task.HostID = &otherHost
	decision := domain.DecideContainerOwnership(task, fixture.container)
	if decision.Allowed {
		t.Fatal("容器所属节点与任务不一致时必须拒绝")
	}
	if decision.Reason != domain.ContainerOwnershipHostMismatch {
		t.Fatalf("拒绝原因应为 %s，实际 %s", domain.ContainerOwnershipHostMismatch, decision.Reason)
	}
}

// TestDecideContainerOwnershipRejectsDetachedAndRemoved 验证终态容器被拒绝。
func TestDecideContainerOwnershipRejectsDetachedAndRemoved(t *testing.T) {
	t.Parallel()
	fixture := newOwnershipFixture(t)
	detached := fixture.container
	if err := detached.Detach(time.Now().UTC()); err != nil {
		t.Fatalf("脱离管理失败: %v", err)
	}
	decision := domain.DecideContainerOwnership(fixture.task, detached)
	if decision.Allowed || decision.Reason != domain.ContainerOwnershipDetached {
		t.Fatalf("已脱离管理的容器必须被拒绝，实际 %s", decision.Reason)
	}
	removed := fixture.container
	if err := removed.MarkRemoved(domain.ContainerEndReasonManual, time.Now().UTC()); err != nil {
		t.Fatalf("标记删除失败: %v", err)
	}
	decision = domain.DecideContainerOwnership(fixture.task, removed)
	if decision.Allowed || decision.Reason != domain.ContainerOwnershipRemoved {
		t.Fatalf("已删除的容器必须被拒绝，实际 %s", decision.Reason)
	}
}

// TestDecideContainerOwnershipRejectsMissingDockerID 验证缺少 Docker ID 被拒绝。
func TestDecideContainerOwnershipRejectsMissingDockerID(t *testing.T) {
	t.Parallel()
	fixture := newOwnershipFixture(t)
	container := fixture.container
	container.DockerID = ""
	decision := domain.DecideContainerOwnership(fixture.task, container)
	if decision.Allowed {
		t.Fatal("缺少 Docker 容器 ID 时必须拒绝")
	}
	if decision.Reason != domain.ContainerOwnershipDockerIDMissing {
		t.Fatalf("拒绝原因应为 %s，实际 %s", domain.ContainerOwnershipDockerIDMissing, decision.Reason)
	}
}
