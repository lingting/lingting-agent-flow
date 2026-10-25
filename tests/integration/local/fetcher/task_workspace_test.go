// Package fetcher_local 验证任务级工作区初始化复用同一套代码获取实现。
//
// 用例使用测试工作空间内的真实 Git 仓库与一个把"容器内命令"映射到本机目录的
// 执行替身：Clone、分支创建与 commit 校验都经过真实 Git 执行，因此不需要
// SSH、Docker 或外网。
//
// 断言的重点不是"又一次实现 Git"，而是：任务级初始化与尝试级初始化走同一条
// source.Provider 路径，且不携带伪造的尝试归属。
package fetcher_local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/source"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// workspaceContainerID 是任务级初始化用例使用的容器登记记录标识。
const workspaceContainerID = "container-task-workspace"

// taskWorkspaceContainers 把任务级初始化对容器的需求接到本机执行替身。
//
// 它同时是容器核验与代码获取两个端口：核验返回一个运行中的容器记录，
// 代码获取复用 source.GitFetcher（也就是产品里 ContainerService.FetchSourceWith
// 使用的同一个实现）。
type taskWorkspaceContainers struct {
	fetcher source.Fetcher
	// fetches 记录收到的代码获取请求。
	fetches []source.FetchRequest
}

// InspectContainer 返回一个运行中的受管理容器。
func (c *taskWorkspaceContainers) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	return app.ContainerDetail{
		Container: domain.Container{
			ID:       domain.ID(request.ContainerID),
			DockerID: testDockerID,
			State:    domain.ContainerStateRunning,
		},
		Inspect: &docker.ContainerInspect{Running: true},
	}, nil
}

// FetchSourceWith 用真实的 source.GitFetcher 执行一次代码获取。
func (c *taskWorkspaceContainers) FetchSourceWith(
	ctx context.Context,
	containerID string,
	request source.FetchRequest,
) (source.FetchResult, error) {
	c.fetches = append(c.fetches, request)
	scoped := request
	scoped.ContainerID = containerID
	return c.fetcher.Fetch(ctx, scoped)
}

// taskWorkspaceFixture 是一套完整的任务级工作区初始化现场。
type taskWorkspaceFixture struct {
	ctx        context.Context
	db         *sqlite.DB
	service    *app.TaskWorkspaceService
	containers *taskWorkspaceContainers
	task       domain.Task
	repo       repository
	gitPath    string
	root       string
}

// newTaskWorkspaceFixture 在测试工作空间内创建数据库、任务与真实 Git 仓库。
func newTaskWorkspaceFixture(t *testing.T) *taskWorkspaceFixture {
	t.Helper()
	gitPath := requireGit(t)
	workspace := testkit.New(t)
	root := workspace.Dir("task-workspace-root")
	repo := newRepository(t, gitPath, workspace.Dir("task-workspace-repo"), "main")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	now := time.Now().UTC()
	host := newNormalHost(t, db, ctx, now)
	plannerRole := newWorkspaceRole(t, db, ctx, domain.RoleTypePlanner, "规划师", now)
	developerRole := newWorkspaceRole(t, db, ctx, domain.RoleTypeDeveloper, "开发工程师", now)
	requirement, err := domain.NewRequirement("需求标题", "需求正文", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	// 仓库地址必须是合法的 Git URL：真正的 Clone 目标由执行替身重写到本机裸仓库，
	// 这与产品里"地址来自任务记录、命令由 source 层构造"的分工一致。
	task, err := domain.NewTask(
		requirement.ID,
		"https://git.example.com/group/repo.git",
		"main",
		"",
		now,
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &host.ID
	seedOwnedContainer(t, db, ctx, task, workspaceContainerID, testDockerID)
	task.ContainerID = workspaceContainerID
	task.Config = domain.TaskConfig{
		Goal:            "完成本次需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		PlannerRoleID:   plannerRole.ID,
		DeveloperRoleID: developerRole.ID,
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	executor := &localExecutor{
		gitPath:          gitPath,
		root:             root,
		localCloneSource: repo.barePath,
	}
	fetcher, err := source.NewGitFetcher(source.Options{
		Executor:     executor,
		Guard:        &fakeGuard{container: workspaceContainer()},
		CloneTimeout: gitTimeout,
		ProbeTimeout: gitTimeout,
	})
	if err != nil {
		t.Fatalf("构造 Git 代码获取实现失败: %v", err)
	}
	containers := &taskWorkspaceContainers{fetcher: fetcher}
	service, err := app.NewTaskWorkspaceService(app.TaskWorkspaceOptions{
		Store:      db,
		Containers: containers,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造工作区初始化服务失败: %v", err)
	}
	return &taskWorkspaceFixture{
		ctx:        ctx,
		db:         db,
		service:    service,
		containers: containers,
		task:       task,
		repo:       repo,
		gitPath:    gitPath,
		root:       root,
	}
}

// newWorkspaceRole 写入一个任务配置所需的角色。
func newWorkspaceRole(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	roleType domain.RoleType,
	name string,
	now time.Time,
) domain.Role {
	t.Helper()
	role, err := domain.NewRole(
		name,
		roleType,
		"provider/model",
		"提示词",
		domain.DefaultToolsPolicy(roleType),
		now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := db.CreateRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	return role
}

// newNormalHost 写入一个已通过环境预检的执行节点。
func newNormalHost(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	now time.Time,
) domain.ExecutionHost {
	t.Helper()
	host, err := domain.NewExecutionHost(
		"",
		"测试节点",
		"10.0.0.1",
		22,
		"deploy",
		"/srv/agentflow",
		now,
		domain.WithPrivateKey(testkit.PrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	stored, err := db.GetExecutionHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	details := domain.ValidationDetails{
		ValidatorVersion: domain.ValidatorVersion,
		Checks: []domain.ValidationCheck{
			{Name: domain.ValidationCheckSSHConnection, Status: domain.CheckStatusPassed},
		},
	}
	if err := stored.RecordValidationResult(stored.ValidationGeneration, details, now); err != nil {
		t.Fatalf("记录校验结果失败: %v", err)
	}
	if _, err := db.UpdateExecutionHostValidation(ctx, *stored, stored.ValidationGeneration); err != nil {
		t.Fatalf("写入校验结果失败: %v", err)
	}
	return *stored
}

// workspaceContainer 构造任务级初始化使用的容器记录。
//
// 它刻意不带 attempt_id：任务级初始化发生在任何 Attempt 存在之前。
func workspaceContainer() domain.Container {
	return domain.Container{
		ID:          domain.ID(workspaceContainerID),
		HostID:      domain.ID("host-1"),
		DockerID:    testDockerID,
		Name:        "agentflow-task-workspace",
		Environment: domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		TaskID:      ptrID(domain.ID("task-1")),
		State:       domain.ContainerStateRunning,
		DockerState: domain.DockerStateRunning,
	}
}

// TestTaskWorkspaceUsesSharedFetcher 校验任务级初始化复用同一套 Git 实现。
func TestTaskWorkspaceUsesSharedFetcher(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkspaceFixture(t)
	result, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, app.TaskWorkspaceRequest{
		TaskID: fixture.task.ID.String(),
	})
	if err != nil {
		t.Fatalf("初始化失败: %v", err)
	}
	if result.Status != domain.TaskWorkspaceInitStatusSucceeded {
		t.Fatalf("状态应为 succeeded，实际 %s", result.Status)
	}
	if result.Commit != fixture.repo.baseCommit {
		t.Fatalf("commit 应为 %s，实际 %s", fixture.repo.baseCommit, result.Commit)
	}
	if result.TaskBranch != "agent/"+fixture.task.ID.String() {
		t.Fatalf("任务分支不符: %s", result.TaskBranch)
	}
	// 任务级初始化不得携带尝试归属。
	if len(fixture.containers.fetches) != 1 {
		t.Fatalf("代码获取应调用 1 次，实际 %d", len(fixture.containers.fetches))
	}
	if attemptID := fixture.containers.fetches[0].AttemptID; attemptID != "" {
		t.Fatalf("任务级初始化不得携带尝试归属，实际 %q", attemptID)
	}
	// 真实 Git 结果：容器内目标目录存在且位于任务分支上。
	projectDir := fixture.projectDir(t)
	if _, statErr := os.Stat(filepath.Join(projectDir, "README.md")); statErr != nil {
		t.Fatalf("容器内工作区缺少仓库文件: %v", statErr)
	}
	branch := strings.TrimSpace(runGit(t, fixture.gitPath, projectDir, "rev-parse", "--abbrev-ref", "HEAD"))
	if branch != "agent/"+fixture.task.ID.String() {
		t.Fatalf("容器内分支应为任务分支，实际 %s", branch)
	}
}

// TestTaskWorkspaceSkipsSecondRun 校验同容器重复初始化不重新 Clone。
func TestTaskWorkspaceSkipsSecondRun(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkspaceFixture(t)
	if _, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, app.TaskWorkspaceRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("首次初始化失败: %v", err)
	}
	// 在容器内工作区写入一个标记文件：重新 Clone 会把它清掉。
	projectDir := fixture.projectDir(t)
	marker := filepath.Join(projectDir, "local-change.txt")
	if err := os.WriteFile(marker, []byte("未提交的改动\n"), 0o644); err != nil {
		t.Fatalf("写入标记文件失败: %v", err)
	}
	second, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, app.TaskWorkspaceRequest{
		TaskID: fixture.task.ID.String(),
	})
	if err != nil {
		t.Fatalf("重复初始化失败: %v", err)
	}
	if second.Status != domain.TaskWorkspaceInitStatusSkipped {
		t.Fatalf("状态应为 skipped，实际 %s", second.Status)
	}
	if len(fixture.containers.fetches) != 1 {
		t.Fatalf("已就绪时不得重新获取代码，实际 %d 次", len(fixture.containers.fetches))
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("已有开发成果不得被覆盖: %v", statErr)
	}
}

// TestTaskWorkspaceRefusesNonEmptyWorkDir 校验目标目录非空时失败且不删除内容。
func TestTaskWorkspaceRefusesNonEmptyWorkDir(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkspaceFixture(t)
	projectDir := fixture.projectDir(t)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("创建目标目录失败: %v", err)
	}
	existing := filepath.Join(projectDir, "existing.txt")
	if err := os.WriteFile(existing, []byte("已有内容\n"), 0o644); err != nil {
		t.Fatalf("写入已有文件失败: %v", err)
	}
	result, err := fixture.service.InitializeTaskWorkspace(fixture.ctx, app.TaskWorkspaceRequest{
		TaskID: fixture.task.ID.String(),
	})
	if !errors.Is(err, app.ErrTaskWorkspaceFailed) {
		t.Fatalf("目标目录非空时必须失败，实际 %v", err)
	}
	if result.Status != domain.TaskWorkspaceInitStatusFailed {
		t.Fatalf("状态应为 failed，实际 %s", result.Status)
	}
	if result.Stage != domain.CodeFetchStageProbe {
		t.Fatalf("失败阶段应为 probe，实际 %s", result.Stage)
	}
	if _, statErr := os.Stat(existing); statErr != nil {
		t.Fatalf("失败不得删除已有内容: %v", statErr)
	}
}

// projectDir 返回映射到本机的容器内项目目录。
func (f *taskWorkspaceFixture) projectDir(t *testing.T) string {
	t.Helper()
	executor := &localExecutor{root: f.root}
	return executor.mapPath(domain.DefaultContainerWorkDir)
}
