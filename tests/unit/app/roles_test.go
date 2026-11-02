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
	// 四个角色的默认策略经能力上限映射后必须遵守系统安全边界：规划角色与验收者只读，
	// 只有开发者可以写入并执行命令。
	for _, seed := range seeds {
		tools, err := domain.EffectivePiTools(seed.Type, seed.ToolsPolicy)
		if err != nil {
			t.Fatalf("计算角色 %s 的有效工具失败: %v", seed.ID, err)
		}
		writable := containsTool(tools, domain.PiToolEdit) || containsTool(tools, domain.PiToolWrite)
		executable := containsTool(tools, domain.PiToolBash)
		if seed.Type == domain.RoleTypeDeveloper {
			if !writable || !executable {
				t.Fatalf("开发者应可写入并执行命令: %v", tools)
			}
			continue
		}
		if writable || executable {
			t.Fatalf("角色 %s 不应具备写入或执行能力: %v", seed.ID, tools)
		}
	}
}

// containsTool 判断工具列表中是否包含指定工具。
func containsTool(tools []string, want string) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
}
