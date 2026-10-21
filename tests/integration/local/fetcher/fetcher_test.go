// Package fetcher_local 提供 CodeFetcher 抽象的本机集成测试。
//
// 这里使用测试工作空间内的真实 Git 仓库与一个把"容器内命令"映射到本机目录的
// 执行替身：Clone、分支创建与 commit 校验都经过真实 Git 执行，因此不需要
// SSH、Docker 或外网。
package fetcher_local

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
	testTaskID      = "task-1"
	gitTimeout      = 2 * time.Minute
)

// requireGit 检查本机 Git 是否可用，缺失时输出警告并跳过。
func requireGit(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		reason := "WARN [SKIP] fetcher local tests: git not found in PATH."
		t.Log(reason)
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
	// cloneFailure 使 Clone 返回受控失败。
	cloneFailure *docker.CommandResult
	mu           sync.Mutex
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
	failure := e.cloneFailure
	e.mu.Unlock()
	if result, handled, err := e.runShellTemplate(ctx, command, timeout); handled {
		return result, err
	}
	if failure != nil && len(command) > 1 && command[0] == "git" && command[1] == "clone" {
		return *failure, nil
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
		if _, err := os.Stat(e.mapPath(unquote(script[len("test -e "):]))); err != nil {
			return failure
		}
		return success
	case strings.HasPrefix(script, "test -d "):
		info, err := os.Stat(e.mapPath(unquote(script[len("test -d "):])))
		if err != nil || !info.IsDir() {
			return failure
		}
		return success
	case strings.HasPrefix(script, "ls -A "):
		entries, err := os.ReadDir(e.mapPath(unquote(script[len("ls -A "):])))
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
			for replace := index + 1; replace < len(rewritten); replace++ {
				if strings.Contains(command[replace], "://") {
					rewritten[replace] = e.localCloneSource
					break
				}
			}
			break
		}
	}
	return rewritten, true
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
	return strings.Trim(strings.TrimSpace(value), "'")
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
func newContainer(attemptID domain.ID) domain.Container {
	return domain.Container{
		ID:          domain.ID(testContainerID),
		HostID:      domain.ID("host-1"),
		DockerID:    testDockerID,
		Name:        "agentflow-container-1",
		Environment: domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "node"},
		TaskID:      ptrID(domain.ID(testTaskID)),
		AttemptID:   ptrID(attemptID),
		State:       domain.ContainerStateRunning,
		DockerState: domain.DockerStateRunning,
	}
}

// ptrID 返回标识的指针。
func ptrID(id domain.ID) *domain.ID {
	return &id
}

// repository 是一个测试用 Git 仓库。
type repository struct {
	barePath   string
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
	baseCommit := strings.TrimSpace(runGit(t, gitPath, work, "rev-parse", "HEAD"))

	barePath := filepath.Join(root, "repo.git")
	runGit(t, gitPath, root, "clone", "--bare", work, barePath)
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

// newFetcher 构造绑定到测试执行替身的 Git 代码获取实现。
func newFetcher(
	t *testing.T,
	gitPath, root, cloneSource string,
	attemptID domain.ID,
) (source.Fetcher, *localExecutor) {
	t.Helper()
	executor := &localExecutor{gitPath: gitPath, root: root, localCloneSource: cloneSource}
	fetcher, err := source.NewGitFetcher(source.Options{
		Executor:     executor,
		Guard:        &fakeGuard{container: newContainer(attemptID)},
		CloneTimeout: gitTimeout,
		ProbeTimeout: gitTimeout,
	})
	if err != nil {
		t.Fatalf("构造 Git 代码获取实现失败: %v", err)
	}
	return fetcher, executor
}

// TestFetcherKindIsGit 校验第一版只注册 git 获取方式。
func TestFetcherKindIsGit(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	root := workspace.Dir("container-root")
	fetcher, _ := newFetcher(t, gitPath, root, "", domain.ID("attempt-1"))
	if fetcher.Kind() != domain.CodeSourceGit {
		t.Fatalf("获取方式不符: %s", fetcher.Kind())
	}
}

// TestFetcherRejectsUnknownSourceType 校验未知获取方式被拒绝。
func TestFetcherRejectsUnknownSourceType(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	root := workspace.Dir("container-root")
	fetcher, executor := newFetcher(t, gitPath, root, "", domain.ID("attempt-1"))
	_, err := fetcher.Fetch(context.Background(), source.FetchRequest{
		TaskID:      testTaskID,
		AttemptID:   "attempt-1",
		SourceType:  domain.CodeSourceType("http_archive"),
		Repository:  "https://git.example.com/group/repo.git",
		ContainerID: testContainerID,
		Timeout:     gitTimeout,
	})
	if source.ErrorCodeOf(err) != source.CodeInvalidArgument {
		t.Fatalf("未知获取方式必须返回参数错误: %v", err)
	}
	if len(executor.commands) != 0 {
		t.Fatal("参数校验失败时不得执行任何命令")
	}
}

// TestFetcherCloneSucceeds 校验真实 Clone 与任务分支创建。
func TestFetcherCloneSucceeds(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	workspace.Report()
	root := workspace.Dir("container-root")
	repo := newRepository(t, gitPath, workspace.Dir("repository"), "main")
	fetcher, executor := newFetcher(t, gitPath, root, repo.barePath, domain.ID("attempt-1"))
	result, err := fetcher.Fetch(context.Background(), source.FetchRequest{
		ProjectID:   "demo",
		TaskID:      testTaskID,
		AttemptID:   "attempt-1",
		SourceType:  domain.CodeSourceGit,
		Repository:  "https://git.example.com/group/repo.git",
		ContainerID: testContainerID,
		Timeout:     gitTimeout,
	})
	if err != nil {
		t.Fatalf("代码获取失败: %v", err)
	}
	if !result.Succeeded() {
		t.Fatalf("结果必须报告成功: %+v", result)
	}
	if result.SourceType != domain.CodeSourceGit {
		t.Fatalf("获取方式不符: %s", result.SourceType)
	}
	if result.BaseBranch != "main" {
		t.Fatalf("基础分支不符: %s", result.BaseBranch)
	}
	if result.BaseCommit != repo.baseCommit {
		t.Fatalf("基础 commit 不符: %s", result.BaseCommit)
	}
	if result.TaskBranch != "agent/"+testTaskID {
		t.Fatalf("任务分支不符: %s", result.TaskBranch)
	}
	if result.TaskBranchSource != domain.TaskBranchSourceNew {
		t.Fatalf("分支来源不符: %s", result.TaskBranchSource)
	}
	if result.Commit != repo.baseCommit {
		t.Fatalf("HEAD commit 不符: %s", result.Commit)
	}
	if result.Stage != domain.CodeFetchStageVerify {
		t.Fatalf("阶段不符: %s", result.Stage)
	}
	workDir := filepath.Join(root, "workspace", "project")
	if _, statErr := os.Stat(filepath.Join(workDir, "README.md")); statErr != nil {
		t.Fatalf("容器内工作目录缺少仓库文件: %v", statErr)
	}
	current := strings.TrimSpace(runGit(t, gitPath, workDir, "rev-parse", "--abbrev-ref", "HEAD"))
	if current != "agent/"+testTaskID {
		t.Fatalf("当前分支不符: %q", current)
	}
	// Clone 必须以参数数组形式执行，不经过 shell 拼接。
	found := false
	for _, command := range executor.commands {
		if len(command) > 1 && command[0] == "git" && command[1] == "clone" {
			found = true
			for _, arg := range command {
				if strings.Contains(arg, ";") || strings.Contains(arg, "&&") {
					t.Fatalf("Clone 命令不得包含 shell 连接符: %v", command)
				}
			}
		}
	}
	if !found {
		t.Fatal("必须通过参数数组执行 git clone")
	}
	t.Logf("CONTAINER_WORKDIR=%s", workDir)
	t.Logf("CLONE_COMMIT=%s", result.Commit)
}

// TestFetcherRejectsNonEmptyTarget 校验非空目标目录不会被覆盖。
func TestFetcherRejectsNonEmptyTarget(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	root := workspace.Dir("container-root")
	repo := newRepository(t, gitPath, workspace.Dir("repository"), "main")
	workDir := filepath.Join(root, "workspace", "project")
	writeFile(t, filepath.Join(workDir, "existing.txt"), "已有内容\n")
	fetcher, _ := newFetcher(t, gitPath, root, repo.barePath, domain.ID("attempt-1"))
	_, err := fetcher.Fetch(context.Background(), source.FetchRequest{
		TaskID:      testTaskID,
		AttemptID:   "attempt-1",
		Repository:  "https://git.example.com/group/repo.git",
		ContainerID: testContainerID,
		Timeout:     gitTimeout,
	})
	if source.ErrorCodeOf(err) != source.CodeTargetDirNotEmpty {
		t.Fatalf("非空目录必须被拒绝: %v", err)
	}
	// 已有内容必须保留。
	if _, statErr := os.Stat(filepath.Join(workDir, "existing.txt")); statErr != nil {
		t.Fatalf("已有内容被删除: %v", statErr)
	}
}

// TestFetcherCloneFailureKeepsStage 校验 Clone 失败记录真实原因。
func TestFetcherCloneFailureKeepsStage(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	root := workspace.Dir("container-root")
	fetcher, executor := newFetcher(t, gitPath, root, "", domain.ID("attempt-1"))
	executor.cloneFailure = &docker.CommandResult{
		ExitCode: 128,
		Stderr:   "fatal: could not resolve host: git.example.com\n",
		Status:   sshclient.CommandStatusExitError,
		Certain:  true,
	}
	result, err := fetcher.Fetch(context.Background(), source.FetchRequest{
		TaskID:      testTaskID,
		AttemptID:   "attempt-1",
		Repository:  "https://git.example.com/group/repo.git",
		ContainerID: testContainerID,
		Timeout:     gitTimeout,
	})
	if err == nil {
		t.Fatal("Clone 失败必须返回错误")
	}
	if result.Succeeded() {
		t.Fatal("失败的获取不得报告成功")
	}
	if result.Stage != domain.CodeFetchStageClone {
		t.Fatalf("失败阶段不符: %s", result.Stage)
	}
	if result.ErrorCode != source.CodeRepositoryAccessFailed.String() {
		t.Fatalf("错误类别不符: %s", result.ErrorCode)
	}
	if result.ErrorSummary == "" {
		t.Fatal("失败必须保留脱敏原因")
	}
}

// TestFetcherAuthFailureIsExplicit 校验需要认证时返回明确错误。
func TestFetcherAuthFailureIsExplicit(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	root := workspace.Dir("container-root")
	fetcher, executor := newFetcher(t, gitPath, root, "", domain.ID("attempt-1"))
	executor.cloneFailure = &docker.CommandResult{
		ExitCode: 128,
		Stderr:   "fatal: Authentication failed for 'https://git.example.com/group/repo.git/'\n",
		Status:   sshclient.CommandStatusExitError,
		Certain:  true,
	}
	result, err := fetcher.Fetch(context.Background(), source.FetchRequest{
		TaskID:      testTaskID,
		AttemptID:   "attempt-1",
		Repository:  "https://git.example.com/group/repo.git",
		ContainerID: testContainerID,
		Timeout:     gitTimeout,
	})
	if source.ErrorCodeOf(err) != source.CodeRepositoryAuthRequired {
		t.Fatalf("认证失败必须返回明确类别: %v", err)
	}
	if result.ErrorCode != source.CodeRepositoryAuthRequired.String() {
		t.Fatalf("错误类别不符: %s", result.ErrorCode)
	}
}

// TestFetcherGitUnavailable 校验容器内缺少 Git 时的错误。
func TestFetcherGitUnavailable(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	root := workspace.Dir("container-root")
	repo := newRepository(t, gitPath, workspace.Dir("repository"), "main")
	fetcher, executor := newFetcher(t, gitPath, root, repo.barePath, domain.ID("attempt-1"))
	// 把 git 替换为不存在的可执行文件，模拟容器内缺少 Git。
	executor.gitPath = filepath.Join(root, "missing-git")
	_, err := fetcher.Fetch(context.Background(), source.FetchRequest{
		TaskID:      testTaskID,
		AttemptID:   "attempt-1",
		Repository:  "https://git.example.com/group/repo.git",
		ContainerID: testContainerID,
		Timeout:     gitTimeout,
	})
	if source.ErrorCodeOf(err) != source.CodeGitUnavailable {
		t.Fatalf("缺少 Git 必须返回明确类别: %v", err)
	}
}

// TestFetcherCancelledBeforeStart 校验已取消的上下文不执行命令。
func TestFetcherCancelledBeforeStart(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	root := workspace.Dir("container-root")
	repo := newRepository(t, gitPath, workspace.Dir("repository"), "main")
	fetcher, executor := newFetcher(t, gitPath, root, repo.barePath, domain.ID("attempt-1"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := fetcher.Fetch(ctx, source.FetchRequest{
		TaskID:      testTaskID,
		AttemptID:   "attempt-1",
		Repository:  "https://git.example.com/group/repo.git",
		ContainerID: testContainerID,
		Timeout:     gitTimeout,
	})
	if err == nil {
		t.Fatal("已取消的上下文必须失败")
	}
	if result.Succeeded() {
		t.Fatal("取消的获取不得报告成功")
	}
	// 已取消的上下文不应完成 Clone。
	workDir := filepath.Join(root, "workspace", "project")
	if _, statErr := os.Stat(filepath.Join(workDir, ".git")); statErr == nil {
		t.Fatal("取消后不应留下完整的 Git 工作区")
	}
	_ = executor
}

// TestFetcherBaseBranchDetection 校验不同默认分支名称的识别。
func TestFetcherBaseBranchDetection(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	for _, baseBranch := range []string{"main", "master", "trunk"} {
		baseBranch := baseBranch
		t.Run(baseBranch, func(t *testing.T) {
			t.Parallel()
			workspace := testkit.New(t)
			root := workspace.Dir("container-root-" + baseBranch)
			repo := newRepository(t, gitPath, workspace.Dir("repository-"+baseBranch), baseBranch)
			fetcher, _ := newFetcher(t, gitPath, root, repo.barePath, domain.ID("attempt-1"))
			result, err := fetcher.Fetch(context.Background(), source.FetchRequest{
				TaskID:      testTaskID,
				AttemptID:   "attempt-1",
				Repository:  "https://git.example.com/group/repo.git",
				ContainerID: testContainerID,
				Timeout:     gitTimeout,
			})
			if err != nil {
				t.Fatalf("代码获取失败: %v", err)
			}
			if result.BaseBranch != baseBranch {
				t.Fatalf("期望基础分支 %q，实际 %q", baseBranch, result.BaseBranch)
			}
			if result.BaseCommit != repo.baseCommit {
				t.Fatalf("基础 commit 不符: %s", result.BaseCommit)
			}
			if result.TaskBranch != "agent/"+testTaskID {
				t.Fatalf("任务分支不符: %s", result.TaskBranch)
			}
		})
	}
}

// TestFetcherRejectsInvalidTaskID 校验非法任务标识无法生成分支。
func TestFetcherRejectsInvalidTaskID(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	root := workspace.Dir("container-root")
	repo := newRepository(t, gitPath, workspace.Dir("repository"), "main")
	fetcher, executor := newFetcher(t, gitPath, root, repo.barePath, domain.ID("attempt-1"))
	_, err := fetcher.Fetch(context.Background(), source.FetchRequest{
		TaskID:      "Task/1",
		AttemptID:   "attempt-1",
		Repository:  "https://git.example.com/group/repo.git",
		ContainerID: testContainerID,
		Timeout:     gitTimeout,
	})
	if source.ErrorCodeOf(err) != source.CodeInvalidArgument {
		t.Fatalf("非法任务标识必须返回参数错误: %v", err)
	}
	if len(executor.commands) != 0 {
		t.Fatal("参数校验失败时不得执行任何命令")
	}
}

// TestFetcherRejectsCredentialURL 校验内嵌凭据的仓库地址被拒绝。
func TestFetcherRejectsCredentialURL(t *testing.T) {
	t.Parallel()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	root := workspace.Dir("container-root")
	repo := newRepository(t, gitPath, workspace.Dir("repository"), "main")
	fetcher, executor := newFetcher(t, gitPath, root, repo.barePath, domain.ID("attempt-1"))
	_, err := fetcher.Fetch(context.Background(), source.FetchRequest{
		TaskID:      testTaskID,
		AttemptID:   "attempt-1",
		Repository:  "https://user:token@git.example.com/group/repo.git",
		ContainerID: testContainerID,
		Timeout:     gitTimeout,
	})
	if source.ErrorCodeOf(err) != source.CodeInvalidRepositoryURL {
		t.Fatalf("内嵌凭据的地址必须被拒绝: %v", err)
	}
	if len(executor.commands) != 0 {
		t.Fatal("地址校验失败时不得执行任何命令")
	}
}
