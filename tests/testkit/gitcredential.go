// 该文件把 tests/config/git.yml 中的可选凭证注入测试容器内部，供容器内的 Git
// 操作完成鉴权。
//
// 约束（与项目对凭据的长期要求一致）：
//
//   - 凭证只存在于容器内部文件系统，位于本次测试的随机临时目录，不写入项目代码
//     目录、不写入远程项目配置目录、不挂载宿主机目录，容器删除后随之消失；
//   - 凭证不进入仓库 URL、不进入日志、不进入命令行参数，也不写入数据库；
//   - 私钥只被读取后写入容器内临时文件，不修改本机文件的内容与权限；
//   - 未配置凭证时不写入任何内容，容器内 Git 沿用原有配置。
package testkit

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// DefaultTokenUsername 是只配置 token 时使用的 HTTPS 用户名。
//
// GitHub、GitLab 等平台接受任意非空用户名搭配访问令牌，这里使用平台通用的占位
// 用户名，避免要求配置文件同时填写用户名。
const DefaultTokenUsername = "x-access-token"

// GitCredentialExecutor 是容器内命令执行能力。
//
// 由各远程测试把真实的 Docker 执行器适配进来，避免测试辅助代码直接依赖 SSH 与
// Docker 实现；command 中的每个参数都会被实现方单独转义。
type GitCredentialExecutor interface {
	// ExecContainer 在运行中的容器内执行命令并写入标准输入。
	//
	// 返回的命令输出只用于诊断，实现方负责截断。
	ExecContainer(ctx context.Context, dockerID string, command []string, stdin string) (GitCredentialResult, error)
}

// GitCredentialResult 是容器内命令的执行结果。
type GitCredentialResult struct {
	// ExitCode 是容器内命令的退出码。
	ExitCode int
	// Stdout 是标准输出。
	Stdout string
	// Stderr 是标准错误。
	Stderr string
}

// Succeeded 判断命令是否成功结束。
func (r GitCredentialResult) Succeeded() bool {
	return r.ExitCode == 0
}

// GitAuth 是注入到容器内的 Git 鉴权配置。
//
// 鉴权通过容器内的一次性 git 全局配置生效：该配置只存在于本次测试的容器内部，
// 容器删除后随之消失，不涉及宿主机、远程项目配置目录与仓库的 .git/config。
type GitAuth struct {
	// Kind 是本次实际使用的鉴权方式。
	Kind GitCredentialKind
	// Dir 是容器内保存凭证文件的临时目录。
	Dir string
}

// Enabled 判断是否注入了凭证。
func (a GitAuth) Enabled() bool {
	return a.Kind != "" && a.Kind != GitCredentialNone
}

// ApplyGitCredentials 把配置中的凭证注入容器内临时目录，并写入容器内的临时 Git 配置。
//
// 凭证选择顺序固定为 私钥 → token → 账号密码，由 GitConfig.CredentialKind 决定；
// 未配置凭证时不做任何写入，返回 Kind 为 GitCredentialNone 的空配置。账号密码只
// 填一半属于配置错误，直接返回错误而不是静默降级为匿名访问。
//
// remoteDir 必须是容器内本次测试专属的绝对路径，凭证文件写入其 git 子目录。
func ApplyGitCredentials(
	ctx context.Context,
	exec GitCredentialExecutor,
	dockerID string,
	config GitConfig,
	remoteDir string,
) (GitAuth, error) {
	if missing := config.CredentialMissing(); len(missing) > 0 {
		return GitAuth{}, fmt.Errorf("Git 凭证不完整，缺少 %s", strings.Join(missing, ", "))
	}
	kind := config.CredentialKind()
	if kind == GitCredentialNone {
		return GitAuth{Kind: GitCredentialNone}, nil
	}
	dir := strings.TrimSuffix(strings.TrimSpace(remoteDir), "/") + "/git"
	if !strings.HasPrefix(dir, "/") {
		return GitAuth{}, fmt.Errorf("容器内凭证目录必须是绝对路径: %q", remoteDir)
	}
	// 目录与其中的凭证文件只对容器内当前用户可读。
	prepare := []string{
		"sh", "-c",
		"umask 077 && mkdir -p " + shellQuoteContainer(dir) + " && chmod 700 " + shellQuoteContainer(dir),
	}
	if err := runContainer(ctx, exec, dockerID, prepare, ""); err != nil {
		return GitAuth{}, fmt.Errorf("准备容器内凭证目录失败: %w", err)
	}
	switch kind {
	case GitCredentialPrivateKey:
		return applySSHKey(ctx, exec, dockerID, config, dir)
	case GitCredentialToken:
		username := strings.TrimSpace(config.Username)
		if username == "" {
			username = DefaultTokenUsername
		}
		return applyHTTPS(ctx, exec, dockerID, kind, dir, username, config.Token)
	case GitCredentialPassword:
		return applyHTTPS(ctx, exec, dockerID, kind, dir, config.Username, config.Password)
	default:
		return GitAuth{}, fmt.Errorf("不支持的 Git 鉴权方式: %s", kind)
	}
}

// applySSHKey 把私钥写入容器内临时文件，并把容器内 Git 的 ssh 命令限定到该私钥。
//
// 私钥内容通过标准输入写入，不经过命令行参数，也不出现在日志里。宿主机与容器内的
// known_hosts 都不参与：容器是一次性的测试容器，使用 accept-new 只为避免测试被
// 主机密钥首次确认阻塞。
func applySSHKey(
	ctx context.Context,
	exec GitCredentialExecutor,
	dockerID string,
	config GitConfig,
	dir string,
) (GitAuth, error) {
	keyPath := config.ResolvedSSHKeyPath()
	content, err := os.ReadFile(keyPath)
	if err != nil {
		return GitAuth{}, fmt.Errorf("读取 Git 私钥失败: %s: %w", keyPath, err)
	}
	if len(strings.TrimSpace(string(content))) == 0 {
		return GitAuth{}, fmt.Errorf("Git 私钥内容为空: %s", keyPath)
	}
	target := dir + "/id_agentflow_test"
	write := []string{"sh", "-c", "umask 077 && cat > " + shellQuoteContainer(target)}
	if err := runContainer(ctx, exec, dockerID, write, string(content)); err != nil {
		return GitAuth{}, fmt.Errorf("写入容器内 Git 私钥失败: %w", err)
	}
	sshCommand := "ssh -i " + target +
		" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new" +
		" -o UserKnownHostsFile=/dev/null"
	if err := gitConfigGlobal(ctx, exec, dockerID, "core.sshCommand", sshCommand); err != nil {
		return GitAuth{}, err
	}
	return GitAuth{Kind: GitCredentialPrivateKey, Dir: dir}, nil
}

// applyHTTPS 写入容器内一次性 credential helper，并把它配置为容器内 Git 的凭据来源。
//
// 凭据内容通过标准输入写入 helper 脚本，不进入命令行参数与日志；helper 只输出本次
// 测试的 username/password，供 Git 在需要时读取。
func applyHTTPS(
	ctx context.Context,
	exec GitCredentialExecutor,
	dockerID string,
	kind GitCredentialKind,
	dir, username, password string,
) (GitAuth, error) {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if username == "" || password == "" {
		return GitAuth{}, fmt.Errorf("Git HTTPS 凭据不完整")
	}
	if strings.ContainsAny(username, "\r\n") || strings.ContainsAny(password, "\r\n") {
		return GitAuth{}, fmt.Errorf("Git HTTPS 凭据不能包含换行")
	}
	helperPath := dir + "/credential.sh"
	helper := "#!/bin/sh\n" +
		"printf 'username=%s\\n' " + shellQuoteContainer(username) + "\n" +
		"printf 'password=%s\\n' " + shellQuoteContainer(password) + "\n"
	write := []string{
		"sh", "-c",
		"umask 077 && cat > " + shellQuoteContainer(helperPath) +
			" && chmod 700 " + shellQuoteContainer(helperPath),
	}
	if err := runContainer(ctx, exec, dockerID, write, helper); err != nil {
		return GitAuth{}, fmt.Errorf("写入容器内 Git 凭据助手失败: %w", err)
	}
	// helper 值以 ! 开头时 Git 会把它当 shell 命令执行。
	if err := gitConfigGlobal(ctx, exec, dockerID, "credential.helper", "!"+helperPath); err != nil {
		return GitAuth{}, err
	}
	return GitAuth{Kind: kind, Dir: dir}, nil
}

// gitConfigGlobal 写入容器内的临时 git 全局配置。
//
// 该配置只存在于本次测试的容器内部文件系统，容器删除后随之消失；不接受来自本机
// 的全局配置，也不修改宿主机的任何 Git 配置。
func gitConfigGlobal(
	ctx context.Context,
	exec GitCredentialExecutor,
	dockerID, key, value string,
) error {
	command := []string{"git", "config", "--global", key, value}
	if err := runContainer(ctx, exec, dockerID, command, ""); err != nil {
		return fmt.Errorf("写入容器内 Git 配置 %s 失败: %w", key, err)
	}
	return nil
}

// runContainer 执行一条容器内命令，非零退出码返回错误。
//
// 错误信息只包含退出码与截断后的标准错误，不包含命令内容，避免凭证泄漏。
func runContainer(
	ctx context.Context,
	exec GitCredentialExecutor,
	dockerID string,
	command []string,
	stdin string,
) error {
	result, err := exec.ExecContainer(ctx, dockerID, command, stdin)
	if err != nil {
		return fmt.Errorf("容器内命令执行失败: %w", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("容器内命令退出码 %d: %s", result.ExitCode, truncateOutput(result.Stderr))
	}
	return nil
}

// truncateOutput 截断容器内命令输出，避免把大段内容写进失败信息。
func truncateOutput(output string) string {
	trimmed := strings.TrimSpace(output)
	const limit = 300
	if len(trimmed) > limit {
		return trimmed[:limit] + "..."
	}
	return trimmed
}

// shellQuoteContainer 把参数转义为可安全嵌入 sh -c 的形式。
//
// 只用于本文件内的固定模板，变量都是容器内绝对路径或凭据内容。
func shellQuoteContainer(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
