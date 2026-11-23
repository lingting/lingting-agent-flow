// 代码来源用例共用的现场构造：迁移自 tests/integration/local/source/source_test.go 的辅助部分。
//
// 执行替身只实现 SourceProvider 实际使用的固定命令模板，其余命令一律返回失败，任何未预期
// 的命令都不会被静默放过；文件系统检查类模板在 Go 内直接实现，不依赖本机是否提供 sh。
package source

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/testrunner"
)

// 测试使用的固定值。
const (
	testContainerID = "container-1"
	testDockerID    = "docker-container-1"
	gitTimeout      = 2 * time.Minute
)

// requireGit 检查本机 Git 是否可用，缺失时输出警告并跳过。
//
// 缺少本地测试依赖必须显式跳过，不能静默通过。
func requireGit(t *testrunner.Context) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		reason := "WARN [SKIP] source local integration tests: git not found in PATH."
		t.Logf("%s", reason)
		t.Skip(reason)
	}
	return path
}

// localExecutor 把容器内命令映射到本机目录执行。
type localExecutor struct {
	gitPath string
	// root 是"容器文件系统"在本机的映射根目录。
	root string
	// localCloneSource 是本机裸仓库路径，用于替换 Clone 的仓库地址。
	localCloneSource string
	mu               sync.Mutex
	// commands 记录收到的命令，用于断言命令构造。
	commands [][]string
}

// ExecInContainer 在映射根目录内执行命令。
func (e *localExecutor) ExecInContainer(
	ctx context.Context,
	dockerID string,
	command []string,
	timeout time.Duration,
) (docker.CommandResult, error) {
	e.mu.Lock()
	e.commands = append(e.commands, append([]string(nil), command...))
	e.mu.Unlock()
	if result, handled, err := e.runShellTemplate(ctx, command, timeout); handled {
		return result, err
	}
	rewritten, ok := e.rewrite(command)
	if !ok {
		return docker.CommandResult{
			ExitCode: 127,
			Stderr:   "unsupported command: " + strings.Join(command, " "),
			Status:   sshclient.CommandStatusExitError,
			Certain:  true,
		}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, rewritten[0], rewritten[1:]...)
	cmd.Dir = e.root
	output, err := cmd.CombinedOutput()
	result := docker.CommandResult{
		Stdout:  string(output),
		Status:  sshclient.CommandStatusSucceeded,
		Certain: true,
	}
	if err != nil {
		result.Status = sshclient.CommandStatusExitError
		result.ExitCode = exitCodeOf(err)
		return result, nil
	}
	return result, nil
}

// runShellTemplate 在 Go 内实现固定的文件系统检查模板。
func (e *localExecutor) runShellTemplate(
	ctx context.Context,
	command []string,
	timeout time.Duration,
) (docker.CommandResult, bool, error) {
	if err := ctx.Err(); err != nil {
		return docker.CommandResult{ExitCode: -1, Status: sshclient.CommandStatusCancelled}, true, err
	}
	switch {
	case len(command) == 3 && command[0] == "sh" && command[1] == "-c":
		return e.runScriptTemplate(command[2]), true, nil
	case len(command) == 3 && command[0] == "mkdir" && command[1] == "-p":
		if err := os.MkdirAll(e.mapPath(command[2]), 0o755); err != nil {
			return docker.CommandResult{
				ExitCode: 1,
				Stderr:   err.Error(),
				Status:   sshclient.CommandStatusExitError,
				Certain:  true,
			}, true, nil
		}
		return docker.CommandResult{Status: sshclient.CommandStatusSucceeded, Certain: true}, true, nil
	}
	return docker.CommandResult{}, false, nil
}

// runScriptTemplate 解析并执行 sh -c 模板。
func (e *localExecutor) runScriptTemplate(script string) docker.CommandResult {
	success := docker.CommandResult{Status: sshclient.CommandStatusSucceeded, Certain: true}
	failure := docker.CommandResult{
		ExitCode: 1,
		Status:   sshclient.CommandStatusExitError,
		Certain:  true,
	}
	switch {
	case strings.HasPrefix(script, "test -e "):
		target := e.mapPath(unquote(script[len("test -e "):]))
		if _, err := os.Stat(target); err != nil {
			return failure
		}
		return success
	case strings.HasPrefix(script, "test -d "):
		target := e.mapPath(unquote(script[len("test -d "):]))
		info, err := os.Stat(target)
		if err != nil || !info.IsDir() {
			return failure
		}
		return success
	case strings.HasPrefix(script, "ls -A "):
		target := e.mapPath(unquote(script[len("ls -A "):]))
		entries, err := os.ReadDir(target)
		if err != nil {
			result := failure
			result.Stderr = err.Error()
			return result
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		result := success
		if len(names) > 0 {
			result.Stdout = strings.Join(names, "\n") + "\n"
		}
		return result
	}
	return docker.CommandResult{
		ExitCode: 127,
		Stderr:   "unsupported shell template: " + script,
		Status:   sshclient.CommandStatusExitError,
		Certain:  true,
	}
}

// rewrite 把容器内命令重写为本机可执行形式。
//
// Clone 的仓库地址被替换为本机裸仓库路径，其余 /workspace 路径映射到测试目录，
// 因此真实 Git 会在用例工作空间中完成 Clone 与分支操作。
func (e *localExecutor) rewrite(command []string) ([]string, bool) {
	if len(command) == 0 {
		return nil, false
	}
	rewritten := make([]string, 0, len(command))
	for _, arg := range command {
		rewritten = append(rewritten, e.mapPath(arg))
	}
	if command[0] != "git" {
		return nil, false
	}
	rewritten[0] = e.gitPath
	for index, arg := range command {
		if arg == "clone" && e.localCloneSource != "" {
			// Clone 命令形如 git clone --no-hardlinks -- <url> <dir>。
			for replace := index + 1; replace < len(rewritten); replace++ {
				if isRepositoryURL(command[replace]) {
					rewritten[replace] = e.localCloneSource
					break
				}
			}
			break
		}
	}
	return rewritten, true
}

// isRepositoryURL 判断参数是否是仓库地址。
func isRepositoryURL(value string) bool {
	return strings.Contains(value, "://")
}

// mapPath 把容器内绝对路径映射到用例工作空间。
func (e *localExecutor) mapPath(value string) string {
	if !strings.Contains(value, domain.WorkspaceContainerRoot) {
		return value
	}
	prefix := filepath.Join(e.root, "workspace")
	if strings.HasPrefix(value, domain.WorkspaceContainerRoot) {
		rest := strings.TrimPrefix(value, domain.WorkspaceContainerRoot)
		return filepath.Join(prefix, filepath.FromSlash(strings.TrimPrefix(rest, "/")))
	}
	return strings.ReplaceAll(value, domain.WorkspaceContainerRoot, filepath.ToSlash(prefix))
}

// unquote 去掉模板中的单引号。
func unquote(value string) string {
	trimmed := strings.TrimSpace(value)
	return strings.Trim(trimmed, "'")
}

// exitCodeOf 提取退出码。
func exitCodeOf(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// fakeGuard 返回固定的容器记录。
type fakeGuard struct {
	container domain.Container
}

// EnsureExecutable 返回预设容器记录。
func (g *fakeGuard) EnsureExecutable(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	return g.container, nil
}

// newContainer 构造运行中的容器记录。
func newContainer() domain.Container {
	return domain.Container{
		ID:          domain.ID(testContainerID),
		HostID:      domain.ID("host-1"),
		DockerID:    testDockerID,
		Name:        "agentflow-container-1",
		Environment: domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "node"},
		State:       domain.ContainerStateRunning,
		DockerState: domain.DockerStateRunning,
	}
}

// repository 是一个测试用 Git 仓库。
type repository struct {
	// barePath 是裸仓库路径，作为 Clone 源。
	barePath string
	// baseCommit 是基础分支的 commit SHA。
	baseCommit string
}

// newRepository 在测试工作空间内创建一个带初始提交的裸仓库。
func newRepository(t *testrunner.Context, gitPath, root, baseBranch string) repository {
	t.Helper()
	work := filepath.Join(root, "repo-work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("创建仓库工作目录失败: %v", err)
	}
	runGit(t, gitPath, work, "init", "--initial-branch", baseBranch)
	runGit(t, gitPath, work, "config", "user.email", "test@example.com")
	runGit(t, gitPath, work, "config", "user.name", "Test")
	writeFile(t, filepath.Join(work, "README.md"), "# 测试仓库\n")
	writeFile(t, filepath.Join(work, "src", "main.go"), "package main\n")
	runGit(t, gitPath, work, "add", ".")
	runGit(t, gitPath, work, "commit", "-m", "初始提交")
	writeFile(t, filepath.Join(work, "CHANGELOG.md"), "变更记录\n")
	runGit(t, gitPath, work, "add", ".")
	runGit(t, gitPath, work, "commit", "-m", "第二个提交")
	baseCommit := strings.TrimSpace(runGit(t, gitPath, work, "rev-parse", "HEAD"))

	barePath := filepath.Join(root, "repo.git")
	runGit(t, gitPath, root, "clone", "--bare", work, barePath)
	// 显式设置远程 HEAD，使 origin/HEAD 指向基础分支。
	runGit(t, gitPath, barePath, "symbolic-ref", "HEAD", "refs/heads/"+baseBranch)
	return repository{barePath: barePath, baseCommit: baseCommit}
}

// runGit 执行一条 Git 命令并返回标准输出。
func runGit(t *testrunner.Context, gitPath, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(gitPath, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s 失败: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

// writeFile 写入文件并创建父目录。
func writeFile(t *testrunner.Context, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入文件失败: %v", err)
	}
}
