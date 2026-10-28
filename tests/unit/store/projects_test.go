package sqlite_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// mustProject 写入一个测试项目。
func mustProject(t *testing.T, db *sqlite.DB, projectID string) domain.Project {
	t.Helper()
	return testkit.CreateProject(t, db, projectID, time.Now().UTC())
}

// TestCreateAndGetProject 校验项目字段完整往返。
func TestCreateAndGetProject(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	created := mustProject(t, db, "demo-service")

	loaded, err := db.GetProject(ctx, domain.ID(created.ID))
	if err != nil {
		t.Fatalf("读取项目失败: %v", err)
	}
	if loaded.ID != created.ID || loaded.Name != created.Name {
		t.Fatalf("项目标识或名称不符: %+v", loaded)
	}
	if loaded.RepositoryURL != created.RepositoryURL {
		t.Fatalf("仓库地址不符: %s", loaded.RepositoryURL)
	}
	if loaded.BaseBranch != "main" {
		t.Fatalf("基础分支不符: %s", loaded.BaseBranch)
	}
	if loaded.Environment.Type != domain.EnvironmentTypeLanguage ||
		loaded.Environment.Name != "go" {
		t.Fatalf("环境配置不符: %+v", loaded.Environment)
	}
	if loaded.EnvironmentVersion != "" {
		t.Fatalf("空环境版本应保持为空: %q", loaded.EnvironmentVersion)
	}
	if loaded.EffectiveWorkDir() != domain.DefaultContainerWorkDir {
		t.Fatalf("工作目录不符: %s", loaded.EffectiveWorkDir())
	}
	if loaded.Deleted() {
		t.Fatal("新建项目不应处于删除状态")
	}
	if !loaded.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("创建时间未按毫秒精度保存: %s vs %s", loaded.CreatedAt, created.CreatedAt)
	}
}

// TestProjectCachesRoundTrip 校验缓存类型数组的往返与空值形态。
func TestProjectCachesRoundTrip(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	environment, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "node")
	if err != nil {
		t.Fatalf("构造环境失败: %v", err)
	}
	project, err := domain.NewProject(domain.ProjectInput{
		ID:            "web-app",
		Name:          "前端应用",
		RepositoryURL: "https://git.example.com/team/web.git",
		BaseBranch:    "develop",
		Environment:   environment,
		Caches:        []string{"node-npm", "node-pnpm"},
		WorkDir:       "/workspace/app",
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造项目失败: %v", err)
	}
	if err := db.CreateProject(ctx, project); err != nil {
		t.Fatalf("写入项目失败: %v", err)
	}
	loaded, err := db.GetProject(ctx, domain.ID(project.ID))
	if err != nil {
		t.Fatalf("读取项目失败: %v", err)
	}
	if len(loaded.Caches) != 2 || loaded.Caches[0] != "node-npm" || loaded.Caches[1] != "node-pnpm" {
		t.Fatalf("缓存类型未完整保存: %+v", loaded.Caches)
	}
	if loaded.EffectiveWorkDir() != "/workspace/app" {
		t.Fatalf("自定义工作目录未保存: %s", loaded.EffectiveWorkDir())
	}
	if loaded.BaseBranch != "develop" {
		t.Fatalf("基础分支不符: %s", loaded.BaseBranch)
	}
}

// TestCreateProjectRejectsDuplicateID 校验项目标识冲突映射为 ErrConflict。
func TestCreateProjectRejectsDuplicateID(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	created := mustProject(t, db, "demo-service")
	if err := db.CreateProject(ctx, created); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("重复标识应返回唯一约束冲突，实际: %v", err)
	}
}

// TestGetProjectNotFound 校验读取不存在的项目返回 ErrNotFound。
func TestGetProjectNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	if _, err := db.GetProject(testContext(t), domain.ID("missing")); !errors.Is(
		err,
		storage.ErrNotFound,
	) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

// TestUpdateProject 校验整体更新只改可改字段。
func TestUpdateProject(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	project := mustProject(t, db, "demo-service")
	project.Name = "改名后的项目"
	project.RepositoryURL = "ssh://git.example.com/team/other"
	project.BaseBranch = "release/1.x"
	project.EnvironmentVersion = "v0002"
	project.Caches = []string{"go-mod"}
	project.WorkDir = "/workspace/other"
	project.UpdatedAt = time.Now().UTC().Add(time.Minute)

	if err := db.UpdateProject(ctx, project); err != nil {
		t.Fatalf("更新项目失败: %v", err)
	}
	loaded, err := db.GetProject(ctx, domain.ID(project.ID))
	if err != nil {
		t.Fatalf("读取项目失败: %v", err)
	}
	if loaded.Name != "改名后的项目" ||
		loaded.RepositoryURL != "ssh://git.example.com/team/other" ||
		loaded.BaseBranch != "release/1.x" ||
		loaded.EnvironmentVersion != "v0002" ||
		loaded.EffectiveWorkDir() != "/workspace/other" {
		t.Fatalf("更新未生效: %+v", loaded)
	}
	if len(loaded.Caches) != 1 || loaded.Caches[0] != "go-mod" {
		t.Fatalf("缓存类型未更新: %+v", loaded.Caches)
	}
	if loaded.CreatedAt.After(loaded.UpdatedAt) {
		t.Fatalf("创建时间不应晚于更新时间: %s / %s", loaded.CreatedAt, loaded.UpdatedAt)
	}
}

// TestUpdateProjectNotFound 校验更新不存在的项目返回 ErrNotFound。
func TestUpdateProjectNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	project := mustProject(t, db, "demo-service")
	project.ID = "missing"
	project.UpdatedAt = time.Now().UTC()
	if err := db.UpdateProject(ctx, project); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

// TestDeleteProjectIsLogical 校验删除只写删除标记，不物理删除记录。
func TestDeleteProjectIsLogical(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	project := mustProject(t, db, "demo-service")
	if err := db.DeleteProject(ctx, domain.ID(project.ID)); err != nil {
		t.Fatalf("删除项目失败: %v", err)
	}
	// 默认读取过滤已删除记录。
	if _, err := db.GetProject(ctx, domain.ID(project.ID)); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("已删除项目不应被默认读取，实际: %v", err)
	}
	// 记录仍然存在，删除标记被写入。
	var deletedAt *int64
	row := rawDB(t, db).QueryRow(
		`SELECT deleted_at FROM projects WHERE id = ?`,
		project.ID,
	)
	if err := row.Scan(&deletedAt); err != nil {
		t.Fatalf("读取删除标记失败: %v", err)
	}
	if deletedAt == nil || *deletedAt <= 0 {
		t.Fatalf("删除标记未写入: %v", deletedAt)
	}
	// 重复删除返回不存在，不改写首次删除时间。
	if err := db.DeleteProject(ctx, domain.ID(project.ID)); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("重复删除应返回不存在，实际: %v", err)
	}
	// 已删除项目不能被更新。
	project.Name = "不应生效"
	project.UpdatedAt = time.Now().UTC().Add(time.Minute)
	if err := db.UpdateProject(ctx, project); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("已删除项目不应可更新，实际: %v", err)
	}
}

// TestListProjectsFiltersDeleted 校验列表默认过滤已删除记录。
func TestListProjectsFiltersDeleted(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	first := mustProject(t, db, "first-project")
	second := mustProject(t, db, "second-project")
	if err := db.DeleteProject(ctx, domain.ID(first.ID)); err != nil {
		t.Fatalf("删除项目失败: %v", err)
	}
	visible, err := db.ListProjects(ctx, storage.ProjectFilter{})
	if err != nil {
		t.Fatalf("查询项目列表失败: %v", err)
	}
	if len(visible) != 1 || visible[0].ID != second.ID {
		t.Fatalf("默认列表应只包含未删除项目: %+v", visible)
	}
	all, err := db.ListProjects(ctx, storage.ProjectFilter{IncludeDeleted: true})
	if err != nil {
		t.Fatalf("查询全部项目失败: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("IncludeDeleted 应返回全部记录: %+v", all)
	}
}

// TestProjectKeepsUnknownEnvironmentType 校验未知环境类型可以原样读回。
//
// 数据库不限定枚举取值集合，读取层不得把未知取值映射为默认值或当作损坏数据。
func TestProjectKeepsUnknownEnvironmentType(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	project := mustProject(t, db, "demo-service")
	testkit.MustExec(
		t,
		rawDB(t, db),
		`UPDATE projects SET env_type = ? WHERE id = ?`,
		"quantum",
		project.ID,
	)
	loaded, err := db.GetProject(ctx, domain.ID(project.ID))
	if err != nil {
		t.Fatalf("未知环境类型不应导致读取失败: %v", err)
	}
	if loaded.Environment.Type != domain.EnvironmentType("quantum") {
		t.Fatalf("未知环境类型应原样保留，实际: %q", loaded.Environment.Type)
	}
}
