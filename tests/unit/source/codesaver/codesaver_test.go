// Package codesaver_unit 提供代码保存组件的单元测试。
//
// 这里使用可注入的容器执行器、容器核验器与记录仓储替身：Git 命令构造、参数
// 转义、状态推进、远程结果核验与错误分类都经过真实代码路径，但不需要 SSH 连接、
// Docker Engine 或真实 Git。
package codesaver_unit

import (
	"context"
	"errors"
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
	testRepository  = "https://git.example.com/group/repo.git"
	testBaseBranch  = "main"
	// 合法的测试 commit SHA（40 位十六进制）。
	testHeadCommit    = "1111111111111111111111111111111111111111"
	testCommitSHA     = "2222222222222222222222222222222222222222"
	testMergeSHA      = "3333333333333333333333333333333333333333"
	testBaseRemoteSHA = "4444444444444444444444444444444444444444"
	testRemoteDiverge = "5555555555555555555555555555555555555555"
)

// succeeded 构造成功结果。
func succeeded(stdout string) docker.CommandResult {
	return docker.CommandResult{
		ExitCode: 0,
		Stdout:   stdout,
		Status:   sshclient.CommandStatusSucceeded,
		Certain:  true,
	}
}

// failed 构造非零退出码结果。
func failed(code int, stderr string) docker.CommandResult {
	return docker.CommandResult{
		ExitCode: code,
		Stderr:   stderr,
		Status:   sshclient.CommandStatusExitError,
		Certain:  true,
	}
}

// uncertain 构造结果不确定的命令结果。
func uncertain(status sshclient.CommandStatus) docker.CommandResult {
	return docker.CommandResult{
		ExitCode: -1,
		Status:   status,
		Certain:  false,
	}
}

// override 是一次测试对某个子命令的替身响应。
type override func(repo *fakeRepo, args []string) (docker.CommandResult, bool)

// fakeRepo 模拟容器内 Git 仓库状态，并作为命令响应器使用。
//
// 它只响应代码保存实际使用的固定命令模板；未预期的命令一律返回失败，因此任何
// 未被测试覆盖的命令都不会被静默放过。
type fakeRepo struct {
	mu sync.Mutex
	// head 是当前 HEAD 指向的 commit。
	head string
	// dirty 表示工作区存在未提交变更。
	dirty bool
	// staged 表示暂存区已有内容。
	staged bool
	// conflicts 表示工作区存在未解决的冲突。
	conflicts bool
	// taskBranch 是当前分支名。
	taskBranch string
	// remoteTask 是远程任务分支指向的 commit。
	remoteTask string
	// remoteBase 是远程基础分支指向的 commit。
	remoteBase string
	// baseExists 表示本地基础分支是否存在。
	baseExists bool
	// overrides 按子命令覆盖默认响应。
	overrides map[string]override
	// calls 记录收到的子命令序列，用于断言执行顺序。
	calls []string
	// commitArgs 记录最近一次提交命令的参数。
	commitArgs []string
	// pushCalls 记录每次推送命令的参数。
	pushCalls [][]string
	// onPush 在收到推送命令时回调，用于断言记录已落盘。
	onPush func()
}

// newFakeRepo 构造与默认请求匹配的仓库状态。
func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		head:       testHeadCommit,
		taskBranch: testTaskBranch,
		remoteTask: testHeadCommit,
		remoteBase: testBaseRemoteSHA,
		overrides:  map[string]override{},
	}
}

// set 在锁保护下修改仓库状态。
func (r *fakeRepo) set(mutate func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	mutate()
}

// recorded 返回收到的子命令序列副本。
func (r *fakeRepo) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// count 统计某个子命令被调用的次数。
func (r *fakeRepo) count(subcommand string) int {
	total := 0
	for _, call := range r.recorded() {
		if call == subcommand {
			total++
		}
	}
	return total
}

// exec 是 ContainerExecutor 的替身实现。
func (r *fakeRepo) exec(
	ctx context.Context,
	command []string,
) (docker.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return uncertain(sshclient.CommandStatusCancelled), err
	}
	if len(command) == 2 && command[0] == "git" && command[1] == "--version" {
		return succeeded("git version 2.43.0"), nil
	}
	if len(command) < 3 || command[0] != "git" || command[1] != "-C" {
		return failed(127, "unsupported command: "+strings.Join(command, " ")), nil
	}
	args := command[3:]
	if len(args) == 0 {
		return failed(127, "missing git subcommand"), nil
	}
	// 跳过 Git 的 -c key=value 前缀，定位真正的子命令。
	fullArgs := args
	for len(args) >= 2 && args[0] == "-c" {
		args = args[2:]
	}
	if len(args) == 0 {
		return failed(127, "missing git subcommand"), nil
	}
	subcommand := args[0]
	r.mu.Lock()
	r.calls = append(r.calls, subcommand)
	if subcommand == "commit" {
		r.commitArgs = append([]string(nil), fullArgs...)
	}
	if subcommand == "push" {
		r.pushCalls = append(r.pushCalls, append([]string(nil), args...))
	}
	handler := r.overrides[subcommand]
	r.mu.Unlock()
	if handler != nil {
		if result, ok := handler(r, args); ok {
			return result, nil
		}
	}
	return r.defaultResult(subcommand, args)
}

// defaultResult 返回各子命令的默认响应。
func (r *fakeRepo) defaultResult(
	subcommand string,
	args []string,
) (docker.CommandResult, error) {
	switch subcommand {
	case "rev-parse":
		return r.revParse(args), nil
	case "config":
		return r.config(args), nil
	case "status":
		return r.status(), nil
	case "symbolic-ref":
		return succeeded("origin/" + testBaseBranch + "\n"), nil
	case "for-each-ref":
		return succeeded("origin/main\norigin/master\n"), nil
	case "ls-remote":
		return r.lsRemote(args), nil
	case "add":
		return succeeded(""), nil
	case "commit":
		r.set(func() {
			r.head = testCommitSHA
			r.dirty = false
			r.staged = false
		})
		return succeeded(""), nil
	case "push":
		if r.onPush != nil {
			r.onPush()
		}
		return r.push(args), nil
	case "fetch", "checkout":
		return succeeded(""), nil
	case "merge":
		return r.merge(args), nil
	}
	return failed(127, "unsupported git subcommand: "+subcommand), nil
}

// revParse 返回 rev-parse 查询结果。
func (r *fakeRepo) revParse(args []string) docker.CommandResult {
	for _, arg := range args {
		switch {
		case arg == "--show-toplevel":
			return succeeded(domain.DefaultContainerWorkDir + "\n")
		case arg == "--abbrev-ref":
			r.mu.Lock()
			branch := r.taskBranch
			r.mu.Unlock()
			return succeeded(branch + "\n")
		case strings.HasSuffix(arg, "^{commit}"):
			r.mu.Lock()
			exists := r.baseExists
			remoteBase := r.remoteBase
			r.mu.Unlock()
			if exists {
				return succeeded(remoteBase + "\n")
			}
			return failed(1, "")
		}
	}
	r.mu.Lock()
	head := r.head
	r.mu.Unlock()
	return succeeded(head + "\n")
}

// config 返回 config 查询结果。
func (r *fakeRepo) config(args []string) docker.CommandResult {
	for _, arg := range args {
		switch arg {
		case "remote." + testRemoteName + ".url":
			return succeeded(testRepository + "\n")
		case "user.name":
			return succeeded("AgentFlow Tester\n")
		case "user.email":
			return succeeded("agentflow@example.com\n")
		}
	}
	return failed(1, "")
}

// status 返回工作区 porcelain 状态。
func (r *fakeRepo) status() docker.CommandResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	builder := strings.Builder{}
	builder.WriteString("## " + r.taskBranch + "...origin/" + r.taskBranch + "\x00")
	switch {
	case r.conflicts:
		builder.WriteString("UU internal/app/app.go\x00")
	case r.staged:
		builder.WriteString("M  internal/app/app.go\x00")
	case r.dirty:
		builder.WriteString(" M internal/app/app.go\x00")
	}
	return succeeded(builder.String())
}

// lsRemote 返回远程分支引用查询结果。
func (r *fakeRepo) lsRemote(args []string) docker.CommandResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, arg := range args {
		switch arg {
		case "refs/heads/" + r.taskBranch:
			if r.remoteTask == "" {
				return succeeded("")
			}
			return succeeded(r.remoteTask + "\trefs/heads/" + r.taskBranch + "\n")
		case "refs/heads/" + testBaseBranch:
			if r.remoteBase == "" {
				return succeeded("")
			}
			return succeeded(r.remoteBase + "\trefs/heads/" + testBaseBranch + "\n")
		}
	}
	return succeeded("")
}

// push 更新远程分支引用。
func (r *fakeRepo) push(args []string) docker.CommandResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, r.taskBranch+":refs/heads/"):
			r.remoteTask = r.head
		case strings.HasPrefix(arg, testBaseBranch+":refs/heads/"):
			r.remoteBase = r.head
		}
	}
	return succeeded("")
}

// merge 处理合并子命令。
func (r *fakeRepo) merge(args []string) docker.CommandResult {
	for _, arg := range args {
		if arg == "--abort" {
			return succeeded("")
		}
	}
	r.set(func() {
		r.head = testMergeSHA
	})
	return succeeded("")
}

// executor 把仓库状态适配为容器执行器。
type executor struct {
	repo *fakeRepo
}

// ExecInContainer 在仓库状态上执行命令。
func (e executor) ExecInContainer(
	ctx context.Context,
	dockerID string,
	command []string,
	timeout time.Duration,
) (docker.CommandResult, error) {
	return e.repo.exec(ctx, command)
}

// fakeGuard 返回预设的容器记录或错误。
type fakeGuard struct {
	container domain.Container
	err       error
}

// EnsureExecutable 返回预设容器记录。
func (g *fakeGuard) EnsureExecutable(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	if g.err != nil {
		return domain.Container{}, g.err
	}
	return g.container, nil
}

// memoryRecorder 在内存中保存代码保存记录。
type memoryRecorder struct {
	mu      sync.Mutex
	records map[domain.ID]domain.CodeSaveRecord
	// creates 记录写入次数，用于断言记录在远程操作前已落盘。
	creates int
	// updates 记录更新次数。
	updates int
	// updateErr 非空时让 Update 返回该错误，用于构造"Git 已成功但记录没落盘"。
	updateErr error
	// updateErrTimes 限制 updateErr 的生效次数，负数表示一直生效。
	updateErrTimes int
	// updateErrIf 非空时只在谓词命中该次写入时失败，用于精确定位到某个阶段。
	updateErrIf func(domain.CodeSaveRecord) bool
	// err 让 Create 与 Update 统一失败。
	err error
}

// newRecorder 构造空的内存仓储。
func newRecorder() *memoryRecorder {
	return &memoryRecorder{records: make(map[domain.ID]domain.CodeSaveRecord)}
}

// Create 保存新记录。
func (r *memoryRecorder) Create(ctx context.Context, record domain.CodeSaveRecord) error {
	if r.err != nil {
		return r.err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[record.ID] = record
	r.creates++
	return nil
}

// Update 更新记录。
//
// updateErr 可以只生效有限次，用于验证"第一次写入失败、后续恢复成功"的窗口。
func (r *memoryRecorder) Update(ctx context.Context, record domain.CodeSaveRecord) error {
	if r.err != nil {
		return r.err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		switch {
		case r.updateErrIf != nil:
			// 谓词模式只让命中的那一次写入失败，其余正常落盘。
			if !r.updateErrIf(record) {
				break
			}
			return r.updateErr
		case r.updateErrTimes < 0:
			return r.updateErr
		case r.updateErrTimes > 0:
			r.updateErrTimes--
			return r.updateErr
		}
	}
	r.records[record.ID] = record
	r.updates++
	return nil
}

// failUpdates 让接下来的 times 次更新失败，times 为负数表示一直失败。
func (r *memoryRecorder) failUpdates(err error, times int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateErr = err
	r.updateErrTimes = times
	r.updateErrIf = nil
}

// failUpdateWhen 让满足条件的写入失败，用于精确定位"某个阶段落盘失败"。
func (r *memoryRecorder) failUpdateWhen(err error, when func(domain.CodeSaveRecord) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateErr = err
	r.updateErrTimes = -1
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

// ListByTask 按任务列出记录，最新的在前。
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
	for left, right := 0, len(records)-1; left < right; left, right = left+1, right-1 {
		records[left], records[right] = records[right], records[left]
	}
	return records, nil
}

// single 返回唯一的记录，用于断言最终状态。
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

// createsBeforePush 返回推送时已写入的记录数量。
func (r *memoryRecorder) createsBeforePush() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creates
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

// baseRequest 返回一份可通过前置校验的请求。
func baseRequest() codesaver.Request {
	return codesaver.Request{
		MachineID:   testHostID,
		ContainerID: testContainerID,
		DockerID:    testDockerID,
		TaskID:      testTaskID,
		WorkDir:     domain.DefaultContainerWorkDir,
		RemoteName:  testRemoteName,
		BaseBranch:  testBaseBranch,
	}
}

// harness 是一次单元测试使用的组件集合。
type harness struct {
	repo     *fakeRepo
	recorder *memoryRecorder
	saver    *codesaver.Saver
}

// newHarness 构造使用替身的代码保存组件。
func newHarness(t *testing.T) *harness {
	t.Helper()
	repo := newFakeRepo()
	recorder := newRecorder()
	saver, err := codesaver.New(codesaver.Options{
		Executor: executor{repo: repo},
		Guard:    &fakeGuard{container: newContainer()},
		Recorder: recorder,
	})
	if err != nil {
		t.Fatalf("构造代码保存组件失败: %v", err)
	}
	return &harness{repo: repo, recorder: recorder, saver: saver}
}

// TestRejectDockerIDMismatch 校验登记的 Docker 容器 ID 与实际不一致时拒绝执行。
func TestRejectDockerIDMismatch(t *testing.T) {
	t.Parallel()
	workspace := testkit.New(t)
	t.Logf("测试工作空间: %s", workspace.Root())
	instance := newHarness(t)
	request := baseRequest()
	request.DockerID = "docker-container-other"
	_, err := instance.saver.Run(context.Background(), request)
	if codesaver.ErrorCodeOf(err) != codesaver.CodeContainerNotFound {
		t.Fatalf("期望 container_not_found，实际 %v", err)
	}
	if instance.recorder.createsBeforePush() != 0 {
		t.Fatal("前置核验失败时不应写入保存记录")
	}
}

// TestRejectDetachedContainer 校验已脱离管理的容器被拒绝。
func TestRejectDetachedContainer(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	container := newContainer()
	container.Detached = true
	container.State = domain.ContainerStateDetached
	detachedAt := time.Now().UTC()
	container.DetachedAt = &detachedAt
	saver, err := codesaver.New(codesaver.Options{
		Executor: executor{repo: repo},
		Guard:    &fakeGuard{container: container},
		Recorder: newRecorder(),
	})
	if err != nil {
		t.Fatalf("构造代码保存组件失败: %v", err)
	}
	if _, runErr := saver.Run(context.Background(), baseRequest()); codesaver.ErrorCodeOf(
		runErr,
	) != codesaver.CodeContainerNotManaged {
		t.Fatalf("期望 container_not_managed，实际 %v", runErr)
	}
}

// TestRejectTaskBranchMismatch 校验当前分支不是任务分支时拒绝提交。
func TestRejectTaskBranchMismatch(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.taskBranch = testBaseBranch
	})
	_, err := instance.saver.Run(context.Background(), baseRequest())
	if codesaver.ErrorCodeOf(err) != codesaver.CodeTaskBranchMismatch {
		t.Fatalf("期望 task_branch_mismatch，实际 %v", err)
	}
	if instance.repo.count("commit") != 0 {
		t.Fatal("分支不匹配时不得执行提交")
	}
	record := instance.recorder.single(t)
	if record.ErrorCode != string(codesaver.CodeTaskBranchMismatch) {
		t.Fatalf("记录应保存失败分类，实际 %q", record.ErrorCode)
	}
}

// TestRejectDetachedHead 校验分离头指针被拒绝。
func TestRejectDetachedHead(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.taskBranch = "HEAD"
	})
	_, err := instance.saver.Run(context.Background(), baseRequest())
	if codesaver.ErrorCodeOf(err) != codesaver.CodeTaskBranchMismatch {
		t.Fatalf("期望 task_branch_mismatch，实际 %v", err)
	}
}

// TestRejectInvalidWorktree 校验目标目录不是 Git 仓库根目录时拒绝执行。
func TestRejectInvalidWorktree(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.overrides["rev-parse"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		for _, arg := range args {
			if arg == "--show-toplevel" {
				return failed(128, "fatal: not a git repository\n"), true
			}
		}
		return docker.CommandResult{}, false
	}
	_, err := instance.saver.Run(context.Background(), baseRequest())
	if codesaver.ErrorCodeOf(err) != codesaver.CodeWorktreeInvalid {
		t.Fatalf("期望 worktree_invalid，实际 %v", err)
	}
}

// TestRejectWorktreeSubdirectory 校验目标目录不是仓库根目录时拒绝执行。
func TestRejectWorktreeSubdirectory(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.overrides["rev-parse"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		for _, arg := range args {
			if arg == "--show-toplevel" {
				return succeeded(domain.DefaultContainerWorkDir + "/sub\n"), true
			}
		}
		return docker.CommandResult{}, false
	}
	_, err := instance.saver.Run(context.Background(), baseRequest())
	if codesaver.ErrorCodeOf(err) != codesaver.CodeWorktreeInvalid {
		t.Fatalf("期望 worktree_invalid，实际 %v", err)
	}
}

// TestNoChanges 校验没有变更时不创建空提交。
func TestNoChanges(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	result, err := instance.saver.Run(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("无变更时不应失败: %v", err)
	}
	if !result.NoChanges {
		t.Fatal("期望记录 no_changes")
	}
	if instance.repo.count("commit") != 0 || instance.repo.count("add") != 0 {
		t.Fatal("没有变更时不得执行 add 或 commit")
	}
	record := instance.recorder.single(t)
	if record.CommitStatus != domain.CommitStatusNoChanges {
		t.Fatalf("提交状态应为 no_changes，实际 %s", record.CommitStatus)
	}
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("远程已包含提交时推送状态应为 succeeded，实际 %s", record.PushStatus)
	}
	if record.MergeStatus != domain.MergeStatusNotRequested {
		t.Fatalf("未请求合并时合并状态应为 not_requested，实际 %s", record.MergeStatus)
	}
}

// TestRejectUnresolvedConflict 校验存在未解决冲突时拒绝提交。
func TestRejectUnresolvedConflict(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.conflicts = true
	})
	_, err := instance.saver.Run(context.Background(), baseRequest())
	if codesaver.ErrorCodeOf(err) != codesaver.CodeUnresolvedConflict {
		t.Fatalf("期望 unresolved_conflict，实际 %v", err)
	}
	if instance.repo.count("commit") != 0 {
		t.Fatal("存在冲突时不得执行提交")
	}
	if instance.repo.count("push") != 0 {
		t.Fatal("存在冲突时不得执行推送")
	}
}

// TestAuthorMissing 校验缺少提交身份时明确报错。
func TestAuthorMissing(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.repo.overrides["config"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		for _, arg := range args {
			if arg == "user.name" || arg == "user.email" {
				return failed(1, ""), true
			}
		}
		return docker.CommandResult{}, false
	}
	_, err := instance.saver.Run(context.Background(), baseRequest())
	if codesaver.ErrorCodeOf(err) != codesaver.CodeAuthorMissing {
		t.Fatalf("期望 author_missing，实际 %v", err)
	}
	if instance.repo.count("commit") != 0 {
		t.Fatal("缺少提交身份时不得执行提交")
	}
}

// TestCommitUsesAuthorConfigOnce 校验作者身份与提交信息按参数传递。
func TestCommitUsesAuthorConfigOnce(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	request := baseRequest()
	request.Author = domain.GitAuthor{Name: "Task Author", Email: "author@example.com"}
	request.CommitMessage = `任务: 支持 "引号" 与 ; 分号 $(whoami)`
	result, err := instance.saver.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("提交应成功: %v", err)
	}
	commitArgs := instance.commitArgs(t)
	if !containsPair(commitArgs, "-c", "user.name=Task Author") {
		t.Fatalf("提交命令缺少一次性作者姓名配置: %v", commitArgs)
	}
	if !containsPair(commitArgs, "-c", "user.email=author@example.com") {
		t.Fatalf("提交命令缺少一次性作者邮箱配置: %v", commitArgs)
	}
	if !containsPair(commitArgs, "-m", request.CommitMessage) {
		t.Fatalf("提交信息必须作为独立参数传递: %v", commitArgs)
	}
	if result.Record.CommitSHA != testCommitSHA {
		t.Fatalf("提交 SHA 应为 %s，实际 %s", testCommitSHA, result.Record.CommitSHA)
	}
	if instance.recorder.single(t).CommitStatus != domain.CommitStatusSucceeded {
		t.Fatal("提交状态应为 succeeded")
	}
}

// TestDefaultCommitMessage 校验未提供提交信息时使用可追踪的默认信息。
func TestDefaultCommitMessage(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	if _, err := instance.saver.Run(context.Background(), baseRequest()); err != nil {
		t.Fatalf("提交应成功: %v", err)
	}
	commitArgs := instance.commitArgs(t)
	if !containsPair(commitArgs, "-m", "AgentFlow: save task "+testTaskID) {
		t.Fatalf("默认提交信息不符合约定: %v", commitArgs)
	}
}

// TestCommitHeadChangedDespiteError 校验提交命令异常但 HEAD 已变化时按成功处理。
func TestCommitHeadChangedDespiteError(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.repo.overrides["commit"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		// 提交实际生效，但远程执行未确认结束。
		repo.set(func() {
			repo.head = testCommitSHA
			repo.dirty = false
		})
		return uncertain(sshclient.CommandStatusConnectionError), true
	}
	result, err := instance.saver.Run(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("HEAD 已变化时应按成功处理: %v", err)
	}
	if result.Record.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("提交状态应为 succeeded，实际 %s", result.Record.CommitStatus)
	}
	if result.Record.CommitSHA != testCommitSHA {
		t.Fatalf("提交 SHA 应为 %s，实际 %s", testCommitSHA, result.Record.CommitSHA)
	}
	if instance.repo.count("commit") != 1 {
		t.Fatal("不得因为结果不确定而重复创建提交")
	}
}

// TestCommitFailureKeepsHead 校验提交失败且 HEAD 未变化时记录失败。
func TestCommitFailureKeepsHead(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.repo.overrides["commit"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		return failed(1, "error: gpg failed to sign the data\n"), true
	}
	_, err := instance.saver.Run(context.Background(), baseRequest())
	if codesaver.ErrorCodeOf(err) != codesaver.CodeCommitFailed {
		t.Fatalf("期望 commit_failed，实际 %v", err)
	}
	record := instance.recorder.single(t)
	if record.CommitStatus != domain.CommitStatusFailed {
		t.Fatalf("提交状态应为 failed，实际 %s", record.CommitStatus)
	}
	if record.PushStatus != domain.PushStatusNotAttempted {
		t.Fatalf("提交失败时推送状态应为 not_attempted，实际 %s", record.PushStatus)
	}
	if instance.repo.count("push") != 0 {
		t.Fatal("提交失败后不得推送")
	}
}

// TestStagedChangesAreCommittedNotReset 校验既有暂存内容被提交而不是被重置。
func TestStagedChangesAreCommittedNotReset(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.staged = true
	})
	if _, err := instance.saver.Run(context.Background(), baseRequest()); err != nil {
		t.Fatalf("既有暂存内容应可提交: %v", err)
	}
	for _, subcommand := range instance.repo.recorded() {
		switch subcommand {
		case "reset", "clean":
			t.Fatalf("不得执行破坏性命令: %s", subcommand)
		}
	}
	if instance.repo.count("add") != 1 {
		t.Fatal("提交前应把变更加入暂存区")
	}
}

// TestPushOnlyTaskBranchWithoutForce 校验只推送任务分支且不使用强制推送。
func TestPushOnlyTaskBranchWithoutForce(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	if _, err := instance.saver.Run(context.Background(), baseRequest()); err != nil {
		t.Fatalf("保存流程应成功: %v", err)
	}
	pushArgs := instance.pushArgs(t, testTaskBranch)
	if !containsArg(pushArgs, testTaskBranch+":refs/heads/"+testTaskBranch) {
		t.Fatalf("推送目标必须是任务分支: %v", pushArgs)
	}
	for _, arg := range pushArgs {
		switch arg {
		case "--force", "--force-with-lease", "-f", "--all", "--mirror", "--tags":
			t.Fatalf("不得使用强制或全量推送: %v", pushArgs)
		}
	}
}

// TestPushRemoteMissing 校验远程名称缺失时拒绝推送。
func TestPushRemoteMissing(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.repo.overrides["config"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		for _, arg := range args {
			if arg == "remote."+testRemoteName+".url" {
				return failed(1, ""), true
			}
		}
		return docker.CommandResult{}, false
	}
	_, err := instance.saver.Run(context.Background(), baseRequest())
	if codesaver.ErrorCodeOf(err) != codesaver.CodeRemoteInvalid {
		t.Fatalf("期望 remote_invalid，实际 %v", err)
	}
}

// TestPushRejectsCredentialURL 校验远程地址含内嵌凭据时拒绝继续。
func TestPushRejectsCredentialURL(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.repo.overrides["config"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		for _, arg := range args {
			if arg == "remote."+testRemoteName+".url" {
				return succeeded("https://user:token@git.example.com/group/repo.git\n"), true
			}
		}
		return docker.CommandResult{}, false
	}
	_, err := instance.saver.Run(context.Background(), baseRequest())
	if codesaver.ErrorCodeOf(err) != codesaver.CodeRemoteInvalid {
		t.Fatalf("期望 remote_invalid，实际 %v", err)
	}
	if strings.Contains(err.Error(), "token") {
		t.Fatalf("错误信息不得包含凭据: %v", err)
	}
	record := instance.recorder.single(t)
	if strings.Contains(record.ErrorSummary, "token") {
		t.Fatalf("记录摘要不得包含凭据: %s", record.ErrorSummary)
	}
	if strings.Contains(record.Repository, "token") {
		t.Fatalf("记录仓库地址不得包含凭据: %s", record.Repository)
	}
}

// TestPushAuthFailure 校验认证失败被分类为凭据问题。
func TestPushAuthFailure(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.repo.overrides["push"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		return failed(128, "fatal: Authentication failed for repository\n"), true
	}
	_, err := instance.saver.Run(context.Background(), baseRequest())
	if codesaver.ErrorCodeOf(err) != codesaver.CodeRepositoryAuthFailed {
		t.Fatalf("期望 repository_auth_failed，实际 %v", err)
	}
	record := instance.recorder.single(t)
	if record.PushStatus != domain.PushStatusFailed {
		t.Fatalf("推送状态应为 failed，实际 %s", record.PushStatus)
	}
	if record.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatal("提交状态不应被推送失败覆盖")
	}
}

// TestPushNonFastForwardIsConflict 校验非快进推送被识别为冲突。
func TestPushNonFastForwardIsConflict(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.repo.overrides["push"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		return failed(1, "! [rejected] agent/task-1 -> agent/task-1 (non-fast-forward)\n"), true
	}
	_, err := instance.saver.Run(context.Background(), baseRequest())
	if codesaver.ErrorCodeOf(err) != codesaver.CodePushConflict {
		t.Fatalf("期望 push_conflict，实际 %v", err)
	}
	if instance.recorder.single(t).PushStatus != domain.PushStatusConflict {
		t.Fatal("推送状态应为 conflict")
	}
	if instance.repo.count("push") != 1 {
		t.Fatal("冲突后不得自动重试推送")
	}
}

// TestPushTimeoutButRemoteAccepted 校验推送超时但远程已收到提交时判定成功。
func TestPushTimeoutButRemoteAccepted(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.repo.overrides["push"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		// 远程已经接受推送，但本地没有收到成功响应。
		repo.set(func() {
			repo.remoteTask = repo.head
		})
		return uncertain(sshclient.CommandStatusTimeout), true
	}
	result, err := instance.saver.Run(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("远程已包含提交时应判定推送成功: %v", err)
	}
	record := instance.recorder.single(t)
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("推送状态应为 succeeded，实际 %s", record.PushStatus)
	}
	if record.RemoteCommitSHA != testCommitSHA {
		t.Fatalf("远程 commit 应为 %s，实际 %s", testCommitSHA, record.RemoteCommitSHA)
	}
	if result.Record.CommitSHA != testCommitSHA {
		t.Fatal("提交记录不应受推送结果影响")
	}
	if instance.repo.count("push") != 1 {
		t.Fatal("状态确认前不得重复推送")
	}
}

// TestPushUnknownWhenRemoteUnverifiable 校验无法核验远程时记录 unknown。
func TestPushUnknownWhenRemoteUnverifiable(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.repo.overrides["push"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		return uncertain(sshclient.CommandStatusTimeout), true
	}
	instance.repo.overrides["ls-remote"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		return uncertain(sshclient.CommandStatusTimeout), true
	}
	_, err := instance.saver.Run(context.Background(), baseRequest())
	if codesaver.ErrorCodeOf(err) != codesaver.CodeStateUncertain {
		t.Fatalf("期望 state_uncertain，实际 %v", err)
	}
	record := instance.recorder.single(t)
	if record.PushStatus != domain.PushStatusUnknown {
		t.Fatalf("推送状态应为 unknown，实际 %s", record.PushStatus)
	}
	if record.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatal("提交状态不应被推送结果覆盖")
	}
	if instance.repo.count("push") != 1 {
		t.Fatal("结果未知时不得盲目重复推送")
	}
}

// TestPushRemoteMismatchIsConflict 校验远程与本地不一致时记录冲突。
func TestPushRemoteMismatchIsConflict(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.repo.overrides["ls-remote"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		for _, arg := range args {
			switch arg {
			case "refs/heads/" + testTaskBranch:
				return succeeded(testRemoteDiverge + "\trefs/heads/" + testTaskBranch + "\n"), true
			case "refs/heads/" + testBaseBranch:
				return succeeded(testBaseRemoteSHA + "\trefs/heads/" + testBaseBranch + "\n"), true
			}
		}
		return docker.CommandResult{}, false
	}
	_, err := instance.saver.Run(context.Background(), baseRequest())
	if codesaver.ErrorCodeOf(err) != codesaver.CodePushConflict {
		t.Fatalf("期望 push_conflict，实际 %v", err)
	}
	record := instance.recorder.single(t)
	if record.PushStatus != domain.PushStatusConflict {
		t.Fatalf("推送状态应为 conflict，实际 %s", record.PushStatus)
	}
	if record.RemoteCommitSHA != testRemoteDiverge {
		t.Fatalf("应保留核验到的远程 commit，实际 %s", record.RemoteCommitSHA)
	}
}

// TestMergeNotRequestedByDefault 校验默认不合并基础分支。
func TestMergeNotRequestedByDefault(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	if _, err := instance.saver.Run(context.Background(), baseRequest()); err != nil {
		t.Fatalf("保存流程应成功: %v", err)
	}
	record := instance.recorder.single(t)
	if record.MergeEnabled {
		t.Fatal("默认不应启用合并")
	}
	if record.MergeStatus != domain.MergeStatusNotRequested {
		t.Fatalf("合并状态应为 not_requested，实际 %s", record.MergeStatus)
	}
	for _, subcommand := range instance.repo.recorded() {
		switch subcommand {
		case "merge", "checkout", "fetch":
			t.Fatalf("未授权时不得触碰基础分支: %s", subcommand)
		}
	}
}

// TestMergeRequiresSuccessfulPush 校验推送未成功时禁止合并。
func TestMergeRequiresSuccessfulPush(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.repo.overrides["push"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		return failed(1, "fatal: unable to access remote\n"), true
	}
	request := baseRequest()
	request.MergeAfterPush = true
	_, err := instance.saver.Run(context.Background(), request)
	if codesaver.ErrorCodeOf(err) != codesaver.CodePushFailed {
		t.Fatalf("期望 push_failed，实际 %v", err)
	}
	record := instance.recorder.single(t)
	if record.MergeStatus == domain.MergeStatusSucceeded {
		t.Fatal("推送失败时不得记录合并成功")
	}
	if instance.repo.count("merge") != 0 {
		t.Fatal("推送失败后不得执行合并")
	}
}

// TestMergeBaseBranchMissing 校验无法识别基础分支时记录跳过。
func TestMergeBaseBranchMissing(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.repo.overrides["symbolic-ref"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		return failed(1, ""), true
	}
	instance.repo.overrides["for-each-ref"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		return succeeded(""), true
	}
	request := baseRequest()
	request.BaseBranch = ""
	request.MergeAfterPush = true
	_, err := instance.saver.Run(context.Background(), request)
	if codesaver.ErrorCodeOf(err) != codesaver.CodeBaseBranchMissing {
		t.Fatalf("期望 base_branch_missing，实际 %v", err)
	}
	record := instance.recorder.single(t)
	if record.MergeStatus != domain.MergeStatusSkipped {
		t.Fatalf("合并状态应为 skipped，实际 %s", record.MergeStatus)
	}
	if record.PushStatus != domain.PushStatusNotAttempted {
		t.Fatalf("基础分支缺失时不应继续推送，实际 %s", record.PushStatus)
	}
	if instance.repo.count("push") != 0 {
		t.Fatal("基础分支缺失时不得推送")
	}
}

// TestMergeConflictKeepsPushSuccess 校验合并冲突时任务分支仍保持已推送状态。
func TestMergeConflictKeepsPushSuccess(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.repo.overrides["merge"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		for _, arg := range args {
			switch arg {
			case "--abort":
				return succeeded(""), true
			case "--ff-only":
				return docker.CommandResult{}, false
			}
		}
		return failed(1, "CONFLICT (content): Merge conflict in internal/app/app.go\n"+
			"Automatic merge failed; fix conflicts and then commit the result.\n"), true
	}
	request := baseRequest()
	request.MergeAfterPush = true
	_, err := instance.saver.Run(context.Background(), request)
	if codesaver.ErrorCodeOf(err) != codesaver.CodeMergeConflict {
		t.Fatalf("期望 merge_conflict，实际 %v", err)
	}
	record := instance.recorder.single(t)
	if record.MergeStatus != domain.MergeStatusConflict {
		t.Fatalf("合并状态应为 conflict，实际 %s", record.MergeStatus)
	}
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("任务分支应保持推送成功，实际 %s", record.PushStatus)
	}
	if !record.SavedToRemote() {
		t.Fatal("任务分支已推送时 SavedToRemote 应为真")
	}
	for _, subcommand := range instance.repo.recorded() {
		switch subcommand {
		case "reset", "clean":
			t.Fatalf("合并冲突时不得重置仓库: %s", subcommand)
		}
	}
}

// TestMergePushFailureKeepsPushSuccess 校验主分支推送失败时任务分支状态不变。
func TestMergePushFailureKeepsPushSuccess(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.repo.overrides["push"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		for _, arg := range args {
			if strings.HasPrefix(arg, testBaseBranch+":refs/heads/") {
				return failed(1, "fatal: unable to access remote\n"), true
			}
		}
		return docker.CommandResult{}, false
	}
	request := baseRequest()
	request.MergeAfterPush = true
	_, err := instance.saver.Run(context.Background(), request)
	if codesaver.ErrorCodeOf(err) != codesaver.CodeMergeFailed {
		t.Fatalf("期望 merge_failed，实际 %v", err)
	}
	record := instance.recorder.single(t)
	if record.MergeStatus != domain.MergeStatusFailed {
		t.Fatalf("合并状态应为 failed，实际 %s", record.MergeStatus)
	}
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("任务分支应保持推送成功，实际 %s", record.PushStatus)
	}
}

// TestMergeSuccessVerifiesRemoteCommit 校验合并成功后核验远程基础分支 commit。
func TestMergeSuccessVerifiesRemoteCommit(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	request := baseRequest()
	request.MergeAfterPush = true
	result, err := instance.saver.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("合并应成功: %v", err)
	}
	record := instance.recorder.single(t)
	if record.MergeStatus != domain.MergeStatusSucceeded {
		t.Fatalf("合并状态应为 succeeded，实际 %s", record.MergeStatus)
	}
	if record.MergeSHA != testMergeSHA {
		t.Fatalf("合并 commit 应为 %s，实际 %s", testMergeSHA, record.MergeSHA)
	}
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("任务分支应推送成功，实际 %s", record.PushStatus)
	}
	if !result.Succeeded() {
		t.Fatal("完整保存流程应判定成功")
	}
	pushArgs := instance.pushArgs(t, testBaseBranch)
	if !containsArg(pushArgs, testBaseBranch+":refs/heads/"+testBaseBranch) {
		t.Fatalf("合并后应推送基础分支: %v", pushArgs)
	}
}

// TestMergeRefreshesRemoteBase 校验合并前刷新远程基础分支最新状态。
func TestMergeRefreshesRemoteBase(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	// 模拟基础分支在执行期间被其他人更新：远程已经前移。
	instance.repo.set(func() {
		instance.repo.remoteBase = testRemoteDiverge
	})
	request := baseRequest()
	request.MergeAfterPush = true
	if _, err := instance.saver.Run(context.Background(), request); err != nil {
		t.Fatalf("合并应成功: %v", err)
	}
	if instance.repo.count("fetch") != 1 {
		t.Fatal("合并前必须刷新远程引用")
	}
	record := instance.recorder.single(t)
	if record.MergeSHA != testMergeSHA {
		t.Fatalf("合并结果应以刷新后的状态为准，实际 %s", record.MergeSHA)
	}
}

// TestMergeUnknownWhenRemoteUnverifiable 校验合并结果无法核验时记录 unknown。
func TestMergeUnknownWhenRemoteUnverifiable(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.repo.overrides["push"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		for _, arg := range args {
			if strings.HasPrefix(arg, testBaseBranch+":refs/heads/") {
				return uncertain(sshclient.CommandStatusTimeout), true
			}
		}
		return docker.CommandResult{}, false
	}
	baseQueried := false
	instance.repo.overrides["ls-remote"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		for _, arg := range args {
			switch arg {
			case "refs/heads/" + testTaskBranch:
				return succeeded(testCommitSHA + "\trefs/heads/" + testTaskBranch + "\n"), true
			case "refs/heads/" + testBaseBranch:
				if baseQueried {
					return uncertain(sshclient.CommandStatusTimeout), true
				}
				baseQueried = true
				return succeeded(testBaseRemoteSHA + "\trefs/heads/" + testBaseBranch + "\n"), true
			}
		}
		return docker.CommandResult{}, false
	}
	request := baseRequest()
	request.MergeAfterPush = true
	_, err := instance.saver.Run(context.Background(), request)
	if codesaver.ErrorCodeOf(err) != codesaver.CodeStateUncertain {
		t.Fatalf("期望 state_uncertain，实际 %v", err)
	}
	record := instance.recorder.single(t)
	if record.MergeStatus != domain.MergeStatusUnknown {
		t.Fatalf("合并状态应为 unknown，实际 %s", record.MergeStatus)
	}
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatal("任务分支应保持推送成功")
	}
}

// TestMergeOperationRequiresPushedRecord 校验单独合并操作需要已推送记录。
func TestMergeOperationRequiresPushedRecord(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	request := baseRequest()
	request.Operation = codesaver.OperationMerge
	_, err := instance.saver.Merge(context.Background(), request)
	if codesaver.ErrorCodeOf(err) != codesaver.CodeMergeNotAllowed {
		t.Fatalf("期望 merge_not_allowed，实际 %v", err)
	}
	if instance.repo.count("merge") != 0 {
		t.Fatal("缺少已推送记录时不得执行合并")
	}
}

// TestMergeOperationReusesSavedRecord 校验单独合并沿用已推送记录的状态。
func TestMergeOperationReusesSavedRecord(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	if _, err := instance.saver.Run(context.Background(), baseRequest()); err != nil {
		t.Fatalf("保存流程应成功: %v", err)
	}
	request := baseRequest()
	request.Operation = codesaver.OperationMerge
	result, err := instance.saver.Merge(context.Background(), request)
	if err != nil {
		t.Fatalf("合并应成功: %v", err)
	}
	if result.Record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("合并记录应沿用已推送状态，实际 %s", result.Record.PushStatus)
	}
	if result.Record.MergeStatus != domain.MergeStatusSucceeded {
		t.Fatalf("合并状态应为 succeeded，实际 %s", result.Record.MergeStatus)
	}
}

// TestPushOperationDoesNotMerge 校验只推送时不触碰基础分支。
func TestPushOperationDoesNotMerge(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	request := baseRequest()
	request.Operation = codesaver.OperationPush
	request.MergeAfterPush = true
	if _, err := instance.saver.Push(context.Background(), request); err != nil {
		t.Fatalf("推送应成功: %v", err)
	}
	for _, subcommand := range instance.repo.recorded() {
		switch subcommand {
		case "merge", "checkout", "fetch":
			t.Fatalf("只推送时不得触碰基础分支: %s", subcommand)
		}
	}
}

// TestRecordPersistedBeforeRemoteOperations 校验记录在远程操作前已落盘。
func TestRecordPersistedBeforeRemoteOperations(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	var creates int
	instance.repo.onPush = func() {
		creates = instance.recorder.createsBeforePush()
	}
	if _, err := instance.saver.Run(context.Background(), baseRequest()); err != nil {
		t.Fatalf("保存流程应成功: %v", err)
	}
	if creates == 0 {
		t.Fatal("推送前必须已写入保存记录")
	}
	instance.recorder.mu.Lock()
	updates := instance.recorder.updates
	instance.recorder.mu.Unlock()
	if updates == 0 {
		t.Fatal("状态推进后必须更新保存记录")
	}
}

// TestRecordFailureIsReported 校验记录写入失败时明确报错。
func TestRecordFailureIsReported(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	repo.set(func() {
		repo.dirty = true
	})
	recorder := newRecorder()
	recorder.err = errors.New("磁盘不可写")
	saver, err := codesaver.New(codesaver.Options{
		Executor: executor{repo: repo},
		Guard:    &fakeGuard{container: newContainer()},
		Recorder: recorder,
	})
	if err != nil {
		t.Fatalf("构造代码保存组件失败: %v", err)
	}
	if _, runErr := saver.Run(context.Background(), baseRequest()); codesaver.ErrorCodeOf(
		runErr,
	) != codesaver.CodeRecordFailed {
		t.Fatalf("期望 record_failed，实际 %v", runErr)
	}
	if repo.count("push") != 0 {
		t.Fatal("记录写入失败时不得继续推送")
	}
}

// TestConcurrentSavesAreIsolated 校验并发保存互不覆盖记录。
func TestConcurrentSavesAreIsolated(t *testing.T) {
	t.Parallel()
	recorder := newRecorder()
	const total = 4
	done := make(chan error, total)
	for index := 0; index < total; index++ {
		go func(offset int) {
			// 每个任务使用独立的仓库状态，只共享记录仓储：
			// 这样才能验证并发写入不会互相覆盖记录。
			repo := newFakeRepo()
			repo.set(func() {
				repo.dirty = true
				repo.taskBranch = "agent/task-" + itoa(offset)
				repo.remoteTask = ""
			})
			saver, err := codesaver.New(codesaver.Options{
				Executor: executor{repo: repo},
				Guard:    &fakeGuard{container: newContainer()},
				Recorder: recorder,
			})
			if err != nil {
				done <- err
				return
			}
			request := baseRequest()
			request.TaskID = "task-" + itoa(offset)
			_, runErr := saver.Run(context.Background(), request)
			done <- runErr
		}(index)
	}
	for index := 0; index < total; index++ {
		if err := <-done; err != nil {
			t.Fatalf("并发保存不应失败: %v", err)
		}
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.records) != total {
		t.Fatalf("每个任务都应有独立记录，实际 %d 条", len(recorder.records))
	}
	for _, record := range recorder.records {
		if record.TaskBranch != "agent/"+record.TaskID {
			t.Fatalf("任务分支与任务标识不一致: %s / %s", record.TaskID, record.TaskBranch)
		}
		if record.PushStatus != domain.PushStatusSucceeded {
			t.Fatalf("每个任务的推送状态都应独立成功，实际 %s", record.PushStatus)
		}
	}
}

// TestCancellationDoesNotTouchOtherTask 校验取消不会误改其他任务的状态。
func TestCancellationDoesNotTouchOtherTask(t *testing.T) {
	t.Parallel()
	recorder := newRecorder()
	first := newFakeRepo()
	first.set(func() {
		first.dirty = true
	})
	firstSaver, err := codesaver.New(codesaver.Options{
		Executor: executor{repo: first},
		Guard:    &fakeGuard{container: newContainer()},
		Recorder: recorder,
	})
	if err != nil {
		t.Fatalf("构造代码保存组件失败: %v", err)
	}
	if _, runErr := firstSaver.Run(context.Background(), baseRequest()); runErr != nil {
		t.Fatalf("第一次保存应成功: %v", runErr)
	}
	second := newFakeRepo()
	second.set(func() {
		second.dirty = true
		second.taskBranch = "agent/task-2"
		second.remoteTask = ""
	})
	second.overrides["push"] = func(repo *fakeRepo, args []string) (docker.CommandResult, bool) {
		return uncertain(sshclient.CommandStatusCancelled), true
	}
	secondSaver, err := codesaver.New(codesaver.Options{
		Executor: executor{repo: second},
		Guard:    &fakeGuard{container: newContainer()},
		Recorder: recorder,
	})
	if err != nil {
		t.Fatalf("构造代码保存组件失败: %v", err)
	}
	request := baseRequest()
	request.TaskID = "task-2"
	_, runErr := secondSaver.Run(context.Background(), request)
	if codesaver.ErrorCodeOf(runErr) != codesaver.CodeStateUncertain {
		t.Fatalf("期望 state_uncertain，实际 %v", runErr)
	}
	records, listErr := recorder.ListByTask(context.Background(), testTaskID)
	if listErr != nil {
		t.Fatalf("读取记录失败: %v", listErr)
	}
	if len(records) != 1 || records[0].PushStatus != domain.PushStatusSucceeded {
		t.Fatal("取消另一个任务不应影响已成功任务的状态")
	}
}

// commitArgs 返回提交命令的 Git 参数。
func (h *harness) commitArgs(t *testing.T) []string {
	t.Helper()
	// 通过重新执行一次提交路径无法拿到参数，因此这里从记录的命令序列推断：
	// fakeRepo 只在默认实现中更新 HEAD，参数通过 overrides 捕获。
	if h.repo.commitArgs == nil {
		t.Fatal("没有收到提交命令")
	}
	return h.repo.commitArgs
}

// pushArgs 返回推送指定分支的命令参数。
func (h *harness) pushArgs(t *testing.T, branch string) []string {
	t.Helper()
	for _, args := range h.repo.pushCalls {
		if containsArg(args, branch+":refs/heads/"+branch) {
			return args
		}
	}
	t.Fatalf("没有收到推送分支 %s 的命令", branch)
	return nil
}

// containsArg 判断命令中是否包含指定参数。
func containsArg(command []string, target string) bool {
	for _, arg := range command {
		if arg == target {
			return true
		}
	}
	return false
}

// containsPair 判断命令中是否包含相邻的两个参数。
func containsPair(command []string, first, second string) bool {
	for index := 0; index+1 < len(command); index++ {
		if command[index] == first && command[index+1] == second {
			return true
		}
	}
	return false
}

// itoa 把整数转换为十进制字符串。
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// TestPersistFailureAfterCommitKeepsGitResult 校验"Git 已提交但记录没落盘"。
//
// 这是本轮最关键的失败窗口：内存中提交已经成功，数据库里仍是 pending。
// 结果必须同时表达"Git 成功"与"记录未落盘"，且返回的错误类别必须是
// state_unconfirmed，而不是普通的 record_failed。
func TestPersistFailureAfterCommitKeepsGitResult(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	// 只让"提交成功"那一次写入失败，其余阶段正常落盘。
	instance.recorder.failUpdateWhen(errors.New("database is locked"), func(record domain.CodeSaveRecord) bool {
		return record.CommitStatus == domain.CommitStatusSucceeded &&
			record.PushStatus == domain.PushStatusPending
	})
	result, err := instance.saver.Run(context.Background(), baseRequest())
	if err == nil {
		t.Fatal("记录写入失败时必须返回错误")
	}
	if codesaver.ErrorCodeOf(err) != codesaver.CodeStateUnconfirmed {
		t.Fatalf("期望 state_unconfirmed，实际 %v", err)
	}
	if !result.Record.PersistStatus.IsUnconfirmed() {
		t.Fatal("写入失败时记录必须被标记为未确认")
	}
	if len(result.PersistenceErrors) == 0 {
		t.Fatal("必须保留写入失败摘要")
	}
	// Git 的事实不能被写坏：内存记录仍是提交成功。
	if result.Record.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("内存记录应保留真实的提交结论，实际 %s", result.Record.CommitStatus)
	}
	if result.Record.PersistStatus.IsUnconfirmed() == false {
		t.Fatal("内存记录必须被标记为未确认")
	}
}

// TestPersistFailureLeavesPendingInDatabase 校验数据库确实停在 pending。
//
// 该用例与上一条互补：上一条验证返回值，这一条验证"记录落后于真实 Git 状态"
// 这个事实真的发生了，后续恢复判定必须依赖它。
func TestPersistFailureLeavesPendingInDatabase(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	instance.recorder.failUpdateWhen(errors.New("database is locked"), func(record domain.CodeSaveRecord) bool {
		return record.CommitStatus == domain.CommitStatusSucceeded &&
			record.PushStatus == domain.PushStatusPending
	})
	// 让提交成功后的所有写入都失败：数据库会停留在"基线已落盘、提交仍 pending"，
	// 这正是需要被恢复判定识别出来的窗口。
	instance.recorder.failUpdateWhen(errors.New("database is locked"), func(record domain.CodeSaveRecord) bool {
		return record.CommitStatus != domain.CommitStatusPending
	})
	result, err := instance.saver.Run(context.Background(), baseRequest())
	if err == nil {
		t.Fatal("记录写入失败时必须返回错误")
	}
	stored := instance.recorder.snapshot(t, result.Record.ID)
	if stored.CommitStatus != domain.CommitStatusPending {
		t.Fatalf("数据库应停留在 pending，实际 %s", stored.CommitStatus)
	}
	if stored.BaselineCommitSHA != testHeadCommit {
		t.Fatalf("提交基线必须在副作用之前落盘，实际 %q", stored.BaselineCommitSHA)
	}
	// 有了基线，恢复判定就可以确认"提交已经发生"。
	decision := domain.DecideCodeSaveRecovery(stored, domain.CodeSaveObservation{
		HeadCommit: testCommitSHA,
	})
	if decision.Status != domain.RecoveryStatusConfirmedApplied {
		t.Fatalf("依据基线应确认提交已发生，实际 %s", decision.Status)
	}
	if decision.CommitSHA != testCommitSHA {
		t.Fatalf("确认的提交应是新的 HEAD，实际 %s", decision.CommitSHA)
	}
	if decision.SafeToRetry {
		t.Fatal("提交已发生时不得提示可以重试")
	}
}

// TestPersistFailureRecoversOnNextWrite 校验短暂写入故障会被后续写入修复。
func TestPersistFailureRecoversOnNextWrite(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	// 只让一次中间写入失败：后续阶段推进时会再次落盘。
	// 该次失败会中断本次调用（失败状态无法确认，不能继续推送），
	// 但数据库中的记录必须仍然可用，并且不再被标记为未确认。
	instance.recorder.failUpdateWhen(errors.New("database is locked"), func(record domain.CodeSaveRecord) bool {
		return record.CommitStatus == domain.CommitStatusSucceeded &&
			record.PushStatus == domain.PushStatusPending
	})
	if _, err := instance.saver.Run(context.Background(), baseRequest()); err == nil {
		t.Fatal("状态写入失败时必须返回错误，不能静默继续")
	}
	// 第一次调用因提交阶段落盘失败而中断，数据库停在"基线已落盘、提交 pending"。
	first := instance.recorder.single(t)
	if first.CommitStatus != domain.CommitStatusPending {
		t.Fatalf("第一次调用后数据库应停留在 pending，实际 %s", first.CommitStatus)
	}
	// 故障消失后重新执行：新的写入必须成功，记录完整落盘且不再标记为未确认。
	if _, err := instance.saver.Run(context.Background(), baseRequest()); err != nil {
		t.Fatalf("故障恢复后应能成功保存: %v", err)
	}
	records, listErr := instance.recorder.ListByTask(context.Background(), testTaskID)
	if listErr != nil {
		t.Fatalf("读取记录失败: %v", listErr)
	}
	if len(records) != 2 {
		t.Fatalf("两次执行应产生两条独立记录，实际 %d 条", len(records))
	}
	// ListByTask 的返回顺序由替身决定，这里按 ID 找到第二次执行的记录。
	var second domain.CodeSaveRecord
	for _, candidate := range records {
		if candidate.ID != first.ID {
			second = candidate
		}
	}
	if second.ID.IsEmpty() {
		t.Fatal("没有找到第二次执行的记录")
	}
	if second.CommitStatus != domain.CommitStatusNoChanges {
		t.Fatalf("第二次执行时工作区已干净，应记录 no_changes，实际 %s", second.CommitStatus)
	}
	if second.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("推送状态应完整落盘，实际 %s", second.PushStatus)
	}
	if second.PersistStatus.IsUnconfirmed() {
		t.Fatalf("写入成功后不得保留未确认标记，实际 %s", second.PersistStatus)
	}
	if second.PersistError != "" {
		t.Fatalf("写入成功后必须清空失败摘要，实际 %q", second.PersistError)
	}
}

// TestFailUsesUncancelledContext 校验取消不会让失败记录写不进去。
//
// fail 必须使用未取消的上下文：调用方取消是失败原因之一，不能同时成为
// "失败记录写不进去"的原因，否则中断现场会永久丢失。
func TestFailUsesUncancelledContext(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	ctx, cancel := context.WithCancel(context.Background())
	// 在推送阶段取消：fail 会用未取消的上下文写入失败记录。
	instance.repo.overrides["push"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		cancel()
		return uncertain(sshclient.CommandStatusCancelled), true
	}
	_, err := instance.saver.Run(ctx, baseRequest())
	if err == nil {
		t.Fatal("取消后必须返回错误")
	}
	stored := instance.recorder.single(t)
	if stored.PushStatus == domain.PushStatusPending {
		t.Fatal("取消后推送状态必须被写为终态，不能停留在 pending")
	}
	if stored.FinishedAt == nil {
		t.Fatal("失败记录应写入结束时间")
	}
}

// TestCommitBaselineIsPersistedBeforeCommit 校验基线在提交命令之前落盘。
func TestCommitBaselineIsPersistedBeforeCommit(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	var baselineAtCommit string
	instance.repo.overrides["commit"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		// 提交命令到达时，数据库中的记录必须已经带有基线。
		instance.recorder.mu.Lock()
		for _, record := range instance.recorder.records {
			baselineAtCommit = record.BaselineCommitSHA
		}
		instance.recorder.mu.Unlock()
		repo.set(func() {
			repo.head = testCommitSHA
			repo.dirty = false
			repo.staged = false
		})
		return succeeded(""), true
	}
	if _, err := instance.saver.Run(context.Background(), baseRequest()); err != nil {
		t.Fatalf("保存流程应成功: %v", err)
	}
	if baselineAtCommit != testHeadCommit {
		t.Fatalf("提交前必须已落盘基线 %s，实际 %q", testHeadCommit, baselineAtCommit)
	}
}

// TestMergeBaselineIsPersistedBeforeMerge 校验合并基线在合并前落盘。
func TestMergeBaselineIsPersistedBeforeMerge(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	instance.repo.set(func() {
		instance.repo.dirty = true
	})
	var baselineBeforeMerge string
	instance.repo.overrides["merge"] = func(repo *fakeRepo, args []string) (
		docker.CommandResult,
		bool,
	) {
		for _, arg := range args {
			if arg == "--abort" {
				return succeeded(""), true
			}
		}
		instance.recorder.mu.Lock()
		for _, record := range instance.recorder.records {
			baselineBeforeMerge = record.BaselineBaseCommitSHA
		}
		instance.recorder.mu.Unlock()
		repo.set(func() {
			repo.head = testMergeSHA
		})
		return succeeded(""), true
	}
	request := baseRequest()
	request.MergeAfterPush = true
	if _, err := instance.saver.Run(context.Background(), request); err != nil {
		t.Fatalf("保存流程应成功: %v", err)
	}
	if baselineBeforeMerge != testBaseRemoteSHA {
		t.Fatalf("合并前必须已落盘基础分支基线 %s，实际 %q", testBaseRemoteSHA, baselineBeforeMerge)
	}
}

// TestObserveConfirmsCommitApplied 校验只读核验能确认提交已经发生。
func TestObserveConfirmsCommitApplied(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	record := domain.CodeSaveRecord{
		ID:                domain.NewID(),
		TaskID:            testTaskID,
		TaskBranch:        testTaskBranch,
		ContainerID:       testContainerID,
		RemoteName:        testRemoteName,
		CommitStatus:      domain.CommitStatusPending,
		PushStatus:        domain.PushStatusPending,
		MergeStatus:       domain.MergeStatusNotRequested,
		BaselineCommitSHA: testHeadCommit,
	}
	observation := instance.saver.Observe(context.Background(), baseRequest(), record)
	if observation.ReadFailed {
		t.Fatalf("核验不应失败: %s", observation.ReadError)
	}
	if observation.HeadCommit != testHeadCommit {
		t.Fatalf("HEAD 应为 %s，实际 %s", testHeadCommit, observation.HeadCommit)
	}
	decision := domain.DecideCodeSaveRecovery(record, observation)
	if decision.Status != domain.RecoveryStatusConfirmedNotApplied {
		t.Fatalf("HEAD 未变化时应确认提交未发生，实际 %s", decision.Status)
	}
	if !decision.SafeToRetry {
		t.Fatal("确认未发生时应提示可以安全重试")
	}
}

// TestObserveDoesNotWriteRecord 校验核验本身不写记录、不做有副作用的 Git 操作。
func TestObserveDoesNotWriteRecord(t *testing.T) {
	t.Parallel()
	instance := newHarness(t)
	record := domain.CodeSaveRecord{
		ID:                domain.NewID(),
		TaskID:            testTaskID,
		TaskBranch:        testTaskBranch,
		ContainerID:       testContainerID,
		RemoteName:        testRemoteName,
		CommitStatus:      domain.CommitStatusPending,
		PushStatus:        domain.PushStatusPending,
		MergeStatus:       domain.MergeStatusNotRequested,
		BaselineCommitSHA: testHeadCommit,
	}
	instance.recorder.mu.Lock()
	before := instance.recorder.updates
	instance.recorder.mu.Unlock()
	instance.saver.Observe(context.Background(), baseRequest(), record)
	instance.recorder.mu.Lock()
	after := instance.recorder.updates
	instance.recorder.mu.Unlock()
	if after != before {
		t.Fatalf("核验不得写入保存记录: %d -> %d", before, after)
	}
	for _, subcommand := range instance.repo.recorded() {
		switch subcommand {
		case "commit", "add", "push", "merge", "checkout", "fetch":
			t.Fatalf("核验不得执行有副作用的 Git 操作: %s", subcommand)
		}
	}
}
