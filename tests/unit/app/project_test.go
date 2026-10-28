package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// projectFixture 是项目管理服务的测试环境。
type projectFixture struct {
	db      *sqlite.DB
	service *app.ProjectService
}

// newProjectFixture 在测试工作空间中构造项目管理服务。
func newProjectFixture(t *testing.T) *projectFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: testkit.New(t).DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	service, err := app.NewProjectService(app.ProjectServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造项目服务失败: %v", err)
	}
	return &projectFixture{db: db, service: service}
}

// createRequest 返回一份合法的建项目请求。
func createProjectRequest() app.CreateProjectRequest {
	return app.CreateProjectRequest{
		ID:            "demo-service",
		Name:          "示例服务",
		RepositoryURL: "ssh://git.example.com/team/repo",
		BaseBranch:    "main",
		Environment:   "go",
		Caches:        []string{"go-mod"},
	}
}

// TestProjectServiceCreateAndGet 校验创建后可以按标识读取。
func TestProjectServiceCreateAndGet(t *testing.T) {
	t.Parallel()
	fixture := newProjectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	created, err := fixture.service.Create(ctx, createProjectRequest())
	if err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}
	if created.ID != "demo-service" {
		t.Fatalf("项目标识不符: %s", created.ID)
	}
	loaded, err := fixture.service.Get(ctx, "demo-service")
	if err != nil {
		t.Fatalf("读取项目失败: %v", err)
	}
	if loaded.Name != "示例服务" || loaded.Environment.Name != "go" {
		t.Fatalf("项目字段不符: %+v", loaded)
	}
	if loaded.EffectiveWorkDir() != domain.DefaultContainerWorkDir {
		t.Fatalf("工作目录未使用默认值: %s", loaded.EffectiveWorkDir())
	}
}

// TestProjectServiceRejectsDuplicateID 校验重复标识映射为状态冲突。
func TestProjectServiceRejectsDuplicateID(t *testing.T) {
	t.Parallel()
	fixture := newProjectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	if _, err := fixture.service.Create(ctx, createProjectRequest()); err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}
	_, err := fixture.service.Create(ctx, createProjectRequest())
	if !errors.Is(err, app.ErrProjectStateConflict) {
		t.Fatalf("重复标识应返回状态冲突，实际: %v", err)
	}
}

// TestProjectServiceRejectsInvalidInput 校验参数校验错误类别。
func TestProjectServiceRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	fixture := newProjectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	cases := map[string]func(*app.CreateProjectRequest){
		"标识为空":   func(r *app.CreateProjectRequest) { r.ID = "  " },
		"标识非法":   func(r *app.CreateProjectRequest) { r.ID = "Demo Project" },
		"名称为空":   func(r *app.CreateProjectRequest) { r.Name = "  " },
		"仓库地址为空": func(r *app.CreateProjectRequest) { r.RepositoryURL = " " },
		"仓库地址带凭据": func(r *app.CreateProjectRequest) {
			r.RepositoryURL = "https://user:token@git.example.com/r.git"
		},
		"基础分支为空":   func(r *app.CreateProjectRequest) { r.BaseBranch = " " },
		"环境为空":     func(r *app.CreateProjectRequest) { r.Environment = " " },
		"版本标签非法":   func(r *app.CreateProjectRequest) { r.EnvironmentVersion = "1" },
		"工作目录越界":   func(r *app.CreateProjectRequest) { r.WorkDir = "/etc" },
		"缓存类型书写非法": func(r *app.CreateProjectRequest) { r.Caches = []string{"Go Mod"} },
	}
	for name, mutate := range cases {
		request := createProjectRequest()
		mutate(&request)
		_, err := fixture.service.Create(ctx, request)
		if err == nil {
			t.Fatalf("%s: 应当返回参数错误", name)
		}
		if !errors.Is(err, app.ErrProjectInvalidInput) {
			t.Fatalf("%s: 错误类别应为参数错误，实际: %v", name, err)
		}
	}
}

// TestProjectServiceGetNotFound 校验不存在与已删除的项目都按不存在处理。
func TestProjectServiceGetNotFound(t *testing.T) {
	t.Parallel()
	fixture := newProjectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	if _, err := fixture.service.Get(ctx, "missing"); !errors.Is(err, app.ErrProjectNotFound) {
		t.Fatalf("不存在的项目应返回未找到，实际: %v", err)
	}
	if _, err := fixture.service.Create(ctx, createProjectRequest()); err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}
	if err := fixture.service.Delete(ctx, "demo-service"); err != nil {
		t.Fatalf("删除项目失败: %v", err)
	}
	if _, err := fixture.service.Get(ctx, "demo-service"); !errors.Is(
		err,
		app.ErrProjectNotFound,
	) {
		t.Fatalf("已删除项目应按不存在处理，实际: %v", err)
	}
	if err := fixture.service.Delete(ctx, "demo-service"); !errors.Is(
		err,
		app.ErrProjectNotFound,
	) {
		t.Fatalf("重复删除应返回未找到，实际: %v", err)
	}
}

// TestProjectServiceUpdateReplacesAllFields 校验更新是整体替换。
func TestProjectServiceUpdateReplacesAllFields(t *testing.T) {
	t.Parallel()
	fixture := newProjectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	if _, err := fixture.service.Create(ctx, createProjectRequest()); err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}
	updated, err := fixture.service.Update(ctx, "demo-service", app.UpdateProjectRequest{
		Name:               "改名后的项目",
		RepositoryURL:      "https://git.example.com/team/other.git",
		BaseBranch:         "release/1.x",
		Environment:        "node",
		EnvironmentVersion: "v0002",
		Caches:             []string{"node-npm"},
		WorkDir:            "/workspace/other",
	})
	if err != nil {
		t.Fatalf("更新项目失败: %v", err)
	}
	if updated.ID != "demo-service" {
		t.Fatalf("项目标识不可修改: %s", updated.ID)
	}
	if updated.Name != "改名后的项目" ||
		updated.RepositoryURL != "https://git.example.com/team/other.git" ||
		updated.BaseBranch != "release/1.x" ||
		updated.Environment.Name != "node" ||
		updated.EnvironmentVersion != "v0002" ||
		updated.EffectiveWorkDir() != "/workspace/other" {
		t.Fatalf("更新未生效: %+v", updated)
	}
	if len(updated.Caches) != 1 || updated.Caches[0] != "node-npm" {
		t.Fatalf("缓存类型未替换: %+v", updated.Caches)
	}
}

// TestProjectServiceUpdateNotFound 校验更新不存在的项目返回未找到。
func TestProjectServiceUpdateNotFound(t *testing.T) {
	t.Parallel()
	fixture := newProjectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	_, err := fixture.service.Update(ctx, "missing", app.UpdateProjectRequest{
		Name:          "示例",
		RepositoryURL: "ssh://git.example.com/team/repo",
		BaseBranch:    "main",
		Environment:   "go",
	})
	if !errors.Is(err, app.ErrProjectNotFound) {
		t.Fatalf("更新不存在的项目应返回未找到，实际: %v", err)
	}
}

// TestProjectServiceListOmitsDeleted 校验列表不返回已删除项目。
func TestProjectServiceListOmitsDeleted(t *testing.T) {
	t.Parallel()
	fixture := newProjectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	first := createProjectRequest()
	if _, err := fixture.service.Create(ctx, first); err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}
	second := createProjectRequest()
	second.ID = "second-project"
	if _, err := fixture.service.Create(ctx, second); err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}
	if err := fixture.service.Delete(ctx, first.ID); err != nil {
		t.Fatalf("删除项目失败: %v", err)
	}
	projects, err := fixture.service.List(ctx)
	if err != nil {
		t.Fatalf("查询项目列表失败: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != second.ID {
		t.Fatalf("列表应只包含未删除项目: %+v", projects)
	}
}

// TestTaskCreationRequiresExistingProject 校验任务创建的三种项目失败分支。
func TestTaskCreationRequiresExistingProject(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	cases := map[string]string{
		"项目标识为空": "",
		"项目标识非法": "Demo Project",
		"项目不存在":  "missing-project",
	}
	for name, projectID := range cases {
		request := fixture.request("任务", "描述")
		request.ProjectID = projectID
		if _, err := fixture.service.CreateTaskFromAPI(ctx, request); !errors.Is(
			err,
			app.ErrTaskInvalidInput,
		) {
			t.Fatalf("%s: 错误类别应为参数错误，实际: %v", name, err)
		}
	}

	// 项目被逻辑删除后同样不能再被关联。
	if err := fixture.db.DeleteProject(ctx, domain.ID(fixture.project.ID)); err != nil {
		t.Fatalf("删除项目失败: %v", err)
	}
	if _, err := fixture.service.CreateTaskFromAPI(ctx, fixture.request("任务", "描述")); !errors.Is(
		err,
		app.ErrTaskInvalidInput,
	) {
		t.Fatalf("已删除项目不应可关联，实际: %v", err)
	}
}
