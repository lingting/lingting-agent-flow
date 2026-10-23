// Package workflow_test 提供端到端验收使用的统一执行环境装配。
//
// 端到端链路必须经过真实的生产环境准备路径：四个阶段（校验 → 镜像 → 创建容器 →
// 核验可用）由 app.ExecutionEnvironmentService 执行，容器登记记录写回任务也由它
// 完成。因此这里只替换它的两个外部端口（镜像与 Docker 容器），不替换准备流程本身。
package workflow_test

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// e2eImages 是镜像能力的受控替身。
//
// 它返回一个可用于创建容器的环境版本，不访问真实 Docker Engine，也不下载镜像层。
// 端到端用例不覆盖镜像准备失败的路径（那是 P2 的恢复矩阵范围），因此这里只按契约
// 返回成功结果，不额外记录调用。
type e2eImages struct{}

// Preflight 返回受控的远程 Docker 预检结果。
func (i *e2eImages) Preflight(
	ctx context.Context,
	options app.InitOptions,
) (app.InitResult, error) {
	return app.InitResult{
		Host:          domain.ExecutionHost{ID: domain.ID(options.MachineID)},
		WorkspaceRoot: "/home/deploy/.lingting/agentflow",
		Preflight: docker.Preflight{
			SSHOK:             true,
			CLIPresent:        true,
			CLIVersion:        docker.MinimumCLIVersion,
			EngineReachable:   true,
			EngineVersion:     docker.MinimumEngineVersion,
			APIVersion:        docker.EngineAPIBaseVersion,
			MinAPIVersion:     "1.24",
			APIVersionCheckOK: true,
			PermissionOK:      true,
			WorkspaceWritable: true,
			ErrorCode:         docker.CodeOK,
		},
	}, nil
}

// Build 返回受控的镜像准备结果。
func (i *e2eImages) Build(
	ctx context.Context,
	request app.BuildRequest,
) (app.BuildResult, error) {
	return app.BuildResult{Version: e2eEnvironmentVersion(request.Ref)}, nil
}

// ResolveVersion 返回受控的版本解析结果。
func (i *e2eImages) ResolveVersion(
	ctx context.Context,
	hostID domain.ID,
	ref domain.EnvironmentRef,
	number int,
) (domain.EnvironmentVersion, error) {
	return e2eEnvironmentVersion(ref), nil
}

// e2eEnvironmentVersion 构造一个可用于创建容器的环境版本。
func e2eEnvironmentVersion(ref domain.EnvironmentRef) domain.EnvironmentVersion {
	if ref.IsZero() {
		ref = domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"}
	}
	builtAt := time.Now().UTC()
	return domain.EnvironmentVersion{
		HostID:            e2eHostID,
		Ref:               ref,
		Number:            1,
		DockerfileSHA256:  strings.Repeat("a", 64),
		BuildConfigSHA256: strings.Repeat("b", 64),
		ContextSHA256:     strings.Repeat("c", 64),
		ParentVersion:     "v0001",
		ImageRef:          "agentflow-language-go:v0001-0123456789ab",
		ImageID:           "sha256:" + strings.Repeat("0", 64),
		Status:            domain.BuildStatusSucceeded,
		CreatedAt:         builtAt,
		BuiltAt:           &builtAt,
	}
}

// e2eEnvContainers 是执行环境服务对容器能力的受控替身。
//
// 它把容器登记记录写入真实数据库，使"容器已创建"与"任务已关联容器"都成为可查询
// 的持久化事实；容器核验与命令执行则复用 e2ePorts，保证整条链路看到同一个容器。
type e2eEnvContainers struct {
	ports *e2ePorts
	db    *sqlite.DB
}

// CreateContainer 创建并登记一个受管理的任务容器。
func (c *e2eEnvContainers) CreateContainer(
	ctx context.Context,
	request app.CreateContainerRequest,
) (app.ContainerDetail, error) {
	container, err := domain.NewContainer(
		e2eContainerID,
		domain.ID(request.MachineID),
		"agentflow-e2e",
		request.Ref,
		domain.FormatVersionLabel(maxE2EInt(request.VersionNumber, 1)),
		strings.Repeat("b", 64),
		"sha256:"+strings.Repeat("0", 64),
		time.Now().UTC(),
	)
	if err != nil {
		return app.ContainerDetail{}, err
	}
	container.TaskID = request.TaskID
	container.AttemptID = request.AttemptID
	container.DockerID = e2eDockerID
	container.State = domain.ContainerStateRunning
	container.DockerState = domain.DockerStateRunning
	if err := c.db.CreateContainer(ctx, container); err != nil {
		return app.ContainerDetail{}, err
	}
	return app.ContainerDetail{
		Container: container,
		Inspect:   &docker.ContainerInspect{Running: true},
	}, nil
}

// InspectContainer 返回受控的容器状态。
func (c *e2eEnvContainers) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	return c.ports.InspectContainer(ctx, request)
}

// Exec 在容器内执行一条命令。
func (c *e2eEnvContainers) Exec(
	ctx context.Context,
	request app.ExecRequest,
) (app.ExecResult, error) {
	return c.ports.Exec(ctx, request)
}

// DeleteContainer 拒绝删除：端到端链路不验证容器清理。
func (c *e2eEnvContainers) DeleteContainer(
	ctx context.Context,
	request app.DeleteContainerRequest,
) (app.ContainerDetail, error) {
	return app.ContainerDetail{}, errors.New("端到端验收不执行容器删除")
}

// DetachContainer 拒绝脱离管理：端到端链路不验证容器处置。
func (c *e2eEnvContainers) DetachContainer(
	ctx context.Context,
	request app.DetachContainerRequest,
) (app.ContainerDetail, error) {
	return app.ContainerDetail{}, errors.New("端到端验收不执行容器脱离管理")
}

// LoadContainer 读取容器登记记录。
func (c *e2eEnvContainers) LoadContainer(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	container, err := c.db.GetContainer(ctx, domain.ID(strings.TrimSpace(containerID)))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Container{}, errors.New("容器记录不存在")
		}
		return domain.Container{}, err
	}
	return *container, nil
}

// LoadHost 读取执行节点配置。
func (c *e2eEnvContainers) LoadHost(
	ctx context.Context,
	hostID string,
) (domain.ExecutionHost, error) {
	host, err := c.db.GetExecutionHost(ctx, domain.ID(strings.TrimSpace(hostID)))
	if err != nil {
		return domain.ExecutionHost{}, err
	}
	return *host, nil
}

// maxE2EInt 返回两个整数中的较大值。
func maxE2EInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
