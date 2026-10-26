package sqlite_test

import (
	"sync"
	"testing"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// seedDefinitions 返回一组用于并发初始化的角色定义。
func seedDefinitions() []storage.RoleSeed {
	return []storage.RoleSeed{
		{
			ID:           domain.ID("planner"),
			Name:         "架构规划师",
			Type:         domain.RoleTypePlanner,
			Runner:       domain.RoleRunnerPi,
			Model:        "provider/planner",
			SystemPrompt: "规划提示词",
			ToolsPolicy:  domain.DefaultToolsPolicy(domain.RoleTypePlanner),
		},
		{
			ID:           domain.ID("developer"),
			Name:         "开发工程师",
			Type:         domain.RoleTypeDeveloper,
			Runner:       domain.RoleRunnerPi,
			Model:        "provider/developer",
			SystemPrompt: "开发提示词",
			ToolsPolicy:  domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		},
	}
}

// TestConcurrentSeedRolesImportsOnce 验证并发初始化只导入一次，不会产生重复角色。
//
// 检查与写入在同一事务内完成，因此多个调用者同时初始化时只有第一个写入生效，
// 其余调用返回 0 条写入。
func TestConcurrentSeedRolesImportsOnce(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	const callers = 6
	var group sync.WaitGroup
	created := make(chan int, callers)
	failures := make(chan string, callers)
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			count, err := db.SeedRoles(ctx, seedDefinitions())
			if err != nil {
				failures <- "并发初始化角色失败: " + err.Error()
				return
			}
			created <- count
		}()
	}
	group.Wait()
	close(created)
	close(failures)
	for message := range failures {
		t.Fatal(message)
	}
	total := 0
	for count := range created {
		total += count
	}
	if total != len(seedDefinitions()) {
		t.Fatalf("并发初始化应只写入 %d 个角色，实际写入 %d 个", len(seedDefinitions()), total)
	}
	roles, err := db.ListRoles(ctx)
	if err != nil {
		t.Fatalf("查询角色失败: %v", err)
	}
	if len(roles) != len(seedDefinitions()) {
		t.Fatalf("角色数量应为 %d，实际 %d", len(seedDefinitions()), len(roles))
	}
}

// TestSeedRolesKeepsDisabledRoleAsExisting 验证禁用角色同样被视为已有记录。
//
// 初始化只在空表时导入，因此禁用角色不会被配置中的启用角色覆盖，也不会触发
// 追加其他配置角色。
func TestSeedRolesKeepsDisabledRoleAsExisting(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	existing := mustRole(t, db, domain.RoleTypePlanner, "已禁用规划师")
	existing.Enabled = false
	if err := db.UpdateRole(ctx, existing); err != nil {
		t.Fatalf("禁用角色失败: %v", err)
	}

	created, err := db.SeedRoles(ctx, seedDefinitions())
	if err != nil {
		t.Fatalf("初始化角色失败: %v", err)
	}
	if created != 0 {
		t.Fatalf("已有禁用角色时不应导入任何角色，实际 %d", created)
	}
	loaded, err := db.GetRole(ctx, existing.ID)
	if err != nil {
		t.Fatalf("读取角色失败: %v", err)
	}
	if loaded.Enabled {
		t.Fatal("禁用状态不应被初始化流程改写")
	}
	if loaded.Name != "已禁用规划师" {
		t.Fatalf("已有角色名称被覆盖: %s", loaded.Name)
	}
}
