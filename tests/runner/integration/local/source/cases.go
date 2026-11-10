// 本文件是 tests/integration/local/source/source_test.go 中 6 个用例的迁移结果。
//
// 改造点只有三类：函数头改成 func run*、删除 t.Parallel()、工作空间改为编排进程下发的
// t.WorkspaceDir()；断言、注释与表内逻辑逐行保持原样。
package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/source"
	"github.com/lingting/agentflow/internal/testrunner"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 登记本包 6 条用例，顺序与源文件一致，便于与 R0 冻结清单逐条对账。
var (
	_ = register("fetch-with-local-path", runFetchWithLocalPath)
	_ = register("fetch-base-branch-missing", runFetchBaseBranchMissing)
	_ = register("fetch-target-dir-not-empty-kept", runFetchTargetDirNotEmptyKept)
	_ = register("fetch-existing-remote-task-branch", runFetchExistingRemoteTaskBranch)
	_ = register("concurrent-fetches-do-not-share-work-dir", runConcurrentFetchesDoNotShareWorkDir)
	_ = register("repository-and-work-dir-kept-after-test", runRepositoryAndWorkDirKeptAfterTest)
)

// 原 TestFetchWithLocalPath 使用本机路径形式的仓库地址验证完整 Clone 流程。
//
// 仓库地址通过 ssh:// 形式表达，但执行替身把它映射到本机裸仓库，
// 因此 Git 命令、分支操作与 commit 校验都真实执行。
func runFetchWithLocalPath(t *testrunner.Context) {
	gitPath := requireGit(t)
	workspace := testkit.NewAt(t, t.WorkspaceDir())
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

// 原 TestFetchBaseBranchMissing 验证指定的基础分支不存在时失败。
func runFetchBaseBranchMissing(t *testrunner.Context) {
	gitPath := requireGit(t)
	workspace := testkit.NewAt(t, t.WorkspaceDir())
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

// 原 TestFetchTargetDirNotEmptyKept 验证目标目录非空时失败且内容不被删除。
func runFetchTargetDirNotEmptyKept(t *testrunner.Context) {
	gitPath := requireGit(t)
	workspace := testkit.NewAt(t, t.WorkspaceDir())
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

// 原 TestFetchExistingRemoteTaskBranch 验证远程已存在任务分支时检出该分支。
func runFetchExistingRemoteTaskBranch(t *testrunner.Context) {
	gitPath := requireGit(t)
	workspace := testkit.NewAt(t, t.WorkspaceDir())
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

// 原 TestConcurrentFetchesDoNotShareWorkDir 验证并发任务各自使用独立目录与分支状态。
func runConcurrentFetchesDoNotShareWorkDir(t *testrunner.Context) {
	gitPath := requireGit(t)
	workspace := testkit.NewAt(t, t.WorkspaceDir())
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

// 原 TestRepositoryAndWorkDirKeptAfterTest 确认测试产物保留在用例工作空间中。
func runRepositoryAndWorkDirKeptAfterTest(t *testrunner.Context) {
	gitPath := requireGit(t)
	workspace := testkit.NewAt(t, t.WorkspaceDir())
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
	// 仓库与克隆结果都必须存在于用例工作空间中，测试结束不删除。
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
