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

// roleFixture 是角色配置服务的测试环境：数据库里已经有四个固定角色。
type roleFixture struct {
	db      *sqlite.DB
	service *app.RoleService
}

// newRoleFixture 在测试工作空间中初始化四个固定角色并构造角色配置服务。
func newRoleFixture(t *testing.T) *roleFixture {
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
	if _, err := db.SeedRoles(ctx, app.RoleSeeds()); err != nil {
		t.Fatalf("初始化固定角色失败: %v", err)
	}
	service, err := app.NewRoleService(db)
	if err != nil {
		t.Fatalf("构造角色服务失败: %v", err)
	}
	return &roleFixture{db: db, service: service}
}

// TestRoleServiceUpdatePersistsEditableFields 校验可编辑字段保存后重新读取一致。
//
// 断言的是重新读取的持久化结果，而不是 Update 的入参。
func TestRoleServiceUpdatePersistsEditableFields(t *testing.T) {
	t.Parallel()
	fixture := newRoleFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	original, err := fixture.service.Get(ctx, domain.RoleIDPlanner.String())
	if err != nil {
		t.Fatalf("读取规划角色失败: %v", err)
	}
	prompt := "自定义的规划提示词"
	model := "group/free"
	enabled := false
	updated, err := fixture.service.Update(ctx, domain.RoleIDPlanner.String(), app.RoleUpdate{
		SystemPrompt: &prompt,
		Model:        &model,
		Enabled:      &enabled,
	})
	if err != nil {
		t.Fatalf("更新角色失败: %v", err)
	}
	if updated.SystemPrompt != prompt || updated.Model != model || updated.Enabled {
		t.Fatalf("更新结果不符: %+v", updated)
	}
	// 不可修改的字段必须保持不变。
	if updated.ID != original.ID || updated.Type != original.Type {
		t.Fatalf("角色身份被改写: %+v", updated)
	}
	if updated.Name != original.Name || updated.Runner != original.Runner {
		t.Fatalf("角色名称或运行时被改写: %+v", updated)
	}

	reloaded, err := fixture.service.Get(ctx, domain.RoleIDPlanner.String())
	if err != nil {
		t.Fatalf("重新读取角色失败: %v", err)
	}
	if reloaded.SystemPrompt != prompt || reloaded.Model != model || reloaded.Enabled {
		t.Fatalf("重新读取的角色配置不符: %+v", reloaded)
	}
	// 只更新一个角色时其他角色保持默认配置。
	other, err := fixture.service.Get(ctx, domain.RoleIDDeveloper.String())
	if err != nil {
		t.Fatalf("读取开发角色失败: %v", err)
	}
	if other.Model != domain.DefaultRoleModel || !other.Enabled {
		t.Fatalf("其他角色不应被改动: %+v", other)
	}
}

// TestRoleServiceRejectsIncompleteConfig 校验不合法的角色配置被拒绝且不写入数据库。
func TestRoleServiceRejectsIncompleteConfig(t *testing.T) {
	t.Parallel()
	fixture := newRoleFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	empty := "  "
	if _, err := fixture.service.Update(ctx, domain.RoleIDDeveloper.String(), app.RoleUpdate{
		Model: &empty,
	}); !errors.Is(err, app.ErrRoleInvalidInput) {
		t.Fatalf("空模型应当被拒绝，实际: %v", err)
	}
	reloaded, err := fixture.service.Get(ctx, domain.RoleIDDeveloper.String())
	if err != nil {
		t.Fatalf("读取开发角色失败: %v", err)
	}
	if reloaded.Model != domain.DefaultRoleModel {
		t.Fatalf("被拒绝的更新不应写入数据库: %+v", reloaded)
	}
}

// TestRoleServiceUpdateToolsPolicy 校验工具策略按角色能力上限校验、持久化并影响实际工具。
//
// 断言的是保存后重新读取的策略与由它计算出的工具列表：配置保存之后，Pi 实际可用工具
// 必须与配置一致，拒绝列表优先于允许列表。
func TestRoleServiceUpdateToolsPolicy(t *testing.T) {
	t.Parallel()
	fixture := newRoleFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	// 开发者可以拒绝 bash：拒绝列表优先于已授权的 execute 能力。
	policy := domain.ToolsPolicy{
		Permissions: []domain.ToolPermission{
			domain.ToolPermissionRead,
			domain.ToolPermissionWrite,
			domain.ToolPermissionExecute,
		},
		DenyTools: []string{domain.PiToolBash},
	}
	updated, err := fixture.service.Update(ctx, domain.RoleIDDeveloper.String(), app.RoleUpdate{
		ToolsPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("更新开发者工具策略失败: %v", err)
	}
	reloaded, err := fixture.service.Get(ctx, domain.RoleIDDeveloper.String())
	if err != nil {
		t.Fatalf("重新读取开发者失败: %v", err)
	}
	if len(reloaded.ToolsPolicy.DenyTools) != 1 || reloaded.ToolsPolicy.DenyTools[0] != domain.PiToolBash {
		t.Fatalf("工具策略未持久化: %+v", reloaded.ToolsPolicy)
	}
	tools, err := domain.EffectivePiTools(updated.Type, updated.ToolsPolicy)
	if err != nil {
		t.Fatalf("计算开发者有效工具失败: %v", err)
	}
	if containsTool(tools, domain.PiToolBash) {
		t.Fatalf("拒绝列表应优先于授予的能力: %v", tools)
	}
	if !containsTool(tools, domain.PiToolWrite) {
		t.Fatalf("开发者仍应保留写入工具: %v", tools)
	}

	// 规划角色不能通过配置获得写入或执行能力。
	for _, invalid := range [][]domain.ToolPermission{
		{domain.ToolPermissionRead, domain.ToolPermissionWrite},
		{domain.ToolPermissionRead, domain.ToolPermissionExecute},
		{domain.ToolPermissionExecute},
	} {
		request := domain.ToolsPolicy{Permissions: invalid}
		if _, updateErr := fixture.service.Update(ctx, domain.RoleIDPlanner.String(), app.RoleUpdate{
			ToolsPolicy: &request,
		}); !errors.Is(updateErr, app.ErrRoleInvalidInput) {
			t.Fatalf("规划角色的越界能力 %v 应被拒绝，实际: %v", invalid, updateErr)
		}
	}
	planner, err := fixture.service.Get(ctx, domain.RoleIDPlanner.String())
	if err != nil {
		t.Fatalf("读取规划角色失败: %v", err)
	}
	plannerTools, err := domain.EffectivePiTools(planner.Type, planner.ToolsPolicy)
	if err != nil {
		t.Fatalf("计算规划角色有效工具失败: %v", err)
	}
	for _, forbidden := range []string{domain.PiToolBash, domain.PiToolEdit, domain.PiToolWrite} {
		if containsTool(plannerTools, forbidden) {
			t.Fatalf("规划角色不应获得工具 %s: %v", forbidden, plannerTools)
		}
	}
}
