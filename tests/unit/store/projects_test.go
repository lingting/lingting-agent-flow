package sqlite_test

import (
	"database/sql"
	"errors"
	"strings"
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

// openMigrationDB 打开指定路径的数据库；调用方负责关闭，便于模拟重复打开触发迁移。
func openMigrationDB(t *testing.T, path string) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(testContext(t), sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

// projectDefaultHostValue 直接读取项目默认执行节点列的原始值。
func projectDefaultHostValue(t *testing.T, db *sql.DB, projectID string) sql.NullString {
	t.Helper()
	var value sql.NullString
	if err := db.QueryRow(
		`SELECT default_host_id FROM projects WHERE id = ?`,
		projectID,
	).Scan(&value); err != nil {
		t.Fatalf("读取 default_host_id 失败: %v", err)
	}
	return value
}

// TestProjectDefaultHostRoundTrip 校验默认执行节点列的 NULL 与非 NULL 往返。
func TestProjectDefaultHostRoundTrip(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	raw := rawDB(t, db)

	unset := mustProject(t, db, "demo-service")
	loaded, err := db.GetProject(ctx, domain.ID(unset.ID))
	if err != nil {
		t.Fatalf("读取项目失败: %v", err)
	}
	if loaded.DefaultHostID != "" {
		t.Fatalf("未设置默认节点时读取结果应为空: %q", loaded.DefaultHostID)
	}
	if column := projectDefaultHostValue(t, raw, unset.ID); column.Valid {
		t.Fatalf("未设置默认节点时数据库应为 NULL，实际: %q", column.String)
	}

	environment, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造环境失败: %v", err)
	}
	withHost, err := domain.NewProject(domain.ProjectInput{
		ID:            "with-host",
		Name:          "带默认节点的项目",
		RepositoryURL: "ssh://git.example.com/team/other",
		BaseBranch:    "main",
		Environment:   environment,
		DefaultHostID: "host-7",
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造项目失败: %v", err)
	}
	if err := db.CreateProject(ctx, withHost); err != nil {
		t.Fatalf("写入项目失败: %v", err)
	}
	loaded, err = db.GetProject(ctx, domain.ID(withHost.ID))
	if err != nil {
		t.Fatalf("读取项目失败: %v", err)
	}
	if loaded.DefaultHostID != "host-7" {
		t.Fatalf("默认节点未完整往返: %q", loaded.DefaultHostID)
	}
	column := projectDefaultHostValue(t, raw, withHost.ID)
	if !column.Valid || column.String != "host-7" {
		t.Fatalf("数据库中的默认节点不符: %+v", column)
	}
}

// TestProjectDefaultHostUpdateKeepsOtherFields 校验更新默认节点不影响其它项目字段。
func TestProjectDefaultHostUpdateKeepsOtherFields(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	project := mustProject(t, db, "demo-service")
	createdAt := project.CreatedAt

	project.DefaultHostID = "host-9"
	project.UpdatedAt = time.Now().UTC().Add(time.Minute)
	if err := db.UpdateProject(ctx, project); err != nil {
		t.Fatalf("更新项目失败: %v", err)
	}
	loaded, err := db.GetProject(ctx, domain.ID(project.ID))
	if err != nil {
		t.Fatalf("读取项目失败: %v", err)
	}
	if loaded.DefaultHostID != "host-9" {
		t.Fatalf("默认节点未更新: %q", loaded.DefaultHostID)
	}
	if loaded.Name != project.Name ||
		loaded.RepositoryURL != project.RepositoryURL ||
		loaded.BaseBranch != project.BaseBranch ||
		loaded.Environment.Name != project.Environment.Name ||
		loaded.EffectiveWorkDir() != project.EffectiveWorkDir() {
		t.Fatalf("其它项目字段被改动: %+v", loaded)
	}
	if !loaded.CreatedAt.Equal(createdAt) {
		t.Fatalf("创建时间不应被更新改写: %s vs %s", loaded.CreatedAt, createdAt)
	}
	if loaded.Deleted() {
		t.Fatal("更新不应写入删除标记")
	}
}

// TestProjectDefaultHostDoesNotTouchTaskSnapshot 校验项目默认节点变化不改写历史任务事实。
func TestProjectDefaultHostDoesNotTouchTaskSnapshot(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	raw := rawDB(t, db)

	project := mustProject(t, db, "demo-service")
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	hostID := domain.ID("host-legacy")
	task.HostID = &hostID
	environment, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造环境失败: %v", err)
	}
	planner := mustRole(t, db, domain.RoleTypePlanner, "规划师")
	developer := mustRole(t, db, domain.RoleTypeDeveloper, "开发工程师")
	task.Config = domain.TaskConfig{
		Goal:            "验证项目默认节点不改写任务快照",
		Environment:     environment,
		ProjectID:       project.ID,
		PlannerRoleID:   planner.ID,
		DeveloperRoleID: developer.ID,
	}
	if err := db.UpdateTask(ctx, task); err != nil {
		t.Fatalf("写入任务快照失败: %v", err)
	}

	var hostBefore, configBefore string
	if err := raw.QueryRow(
		`SELECT host_id, config_json FROM tasks WHERE id = ?`,
		task.ID.String(),
	).Scan(&hostBefore, &configBefore); err != nil {
		t.Fatalf("读取任务快照失败: %v", err)
	}

	project.DefaultHostID = "host-new"
	project.UpdatedAt = time.Now().UTC().Add(time.Minute)
	if err := db.UpdateProject(ctx, project); err != nil {
		t.Fatalf("更新项目失败: %v", err)
	}

	var hostAfter, configAfter string
	if err := raw.QueryRow(
		`SELECT host_id, config_json FROM tasks WHERE id = ?`,
		task.ID.String(),
	).Scan(&hostAfter, &configAfter); err != nil {
		t.Fatalf("读取任务快照失败: %v", err)
	}
	if hostAfter != hostBefore {
		t.Fatalf("项目默认节点变化不应改写任务的 host_id: %q vs %q", hostAfter, hostBefore)
	}
	if configAfter != configBefore {
		t.Fatalf("项目默认节点变化不应改写任务的 config_json: %q vs %q", configAfter, configBefore)
	}
}

// TestProjectsMigrationDefaultHostKeepsExistingProjects 校验 0028 在既有项目表上增量升级。
//
// 用例把已迁移的数据库回退到"0028 之前"的形态：删除部分索引与新列，并移除版本记录，
// 然后重新打开数据库触发迁移。断言的是真实迁移行为：既有项目数据保留、新增列为 NULL、
// 版本记录正确、部分索引条件正确。
func TestProjectsMigrationDefaultHostKeepsExistingProjects(t *testing.T) {
	t.Parallel()
	path := testkit.New(t).SQLitePath()

	legacy := openMigrationDB(t, path)
	project := testkit.CreateProject(t, legacy, "legacy-project", time.Now().UTC())
	if err := legacy.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	setup := testkit.OpenSQLite(t, path)
	testkit.MustExec(t, setup, `DROP INDEX idx_projects_default_host`)
	testkit.MustExec(t, setup, `ALTER TABLE projects DROP COLUMN default_host_id`)
	testkit.MustExec(t, setup, `DELETE FROM schema_migrations WHERE version = ?`, 28)
	if err := setup.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	upgraded := openMigrationDB(t, path)
	defer func() {
		if err := upgraded.Close(); err != nil {
			t.Fatalf("关闭数据库失败: %v", err)
		}
	}()
	ctx := testContext(t)

	loaded, err := upgraded.GetProject(ctx, domain.ID(project.ID))
	if err != nil {
		t.Fatalf("升级后既有项目应当仍可读取: %v", err)
	}
	if loaded.Name != project.Name || loaded.RepositoryURL != project.RepositoryURL {
		t.Fatalf("升级不应改动既有项目数据: %+v", loaded)
	}
	if loaded.DefaultHostID != "" {
		t.Fatalf("旧项目升级后默认节点应为空: %q", loaded.DefaultHostID)
	}

	version, err := upgraded.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("读取 Schema 版本失败: %v", err)
	}
	if version != 28 {
		t.Fatalf("当前程序支持的迁移版本应为 28，实际: %d", version)
	}

	raw := testkit.OpenSQLite(t, path)
	defer func() {
		if err := raw.Close(); err != nil {
			t.Errorf("关闭数据库失败: %v", err)
		}
	}()
	var recorded int
	if err := raw.QueryRow(
		`SELECT COUNT(*) FROM schema_migrations WHERE version = ? AND name = ?`,
		28,
		"projects_default_host",
	).Scan(&recorded); err != nil {
		t.Fatalf("读取迁移版本记录失败: %v", err)
	}
	if recorded != 1 {
		t.Fatalf("0028 的版本记录不正确: %d", recorded)
	}

	var indexSQL string
	if err := raw.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`,
		"idx_projects_default_host",
	).Scan(&indexSQL); err != nil {
		t.Fatalf("读取部分索引定义失败: %v", err)
	}
	if !strings.Contains(indexSQL, "default_host_id") ||
		!strings.Contains(indexSQL, "WHERE default_host_id IS NOT NULL") {
		t.Fatalf("部分索引的条件不正确: %s", indexSQL)
	}

	if column := projectDefaultHostValue(t, raw, project.ID); column.Valid {
		t.Fatalf("旧项目升级后新增列应为 NULL，实际: %q", column.String)
	}
}
