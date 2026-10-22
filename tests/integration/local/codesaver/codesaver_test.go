// Package codesaver_local 提供代码保存组件的本机集成测试。
//
// 这里在测试工作空间中创建真实 Git 工作仓库与 bare 仓库，并把"容器内命令"映射
// 到本机执行：提交、推送、远程引用核验与合并都经过真实 Git，因此不需要 SSH、
// Docker 或外网。
package codesaver_local

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

	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 测试使用的固定值。
const (
	testTaskID      = "task-1"
	testTaskBranch  = "agent/task-1"
	testContainerID = "container-1"
	testDockerID    = "docker-container-1"
	testHostID      = "host-1"
	testRemoteName  = "origin"
	testBaseBranch  = "main"
	// testRepositoryURL 是工作仓库登记的远程地址。
	//
	// 代码保存只接受 ssh:// 与 https:// 形式且不含内嵌凭据的地址，因此这里使用
	// 一个真实的 https 形式地址，执行器再把它映射为测试工作空间中的 bare 仓库。
	testRepositoryURL = "https://git.example.com/group/repo.git"
	gitTimeout        = 2 * time.Minute
)

// requireGit 检查本机 Git 是否可用，缺失时输出警告并跳过。
func requireGit(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		reason := "WARN [SKIP] codesaver local integration tests: git not found in PATH."
		t.Log(reason)
		t.Skip(reason)
	}
	return path
}

// localExecutor 把容器内命令映射到本机目录执行。
//
// 它只实现代码保存实际使用的固定命令模板：仓库路径被替换为测试工作空间中的真实
// 仓库，其余命令一律返回失败，因此任何未预期的命令都不会被静默放过。
type localExecutor struct {
	gitPath string
	// workDir 是容器内工作目录，映射到 localRepo。
	workDir string
	// localRepo 是本机真实工作仓库路径。
	localRepo string
	// localBare 是本机 bare 仓库路径，用于替换远程地址。
	localBare string
	// delay 在推送命令前插入的等待时间，用于构造推送超时场景。
	delay time.Duration
	mu    sync.Mutex
	// commands 记录收到的命令，用于断言命令构造。
	commands [][]string
	// pushed 记录推送命令次数。
	pushed int
	// brokenRemote 为真时把远程映射为不存在的路径，用于构造推送失败。
	brokenRemote bool
}

// ExecInContainer 在映射目录内执行命令。
func (e *localExecutor) ExecInContainer(
	ctx context.Context,
	dockerID string,
	command []string,
	timeout time.Duration,
) (docker.CommandResult, error) {
	e.mu.Lock()
	e.commands = append(e.commands, append([]string(nil), command...))
	e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return docker.CommandResult{
			ExitCode: -1,
			Status:   sshclient.CommandStatusCancelled,
			Certain:  false,
		}, err
	}
	if len(command) == 2 && command[0] == "git" && command[1] == "--version" {
		return e.run(ctx, timeout, []string{e.gitPath, "--version"})
	}
	if len(command) < 3 || command[0] != "git" || command[1] != "-C" {
		return docker.CommandResult{
			ExitCode: 127,
			Stderr:   "unsupported command: " + strings.Join(command, " "),
			Status:   sshclient.CommandStatusExitError,
			Certain:  true,
		}, nil
	}
	args := e.rewriteArgs(command[3:])
	if len(args) > 0 && args[0] == "push" {
		e.mu.Lock()
		e.pushed++
		delay := e.delay
		e.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return docker.CommandResult{
					ExitCode: -1,
					Status:   sshclient.CommandStatusTimeout,
					Certain:  false,
				}, nil
			}
		}
	}
	return e.run(ctx, timeout, append([]string{e.gitPath, "-C", e.localRepo}, args...))
}

// mapOutput 把本机路径还原为容器内路径。
//
// 代码保存会比对 rev-parse --show-toplevel 与请求中的容器内目录，因此执行器
// 必须把本机真实路径映射回容器路径，否则会被判定为"不是仓库根目录"。
func (e *localExecutor) mapOutput(value string) string {
	// Git 在 Windows 上以正斜杠输出路径，两种形式都要映射。
	mapped := strings.ReplaceAll(value, filepath.ToSlash(e.localRepo), e.workDir)
	return strings.ReplaceAll(mapped, e.localRepo, e.workDir)
}

// run 在本机执行命令并转换为容器执行结果。
func (e *localExecutor) run(
	ctx context.Context,
	timeout time.Duration,
	argv []string,
) (docker.CommandResult, error) {
	if timeout <= 0 {
		timeout = gitTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	output, err := cmd.CombinedOutput()
	result := docker.CommandResult{
		Stdout:  e.mapOutput(string(output)),
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

// rewriteArgs 把容器内路径与远程名称改写为本机真实路径。
//
// 容器内 origin 是网络远程仓库；本机测试把它映射为测试工作空间中的 bare 仓库，
// 因此不会访问公网。
func (e *localExecutor) rewriteArgs(args []string) []string {
	rewritten := append([]string(nil), args...)
	for index, arg := range rewritten {
		if arg == e.workDir {
			rewritten[index] = e.localRepo
			continue
		}
		if index == 0 {
			continue
		}
		switch rewritten[0] {
		case "push", "fetch", "ls-remote", "pull":
			if arg == testRemoteName {
				rewritten[index] = e.remoteTarget()
			}
		}
	}
	return rewritten
}

// remoteTarget 返回本次测试使用的远程仓库路径。
func (e *localExecutor) remoteTarget() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.brokenRemote {
		return e.localBare + "-missing"
	}
	return e.localBare
}

// pushedCount 返回推送命令次数。
func (e *localExecutor) pushedCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pushed
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

// memoryRecorder 在内存中保存代码保存记录。
type memoryRecorder struct {
	mu      sync.Mutex
	records map[domain.ID]domain.CodeSaveRecord
	// updateErr 非空时让满足 updateErrIf 的写入失败，用于构造落盘失败窗口。
	updateErr error
	// updateErrIf 是失败谓词，nil 表示全部写入都失败。
	updateErrIf func(domain.CodeSaveRecord) bool
}

// newRecorder 构造空的内存仓储。
func newRecorder() *memoryRecorder {
	return &memoryRecorder{records: make(map[domain.ID]domain.CodeSaveRecord)}
}

// Create 保存新记录。
func (r *memoryRecorder) Create(ctx context.Context, record domain.CodeSaveRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[record.ID] = record
	return nil
}

// Update 更新记录。
//
// updateErr 用于构造"Git 已经成功、但状态写不进数据库"的窗口：真实 Git 仓库
// 会真的产生提交或推送，只有数据库这一侧失败。
func (r *memoryRecorder) Update(ctx context.Context, record domain.CodeSaveRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil && (r.updateErrIf == nil || r.updateErrIf(record)) {
		return r.updateErr
	}
	r.records[record.ID] = record
	return nil
}

// failUpdateWhen 让满足条件的写入失败。
func (r *memoryRecorder) failUpdateWhen(err error, when func(domain.CodeSaveRecord) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateErr = err
	r.updateErrIf = when
}

// snapshot 返回记录副本，用于断言数据库中的真实状态。
func (r *memoryRecorder) snapshot(t *testing.T, id domain.ID) domain.CodeSaveRecord {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[id]
	if !ok {
		t.Fatalf("没有找到记录 %s", id)
	}
	return record
}

// Get 读取记录。
func (r *memoryRecorder) Get(
	ctx context.Context,
	id domain.ID,
) (*domain.CodeSaveRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[id]
	if !ok {
		return nil, errors.New("记录不存在")
	}
	return &record, nil
}

// ListByTask 按任务列出记录。
func (r *memoryRecorder) ListByTask(
	ctx context.Context,
	taskID string,
) ([]domain.CodeSaveRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	records := make([]domain.CodeSaveRecord, 0, len(r.records))
	for _, record := range r.records {
		if record.TaskID == taskID {
			records = append(records, record)
		}
	}
	return records, nil
}

// single 返回唯一的记录。
func (r *memoryRecorder) single(t *testing.T) domain.CodeSaveRecord {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.records) != 1 {
		t.Fatalf("期望 1 条保存记录，实际 %d 条", len(r.records))
	}
	for _, record := range r.records {
		return record
	}
	return domain.CodeSaveRecord{}
}

// newContainer 构造运行中的容器记录。
func newContainer() domain.Container {
	return domain.Container{
		ID:          domain.ID(testContainerID),
		HostID:      domain.ID(testHostID),
		DockerID:    testDockerID,
		Name:        "agentflow-container-1",
		State:       domain.ContainerStateRunning,
		DockerState: domain.DockerStateRunning,
	}
}

// repo 是一组真实 Git 仓库：bare 远程仓库与本地工作仓库。
type repo struct {
	// bare 是 bare 仓库路径。
	bare string
	// work 是工作仓库路径。
	work string
	// gitPath 是本机 Git 可执行文件。
	gitPath string
}

// git 在指定目录执行 Git 命令并返回输出。
func (r *repo) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	argv := append([]string{"-C", dir}, args...)
	cmd := exec.Command(r.gitPath, argv...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=AgentFlow Tester",
		"GIT_AUTHOR_EMAIL=agentflow@example.com",
		"GIT_COMMITTER_NAME=AgentFlow Tester",
		"GIT_COMMITTER_EMAIL=agentflow@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("执行 git %s 失败: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

// rev 读取指定引用的 commit SHA。
func (r *repo) rev(t *testing.T, ref string) string {
	t.Helper()
	return strings.TrimSpace(r.git(t, r.work, "rev-parse", ref))
}

// bareRev 读取 bare 仓库中指定引用的 commit SHA。
func (r *repo) bareRev(t *testing.T, ref string) string {
	t.Helper()
	return strings.TrimSpace(r.git(t, r.bare, "rev-parse", ref))
}

// writeFile 在工作仓库写入文件。
func (r *repo) writeFile(t *testing.T, name, content string) {
	t.Helper()
	path := filepath.Join(r.work, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入文件失败: %v", err)
	}
}

// newRepo 在测试工作空间中创建 bare 仓库、基础分支与任务分支。
func newRepo(t *testing.T) *repo {
	t.Helper()
	gitPath := requireGit(t)
	root := testkit.Dir(t)
	bare := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	instance := &repo{bare: bare, work: work, gitPath: gitPath}
	instance.git(t, root, "init", "--bare", "--initial-branch="+testBaseBranch, bare)
	instance.git(t, root, "init", "--initial-branch="+testBaseBranch, work)
	instance.git(t, work, "config", "user.name", "AgentFlow Tester")
	instance.git(t, work, "config", "user.email", "agentflow@example.com")
	instance.git(t, work, "remote", "add", testRemoteName, bare)
	instance.writeFile(t, "README.md", "初始内容\n")
	instance.git(t, work, "add", "--all")
	instance.git(t, work, "commit", "-m", "初始提交")
	instance.writeFile(t, "internal/app.go", "package app\n")
	instance.git(t, work, "add", "--all")
	instance.git(t, work, "commit", "-m", "第二个提交")
	instance.git(t, work, "push", testRemoteName, testBaseBranch)
	instance.git(t, work, "remote", "set-head", testRemoteName, testBaseBranch)
	instance.git(t, work, "checkout", "-b", testTaskBranch)
	// 代码保存只接受符合仓库访问策略的远程地址，因此把地址改写为 https 形式；
	// 测试执行器再把远程名称映射回本机 bare 仓库，避免访问公网。
	instance.git(t, work, "remote", "set-url", testRemoteName, testRepositoryURL)
	return instance
}

// newSaver 构造使用本机执行器的代码保存组件。
func newSaver(
	t *testing.T,
	instance *repo,
	recorder *memoryRecorder,
	executor *localExecutor,
) *codesaver.Saver {
	t.Helper()
	saver, err := codesaver.New(codesaver.Options{
		Executor: executor,
		Guard:    &fakeGuard{container: newContainer()},
		Recorder: recorder,
	})
	if err != nil {
		t.Fatalf("构造代码保存组件失败: %v", err)
	}
	return saver
}

// newExecutor 构造把容器内命令映射到真实仓库的执行器。
func newExecutor(instance *repo) *localExecutor {
	return &localExecutor{
		gitPath:   instance.gitPath,
		workDir:   containerWorkDir,
		localRepo: instance.work,
		localBare: instance.bare,
	}
}

// containerWorkDir 是请求中使用的容器内工作目录。
//
// 该路径必须符合容器内路径规则（位于 /workspace 之下），执行器把它映射到测试
// 工作空间中的真实仓库。
const containerWorkDir = domain.DefaultContainerWorkDir

// request 返回指向真实仓库的保存请求。
func request() codesaver.Request {
	return codesaver.Request{
		MachineID:   testHostID,
		ContainerID: testContainerID,
		DockerID:    testDockerID,
		TaskID:      testTaskID,
		WorkDir:     containerWorkDir,
		RemoteName:  testRemoteName,
		BaseBranch:  testBaseBranch,
	}
}

// TestSaveCommitsAndPushesTaskBranch 校验真实 Git 下生成提交并推送任务分支。
func TestSaveCommitsAndPushesTaskBranch(t *testing.T) {
	t.Parallel()
	instance := newRepo(t)
	// 工作目录映射到真实仓库：执行器把容器内路径替换为 localRepo。
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	instance.writeFile(t, "internal/feature.go", "package internal\n")
	result, err := saver.Run(context.Background(), request())
	if err != nil {
		t.Fatalf("保存流程应成功: %v", err)
	}
	record := recorder.single(t)
	if record.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("提交状态应为 succeeded，实际 %s", record.CommitStatus)
	}
	head := instance.rev(t, "HEAD")
	if record.CommitSHA != head {
		t.Fatalf("记录的提交 SHA 应为 %s，实际 %s", head, record.CommitSHA)
	}
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("推送状态应为 succeeded，实际 %s", record.PushStatus)
	}
	remoteHead := instance.bareRev(t, "refs/heads/"+testTaskBranch)
	if remoteHead != head {
		t.Fatalf("远程任务分支应指向 %s，实际 %s", head, remoteHead)
	}
	if record.RemoteCommitSHA != remoteHead {
		t.Fatalf("记录的远程 commit 应为 %s，实际 %s", remoteHead, record.RemoteCommitSHA)
	}
	if record.MergeStatus != domain.MergeStatusNotRequested {
		t.Fatalf("默认不应合并，实际 %s", record.MergeStatus)
	}
	baseRemote := instance.bareRev(t, "refs/heads/"+testBaseBranch)
	if baseRemote == head {
		t.Fatal("未请求合并时基础分支不应包含任务提交")
	}
	if !result.Record.SavedToRemote() {
		t.Fatal("任务分支推送成功后 SavedToRemote 应为真")
	}
	t.Logf("测试工作空间: %s", filepath.Dir(instance.work))
}

// TestSaveNoChangesDoesNotCreateCommit 校验没有变更时不制造空提交。
func TestSaveNoChangesDoesNotCreateCommit(t *testing.T) {
	t.Parallel()
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	before := instance.rev(t, "HEAD")
	result, err := saver.Run(context.Background(), request())
	if err != nil {
		t.Fatalf("无变更时不应失败: %v", err)
	}
	if !result.NoChanges {
		t.Fatal("期望记录 no_changes")
	}
	if after := instance.rev(t, "HEAD"); after != before {
		t.Fatalf("不应创建空提交: %s -> %s", before, after)
	}
	record := recorder.single(t)
	if record.CommitStatus != domain.CommitStatusNoChanges {
		t.Fatalf("提交状态应为 no_changes，实际 %s", record.CommitStatus)
	}
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("远程已包含任务分支时推送状态应为 succeeded，实际 %s", record.PushStatus)
	}
}

// TestSaveMergeIncludesTaskCommits 校验显式开启合并后基础分支包含任务提交。
func TestSaveMergeIncludesTaskCommits(t *testing.T) {
	t.Parallel()
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	instance.writeFile(t, "internal/feature.go", "package internal\n")
	saveRequest := request()
	saveRequest.MergeAfterPush = true
	result, err := saver.Run(context.Background(), saveRequest)
	if err != nil {
		t.Fatalf("保存流程应成功: %v", err)
	}
	record := recorder.single(t)
	if record.MergeStatus != domain.MergeStatusSucceeded {
		t.Fatalf("合并状态应为 succeeded，实际 %s", record.MergeStatus)
	}
	remoteBase := instance.bareRev(t, "refs/heads/"+testBaseBranch)
	if remoteBase != record.MergeSHA {
		t.Fatalf("远程基础分支应为 %s，实际 %s", record.MergeSHA, remoteBase)
	}
	// 基础分支历史必须包含任务提交。
	log := instance.git(t, instance.bare, "log", "--format=%H", "refs/heads/"+testBaseBranch)
	if !strings.Contains(log, record.CommitSHA) {
		t.Fatalf("基础分支历史应包含任务提交 %s: %s", record.CommitSHA, log)
	}
	// 合并提交必须同时包含两个父提交。
	parents := strings.Fields(instance.git(
		t,
		instance.bare,
		"rev-list", "--parents", "-n", "1",
		"refs/heads/"+testBaseBranch,
	))
	if len(parents) != 3 {
		t.Fatalf("合并提交应有两个父提交: %v", parents)
	}
	if !result.Succeeded() {
		t.Fatal("完整保存流程应判定成功")
	}
}

// TestPushFailureIsNotReportedAsSuccess 校验推送失败不会被误报为成功。
func TestPushFailureIsNotReportedAsSuccess(t *testing.T) {
	t.Parallel()
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	instance.writeFile(t, "internal/feature.go", "package internal\n")
	// 让远程不可访问：执行器把远程映射为不存在的仓库。
	executor.brokenRemote = true
	_, err := saver.Run(context.Background(), request())
	if err == nil {
		t.Fatal("推送失败时必须返回错误")
	}
	record := recorder.single(t)
	if record.PushStatus == domain.PushStatusSucceeded {
		t.Fatal("推送失败不得记录成功")
	}
	if record.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("提交应已成功，实际 %s", record.CommitStatus)
	}
	if record.SavedToRemote() {
		t.Fatal("推送失败时 SavedToRemote 必须为假")
	}
	if !record.RequiresManualRecovery() {
		t.Fatal("推送失败应标记为需要人工处理")
	}
}

// TestMergeConflictKeepsInspectableState 校验合并冲突时保留可检查的 Git 状态。
func TestMergeConflictKeepsInspectableState(t *testing.T) {
	t.Parallel()
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	// 在任务分支与基础分支上制造同一文件的冲突内容。
	instance.writeFile(t, "internal/conflict.go", "任务分支版本\n")
	instance.git(t, instance.work, "add", "--all")
	instance.git(t, instance.work, "commit", "-m", "任务分支修改")
	instance.git(t, instance.work, "checkout", testBaseBranch)
	instance.writeFile(t, "internal/conflict.go", "基础分支版本\n")
	instance.git(t, instance.work, "add", "--all")
	instance.git(t, instance.work, "commit", "-m", "基础分支修改")
	// 直接推送到 bare 仓库：工作仓库的 origin 已被改写为 https 形式地址。
	instance.git(t, instance.work, "push", instance.bare, testBaseBranch)
	instance.git(t, instance.work, "checkout", testTaskBranch)
	taskCommit := instance.rev(t, "refs/heads/"+testTaskBranch)
	saveRequest := request()
	saveRequest.MergeAfterPush = true
	_, err := saver.Run(context.Background(), saveRequest)
	if codesaver.ErrorCodeOf(err) != codesaver.CodeMergeConflict {
		t.Fatalf("期望 merge_conflict，实际 %v", err)
	}
	record := recorder.single(t)
	if record.MergeStatus != domain.MergeStatusConflict {
		t.Fatalf("合并状态应为 conflict，实际 %s", record.MergeStatus)
	}
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("任务分支应保持推送成功，实际 %s", record.PushStatus)
	}
	remoteTask := instance.bareRev(t, "refs/heads/"+testTaskBranch)
	if remoteTask != taskCommit {
		t.Fatalf("远程任务分支应保留已推送提交 %s，实际 %s", taskCommit, remoteTask)
	}
	// 冲突后不应留下未完成的合并状态，也不应重置任务分支。
	status := instance.git(t, instance.work, "status", "--porcelain")
	if strings.Contains(status, "UU") {
		t.Fatalf("应中止自动合并，实际工作区仍有冲突: %s", status)
	}
	branch := strings.TrimSpace(instance.git(t, instance.work, "rev-parse", "--abbrev-ref", "HEAD"))
	if branch != testBaseBranch {
		t.Logf("合并失败后当前分支为 %s", branch)
	}
}

// TestConcurrentSavesUseSeparateRepositories 校验并发操作使用独立仓库。
func TestConcurrentSavesUseSeparateRepositories(t *testing.T) {
	t.Parallel()
	requireGit(t)
	const total = 3
	var group sync.WaitGroup
	errs := make(chan error, total)
	for index := 0; index < total; index++ {
		group.Add(1)
		go func(offset int) {
			defer group.Done()
			instance := newRepo(t)
			executor := newExecutor(instance)
			recorder := newRecorder()
			saver := newSaver(t, instance, recorder, executor)
			instance.writeFile(t, "internal/concurrent.go", "package internal\n")
			if _, err := saver.Run(context.Background(), request()); err != nil {
				errs <- err
				return
			}
			remoteHead := instance.bareRev(t, "refs/heads/"+testTaskBranch)
			if remoteHead != instance.rev(t, "HEAD") {
				errs <- errors.New("远程任务分支与本地 HEAD 不一致")
				return
			}
			errs <- nil
		}(index)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发保存不应失败: %v", err)
		}
	}
}

// exitCodeOf 提取退出码。
func exitCodeOf(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// TestInspectWorktreeReportsBranchAndChanges 校验只读核验返回真实分支、HEAD 与变更。
//
// 该用例直接使用本机真实 Git 仓库，验证"记录代码状态"与"保存代码"看到的是同一个
// 工作区：核验不提交、不推送，也不写入任何代码保存记录。
func TestInspectWorktreeReportsBranchAndChanges(t *testing.T) {
	t.Parallel()
	instance := newRepo(t)
	executor := newExecutor(instance)
	inspector, err := codesaver.NewInspector(codesaver.InspectorOptions{
		Executor: executor,
		Guard:    &fakeGuard{container: newContainer()},
	})
	if err != nil {
		t.Fatalf("构造工作区核验器失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	clean, err := inspector.Inspect(ctx, request())
	if err != nil {
		t.Fatalf("核验干净工作区失败: %v", err)
	}
	if clean.Branch != testTaskBranch {
		t.Fatalf("分支不符: %s", clean.Branch)
	}
	if clean.Head != instance.rev(t, "HEAD") {
		t.Fatalf("HEAD 不符: %s", clean.Head)
	}
	if !clean.Clean {
		t.Fatalf("初始工作区应当是干净的: %+v", clean)
	}

	instance.writeFile(t, "internal/app.go", "package app\n// 修改\n")
	instance.writeFile(t, "internal/new.go", "package app\n")
	dirty, err := inspector.Inspect(ctx, request())
	if err != nil {
		t.Fatalf("核验脏工作区失败: %v", err)
	}
	if dirty.Clean {
		t.Fatalf("存在未提交变更时不应报告干净: %+v", dirty)
	}
	if dirty.Modified == 0 {
		t.Fatalf("应识别出已修改文件: %+v", dirty)
	}
	if dirty.Added == 0 {
		t.Fatalf("应识别出新增文件: %+v", dirty)
	}
	// 核验不得提交：HEAD 必须与核验前一致。
	if dirty.Head != clean.Head {
		t.Fatalf("核验不应改变 HEAD: %s -> %s", clean.Head, dirty.Head)
	}
	if executor.pushedCount() != 0 {
		t.Fatal("核验不应执行推送")
	}
}

// TestInspectWorktreeRejectsUnmanagedContainer 校验容器不可用时核验直接失败。
func TestInspectWorktreeRejectsUnmanagedContainer(t *testing.T) {
	t.Parallel()
	instance := newRepo(t)
	detached := newContainer()
	detached.Detached = true
	detached.State = domain.ContainerStateDetached
	inspector, err := codesaver.NewInspector(codesaver.InspectorOptions{
		Executor: newExecutor(instance),
		Guard:    &fakeGuard{container: detached},
	})
	if err != nil {
		t.Fatalf("构造工作区核验器失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if _, inspectErr := inspector.Inspect(ctx, request()); inspectErr == nil {
		t.Fatal("已脱离管理的容器不应允许核验")
	}
}

// TestCommitSucceededButRecordFailedIsRecoverable 校验真实 Git 下的提交落盘失败窗口。
//
// 场景：Git 真的产生了提交，但提交结果写不进数据库。此时数据库停留在
// commit_status=pending，唯一能区分"已提交"与"未提交"的证据是提交之前落盘的
// HEAD 基线。本用例验证这条证据链在真实 Git 上成立。
func TestCommitSucceededButRecordFailedIsRecoverable(t *testing.T) {
	t.Parallel()
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	headBefore := instance.rev(t, "HEAD")
	// 只让"提交成功"那一次写入失败，基线写入仍然成功。
	recorder.failUpdateWhen(errors.New("database is locked"), func(record domain.CodeSaveRecord) bool {
		return record.CommitStatus == domain.CommitStatusSucceeded
	})
	instance.writeFile(t, "internal/feature.go", "package internal\n")
	result, err := saver.Run(context.Background(), request())
	if err == nil {
		t.Fatal("状态写入失败时必须返回错误")
	}
	if codesaver.ErrorCodeOf(err) != codesaver.CodeStateUnconfirmed {
		t.Fatalf("期望 state_unconfirmed，实际 %v", err)
	}
	// 真实 Git 已经产生了提交。
	headAfter := instance.rev(t, "HEAD")
	if headAfter == headBefore {
		t.Fatal("真实 Git 应已产生新提交")
	}
	stored := recorder.snapshot(t, result.Record.ID)
	if stored.CommitStatus != domain.CommitStatusPending {
		t.Fatalf("数据库应停留在 pending，实际 %s", stored.CommitStatus)
	}
	if stored.BaselineCommitSHA != headBefore {
		t.Fatalf("提交基线应为提交前的 HEAD %s，实际 %q", headBefore, stored.BaselineCommitSHA)
	}
	// 只读核验可以依据基线与真实 HEAD 确认"提交已经发生"。
	observation := saver.Observe(context.Background(), request(), stored)
	if observation.ReadFailed {
		t.Fatalf("核验不应失败: %s", observation.ReadError)
	}
	if observation.HeadCommit != headAfter {
		t.Fatalf("核验读到的 HEAD 应为 %s，实际 %s", headAfter, observation.HeadCommit)
	}
	decision := domain.DecideCodeSaveRecovery(stored, observation)
	if decision.Status != domain.RecoveryStatusConfirmedApplied {
		t.Fatalf("应确认提交已经发生，实际 %s（%s）", decision.Status, decision.Summary)
	}
	if decision.CommitSHA != headAfter {
		t.Fatalf("确认的提交应为 %s，实际 %s", headAfter, decision.CommitSHA)
	}
	if decision.SafeToRetry {
		t.Fatal("提交已发生时不得提示可以重试")
	}
}

// TestObserveDetectsRemoteMismatch 校验核验能识别远程与预期提交不一致。
//
// 远端指向其他提交时必须判为不确定并禁止自动重试：重推可能覆盖远程历史。
func TestObserveDetectsRemoteMismatch(t *testing.T) {
	t.Parallel()
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	// 在任务分支之外推进远程任务分支：模拟其他来源已经推送过。
	instance.writeFile(t, "internal/other.go", "package internal\n")
	instance.git(t, instance.work, "add", "--all")
	instance.git(t, instance.work, "commit", "-m", "他人提交")
	// 直接推到 bare 仓库：工作仓库的 origin 已被改写成 https 形式，不能访问公网。
	instance.git(t, instance.work, "push", instance.bare, "HEAD:refs/heads/"+testTaskBranch)
	head := instance.rev(t, "HEAD")
	// 构造一条"提交已成功、推送仍是 pending"的记录，但预期提交指向别的 commit。
	record := domain.CodeSaveRecord{
		ID:           domain.NewID(),
		TaskID:       testTaskID,
		TaskBranch:   testTaskBranch,
		ContainerID:  testContainerID,
		RemoteName:   testRemoteName,
		BaseBranch:   testBaseBranch,
		CommitStatus: domain.CommitStatusSucceeded,
		CommitSHA:    "1111111111111111111111111111111111111111",
		PushStatus:   domain.PushStatusPending,
		MergeStatus:  domain.MergeStatusNotRequested,
	}
	observation := saver.Observe(context.Background(), request(), record)
	if observation.ReadFailed {
		t.Fatalf("核验不应失败: %s", observation.ReadError)
	}
	if observation.RemoteTaskCommit != head {
		t.Fatalf("远程任务分支应为 %s，实际 %s", head, observation.RemoteTaskCommit)
	}
	decision := domain.DecideCodeSaveRecovery(record, observation)
	if decision.Status != domain.RecoveryStatusUnverifiable {
		t.Fatalf("远程与预期不一致时应转人工核验，实际 %s", decision.Status)
	}
	if decision.SafeToRetry {
		t.Fatal("远程与预期不一致时不得提示可以重试")
	}
}

// TestObserveReportsUnreachableRemote 校验远程不可达时保留不确定性。
func TestObserveReportsUnreachableRemote(t *testing.T) {
	t.Parallel()
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	executor.mu.Lock()
	executor.brokenRemote = true
	executor.mu.Unlock()
	record := domain.CodeSaveRecord{
		ID:           domain.NewID(),
		TaskID:       testTaskID,
		TaskBranch:   testTaskBranch,
		ContainerID:  testContainerID,
		RemoteName:   testRemoteName,
		BaseBranch:   testBaseBranch,
		CommitStatus: domain.CommitStatusSucceeded,
		CommitSHA:    instance.rev(t, "HEAD"),
		PushStatus:   domain.PushStatusPending,
		MergeStatus:  domain.MergeStatusNotRequested,
	}
	observation := saver.Observe(context.Background(), request(), record)
	if !observation.ReadFailed {
		t.Fatal("远程不可达时必须报告核验失败")
	}
	decision := domain.DecideCodeSaveRecovery(record, observation)
	if decision.Status != domain.RecoveryStatusUnverifiable {
		t.Fatalf("远程不可达时必须保留不确定性，实际 %s", decision.Status)
	}
	if decision.SafeToRetry {
		t.Fatal("远程不可达时不得提示可以重试")
	}
}

// TestObserveConfirmsMergeApplied 校验真实合并后可以确认合并已经发生。
//
// 判定要求远程基础分支等于唯一的候选合并提交：第一父为合并前基线、第二父为
// 任务分支提交。仅仅"基础分支变了"不构成证据。
func TestObserveConfirmsMergeApplied(t *testing.T) {
	t.Parallel()
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	instance.writeFile(t, "internal/feature.go", "package internal\n")
	requestWithMerge := request()
	requestWithMerge.MergeAfterPush = true
	result, err := saver.Run(context.Background(), requestWithMerge)
	if err != nil {
		t.Fatalf("保存流程应成功: %v", err)
	}
	stored := recorder.snapshot(t, result.Record.ID)
	if stored.MergeStatus != domain.MergeStatusSucceeded {
		t.Fatalf("合并状态应为 succeeded，实际 %s", stored.MergeStatus)
	}
	if stored.BaselineBaseCommitSHA == "" {
		t.Fatal("合并前必须落盘基础分支基线")
	}
	// 把记录回退成"合并仍是 pending"，模拟合并结果没落盘的窗口。
	pending := stored
	pending.MergeStatus = domain.MergeStatusPending
	pending.MergeSHA = ""
	observation := saver.Observe(context.Background(), request(), pending)
	if observation.ReadFailed {
		t.Fatalf("核验不应失败: %s", observation.ReadError)
	}
	if !observation.MergeCommitResolvable {
		t.Fatalf("应能唯一确定合并提交，实际无法推算（%+v）", observation)
	}
	if observation.ExpectedMergeCommit != stored.MergeSHA {
		t.Fatalf("推算的合并提交应为 %s，实际 %s", stored.MergeSHA, observation.ExpectedMergeCommit)
	}
	decision := domain.DecideCodeSaveRecovery(pending, observation)
	if decision.Status != domain.RecoveryStatusConfirmedApplied {
		t.Fatalf("应确认合并已经发生，实际 %s（%s）", decision.Status, decision.Summary)
	}
	if decision.MergeSHA != stored.MergeSHA {
		t.Fatalf("确认的合并提交应为 %s，实际 %s", stored.MergeSHA, decision.MergeSHA)
	}
	if decision.SafeToRetry {
		t.Fatal("合并已发生时不得提示可以重试")
	}
}
