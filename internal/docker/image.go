package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
)

// 构建相关常量。
const (
	// DefaultImageNamespace 是镜像名的默认命名空间。
	DefaultImageNamespace = "agentflow"
	// BuildSummaryTailLines 是本机回传的构建日志末尾行数。
	BuildSummaryTailLines = 40
	// MaxBuildLogBytes 是允许下载的构建日志大小上限。
	MaxBuildLogBytes = 8 << 20
	// OwnershipLabelKey 是标识资源由 AgentFlow 创建的标签键。
	OwnershipLabelKey = "agentflow.managed"
	// HostLabelKey 是标识资源所属执行节点的标签键。
	HostLabelKey = "agentflow.host_id"
	// EnvironmentLabelKey 是标识环境引用的标签键。
	EnvironmentLabelKey = "agentflow.environment"
	// EnvironmentVersionLabelKey 是标识环境版本标签的标签键。
	EnvironmentVersionLabelKey = "agentflow.environment_version"
	// BuildConfigLabelKey 是标识构建配置哈希的标签键。
	BuildConfigLabelKey = "agentflow.build_config_sha256"
	// ContainerLabelKey 是标识容器登记记录 ID 的标签键。
	ContainerLabelKey = "agentflow.container_id"
	// TaskLabelKey 是标识任务 ID 的标签键。
	TaskLabelKey = "agentflow.task_id"
	// AttemptLabelKey 是标识尝试 ID 的标签键。
	AttemptLabelKey = "agentflow.attempt_id"
)

// BuildSpec 描述一次镜像构建。
type BuildSpec struct {
	// Version 是要构建的环境版本。
	Version domain.EnvironmentVersion
	// ParentImageRef 是父环境镜像引用，基础环境为空。
	ParentImageRef string
	// Context 是构建上下文。
	Context Context
	// RemoteContextDir 是构建上下文在远程机器上的目录。
	RemoteContextDir string
	// LogPath 是远程构建日志文件路径。
	LogPath string
	// BuildID 是本次构建的标识，用于日志目录命名。
	BuildID string
	// NoCache 表示禁用 Docker 构建缓存。
	NoCache bool
}

// BuildResult 是一次镜像构建的结果。
type BuildResult struct {
	// ImageRef 是构建使用的镜像引用。
	ImageRef string
	// ImageID 是核实后的实际镜像 ID。
	ImageID string
	// LogPath 是远程构建日志路径。
	LogPath string
	// LogTail 是构建日志末尾摘要。
	LogTail string
	// DurationSeconds 是构建耗时秒数。
	DurationSeconds int64
	// Cached 表示复用了已有的成功构建结果。
	Cached bool
}

// BuildConfigHash 计算构建配置的哈希。
//
// 哈希覆盖会影响构建产物的全部输入：环境引用、版本号、Dockerfile 内容哈希、
// 构建上下文哈希与父镜像引用。Dockerfile 内容哈希与之分开保存，便于区分
// "仅 Dockerfile 变化"与"父镜像或上下文变化"。
//
// --no-cache 不参与哈希：它只影响是否复用 Docker 构建缓存，不改变版本身份，
// 否则同一个版本会因为构建选项不同而得到不同的镜像标签。
func BuildConfigHash(version domain.EnvironmentVersion, parentImageRef string) string {
	digest := sha256.New()
	writeHashField(digest, version.Ref.Label())
	writeHashField(digest, version.Label())
	writeHashField(digest, version.DockerfileSHA256)
	writeHashField(digest, version.ContextSHA256)
	writeHashField(digest, parentImageRef)
	return hex.EncodeToString(digest.Sum(nil))
}

// ImageRefFor 生成可追溯的镜像引用。
//
// 标签由环境引用、版本号与构建配置哈希前缀组成，不使用 latest：
// 同一版本号的配置内容不同会得到不同标签，避免历史结果被覆盖。
func ImageRefFor(version domain.EnvironmentVersion) (string, error) {
	if err := version.Ref.Validate(); err != nil {
		return "", err
	}
	hashPrefix := version.BuildConfigSHA256
	if len(hashPrefix) > 12 {
		hashPrefix = hashPrefix[:12]
	}
	repository := strings.Join([]string{
		DefaultImageNamespace,
		version.Ref.Type.String(),
		version.Ref.Name,
	}, "-")
	ref := fmt.Sprintf("%s:%s-%s", repository, version.Label(), hashPrefix)
	if err := domain.ValidateImageRef(ref); err != nil {
		return "", err
	}
	return ref, nil
}

// ImageInspect 是 docker image inspect 的关键字段。
type ImageInspect struct {
	// ID 是实际镜像 ID。
	ID string
	// RepoTags 是镜像的仓库标签。
	RepoTags []string
	// Created 是镜像创建时间。
	Created string
	// Size 是镜像大小（字节）。
	Size int64
	// Labels 是镜像标签，用于核验构建配置哈希。
	Labels map[string]string
}

// UploadContext 把构建上下文上传到远程目录。
//
// 上传前先递归清理该目录，避免上一次构建的残留文件混入上下文；上传后逐个校验
// 文件大小，确保远程内容与本机一致。
func (r *Runner) UploadContext(
	ctx context.Context,
	transfer sshclient.FileTransfer,
	spec BuildSpec,
) error {
	if transfer == nil {
		return newError(CodeSSHUnavailable, "upload context", "SFTP 通道不可用", nil)
	}
	if strings.TrimSpace(spec.RemoteContextDir) == "" {
		return newError(CodeInvalidArgument, "upload context", "远程上下文目录不能为空", nil)
	}
	if err := transfer.MkdirAll(ctx, spec.RemoteContextDir); err != nil {
		return newError(CodeSSHUnavailable, "upload context", "创建远程上下文目录失败", err)
	}
	for _, file := range spec.Context.Files {
		target, err := sshclient.JoinRemotePath(spec.RemoteContextDir, file.RelativePath)
		if err != nil {
			return newError(CodeInvalidArgument, "upload context", "拼接远程路径失败", err)
		}
		writeErr := transfer.WriteFile(ctx, target, file.Content, sshclient.WriteOptions{
			Overwrite:    true,
			Mode:         file.Mode,
			CreateParent: true,
		})
		if writeErr != nil {
			return newError(
				CodeSSHUnavailable,
				"upload context",
				"上传上下文文件失败: "+file.RelativePath,
				writeErr,
			)
		}
	}
	return nil
}

// BuildImage 在远程机器上构建镜像并核实实际镜像 ID。
//
// 构建日志写入远程文件，只把末尾若干行回传到本机，避免把完整构建输出缓存在
// 本机内存中。构建成功后必须能检查到镜像，否则返回 CodeImageMissing，
// 由调用方把版本标记为失败而不是成功。
func (r *Runner) BuildImage(
	ctx context.Context,
	transfer sshclient.FileTransfer,
	spec BuildSpec,
) (BuildResult, error) {
	result := BuildResult{ImageRef: spec.Version.ImageRef, LogPath: spec.LogPath}
	if spec.Version.ImageRef == "" {
		return result, newError(CodeInvalidArgument, "docker build", "镜像引用不能为空", nil)
	}
	if err := spec.Context.Validate(); err != nil {
		return result, newError(CodeInvalidArgument, "docker build", err.Error(), err)
	}
	if err := r.UploadContext(ctx, transfer, spec); err != nil {
		return result, err
	}
	args := []string{
		"--tag", spec.Version.ImageRef,
		"--label", OwnershipLabelKey + "=true",
		"--label", HostLabelKey + "=" + spec.Version.HostID.String(),
		"--label", EnvironmentLabelKey + "=" + spec.Version.Ref.Label(),
		"--label", EnvironmentVersionLabelKey + "=" + spec.Version.Label(),
		"--label", BuildConfigLabelKey + "=" + spec.Version.BuildConfigSHA256,
		"--file", spec.ContextPath(DockerfileName),
	}
	if spec.ParentImageRef != "" {
		args = append(args, "--build-arg", "AGENTFLOW_BASE_IMAGE="+spec.ParentImageRef)
	}
	if spec.NoCache {
		args = append(args, "--no-cache")
	}
	args = append(args, "--progress", "plain", spec.RemoteContextDir)
	command, err := dockerCommand("build", args...)
	if err != nil {
		return result, err
	}
	logged, err := BuildLogCommand(command, spec.LogPath)
	if err != nil {
		return result, err
	}
	run, runErr := r.Run(ctx, "docker build", logged, DefaultBuildTimeout)
	result.DurationSeconds = int64(run.Duration.Seconds())
	if tailErr := r.readLogTail(ctx, transfer, spec.LogPath, &result); tailErr != nil && runErr == nil {
		return result, tailErr
	}
	if runErr != nil {
		if run.Uncertain() {
			return result, newError(
				CodeStateUncertain,
				"docker build",
				"构建结果不确定，请检查远程日志: "+spec.LogPath,
				runErr,
			)
		}
		return result, newError(CodeBuildFailed, "docker build", buildFailureDetail(run, result.LogTail), runErr)
	}
	imageID, inspectErr := r.InspectImageID(ctx, spec.Version.ImageRef)
	if inspectErr != nil {
		return result, inspectErr
	}
	result.ImageID = imageID
	return result, nil
}

// ContextPath 返回构建上下文内某个文件的远程路径。
func (spec BuildSpec) ContextPath(relative string) string {
	joined := strings.TrimSuffix(spec.RemoteContextDir, "/") + "/" + strings.TrimPrefix(relative, "/")
	return joined
}

// BuildLogCommand 把构建命令包装为"同时写入远程日志文件"的形式。
//
// 使用 tee 把输出写入远程日志，同时保留标准输出，使本机仍能拿到有界摘要；
// 退出码通过 PIPESTATUS 透传，避免 tee 成功掩盖构建失败。
func BuildLogCommand(command, logPath string) (string, error) {
	if strings.TrimSpace(logPath) == "" {
		return "", newError(CodeInvalidArgument, "docker build", "构建日志路径不能为空", nil)
	}
	quotedLog := sshclient.ShellQuote(logPath)
	quotedDir := sshclient.ShellQuote(pathDir(logPath))
	script := fmt.Sprintf(
		"mkdir -p %s && %s 2>&1 | tee %s; exit ${PIPESTATUS[0]}",
		quotedDir,
		command,
		quotedLog,
	)
	return script, nil
}

// pathDir 返回远程路径的父目录。
func pathDir(target string) string {
	index := strings.LastIndex(target, "/")
	if index <= 0 {
		return "/"
	}
	return target[:index]
}

// readLogTail 读取构建日志末尾若干行作为摘要。
func (r *Runner) readLogTail(
	ctx context.Context,
	transfer sshclient.FileTransfer,
	logPath string,
	result *BuildResult,
) error {
	if transfer == nil || strings.TrimSpace(logPath) == "" {
		return nil
	}
	content, err := transfer.ReadFile(ctx, logPath, MaxBuildLogBytes)
	if err != nil {
		// 日志缺失不视为构建失败：构建命令本身的结果才是判定依据。
		result.LogTail = "（无法读取远程构建日志: " + summarizeOutput(err.Error()) + "）"
		return nil
	}
	result.LogTail = tailLines(string(content), BuildSummaryTailLines)
	return nil
}

// tailLines 返回文本末尾的若干行。
func tailLines(text string, count int) string {
	trimmed := strings.TrimRight(text, "\n")
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	if count > 0 && len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "\n")
}

// buildFailureDetail 生成构建失败的可读说明。
func buildFailureDetail(run CommandResult, logTail string) string {
	detail := fmt.Sprintf("退出码 %d", run.ExitCode)
	if summary := summarizeOutput(run.Stderr); summary != "" {
		detail += ": " + summary
	}
	if logTail != "" {
		detail += " | 日志末尾: " + summarizeOutput(logTail)
	}
	return detail
}

// InspectImageID 检查镜像是否存在并返回实际镜像 ID。
//
// 镜像标签不能代替实际镜像 ID：构建成功后必须通过 inspect 取得真实 ID 并持久化。
func (r *Runner) InspectImageID(ctx context.Context, imageRef string) (string, error) {
	inspect, err := r.InspectImage(ctx, imageRef)
	if err != nil {
		return "", err
	}
	if err := domain.ValidateImageID(inspect.ID); err != nil {
		return "", newError(
			CodeImageMissing,
			"docker image inspect",
			"镜像 ID 格式不合法: "+inspect.ID,
			err,
		)
	}
	return inspect.ID, nil
}

// InspectImage 检查镜像并返回关键字段。
func (r *Runner) InspectImage(ctx context.Context, imageRef string) (ImageInspect, error) {
	if strings.TrimSpace(imageRef) == "" {
		return ImageInspect{}, newError(CodeInvalidArgument, "docker image inspect", "镜像引用不能为空", nil)
	}
	command, err := dockerCommand("image", "inspect", imageRef)
	if err != nil {
		return ImageInspect{}, err
	}
	run, runErr := r.RunQuery(ctx, "docker image inspect", command)
	if runErr != nil {
		if ErrorCodeOf(runErr) == CodeContainerNotFound {
			return ImageInspect{}, newError(
				CodeImageMissing,
				"docker image inspect",
				"镜像不存在: "+imageRef,
				runErr,
			)
		}
		return ImageInspect{}, runErr
	}
	inspect, parseErr := ParseImageInspect(run.Stdout)
	if parseErr != nil {
		return ImageInspect{}, parseErr
	}
	if !containsString(inspect.RepoTags, imageRef) {
		return ImageInspect{}, newError(
			CodeImageMissing,
			"docker image inspect",
			"镜像标签与预期不一致: "+imageRef,
			nil,
		)
	}
	return inspect, nil
}

// imageInspectEntry 是 docker image inspect JSON 数组的元素。
type imageInspectEntry struct {
	ID       string   `json:"Id"`
	RepoTags []string `json:"RepoTags"`
	Created  string   `json:"Created"`
	Size     int64    `json:"Size"`
	Config   struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// ParseImageInspect 解析 docker image inspect 的 JSON 输出。
func ParseImageInspect(output string) (ImageInspect, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return ImageInspect{}, newError(
			CodeImageMissing,
			"docker image inspect",
			"镜像检查输出为空",
			nil,
		)
	}
	var entries []imageInspectEntry
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		return ImageInspect{}, newError(
			CodeImageMissing,
			"docker image inspect",
			"解析镜像检查结果失败",
			err,
		)
	}
	if len(entries) == 0 {
		return ImageInspect{}, newError(
			CodeImageMissing,
			"docker image inspect",
			"镜像检查结果为空",
			nil,
		)
	}
	return ImageInspect{
		ID:       entries[0].ID,
		RepoTags: entries[0].RepoTags,
		Created:  entries[0].Created,
		Size:     entries[0].Size,
		Labels:   entries[0].Config.Labels,
	}, nil
}

// containsString 判断字符串是否在切片中。
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// RemoveRemoteDirectory 清理远程临时目录。
//
// 该方法只用于清理本次构建上传的上下文目录与任务级临时资源，
// 不会清理共享镜像、共享缓存卷或项目配置目录。
func (r *Runner) RemoveRemoteDirectory(
	ctx context.Context,
	transfer sshclient.FileTransfer,
	target string,
) error {
	if transfer == nil {
		return newError(CodeSSHUnavailable, "remove directory", "SFTP 通道不可用", nil)
	}
	if err := transfer.RemoveAll(ctx, target); err != nil {
		return newError(CodeSSHUnavailable, "remove directory", "清理远程目录失败", err)
	}
	return nil
}
