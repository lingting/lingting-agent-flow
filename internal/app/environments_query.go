package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 环境查询的错误类别。
//
// HTTP 层依据错误类别映射状态码，而不是匹配错误文本：非法参数 400、目标不存在 404、
// 其余按服务内部错误处理。环境元数据全部来自 SQLite，因此这些错误与远程 Docker 无关。
var (
	// ErrEnvironmentInvalidInput 表示查询参数不合法（节点标识、环境引用或版本号）。
	ErrEnvironmentInvalidInput = errors.New("app: 环境查询参数不合法")
	// ErrEnvironmentHostNotFound 表示指定的执行节点不存在或已删除。
	ErrEnvironmentHostNotFound = errors.New("app: 执行节点不存在")
	// ErrEnvironmentNotFound 表示该节点上不存在指定环境的任何版本。
	ErrEnvironmentNotFound = errors.New("app: 环境不存在")
	// ErrEnvironmentVersionNotFound 表示该环境不存在指定版本。
	ErrEnvironmentVersionNotFound = errors.New("app: 环境版本不存在")
)

// EnvironmentQueryService 提供环境定义的只读查询。
//
// 它只依赖存储能力：环境定义与版本元数据全部持久化在 SQLite 中，读取它们不需要 SSH、
// Docker 或远程会话。因此只读查询既不会建立远程连接，也不会创建目录、构建镜像或改动
// 任何远程资源。有副作用的操作（初始化、创建版本、构建）仍由 EnvironmentService 承担。
type EnvironmentQueryService struct {
	store storage.Store
}

// NewEnvironmentQueryService 构造环境只读查询服务。
func NewEnvironmentQueryService(store storage.Store) (*EnvironmentQueryService, error) {
	if store == nil {
		return nil, fmt.Errorf("环境查询服务初始化失败: 存储不能为空")
	}
	return &EnvironmentQueryService{store: store}, nil
}

// ListRequest 是查询环境定义的输入。
type ListRequest struct {
	// MachineID 是目标执行节点。
	MachineID string
}

// List 返回指定节点上的环境定义与版本概览。
//
// 节点不存在返回 ErrEnvironmentHostNotFound；节点存在但没有任何版本时返回空列表，
// 由调用方决定如何呈现"尚未初始化环境"。
func (s *EnvironmentQueryService) List(
	ctx context.Context,
	request ListRequest,
) ([]EnvironmentSummary, error) {
	host, err := s.loadHost(ctx, request.MachineID)
	if err != nil {
		return nil, err
	}
	versions, err := s.store.ListEnvironmentVersions(ctx, storage.EnvironmentVersionFilter{
		HostID: host.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("读取环境版本失败: %w", err)
	}
	return summarizeEnvironments(versions), nil
}

// loadHost 读取执行节点配置。
//
// 参数非法与节点不存在使用不同错误类别，使 HTTP 层能区分 400 与 404。
func (s *EnvironmentQueryService) loadHost(
	ctx context.Context,
	hostID string,
) (domain.ExecutionHost, error) {
	id := domain.ID(hostID)
	if err := id.Validate(); err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("%w: 执行节点标识不能为空", ErrEnvironmentInvalidInput)
	}
	host, err := s.store.GetExecutionHost(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.ExecutionHost{}, fmt.Errorf("%w: %s", ErrEnvironmentHostNotFound, hostID)
		}
		return domain.ExecutionHost{}, fmt.Errorf("读取执行节点失败: %w", err)
	}
	return *host, nil
}

// summarizeEnvironments 把版本列表汇总为环境概览。
//
// 汇总规则：环境按首次出现顺序输出，版本按传入顺序保留（存储层按版本号升序返回），
// LatestVersion 取最后一个版本，CurrentVersion 取最后一个可用版本。可用性判定复用
// domain.EnvironmentVersion.Usable，不在查询层重新定义。
func summarizeEnvironments(versions []domain.EnvironmentVersion) []EnvironmentSummary {
	summaries := make([]EnvironmentSummary, 0, 4)
	index := make(map[string]int, 4)
	for _, version := range versions {
		label := version.Ref.Label()
		position, ok := index[label]
		if !ok {
			summaries = append(summaries, EnvironmentSummary{Ref: version.Ref})
			position = len(summaries) - 1
			index[label] = position
		}
		summary := &summaries[position]
		summary.Versions = append(summary.Versions, version)
		summary.LatestVersion = version.Label()
		if version.Usable() {
			summary.CurrentVersion = version.Label()
		}
	}
	return summaries
}

// GetRequest 是查询单个环境定义的输入。
type GetRequest struct {
	// MachineID 是目标执行节点。
	MachineID string
	// Ref 是环境引用。
	Ref domain.EnvironmentRef
}

// Get 返回单个环境定义的概览。
func (s *EnvironmentQueryService) Get(
	ctx context.Context,
	request GetRequest,
) (EnvironmentSummary, error) {
	if err := request.Ref.Validate(); err != nil {
		return EnvironmentSummary{}, fmt.Errorf("%w: %w", ErrEnvironmentInvalidInput, err)
	}
	host, err := s.loadHost(ctx, request.MachineID)
	if err != nil {
		return EnvironmentSummary{}, err
	}
	ref := request.Ref
	versions, err := s.store.ListEnvironmentVersions(ctx, storage.EnvironmentVersionFilter{
		HostID: host.ID,
		Ref:    &ref,
	})
	if err != nil {
		return EnvironmentSummary{}, fmt.Errorf("读取环境版本失败: %w", err)
	}
	if len(versions) == 0 {
		return EnvironmentSummary{}, fmt.Errorf("%w: %s", ErrEnvironmentNotFound, ref.Label())
	}
	summaries := summarizeEnvironments(versions)
	return summaries[0], nil
}

// GetVersionRequest 是查询单个环境版本的输入。
type GetVersionRequest struct {
	// MachineID 是目标执行节点。
	MachineID string
	// Ref 是环境引用。
	Ref domain.EnvironmentRef
	// Number 是版本号，必须大于 0：单版本查询不接受"最新版本"的隐式语义。
	Number int
}

// GetVersion 返回指定环境版本的元数据。
//
// 它直接读取版本记录，不建立远程会话：返回的构建状态、镜像引用与镜像 ID 都是持久化
// 事实，调用方据此判断版本是否可用于创建容器。Dockerfile 正文与远程路径不在返回范围内。
func (s *EnvironmentQueryService) GetVersion(
	ctx context.Context,
	request GetVersionRequest,
) (domain.EnvironmentVersion, error) {
	if err := request.Ref.Validate(); err != nil {
		return domain.EnvironmentVersion{}, fmt.Errorf("%w: %w", ErrEnvironmentInvalidInput, err)
	}
	if request.Number < domain.MinEnvironmentVersionNumber ||
		request.Number > domain.MaxEnvironmentVersionNumber {
		return domain.EnvironmentVersion{}, fmt.Errorf(
			"%w: 版本号必须在 %d-%d 之间",
			ErrEnvironmentInvalidInput,
			domain.MinEnvironmentVersionNumber,
			domain.MaxEnvironmentVersionNumber,
		)
	}
	host, err := s.loadHost(ctx, request.MachineID)
	if err != nil {
		return domain.EnvironmentVersion{}, err
	}
	version, err := s.store.GetEnvironmentVersion(ctx, host.ID, request.Ref, request.Number)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.EnvironmentVersion{}, fmt.Errorf(
				"%w: %s %s",
				ErrEnvironmentVersionNotFound,
				request.Ref.Label(),
				domain.FormatVersionLabel(request.Number),
			)
		}
		return domain.EnvironmentVersion{}, fmt.Errorf("读取环境版本失败: %w", err)
	}
	return *version, nil
}

// History 返回指定环境的全部历史版本。
func (s *EnvironmentQueryService) History(
	ctx context.Context,
	request GetRequest,
) ([]domain.EnvironmentVersion, error) {
	summary, err := s.Get(ctx, request)
	if err != nil {
		return nil, err
	}
	return summary.Versions, nil
}

// ReadDockerfileRequest 是读取环境版本 Dockerfile 的输入。
type ReadDockerfileRequest struct {
	// MachineID 是目标执行节点。
	MachineID string
	// Ref 是环境引用。
	Ref domain.EnvironmentRef
	// VersionNumber 是版本号，0 表示当前最新版本。
	VersionNumber int
}

// DockerfileContent 是读取到的 Dockerfile 内容及其来源信息。
type DockerfileContent struct {
	// Version 是版本元数据。
	Version domain.EnvironmentVersion
	// Content 是 Dockerfile 内容。
	Content []byte
	// RemotePath 是 Dockerfile 的远程路径。
	RemotePath string
}

// ReadDockerfile 读取指定版本的 Dockerfile。
func (s *EnvironmentService) ReadDockerfile(
	ctx context.Context,
	request ReadDockerfileRequest,
) (DockerfileContent, error) {
	var content DockerfileContent
	err := s.withRemoteDocker(
		ctx,
		request.MachineID,
		false,
		func(ctx context.Context, session *remoteDocker) error {
			version, err := s.resolveVersion(ctx, session.host.ID, request.Ref, request.VersionNumber)
			if err != nil {
				return err
			}
			remotePath := session.layout.DockerfilePath(version.Ref, version.Number)
			data, readErr := session.transfer.ReadFile(ctx, remotePath, docker.MaxContextFileBytes)
			if readErr != nil {
				return fmt.Errorf("读取远程 Dockerfile 失败: %s: %w", remotePath, readErr)
			}
			content = DockerfileContent{
				Version:    version,
				Content:    data,
				RemotePath: remotePath,
			}
			return nil
		},
	)
	if err != nil {
		return DockerfileContent{}, err
	}
	return content, nil
}

// resolveVersion 解析版本号，0 表示该环境的最新版本。
func (s *EnvironmentService) resolveVersion(
	ctx context.Context,
	hostID domain.ID,
	ref domain.EnvironmentRef,
	number int,
) (domain.EnvironmentVersion, error) {
	if number > 0 {
		version, err := s.store.GetEnvironmentVersion(ctx, hostID, ref, number)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return domain.EnvironmentVersion{}, fmt.Errorf(
					"环境 %s 不存在版本 %s",
					ref.Label(),
					domain.FormatVersionLabel(number),
				)
			}
			return domain.EnvironmentVersion{}, fmt.Errorf("读取环境版本失败: %w", err)
		}
		return *version, nil
	}
	versions, err := s.store.ListEnvironmentVersions(ctx, storage.EnvironmentVersionFilter{
		HostID: hostID,
		Ref:    &ref,
	})
	if err != nil {
		return domain.EnvironmentVersion{}, fmt.Errorf("读取环境版本失败: %w", err)
	}
	if len(versions) == 0 {
		return domain.EnvironmentVersion{}, fmt.Errorf(
			"环境 %s 尚未创建版本；请先执行 agentflow docker init",
			ref.Label(),
		)
	}
	return versions[len(versions)-1], nil
}
