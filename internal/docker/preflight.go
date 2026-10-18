package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/pkg/sftp"
)

// 远程 Docker 预检的最低版本要求。
const (
	// MinDockerMajor 是支持的最低 Docker 主版本。
	MinDockerMajor = 20
	// MinDockerMinor 是主版本等于 MinDockerMajor 时的最低次版本。
	MinDockerMinor = 10
	// MinFreeDiskBytes 是构建与运行容器所需的最小剩余空间。
	MinFreeDiskBytes = 3 << 30
)

// Preflight 是一次远程 Docker 环境预检的结果。
//
// 预检只做只读检查，不创建、不删除任何 Docker 资源。
type Preflight struct {
	// SSHOK 表示 SSH 连接可用。
	SSHOK bool
	// CLIPresent 表示远程存在 docker 命令。
	CLIPresent bool
	// CLIVersion 是 docker CLI 版本，形如 24.0.7。
	CLIVersion string
	// EngineReachable 表示 Docker Engine 可访问。
	EngineReachable bool
	// EngineVersion 是 Docker Engine 版本。
	EngineVersion string
	// PermissionOK 表示当前 SSH 用户有使用 Docker 的权限。
	PermissionOK bool
	// WorkspaceRoot 是解析后的远程工作空间绝对路径。
	WorkspaceRoot string
	// WorkspaceWritable 表示工作空间可写。
	WorkspaceWritable bool
	// FreeDiskBytes 是工作空间所在文件系统的剩余字节数。
	FreeDiskBytes int64
	// ErrorCode 是预检失败的错误类别，成功时为 CodeOK。
	ErrorCode ErrorCode
	// Detail 是失败的可读说明。
	Detail string
}

// Ready 判断预检是否全部通过。
func (p Preflight) Ready() bool {
	return p.ErrorCode == CodeOK &&
		p.SSHOK &&
		p.CLIPresent &&
		p.EngineReachable &&
		p.PermissionOK &&
		p.WorkspaceWritable
}

// PreflightOptions 约束一次预检。
type PreflightOptions struct {
	// WorkspaceRoot 是已解析的远程工作空间绝对路径。
	WorkspaceRoot string
	// CreateWorkspace 表示按需创建工作空间目录。
	CreateWorkspace bool
}

// Preflight 检查远程 Docker CLI、Engine、权限、工作空间与存储空间。
//
// 检查顺序与错误分类一一对应：SSH 不可用、CLI 缺失、Engine 不可达、权限不足、
// 存储不足分别返回不同的错误类别。任何一步失败都不会尝试使用 sudo 或修改
// 用户组、Socket 权限等系统配置。
func (r *Runner) Preflight(
	ctx context.Context,
	options PreflightOptions,
) Preflight {
	result := Preflight{ErrorCode: CodeOK}
	if r == nil || r.client == nil {
		result.ErrorCode = CodeSSHUnavailable
		result.Detail = "SSH 连接不可用"
		return result
	}
	result.SSHOK = true
	result.WorkspaceRoot = strings.TrimSpace(options.WorkspaceRoot)
	if err := r.checkCLI(ctx, &result); err != nil {
		return result
	}
	if err := r.checkEngine(ctx, &result); err != nil {
		return result
	}
	if err := r.checkWorkspace(ctx, &result, options.CreateWorkspace); err != nil {
		return result
	}
	if err := r.checkDisk(ctx, &result); err != nil {
		return result
	}
	return result
}

// checkCLI 检查 docker CLI 是否存在及其版本。
func (r *Runner) checkCLI(ctx context.Context, result *Preflight) error {
	command, err := dockerCommand("version", "--format", "{{.Client.Version}}")
	if err != nil {
		result.ErrorCode = CodeInvalidArgument
		result.Detail = err.Error()
		return err
	}
	run, runErr := r.RunQuery(ctx, "docker version", command)
	if runErr != nil {
		result.ErrorCode = classifyPreflightCode(run, runErr)
		result.Detail = summarizeOutput(run.Stderr + "\n" + run.Stdout)
		if result.Detail == "" {
			result.Detail = runErr.Error()
		}
		return runErr
	}
	result.CLIPresent = true
	result.CLIVersion = strings.TrimSpace(run.Stdout)
	return nil
}

// checkEngine 检查 Docker Engine 是否可访问并取得其版本。
func (r *Runner) checkEngine(ctx context.Context, result *Preflight) error {
	command, err := dockerCommand("info", "--format", "{{.ServerVersion}}")
	if err != nil {
		result.ErrorCode = CodeInvalidArgument
		result.Detail = err.Error()
		return err
	}
	run, runErr := r.RunQuery(ctx, "docker info", command)
	if runErr != nil {
		result.ErrorCode = classifyPreflightCode(run, runErr)
		result.Detail = summarizeOutput(run.Stderr + "\n" + run.Stdout)
		if result.Detail == "" {
			result.Detail = runErr.Error()
		}
		return runErr
	}
	result.EngineReachable = true
	result.PermissionOK = true
	result.EngineVersion = strings.TrimSpace(run.Stdout)
	if err := CheckVersionSupport(result.CLIVersion); err != nil {
		result.ErrorCode = CodeVersionUnsupported
		result.Detail = err.Error()
		return err
	}
	return nil
}

// checkWorkspace 检查远程工作空间可写。
//
// 检查只使用固定 shell 命令，不依赖 SFTP：即使 SFTP 子系统不可用，预检仍能给出
// 关于工作空间的明确结论。
func (r *Runner) checkWorkspace(
	ctx context.Context,
	result *Preflight,
	create bool,
) error {
	if result.WorkspaceRoot == "" {
		result.ErrorCode = CodeInvalidArgument
		result.Detail = "远程工作空间路径为空"
		return fmt.Errorf("docker: 远程工作空间路径为空")
	}
	exists, err := r.runPathTest(ctx, "test -d", "-d", result.WorkspaceRoot)
	if err != nil {
		result.ErrorCode = CodeSSHUnavailable
		result.Detail = fmt.Sprintf("检查远程工作空间失败: %v", err)
		return err
	}
	if !exists {
		if !create {
			result.ErrorCode = CodeInvalidArgument
			result.Detail = "远程工作空间不存在: " + result.WorkspaceRoot
			return fmt.Errorf("docker: 远程工作空间不存在: %s", result.WorkspaceRoot)
		}
		command, cmdErr := sshclient.ShellCommand("mkdir", "-p", result.WorkspaceRoot)
		if cmdErr != nil {
			result.ErrorCode = CodeInvalidArgument
			result.Detail = cmdErr.Error()
			return cmdErr
		}
		run, runErr := r.RunQuery(ctx, "mkdir -p", command)
		if runErr != nil || run.ExitCode != 0 {
			result.ErrorCode = CodeSSHUnavailable
			result.Detail = "创建远程工作空间失败: " + summarizeOutput(run.Stderr)
			if runErr != nil {
				return runErr
			}
			return fmt.Errorf("docker: 创建远程工作空间失败: %s", result.WorkspaceRoot)
		}
	}
	writable, err := r.runPathTest(ctx, "test -w", "-w", result.WorkspaceRoot)
	if err != nil {
		result.ErrorCode = CodeSSHUnavailable
		result.Detail = fmt.Sprintf("检查工作空间写入权限失败: %v", err)
		return err
	}
	if !writable {
		result.ErrorCode = CodePermissionDenied
		result.Detail = "远程工作空间不可写: " + result.WorkspaceRoot
		return fmt.Errorf("docker: 远程工作空间不可写: %s", result.WorkspaceRoot)
	}
	result.WorkspaceWritable = true
	return nil
}

// runPathTest 执行一次 test 判断，返回条件是否成立。
//
// 退出码 0 表示条件成立，1 表示条件不成立，其余退出码视为执行失败。
func (r *Runner) runPathTest(
	ctx context.Context,
	op, flag, target string,
) (bool, error) {
	command, err := sshclient.ShellCommand("test", flag, target)
	if err != nil {
		return false, err
	}
	run, runErr := r.RunQuery(ctx, op, command)
	if runErr != nil {
		if !run.Certain {
			return false, runErr
		}
		// test 以 1 退出表示条件不成立，属于正常结果而不是执行失败。
		if run.ExitCode == 1 {
			return false, nil
		}
		return false, runErr
	}
	return run.ExitCode == 0, nil
}

// checkDisk 检查工作空间所在文件系统的剩余空间。
//
// 空间不足不会阻止只读查询，但会让构建与运行容器失败，因此作为预检的一部分。
func (r *Runner) checkDisk(ctx context.Context, result *Preflight) error {
	command, err := sshclient.ShellCommand("df", "-Pk", result.WorkspaceRoot)
	if err != nil {
		result.ErrorCode = CodeInvalidArgument
		result.Detail = err.Error()
		return err
	}
	run, runErr := r.RunQuery(ctx, "df", command)
	if runErr != nil {
		// 空间检查失败不阻断预检结论，但记录可读说明供诊断。
		result.Detail = "无法读取远程剩余空间: " + summarizeOutput(run.Stderr)
		return nil
	}
	result.FreeDiskBytes = ParseDfAvailableKilobytes(run.Stdout) * 1024
	if result.FreeDiskBytes > 0 && result.FreeDiskBytes < MinFreeDiskBytes {
		result.ErrorCode = CodeDiskInsufficient
		result.Detail = fmt.Sprintf(
			"远程工作空间剩余空间不足: 可用 %d 字节，至少需要 %d 字节",
			result.FreeDiskBytes,
			int64(MinFreeDiskBytes),
		)
		return fmt.Errorf("docker: 远程剩余空间不足")
	}
	return nil
}

// classifyPreflightCode 把预检命令失败归类为可诊断的类别。
func classifyPreflightCode(run CommandResult, runErr error) ErrorCode {
	if code := ErrorCodeOf(runErr); code != CodeOK && code != CodeCommandFailed {
		return code
	}
	if !run.Certain {
		return CodeStateUncertain
	}
	if run.ExitCode == 127 {
		return CodeCLIMissing
	}
	return ClassifyDockerMessage(run.Stderr + "\n" + run.Stdout)
}

// CheckVersionSupport 校验 Docker 版本是否满足最低要求。
func CheckVersionSupport(raw string) error {
	major, minor, ok := parseDockerVersion(raw)
	if !ok {
		return fmt.Errorf("docker: 无法解析 Docker 版本 %q", raw)
	}
	if major > MinDockerMajor {
		return nil
	}
	if major == MinDockerMajor && minor >= MinDockerMinor {
		return nil
	}
	return fmt.Errorf(
		"docker: Docker 版本 %d.%d 低于要求的 %d.%d",
		major,
		minor,
		MinDockerMajor,
		MinDockerMinor,
	)
}

// parseDockerVersion 解析 24.0.7 形式的版本号。
func parseDockerVersion(raw string) (int, int, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, 0, false
	}
	parts := strings.SplitN(trimmed, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, false
	}
	minorText := strings.TrimSpace(parts[1])
	for index, char := range minorText {
		if char < '0' || char > '9' {
			minorText = minorText[:index]
			break
		}
	}
	minor, err := strconv.Atoi(minorText)
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// ParseDfAvailableKilobytes 从 df -Pk 输出中解析可用块数。
//
// 输出第二行形如：/dev/sda1 1024000 512000 512000 50% /。第 4 列是可用块数。
func ParseDfAvailableKilobytes(output string) int64 {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		return 0
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0
	}
	available, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return 0
	}
	return available
}

// isNotExist 判断错误是否表示远程路径不存在。
//
// 远程 SFTP 与远程命令会以不同形式表达"不存在"，因此这里同时检查标准错误类型、
// SFTP 状态码与常见消息片段。
func isNotExist(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	var statusErr *sftp.StatusError
	if errors.As(err, &statusErr) && statusErr.FxCode() == sftp.ErrSSHFxNoSuchFile {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such file") ||
		strings.Contains(message, "not exist") ||
		strings.Contains(message, "cannot find the file")
}
