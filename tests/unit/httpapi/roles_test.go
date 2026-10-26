package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// roleAPIFixture 是角色接口测试的运行环境。
type roleAPIFixture struct {
	handler http.Handler
	db      *sqlite.DB
	service *app.RoleService
}

// newRoleAPIFixture 打开真实数据库并构造只读角色服务与处理器。
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
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	service, err := app.NewRoleService(db)
	if err != nil {
		t.Fatalf("构造角色服务失败: %v", err)
	}
	return &roleAPIFixture{
		db:      db,
		service: service,
		handler: httpapi.NewHandler(httpapi.Deps{Roles: service, Logger: logger.Slog()}),
	}
}

// createRole 写入一个真实角色记录。
func (f *roleAPIFixture) createRole(
	t *testing.T,
	ctx context.Context,
	roleType domain.RoleType,
	name string,
	enabled bool,
) domain.Role {
	t.Helper()
	role, err := domain.NewRole(
		name,
		roleType,
		"provider/model",
		name+"的系统提示词",
		domain.DefaultToolsPolicy(roleType),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	role.Enabled = enabled
	if err := f.db.CreateRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	return role
}

// TestRoleListReturnsPersistedRoles 校验接口返回真实持久化的角色与最小字段集合。
func TestRoleListReturnsPersistedRoles(t *testing.T) {
	t.Parallel()
	fixture := newRoleAPIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	planner := fixture.createRole(t, ctx, domain.RoleTypePlanner, "架构规划师", true)
	developer := fixture.createRole(t, ctx, domain.RoleTypeDeveloper, "开发工程师", true)

	recorder := doRequest(t, fixture.handler, http.MethodGet, httpapi.PathRoles, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("角色列表应返回 200，实际 %d，响应: %s", recorder.Code, recorder.Body.String())
	}
	var raw struct {
		Records []map[string]any `json:"records"`
		Total   int              `json:"total"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if raw.Total != 2 || len(raw.Records) != 2 {
		t.Fatalf("应返回 2 个角色，实际 total=%d records=%d", raw.Total, len(raw.Records))
	}
	wantKeys := []string{"enabled", "id", "model", "name", "runner", "type"}
	for index, record := range raw.Records {
		keys := make([]string, 0, len(record))
		for key := range record {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) != len(wantKeys) {
			t.Fatalf("第 %d 条角色字段应只有 %v，实际 %v", index, wantKeys, keys)
		}
		for position, key := range wantKeys {
			if keys[position] != key {
				t.Fatalf("第 %d 条角色字段应只有 %v，实际 %v", index, wantKeys, keys)
			}
		}
		if _, leaked := record["system_prompt"]; leaked {
			t.Fatalf("响应不得包含 system_prompt: %v", record)
		}
	}
	var body httpapi.RoleListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析角色列表失败: %v", err)
	}
	got := map[string]httpapi.RoleResponse{}
	for _, record := range body.Records {
		got[record.ID] = record
	}
	for _, want := range []domain.Role{planner, developer} {
		record, ok := got[want.ID.String()]
		if !ok {
			t.Fatalf("响应缺少角色 %s", want.ID)
		}
		if record.Name != want.Name || record.Type != want.Type.String() ||
			record.Runner != want.Runner || record.Model != want.Model {
			t.Fatalf("角色字段与持久化记录不一致: %+v vs %+v", record, want)
		}
		if !record.Enabled {
			t.Fatalf("启用角色应返回 enabled=true: %+v", record)
		}
	}
}

// TestRoleListKeepsDisabledRoles 校验禁用角色如实返回 enabled=false。
//
// 保留禁用角色是为了诊断；创建任务时的选择器必须自行排除它们。
func TestRoleListKeepsDisabledRoles(t *testing.T) {
	t.Parallel()
	fixture := newRoleAPIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	disabled := fixture.createRole(t, ctx, domain.RoleTypeDeveloper, "已禁用开发", false)

	recorder := doRequest(t, fixture.handler, http.MethodGet, httpapi.PathRoles, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("角色列表应返回 200，实际 %d", recorder.Code)
	}
	var body httpapi.RoleListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析角色列表失败: %v", err)
	}
	if body.Total != 1 || len(body.Records) != 1 {
		t.Fatalf("禁用角色应保留在列表中，实际 %+v", body)
	}
	record := body.Records[0]
	if record.ID != disabled.ID.String() || record.Enabled {
		t.Fatalf("禁用角色应返回 enabled=false，实际 %+v", record)
	}
}

// TestRoleListEmptyDatabaseReturnsEmptyList 校验空库返回真实空列表而不是伪造角色。
func TestRoleListEmptyDatabaseReturnsEmptyList(t *testing.T) {
	t.Parallel()
	fixture := newRoleAPIFixture(t)

	recorder := doRequest(t, fixture.handler, http.MethodGet, httpapi.PathRoles, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("空库也应返回 200，实际 %d", recorder.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if string(raw["records"]) != "[]" {
		t.Fatalf("空库应返回空数组，实际 %s", raw["records"])
	}
	if string(raw["total"]) != "0" {
		t.Fatalf("空库 total 应为 0，实际 %s", raw["total"])
	}
	// 接口不得在读取路径上写入任何角色。
	roles, err := fixture.db.ListRoles(context.Background())
	if err != nil {
		t.Fatalf("查询角色失败: %v", err)
	}
	if len(roles) != 0 {
		t.Fatalf("读取角色不应创建记录，实际 %d 条", len(roles))
	}
}

// TestRoleListRejectsUnsupportedMethods 校验不支持的方法返回 405 与 Allow。
func TestRoleListRejectsUnsupportedMethods(t *testing.T) {
	t.Parallel()
	fixture := newRoleAPIFixture(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		recorder := doRequest(t, fixture.handler, method, httpapi.PathRoles, "{}")
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s 应返回 405，实际 %d", method, recorder.Code)
		}
		if allow := recorder.Header().Get("Allow"); allow != "GET, HEAD" {
			t.Fatalf("%s 的 Allow 应为 GET, HEAD，实际 %q", method, allow)
		}
	}
}

// TestRoleListWithoutServiceReturnsUnavailable 校验依赖缺失时返回 503 而不是空列表。
func TestRoleListWithoutServiceReturnsUnavailable(t *testing.T) {
	t.Parallel()
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	handler := httpapi.NewHandler(httpapi.Deps{Logger: logger.Slog()})

	recorder := doRequest(t, handler, http.MethodGet, httpapi.PathRoles, "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("角色服务未初始化时应返回 503，实际 %d", recorder.Code)
	}
	var body httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if body.Error.Code != httpapi.CodeInternal {
		t.Fatalf("错误代码应为 %s，实际 %q", httpapi.CodeInternal, body.Error.Code)
	}
}

// TestRoleListStorageFailureIsNotAnEmptyList 校验存储错误不被伪装成成功空列表。
func TestRoleListStorageFailureIsNotAnEmptyList(t *testing.T) {
	t.Parallel()
	fixture := newRoleAPIFixture(t)
	// 关闭数据库连接，制造真实的存储层错误。
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	recorder := doRequest(t, fixture.handler, http.MethodGet, httpapi.PathRoles, "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("存储错误应返回 500，实际 %d，响应: %s", recorder.Code, recorder.Body.String())
	}
	var body httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if body.Error.Code != httpapi.CodeInternal {
		t.Fatalf("错误代码应为 %s，实际 %q", httpapi.CodeInternal, body.Error.Code)
	}
	if body.Error.Message == "" {
		t.Fatal("错误响应应包含可读说明")
	}
}

// TestRoleListSubPathIsNotFound 校验本阶段不提供角色 CRUD 子路径。
func TestRoleListSubPathIsNotFound(t *testing.T) {
	t.Parallel()
	fixture := newRoleAPIFixture(t)
	recorder := doRequest(t, fixture.handler, http.MethodGet, httpapi.PathRoles+"/planner", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("角色子路径应返回 404，实际 %d", recorder.Code)
	}
}
