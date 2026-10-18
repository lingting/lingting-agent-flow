package sqlite_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

func TestCreateAndGetRole(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	role := mustRole(t, db, domain.RoleTypeDeveloper, "开发工程师")

	loaded, err := db.GetRole(ctx, role.ID)
	if err != nil {
		t.Fatalf("读取角色失败: %v", err)
	}
	if loaded.Name != role.Name || loaded.Type != role.Type || loaded.Runner != role.Runner {
		t.Fatalf("角色字段不符: %+v", loaded)
	}
	if loaded.Model != role.Model || loaded.SystemPrompt != role.SystemPrompt {
		t.Fatalf("角色配置不符: %+v", loaded)
	}
	if !loaded.Enabled {
		t.Fatal("角色应当默认启用")
	}
	if len(loaded.ToolsPolicy.Permissions) != len(role.ToolsPolicy.Permissions) {
		t.Fatalf("工具策略未完整保存: %+v", loaded.ToolsPolicy)
	}
	if !loaded.CreatedAt.Equal(role.CreatedAt) {
		t.Fatalf("创建时间未按毫秒精度保存: %s vs %s", loaded.CreatedAt, role.CreatedAt)
	}
}

func TestGetRoleNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	_, err := db.GetRole(testContext(t), domain.NewID())
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

func TestUpdateRole(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	role := mustRole(t, db, domain.RoleTypePlanner, "规划师")
	role.Name = "首席规划师"
	role.Model = "provider/other"
	role.Enabled = false
	role.UpdatedAt = time.Now().UTC()

	if err := db.UpdateRole(ctx, role); err != nil {
		t.Fatalf("更新角色失败: %v", err)
	}
	loaded, err := db.GetRole(ctx, role.ID)
	if err != nil {
		t.Fatalf("读取角色失败: %v", err)
	}
	if loaded.Name != "首席规划师" || loaded.Model != "provider/other" || loaded.Enabled {
		t.Fatalf("更新未生效: %+v", loaded)
	}
}

func TestUpdateMissingRoleReturnsNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	role, err := domain.NewRole(
		"不存在",
		domain.RoleTypePlanner,
		"provider/model",
		"提示词",
		domain.DefaultToolsPolicy(domain.RoleTypePlanner),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := db.UpdateRole(testContext(t), role); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

func TestListRolesOrderedByCreation(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	mustRole(t, db, domain.RoleTypePlanner, "第一个")
	mustRole(t, db, domain.RoleTypeDeveloper, "第二个")
	mustRole(t, db, domain.RoleTypeValidator, "第三个")

	roles, err := db.ListRoles(ctx)
	if err != nil {
		t.Fatalf("查询角色失败: %v", err)
	}
	if len(roles) != 3 {
		t.Fatalf("角色数量不符: %d", len(roles))
	}
	if roles[0].Name != "第一个" || roles[2].Name != "第三个" {
		t.Fatalf("角色顺序不符: %+v", roles)
	}
}

func TestCreateRoleRejectsInvalidValue(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	role := domain.Role{
		ID:           domain.NewID(),
		Name:         "非法角色",
		Type:         domain.RoleType("reviewer"),
		Runner:       domain.RoleRunnerPi,
		Model:        "provider/model",
		SystemPrompt: "提示词",
		Enabled:      true,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := db.CreateRole(testContext(t), role); err == nil {
		t.Fatal("非法角色类型应当被拒绝")
	}
}

func TestDeleteRoleReferencedByHistoryIsSoftDeleted(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	// 数据库不声明外键，删除角色改为逻辑删除：历史引用不会阻止删除，
	// 但步骤与会话历史仍然完整保留。
	if err := db.DeleteRole(ctx, built.developer.ID); err != nil {
		t.Fatalf("逻辑删除角色失败: %v", err)
	}
	if _, err := db.GetRole(ctx, built.developer.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("逻辑删除后角色不应出现在正常查询中，实际: %v", err)
	}
	step, err := db.GetStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("步骤历史不应丢失: %v", err)
	}
	if step.RoleID != built.developer.ID {
		t.Fatalf("步骤仍应保留原角色标识: %s", step.RoleID)
	}
	// 删除是幂等的失败路径：重复删除返回记录不存在。
	if err := db.DeleteRole(ctx, built.developer.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("重复删除应当返回记录不存在，实际: %v", err)
	}
}

func TestDeleteRoleKeepsRowForHistory(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	if err := db.DeleteRole(testContext(t), built.developer.ID); err != nil {
		t.Fatalf("逻辑删除角色失败: %v", err)
	}
	// 逻辑删除只标记 deleted_at，物理行与历史引用都必须保留。
	var total, deleted int
	ctx := testContext(t)
	if err := rawDB(t, db).QueryRowContext(
		ctx,
		`SELECT COUNT(*), SUM(CASE WHEN deleted_at IS NOT NULL THEN 1 ELSE 0 END) FROM roles`,
	).Scan(&total, &deleted); err != nil {
		t.Fatalf("统计角色行失败: %v", err)
	}
	if total != 3 || deleted != 1 {
		t.Fatalf("逻辑删除结果不符: 总行数 %d，已删除 %d", total, deleted)
	}
}

func TestDeleteUnreferencedRole(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	role := mustRole(t, db, domain.RoleTypeValidator, "未使用的角色")
	if err := db.DeleteRole(ctx, role.ID); err != nil {
		t.Fatalf("删除未引用角色失败: %v", err)
	}
	if _, err := db.GetRole(ctx, role.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("角色应当已删除，实际: %v", err)
	}
}

func TestSeedRolesOnlyWhenEmpty(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	seeds := []storage.RoleSeed{
		{
			ID:           domain.ID("planner"),
			Name:         "架构规划师",
			Type:         domain.RoleTypePlanner,
			Runner:       domain.RoleRunnerPi,
			Model:        "provider/model",
			SystemPrompt: "规划提示词",
			ToolsPolicy:  domain.DefaultToolsPolicy(domain.RoleTypePlanner),
		},
		{
			ID:           domain.ID("developer"),
			Name:         "开发工程师",
			Type:         domain.RoleTypeDeveloper,
			Runner:       domain.RoleRunnerPi,
			Model:        "provider/model",
			SystemPrompt: "开发提示词",
			ToolsPolicy:  domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		},
	}
	created, err := db.SeedRoles(ctx, seeds)
	if err != nil {
		t.Fatalf("初始化角色失败: %v", err)
	}
	if created != len(seeds) {
		t.Fatalf("初始化数量不符: %d", created)
	}

	again, err := db.SeedRoles(ctx, seeds)
	if err != nil {
		t.Fatalf("重复初始化应当成功: %v", err)
	}
	if again != 0 {
		t.Fatalf("已有角色时不应再次导入: %d", again)
	}
	roles, err := db.ListRoles(ctx)
	if err != nil {
		t.Fatalf("查询角色失败: %v", err)
	}
	if len(roles) != len(seeds) {
		t.Fatalf("角色数量不符: %d", len(roles))
	}
	if roles[0].ID != seeds[0].ID {
		t.Fatalf("角色 ID 应沿用配置: %s", roles[0].ID)
	}
}

func TestSeedRolesDoesNotOverwriteExistingRole(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	existing := mustRole(t, db, domain.RoleTypePlanner, "已存在角色")
	created, err := db.SeedRoles(ctx, []storage.RoleSeed{{
		ID:           domain.ID("planner"),
		Name:         "覆盖角色",
		Type:         domain.RoleTypePlanner,
		Model:        "provider/model",
		SystemPrompt: "提示词",
	}})
	if err != nil {
		t.Fatalf("初始化角色失败: %v", err)
	}
	if created != 0 {
		t.Fatalf("已有角色时不应导入: %d", created)
	}
	loaded, err := db.GetRole(ctx, existing.ID)
	if err != nil {
		t.Fatalf("读取角色失败: %v", err)
	}
	if loaded.Name != "已存在角色" {
		t.Fatalf("已有角色被覆盖: %s", loaded.Name)
	}
}
