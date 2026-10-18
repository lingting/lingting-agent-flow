package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage"
)

// EnvironmentService 管理远程环境定义、不可覆盖的配置版本与镜像构建。
//
// 所有环境文件都保存在远程 AgentFlow 工作空间内，本机只保存版本元数据与构建结果；
// 版本一旦保存即不可覆盖，新配置只能生成新的版本号。
type EnvironmentService struct {
	*remoteDockerService
	// locks 按"节点+环境"提供范围明确的互斥锁，避免同一版本的并发构建互相覆盖
	// 日志与元数据，同时不影响不同环境的并发构建。
	locks keyedMutex
}

// NewEnvironmentService 构造环境服务。
func NewEnvironmentService(options DockerServiceOptions) (*EnvironmentService, error) {
	base, err := newRemoteDockerService(options)
	if err != nil {
		return nil, err
	}
	return &EnvironmentService{remoteDockerService: base}, nil
}

// keyedMutex 是按字符串键提供互斥锁的集合。
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Lock 取得指定键的互斥锁并返回解锁函数。
func (m *keyedMutex) Lock(key string) func() {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = make(map[string]*sync.Mutex)
	}
	lock, ok := m.locks[key]
	if !ok {
		lock = &sync.Mutex{}
		m.locks[key] = lock
	}
	m.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// EnvironmentSummary 是一个环境定义的概览。
type EnvironmentSummary struct {
	// Ref 是环境引用。
	Ref domain.EnvironmentRef
	// CurrentVersion 是当前可用版本标签，没有可用版本时为空。
	CurrentVersion string
	// LatestVersion 是已创建的最大版本标签。
	LatestVersion string
	// Versions 是全部版本，按版本号升序排列。
	Versions []domain.EnvironmentVersion
}

// InitOptions 约束一次远程环境目录初始化。
type InitOptions struct {
	// MachineID 是目标执行节点。
	MachineID string
	// Credentials 提供本次连接所需的凭据。
	Credentials sshclient.CredentialProvider
	// WithLanguages 是同时初始化模板的语言环境名称列表。
	WithLanguages []string
}

// InitResult 是远程环境目录初始化的结果。
type InitResult struct {
	// Host 是目标执行节点。
	Host domain.ExecutionHost
	// WorkspaceRoot 是解析后的远程工作空间路径。
	WorkspaceRoot string
	// Preflight 是 Docker 环境预检结果。
	Preflight docker.Preflight
	// Created 是本次新建的路径列表。
	Created []string
	// Environments 是初始化后已存在的环境定义。
	Environments []domain.EnvironmentRef
}

// Preflight 检查远程 Docker 环境，不创建任何目录或 Docker 资源。
//
// 该方法是只读检查：即使工作空间不存在也不会创建，便于用户先确认环境是否可用。
func (s *EnvironmentService) Preflight(
	ctx context.Context,
	options InitOptions,
) (InitResult, error) {
	result := InitResult{}
	err := s.withRemoteSession(
		ctx,
		options.MachineID,
		options.Credentials,
		sessionOptions{AllowMissingWorkspace: true},
		func(ctx context.Context, session *remoteDocker) error {
			result.Host = session.host
			result.WorkspaceRoot = session.layout.Root
			result.Preflight = session.runner.Preflight(ctx, docker.PreflightOptions{
				WorkspaceRoot: session.layout.Root,
			})
			if !result.Preflight.Ready() {
				return fmt.Errorf(
					"远程 Docker 环境预检未通过(%s): %s",
					result.Preflight.ErrorCode,
					result.Preflight.Detail,
				)
			}
			return nil
		},
	)
	if err != nil {
		return result, err
	}
	s.debug(ctx, "远程 Docker 环境预检通过", slog.String("machine", options.MachineID))
	return result, nil
}

// Init 检查远程 Docker 环境并初始化 AgentFlow 工作空间目录。
//
// 只有在预检通过后才会创建目录与默认基础环境版本，避免在无法运行 Docker 的
// 机器上留下半成品配置。
func (s *EnvironmentService) Init(ctx context.Context, options InitOptions) (InitResult, error) {
	result := InitResult{}
	if len(options.WithLanguages) > 0 {
		for _, name := range options.WithLanguages {
			if _, err := docker.LanguageTemplateFor(name); err != nil {
				return result, err
			}
		}
	}
	err := s.withRemoteDocker(
		ctx,
		options.MachineID,
		options.Credentials,
		true,
		func(ctx context.Context, session *remoteDocker) error {
			result.Host = session.host
			result.WorkspaceRoot = session.layout.Root
			preflight := session.runner.Preflight(ctx, docker.PreflightOptions{
				WorkspaceRoot:   session.layout.Root,
				CreateWorkspace: true,
			})
			result.Preflight = preflight
			if !preflight.Ready() {
				return fmt.Errorf(
					"远程 Docker 环境预检未通过(%s): %s",
					preflight.ErrorCode,
					preflight.Detail,
				)
			}
			return s.initLayout(ctx, session, options, &result)
		},
	)
	if err != nil {
		return result, err
	}
	s.debug(ctx, "远程环境目录初始化完成", slog.String("machine", options.MachineID))
	return result, nil
}

// initLayout 创建目录结构并写入默认基础环境与语言环境版本。
func (s *EnvironmentService) initLayout(
	ctx context.Context,
	session *remoteDocker,
	options InitOptions,
	result *InitResult,
) error {
	for _, directory := range session.layout.InitialDirectories() {
		created, err := ensureRemoteDirectory(ctx, session.transfer, directory)
		if err != nil {
			return err
		}
		if created {
			result.Created = append(result.Created, directory)
		}
	}
	base, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		return err
	}
	created, err := s.ensureDefaultBaseVersion(ctx, session)
	if err != nil {
		return err
	}
	if created != "" {
		result.Created = append(result.Created, created)
	}
	result.Environments = append(result.Environments, base)
	for _, name := range options.WithLanguages {
		ref, refErr := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, name)
		if refErr != nil {
			return refErr
		}
		versionDir, createErr := s.ensureDefaultLanguageVersion(ctx, session, ref)
		if createErr != nil {
			return createErr
		}
		if versionDir != "" {
			result.Created = append(result.Created, versionDir)
		}
		result.Environments = append(result.Environments, ref)
	}
	return nil
}

// ensureDefaultBaseVersion 在基础环境没有任何版本时写入默认 v0001。
func (s *EnvironmentService) ensureDefaultBaseVersion(
	ctx context.Context,
	session *remoteDocker,
) (string, error) {
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		return "", err
	}
	existing, err := s.store.ListEnvironmentVersions(ctx, storage.EnvironmentVersionFilter{
		HostID: session.host.ID,
		Ref:    &ref,
	})
	if err != nil {
		return "", fmt.Errorf("读取基础环境版本失败: %w", err)
	}
	if len(existing) > 0 {
		return "", nil
	}
	version, err := s.CreateVersion(ctx, CreateVersionRequest{
		MachineID:  session.host.ID.String(),
		Ref:        ref,
		Dockerfile: []byte(docker.DefaultBaseDockerfile),
	})
	if err != nil {
		return "", err
	}
	return session.layout.VersionDir(ref, version.Number), nil
}

// ensureDefaultLanguageVersion 在语言环境没有任何版本时写入模板版本。
func (s *EnvironmentService) ensureDefaultLanguageVersion(
	ctx context.Context,
	session *remoteDocker,
	ref domain.EnvironmentRef,
) (string, error) {
	existing, err := s.store.ListEnvironmentVersions(ctx, storage.EnvironmentVersionFilter{
		HostID: session.host.ID,
		Ref:    &ref,
	})
	if err != nil {
		return "", fmt.Errorf("读取语言环境版本失败: %w", err)
	}
	if len(existing) > 0 {
		return "", nil
	}
	template, err := docker.LanguageTemplateFor(ref.Name)
	if err != nil {
		return "", err
	}
	baseRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		return "", err
	}
	baseVersion, err := s.currentUsableVersion(ctx, session.host.ID, baseRef)
	if err != nil {
		return "", fmt.Errorf("语言环境 %s 缺少可用的基础环境版本: %w", ref.Name, err)
	}
	version, err := s.CreateVersion(ctx, CreateVersionRequest{
		MachineID:     session.host.ID.String(),
		Ref:           ref,
		Dockerfile:    []byte(template.Dockerfile),
		ParentVersion: baseVersion.Label(),
	})
	if err != nil {
		return "", err
	}
	if err := s.writeLanguageManifest(ctx, session, ref, template); err != nil {
		return "", err
	}
	return session.layout.VersionDir(ref, version.Number), nil
}

// ensureRemoteDirectory 按需创建远程目录，返回是否真的新建了目录。
func ensureRemoteDirectory(
	ctx context.Context,
	transfer sshclient.FileTransfer,
	target string,
) (bool, error) {
	if _, err := transfer.Stat(ctx, target); err == nil {
		return false, nil
	} else if !isRemoteNotExist(err) {
		return false, fmt.Errorf("检查远程目录失败: %s: %w", target, err)
	}
	if err := transfer.MkdirAll(ctx, target); err != nil {
		return false, fmt.Errorf("创建远程目录失败: %s: %w", target, err)
	}
	return true, nil
}

// isRemoteNotExist 判断错误是否表示远程路径不存在。
func isRemoteNotExist(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such file") || strings.Contains(message, "not exist")
}

// CreateVersionRequest 是创建环境版本的输入。
type CreateVersionRequest struct {
	// MachineID 是目标执行节点。
	MachineID string
	// Ref 是环境引用。
	Ref domain.EnvironmentRef
	// Dockerfile 是新版本的 Dockerfile 内容。
	Dockerfile []byte
	// ParentVersion 是依赖的父基础环境版本标签，语言环境必填。
	ParentVersion string
	// ExtraFiles 是构建上下文中的额外文件，相对路径必须已规范化。
	ExtraFiles []docker.ContextFile
	// Credentials 提供本次连接所需的凭据。
	Credentials sshclient.CredentialProvider
}

// CreateVersion 保存一个新的不可覆盖的环境版本。
//
// 保存流程：读取已用最大版本号 → 计算 Dockerfile 与上下文哈希 → 在远程创建
// 版本目录并写入文件 → 写入 SQLite 元数据。版本目录创建后不会被后续版本覆盖；
// 内容与当前版本完全一致时不会重复创建新版本，而是返回现有版本。
func (s *EnvironmentService) CreateVersion(
	ctx context.Context,
	request CreateVersionRequest,
) (domain.EnvironmentVersion, error) {
	if err := request.Ref.Validate(); err != nil {
		return domain.EnvironmentVersion{}, fmt.Errorf("创建环境版本失败: %w", err)
	}
	if err := docker.ValidateDockerfile(request.Dockerfile); err != nil {
		return domain.EnvironmentVersion{}, fmt.Errorf("创建环境版本失败: %w", err)
	}
	var created domain.EnvironmentVersion
	err := s.withRemoteDocker(
		ctx,
		request.MachineID,
		request.Credentials,
		false,
		func(ctx context.Context, session *remoteDocker) error {
			unlock := s.locks.Lock(lockKey(session.host.ID, request.Ref))
			defer unlock()
			version, err := s.createVersionLocked(ctx, session, request)
			if err != nil {
				return err
			}
			created = version
			return nil
		},
	)
	if err != nil {
		return domain.EnvironmentVersion{}, err
	}
	s.debug(
		ctx,
		"环境版本已创建",
		slog.String("machine", request.MachineID),
		slog.String("environment", request.Ref.Label()),
		slog.String("version", created.Label()),
	)
	return created, nil
}

// lockKey 返回"节点+环境"维度的锁键。
func lockKey(hostID domain.ID, ref domain.EnvironmentRef) string {
	return hostID.String() + "|" + ref.Label()
}

// createVersionLocked 在持锁状态下创建版本。
func (s *EnvironmentService) createVersionLocked(
	ctx context.Context,
	session *remoteDocker,
	request CreateVersionRequest,
) (domain.EnvironmentVersion, error) {
	context, err := buildEnvironmentContext(request.Dockerfile, request.ExtraFiles)
	if err != nil {
		return domain.EnvironmentVersion{}, fmt.Errorf("创建环境版本失败: %w", err)
	}
	dockerfileHash := sha256Hex(request.Dockerfile)
	contextHash := context.Hash()
	existing, err := s.store.ListEnvironmentVersions(ctx, storage.EnvironmentVersionFilter{
		HostID: session.host.ID,
		Ref:    &request.Ref,
	})
	if err != nil {
		return domain.EnvironmentVersion{}, fmt.Errorf("读取环境版本失败: %w", err)
	}
	if reuse, ok := findReusableVersion(
		existing,
		dockerfileHash,
		contextHash,
		strings.TrimSpace(request.ParentVersion),
	); ok {
		return reuse, nil
	}
	next, err := domain.NextVersionNumber(versionNumbers(existing))
	if err != nil {
		return domain.EnvironmentVersion{}, fmt.Errorf("创建环境版本失败: %w", err)
	}
	version := domain.EnvironmentVersion{
		HostID:           session.host.ID,
		Ref:              request.Ref,
		Number:           next,
		DockerfileSHA256: dockerfileHash,
		ContextSHA256:    contextHash,
		ParentVersion:    strings.TrimSpace(request.ParentVersion),
		Status:           domain.BuildStatusPending,
		CreatedAt:        time.Now().UTC().Truncate(time.Millisecond),
	}
	parentImageRef := ""
	if !request.Ref.IsBase() {
		resolved, parentErr := s.resolveParentVersion(ctx, session.host.ID, &version)
		if parentErr != nil {
			return domain.EnvironmentVersion{}, parentErr
		}
		parentImageRef = resolved
	}
	// 构建配置哈希与镜像标签在创建版本时即固定：后续构建只更新构建结果，
	// 不会因为父镜像被重新构建而改变本版本的标签。
	version.BuildConfigSHA256 = docker.BuildConfigHash(version, parentImageRef)
	imageRef, err := docker.ImageRefFor(version)
	if err != nil {
		return domain.EnvironmentVersion{}, fmt.Errorf("创建环境版本失败: %w", err)
	}
	version.ImageRef = imageRef
	if err := version.Validate(); err != nil {
		return domain.EnvironmentVersion{}, fmt.Errorf("创建环境版本失败: %w", err)
	}
	if err := s.writeVersionFiles(ctx, session, version, context); err != nil {
		return domain.EnvironmentVersion{}, err
	}
	if err := s.store.CreateEnvironmentVersion(ctx, version); err != nil {
		return domain.EnvironmentVersion{}, fmt.Errorf("保存环境版本失败: %w", err)
	}
	return version, nil
}

// resolveParentVersion 校验语言环境依赖的父基础环境版本并返回其镜像引用。
func (s *EnvironmentService) resolveParentVersion(
	ctx context.Context,
	hostID domain.ID,
	version *domain.EnvironmentVersion,
) (string, error) {
	if version.ParentVersion == "" {
		return "", fmt.Errorf(
			"创建环境版本失败: 语言环境 %s 必须指定父基础环境版本",
			version.Ref.Name,
		)
	}
	parentNumber, err := domain.ParseVersionLabel(version.ParentVersion)
	if err != nil {
		return "", fmt.Errorf("创建环境版本失败: %w", err)
	}
	baseRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		return "", err
	}
	parent, err := s.store.GetEnvironmentVersion(ctx, hostID, baseRef, parentNumber)
	if err != nil {
		return "", fmt.Errorf("创建环境版本失败: 父基础环境版本不存在: %w", err)
	}
	if !parent.Usable() {
		return "", fmt.Errorf(
			"创建环境版本失败: 父基础环境版本 %s 尚未构建成功",
			parent.Label(),
		)
	}
	return parent.ImageRef, nil
}

// writeVersionFiles 在远程创建版本目录并写入 Dockerfile 与清单。
func (s *EnvironmentService) writeVersionFiles(
	ctx context.Context,
	session *remoteDocker,
	version domain.EnvironmentVersion,
	context docker.Context,
) error {
	versionDir := session.layout.VersionDir(version.Ref, version.Number)
	if _, err := session.transfer.Stat(ctx, versionDir); err == nil {
		return fmt.Errorf(
			"创建环境版本失败: 远程版本目录已存在，历史版本不可覆盖: %s",
			versionDir,
		)
	} else if !isRemoteNotExist(err) {
		return fmt.Errorf("检查远程版本目录失败: %s: %w", versionDir, err)
	}
	if err := session.transfer.MkdirAll(ctx, versionDir); err != nil {
		return fmt.Errorf("创建远程版本目录失败: %s: %w", versionDir, err)
	}
	for _, file := range context.Files {
		target, err := sshclient.JoinRemotePath(versionDir, file.RelativePath)
		if err != nil {
			return fmt.Errorf("拼接远程路径失败: %w", err)
		}
		if err := session.transfer.WriteFile(ctx, target, file.Content, sshclient.WriteOptions{
			Overwrite:    false,
			Mode:         file.Mode,
			CreateParent: true,
		}); err != nil {
			return fmt.Errorf("写入远程版本文件失败: %s: %w", target, err)
		}
	}
	manifest, err := json.MarshalIndent(version, "", "  ")
	if err != nil {
		return fmt.Errorf("编码版本清单失败: %w", err)
	}
	manifestPath := session.layout.ManifestPath(version.Ref, version.Number)
	if err := session.transfer.WriteFile(ctx, manifestPath, append(manifest, '\n'), sshclient.WriteOptions{
		Overwrite:    false,
		Mode:         0o644,
		CreateParent: true,
	}); err != nil {
		return fmt.Errorf("写入版本清单失败: %s: %w", manifestPath, err)
	}
	return nil
}

// writeLanguageManifest 写入语言环境定义清单。
func (s *EnvironmentService) writeLanguageManifest(
	ctx context.Context,
	session *remoteDocker,
	ref domain.EnvironmentRef,
	template docker.LanguageTemplate,
) error {
	manifest := docker.NewLanguageManifest(template, time.Now().UTC().Format(time.RFC3339))
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("编码语言环境清单失败: %w", err)
	}
	target := session.layout.LanguageManifestPath(ref)
	if err := session.transfer.WriteFile(ctx, target, append(content, '\n'), sshclient.WriteOptions{
		Overwrite:    true,
		Mode:         0o644,
		CreateParent: true,
	}); err != nil {
		return fmt.Errorf("写入语言环境清单失败: %s: %w", target, err)
	}
	return nil
}

// buildEnvironmentContext 构造构建上下文。
//
// 上下文只包含 Dockerfile 与显式给出的额外文件，不包含项目源代码。
func buildEnvironmentContext(dockerfile []byte, extra []docker.ContextFile) (docker.Context, error) {
	files := make([]docker.ContextFile, 0, len(extra)+1)
	files = append(files, docker.ContextFile{
		RelativePath: docker.DockerfileName,
		Content:      dockerfile,
		Mode:         0o644,
	})
	files = append(files, extra...)
	context, err := docker.NewContext(files)
	if err != nil {
		return docker.Context{}, err
	}
	if err := context.Validate(); err != nil {
		return docker.Context{}, err
	}
	return context, nil
}

// sha256Hex 计算内容的 SHA-256 十六进制摘要。
func sha256Hex(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

// versionNumbers 提取版本号列表。
func versionNumbers(versions []domain.EnvironmentVersion) []int {
	numbers := make([]int, 0, len(versions))
	for _, version := range versions {
		numbers = append(numbers, version.Number)
	}
	return numbers
}

// findReusableVersion 查找内容完全一致且已成功构建的历史版本。
//
// 只有在 Dockerfile 哈希、构建上下文哈希与父环境版本都未变化时才复用；
// 复用前调用方必须再核验镜像仍然存在（在 Build 中完成）。
func findReusableVersion(
	versions []domain.EnvironmentVersion,
	dockerfileHash, contextHash, parentVersion string,
) (domain.EnvironmentVersion, bool) {
	for _, version := range versions {
		if version.DockerfileSHA256 != dockerfileHash || version.ContextSHA256 != contextHash {
			continue
		}
		if version.ParentVersion != parentVersion {
			continue
		}
		if version.Status != domain.BuildStatusSucceeded {
			continue
		}
		return version, true
	}
	return domain.EnvironmentVersion{}, false
}

// currentUsableVersion 返回指定环境当前最新的可用版本。
func (s *EnvironmentService) currentUsableVersion(
	ctx context.Context,
	hostID domain.ID,
	ref domain.EnvironmentRef,
) (domain.EnvironmentVersion, error) {
	versions, err := s.store.ListEnvironmentVersions(ctx, storage.EnvironmentVersionFilter{
		HostID:   hostID,
		Ref:      &ref,
		Statuses: []domain.BuildStatus{domain.BuildStatusSucceeded},
	})
	if err != nil {
		return domain.EnvironmentVersion{}, err
	}
	if len(versions) == 0 {
		return domain.EnvironmentVersion{}, fmt.Errorf(
			"环境 %s 没有构建成功的版本",
			ref.Label(),
		)
	}
	return versions[len(versions)-1], nil
}
