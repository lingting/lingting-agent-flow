// Package source_local 提供 Git Clone 代码来源的本机集成测试。
//
// 这里使用测试工作空间内的真实 Git 仓库与一个把"容器内命令"映射到本机目录的
// 执行替身：Git 命令、分支创建、commit 校验与目录检查都经过真实 Git 执行，
// 因此不需要 SSH、Docker 或外网。
package source_local

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/source"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
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
func requireGit(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		reason := "WARN [SKIP] source local integration tests: git not found in PATH."
		t.Log(reason)
		t.Skip(reason)
	}
	return path
}

// localExecutor 把容器内命令映射到本机目录执行。
//
// 它只实现 SourceProvider 实际使用的固定命令模板，其余命令一律返回失败：
// 这样任何未预期的命令都不会被静默放过。文件系统检查类模板在 Go 内直接实现，
// 因此不依赖本机是否提供 sh。
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
// 因此真实 Git 会在测试工作空间中完成 Clone 与分支操作。
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

// mapPath 把容器内绝对路径映射到测试工作空间。
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
func newRepository(t *testing.T, gitPath, root, baseBranch string) repository {
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
func runGit(t *testing.T, gitPath, dir string, args ...string) string {
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
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入文件失败: %v", err)
	}
}

// TestFetchWithLocalPath 使用本机路径形式的仓库地址验证完整 Clone 流程。
//
// 仓库地址通过 ssh:// 形式表达，但执行替身把它映射到本机裸仓库，
// 因此 Git 命令、分支操作与 commit 校验都真实执行。
func TestFetchWithLocalPath(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	workspace.Report()
	root := workspace.Dir("container-root")
	repo := newRepository(t, gitPath, workspace.Dir("repository"), "main")
	executor := &localExecutor{gitPath: gitPath, root: root, localCloneSource: repo.barePath}
	provider, err := source.NewProvider(source.Options{
		Executor:     executor,
		Guard:        &fakeGuard{container: newContainer()},
		CloneTimeout: gitTimeout,
		ProbeTimeout: gitTimeout,
	})
	if err != nil {
		t.Fatalf("构造代码来源失败: %v", err)
	}
	result, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        "task-1",
		ContainerID:   testContainerID,
		RepositoryURL: "https://git.example.com/group/repo.git",
	})
	if err != nil {
		t.Fatalf("代码获取失败: %v", err)
	}
	workDir := filepath.Join(root, "workspace", "project")
	// 工作区文件来自真实 Clone 结果。
	if _, statErr := os.Stat(filepath.Join(workDir, "README.md")); statErr != nil {
		t.Fatalf("容器内工作目录缺少仓库文件: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(workDir, ".git")); statErr != nil {
		t.Fatalf("容器内工作目录缺少 Git 元数据: %v", statErr)
	}
	// 基础分支按 origin/HEAD 识别。
	if result.BaseBranch != "main" {
		t.Fatalf("期望基础分支 main，实际 %q", result.BaseBranch)
	}
	if result.BaseSelection != domain.GitBranchSelectionOriginHead {
		t.Fatalf("期望识别方式 origin_head，实际 %q", result.BaseSelection)
	}
	if result.BaseCommit != repo.baseCommit {
		t.Fatalf("基础分支 commit 不符: 期望 %s，实际 %s", repo.baseCommit, result.BaseCommit)
	}
	// 任务分支为 agent/<task-id>，且 HEAD 与基础分支起点一致。
	if result.TaskBranch != "agent/task-1" {
		t.Fatalf("期望任务分支 agent/task-1，实际 %q", result.TaskBranch)
	}
	current := strings.TrimSpace(runGit(t, gitPath, workDir, "rev-parse", "--abbrev-ref", "HEAD"))
	if current != "agent/task-1" {
		t.Fatalf("工作区当前分支不符: %q", current)
	}
	if result.Commit != repo.baseCommit {
		t.Fatalf("任务分支起点 commit 不符: 期望 %s，实际 %s", repo.baseCommit, result.Commit)
	}
	t.Logf("CONTAINER_WORKDIR=%s", workDir)
	t.Logf("CLONE_COMMIT=%s", result.Commit)
}

// TestFetchBaseBranchMissing 验证指定的基础分支不存在时失败。
func TestFetchBaseBranchMissing(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	root := workspace.Dir("container-root")
	repo := newRepository(t, gitPath, workspace.Dir("repository"), "main")
	executor := &localExecutor{gitPath: gitPath, root: root, localCloneSource: repo.barePath}
	provider, err := source.NewProvider(source.Options{
		Executor:     executor,
		Guard:        &fakeGuard{container: newContainer()},
		CloneTimeout: gitTimeout,
		ProbeTimeout: gitTimeout,
	})
	if err != nil {
		t.Fatalf("构造代码来源失败: %v", err)
	}
	_, err = provider.Fetch(context.Background(), source.Request{
		TaskID:        "task-1",
		ContainerID:   testContainerID,
		RepositoryURL: "https://git.example.com/group/repo.git",
		BaseBranch:    "release",
	})
	if code := source.ErrorCodeOf(err); code != source.CodeBaseBranchMissing {
		t.Fatalf("期望错误类别 %s，实际 %s（%v）", source.CodeBaseBranchMissing, code, err)
	}
	// 失败后克隆目录仍然保留，便于人工检查。
	workDir := filepath.Join(root, "workspace", "project")
	if _, statErr := os.Stat(workDir); statErr != nil {
		t.Fatalf("失败后克隆目录不应被删除: %v", statErr)
	}
}

// TestFetchTargetDirNotEmptyKept 验证目标目录非空时失败且内容不被删除。
func TestFetchTargetDirNotEmptyKept(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	root := workspace.Dir("container-root")
	repo := newRepository(t, gitPath, workspace.Dir("repository"), "main")
	workDir := filepath.Join(root, "workspace", "project")
	writeFile(t, filepath.Join(workDir, "existing.txt"), "用户已有内容\n")
	executor := &localExecutor{gitPath: gitPath, root: root, localCloneSource: repo.barePath}
	provider, err := source.NewProvider(source.Options{
		Executor:     executor,
		Guard:        &fakeGuard{container: newContainer()},
		CloneTimeout: gitTimeout,
		ProbeTimeout: gitTimeout,
	})
	if err != nil {
		t.Fatalf("构造代码来源失败: %v", err)
	}
	_, err = provider.Fetch(context.Background(), source.Request{
		TaskID:        "task-1",
		ContainerID:   testContainerID,
		RepositoryURL: "https://git.example.com/group/repo.git",
	})
	if code := source.ErrorCodeOf(err); code != source.CodeTargetDirNotEmpty {
		t.Fatalf("期望错误类别 %s，实际 %s（%v）", source.CodeTargetDirNotEmpty, code, err)
	}
	content, readErr := os.ReadFile(filepath.Join(workDir, "existing.txt"))
	if readErr != nil {
		t.Fatalf("目标目录内容不应被删除: %v", readErr)
	}
	if strings.TrimSpace(string(content)) != "用户已有内容" {
		t.Fatalf("目标目录内容被修改: %q", content)
	}
}

// TestFetchExistingRemoteTaskBranch 验证远程已存在任务分支时检出该分支。
func TestFetchExistingRemoteTaskBranch(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	root := workspace.Dir("container-root")
	repo := newRepository(t, gitPath, workspace.Dir("repository"), "main")
	// 在裸仓库中创建 agent/task-1 分支并追加一个提交。
	cloneDir := workspace.Dir("seed")
	runGit(t, gitPath, cloneDir, "clone", repo.barePath, "seed-work")
	seedWork := filepath.Join(cloneDir, "seed-work")
	runGit(t, gitPath, seedWork, "config", "user.email", "test@example.com")
	runGit(t, gitPath, seedWork, "config", "user.name", "Test")
	runGit(t, gitPath, seedWork, "checkout", "-b", "agent/task-1")
	writeFile(t, filepath.Join(seedWork, "branch.txt"), "任务分支内容\n")
	runGit(t, gitPath, seedWork, "add", ".")
	runGit(t, gitPath, seedWork, "commit", "-m", "任务分支提交")
	branchCommit := strings.TrimSpace(runGit(t, gitPath, seedWork, "rev-parse", "HEAD"))
	runGit(t, gitPath, seedWork, "push", "origin", "agent/task-1")

	executor := &localExecutor{gitPath: gitPath, root: root, localCloneSource: repo.barePath}
	provider, err := source.NewProvider(source.Options{
		Executor:     executor,
		Guard:        &fakeGuard{container: newContainer()},
		CloneTimeout: gitTimeout,
		ProbeTimeout: gitTimeout,
	})
	if err != nil {
		t.Fatalf("构造代码来源失败: %v", err)
	}
	result, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        "task-1",
		ContainerID:   testContainerID,
		RepositoryURL: "https://git.example.com/group/repo.git",
	})
	if err != nil {
		t.Fatalf("代码获取失败: %v", err)
	}
	if result.TaskBranchSource != source.TaskBranchSourceRemote {
		t.Fatalf("期望分支来源 %q，实际 %q", source.TaskBranchSourceRemote, result.TaskBranchSource)
	}
	if result.Commit != branchCommit {
		t.Fatalf("任务分支 commit 不符: 期望 %s，实际 %s", branchCommit, result.Commit)
	}
	workDir := filepath.Join(root, "workspace", "project")
	if _, statErr := os.Stat(filepath.Join(workDir, "branch.txt")); statErr != nil {
		t.Fatalf("远程任务分支内容未检出: %v", statErr)
	}
}

// TestConcurrentFetchesDoNotShareWorkDir 验证并发任务各自使用独立目录与分支状态。
func TestConcurrentFetchesDoNotShareWorkDir(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	workspace.Report()
	root := workspace.Dir("container-root")
	repo := newRepository(t, gitPath, workspace.Dir("repository"), "main")
	taskIDs := []string{"task-a", "task-b", "task-c"}
	type outcome struct {
		taskID string
		result source.Result
		err    error
	}
	results := make(chan outcome, len(taskIDs))
	for _, taskID := range taskIDs {
		taskID := taskID
		go func() {
			executor := &localExecutor{gitPath: gitPath, root: root, localCloneSource: repo.barePath}
			provider, err := source.NewProvider(source.Options{
				Executor:     executor,
				Guard:        &fakeGuard{container: newContainer()},
				CloneTimeout: gitTimeout,
				ProbeTimeout: gitTimeout,
			})
			if err != nil {
				results <- outcome{taskID: taskID, err: err}
				return
			}
			result, fetchErr := provider.Fetch(context.Background(), source.Request{
				TaskID:        taskID,
				ContainerID:   testContainerID,
				RepositoryURL: "https://git.example.com/group/repo.git",
				WorkDir:       domain.DefaultContainerWorkDir + "/" + taskID,
			})
			results <- outcome{taskID: taskID, result: result, err: fetchErr}
		}()
	}
	seen := make(map[string]string, len(taskIDs))
	for index := 0; index < len(taskIDs); index++ {
		item := <-results
		if item.err != nil {
			t.Fatalf("任务 %s 代码获取失败: %v", item.taskID, item.err)
		}
		if existing, ok := seen[item.result.WorkDir]; ok {
			t.Fatalf("任务 %s 与 %s 使用了相同目录: %s", item.taskID, existing, item.result.WorkDir)
		}
		seen[item.result.WorkDir] = item.taskID
		if item.result.TaskBranch != "agent/"+item.taskID {
			t.Fatalf("任务 %s 分支不符: %q", item.taskID, item.result.TaskBranch)
		}
		// 每个工作区的分支状态独立。
		workDir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(item.result.WorkDir, "/")))
		current := strings.TrimSpace(runGit(t, gitPath, workDir, "rev-parse", "--abbrev-ref", "HEAD"))
		if current != "agent/"+item.taskID {
			t.Fatalf("任务 %s 工作区分支不符: %q", item.taskID, current)
		}
	}
	if len(seen) != len(taskIDs) {
		t.Fatalf("期望 %d 个独立目录，实际 %d", len(taskIDs), len(seen))
	}
}

// TestRepositoryAndWorkDirKeptAfterTest 确认测试产物保留在测试工作空间中。
func TestRepositoryAndWorkDirKeptAfterTest(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	workspace.Report()
	root := workspace.Dir("container-root")
	repo := newRepository(t, gitPath, workspace.Dir("repository"), "main")
	executor := &localExecutor{gitPath: gitPath, root: root, localCloneSource: repo.barePath}
	provider, err := source.NewProvider(source.Options{
		Executor:     executor,
		Guard:        &fakeGuard{container: newContainer()},
		CloneTimeout: gitTimeout,
		ProbeTimeout: gitTimeout,
	})
	if err != nil {
		t.Fatalf("构造代码来源失败: %v", err)
	}
	if _, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        "task-1",
		ContainerID:   testContainerID,
		RepositoryURL: "https://git.example.com/group/repo.git",
	}); err != nil {
		t.Fatalf("代码获取失败: %v", err)
	}
	// 仓库与克隆结果都必须存在于测试工作空间中，测试结束不删除。
	for _, path := range []string{
		repo.barePath,
		filepath.Join(root, "workspace", "project"),
	} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("测试产物应保留在磁盘上: %s: %v", path, statErr)
		}
	}
	t.Logf("TEST_REPOSITORY=%s", repo.barePath)
}
