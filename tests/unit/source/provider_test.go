// Package source_unit 提供 Git Clone 代码来源的单元测试。
//
// 这里使用可替换的容器执行器与容器核验器替身：命令构造、参数转义、错误分类与
// 分支初始化策略都经过真实代码路径，但不需要 SSH 连接、Docker Engine 或 Git。
package source_unit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/source"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 测试使用的固定值。
const (
	testDockerID    = "docker-container-1"
	testContainerID = "container-1"
	testTaskID      = "task-1"
	testRepository  = "https://git.example.com/group/repo.git"
	testBaseCommit  = "1111111111111111111111111111111111111111"
	testHeadCommit  = "2222222222222222222222222222222222222222"
)

// fakeExecutor 记录容器内命令并按预设规则返回结果。
type fakeExecutor struct {
	mu       sync.Mutex
	commands [][]string
	// responder 按命令返回结果；返回 zero 结果时使用默认成功响应。
	responder func(command []string) (docker.CommandResult, error)
}

// ExecInContainer 记录命令并返回预设结果。
func (e *fakeExecutor) ExecInContainer(
	ctx context.Context,
	dockerID string,
	command []string,
	timeout time.Duration,
) (docker.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return docker.CommandResult{
			Status:   dockerStatusCancelled(),
			ExitCode: -1,
			Certain:  false,
		}, err
	}
	e.mu.Lock()
	e.commands = append(e.commands, append([]string{dockerID}, command...))
	responder := e.responder
	e.mu.Unlock()
	if responder != nil {
		if result, err := responder(command); result != (docker.CommandResult{}) || err != nil {
			return result, err
		}
	}
	return succeeded(""), nil
}

// recorded 返回收到的全部命令副本。
func (e *fakeExecutor) recorded() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	copied := make([][]string, 0, len(e.commands))
	for _, command := range e.commands {
		copied = append(copied, append([]string(nil), command...))
	}
	return copied
}

// contains 判断是否收到过满足条件的命令。
func (e *fakeExecutor) contains(match func(command []string) bool) bool {
	for _, command := range e.recorded() {
		if match(command) {
			return true
		}
	}
	return false
}

// fakeGuard 返回预设的容器记录或错误。
type fakeGuard struct {
	container domain.Container
	err       error
	mu        sync.Mutex
	calls     int
}

// EnsureExecutable 返回预设容器记录。
func (g *fakeGuard) EnsureExecutable(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	if g.err != nil {
		return domain.Container{}, g.err
	}
	return g.container, nil
}

// succeeded 构造成功结果。
func succeeded(stdout string) docker.CommandResult {
	return docker.CommandResult{
		ExitCode: 0,
		Stdout:   stdout,
		Status:   dockerStatusSucceeded(),
		Certain:  true,
	}
}

// exitCode 构造非零退出码结果。
func exitCode(code int, stderr string) docker.CommandResult {
	return docker.CommandResult{
		ExitCode: code,
		Stderr:   stderr,
		Status:   dockerStatusExitError(),
		Certain:  true,
	}
}

// newContainer 构造一个处于运行状态且受管理的容器记录。
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

// newProvider 构造使用替身的代码来源。
func newProvider(t *testing.T, executor *fakeExecutor, guard *fakeGuard) *source.Provider {
	t.Helper()
	provider, err := source.NewProvider(source.Options{
		Executor:     executor,
		Guard:        guard,
		CloneTimeout: 10 * time.Second,
		ProbeTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("构造代码来源失败: %v", err)
	}
	return provider
}

// standardResponder 返回一个覆盖完整成功流程的命令响应器。
//
// 它按命令内容匹配：Git 版本、目录存在性、Clone、分支列举、引用解析、
// 分支检出与工作区校验都给出符合预期的结果。
func standardResponder(originHead string, branches []string, hasRemoteTaskBranch bool) func([]string) (docker.CommandResult, error) {
	return taskResponder(testTaskID, originHead, branches, hasRemoteTaskBranch)
}

// taskResponder 返回针对指定任务标识的命令响应器。
//
// 并发用例各自使用不同任务标识，因此分支名解析必须按任务区分。
func taskResponder(
	taskID, originHead string,
	branches []string,
	hasRemoteTaskBranch bool,
) func([]string) (docker.CommandResult, error) {
	known := make(map[string]struct{}, len(branches))
	for _, branch := range branches {
		known[branch] = struct{}{}
	}
	remoteTaskRef := "refs/remotes/origin/agent/" + taskID
	localTaskRef := "refs/heads/agent/" + taskID
	return func(command []string) (docker.CommandResult, error) {
		joined := strings.Join(command, " ")
		switch {
		case joined == "git --version":
			return succeeded("git version 2.43.0\n"), nil
		case strings.HasPrefix(joined, "sh -c test -e "):
			return exitCode(1, ""), nil
		case strings.HasPrefix(joined, "sh -c ls -A "):
			return succeeded(""), nil
		case strings.HasPrefix(joined, "mkdir -p "):
			return succeeded(""), nil
		case strings.Contains(joined, "git clone"):
			return succeeded("Cloning into '/workspace/project'...\n"), nil
		case strings.Contains(joined, "symbolic-ref"):
			return succeeded(originHead + "\n"), nil
		case strings.Contains(joined, "for-each-ref"):
			return succeeded(strings.Join(branches, "\n") + "\n"), nil
		case strings.Contains(joined, "rev-parse --verify"):
			switch {
			case strings.Contains(joined, remoteTaskRef) && hasRemoteTaskBranch:
				return succeeded(testHeadCommit + "\n"), nil
			case strings.Contains(joined, remoteTaskRef), strings.Contains(joined, localTaskRef):
				return exitCode(1, ""), nil
			}
			for branch := range known {
				if strings.Contains(joined, "refs/remotes/origin/"+branch) ||
					strings.Contains(joined, "refs/heads/"+branch) {
					return succeeded(testBaseCommit + "\n"), nil
				}
			}
			return exitCode(1, ""), nil
		case strings.Contains(joined, "checkout"):
			return succeeded("Switched to a new branch\n"), nil
		case strings.HasPrefix(joined, "sh -c test -d "):
			return succeeded(""), nil
		case strings.Contains(joined, "rev-parse --abbrev-ref"):
			return succeeded("agent/" + taskID + "\n"), nil
		case strings.Contains(joined, "rev-parse HEAD"):
			return succeeded(testHeadCommit + "\n"), nil
		}
		return docker.CommandResult{}, nil
	}
}

// TestRepositoryURLEscaping 验证仓库地址与目录以独立参数传递，不会被拼进 shell 字符串。
func TestRepositoryURLEscaping(t *testing.T) {
	t.Parallel()
	executor := &fakeExecutor{responder: standardResponder("origin/main", []string{"main"}, false)}
	provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
	if _, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        testTaskID,
		ContainerID:   testContainerID,
		RepositoryURL: testRepository,
	}); err != nil {
		t.Fatalf("代码获取失败: %v", err)
	}
	var cloneCommand []string
	for _, command := range executor.recorded() {
		if len(command) > 1 && command[1] == "git" {
			for _, arg := range command {
				if arg == "clone" {
					cloneCommand = command
				}
			}
		}
	}
	if cloneCommand == nil {
		t.Fatalf("没有收到 git clone 命令: %v", executor.recorded())
	}
	// 命令必须以参数切片形式给出：仓库地址与目标目录各自是一个独立参数。
	containsRepository := false
	containsWorkDir := false
	for _, arg := range cloneCommand {
		if arg == testRepository {
			containsRepository = true
		}
		if arg == domain.DefaultContainerWorkDir {
			containsWorkDir = true
		}
	}
	if !containsRepository || !containsWorkDir {
		t.Fatalf("Clone 参数未按结构化方式传递: %v", cloneCommand)
	}
	for _, arg := range cloneCommand {
		if strings.ContainsAny(arg, ";&|`$") {
			t.Fatalf("参数中不应出现 shell 元字符: %q", arg)
		}
	}
}

// TestInvalidRepositoryURL 验证非法仓库地址在容器操作之前就被拒绝。
func TestInvalidRepositoryURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		url  string
	}{
		{"空地址", ""},
		{"不支持的协议", "git://git.example.com/repo.git"},
		{"内嵌口令", "https://user:token@git.example.com/repo.git"},
		{"缺少仓库路径", "https://git.example.com"},
		{"含空白", "https://git.example.com/repo .git"},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			t.Parallel()
			executor := &fakeExecutor{}
			provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
			_, err := provider.Fetch(context.Background(), source.Request{
				TaskID:        testTaskID,
				ContainerID:   testContainerID,
				RepositoryURL: item.url,
			})
			if err == nil {
				t.Fatalf("期望失败，实际成功")
			}
			if code := source.ErrorCodeOf(err); code != source.CodeInvalidRepositoryURL {
				t.Fatalf("期望错误类别 %s，实际 %s（%v）", source.CodeInvalidRepositoryURL, code, err)
			}
			if len(executor.recorded()) != 0 {
				t.Fatalf("仓库地址非法时不应执行任何容器命令: %v", executor.recorded())
			}
		})
	}
}

// TestInvalidTaskID 验证非法任务标识在容器操作之前就被拒绝。
func TestInvalidTaskID(t *testing.T) {
	t.Parallel()
	cases := []string{"", "Task-1", "../escape", "task 1", "-task"}
	for _, taskID := range cases {
		taskID := taskID
		t.Run(taskID, func(t *testing.T) {
			t.Parallel()
			executor := &fakeExecutor{}
			provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
			_, err := provider.Fetch(context.Background(), source.Request{
				TaskID:        taskID,
				ContainerID:   testContainerID,
				RepositoryURL: testRepository,
			})
			if err == nil {
				t.Fatalf("期望失败，实际成功")
			}
			if code := source.ErrorCodeOf(err); code != source.CodeInvalidArgument {
				t.Fatalf("期望错误类别 %s，实际 %s", source.CodeInvalidArgument, code)
			}
			if len(executor.recorded()) != 0 {
				t.Fatalf("任务标识非法时不应执行任何容器命令")
			}
		})
	}
}

// TestContainerGuardErrors 验证容器核验失败的错误分类。
func TestContainerGuardErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want source.ErrorCode
	}{
		{
			name: "容器不存在",
			err:  &docker.Error{Code: docker.CodeContainerNotFound, Op: "docker inspect"},
			want: source.CodeContainerNotFound,
		},
		{
			name: "容器已脱离管理",
			err:  &docker.Error{Code: docker.CodeDetached, Op: "container"},
			want: source.CodeContainerNotManaged,
		},
		{
			name: "状态不确定",
			err:  &docker.Error{Code: docker.CodeStateUncertain, Op: "docker inspect"},
			want: source.CodeStateUncertain,
		},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			t.Parallel()
			executor := &fakeExecutor{}
			provider := newProvider(t, executor, &fakeGuard{err: item.err})
			_, err := provider.Fetch(context.Background(), source.Request{
				TaskID:        testTaskID,
				ContainerID:   testContainerID,
				RepositoryURL: testRepository,
			})
			if err == nil {
				t.Fatalf("期望失败，实际成功")
			}
			if code := source.ErrorCodeOf(err); code != item.want {
				t.Fatalf("期望错误类别 %s，实际 %s（%v）", item.want, code, err)
			}
		})
	}
}

// TestDetachedContainerRejected 验证已脱离管理的容器不会被自动接管。
func TestDetachedContainerRejected(t *testing.T) {
	t.Parallel()
	container := newContainer()
	container.Detached = true
	container.State = domain.ContainerStateDetached
	executor := &fakeExecutor{}
	provider := newProvider(t, executor, &fakeGuard{container: container})
	_, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        testTaskID,
		ContainerID:   testContainerID,
		RepositoryURL: testRepository,
	})
	if code := source.ErrorCodeOf(err); code != source.CodeContainerNotManaged {
		t.Fatalf("期望错误类别 %s，实际 %s（%v）", source.CodeContainerNotManaged, code, err)
	}
	if len(executor.recorded()) != 0 {
		t.Fatalf("已脱离管理的容器不应执行任何命令")
	}
}

// TestGitUnavailable 验证容器内缺少 Git 时的错误分类。
func TestGitUnavailable(t *testing.T) {
	t.Parallel()
	executor := &fakeExecutor{responder: func(command []string) (docker.CommandResult, error) {
		if strings.Join(command, " ") == "git --version" {
			return exitCode(127, "sh: git: command not found\n"), nil
		}
		return docker.CommandResult{}, nil
	}}
	provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
	_, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        testTaskID,
		ContainerID:   testContainerID,
		RepositoryURL: testRepository,
	})
	if code := source.ErrorCodeOf(err); code != source.CodeGitUnavailable {
		t.Fatalf("期望错误类别 %s，实际 %s（%v）", source.CodeGitUnavailable, code, err)
	}
	if executor.contains(func(command []string) bool {
		return len(command) > 1 && command[1] == "git" && containsArg(command, "clone")
	}) {
		t.Fatalf("Git 不可用时不应执行 Clone")
	}
}

// TestTargetDirNotEmpty 验证目标目录非空时失败且不删除目录内容。
func TestTargetDirNotEmpty(t *testing.T) {
	t.Parallel()
	executor := &fakeExecutor{responder: func(command []string) (docker.CommandResult, error) {
		joined := strings.Join(command, " ")
		switch {
		case joined == "git --version":
			return succeeded("git version 2.43.0\n"), nil
		case strings.HasPrefix(joined, "sh -c test -e "):
			return succeeded(""), nil
		case strings.HasPrefix(joined, "sh -c ls -A "):
			return succeeded("src\nREADME.md\n"), nil
		}
		return docker.CommandResult{}, nil
	}}
	provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
	_, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        testTaskID,
		ContainerID:   testContainerID,
		RepositoryURL: testRepository,
	})
	if code := source.ErrorCodeOf(err); code != source.CodeTargetDirNotEmpty {
		t.Fatalf("期望错误类别 %s，实际 %s（%v）", source.CodeTargetDirNotEmpty, code, err)
	}
	// 失败路径上不允许出现任何删除命令。
	for _, command := range executor.recorded() {
		joined := strings.Join(command, " ")
		if strings.Contains(joined, "rm -rf") || strings.Contains(joined, "rm -r ") {
			t.Fatalf("不应在失败路径上删除目录: %v", command)
		}
	}
	if executor.contains(func(command []string) bool { return containsArg(command, "clone") }) {
		t.Fatalf("目标目录非空时不应执行 Clone")
	}
}

// TestBaseBranchSelectionPriority 验证基础分支识别优先级。
func TestBaseBranchSelectionPriority(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		originHead string
		branches   []string
		wantBranch string
		wantSelect domain.GitBranchSelection
	}{
		{
			name:       "origin HEAD 优先",
			originHead: "origin/develop",
			branches:   []string{"main", "master", "develop"},
			wantBranch: "develop",
			wantSelect: domain.GitBranchSelectionOriginHead,
		},
		{
			name:       "回退到 main",
			originHead: "",
			branches:   []string{"main", "master"},
			wantBranch: "main",
			wantSelect: domain.GitBranchSelectionMain,
		},
		{
			name:       "回退到 master",
			originHead: "",
			branches:   []string{"master"},
			wantBranch: "master",
			wantSelect: domain.GitBranchSelectionMaster,
		},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			t.Parallel()
			executor := &fakeExecutor{
				responder: standardResponder(item.originHead, item.branches, false),
			}
			provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
			result, err := provider.Fetch(context.Background(), source.Request{
				TaskID:        testTaskID,
				ContainerID:   testContainerID,
				RepositoryURL: testRepository,
			})
			if err != nil {
				t.Fatalf("代码获取失败: %v", err)
			}
			if result.BaseBranch != item.wantBranch {
				t.Fatalf("期望基础分支 %q，实际 %q", item.wantBranch, result.BaseBranch)
			}
			if result.BaseSelection != item.wantSelect {
				t.Fatalf("期望识别方式 %q，实际 %q", item.wantSelect, result.BaseSelection)
			}
		})
	}
}

// TestBaseBranchUnrecognized 验证无法识别基础分支时返回明确错误。
func TestBaseBranchUnrecognized(t *testing.T) {
	t.Parallel()
	executor := &fakeExecutor{responder: standardResponder("", []string{"develop"}, false)}
	provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
	_, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        testTaskID,
		ContainerID:   testContainerID,
		RepositoryURL: testRepository,
	})
	if code := source.ErrorCodeOf(err); code != source.CodeBaseBranchMissing {
		t.Fatalf("期望错误类别 %s，实际 %s（%v）", source.CodeBaseBranchMissing, code, err)
	}
	if !strings.Contains(err.Error(), "显式指定基础分支") {
		t.Fatalf("错误信息应提示显式指定基础分支: %v", err)
	}
}

// TestExplicitBaseBranch 验证显式基础分支优先于自动识别。
func TestExplicitBaseBranch(t *testing.T) {
	t.Parallel()
	executor := &fakeExecutor{responder: standardResponder("origin/main", []string{"main", "release"}, false)}
	provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
	result, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        testTaskID,
		ContainerID:   testContainerID,
		RepositoryURL: testRepository,
		BaseBranch:    "refs/remotes/origin/release",
	})
	if err != nil {
		t.Fatalf("代码获取失败: %v", err)
	}
	if result.BaseBranch != "release" {
		t.Fatalf("期望基础分支 release，实际 %q", result.BaseBranch)
	}
	if result.BaseSelection != domain.GitBranchSelectionExplicit {
		t.Fatalf("期望识别方式 explicit，实际 %q", result.BaseSelection)
	}
}

// TestExplicitBaseBranchMissing 验证显式基础分支不存在时失败。
func TestExplicitBaseBranchMissing(t *testing.T) {
	t.Parallel()
	executor := &fakeExecutor{responder: standardResponder("origin/main", []string{"main"}, false)}
	provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
	_, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        testTaskID,
		ContainerID:   testContainerID,
		RepositoryURL: testRepository,
		BaseBranch:    "release",
	})
	if code := source.ErrorCodeOf(err); code != source.CodeBaseBranchMissing {
		t.Fatalf("期望错误类别 %s，实际 %s（%v）", source.CodeBaseBranchMissing, code, err)
	}
}

// TestCreateTaskBranch 验证任务分支名与创建参数。
func TestCreateTaskBranch(t *testing.T) {
	t.Parallel()
	executor := &fakeExecutor{responder: standardResponder("origin/main", []string{"main"}, false)}
	provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
	result, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        testTaskID,
		ContainerID:   testContainerID,
		RepositoryURL: testRepository,
	})
	if err != nil {
		t.Fatalf("代码获取失败: %v", err)
	}
	if result.TaskBranch != "agent/"+testTaskID {
		t.Fatalf("期望任务分支 agent/%s，实际 %q", testTaskID, result.TaskBranch)
	}
	if result.TaskBranchSource != source.TaskBranchSourceNew {
		t.Fatalf("期望分支来源 %q，实际 %q", source.TaskBranchSourceNew, result.TaskBranchSource)
	}
	checkout := findCommand(executor.recorded(), func(command []string) bool {
		return containsArg(command, "checkout")
	})
	if checkout == nil {
		t.Fatalf("没有收到 checkout 命令: %v", executor.recorded())
	}
	if !containsArg(checkout, "agent/"+testTaskID) {
		t.Fatalf("checkout 命令缺少任务分支: %v", checkout)
	}
	if containsArg(checkout, "--force") || containsArg(checkout, "-f") {
		t.Fatalf("不允许强制重置分支: %v", checkout)
	}
}

// TestRemoteTaskBranchExists 验证远程已存在任务分支时检出该分支。
func TestRemoteTaskBranchExists(t *testing.T) {
	t.Parallel()
	executor := &fakeExecutor{responder: standardResponder("origin/main", []string{"main"}, true)}
	provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
	result, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        testTaskID,
		ContainerID:   testContainerID,
		RepositoryURL: testRepository,
	})
	if err != nil {
		t.Fatalf("代码获取失败: %v", err)
	}
	if result.TaskBranchSource != source.TaskBranchSourceRemote {
		t.Fatalf("期望分支来源 %q，实际 %q", source.TaskBranchSourceRemote, result.TaskBranchSource)
	}
	checkout := findCommand(executor.recorded(), func(command []string) bool {
		return containsArg(command, "checkout")
	})
	if !containsArg(checkout, "--track") {
		t.Fatalf("检出远程任务分支时应建立跟踪: %v", checkout)
	}
}

// TestCloneFailureClassification 验证 Clone 失败的错误分类。
func TestCloneFailureClassification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		stderr string
		want   source.ErrorCode
	}{
		{
			name:   "缺少认证",
			stderr: "fatal: could not read Username for 'https://git.example.com': terminal prompts disabled\n",
			want:   source.CodeRepositoryAuthRequired,
		},
		{
			name:   "无法解析主机",
			stderr: "fatal: unable to access: Could not resolve host: git.example.com\n",
			want:   source.CodeRepositoryAccessFailed,
		},
		{
			name:   "仓库不存在",
			stderr: "remote: Repository not found.\nfatal: repository 'https://git.example.com/group/repo.git/' not found\n",
			want:   source.CodeRepositoryAuthRequired,
		},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			t.Parallel()
			base := standardResponder("origin/main", []string{"main"}, false)
			executor := &fakeExecutor{responder: func(command []string) (docker.CommandResult, error) {
				if containsArg(command, "clone") {
					return exitCode(128, item.stderr), nil
				}
				return base(command)
			}}
			provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
			_, err := provider.Fetch(context.Background(), source.Request{
				TaskID:        testTaskID,
				ContainerID:   testContainerID,
				RepositoryURL: testRepository,
			})
			if code := source.ErrorCodeOf(err); code != item.want {
				t.Fatalf("期望错误类别 %s，实际 %s（%v）", item.want, code, err)
			}
			// 失败时不允许删除目标目录。
			if executor.contains(func(command []string) bool {
				return containsArg(command, "rm")
			}) {
				t.Fatalf("Clone 失败时不应删除目标目录")
			}
		})
	}
}

// TestCloneCancelledAndTimeout 验证取消与超时的错误分类。
func TestCloneCancelledAndTimeout(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		result docker.CommandResult
		err    error
		want   source.ErrorCode
	}{
		{
			name: "取消",
			result: docker.CommandResult{
				ExitCode: -1,
				Status:   dockerStatusCancelled(),
				Certain:  false,
			},
			err:  context.Canceled,
			want: source.CodeCloneInterrupted,
		},
		{
			name: "超时",
			result: docker.CommandResult{
				ExitCode: -1,
				Status:   dockerStatusTimeout(),
				Certain:  false,
			},
			err:  context.DeadlineExceeded,
			want: source.CodeCloneTimeout,
		},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			t.Parallel()
			base := standardResponder("origin/main", []string{"main"}, false)
			executor := &fakeExecutor{responder: func(command []string) (docker.CommandResult, error) {
				if containsArg(command, "clone") {
					return item.result, item.err
				}
				return base(command)
			}}
			provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
			_, err := provider.Fetch(context.Background(), source.Request{
				TaskID:        testTaskID,
				ContainerID:   testContainerID,
				RepositoryURL: testRepository,
			})
			if code := source.ErrorCodeOf(err); code != item.want {
				t.Fatalf("期望错误类别 %s，实际 %s（%v）", item.want, code, err)
			}
		})
	}
}

// TestUncertainResultClassification 验证结果不确定时返回可诊断类别。
func TestUncertainResultClassification(t *testing.T) {
	t.Parallel()
	base := standardResponder("origin/main", []string{"main"}, false)
	executor := &fakeExecutor{responder: func(command []string) (docker.CommandResult, error) {
		if containsArg(command, "clone") {
			return docker.CommandResult{
				ExitCode: -1,
				Status:   dockerStatusConnectionError(),
				Certain:  false,
			}, nil
		}
		return base(command)
	}}
	provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
	_, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        testTaskID,
		ContainerID:   testContainerID,
		RepositoryURL: testRepository,
	})
	if code := source.ErrorCodeOf(err); code != source.CodeStateUncertain {
		t.Fatalf("期望错误类别 %s，实际 %s（%v）", source.CodeStateUncertain, code, err)
	}
}

// TestBranchInitFailure 验证 Clone 成功但分支初始化失败时整体失败。
func TestBranchInitFailure(t *testing.T) {
	t.Parallel()
	base := standardResponder("origin/main", []string{"main"}, false)
	executor := &fakeExecutor{responder: func(command []string) (docker.CommandResult, error) {
		if containsArg(command, "checkout") {
			return exitCode(1, "fatal: a branch named 'agent/task-1' already exists\n"), nil
		}
		return base(command)
	}}
	provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
	result, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        testTaskID,
		ContainerID:   testContainerID,
		RepositoryURL: testRepository,
	})
	if err == nil {
		t.Fatalf("期望失败，实际成功")
	}
	if code := source.ErrorCodeOf(err); code != source.CodeTaskBranchConflict {
		t.Fatalf("期望错误类别 %s，实际 %s（%v）", source.CodeTaskBranchConflict, code, err)
	}
	// Clone 已经成功，结果中仍应保留容器内目录信息，便于诊断。
	if result.WorkDir != domain.DefaultContainerWorkDir {
		t.Fatalf("失败结果应保留容器内目录，实际 %q", result.WorkDir)
	}
	if result.BaseBranch != "main" {
		t.Fatalf("失败结果应保留已识别的基础分支，实际 %q", result.BaseBranch)
	}
}

// TestWorktreeVerificationFailure 验证工作区校验失败时整体失败。
func TestWorktreeVerificationFailure(t *testing.T) {
	t.Parallel()
	base := standardResponder("origin/main", []string{"main"}, false)
	executor := &fakeExecutor{responder: func(command []string) (docker.CommandResult, error) {
		if strings.HasPrefix(strings.Join(command, " "), "sh -c test -d ") {
			return exitCode(1, ""), nil
		}
		return base(command)
	}}
	provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
	_, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        testTaskID,
		ContainerID:   testContainerID,
		RepositoryURL: testRepository,
	})
	if code := source.ErrorCodeOf(err); code != source.CodeWorktreeInvalid {
		t.Fatalf("期望错误类别 %s，实际 %s（%v）", source.CodeWorktreeInvalid, code, err)
	}
}

// TestBranchMismatchDetected 验证当前分支与约定任务分支不一致时失败。
func TestBranchMismatchDetected(t *testing.T) {
	t.Parallel()
	base := standardResponder("origin/main", []string{"main"}, false)
	executor := &fakeExecutor{responder: func(command []string) (docker.CommandResult, error) {
		if containsArg(command, "--abbrev-ref") {
			return succeeded("main\n"), nil
		}
		return base(command)
	}}
	provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
	_, err := provider.Fetch(context.Background(), source.Request{
		TaskID:        testTaskID,
		ContainerID:   testContainerID,
		RepositoryURL: testRepository,
	})
	if code := source.ErrorCodeOf(err); code != source.CodeWorktreeInvalid {
		t.Fatalf("期望错误类别 %s，实际 %s（%v）", source.CodeWorktreeInvalid, code, err)
	}
}

// TestConcurrentFetchesUseDistinctWorkDirs 验证并发调用不会共用目标目录。
func TestConcurrentFetchesUseDistinctWorkDirs(t *testing.T) {
	t.Parallel()
	root := testkit.New(t)
	type outcome struct {
		result source.Result
		err    error
	}
	results := make(chan outcome, 4)
	for index := 0; index < 4; index++ {
		index := index
		go func() {
			taskID := "task-" + string(rune('a'+index))
			executor := &fakeExecutor{responder: taskResponder(taskID, "origin/main", []string{"main"}, false)}
			provider, err := source.NewProvider(source.Options{
				Executor:     executor,
				Guard:        &fakeGuard{container: newContainer()},
				CloneTimeout: 10 * time.Second,
				ProbeTimeout: 5 * time.Second,
			})
			if err != nil {
				results <- outcome{err: err}
				return
			}
			workDir := domain.DefaultContainerWorkDir + "/" + taskID
			result, fetchErr := provider.Fetch(context.Background(), source.Request{
				TaskID:        taskID,
				ContainerID:   testContainerID,
				RepositoryURL: testRepository,
				WorkDir:       workDir,
			})
			results <- outcome{result: result, err: fetchErr}
		}()
	}
	seen := make(map[string]struct{}, 4)
	for index := 0; index < 4; index++ {
		item := <-results
		if item.err != nil {
			t.Fatalf("并发代码获取失败: %v", item.err)
		}
		if _, ok := seen[item.result.WorkDir]; ok {
			t.Fatalf("并发调用使用了相同目标目录: %s", item.result.WorkDir)
		}
		seen[item.result.WorkDir] = struct{}{}
	}
	if len(seen) != 4 {
		t.Fatalf("期望 4 个不同目标目录，实际 %d", len(seen))
	}
	_ = root
}

// TestCancelledContextRejected 验证调用前取消直接返回取消状态。
func TestCancelledContextRejected(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor := &fakeExecutor{}
	provider := newProvider(t, executor, &fakeGuard{container: newContainer()})
	_, err := provider.Fetch(ctx, source.Request{
		TaskID:        testTaskID,
		ContainerID:   testContainerID,
		RepositoryURL: testRepository,
	})
	if err == nil {
		t.Fatalf("期望失败，实际成功")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望上下文取消错误，实际 %v", err)
	}
}

// containsArg 判断命令是否包含指定参数。
func containsArg(command []string, want string) bool {
	for _, arg := range command {
		if arg == want {
			return true
		}
	}
	return false
}

// findCommand 返回第一条满足条件的命令。
func findCommand(commands [][]string, match func(command []string) bool) []string {
	for _, command := range commands {
		if match(command) {
			return command
		}
	}
	return nil
}
