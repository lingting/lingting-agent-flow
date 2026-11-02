package app_test

import (
	"testing"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// TestRoleSeedsProvideFourFixedRoles 校验内置默认定义提供四个固定角色。
func TestRoleSeedsProvideFourFixedRoles(t *testing.T) {
	t.Parallel()
	seeds := app.RoleSeeds()
	if len(seeds) != 4 {
		t.Fatalf("固定角色数量不符: %d", len(seeds))
	}
	expected := []struct {
		id       domain.ID
		roleType domain.RoleType
	}{
		{domain.RoleIDRequirementAnalyst, domain.RoleTypeRequirementAnalyst},
		{domain.RoleIDPlanner, domain.RoleTypePlanner},
		{domain.RoleIDDeveloper, domain.RoleTypeDeveloper},
		{domain.RoleIDValidator, domain.RoleTypeValidator},
	}
	for index, want := range expected {
		seed := seeds[index]
		if seed.ID != want.id || seed.Type != want.roleType {
			t.Fatalf("第 %d 个角色不符: %+v", index, seed)
		}
		if seed.Runner != domain.RoleRunnerPi {
			t.Fatalf("角色 %s 的运行时应为 %s: %s", seed.ID, domain.RoleRunnerPi, seed.Runner)
		}
		if seed.Name == "" || seed.Model == "" || seed.SystemPrompt == "" {
			t.Fatalf("角色 %s 缺少名称、模型或提示词: %+v", seed.ID, seed)
		}
		if len(seed.ToolsPolicy.Permissions) == 0 {
			t.Fatalf("角色 %s 缺少默认工具策略: %+v", seed.ID, seed.ToolsPolicy)
		}
	}
	// 需求分析师只读：工具策略不得授予写权限。
	if seeds[0].ToolsPolicy.Has(domain.ToolPermissionWrite) {
		t.Fatalf("需求分析师不应具备写权限: %+v", seeds[0].ToolsPolicy)
	}
}
