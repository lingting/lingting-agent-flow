package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// roleAPIFixture 是角色接口测试的运行环境：数据库已初始化四个固定角色。
type roleAPIFixture struct {
	handler http.Handler
	host    domain.ExecutionHost
	project domain.Project
}

// newRoleAPIFixture 打开真实数据库，初始化固定角色并构造角色与任务接口处理器。
func newRoleAPIFixture(t *testing.T) *roleAPIFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: filepath.Join(testkit.Dir(t), "agentflow.db")})
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
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	roles, err := app.NewRoleService(db)
	if err != nil {
		t.Fatalf("构造角色服务失败: %v", err)
	}
	tasks, err := app.NewTaskService(app.TaskServiceOptions{Store: db, Logger: logger.Slog()})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	now := time.Now().UTC()
	return &roleAPIFixture{
		host:    apiHost(t, db, ctx, now),
		project: testkit.CreateProject(t, db, testkit.DefaultTestProjectID, now),
		handler: httpapi.NewHandler(httpapi.Deps{
			Roles:  roles,
			Tasks:  tasks,
			Logger: logger.Slog(),
		}),
	}
}

// send 发送一次请求并返回响应记录器。
func (f *roleAPIFixture) send(
	t *testing.T,
	method, target string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else if raw, ok := body.(string); ok {
		reader = bytes.NewReader([]byte(raw))
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder
}

// decodeRoleList 解析角色列表响应。
func decodeRoleList(t *testing.T, recorder *httptest.ResponseRecorder) httpapi.RoleListResponse {
	t.Helper()
	var response httpapi.RoleListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析角色列表失败: %v: %s", err, recorder.Body.String())
	}
	return response
}

// decodeRoleDetail 解析单角色配置响应。
func decodeRoleDetail(t *testing.T, recorder *httptest.ResponseRecorder) httpapi.RoleDetailResponse {
	t.Helper()
	var response httpapi.RoleDetailResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析角色配置失败: %v: %s", err, recorder.Body.String())
	}
	return response
}

// roleTarget 返回单角色接口路径。
func roleTarget(id domain.ID) string {
	return httpapi.PathRoles + "/" + id.String()
}

// TestRoleAPILifecycle 校验角色查询、读取完整配置与更新可编辑字段。
func TestRoleAPILifecycle(t *testing.T) {
	t.Parallel()
	fixture := newRoleAPIFixture(t)

	listed := fixture.send(t, http.MethodGet, httpapi.PathRoles, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("角色列表应返回 200，实际 %d: %s", listed.Code, listed.Body.String())
	}
	list := decodeRoleList(t, listed)
	if list.Total != 4 || len(list.Records) != 4 {
		t.Fatalf("固定角色数量不符: %+v", list)
	}
	fixedIDs := []string{
		domain.RoleIDRequirementAnalyst.String(),
		domain.RoleIDPlanner.String(),
		domain.RoleIDDeveloper.String(),
		domain.RoleIDValidator.String(),
	}
	for index, id := range fixedIDs {
		if list.Records[index].ID != id {
			t.Fatalf("第 %d 个角色标识不符: %+v", index, list.Records)
		}
		if !list.Records[index].Enabled {
			t.Fatalf("新建的固定角色应当启用: %+v", list.Records[index])
		}
	}

	detail := fixture.send(t, http.MethodGet, roleTarget(domain.RoleIDPlanner), nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("角色详情应返回 200，实际 %d: %s", detail.Code, detail.Body.String())
	}
	loaded := decodeRoleDetail(t, detail)
	if loaded.ID != domain.RoleIDPlanner.String() || loaded.Runner != domain.RoleRunnerPi {
		t.Fatalf("角色详情字段不符: %+v", loaded)
	}
	if loaded.SystemPrompt == "" || loaded.Model == "" || loaded.Name == "" {
		t.Fatalf("角色详情缺少可编辑字段: %+v", loaded)
	}
	if len(loaded.ToolsPolicy.Permissions) == 0 {
		t.Fatalf("角色详情缺少工具策略: %+v", loaded)
	}

	updated := fixture.send(t, http.MethodPut, roleTarget(domain.RoleIDPlanner), map[string]any{
		"system_prompt": "自定义的规划提示词",
		"model":         "group/free",
		"enabled":       false,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("更新角色应返回 200，实际 %d: %s", updated.Code, updated.Body.String())
	}
	saved := decodeRoleDetail(t, updated)
	if saved.SystemPrompt != "自定义的规划提示词" || saved.Model != "group/free" || saved.Enabled {
		t.Fatalf("更新结果不符: %+v", saved)
	}
	if saved.Name != loaded.Name || saved.Type != loaded.Type {
		t.Fatalf("不可修改字段被改写: %+v", saved)
	}

	// 重新读取服务端结果，确认配置真实落库。
	reloaded := decodeRoleDetail(t, fixture.send(t, http.MethodGet, roleTarget(domain.RoleIDPlanner), nil))
	if reloaded.SystemPrompt != "自定义的规划提示词" || reloaded.Model != "group/free" ||
		reloaded.Enabled {
		t.Fatalf("重新读取的角色配置不符: %+v", reloaded)
	}
}

// TestRoleAPIRejectsForbiddenFields 校验角色身份字段与非法取值被拒绝。
func TestRoleAPIRejectsForbiddenFields(t *testing.T) {
	t.Parallel()
	fixture := newRoleAPIFixture(t)

	for _, body := range []map[string]any{
		{"id": "planner"},
		{"name": "改名"},
		{"type": "developer"},
		{"runner": "other"},
		{"tools_policy": map[string]any{"permissions": []string{"write"}}},
	} {
		recorder := fixture.send(t, http.MethodPut, roleTarget(domain.RoleIDPlanner), body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("请求体 %v 应返回 400，实际 %d: %s", body, recorder.Code, recorder.Body.String())
		}
	}
	// 非法取值由领域校验拒绝：空模型不允许保存。
	invalid := fixture.send(t, http.MethodPut, roleTarget(domain.RoleIDPlanner), map[string]any{
		"model": "   ",
	})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("空模型应返回 400，实际 %d: %s", invalid.Code, invalid.Body.String())
	}
	missing := fixture.send(t, http.MethodGet, roleTarget("not_exists"), nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("不存在的角色应返回 404，实际 %d: %s", missing.Code, missing.Body.String())
	}
	// 角色集合固定：集合路径不接受新增，单角色路径不接受删除。
	created := fixture.send(t, http.MethodPost, httpapi.PathRoles, map[string]any{"name": "新角色"})
	if created.Code != http.StatusMethodNotAllowed {
		t.Fatalf("新增角色应返回 405，实际 %d: %s", created.Code, created.Body.String())
	}
	deleted := fixture.send(t, http.MethodDelete, roleTarget(domain.RoleIDPlanner), nil)
	if deleted.Code != http.StatusMethodNotAllowed {
		t.Fatalf("删除角色应返回 405，实际 %d: %s", deleted.Code, deleted.Body.String())
	}
}

// TestRoleAPIKeepsTaskCreationRoles 校验任务创建仍能使用角色列表返回的启用角色。
func TestRoleAPIKeepsTaskCreationRoles(t *testing.T) {
	t.Parallel()
	fixture := newRoleAPIFixture(t)

	list := decodeRoleList(t, fixture.send(t, http.MethodGet, httpapi.PathRoles, nil))
	enabledID := map[string]string{}
	for _, record := range list.Records {
		if record.Enabled {
			enabledID[record.Type] = record.ID
		}
	}
	plannerID, hasPlanner := enabledID[domain.RoleTypePlanner.String()]
	developerID, hasDeveloper := enabledID[domain.RoleTypeDeveloper.String()]
	if !hasPlanner || !hasDeveloper {
		t.Fatalf("列表未提供启用中的规划与开发角色: %+v", list.Records)
	}

	created := fixture.send(t, http.MethodPost, httpapi.PathTasks, map[string]any{
		"name":              "角色兼容任务",
		"description":       "验证任务创建仍能使用角色列表中的角色",
		"host_id":           fixture.host.ID.String(),
		"project_id":        fixture.project.ID,
		"repository_url":    "ssh://git.example.com/team/repo",
		"base_branch":       "main",
		"environment":       "go",
		"planner_role_id":   plannerID,
		"developer_role_id": developerID,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("创建任务应返回 201，实际 %d: %s", created.Code, created.Body.String())
	}
}
