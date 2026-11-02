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
