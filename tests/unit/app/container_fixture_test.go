package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// 容器归属测试数据。
//
// 任务记录里的 container_id 只是普通文本列，没有外键约束：执行链路会读取容器
// 登记记录并核验归属。用例因此必须写入一条真正属于该任务的容器记录，而不是只给
// 任务填一个字符串——否则验证的不是业务逻辑，而是"记录缺失时被拒绝"这一条路径。
const (
	// testContainerImageID 是容器登记记录使用的镜像 ID。
	testContainerImageID = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	// testContainerBuildHash 是容器登记记录使用的构建配置哈希。
	testContainerBuildHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// createTaskContainer 写入一条属于该任务的运行中容器记录，并把任务引用指向它。
//
// 返回写入后的容器记录与任务副本。调用方必须在写入任务之前调用它，或者随后把
// 返回的 ContainerID 写回任务记录。
func createTaskContainer(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	task domain.Task,
) (domain.Container, domain.Task) {
	t.Helper()
	hostID := domain.NewID()
	if task.HostID != nil {
		hostID = *task.HostID
	}
	container, err := domain.NewContainer(
		"",
		hostID,
		"agentflow-testcontainer",
		domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		domain.FormatVersionLabel(1),
		testContainerBuildHash,
		testContainerImageID,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	container.TaskID = &task.ID
	container.DockerID = "docker-" + container.ID.String()
	container.State = domain.ContainerStateRunning
	container.DockerState = domain.DockerStateRunning
	if err := db.CreateContainer(ctx, container); err != nil {
		t.Fatalf("写入容器记录失败: %v", err)
	}
	updated := task
	updated.ContainerID = container.ID.String()
	return container, updated
}

// ensureTaskContainer 为任务写入归属容器并更新任务记录。
//
// 它用于"任务已经写入数据库、但还没有容器引用"的用例：先写容器，再把引用写回
// 任务。返回值是更新后的任务副本。
func ensureTaskContainer(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	task domain.Task,
) (domain.Container, domain.Task) {
	t.Helper()
	container, updated := createTaskContainer(t, db, ctx, task)
	if err := updated.SetContainer(container.ID.String()); err != nil {
		t.Fatalf("设置任务容器引用失败: %v", err)
	}
	if err := db.UpdateTask(ctx, updated); err != nil {
		t.Fatalf("更新任务容器引用失败: %v", err)
	}
	return container, updated
}

// isOwnershipRejection 判断错误是否来自容器归属校验。
func isOwnershipRejection(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, string(domain.ContainerOwnershipTaskMismatch)) ||
		strings.Contains(message, string(domain.ContainerOwnershipUnassigned)) ||
		strings.Contains(message, string(domain.ContainerOwnershipRecordMismatch))
}

// seedOwnedContainer 写入一条指定标识的、属于该任务的运行中容器记录。
//
// 用例需要把容器标识固定下来（例如与受控容器替身返回的标识对齐）时使用它：
// 归属核验读取的是数据库里的真实记录，因此固定标识必须真实落库。
func seedOwnedContainer(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	task domain.Task,
	containerID, dockerID string,
) domain.Container {
	t.Helper()
	hostID := domain.NewID()
	if task.HostID != nil {
		hostID = *task.HostID
	}
	container, err := domain.NewContainer(
		domain.ID(containerID),
		hostID,
		"agentflow-"+containerID,
		domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		domain.FormatVersionLabel(1),
		testContainerBuildHash,
		testContainerImageID,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	container.TaskID = &task.ID
	container.DockerID = dockerID
	container.State = domain.ContainerStateRunning
	container.DockerState = domain.DockerStateRunning
	if err := db.CreateContainer(ctx, container); err != nil {
		t.Fatalf("写入容器记录失败: %v", err)
	}
	return container
}

// containerHostID 返回任务绑定的执行节点标识，未绑定时返回一个占位标识。
//
// 容器归属校验会比对节点：用例构造的容器记录必须与任务使用同一个节点，否则
// 校验会以"节点不一致"拒绝，从而掩盖用例真正想验证的行为。
func containerHostID(task domain.Task) domain.ID {
	if task.HostID != nil {
		return *task.HostID
	}
	return domain.ID("host-unassigned")
}
