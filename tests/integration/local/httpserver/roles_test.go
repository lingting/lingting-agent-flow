package httpserver_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/httpserver"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// roleServerConfig 构造带角色声明的配置，用于走真实的角色初始化路径。
func roleServerConfig() *config.Config {
	return &config.Config{
		Roles: []config.RoleConfig{
			{
				ID:           "planner",
				Name:         "架构规划师",
				Type:         domain.RoleTypePlanner,
				Runner:       config.RunnerPi,
				Model:        "provider/planner",
				SystemPrompt: "规划提示词",
				ToolsPolicy:  config.ToolsPolicyConfig{Mode: config.ToolsModeReadOnly},
			},
			{
				ID:           "developer",
				Name:         "开发工程师",
				Type:         domain.RoleTypeDeveloper,
				Runner:       config.RunnerPi,
				Model:        "provider/developer",
				SystemPrompt: "开发提示词",
				ToolsPolicy:  config.ToolsPolicyConfig{Mode: config.ToolsModeDevelopment},
			},
		},
	}
}

// roleWebFiles 是一份模拟的前端构建产物。
func roleWebFiles() fstest.MapFS {
	return fstest.MapFS{
		"assets/index-abc.js": &fstest.MapFile{Data: []byte("console.log(1)")},
		"index.html":          &fstest.MapFile{Data: []byte(indexHTML)},
	}
}

// startRoleServer 打开真实数据库并按需执行真实角色初始化，随后启动真实 HTTP 服务。
//
// seed 为 true 时走 app.RoleSeeds + SeedRoles 的正式路径；为 false 时保持空库，
// 用于验证接口返回真实空列表而不是临时造角色。
func startRoleServer(t *testing.T, seed bool) string {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	store, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	workspace.Cleanup(store.Close)
	if seed {
		seeds, seedErr := app.RoleSeeds(roleServerConfig())
		if seedErr != nil {
			t.Fatalf("转换角色配置失败: %v", seedErr)
		}
		if _, seedErr := store.SeedRoles(ctx, seeds); seedErr != nil {
			t.Fatalf("初始化角色失败: %v", seedErr)
		}
	}
	roles, err := app.NewRoleService(store)
	if err != nil {
		t.Fatalf("构造角色服务失败: %v", err)
	}
	server, err := httpserver.New(httpserver.Options{
		Address:    "127.0.0.1:0",
		Files:      roleWebFiles(),
		Health:     app.NewHealthService(store),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		Roles:      roles,
	})
	if err != nil {
		t.Fatalf("构造 HTTP 服务失败: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听随机端口失败: %v", err)
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(runCtx, listener)
	}()
	workspace.Cleanup(func() error {
		runCancel()
		select {
		case err := <-done:
			return err
		case <-time.After(15 * time.Second):
			return nil
		}
	})
	return "http://" + listener.Addr().String()
}

// TestRoleListServesInitializedRoles 校验真实初始化路径写入的角色可被接口读到。
func TestRoleListServesInitializedRoles(t *testing.T) {
	t.Parallel()
	baseURL := startRoleServer(t, true)

	status, header, body := get(t, baseURL, httpapi.PathRoles)
	if status != http.StatusOK {
		t.Fatalf("角色列表应返回 200，实际 %d，响应: %s", status, body)
	}
	if header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("角色列表应为 JSON，实际 %q", header.Get("Content-Type"))
	}
	var response httpapi.RoleListResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("解析角色列表失败: %v，响应: %s", err, body)
	}
	if response.Total != 2 || len(response.Records) != 2 {
		t.Fatalf("应返回 2 个初始化角色，实际 %+v", response)
	}
	seen := map[string]httpapi.RoleResponse{}
	for _, record := range response.Records {
		seen[record.ID] = record
	}
	planner, ok := seen["planner"]
	if !ok {
		t.Fatalf("缺少 planner 角色: %+v", response)
	}
	if planner.Type != domain.RoleTypePlanner.String() || planner.Runner != domain.RoleRunnerPi ||
		planner.Model != "provider/planner" || !planner.Enabled {
		t.Fatalf("planner 角色字段与配置不一致: %+v", planner)
	}
	developer, ok := seen["developer"]
	if !ok {
		t.Fatalf("缺少 developer 角色: %+v", response)
	}
	if developer.Type != domain.RoleTypeDeveloper.String() {
		t.Fatalf("developer 角色类型不符: %+v", developer)
	}
}

// TestRoleListRejectsUnsupportedMethodOverHTTP 校验真实 HTTP 服务返回 405 与 Allow。
func TestRoleListRejectsUnsupportedMethodOverHTTP(t *testing.T) {
	t.Parallel()
	baseURL := startRoleServer(t, true)
	status, header, body := do(t, baseURL, http.MethodDelete, httpapi.PathRoles)
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE 应返回 405，实际 %d，响应: %s", status, body)
	}
	if allow := header.Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("Allow 应为 GET, HEAD，实际 %q", allow)
	}
}

// TestRoleListEmptyDatabaseOverHTTP 校验空库启动后接口返回真实空列表。
func TestRoleListEmptyDatabaseOverHTTP(t *testing.T) {
	t.Parallel()
	baseURL := startRoleServer(t, false)

	status, _, body := get(t, baseURL, httpapi.PathRoles)
	if status != http.StatusOK {
		t.Fatalf("空库也应返回 200，实际 %d，响应: %s", status, body)
	}
	var response httpapi.RoleListResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("解析角色列表失败: %v，响应: %s", err, body)
	}
	if response.Total != 0 || len(response.Records) != 0 {
		t.Fatalf("空库应返回空列表，实际 %+v", response)
	}
}
