package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// 容器运行的默认约束。
const (
	// DefaultRunContractCommand 是基础环境容器的常驻命令。
	//
	// 基础镜像必须能执行该命令，否则说明镜像不满足常驻运行契约。
	DefaultRunContractCommand = "sleep infinity"
	// RunContractCheckTimeout 是运行契约预检的超时时间。
	RunContractCheckTimeout = 30 * time.Second
)

// CacheVolumeSpec 是一个缓存卷的挂载定义。
type CacheVolumeSpec struct {
	// VolumeName 是 Docker Named Volume 名称。
	VolumeName string
	// ContainerPath 是容器内挂载路径。
	ContainerPath string
}

// CreateContainerSpec 描述一次容器创建。
type CreateContainerSpec struct {
	// Container 是容器登记记录。
	Container domain.Container
	// ImageRef 是使用的镜像引用。
	ImageRef string
	// WorkDir 是容器内项目工作目录。
	WorkDir string
	// Command 是容器启动命令，空值使用 DefaultRunContractCommand。
	Command []string
	// Caches 是允许挂载的缓存卷。
	Caches []CacheVolumeSpec
}

// CreateContainer 在远程机器上创建容器并核验实际容器 ID。
//
// 安全边界在这里统一实施，不依赖用户 Dockerfile：
//   - 不使用 --privileged；
//   - 不使用 --pid=host 或 --network=host；
//   - 不挂载宿主机 Docker Socket、用户 Home 或远程项目配置目录；
//   - 只挂载显式声明的缓存卷，项目代码只存在于容器内部可写层。
func (r *Runner) CreateContainer(
	ctx context.Context,
	spec CreateContainerSpec,
) (string, error) {
	if err := spec.Container.Validate(); err != nil {
		return "", newError(CodeInvalidArgument, "docker create", err.Error(), err)
	}
	args, err := ContainerCreateArgs(spec)
	if err != nil {
		return "", err
	}
	command, err := dockerCommand("create", args...)
	if err != nil {
		return "", err
	}
	run, runErr := r.RunMutate(ctx, "docker create", command)
	if runErr != nil {
		if run.Uncertain() {
			return "", newError(
				CodeStateUncertain,
				"docker create",
				"创建结果不确定，请先按名称核验容器是否存在: "+spec.Container.Name,
				runErr,
			)
		}
		return "", runErr
	}
	dockerID := strings.TrimSpace(run.Stdout)
	if dockerID == "" {
		return "", newError(
			CodeStateUncertain,
			"docker create",
			"创建命令未返回容器 ID，请先按名称核验",
			nil,
		)
	}
	return dockerID, nil
}

// ContainerCreateArgs 构造 docker create 的参数列表。
//
// 参数顺序固定，所有值都经过 shell 转义后再由 dockerCommand 统一处理。
func ContainerCreateArgs(spec CreateContainerSpec) ([]string, error) {
	name := spec.Container.Name
	if err := domain.ValidateContainerName(name); err != nil {
		return nil, newError(CodeInvalidArgument, "docker create", err.Error(), err)
	}
	imageRef := strings.TrimSpace(spec.ImageRef)
	if err := domain.ValidateImageRef(imageRef); err != nil {
		return nil, newError(CodeInvalidArgument, "docker create", err.Error(), err)
	}
	workDir := strings.TrimSpace(spec.WorkDir)
	if workDir == "" {
		workDir = domain.DefaultContainerWorkDir
	}
	if err := domain.ValidateContainerWorkDir(workDir); err != nil {
		return nil, newError(CodeInvalidArgument, "docker create", err.Error(), err)
	}
	args := []string{
		"--name", name,
		"--detach",
		"--workdir", workDir,
		"--label", OwnershipLabelKey + "=true",
		"--label", HostLabelKey + "=" + spec.Container.HostID.String(),
		"--label", ContainerLabelKey + "=" + spec.Container.ID.String(),
		"--label", EnvironmentLabelKey + "=" + spec.Container.Environment.Label(),
		"--label", EnvironmentVersionLabelKey + "=" + spec.Container.EnvironmentVersion,
		"--label", BuildConfigLabelKey + "=" + spec.Container.BuildConfigSHA256,
	}
	if spec.Container.TaskID != nil {
		args = append(args, "--label", TaskLabelKey+"="+spec.Container.TaskID.String())
	}
	if spec.Container.AttemptID != nil {
		args = append(args, "--label", AttemptLabelKey+"="+spec.Container.AttemptID.String())
	}
	for _, cache := range spec.Caches {
		if strings.TrimSpace(cache.VolumeName) == "" {
			return nil, newError(CodeInvalidArgument, "docker create", "缓存卷名不能为空", nil)
		}
		if strings.TrimSpace(cache.ContainerPath) == "" {
			return nil, newError(CodeInvalidArgument, "docker create", "缓存挂载路径不能为空", nil)
		}
		args = append(args, "--mount", cacheMountOption(cache))
	}
	args = append(args, imageRef)
	command := spec.Command
	if len(command) == 0 {
		command = []string{"sh", "-c", DefaultRunContractCommand}
	}
	args = append(args, command...)
	return args, nil
}

// cacheMountOption 构造缓存卷挂载参数。
//
// 使用 volume 类型而不是 bind，避免把宿主机目录暴露给容器。
func cacheMountOption(cache CacheVolumeSpec) string {
	return fmt.Sprintf("type=volume,source=%s,target=%s", cache.VolumeName, cache.ContainerPath)
}

// sortedLabelPairs 把标签映射展开为按键排序的 key=value 列表，保证命令可复现。
func sortedLabelPairs(labels map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		if strings.TrimSpace(key) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+labels[key])
	}
	return pairs
}

// StartContainer 启动容器。
//
// 启动命令本身幂等：容器已运行时 docker start 直接成功，不会产生额外容器。
func (r *Runner) StartContainer(ctx context.Context, dockerID string) error {
	if strings.TrimSpace(dockerID) == "" {
		return newError(CodeInvalidArgument, "docker start", "容器 ID 不能为空", nil)
	}
	command, err := dockerCommand("start", dockerID)
	if err != nil {
		return err
	}
	run, runErr := r.RunMutate(ctx, "docker start", command)
	if runErr != nil {
		if run.Uncertain() {
			return newError(
				CodeStateUncertain,
				"docker start",
				"启动结果不确定，请先核验容器状态",
				runErr,
			)
		}
		return runErr
	}
	return nil
}

// StopContainer 停止容器，超时后由 Docker 强制终止。
func (r *Runner) StopContainer(ctx context.Context, dockerID string, timeoutSeconds int) error {
	if strings.TrimSpace(dockerID) == "" {
		return newError(CodeInvalidArgument, "docker stop", "容器 ID 不能为空", nil)
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 10
	}
	command, err := dockerCommand("stop", "--time", fmt.Sprintf("%d", timeoutSeconds), dockerID)
	if err != nil {
		return err
	}
	run, runErr := r.RunMutate(ctx, "docker stop", command)
	if runErr != nil {
		if run.Uncertain() {
			return newError(
				CodeStateUncertain,
				"docker stop",
				"停止结果不确定，请先核验容器状态",
				runErr,
			)
		}
		return runErr
	}
	return nil
}

// RemoveContainer 删除容器。
//
// 删除已不存在的容器视为成功，使删除操作具备幂等性；容器不存在时返回
// Removed=true 与 existed=false，调用方仍需更新本地记录。
func (r *Runner) RemoveContainer(
	ctx context.Context,
	dockerID string,
) (RemoveResult, error) {
	result := RemoveResult{}
	if strings.TrimSpace(dockerID) == "" {
		return result, newError(CodeInvalidArgument, "docker rm", "容器 ID 不能为空", nil)
	}
	command, err := dockerCommand("rm", "--force", dockerID)
	if err != nil {
		return result, err
	}
	run, runErr := r.RunMutate(ctx, "docker rm", command)
	if runErr != nil {
		if run.Uncertain() {
			return result, newError(
				CodeStateUncertain,
				"docker rm",
				"删除结果不确定，请先核验容器是否仍存在",
				runErr,
			)
		}
		if ErrorCodeOf(runErr) == CodeContainerNotFound {
			result.Removed = true
			result.Existed = false
			return result, nil
		}
		return result, runErr
	}
	result.Removed = true
	result.Existed = true
	return result, nil
}

// RemoveResult 是删除容器的结果。
type RemoveResult struct {
	// Removed 表示删除目标已经不存在（幂等成功）。
	Removed bool
	// Existed 表示删除前容器确实存在。
	Existed bool
}

// ContainerInspect 是 docker inspect 的关键字段。
type ContainerInspect struct {
	// ID 是容器 ID。
	ID string
	// Name 是容器名称，不含前导斜杠。
	Name string
	// State 是 Docker 侧状态。
	State domain.DockerContainerState
	// Running 表示容器是否正在运行。
	Running bool
	// ExitCode 是容器退出码。
	ExitCode int
	// ImageID 是容器使用的镜像 ID。
	ImageID string
	// Labels 是容器标签。
	Labels map[string]string
	// StartedAt 是容器启动时间文本。
	StartedAt string
	// FinishedAt 是容器结束时间文本。
	FinishedAt string
	// Error 是 Docker 报告的容器错误。
	Error string
}

// InspectContainer 检查容器并返回关键字段。
//
// 容器不存在时返回 CodeContainerNotFound，调用方据此判断容器已被外部删除。
func (r *Runner) InspectContainer(ctx context.Context, dockerID string) (ContainerInspect, error) {
	if strings.TrimSpace(dockerID) == "" {
		return ContainerInspect{}, newError(CodeInvalidArgument, "docker inspect", "容器 ID 不能为空", nil)
	}
	command, err := dockerCommand("inspect", dockerID)
	if err != nil {
		return ContainerInspect{}, err
	}
	run, runErr := r.RunQuery(ctx, "docker inspect", command)
	if runErr != nil {
		if ErrorCodeOf(runErr) == CodeContainerNotFound {
			return ContainerInspect{}, newError(
				CodeContainerNotFound,
				"docker inspect",
				"容器不存在: "+dockerID,
				runErr,
			)
		}
		return ContainerInspect{}, runErr
	}
	return ParseContainerInspect(run.Stdout)
}

// containerInspectEntry 是 docker inspect JSON 数组的元素。
type containerInspectEntry struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Image  string `json:"Image"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		ExitCode   int    `json:"ExitCode"`
		Error      string `json:"Error"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
	} `json:"State"`
}

// ParseContainerInspect 解析 docker inspect 的 JSON 输出。
func ParseContainerInspect(output string) (ContainerInspect, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return ContainerInspect{}, newError(
			CodeStateUncertain,
			"docker inspect",
			"容器检查输出为空",
			nil,
		)
	}
	var entries []containerInspectEntry
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		return ContainerInspect{}, newError(
			CodeStateUncertain,
			"docker inspect",
			"解析容器检查结果失败",
			err,
		)
	}
	if len(entries) == 0 {
		return ContainerInspect{}, newError(
			CodeContainerNotFound,
			"docker inspect",
			"容器检查结果为空",
			nil,
		)
	}
	entry := entries[0]
	return ContainerInspect{
		ID:         entry.ID,
		Name:       strings.TrimPrefix(entry.Name, "/"),
		State:      domain.ParseDockerContainerState(entry.State.Status),
		Running:    entry.State.Running,
		ExitCode:   entry.State.ExitCode,
		ImageID:    entry.Image,
		Labels:     entry.Config.Labels,
		StartedAt:  entry.State.StartedAt,
		FinishedAt: entry.State.FinishedAt,
		Error:      entry.State.Error,
	}, nil
}

// VerifyOwnership 核验容器标签与登记记录是否一致。
//
// 所有权核验不能只看容器名称：必须同时核对 AgentFlow 管理标志、节点 ID 与
// 容器登记记录 ID。标签不一致时返回 CodeOwnershipMismatch，调用方不得继续
// 执行停止或删除。
func VerifyOwnership(inspect ContainerInspect, expected domain.Container) error {
	if inspect.Labels[OwnershipLabelKey] != "true" {
		return newError(
			CodeOwnershipMismatch,
			"ownership",
			"容器缺少 AgentFlow 管理标签",
			nil,
		)
	}
	if actual := inspect.Labels[HostLabelKey]; actual != expected.HostID.String() {
		return newError(
			CodeOwnershipMismatch,
			"ownership",
			fmt.Sprintf("容器所属节点标签不一致: 期望 %s，实际 %s", expected.HostID, actual),
			nil,
		)
	}
	if actual := inspect.Labels[ContainerLabelKey]; actual != expected.ID.String() {
		return newError(
			CodeOwnershipMismatch,
			"ownership",
			fmt.Sprintf("容器登记记录标签不一致: 期望 %s，实际 %s", expected.ID, actual),
			nil,
		)
	}
	return nil
}

// ExecCommand 在运行中的容器内执行一条非交互命令。
//
// 命令通过 docker exec 执行，输出受 sshclient 的有界缓冲限制；
// 返回的退出码是容器内命令的退出码。
func (r *Runner) ExecCommand(
	ctx context.Context,
	dockerID string,
	command []string,
	timeout time.Duration,
) (CommandResult, error) {
	if strings.TrimSpace(dockerID) == "" {
		return CommandResult{}, newError(CodeInvalidArgument, "docker exec", "容器 ID 不能为空", nil)
	}
	if len(command) == 0 {
		return CommandResult{}, newError(CodeInvalidArgument, "docker exec", "容器内命令不能为空", nil)
	}
	args := append([]string{"--workdir", domain.DefaultContainerWorkDir, dockerID}, command...)
	remote, err := dockerCommand("exec", args...)
	if err != nil {
		return CommandResult{}, err
	}
	if timeout <= 0 {
		timeout = DefaultMutateTimeout
	}
	return r.Run(ctx, "docker exec", remote, timeout)
}

// ContainerLogs 读取容器日志的末尾内容。
func (r *Runner) ContainerLogs(
	ctx context.Context,
	dockerID string,
	tailLines int,
) (string, error) {
	if strings.TrimSpace(dockerID) == "" {
		return "", newError(CodeInvalidArgument, "docker logs", "容器 ID 不能为空", nil)
	}
	if tailLines <= 0 {
		tailLines = 200
	}
	command, err := dockerCommand(
		"logs",
		"--tail", fmt.Sprintf("%d", tailLines),
		dockerID,
	)
	if err != nil {
		return "", err
	}
	run, runErr := r.RunQuery(ctx, "docker logs", command)
	if runErr != nil {
		return "", runErr
	}
	combined := run.Stdout
	if strings.TrimSpace(run.Stderr) != "" {
		combined = run.Stdout + "\n" + run.Stderr
	}
	if run.Truncated {
		combined += "\n（日志超过上限，已截断；如需完整日志请使用远程 docker logs 查看）"
	}
	return combined, nil
}

// EnsureVolume 创建或复用带 AgentFlow 标签的缓存卷。
//
// 缓存卷使用 Named Volume 而不是宿主机 bind mount；卷名由缓存类型、共享范围
// 与 ABI 共同决定，避免不同项目或不同工具链版本误共享缓存。
func (r *Runner) EnsureVolume(
	ctx context.Context,
	volumeName string,
	labels map[string]string,
) error {
	if strings.TrimSpace(volumeName) == "" {
		return newError(CodeInvalidArgument, "docker volume create", "缓存卷名不能为空", nil)
	}
	args := []string{"--label", OwnershipLabelKey + "=true"}
	for _, pair := range sortedLabelPairs(labels) {
		args = append(args, "--label", pair)
	}
	args = append(args, volumeName)
	volumeArgs := append([]string{"create"}, args...)
	command, err := dockerCommand("volume", volumeArgs...)
	if err != nil {
		return err
	}
	run, runErr := r.RunMutate(ctx, "docker volume create", command)
	if runErr != nil {
		return runErr
	}
	_ = run
	return nil
}

// CacheVolumeName 生成缓存卷名称。
//
// 卷名包含节点、环境、缓存类型、共享范围与 ABI，使不同来源的缓存不会因为
// 名称相似而被默认共享。
func CacheVolumeName(
	hostID domain.ID,
	mount domain.CacheMount,
	scopeKey string,
) (string, error) {
	if err := mount.Validate(); err != nil {
		return "", err
	}
	if hostID.IsEmpty() {
		return "", newError(CodeInvalidArgument, "cache volume", "节点标识不能为空", nil)
	}
	parts := []string{"agentflow", shortID(hostID.String()), mount.Type, mount.Scope.String()}
	if mount.ABI != "" {
		parts = append(parts, sanitizeVolumeSegment(mount.ABI))
	}
	if scopeKey != "" {
		parts = append(parts, sanitizeVolumeSegment(scopeKey))
	}
	name := strings.Join(parts, "-")
	if len(name) > 128 {
		name = name[:128]
	}
	return name, nil
}

// shortID 返回标识的前 12 个字符，用于卷名与容器名。
func shortID(id string) string {
	trimmed := strings.TrimSpace(id)
	if len(trimmed) > 12 {
		return trimmed[:12]
	}
	return trimmed
}

// sanitizeVolumeSegment 把值转换为只含小写字母与数字的卷名片段。
func sanitizeVolumeSegment(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(value) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			builder.WriteRune(char)
		default:
			builder.WriteRune('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}

// ContainerNameFor 生成容器名称。
//
// 名称由固定前缀与随机标识组成，任务 ID 等用户输入只能作为经过校验的标签，
// 不会被直接拼接进名称或 shell 命令。
func ContainerNameFor(containerID domain.ID) (string, error) {
	if err := containerID.Validate(); err != nil {
		return "", newError(CodeInvalidArgument, "container name", err.Error(), err)
	}
	name := "agentflow-" + shortID(containerID.String())
	if err := domain.ValidateContainerName(name); err != nil {
		return "", newError(CodeInvalidArgument, "container name", err.Error(), err)
	}
	return name, nil
}

// CheckRunContract 检查镜像是否满足常驻运行契约。
//
// 契约是"镜像可以执行 sh -c <DefaultRunContractCommand>"：基础环境与语言环境
// 都必须满足。契约不满足时返回 CodeRunContractUnsupported，调用方不得创建容器。
func (r *Runner) CheckRunContract(ctx context.Context, imageRef string) error {
	command, err := dockerCommand(
		"run", "--rm",
		"--entrypoint", "",
		imageRef,
		"sh", "-c", "command -v sleep",
	)
	if err != nil {
		return err
	}
	run, runErr := r.Run(ctx, "docker run", command, RunContractCheckTimeout)
	if runErr != nil {
		if run.Uncertain() {
			return newError(
				CodeStateUncertain,
				"docker run",
				"运行契约预检结果不确定",
				runErr,
			)
		}
		return newError(
			CodeRunContractUnsupported,
			"docker run",
			"镜像不满足常驻运行契约: "+summarizeOutput(run.Stderr+run.Stdout),
			runErr,
		)
	}
	return nil
}

// ListAgentFlowContainers 列出带 AgentFlow 管理标签的容器。
//
// 过滤条件包含管理标志与节点 ID，不会匹配机器上的其他容器；已脱离管理的容器
// 仍会出现在结果中，由调用方根据本地记录决定是否忽略。
func (r *Runner) ListAgentFlowContainers(
	ctx context.Context,
	hostID domain.ID,
) ([]string, error) {
	command, err := dockerCommand(
		"ps", "--all",
		"--filter", "label="+OwnershipLabelKey+"=true",
		"--filter", "label="+HostLabelKey+"="+hostID.String(),
		"--format", "{{.ID}}",
	)
	if err != nil {
		return nil, err
	}
	run, runErr := r.RunQuery(ctx, "docker ps", command)
	if runErr != nil {
		return nil, runErr
	}
	lines := strings.Split(strings.TrimSpace(run.Stdout), "\n")
	ids := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		ids = append(ids, trimmed)
	}
	return ids, nil
}
