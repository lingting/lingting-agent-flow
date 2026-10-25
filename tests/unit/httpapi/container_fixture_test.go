package httpapi_test

import (
	"context"
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

// seedOwnedContainer 写入一条属于该任务的运行中容器记录。
//
// 返回写入后的容器记录；调用方需要把它的标识写回任务记录。
func seedOwnedContainer(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	task domain.Task,
	containerID, dockerID string,
) domain.Container {
	t.Helper()
	hostID := containerHostID(task)
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
	taskID := task.ID
	container.TaskID = &taskID
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
