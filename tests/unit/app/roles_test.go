package app_test

import (
	"testing"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
)

func TestRoleSeedsConvertsConfigRoles(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Roles: []config.RoleConfig{
			{
				ID:           "planner",
				Name:         "架构规划师",
				Type:         domain.RoleTypePlanner,
				Runner:       config.RunnerPi,
				Model:        "provider/model",
				SystemPrompt: "规划提示词",
			},
			{
				ID:           "developer",
				Name:         "开发工程师",
				Type:         domain.RoleTypeDeveloper,
				Runner:       "",
				Model:        "provider/model",
				SystemPrompt: "开发提示词",
			},
		},
	}
	seeds, err := app.RoleSeeds(cfg)
	if err != nil {
		t.Fatalf("转换角色失败: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("角色数量不符: %d", len(seeds))
	}
	if seeds[0].ID != domain.ID("planner") || seeds[0].Type != domain.RoleTypePlanner {
		t.Fatalf("第一个角色不符: %+v", seeds[0])
	}
	if seeds[0].Runner != config.RunnerPi {
		t.Fatalf("Agent 运行时不符: %s", seeds[0].Runner)
	}
	if seeds[1].Runner != domain.RoleRunnerPi {
		t.Fatalf("空运行时应当回退到默认值: %q", seeds[1].Runner)
	}
	if !seeds[1].ToolsPolicy.Has(domain.ToolPermissionWrite) {
		t.Fatalf("开发角色应当具备写权限: %+v", seeds[1].ToolsPolicy)
	}
}

func TestRoleSeedsRejectsDuplicateID(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Roles: []config.RoleConfig{
			{ID: "planner", Name: "第一个", Type: domain.RoleTypePlanner},
			{ID: "planner", Name: "第二个", Type: domain.RoleTypeDeveloper},
		},
	}
	if _, err := app.RoleSeeds(cfg); err == nil {
		t.Fatal("重复角色 ID 应当被拒绝")
	}
}

func TestRoleSeedsRejectsInvalidType(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Roles: []config.RoleConfig{
			{ID: "reviewer", Name: "评审者", Type: domain.RoleType("reviewer")},
		},
	}
	if _, err := app.RoleSeeds(cfg); err == nil {
		t.Fatal("非法角色类型应当被拒绝")
	}
}

func TestRoleSeedsRejectsNilConfig(t *testing.T) {
	t.Parallel()
	if _, err := app.RoleSeeds(nil); err == nil {
		t.Fatal("空配置应当被拒绝")
	}
}
