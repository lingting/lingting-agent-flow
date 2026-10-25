package fetcher_local

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
// 登记记录并核验归属。用例因此必须写入一条真正属于该任务的容器记录。
const (
	// fixtureContainerImageID 是容器登记记录使用的镜像 ID。
	fixtureContainerImageID = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	// fixtureContainerBuildHash 是容器登记记录使用的构建配置哈希。
	fixtureContainerBuildHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// seedOwnedContainer 写入一条属于该任务的运行中容器记录。
func seedOwnedContainer(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	task domain.Task,
	containerID, dockerID string,
) domain.Container {
	t.Helper()
	hostID := fixtureContainerHostID(task)
	container, err := domain.NewContainer(
		domain.ID(containerID),
		hostID,
		"agentflow-"+containerID,
		domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		domain.FormatVersionLabel(1),
		fixtureContainerBuildHash,
		fixtureContainerImageID,
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

// fixtureContainerHostID 返回任务绑定的执行节点标识，未绑定时返回占位标识。
//
// 容器归属校验会比对节点：用例构造的容器记录必须与任务使用同一个节点。
func fixtureContainerHostID(task domain.Task) domain.ID {
	if task.HostID != nil {
		return *task.HostID
	}
	return domain.ID("host-unassigned")
}
