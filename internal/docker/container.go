package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
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

// CacheVolumeSpec 是一个可写缓存卷的挂载定义。
type CacheVolumeSpec struct {
	// VolumeName 是 Docker Named Volume 名称。
	VolumeName string
	// ContainerPath 是容器内挂载路径。
	ContainerPath string
}

// ReadOnlyVolumeSpec 是一个只读命名卷的挂载定义。
//
// 它只用于规划容器：项目代码快照以 Docker Named Volume 承载并以只读方式挂载，
// 不使用宿主机 bind mount，也不复用普通任务容器的可写缓存卷。
type ReadOnlyVolumeSpec struct {
	// VolumeName 是 Docker Named Volume 名称。
	VolumeName string
	// ContainerPath 是容器内挂载路径，必须是 /workspace 之下的安全绝对路径。
	ContainerPath string
}

// TmpfsSpec 是一个容器内临时可写目录的定义。
//
// 只读 rootfs 的容器只能用临时目录提供写入能力：目录内容随容器消失，
// 不落在任何宿主机目录，也不影响只读挂载的项目代码。
type TmpfsSpec struct {
	// Target 是容器内挂载路径。
	Target string
}

// VolumeMountSpec 是一个命名卷挂载定义。
//
// 它只用于规划快照的一次性容器：准备容器以可写方式挂载快照卷，核验容器以只读
// 方式挂载同一个卷。与缓存卷不同，这里的卷属于单个规划会话，不能跨会话复用。
type VolumeMountSpec struct {
	// VolumeName 是 Docker Named Volume 名称。
	VolumeName string
	// ContainerPath 是容器内挂载路径，必须是 /workspace 之下的安全绝对路径。
	ContainerPath string
	// ReadOnly 表示以只读方式挂载。
	ReadOnly bool
}

// TemporaryContainerSpec 描述一个后台运行的一次性容器（docker run --detach）。
//
// 该容器只用于把项目代码写入快照卷：它有确定的名称与 AgentFlow 标签，但不会被
// 登记为受管理的执行容器，也不能被当作正式规划容器。
type TemporaryContainerSpec struct {
	// Name 是容器名称，由调用方生成，便于失败后按名称核验现场。
	Name string
	// ImageRef 是使用的镜像引用。
	ImageRef string
	// WorkDir 是容器内工作目录。
	WorkDir string
	// Mounts 是命名卷挂载。
	Mounts []VolumeMountSpec
	// Labels 是可追溯标签，所有权标签由本包自动追加。
	Labels map[string]string
	// Command 是容器启动命令，空值使用常驻命令。
	Command []string
}

// OneShotContainerSpec 描述一个前台运行的一次性容器（docker run --rm）。
//
// 它只用于只读核验：容器随命令结束自动删除，不留在远程机器上，也不写入任何卷。
type OneShotContainerSpec struct {
	// ImageRef 是使用的镜像引用。
	ImageRef string
	// WorkDir 是容器内工作目录。
	WorkDir string
	// Mounts 是命名卷挂载；核验路径只允许只读挂载。
	Mounts []VolumeMountSpec
	// Labels 是可追溯标签，所有权标签由本包自动追加。
	Labels map[string]string
	// Command 是容器内命令，空值使用常驻命令。
	Command []string
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
	// Caches 是允许挂载的可写缓存卷。
	Caches []CacheVolumeSpec
	// ReadOnlyRootFS 表示容器 root 文件系统以只读方式挂载（docker create --read-only）。
	//
	// 它只用于规划容器：普通任务容器保持可写 rootfs，因此该字段默认为 false。
	ReadOnlyRootFS bool
	// CodeVolumes 是只读挂载的项目代码卷，只有 ReadOnlyRootFS 为 true 时才允许声明。
	CodeVolumes []ReadOnlyVolumeSpec
	// Tmpfs 是容器内临时可写目录，只有 ReadOnlyRootFS 为 true 时才允许声明。
	Tmpfs []TmpfsSpec
	// PlanningSessionID 是规划会话标识，只由 PlanningContainerSpec 设置。
	//
	// 它写入 PlanningSessionLabelKey 标签，使规划容器可以被核验为某个会话的
	// 资源；普通任务容器不会带该字段。
	PlanningSessionID string
}

// CreateContainer 在远程机器上创建容器并核验实际容器 ID。
//
// 安全边界在这里统一实施，不依赖用户 Dockerfile：
//   - 不使用 --privileged；
//   - 不使用 --pid=host 或 --network=host；
//   - 不挂载宿主机 Docker Socket、用户 Home 或远程项目配置目录；
//   - 普通任务容器只挂载显式声明的缓存卷，项目代码只存在于容器内部可写层；
//   - 只有明确声明只读容器模式时才追加 --read-only、只读代码卷与 --tmpfs。
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
	if err := validateReadOnlyContainer(spec); err != nil {
		return nil, err
	}
	args := []string{
		"--name", name,
		// docker create 只创建容器，不启动工作负载，因此不接受 --detach：
		// 该参数只属于 docker run，传入会被 CLI 直接拒绝（unknown flag）。
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
	if sessionID := strings.TrimSpace(spec.PlanningSessionID); sessionID != "" {
		if err := domain.ID(sessionID).Validate(); err != nil {
			return nil, newError(CodeInvalidArgument, "docker create", "规划会话标签非法: "+err.Error(), nil)
		}
		args = append(args, "--label", PlanningSessionLabelKey+"="+sessionID)
	}
	if spec.ReadOnlyRootFS {
		// --read-only 只在明确选择只读容器模式时出现：普通任务容器保持可写 rootfs。
		args = append(args, "--read-only")
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
	for _, volume := range spec.CodeVolumes {
		args = append(args, "--mount", readOnlyVolumeMountOption(volume))
	}
	for _, tmpfs := range spec.Tmpfs {
		args = append(args, "--tmpfs", strings.TrimSpace(tmpfs.Target))
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
	return volumeMountOption(cache.VolumeName, cache.ContainerPath, false)
}

// volumeMountOption 构造命名卷挂载参数（volume 类型，可选 readonly）。
func volumeMountOption(volumeName, containerPath string, readOnly bool) string {
	option := fmt.Sprintf("type=volume,source=%s,target=%s", volumeName, containerPath)
	if readOnly {
		option += ",readonly"
	}
	return option
}

// readOnlyVolumeMountOption 构造只读命名卷挂载参数。
//
// 使用 volume 类型加 readonly，使项目代码快照无法被容器内进程改写；
// 这里不接受宿主机 bind mount，也不允许省略 readonly。
func readOnlyVolumeMountOption(volume ReadOnlyVolumeSpec) string {
	return volumeMountOption(
		strings.TrimSpace(volume.VolumeName),
		strings.TrimSpace(volume.ContainerPath),
		true,
	)
}

// dockerVolumeNamePattern 约束命名卷名称的书写形式。
//
// Docker 命名卷名称只允许字母、数字、下划线、点与连字符，且必须字母或数字开头。
var dockerVolumeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,254}$`)

// validateReadOnlyContainer 校验只读容器的挂载参数。
//
// 只读容器是规划容器专用模式：rootfs 只读、项目代码卷只读、可写目录只能是显式声明的
// 临时目录。可写缓存卷、与只读代码卷重叠的临时目录、非只读模式下的只读挂载都会拒绝，
// 避免出现"标称只读但仍能改写项目代码"的组合。
func validateReadOnlyContainer(spec CreateContainerSpec) error {
	if !spec.ReadOnlyRootFS {
		if len(spec.CodeVolumes) > 0 || len(spec.Tmpfs) > 0 {
			return newError(
				CodeInvalidArgument,
				"docker create",
				"只读代码卷与临时目录只能用于只读 rootfs 容器",
				nil,
			)
		}
		return nil
	}
	if len(spec.Caches) > 0 {
		return newError(CodeInvalidArgument, "docker create", "只读容器不能挂载可写缓存卷", nil)
	}
	if len(spec.CodeVolumes) == 0 {
		return newError(CodeInvalidArgument, "docker create", "只读容器必须声明只读项目代码卷", nil)
	}
	if len(spec.Tmpfs) == 0 {
		return newError(CodeInvalidArgument, "docker create", "只读容器必须声明可写的临时目录", nil)
	}
	codeTargets := make([]string, 0, len(spec.CodeVolumes))
	for _, volume := range spec.CodeVolumes {
		name := strings.TrimSpace(volume.VolumeName)
		if !dockerVolumeNamePattern.MatchString(name) {
			return newError(
				CodeInvalidArgument,
				"docker create",
				fmt.Sprintf("只读代码卷名 %q 不合法", volume.VolumeName),
				nil,
			)
		}
		target := strings.TrimSpace(volume.ContainerPath)
		if err := domain.ValidateContainerWorkDir(target); err != nil {
			return newError(CodeInvalidArgument, "docker create", "只读代码卷挂载路径非法: "+err.Error(), nil)
		}
		cleaned := path.Clean(target)
		for _, existing := range codeTargets {
			if existing == cleaned {
				return newError(
					CodeInvalidArgument,
					"docker create",
					"只读代码卷挂载路径重复: "+cleaned,
					nil,
				)
			}
		}
		codeTargets = append(codeTargets, cleaned)
	}
	seen := make(map[string]struct{}, len(spec.Tmpfs))
	for _, tmpfs := range spec.Tmpfs {
		target, err := validateTmpfsTarget(tmpfs.Target)
		if err != nil {
			return err
		}
		if _, exists := seen[target]; exists {
			return newError(CodeInvalidArgument, "docker create", "临时目录重复声明: "+target, nil)
		}
		seen[target] = struct{}{}
		for _, codeTarget := range codeTargets {
			if target == codeTarget ||
				isNestedContainerPath(target, codeTarget) ||
				isNestedContainerPath(codeTarget, target) {
				return newError(
					CodeInvalidArgument,
					"docker create",
					"临时目录 "+target+" 不能与只读代码卷挂载路径 "+codeTarget+" 重叠",
					nil,
				)
			}
		}
	}
	return nil
}

// validateTmpfsTarget 校验临时可写目录的容器内路径。
func validateTmpfsTarget(value string) (string, error) {
	target := strings.TrimSpace(value)
	if target == "" {
		return "", newError(CodeInvalidArgument, "docker create", "临时目录路径不能为空", nil)
	}
	if strings.ContainsRune(target, '\\') || !strings.HasPrefix(target, "/") {
		return "", newError(
			CodeInvalidArgument,
			"docker create",
			"临时目录必须是使用 POSIX 分隔符的绝对路径: "+target,
			nil,
		)
	}
	for _, char := range target {
		if char == 0 || char == '\n' || char == '\r' || char == '\t' {
			return "", newError(CodeInvalidArgument, "docker create", "临时目录不能包含控制字符: "+target, nil)
		}
	}
	if path.Clean(target) != target {
		return "", newError(CodeInvalidArgument, "docker create", "临时目录必须已经规范化: "+target, nil)
	}
	if domain.IsReservedContainerPath(target) {
		return "", newError(CodeInvalidArgument, "docker create", "临时目录不能使用保留路径: "+target, nil)
	}
	return target, nil
}

// isNestedContainerPath 判断 target 是否位于 base 之内（不含 base 本身）。
func isNestedContainerPath(target, base string) bool {
	return strings.HasPrefix(path.Clean(target), path.Clean(base)+"/")
}

// StartTemporaryContainer 后台启动一个一次性容器并返回 Docker 容器 ID。
//
// 它只用于快照准备：容器有确定名称与 AgentFlow 标签，但不登记为受管理的执行容器。
// 启动结果不确定时返回 CodeStateUncertain，调用方必须先按名称核验现场。
func (r *Runner) StartTemporaryContainer(
	ctx context.Context,
	spec TemporaryContainerSpec,
) (string, error) {
	args, err := temporaryContainerArgs(spec)
	if err != nil {
		return "", err
	}
	command, err := dockerCommand("run", args...)
	if err != nil {
		return "", err
	}
	run, runErr := r.RunMutate(ctx, "docker run", command)
	if runErr != nil {
		if run.Uncertain() {
			return "", newError(
				CodeStateUncertain,
				"docker run",
				"启动结果不确定，请先按名称核验容器是否存在: "+spec.Name,
				runErr,
			)
		}
		return "", runErr
	}
	dockerID := strings.TrimSpace(run.Stdout)
	if dockerID == "" {
		return "", newError(
			CodeStateUncertain,
			"docker run",
			"启动命令未返回容器 ID，请先按名称核验容器是否存在: "+spec.Name,
			nil,
		)
	}
	return dockerID, nil
}

// RunOneShotContainer 在前台运行一个一次性容器并返回命令结果。
//
// 容器使用 --rm：命令结束后自动删除，不会留下未受管理的常驻容器。非零退出码
// 通过错误返回，但调用方仍可读取 CommandResult 判断真实退出状态。
func (r *Runner) RunOneShotContainer(
	ctx context.Context,
	spec OneShotContainerSpec,
	timeout time.Duration,
) (CommandResult, error) {
	args, err := oneShotContainerArgs(spec)
	if err != nil {
		return CommandResult{}, err
	}
	command, err := dockerCommand("run", args...)
	if err != nil {
		return CommandResult{}, err
	}
	if timeout <= 0 {
		timeout = r.queryTimeout
	}
	return r.Run(ctx, "docker run", command, timeout)
}

// temporaryContainerArgs 构造一次性后台容器的 docker run 参数。
func temporaryContainerArgs(spec TemporaryContainerSpec) ([]string, error) {
	args, err := runContainerArgs(
		spec.Name,
		spec.ImageRef,
		spec.WorkDir,
		spec.Mounts,
		spec.Labels,
		spec.Command,
	)
	if err != nil {
		return nil, err
	}
	return append([]string{"--detach"}, args...), nil
}

// oneShotContainerArgs 构造一次性前台容器的 docker run 参数。
func oneShotContainerArgs(spec OneShotContainerSpec) ([]string, error) {
	args, err := runContainerArgs(
		"",
		spec.ImageRef,
		spec.WorkDir,
		spec.Mounts,
		spec.Labels,
		spec.Command,
	)
	if err != nil {
		return nil, err
	}
	return append([]string{"--rm"}, args...), nil
}

// runContainerArgs 构造 docker run 的公共参数。
//
// 参数顺序固定；名称、镜像引用、工作目录与挂载路径全部经过校验，所有值都由
// dockerCommand 统一转义。
func runContainerArgs(
	name, imageRef, workDir string,
	mounts []VolumeMountSpec,
	labels map[string]string,
	command []string,
) ([]string, error) {
	image := strings.TrimSpace(imageRef)
	if err := domain.ValidateImageRef(image); err != nil {
		return nil, newError(CodeInvalidArgument, "docker run", err.Error(), err)
	}
	target := strings.TrimSpace(workDir)
	if target == "" {
		target = domain.DefaultContainerWorkDir
	}
	if err := domain.ValidateContainerWorkDir(target); err != nil {
		return nil, newError(CodeInvalidArgument, "docker run", err.Error(), err)
	}
	args := make([]string, 0, 12+2*len(mounts)+2*len(labels))
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		if err := domain.ValidateContainerName(trimmed); err != nil {
			return nil, newError(CodeInvalidArgument, "docker run", err.Error(), err)
		}
		args = append(args, "--name", trimmed)
	}
	args = append(args, "--workdir", target)
	args = append(args, "--label", OwnershipLabelKey+"=true")
	for _, pair := range sortedLabelPairs(labels) {
		args = append(args, "--label", pair)
	}
	for _, mount := range mounts {
		option, err := containerVolumeMountOption(mount)
		if err != nil {
			return nil, err
		}
		args = append(args, "--mount", option)
	}
	args = append(args, image)
	if len(command) == 0 {
		command = []string{"sh", "-c", DefaultRunContractCommand}
	}
	args = append(args, command...)
	return args, nil
}

// containerVolumeMountOption 校验并构造命名卷挂载参数。
func containerVolumeMountOption(mount VolumeMountSpec) (string, error) {
	name := strings.TrimSpace(mount.VolumeName)
	if !dockerVolumeNamePattern.MatchString(name) {
		return "", newError(
			CodeInvalidArgument,
			"docker run",
			fmt.Sprintf("命名卷名 %q 不合法", mount.VolumeName),
			nil,
		)
	}
	target := strings.TrimSpace(mount.ContainerPath)
	if err := domain.ValidateContainerWorkDir(target); err != nil {
		return "", newError(CodeInvalidArgument, "docker run", "命名卷挂载路径非法: "+err.Error(), nil)
	}
	return volumeMountOption(name, target, mount.ReadOnly), nil
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
// 返回的退出码是容器内命令的退出码。工作目录固定为任务项目目录。
func (r *Runner) ExecCommand(
	ctx context.Context,
	dockerID string,
	command []string,
	timeout time.Duration,
) (CommandResult, error) {
	return r.ExecCommandIn(ctx, dockerID, command, ExecOptions{Timeout: timeout})
}

// ExecOptions 约束一次容器内命令执行。
type ExecOptions struct {
	// WorkDir 是容器内执行目录，空值使用任务项目目录。
	//
	// 它必须是 /workspace 之下的安全绝对路径：调用方不能指定任意宿主机目录，
	// 也不能借此读取容器内的敏感路径。
	WorkDir string
	// Timeout 是执行超时，零值使用 DefaultMutateTimeout。
	Timeout time.Duration
	// Env 是显式传入容器内进程的环境变量，形如 KEY=VALUE。
	//
	// 只接受调用方显式声明的变量：执行层不会自动继承远程宿主机或服务进程的环境，
	// 也不允许借此读取容器内的敏感变量。键名由调用方负责校验。
	Env []string
	// MaxOutputBytes 是标准输出与标准错误各自保留的字节上限，
	// 零值使用 sshclient.DefaultMaxOutputBytes。
	MaxOutputBytes int
}

// ExecCommandIn 在运行中的容器内按给定选项执行一条非交互命令。
//
// 工作目录与输出上限都必须显式经过校验：验证引擎需要在任务工作区内执行检查命令，
// 同时避免异常输出耗尽内存或磁盘。
func (r *Runner) ExecCommandIn(
	ctx context.Context,
	dockerID string,
	command []string,
	options ExecOptions,
) (CommandResult, error) {
	if strings.TrimSpace(dockerID) == "" {
		return CommandResult{}, newError(CodeInvalidArgument, "docker exec", "容器 ID 不能为空", nil)
	}
	if len(command) == 0 {
		return CommandResult{}, newError(CodeInvalidArgument, "docker exec", "容器内命令不能为空", nil)
	}
	workDir := strings.TrimSpace(options.WorkDir)
	if workDir == "" {
		workDir = domain.DefaultContainerWorkDir
	}
	if err := domain.ValidateContainerWorkDir(workDir); err != nil {
		return CommandResult{}, newError(
			CodeInvalidArgument,
			"docker exec",
			"容器内工作目录非法: "+err.Error(),
			nil,
		)
	}
	envArgs, err := execEnvArgs(options.Env)
	if err != nil {
		return CommandResult{}, err
	}
	remote, err := execCommandStringIn(workDir, dockerID, command, envArgs...)
	if err != nil {
		return CommandResult{}, err
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultMutateTimeout
	}
	if options.MaxOutputBytes <= 0 {
		return r.Run(ctx, "docker exec", remote, timeout)
	}
	// 只有显式给出上限时才覆盖默认值：其余调用路径保持原有行为。
	var result sshclient.CommandResult
	var runErr error
	result, runErr = r.runWithOutputLimit(ctx, remote, timeout, options.MaxOutputBytes)
	converted := CommandResult{
		Command:   remote,
		ExitCode:  result.ExitCode,
		Stdout:    result.Stdout,
		Stderr:    result.Stderr,
		Truncated: result.StdoutTruncated || result.StderrTruncated,
		Duration:  result.Duration,
		Status:    result.Status,
		Stdin:     result.Stdin,
		Certain:   result.TerminationConfirmed,
		Started:   result.Started,
	}
	if runErr != nil {
		code := CodeSSHUnavailable
		if result.TimedOut() || result.Cancelled() {
			code = CodeStateUncertain
		}
		return converted, newError(code, "docker exec", "远程命令未能确认结束", runErr)
	}
	if converted.Succeeded() {
		return converted, nil
	}
	return converted, converted.commandError("docker exec")
}

// ExecCommandWithInput 在运行中的容器内执行命令并把 input 写入其标准输入。
//
// docker exec 只有在指定 --interactive 时才转发标准输入，因此这里显式启用该参数。
// 写入结果通过 CommandResult.Stdin 报告：写入失败不会把命令本身的退出码改成失败，
// 调用方需要同时检查 Stdin.Status。
func (r *Runner) ExecCommandWithInput(
	ctx context.Context,
	dockerID string,
	command []string,
	input io.Reader,
	timeout time.Duration,
) (CommandResult, error) {
	if strings.TrimSpace(dockerID) == "" {
		return CommandResult{}, newError(CodeInvalidArgument, "docker exec", "容器 ID 不能为空", nil)
	}
	if len(command) == 0 {
		return CommandResult{}, newError(CodeInvalidArgument, "docker exec", "容器内命令不能为空", nil)
	}
	if input == nil {
		return r.ExecCommand(ctx, dockerID, command, timeout)
	}
	remote, err := execCommandString(dockerID, command, "--interactive")
	if err != nil {
		return CommandResult{}, err
	}
	if timeout <= 0 {
		timeout = DefaultMutateTimeout
	}
	return r.RunWithInput(ctx, "docker exec", remote, input, timeout)
}

// ExecCommandInWithInput 在容器内按给定选项执行命令并写入标准输入。
//
// 与 ExecCommandIn 的区别只在于标准输入：工作目录、环境变量、超时与输出上限
// 使用同一套校验与构造逻辑，避免两条路径产生不一致的安全边界。
func (r *Runner) ExecCommandInWithInput(
	ctx context.Context,
	dockerID string,
	command []string,
	input io.Reader,
	options ExecOptions,
) (CommandResult, error) {
	if strings.TrimSpace(dockerID) == "" {
		return CommandResult{}, newError(CodeInvalidArgument, "docker exec", "容器 ID 不能为空", nil)
	}
	if len(command) == 0 {
		return CommandResult{}, newError(CodeInvalidArgument, "docker exec", "容器内命令不能为空", nil)
	}
	if input == nil {
		return r.ExecCommandIn(ctx, dockerID, command, options)
	}
	workDir := strings.TrimSpace(options.WorkDir)
	if workDir == "" {
		workDir = domain.DefaultContainerWorkDir
	}
	if err := domain.ValidateContainerWorkDir(workDir); err != nil {
		return CommandResult{}, newError(
			CodeInvalidArgument,
			"docker exec",
			"容器内工作目录非法: "+err.Error(),
			nil,
		)
	}
	envArgs, err := execEnvArgs(options.Env)
	if err != nil {
		return CommandResult{}, err
	}
	extra := append(envArgs, "--interactive")
	remote, err := execCommandStringIn(workDir, dockerID, command, extra...)
	if err != nil {
		return CommandResult{}, err
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultMutateTimeout
	}
	return r.RunWithInput(ctx, "docker exec", remote, input, timeout)
}

// execCommandString 构造 docker exec 的远程命令字符串。
//
// 额外参数只能来自本包的固定字面量，容器内命令的每个参数都会被单独转义。
func execCommandString(
	dockerID string,
	command []string,
	extra ...string,
) (string, error) {
	return execCommandStringIn(domain.DefaultContainerWorkDir, dockerID, command, extra...)
}

// execCommandStringIn 构造带显式工作目录的 docker exec 远程命令字符串。
//
// workDir 必须已通过 domain.ValidateContainerWorkDir 校验；命令的每个参数都会被
// 单独转义，用户输入不会进入 shell。
func execCommandStringIn(
	workDir, dockerID string,
	command []string,
	extra ...string,
) (string, error) {
	args := []string{"--workdir", workDir}
	args = append(args, extra...)
	args = append(args, dockerID)
	args = append(args, command...)
	return dockerCommand("exec", args...)
}

// execEnvArgs 把显式环境变量转换为 docker exec 的 --env 参数。
//
// 键名与取值都必须已经由调用方校验：这里只做形式检查，拒绝空键、重复键与
// 非 KEY=VALUE 形式，避免把任意字符串当作环境变量注入容器进程。
func execEnvArgs(env []string) ([]string, error) {
	if len(env) == 0 {
		return nil, nil
	}
	args := make([]string, 0, len(env)*2)
	seen := make(map[string]struct{}, len(env))
	for _, entry := range env {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		key, _, found := strings.Cut(trimmed, "=")
		if !found || key == "" {
			return nil, newError(
				CodeInvalidArgument,
				"docker exec",
				"环境变量必须是 KEY=VALUE 形式",
				nil,
			)
		}
		if _, exists := seen[key]; exists {
			return nil, newError(
				CodeInvalidArgument,
				"docker exec",
				"环境变量重复声明: "+key,
				nil,
			)
		}
		seen[key] = struct{}{}
		args = append(args, "--env", trimmed)
	}
	if len(args) == 0 {
		return nil, nil
	}
	return args, nil
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

// VolumeInspect 是 docker volume inspect 的关键字段。
type VolumeInspect struct {
	// Name 是卷名。
	Name string
	// Labels 是卷标签。
	Labels map[string]string
}

// volumeInspectEntry 是 docker volume inspect JSON 数组的元素。
type volumeInspectEntry struct {
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

// InspectVolume 检查命名卷是否存在并返回标签。
//
// 卷不存在时返回 CodeVolumeNotFound：调用方据此区分"从未创建"与"读取失败"。
func (r *Runner) InspectVolume(ctx context.Context, volumeName string) (VolumeInspect, error) {
	name := strings.TrimSpace(volumeName)
	if !dockerVolumeNamePattern.MatchString(name) {
		return VolumeInspect{}, newError(
			CodeInvalidArgument,
			"docker volume inspect",
			fmt.Sprintf("卷名 %q 不合法", volumeName),
			nil,
		)
	}
	command, err := dockerCommand("volume", "inspect", name)
	if err != nil {
		return VolumeInspect{}, err
	}
	run, runErr := r.RunQuery(ctx, "docker volume inspect", command)
	if runErr != nil {
		return VolumeInspect{}, runErr
	}
	return ParseVolumeInspect(run.Stdout)
}

// ParseVolumeInspect 解析 docker volume inspect 的 JSON 输出。
func ParseVolumeInspect(output string) (VolumeInspect, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return VolumeInspect{}, newError(
			CodeStateUncertain,
			"docker volume inspect",
			"卷检查输出为空",
			nil,
		)
	}
	var entries []volumeInspectEntry
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		return VolumeInspect{}, newError(
			CodeStateUncertain,
			"docker volume inspect",
			"解析卷检查结果失败",
			err,
		)
	}
	if len(entries) == 0 {
		return VolumeInspect{}, newError(
			CodeVolumeNotFound,
			"docker volume inspect",
			"卷检查结果为空",
			nil,
		)
	}
	entry := entries[0]
	return VolumeInspect{Name: entry.Name, Labels: entry.Labels}, nil
}

// RemoveVolume 删除命名卷。
//
// 卷已不存在时返回 existed=false 与 nil，使清理操作具备幂等性；卷仍被容器使用时
// Docker 会拒绝删除，错误原样返回，调用方必须保留资源定位信息。
func (r *Runner) RemoveVolume(ctx context.Context, volumeName string) (bool, error) {
	name := strings.TrimSpace(volumeName)
	if !dockerVolumeNamePattern.MatchString(name) {
		return false, newError(
			CodeInvalidArgument,
			"docker volume rm",
			fmt.Sprintf("卷名 %q 不合法", volumeName),
			nil,
		)
	}
	command, err := dockerCommand("volume", "rm", name)
	if err != nil {
		return false, err
	}
	run, runErr := r.RunMutate(ctx, "docker volume rm", command)
	if runErr != nil {
		if run.Uncertain() {
			return false, newError(
				CodeStateUncertain,
				"docker volume rm",
				"删除结果不确定，请先核验卷是否仍存在: "+name,
				runErr,
			)
		}
		if ErrorCodeOf(runErr) == CodeVolumeNotFound {
			return false, nil
		}
		return false, runErr
	}
	return true, nil
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

// ContainerListEntry 是容器清单中的一项。
type ContainerListEntry struct {
	// ID 是容器 ID。
	ID string
	// Name 是容器名称，不含前导斜杠。
	Name string
}

// ListContainersOptions 描述一次容器清单查询。
type ListContainersOptions struct {
	// All 为 true 时包含已停止容器，等价于 docker ps --all。
	All bool
	// NameFilters 是 name 过滤条件；Docker 按名称子串匹配，调用方需自行核对全名。
	NameFilters []string
}

// ListContainers 列出远程机器上容器的 ID 与名称。
//
// 该方法是只读查询，只返回身份信息；容器归属仍必须由 InspectContainer 与
// VerifyOwnership 核验，删除前禁止仅凭本结果判定所有权。
func (r *Runner) ListContainers(
	ctx context.Context,
	options ListContainersOptions,
) ([]ContainerListEntry, error) {
	args := make([]string, 0, 2+2*len(options.NameFilters))
	if options.All {
		args = append(args, "--all")
	}
	for _, name := range options.NameFilters {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		args = append(args, "--filter", "name="+trimmed)
	}
	args = append(args, "--format", "{{.ID}}\t{{.Names}}")
	command, err := dockerCommand("ps", args...)
	if err != nil {
		return nil, err
	}
	run, runErr := r.RunQuery(ctx, "docker ps", command)
	if runErr != nil {
		return nil, runErr
	}
	return parseContainerList(run.Stdout), nil
}

// parseContainerList 解析 docker ps 的 {{.ID}}\t{{.Names}} 输出。
func parseContainerList(output string) []ContainerListEntry {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	entries := make([]ContainerListEntry, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		fields := strings.SplitN(trimmed, "\t", 2)
		entry := ContainerListEntry{ID: strings.TrimSpace(fields[0])}
		if len(fields) == 2 {
			entry.Name = strings.TrimPrefix(strings.TrimSpace(fields[1]), "/")
		}
		if entry.ID == "" {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

// CopyFromContainer 用 docker cp 把容器内文件复制到远程宿主机路径。
//
// 该方法统一走本包的 Docker 命令构造入口，使 docker cp 与其余命令使用相同的 API 基准，
// 业务层不再自行拼接 Docker 命令行。
func (r *Runner) CopyFromContainer(
	ctx context.Context,
	dockerID, containerPath, hostPath string,
	timeout time.Duration,
) (CommandResult, error) {
	if strings.TrimSpace(dockerID) == "" {
		return CommandResult{}, newError(CodeInvalidArgument, "docker cp", "容器 ID 不能为空", nil)
	}
	if !strings.HasPrefix(containerPath, "/") {
		return CommandResult{}, newError(
			CodeInvalidArgument,
			"docker cp",
			fmt.Sprintf("容器内路径 %q 必须是绝对路径", containerPath),
			nil,
		)
	}
	if !strings.HasPrefix(hostPath, "/") {
		return CommandResult{}, newError(
			CodeInvalidArgument,
			"docker cp",
			fmt.Sprintf("宿主机路径 %q 必须是绝对路径", hostPath),
			nil,
		)
	}
	command, err := dockerCommand("cp", dockerID+":"+containerPath, hostPath)
	if err != nil {
		return CommandResult{}, err
	}
	if timeout <= 0 {
		timeout = DefaultMutateTimeout
	}
	return r.Run(ctx, "docker cp", command, timeout)
}
