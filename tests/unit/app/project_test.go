package app_test

import (
	"context"
	"database/sql"
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

// mustProjectDefaultHost 在测试数据库中写入一个可作为项目默认执行节点的节点。
func mustProjectDefaultHost(t *testing.T, db *sqlite.DB, name, address string) domain.ExecutionHost {
	t.Helper()
	host, err := domain.NewExecutionHost(
		"", name, address, 0, "deploy", "", time.Now().UTC(),
		domain.WithPrivateKey(testkit.PrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if err := db.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	return host
}

// projectDefaultHostColumn 直接读取项目默认执行节点列的原始值。
//
// 仓储读取会把 NULL 还原为空字符串，因此"清空后必须是 NULL 而不是空字符串"只能
// 通过原始列值验证。
func projectDefaultHostColumn(t *testing.T, db *sqlite.DB, projectID string) sql.NullString {
	t.Helper()
	raw := testkit.OpenSQLite(t, db.Path())
	var value sql.NullString
	if err := raw.QueryRow(
		`SELECT default_host_id FROM projects WHERE id = ?`,
		projectID,
	).Scan(&value); err != nil {
		t.Fatalf("读取 default_host_id 失败: %v", err)
	}
	return value
}

// TestProjectServiceDefaultHostOptional 校验不提供默认执行节点时创建成功。
func TestProjectServiceDefaultHostOptional(t *testing.T) {
	t.Parallel()
	fixture := newProjectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	created, err := fixture.service.Create(ctx, createProjectRequest())
	if err != nil {
		t.Fatalf("不设置默认节点应当可以创建项目: %v", err)
	}
	if created.DefaultHostID != "" {
		t.Fatalf("未设置默认节点时不应生成隐式默认值: %q", created.DefaultHostID)
	}
	column := projectDefaultHostColumn(t, fixture.db, created.ID)
	if column.Valid {
		t.Fatalf("未设置默认节点时数据库应为 NULL，实际: %q", column.String)
	}
}

// TestProjectServiceDefaultHostCreateAndRead 校验有效默认节点被写入并可读回。
func TestProjectServiceDefaultHostCreateAndRead(t *testing.T) {
	t.Parallel()
	fixture := newProjectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	host := mustProjectDefaultHost(t, fixture.db, "构建节点", "10.0.0.31")
	request := createProjectRequest()
	request.DefaultHostID = host.ID.String()
	created, err := fixture.service.Create(ctx, request)
	if err != nil {
		t.Fatalf("设置有效默认节点应当可以创建项目: %v", err)
	}
	if created.DefaultHostID != host.ID.String() {
		t.Fatalf("创建结果中的默认节点不符: %q", created.DefaultHostID)
	}
	loaded, err := fixture.service.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("读取项目失败: %v", err)
	}
	if loaded.DefaultHostID != host.ID.String() {
		t.Fatalf("默认节点未持久化: %q", loaded.DefaultHostID)
	}
	column := projectDefaultHostColumn(t, fixture.db, created.ID)
	if !column.Valid || column.String != host.ID.String() {
		t.Fatalf("数据库中的默认节点不符: %+v", column)
	}
}

// TestProjectServiceDefaultHostUpdate 校验更新默认节点后新值与切换都被持久化。
func TestProjectServiceDefaultHostUpdate(t *testing.T) {
	t.Parallel()
	fixture := newProjectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	first := mustProjectDefaultHost(t, fixture.db, "构建节点", "10.0.0.31")
	second := mustProjectDefaultHost(t, fixture.db, "验证节点", "10.0.0.32")
	if _, err := fixture.service.Create(ctx, createProjectRequest()); err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}

	updated, err := fixture.service.Update(ctx, "demo-service", app.UpdateProjectRequest{
		Name:          "示例服务",
		RepositoryURL: "ssh://git.example.com/team/repo",
		BaseBranch:    "main",
		Environment:   "go",
		DefaultHostID: first.ID.String(),
	})
	if err != nil {
		t.Fatalf("设置默认节点失败: %v", err)
	}
	if updated.DefaultHostID != first.ID.String() {
		t.Fatalf("更新结果中的默认节点不符: %q", updated.DefaultHostID)
	}
	loaded, err := fixture.service.Get(ctx, "demo-service")
	if err != nil {
		t.Fatalf("读取项目失败: %v", err)
	}
	if loaded.DefaultHostID != first.ID.String() {
		t.Fatalf("新默认节点未持久化: %q", loaded.DefaultHostID)
	}

	if _, err := fixture.service.Update(ctx, "demo-service", app.UpdateProjectRequest{
		Name:          "示例服务",
		RepositoryURL: "ssh://git.example.com/team/repo",
		BaseBranch:    "main",
		Environment:   "go",
		DefaultHostID: second.ID.String(),
	}); err != nil {
		t.Fatalf("切换默认节点失败: %v", err)
	}
	loaded, err = fixture.service.Get(ctx, "demo-service")
	if err != nil {
		t.Fatalf("读取项目失败: %v", err)
	}
	if loaded.DefaultHostID != second.ID.String() {
		t.Fatalf("切换后的默认节点不符: %q", loaded.DefaultHostID)
	}
}

// TestProjectServiceDefaultHostClearedToNull 校验清空默认节点后数据库列为 NULL。
func TestProjectServiceDefaultHostClearedToNull(t *testing.T) {
	t.Parallel()
	fixture := newProjectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	host := mustProjectDefaultHost(t, fixture.db, "构建节点", "10.0.0.31")
	request := createProjectRequest()
	request.DefaultHostID = host.ID.String()
	if _, err := fixture.service.Create(ctx, request); err != nil {
		t.Fatalf("设置默认节点失败: %v", err)
	}

	updated, err := fixture.service.Update(ctx, "demo-service", app.UpdateProjectRequest{
		Name:          "示例服务",
		RepositoryURL: "ssh://git.example.com/team/repo",
		BaseBranch:    "main",
		Environment:   "go",
		DefaultHostID: "",
	})
	if err != nil {
		t.Fatalf("清空默认节点失败: %v", err)
	}
	if updated.DefaultHostID != "" {
		t.Fatalf("清空后返回值应为空: %q", updated.DefaultHostID)
	}
	column := projectDefaultHostColumn(t, fixture.db, "demo-service")
	if column.Valid {
		t.Fatalf("清空后数据库应为 NULL，实际: %q", column.String)
	}
}

// TestProjectServiceDefaultHostRejectsUnknownOrDeleted 校验不存在与已删除的节点都被拒绝。
func TestProjectServiceDefaultHostRejectsUnknownOrDeleted(t *testing.T) {
	t.Parallel()
	fixture := newProjectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	unknown := createProjectRequest()
	unknown.DefaultHostID = "missing-host"
	if _, err := fixture.service.Create(ctx, unknown); !errors.Is(err, app.ErrProjectInvalidInput) {
		t.Fatalf("不存在的执行节点应返回参数错误，实际: %v", err)
	}
	if _, err := fixture.service.Get(ctx, unknown.ID); !errors.Is(err, app.ErrProjectNotFound) {
		t.Fatalf("被拒绝的创建不应留下项目记录，实际: %v", err)
	}

	deleted := mustProjectDefaultHost(t, fixture.db, "构建节点", "10.0.0.31")
	if err := fixture.db.DeleteExecutionHost(ctx, deleted.ID); err != nil {
		t.Fatalf("删除执行节点失败: %v", err)
	}
	rejected := createProjectRequest()
	rejected.DefaultHostID = deleted.ID.String()
	if _, err := fixture.service.Create(ctx, rejected); !errors.Is(err, app.ErrProjectInvalidInput) {
		t.Fatalf("已删除的执行节点应返回参数错误，实际: %v", err)
	}
	if _, err := fixture.service.Get(ctx, rejected.ID); !errors.Is(err, app.ErrProjectNotFound) {
		t.Fatalf("被拒绝的创建不应留下项目记录，实际: %v", err)
	}
}

// TestProjectServiceDefaultHostKeepsDeletedReference 校验节点被删除后项目仍可读可改。
//
// 项目持有的是历史引用：节点下线不能让整个项目读取失败，用户仍能打开项目并清除或
// 重新选择默认节点。
func TestProjectServiceDefaultHostKeepsDeletedReference(t *testing.T) {
	t.Parallel()
	fixture := newProjectFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	host := mustProjectDefaultHost(t, fixture.db, "构建节点", "10.0.0.31")
	request := createProjectRequest()
	request.DefaultHostID = host.ID.String()
	if _, err := fixture.service.Create(ctx, request); err != nil {
		t.Fatalf("设置默认节点失败: %v", err)
	}
	if err := fixture.db.DeleteExecutionHost(ctx, host.ID); err != nil {
		t.Fatalf("删除执行节点失败: %v", err)
	}

	loaded, err := fixture.service.Get(ctx, "demo-service")
	if err != nil {
		t.Fatalf("默认节点被删除后项目仍应可读: %v", err)
	}
	if loaded.DefaultHostID != host.ID.String() {
		t.Fatalf("历史引用应保留: %q", loaded.DefaultHostID)
	}
	// 保留旧引用时仍可保存其它配置。
	if _, err := fixture.service.Update(ctx, "demo-service", app.UpdateProjectRequest{
		Name:          "改名后的项目",
		RepositoryURL: "ssh://git.example.com/team/repo",
		BaseBranch:    "main",
		Environment:   "go",
		DefaultHostID: host.ID.String(),
	}); err != nil {
		t.Fatalf("保留已删除节点引用的更新不应被拒绝: %v", err)
	}
	// 清除旧引用不需要节点存在。
	updated, err := fixture.service.Update(ctx, "demo-service", app.UpdateProjectRequest{
		Name:          "改名后的项目",
		RepositoryURL: "ssh://git.example.com/team/repo",
		BaseBranch:    "main",
		Environment:   "go",
		DefaultHostID: "",
	})
	if err != nil {
		t.Fatalf("清除旧引用失败: %v", err)
	}
	if updated.DefaultHostID != "" {
		t.Fatalf("清除后应为空值: %q", updated.DefaultHostID)
	}
	column := projectDefaultHostColumn(t, fixture.db, "demo-service")
	if column.Valid {
		t.Fatalf("清除后数据库应为 NULL，实际: %q", column.String)
	}
}
