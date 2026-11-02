package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
)

// 环境构建的错误类别。
//
// HTTP 层依据这些类别区分"正在构建（409）"、"服务未装配或已停止（503）"与
// "没有可读取的构建日志（404）"，不匹配错误文本。
var (
	// ErrEnvironmentBuildInProgress 表示该环境版本已有一个后台构建在进行。
	ErrEnvironmentBuildInProgress = errors.New("app: 该环境版本正在构建")
	// ErrEnvironmentBuildStopped 表示后台构建服务未启动或已停止。
	ErrEnvironmentBuildStopped = errors.New("app: 环境构建服务未启动或已停止")
	// ErrEnvironmentBuildLogMissing 表示该版本没有可读取的构建日志。
	ErrEnvironmentBuildLogMissing = errors.New("app: 构建日志不存在")
)

// BuildRequest 是构建环境版本的输入。
type BuildRequest struct {
	// MachineID 是目标执行节点。
	MachineID string
	// Ref 是环境引用。
	Ref domain.EnvironmentRef
	// VersionNumber 是版本号，0 表示最新版本。
	VersionNumber int
	// NoCache 表示禁用 Docker 构建缓存。
	NoCache bool
}

// BuildResult 是一次环境构建的结果。
type BuildResult struct {
	// Version 是构建后的版本元数据。
	Version domain.EnvironmentVersion
	// Build 是远程构建结果。
	Build docker.BuildResult
	// LogPath 是远程构建日志路径。
	LogPath string
	// Cached 表示复用了已有成功构建。
	Cached bool
	// MetadataWarning 描述镜像已生成但元数据写入失败的情况。
	MetadataWarning string
}

// Build 构建指定环境版本的镜像。
//
// 流程：加锁 → 读取版本 → 需要时复用已有成功构建并核验镜像仍存在 → 上传上下文 →
// 远程构建 → 核实实际镜像 ID → 写入远程镜像清单 → 更新 SQLite 记录。
// 构建失败时把版本标记为 failed，不会将其标记为可用。
func (s *EnvironmentService) Build(
	ctx context.Context,
	request BuildRequest,
) (BuildResult, error) {
	var result BuildResult
	err := s.withRemoteDocker(
		ctx,
		request.MachineID,
		false,
		func(ctx context.Context, session *remoteDocker) error {
			unlock := s.locks.Lock(lockKey(session.host.ID, request.Ref))
			defer unlock()
			built, err := s.buildLocked(ctx, session, request)
			if err != nil {
				return err
			}
			result = built
			return nil
		},
	)
	if err != nil {
		return result, err
	}
	s.debug(
		ctx,
		"环境镜像构建完成",
		slog.String("machine", request.MachineID),
		slog.String("environment", request.Ref.Label()),
		slog.String("image_id", result.Version.ImageID),
	)
	return result, nil
}

// buildLocked 在持锁状态下执行构建。
func (s *EnvironmentService) buildLocked(
	ctx context.Context,
	session *remoteDocker,
	request BuildRequest,
) (BuildResult, error) {
	version, err := s.resolveVersion(ctx, session.host.ID, request.Ref, request.VersionNumber)
	if err != nil {
		return BuildResult{}, err
	}
	if version.Status == domain.BuildStatusSucceeded && !request.NoCache {
		reused, reuseErr := s.reuseBuild(ctx, session, version)
		if reuseErr == nil {
			return reused, nil
		}
		s.debug(
			ctx,
			"已有构建不可复用，重新构建",
			slog.String("version", version.Label()),
			slog.String("error", reuseErr.Error()),
		)
	}
	// 构建配置哈希与镜像标签在创建版本时已固定，这里只解析父镜像引用用于构建参数。
	parentImageRef, err := s.parentImageRef(ctx, session.host.ID, version)
	if err != nil {
		return BuildResult{}, err
	}
	dockerfile, err := session.transfer.ReadFile(
		ctx,
		session.layout.DockerfilePath(version.Ref, version.Number),
		docker.MaxContextFileBytes,
	)
	if err != nil {
		return BuildResult{}, fmt.Errorf("读取远程 Dockerfile 失败: %w", err)
	}
	// 局部变量不能命名为 context：遮蔽标准库包会让下面的中断收敛写入无法引用
	// context.WithoutCancel。
	buildContext, err := buildEnvironmentContext(dockerfile, nil)
	if err != nil {
		return BuildResult{}, fmt.Errorf("构造构建上下文失败: %w", err)
	}
	buildID := buildIdentifier(version)
	spec := docker.BuildSpec{
		Version:          version,
		ParentImageRef:   parentImageRef,
		Context:          buildContext,
		RemoteContextDir: session.layout.BuildDir(buildID) + "/context",
		LogPath:          session.layout.BuildLogPath(buildID),
		BuildID:          buildID,
		NoCache:          request.NoCache,
	}
	if err := version.MarkBuilding(); err != nil {
		return BuildResult{}, err
	}
	if err := s.store.UpdateEnvironmentVersion(ctx, version); err != nil {
		return BuildResult{}, fmt.Errorf("更新环境版本状态失败: %w", err)
	}
	build, buildErr := session.runner.BuildImage(ctx, session.transfer, spec)
	if buildErr != nil {
		failed := version
		if interrupted := ctx.Err(); interrupted != nil {
			// 构建被中断（服务停止或调用方取消）：镜像可能已经生成，也可能没有，
			// 因此不能记为 failed，只能记为结果不确定，等待显式重建或人工核验。
			_ = failed.MarkUncertain(
				"构建被中断，结果无法确认: " + buildErr.Error(),
			)
		} else {
			_ = failed.MarkFailed(buildErr.Error())
		}
		// ctx 已被取消时不能用它写库：构建中断的结论必须落库，否则记录会永远
		// 停在 building，看起来像"仍在构建"。
		writeCtx := ctx
		if ctx.Err() != nil {
			writeCtx = context.WithoutCancel(ctx)
		}
		if updateErr := s.store.UpdateEnvironmentVersion(writeCtx, failed); updateErr != nil {
			s.debug(
				ctx,
				"记录构建失败状态失败",
				slog.String("error", updateErr.Error()),
			)
		}
		return BuildResult{
			Version: failed,
			Build:   build,
			LogPath: spec.LogPath,
		}, fmt.Errorf("构建环境镜像失败: %w", buildErr)
	}
	builtAt := time.Now().UTC().Truncate(time.Millisecond)
	finished := version
	if err := finished.MarkBuilt(build.ImageID, builtAt); err != nil {
		return BuildResult{}, fmt.Errorf("记录构建结果失败: %w", err)
	}
	result := BuildResult{
		Version: finished,
		Build:   build,
		LogPath: spec.LogPath,
	}
	manifest, manifestErr := session.runner.ReadImageManifest(
		ctx,
		session.transfer,
		session.layout.ImagesManifestPath(),
	)
	if manifestErr != nil {
		result.MetadataWarning = "镜像已生成，但读取远程镜像清单失败: " + manifestErr.Error()
	} else {
		manifest.Upsert(docker.ImageManifestEntry{
			Environment:       finished.Ref.Label(),
			Version:           finished.Label(),
			DockerfileSHA256:  finished.DockerfileSHA256,
			BuildConfigSHA256: finished.BuildConfigSHA256,
			ContextSHA256:     finished.ContextSHA256,
			ImageRef:          finished.ImageRef,
			ImageID:           finished.ImageID,
			BuiltAt:           builtAt.Format(time.RFC3339),
		})
		if writeErr := session.runner.WriteImageManifest(
			ctx,
			session.transfer,
			session.layout.ImagesManifestPath(),
			manifest,
		); writeErr != nil {
			result.MetadataWarning = "镜像已生成，但写入远程镜像清单失败: " + writeErr.Error()
		}
	}
	if err := s.store.UpdateEnvironmentVersion(ctx, finished); err != nil {
		if result.MetadataWarning == "" {
			result.MetadataWarning = "镜像已生成，但更新本机构建记录失败: " + err.Error()
		} else {
			result.MetadataWarning += "；更新本机构建记录失败: " + err.Error()
		}
		result.Version = finished
		return result, fmt.Errorf(
			"镜像已生成但元数据写入失败，需要修复: %s",
			result.MetadataWarning,
		)
	}
	return result, nil
}

// reuseBuild 复用已有成功构建，但必须先核验镜像仍然存在且元数据匹配。
func (s *EnvironmentService) reuseBuild(
	ctx context.Context,
	session *remoteDocker,
	version domain.EnvironmentVersion,
) (BuildResult, error) {
	inspect, err := session.runner.InspectImage(ctx, version.ImageRef)
	if err != nil {
		return BuildResult{}, err
	}
	if inspect.ID != version.ImageID {
		return BuildResult{}, fmt.Errorf(
			"镜像 ID 与记录不一致: 记录 %s，实际 %s",
			version.ImageID,
			inspect.ID,
		)
	}
	if inspect.Labels[docker.BuildConfigLabelKey] != version.BuildConfigSHA256 {
		return BuildResult{}, fmt.Errorf("镜像构建配置标签与记录不一致")
	}
	return BuildResult{
		Version: version,
		Build: docker.BuildResult{
			ImageRef: version.ImageRef,
			ImageID:  version.ImageID,
			Cached:   true,
		},
		LogPath: session.layout.BuildLogPath(buildIdentifier(version)),
		Cached:  true,
	}, nil
}

// parentImageRef 返回语言环境依赖的父基础环境镜像引用。
func (s *EnvironmentService) parentImageRef(
	ctx context.Context,
	hostID domain.ID,
	version domain.EnvironmentVersion,
) (string, error) {
	if version.Ref.IsBase() {
		return "", nil
	}
	parentNumber, err := domain.ParseVersionLabel(version.ParentVersion)
	if err != nil {
		return "", err
	}
	baseRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		return "", err
	}
	parent, err := s.store.GetEnvironmentVersion(ctx, hostID, baseRef, parentNumber)
	if err != nil {
		return "", fmt.Errorf("读取父基础环境版本失败: %w", err)
	}
	if !parent.Usable() {
		return "", fmt.Errorf("父基础环境版本 %s 尚未构建成功", parent.Label())
	}
	return parent.ImageRef, nil
}

// buildIdentifier 生成构建目录标识。
func buildIdentifier(version domain.EnvironmentVersion) string {
	label := strings.ReplaceAll(version.Ref.Label(), "/", "-")
	return path.Join(label, version.Label()+"-"+version.BuildConfigSHA256[:12])
}

// DownloadBuildLogRequest 是下载构建日志的输入。
type DownloadBuildLogRequest struct {
	// MachineID 是目标执行节点。
	MachineID string
	// Ref 是环境引用。
	Ref domain.EnvironmentRef
	// VersionNumber 是版本号，0 表示最新版本。
	VersionNumber int
	// LocalPath 是本机目标文件路径。
	LocalPath string
	// Overwrite 允许覆盖已存在的本机文件。
	Overwrite bool
}

// DownloadBuildLog 把远程构建日志下载到本机指定位置。
func (s *EnvironmentService) DownloadBuildLog(
	ctx context.Context,
	request DownloadBuildLogRequest,
) (string, error) {
	if strings.TrimSpace(request.LocalPath) == "" {
		return "", fmt.Errorf("下载构建日志失败: 本机路径不能为空")
	}
	remotePath := ""
	err := s.withRemoteDocker(
		ctx,
		request.MachineID,
		false,
		func(ctx context.Context, session *remoteDocker) error {
			version, err := s.resolveVersion(ctx, session.host.ID, request.Ref, request.VersionNumber)
			if err != nil {
				return err
			}
			remotePath = session.layout.BuildLogPath(buildIdentifier(version))
			return session.transfer.Download(
				ctx,
				remotePath,
				request.LocalPath,
				sshclient.DownloadOptions{
					Overwrite:    request.Overwrite,
					CreateParent: true,
					MaxBytes:     docker.MaxBuildLogBytes,
				},
			)
		},
	)
	if err != nil {
		return remotePath, err
	}
	return remotePath, nil
}

// ReadBuildLogRequest 是读取构建日志内容的输入。
//
// 它不接受任何远程或本机路径：日志位置由服务端根据已验证的节点、环境与版本记录
// 推导，调用方只能指定"哪个节点上的哪个环境版本"。
type ReadBuildLogRequest struct {
	// MachineID 是目标执行节点。
	MachineID string
	// Ref 是环境引用。
	Ref domain.EnvironmentRef
	// VersionNumber 是版本号，0 表示最新版本。
	VersionNumber int
}

// BuildLog 是一次构建日志读取的结果。
type BuildLog struct {
	// Version 是版本元数据。
	Version domain.EnvironmentVersion
	// Content 是构建日志正文，最多 docker.MaxBuildLogBytes 字节。
	Content []byte
	// RemotePath 是日志在远程工作空间中的路径，只用于诊断展示。
	RemotePath string
}

// ReadBuildLog 读取指定版本的构建日志内容。
//
// 日志路径固定由 docker.Layout 与版本构建配置生成，不接受调用方传入路径；
// 文件缺失按读取失败返回，不会伪装成"空日志"。
func (s *EnvironmentService) ReadBuildLog(
	ctx context.Context,
	request ReadBuildLogRequest,
) (BuildLog, error) {
	var log BuildLog
	err := s.withRemoteDocker(
		ctx,
		request.MachineID,
		false,
		func(ctx context.Context, session *remoteDocker) error {
			version, err := s.resolveVersion(ctx, session.host.ID, request.Ref, request.VersionNumber)
			if err != nil {
				return err
			}
			if version.Status == domain.BuildStatusPending {
				return fmt.Errorf(
					"%w: 环境 %s 的版本 %s 尚未构建，没有构建日志",
					ErrEnvironmentBuildLogMissing,
					version.Ref.Label(),
					version.Label(),
				)
			}
			remotePath := session.layout.BuildLogPath(buildIdentifier(version))
			content, readErr := session.transfer.ReadFile(ctx, remotePath, docker.MaxBuildLogBytes)
			if readErr != nil {
				// 只有"记录存在但日志文件缺失"才是 404 语义；连接中断、文件过大或
				// 权限问题必须如实报告为读取失败，不能伪装成"日志不存在"。
				if isRemoteNotExist(readErr) {
					return fmt.Errorf(
						"%w: 远程构建日志不存在: %s",
						ErrEnvironmentBuildLogMissing,
						remotePath,
					)
				}
				return fmt.Errorf("读取远程构建日志失败: %s: %w", remotePath, readErr)
			}
			log = BuildLog{Version: version, Content: content, RemotePath: remotePath}
			return nil
		},
	)
	if err != nil {
		return BuildLog{}, err
	}
	return log, nil
}
