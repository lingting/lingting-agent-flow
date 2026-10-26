package server_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/server"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// roleConfig 构造配置中的单个角色声明。
func roleConfig(id string, roleType domain.RoleType, name, model, prompt string) config.RoleConfig {
	return config.RoleConfig{
		ID:           id,
		Name:         name,
		Type:         roleType,
		Runner:       config.RunnerPi,
		Model:        model,
		SystemPrompt: prompt,
		ToolsPolicy:  config.ToolsPolicyConfig{Mode: toolsMode(roleType)},
	}
}

// toolsMode 返回角色类型对应的工具策略模式，与配置校验的期望一致。
func toolsMode(roleType domain.RoleType) string {
	switch roleType {
	case domain.RoleTypeDeveloper:
		return config.ToolsModeDevelopment
	case domain.RoleTypeValidator:
		return config.ToolsModeVerification
	default:
		return config.ToolsModeReadOnly
	}
}

// configWithRoles 构造一份带角色声明的测试配置。
func configWithRoles(t *testing.T, roles ...config.RoleConfig) *config.Config {
	t.Helper()
	cfg := testConfig(t)
	cfg.Roles = roles
	return cfg
}

// defaultRoles 是三条默认角色声明。
func defaultRoles() []config.RoleConfig {
	return []config.RoleConfig{
		roleConfig("planner", domain.RoleTypePlanner, "架构规划师", "provider/planner", "规划提示词"),
		roleConfig("developer", domain.RoleTypeDeveloper, "开发工程师", "provider/developer", "开发提示词"),
		roleConfig("validator", domain.RoleTypeValidator, "验证工程师", "provider/validator", "验证提示词"),
	}
}

// seedTestRoles 用独立连接直接写入角色，用于构造"数据库已有角色"的现场。
func seedTestRoles(t *testing.T, cfg *config.Config, roles ...domain.Role) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sqlite.Open(ctx, sqlite.Options{Path: cfg.Storage.DatabasePath})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	}()
	for _, role := range roles {
		if err := db.CreateRole(ctx, role); err != nil {
			t.Fatalf("写入角色失败: %v", err)
		}
	}
}

// runUntilHealthy 在后台启动服务，等待健康检查通过后返回取消函数与结果通道。
func runUntilHealthy(
	t *testing.T,
	instance *server.Server,
	cfg *config.Config,
) (context.CancelFunc, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- instance.Run(ctx, loader(cfg))
	}()
	waitHealthy(t, instance)
	return cancel, done
}

// stopServer 取消服务并等待其退出。
func stopServer(t *testing.T, cancel context.CancelFunc, done chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("正常关闭不应返回错误: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("等待服务关闭超时")
	}
}

// listRoles 用独立连接读取真实持久化的角色。
//
// 服务关闭后其数据库连接已经关闭，因此这里按同一路径重新打开：断言的是磁盘上的
// 持久化事实，而不是服务进程内的对象状态。
func listRoles(t *testing.T, cfg *config.Config) []domain.Role {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sqlite.Open(ctx, sqlite.Options{Path: cfg.Storage.DatabasePath})
	if err != nil {
		t.Fatalf("重新打开测试数据库失败: %v", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	}()
	roles, err := db.ListRoles(ctx)
	if err != nil {
		t.Fatalf("查询角色失败: %v", err)
	}
	return roles
}

// updateRole 用独立连接改写一个已持久化的角色。
func updateRole(t *testing.T, cfg *config.Config, role domain.Role) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sqlite.Open(ctx, sqlite.Options{Path: cfg.Storage.DatabasePath})
	if err != nil {
		t.Fatalf("重新打开测试数据库失败: %v", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	}()
	if err := db.UpdateRole(ctx, role); err != nil {
		t.Fatalf("改写角色失败: %v", err)
	}
}

// roleByID 按 ID 取角色。
func roleByID(roles []domain.Role, id domain.ID) (domain.Role, bool) {
	for _, role := range roles {
		if role.ID == id {
			return role, true
		}
	}
	return domain.Role{}, false
}

// TestFreshDatabaseImportsConfiguredRoles 校验全新数据库启动后初始角色被真实写入。
func TestFreshDatabaseImportsConfiguredRoles(t *testing.T) {
	t.Parallel()
	cfg := configWithRoles(t, defaultRoles()...)
	instance := newServer(t, server.Options{
		Address:    "127.0.0.1:0",
		OpenStore:  realStoreOpener(t),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
	})
	cancel, done := runUntilHealthy(t, instance, cfg)

	roles := listRoles(t, cfg)
	if len(roles) != 3 {
		t.Fatalf("全新数据库应导入 3 个角色，实际 %d", len(roles))
	}
	for _, want := range cfg.Roles {
		role, ok := roleByID(roles, domain.ID(want.ID))
		if !ok {
			t.Fatalf("缺少配置角色 %s", want.ID)
		}
		if role.Name != want.Name || role.Model != want.Model ||
			role.SystemPrompt != want.SystemPrompt || role.Type != want.Type {
			t.Fatalf("角色 %s 与配置不一致: %+v", want.ID, role)
		}
		if !role.Enabled {
			t.Fatalf("导入的角色应默认启用: %s", want.ID)
		}
		if len(role.ToolsPolicy.Permissions) == 0 {
			t.Fatalf("导入的角色应带默认工具策略: %s", want.ID)
		}
	}
	stopServer(t, cancel, done)
}

// TestRestartDoesNotDuplicateOrOverwriteRoles 校验重复启动不重复导入、不覆盖已有角色。
func TestRestartDoesNotDuplicateOrOverwriteRoles(t *testing.T) {
	t.Parallel()
	cfg := configWithRoles(t, defaultRoles()...)
	workspace := testkit.New(t)
	newInstance := func() *server.Server {
		return newServer(t, server.Options{
			Address:    "127.0.0.1:0",
			OpenStore:  realStoreOpener(t),
			SystemInfo: func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		})
	}

	first := newInstance()
	cancel, done := runUntilHealthy(t, first, cfg)
	stopServer(t, cancel, done)

	// 模拟"已持久化的角色配置被修改"：用独立连接改写数据库中的角色字段与启用状态。
	roles := listRoles(t, cfg)
	developer, ok := roleByID(roles, domain.ID("developer"))
	if !ok {
		t.Fatal("首次启动应已导入 developer 角色")
	}
	developer.Name = "已改名的开发"
	developer.Model = "provider/edited"
	developer.SystemPrompt = "已改写的提示词"
	developer.ToolsPolicy = domain.ToolsPolicy{
		Permissions: []domain.ToolPermission{domain.ToolPermissionRead},
		DenyTools:   []string{"shell"},
	}
	developer.Enabled = false
	developer.UpdatedAt = time.Now().UTC()
	updateRole(t, cfg, developer)

	second := newInstance()
	cancel, done = runUntilHealthy(t, second, cfg)
	stopServer(t, cancel, done)

	roles = listRoles(t, cfg)
	if len(roles) != 3 {
		t.Fatalf("重启后角色数量应仍为 3，实际 %d（%s）", len(roles), workspace.Root())
	}
	loaded, ok := roleByID(roles, domain.ID("developer"))
	if !ok {
		t.Fatal("重启后 developer 角色应仍存在")
	}
	if loaded.Name != "已改名的开发" || loaded.Model != "provider/edited" ||
		loaded.SystemPrompt != "已改写的提示词" || loaded.Enabled {
		t.Fatalf("重启覆盖了已有角色配置: %+v", loaded)
	}
	if len(loaded.ToolsPolicy.DenyTools) != 1 || loaded.ToolsPolicy.DenyTools[0] != "shell" {
		t.Fatalf("重启覆盖了已有工具策略: %+v", loaded.ToolsPolicy)
	}
}

// TestExistingRolesAreNotAugmentedFromConfig 校验已有部分角色时不会隐式追加其他角色。
func TestExistingRolesAreNotAugmentedFromConfig(t *testing.T) {
	t.Parallel()
	cfg := configWithRoles(t, defaultRoles()...)

	// 先手工写入一个角色，模拟"数据库已有记录"。
	role, err := domain.NewRole(
		"自定义规划师",
		domain.RoleTypePlanner,
		"provider/custom",
		"自定义提示词",
		domain.DefaultToolsPolicy(domain.RoleTypePlanner),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	seedTestRoles(t, cfg, role)

	instance := newServer(t, server.Options{
		Address:    "127.0.0.1:0",
		OpenStore:  realStoreOpener(t),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
	})
	cancel, done := runUntilHealthy(t, instance, cfg)
	stopServer(t, cancel, done)

	roles := listRoles(t, cfg)
	if len(roles) != 1 {
		t.Fatalf("已有角色时不应追加配置中的其他角色，实际 %d 条", len(roles))
	}
	if roles[0].ID != role.ID || roles[0].Name != "自定义规划师" {
		t.Fatalf("已有角色被改写: %+v", roles[0])
	}
}

// TestRoleSeedConversionFailureStopsStartup 校验配置转换失败时启动返回可定位错误。
func TestRoleSeedConversionFailureStopsStartup(t *testing.T) {
	t.Parallel()
	// 重复的角色 ID 会让 app.RoleSeeds 失败：不得静默忽略或写入虚构角色。
	cfg := configWithRoles(t, roleConfig("planner", domain.RoleTypePlanner, "规划师", "provider/a", "提示词"),
		roleConfig("planner", domain.RoleTypeDeveloper, "开发", "provider/b", "提示词"))
	instance := newServer(t, server.Options{
		OpenStore:  realStoreOpener(t),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{} },
	})
	err := instance.Run(context.Background(), loader(cfg))
	if err == nil {
		t.Fatal("角色配置转换失败时应返回错误")
	}
	if !strings.Contains(err.Error(), "角色初始化失败") {
		t.Fatalf("错误应指明角色初始化失败，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "roles[1]") {
		t.Fatalf("错误应定位到具体配置项，实际 %v", err)
	}
	if !errors.Is(err, server.ErrStartup) {
		t.Fatalf("错误应标记为启动失败，实际 %v", err)
	}
	if instance.OpenedStore() != nil {
		t.Fatal("启动失败后数据库应已关闭")
	}
}

// failingSeeder 是角色初始化失败的存储替身，其余能力转发给真实数据库。
type failingSeeder struct {
	storage.Store
	err error
}

// SeedRoles 实现 storage.RoleSeeder，始终返回注入的错误。
func (f failingSeeder) SeedRoles(ctx context.Context, seeds []storage.RoleSeed) (int, error) {
	return 0, f.err
}

// TestRoleSeedStorageFailureStopsStartup 校验存储写入失败时服务不进入可接收请求状态。
func TestRoleSeedStorageFailureStopsStartup(t *testing.T) {
	t.Parallel()
	cfg := configWithRoles(t, defaultRoles()...)
	seedErr := errors.New("磁盘只读")
	// 预留一个空闲地址后立即释放：用它验证角色初始化失败时没有 HTTP 监听。
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("预留监听地址失败: %v", err)
	}
	occupied := reserved.Addr().String()
	if err := reserved.Close(); err != nil {
		t.Fatalf("释放预留地址失败: %v", err)
	}
	instance := newServer(t, server.Options{
		Address: occupied,
		OpenStore: func(ctx context.Context, cfg *config.Config) (storage.Store, error) {
			db, openErr := sqlite.Open(ctx, sqlite.Options{Path: cfg.Storage.DatabasePath})
			if openErr != nil {
				return nil, openErr
			}
			return failingSeeder{Store: db, err: seedErr}, nil
		},
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{} },
	})
	err = instance.Run(context.Background(), loader(cfg))
	if err == nil {
		t.Fatal("角色初始化失败时应返回错误")
	}
	if !errors.Is(err, seedErr) {
		t.Fatalf("错误应包含存储层原因，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "初始化角色失败") {
		t.Fatalf("错误应指明角色初始化失败，实际 %v", err)
	}
	if instance.OpenedStore() != nil {
		t.Fatal("启动失败后数据库应已关闭")
	}
	// 服务未启动，监听端口应保持空闲：用同一地址重新监听必须成功。
	response, dialErr := http.Get("http://" + occupied + "/api/v1/health")
	if dialErr == nil {
		_ = response.Body.Close()
		t.Fatal("角色初始化失败后不应有 HTTP 服务在监听")
	}
}

// TestRepeatedStartupKeepsSingleRoleSet 校验重复启动与并发初始化都不会产生重复角色。
func TestRepeatedStartupKeepsSingleRoleSet(t *testing.T) {
	t.Parallel()
	cfg := configWithRoles(t, defaultRoles()...)
	for round := 0; round < 3; round++ {
		instance := newServer(t, server.Options{
			Address:    "127.0.0.1:0",
			OpenStore:  realStoreOpener(t),
			SystemInfo: func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		})
		cancel, done := runUntilHealthy(t, instance, cfg)
		stopServer(t, cancel, done)
		if roles := listRoles(t, cfg); len(roles) != 3 {
			t.Fatalf("第 %d 次启动后角色数量应为 3，实际 %d", round+1, len(roles))
		}
	}
}
