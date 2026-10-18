package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage"
)

// ListRequest 是查询环境定义的输入。
type ListRequest struct {
	// MachineID 是目标执行节点。
	MachineID string
}

// List 返回指定节点上的环境定义与版本概览。
func (s *EnvironmentService) List(
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

// summarizeEnvironments 把版本列表汇总为环境概览。
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
func (s *EnvironmentService) Get(
	ctx context.Context,
	request GetRequest,
) (EnvironmentSummary, error) {
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
		return EnvironmentSummary{}, fmt.Errorf(
			"环境 %s 尚未创建版本；请先执行 agentflow docker init",
			ref.Label(),
		)
	}
	summaries := summarizeEnvironments(versions)
	return summaries[0], nil
}

// ReadDockerfileRequest 是读取环境版本 Dockerfile 的输入。
type ReadDockerfileRequest struct {
	// MachineID 是目标执行节点。
	MachineID string
	// Ref 是环境引用。
	Ref domain.EnvironmentRef
	// VersionNumber 是版本号，0 表示当前最新版本。
	VersionNumber int
	// Credentials 提供本次连接所需的凭据。
	Credentials sshclient.CredentialProvider
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
		request.Credentials,
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

// History 返回指定环境的全部历史版本。
func (s *EnvironmentService) History(
	ctx context.Context,
	request GetRequest,
) ([]domain.EnvironmentVersion, error) {
	summary, err := s.Get(ctx, request)
	if err != nil {
		return nil, err
	}
	return summary.Versions, nil
}
