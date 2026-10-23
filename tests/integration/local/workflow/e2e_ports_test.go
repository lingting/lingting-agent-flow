// Package workflow_test 验证单任务端到端链路在真实持久化现场上的完整成立。
//
// 本文件提供 P1-M5 端到端验收所需的受控执行端口：它同时满足容器核验、容器内
// Agent 调用、容器内命令执行、代码获取与 Git 工作区状态采集五类能力，并把"容器内
// 命令"映射到测试工作空间中的真实 Git 仓库执行。
//
// 边界说明：这里替换的是外部依赖（远程 SSH、Docker、容器内 Pi 进程与远程 Git
// 副作用），不替换任何业务判定。计划生成、开发执行、确定性验证、语义审查、修复
// 循环、代码保存与任务收尾全部走生产代码路径。
package workflow_test

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

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/source"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 端到端验收使用的固定标识。
const (
	e2eContainerID = "container-e2e"
	e2eDockerID    = "docker-container-e2e"
	e2eHostID      = "host-e2e"
	e2eBaseBranch  = "main"
	e2eRemoteName  = "origin"
	// e2eRepositoryURL 是工作仓库登记的远程地址。
	//
	// 代码获取与代码保存都只接受 ssh:// 或 https:// 形式且不含内嵌凭据的地址，
	// 因此这里使用 https 形式；执行端口再把它映射为工作空间内的 bare 仓库，
	// 保证测试不访问公网。
	e2eRepositoryURL = "https://git.example.com/team/repo.git"
	// e2eAuthorName / e2eAuthorEmail 是真实 Git 提交使用的身份。
	e2eAuthorName  = "AgentFlow Tester"
	e2eAuthorEmail = "agentflow@example.com"
	// e2eGitTimeout 是单条容器内 Git 命令的超时。
	e2eGitTimeout = 2 * time.Minute
)

// e2eCheckReply 是一条受控的容器内确定性检查结果。
type e2eCheckReply struct {
	exitCode int
	stdout   string
	stderr   string
	started  bool
	certain  bool
	err      error
}

// e2eAgentReply 是一次受控 Agent 调用的结果。
//
// 端到端用例的 Agent 调用既包含 Developer，也包含 Validator：两者的区别只体现在
// 提示词上，因此替身按调用顺序返回文本，由用例保证顺序。
type e2eAgentReply struct {
	status       agent.RunStatus
	finalText    string
	failure      string
	completeness agent.SessionCompleteness
	err          error
	// duringCall 在返回结果之前被调用，用于在"调用进行中"触发外部动作。
	//
	// 它在独立 goroutine 中执行：调用方此时仍在 RunAgent 内部，同步等待会死锁。
	duringCall func()
}

// e2eRepo 是端到端用例使用的一组真实 Git 仓库。
//
// 端到端链路要求"Developer 的代码改动"能真实地变成提交并推送，因此容器内命令
// 必须落在真实仓库上；否则代码保存记录只能由替身伪造，无法证明保存链路成立。
type e2eRepo struct {
	// bare 是 bare 远程仓库路径。
	bare string
	// work 是工作仓库路径。
	work string
	// gitPath 是本机 Git 可执行文件。
	gitPath string
	// workDir 是请求中使用的容器内工作目录。
	workDir string
}

// newE2ERepo 在工作空间中创建 bare 仓库、基础分支与任务分支。
func newE2ERepo(t *testing.T, taskID domain.ID) *e2eRepo {
	t.Helper()
	gitPath := requireE2EGit(t)
	root := testkit.Dir(t)
	bare := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	repo := &e2eRepo{bare: bare, work: work, gitPath: gitPath, workDir: domain.DefaultContainerWorkDir}
	repo.git(t, root, "init", "--bare", "--initial-branch="+e2eBaseBranch, bare)
	repo.git(t, root, "init", "--initial-branch="+e2eBaseBranch, work)
	repo.git(t, work, "config", "user.name", e2eAuthorName)
	repo.git(t, work, "config", "user.email", e2eAuthorEmail)
	repo.git(t, work, "remote", "add", e2eRemoteName, bare)
	repo.writeFile(t, "README.md", "初始内容\n")
	repo.git(t, work, "add", "--all")
	repo.git(t, work, "commit", "-m", "初始提交")
	repo.git(t, work, "push", e2eRemoteName, e2eBaseBranch)
	repo.git(t, work, "remote", "set-head", e2eRemoteName, e2eBaseBranch)
	repo.git(t, work, "checkout", "-b", "agent/"+taskID.String())
	// 代码获取与代码保存只接受符合仓库访问策略的远程地址，因此把地址改写为
	// https 形式；执行端口再把远程名称映射回本机 bare 仓库。
	repo.git(t, work, "remote", "set-url", e2eRemoteName, e2eRepositoryURL)
	return repo
}

// requireE2EGit 检查本机 Git 是否可用，缺失时输出警告并跳过。
func requireE2EGit(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		reason := "WARN [SKIP] workflow e2e: 本机 PATH 中没有 git，无法执行真实 Git 链路。"
		t.Log(reason)
		t.Skip(reason)
	}
	return path
}

// git 在指定目录执行 Git 命令并返回输出。
func (r *e2eRepo) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	argv := append([]string{"-C", dir}, args...)
	cmd := exec.Command(r.gitPath, argv...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+e2eAuthorName,
		"GIT_AUTHOR_EMAIL="+e2eAuthorEmail,
		"GIT_COMMITTER_NAME="+e2eAuthorName,
		"GIT_COMMITTER_EMAIL="+e2eAuthorEmail,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("执行 git %s 失败: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

// writeFile 在工作仓库写入文件。
func (r *e2eRepo) writeFile(t *testing.T, name, content string) {
	t.Helper()
	path := filepath.Join(r.work, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入文件失败: %v", err)
	}
}

// rev 读取指定引用的 commit SHA。
func (r *e2eRepo) rev(t *testing.T, ref string) string {
	t.Helper()
	return strings.TrimSpace(r.git(t, r.work, "rev-parse", ref))
}

// bareRev 读取 bare 仓库中指定引用的 commit SHA。
func (r *e2eRepo) bareRev(t *testing.T, ref string) string {
	t.Helper()
	return strings.TrimSpace(r.git(t, r.bare, "rev-parse", ref))
}

// e2ePorts 同时满足容器核验、容器内 Agent 调用、容器内命令执行、代码获取与
// Git 工作区状态采集五类能力。
//
// 它不写数据库：会话记录由真实的 app.SessionService 写入，执行记录由真实的编排器
// 写入，检查结果由真实的验证引擎写入。替身只负责返回受控的外部结果，并按调用顺序
// 记录请求，使"是否重复 Clone""是否真的进入第二轮验证"这类断言可以直接比较次数。
type e2ePorts struct {
	mu sync.Mutex
	// repo 是真实 Git 仓库，用于承载容器内命令。
	repo *e2eRepo
	// running 表示容器核验结论中的运行状态。
	running bool
	// inspectCalls 统计容器核验次数。
	inspectCalls int
	// fetchCalls 统计任务级代码获取次数。
	fetchCalls int
	// agentReplies 按调用顺序返回的 Agent 结果。
	agentReplies []e2eAgentReply
	// agentRequests 记录收到的全部 Agent 调用请求。
	agentRequests []app.ContainerAgentRequest
	// checkSequence 按调用顺序返回确定性检查结果；为空时全部通过。
	checkSequence []e2eCheckReply
	// checkRequests 记录收到的检查命令请求。
	checkRequests []app.ExecRequest
	// gitInspectErr 使 Git 状态采集失败。
	gitInspectErr error
	// pushCalls 统计真实 Git 推送命令次数。
	pushCalls int
	// pushUncertain 为 true 时推送命令返回"结果无法确认"，且不真正执行推送。
	//
	// 它对应真实世界里"命令超时、远程可能已经接受推送"的现场：外部副作用没有
	// 被确认发生，因此必须由代码保存按 unknown 处理，而不是假设成功或失败。
	pushUncertain bool
}

// InspectContainer 返回受控的容器状态。
func (p *e2ePorts) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inspectCalls++
	return app.ContainerDetail{
		Container: domain.Container{
			ID:       domain.ID(request.ContainerID),
			HostID:   e2eHostID,
			Name:     "agentflow-e2e",
			DockerID: e2eDockerID,
			State:    domain.ContainerStateRunning,
		},
		Inspect: &docker.ContainerInspect{Running: p.running},
	}, nil
}

// FetchSourceWith 返回一次受控的代码获取结果。
//
// 端到端链路中的"任务级工作区初始化"必须留下真实的工作区记录，但不需要真的再
// Clone 一次：工作仓库已经由用例按任务分支创建好，因此这里返回与之一致的
// commit，并记录调用次数用于断言"不重复 Clone"。
func (p *e2ePorts) FetchSourceWith(
	ctx context.Context,
	containerID string,
	request source.FetchRequest,
) (source.FetchResult, error) {
	p.mu.Lock()
	p.fetchCalls++
	repo := p.repo
	p.mu.Unlock()
	now := time.Now().UTC()
	baseCommit := repo.bareRevInternal()
	head := repo.headInternal()
	return source.FetchResult{
		ProjectID:        request.ProjectID,
		TaskID:           request.TaskID,
		SourceType:       domain.CodeSourceGit,
		Repository:       e2eRepositoryURL,
		ContainerID:      containerID,
		DockerID:         e2eDockerID,
		WorkDir:          request.WorkDir,
		BaseBranch:       e2eBaseBranch,
		BaseSelection:    domain.GitBranchSelectionOriginHead,
		BaseCommit:       baseCommit,
		TaskBranch:       "agent/" + request.TaskID,
		TaskBranchSource: domain.TaskBranchSourceNew,
		Commit:           head,
		Stage:            domain.CodeFetchStageVerify,
		StartedAt:        now,
		FinishedAt:       now,
	}, nil
}

// RunAgent 返回受控的 Agent 调用结果。
//
// 它刻意复用调用方给出的 SessionID：真实执行端口必须让 Session 文件与数据库记录
// 指向同一次会话，替身同样不得自行生成标识。
func (p *e2ePorts) RunAgent(
	ctx context.Context,
	request app.ContainerAgentRequest,
) (app.ContainerAgentResult, error) {
	p.mu.Lock()
	index := len(p.agentRequests)
	p.agentRequests = append(p.agentRequests, request)
	reply := e2eAgentReply{status: agent.RunCompleted, finalText: "已完成"}
	if index < len(p.agentReplies) {
		reply = p.agentReplies[index]
	}
	p.mu.Unlock()
	if reply.duringCall != nil {
		go reply.duringCall()
	}
	now := time.Now().UTC()
	sessionID := domain.ID(request.SessionID)
	if sessionID.IsEmpty() {
		return app.ContainerAgentResult{}, errors.New("执行端口必须复用调用方给出的会话标识")
	}
	completeness := reply.completeness
	if completeness == "" {
		if reply.status == agent.RunCompleted {
			completeness = agent.SessionComplete
		} else {
			completeness = agent.SessionPartial
		}
	}
	result := agent.RunResult{
		SessionID:    sessionID.String(),
		InvocationID: request.InvocationID,
		Status:       reply.status,
		FinalText:    reply.finalText,
		Failure:      reply.failure,
		ExitCode:     0,
		StartedAt:    now,
		FinishedAt:   now,
		Session: agent.SessionFiles{
			JSONLPath:    "sessions/" + sessionID.String() + "/session.jsonl",
			Completeness: completeness,
			ExportStatus: agent.ExportNotAttempted,
		},
	}
	return app.ContainerAgentResult{Result: result}, reply.err
}

// Exec 执行一次容器内命令。
//
// 三类命令分别处理，使 checkSequence 只对应真正的确定性验证：
//   - 代码保存与 Git 状态采集的 `git -C ...` 在真实工作仓库上执行，提交、推送与
//     远程引用核验都经过真实 Git，不产生虚假的保存记录；
//   - 执行环境准备阶段的可用性探针（工作目录可写、git / pi 版本）固定成功，
//     它们不是验证检查，不能占用检查结果序列；
//   - 其余命令（任务配置的必需检查，例如 go build）按 checkSequence 返回受控结果。
func (p *e2ePorts) Exec(
	ctx context.Context,
	request app.ExecRequest,
) (app.ExecResult, error) {
	if isE2EGitCommand(request.Command) {
		return p.execGit(ctx, request)
	}
	if isE2EProbeCommand(request.Command) {
		return e2eProbeResult(), nil
	}
	return p.execCheck(request)
}

// isE2EProbeCommand 判断容器内命令是否是执行环境准备阶段的可用性探针。
//
// 探针由 app.ExecutionEnvironmentService 的容器核验产生：工作目录可写检查使用
// `sh -c`，git 与 pi 使用 `--version`。它们与验证检查的语义完全不同，必须分开。
func isE2EProbeCommand(command []string) bool {
	if len(command) == 0 {
		return false
	}
	switch command[0] {
	case "sh", "bash":
		return true
	}
	for _, arg := range command[1:] {
		if arg == "--version" {
			return true
		}
	}
	return false
}

// e2eProbeResult 返回一条成功探针结果。
func e2eProbeResult() app.ExecResult {
	return app.ExecResult{Result: docker.CommandResult{
		ExitCode: 0,
		Stdout:   "probe ok",
		Status:   sshclient.CommandStatusSucceeded,
		Certain:  true,
		Started:  true,
		Duration: time.Millisecond,
	}}
}

// execCheck 返回受控的确定性检查结果。
func (p *e2ePorts) execCheck(request app.ExecRequest) (app.ExecResult, error) {
	p.mu.Lock()
	p.checkRequests = append(p.checkRequests, request)
	index := len(p.checkRequests) - 1
	reply := e2eCheckReply{exitCode: 0, stdout: "ok", started: true, certain: true}
	if index < len(p.checkSequence) {
		reply = p.checkSequence[index]
	}
	p.mu.Unlock()
	status := sshclient.CommandStatusSucceeded
	switch {
	case reply.err != nil && !reply.started:
		status = sshclient.CommandStatusConnectionError
	case reply.exitCode != 0:
		status = sshclient.CommandStatusExitError
	}
	certain := reply.certain
	started := reply.started
	if status == sshclient.CommandStatusSucceeded || status == sshclient.CommandStatusExitError {
		certain = true
		started = true
	}
	return app.ExecResult{Result: docker.CommandResult{
		ExitCode: reply.exitCode,
		Stdout:   reply.stdout,
		Stderr:   reply.stderr,
		Status:   status,
		Certain:  certain,
		Started:  started,
		Duration: time.Millisecond,
	}}, reply.err
}

// execGit 在真实工作仓库上执行一条 Git 命令。
//
// 它把容器内路径与远程名称改写为本机真实路径：请求中的 /workspace/project 映射为
// 工作仓库，origin 映射为 bare 仓库，因此不会访问公网。
func (p *e2ePorts) execGit(
	ctx context.Context,
	request app.ExecRequest,
) (app.ExecResult, error) {
	p.mu.Lock()
	repo := p.repo
	p.mu.Unlock()
	if repo == nil {
		return app.ExecResult{}, errors.New("测试执行端口没有关联真实仓库")
	}
	if isE2EGitPush(request.Command) {
		p.mu.Lock()
		p.pushCalls++
		uncertain := p.pushUncertain
		p.mu.Unlock()
		if uncertain {
			return app.ExecResult{Result: docker.CommandResult{
				ExitCode: -1,
				Stderr:   "推送命令超时，无法确认远程是否已接受该提交",
				Status:   sshclient.CommandStatusTimeout,
				Certain:  false,
				Started:  true,
				Duration: e2eGitTimeout,
			}}, nil
		}
	}
	argv := rewriteE2EGitArgs(repo, request.Command)
	runCtx, cancel := context.WithTimeout(ctx, e2eGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = repo.work
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+e2eAuthorName,
		"GIT_AUTHOR_EMAIL="+e2eAuthorEmail,
		"GIT_COMMITTER_NAME="+e2eAuthorName,
		"GIT_COMMITTER_EMAIL="+e2eAuthorEmail,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	output, err := cmd.CombinedOutput()
	result := docker.CommandResult{
		Stdout:   mapE2EGitOutput(repo, string(output)),
		Status:   sshclient.CommandStatusSucceeded,
		Certain:  true,
		Started:  true,
		Duration: time.Millisecond,
	}
	if err != nil {
		result.Status = sshclient.CommandStatusExitError
		result.ExitCode = e2eExitCodeOf(err)
	}
	return app.ExecResult{Result: result}, nil
}

// InspectWorktree 返回真实工作仓库的当前状态。
//
// 它直接读取真实仓库，因此 Attempt 上记录的"执行前后 Git 状态"来自事实而不是
// 零值；采集失败时按契约返回 StatusRead=false，不使用零值冒充干净工作区。
func (p *e2ePorts) InspectWorktree(
	ctx context.Context,
	request app.GitWorktreeRequest,
) (domain.GitWorktreeState, error) {
	p.mu.Lock()
	repo := p.repo
	inspectErr := p.gitInspectErr
	p.mu.Unlock()
	if inspectErr != nil {
		return domain.GitWorktreeState{}, inspectErr
	}
	if repo == nil {
		return domain.GitWorktreeState{}, errors.New("测试执行端口没有关联真实仓库")
	}
	return repo.worktreeState(), nil
}

// worktreeState 读取工作仓库的分支、HEAD 与变更计数。
func (r *e2eRepo) worktreeState() domain.GitWorktreeState {
	head := r.headInternal()
	branch := r.branchInternal()
	status := r.statusInternal()
	state := domain.GitWorktreeState{
		StatusRead: true,
		Branch:     branch,
		Head:       head,
		Dirty:      status != "",
	}
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'M':
			state.Modified++
		case 'A':
			state.Added++
		case 'D':
			state.Deleted++
		case 'R':
			state.Renamed++
		case 'U':
			state.Conflicts++
		}
	}
	return state
}

// headInternal 读取 HEAD 指向的 commit，失败时返回空值。
func (r *e2eRepo) headInternal() string {
	return r.gitOutput("rev-parse", "HEAD")
}

// branchInternal 读取当前分支名，失败时返回空值。
func (r *e2eRepo) branchInternal() string {
	return r.gitOutput("rev-parse", "--abbrev-ref", "HEAD")
}

// statusInternal 读取工作区状态，失败时返回空值。
func (r *e2eRepo) statusInternal() string {
	return r.gitOutput("status", "--porcelain=v1")
}

// bareRevInternal 读取 bare 仓库基础分支的 commit，失败时返回空值。
func (r *e2eRepo) bareRevInternal() string {
	cmd := exec.Command(r.gitPath, "-C", r.bare, "rev-parse", "--verify", "--quiet", "refs/heads/"+e2eBaseBranch)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// gitOutput 在工作仓库执行一条只读 Git 命令并返回输出。
func (r *e2eRepo) gitOutput(args ...string) string {
	argv := append([]string{"-C", r.work}, args...)
	cmd := exec.Command(r.gitPath, argv...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// isE2EGitCommand 判断容器内命令是否是 Git 命令。
//
// 代码保存与 Git 状态采集都通过 `git -C <dir> ...` 表达，确定性检查则使用项目
// 自己的命令（例如 go build），两者可以据此区分，不需要在业务代码中新增开关。
func isE2EGitCommand(command []string) bool {
	if len(command) < 3 {
		return false
	}
	return command[0] == "git" && command[1] == "-C"
}

// rewriteE2EGitArgs 把容器内路径与远程名称改写为本机真实路径。
func rewriteE2EGitArgs(repo *e2eRepo, command []string) []string {
	rewritten := append([]string(nil), command...)
	for index, arg := range rewritten {
		if index == 0 {
			rewritten[index] = repo.gitPath
			continue
		}
		if arg == repo.workDir {
			rewritten[index] = repo.work
			continue
		}
		// 命令形如 git -C <dir> <subcommand> ...：子命令在索引 3，只有需要访问
		// 远程的子命令才把远程名称改写为工作空间内的 bare 仓库。
		if index < 3 {
			continue
		}
		switch rewritten[3] {
		case "push", "fetch", "ls-remote", "pull":
			if arg == e2eRemoteName {
				rewritten[index] = repo.bare
			}
		}
	}
	return rewritten
}

// isE2EGitPush 判断容器内命令是否为 Git 推送。
func isE2EGitPush(command []string) bool {
	return len(command) > 3 && command[0] == "git" && command[1] == "-C" && command[3] == "push"
}

// gitPushCount 返回真实 Git 推送命令的执行次数。
//
// 结果不确定时禁止重放外部副作用，因此"推送只发生一次"必须可以从调用次数验证。
func (p *e2ePorts) gitPushCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pushCalls
}

// mapE2EGitOutput 把本机路径还原为容器内路径。
//
// 代码保存会比对 `rev-parse --show-toplevel` 与请求中的容器内目录，因此执行端口
// 必须把本机真实路径映射回容器路径，否则会被判定为"不是仓库根目录"。
func mapE2EGitOutput(repo *e2eRepo, value string) string {
	mapped := strings.ReplaceAll(value, filepath.ToSlash(repo.work), repo.workDir)
	return strings.ReplaceAll(mapped, repo.work, repo.workDir)
}

// e2eExitCodeOf 提取命令退出码。
func e2eExitCodeOf(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// agentPrompts 返回收到的全部 Agent 提示词。
func (p *e2ePorts) agentPrompts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	prompts := make([]string, 0, len(p.agentRequests))
	for _, request := range p.agentRequests {
		prompts = append(prompts, request.Prompt)
	}
	return prompts
}

// counts 返回三类调用的次数快照。
func (p *e2ePorts) counts() (inspect, fetch, agentCalls, checkCalls int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inspectCalls, p.fetchCalls, len(p.agentRequests), len(p.checkRequests)
}

// setPushUncertain 设置推送命令是否返回"结果无法确认"。
func (p *e2ePorts) setPushUncertain(uncertain bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pushUncertain = uncertain
}

// setGitInspectErr 让 Git 状态采集失败。
func (p *e2ePorts) setGitInspectErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gitInspectErr = err
}

// e2eContainerExecutor 把代码保存组件的容器内命令适配到 e2ePorts。
//
// codesaver.ContainerExecutor 只暴露"在容器内执行一条命令"，因此这里把它转换为
// 同一份 app.ExecRequest 走同一条执行路径：真实 Git 仓库、同一套路径改写规则，
// 不出现第二套命令构造。
type e2eContainerExecutor struct {
	ports *e2ePorts
}

// ExecInContainer 在容器内执行一条命令。
func (e e2eContainerExecutor) ExecInContainer(
	ctx context.Context,
	dockerID string,
	command []string,
	timeout time.Duration,
) (docker.CommandResult, error) {
	result, err := e.ports.Exec(ctx, app.ExecRequest{
		ContainerID: e2eContainerID,
		Command:     command,
		WorkDir:     domain.DefaultContainerWorkDir,
		Timeout:     timeout,
	})
	return result.Result, err
}
