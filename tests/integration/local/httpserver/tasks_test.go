package httpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/httpserver"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// taskServer 是带任务服务的真实 HTTP 服务。
type taskServer struct {
	baseURL   string
	host      domain.ExecutionHost
	planner   domain.Role
	developer domain.Role
}

// startTaskServer 在随机端口上启动带任务服务的真实 HTTP 服务。
func startTaskServer(t *testing.T) *taskServer {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	store, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	workspace.Cleanup(store.Close)
	tasks, err := app.NewTaskService(app.TaskServiceOptions{Store: store})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	server, err := httpserver.New(httpserver.Options{
		Address:    "127.0.0.1:0",
		Files:      taskWebFiles(),
		Health:     app.NewHealthService(store),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		Tasks:      tasks,
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

	fixture := &taskServer{baseURL: "http://" + listener.Addr().String()}
	now := time.Now().UTC()
	fixture.planner = seedServerRole(t, store, ctx, domain.RoleTypePlanner, "规划师", now)
	fixture.developer = seedServerRole(t, store, ctx, domain.RoleTypeDeveloper, "开发工程师", now)
	fixture.host = seedServerHost(t, store, ctx, now)
	return fixture
}

// taskWebFiles 是一份模拟的前端构建产物。
func taskWebFiles() fstest.MapFS {
	return fstest.MapFS{
		"assets/index-abc.js": &fstest.MapFile{Data: []byte("console.log(1)")},
		"index.html":          &fstest.MapFile{Data: []byte(indexHTML)},
	}
}

// seedServerRole 写入一个测试角色。
func seedServerRole(
	t *testing.T,
	store interface {
		CreateRole(ctx context.Context, role domain.Role) error
	},
	ctx context.Context,
	roleType domain.RoleType,
	name string,
	now time.Time,
) domain.Role {
	t.Helper()
	role, err := domain.NewRole(
		name,
		roleType,
		"provider/model",
		"提示词",
		domain.DefaultToolsPolicy(roleType),
		now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := store.CreateRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	return role
}

// seedServerHost 写入一个测试执行节点。
func seedServerHost(
	t *testing.T,
	store interface {
		CreateExecutionHost(ctx context.Context, host domain.ExecutionHost) error
	},
	ctx context.Context,
	now time.Time,
) domain.ExecutionHost {
	t.Helper()
	host, err := domain.NewExecutionHost(
		"",
		"测试节点",
		"10.0.0.1",
		22,
		"deploy",
		"/srv/agentflow",
		now,
		domain.WithPrivateKey(testPrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := store.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	return host
}

// request 发起一次 JSON 请求并返回状态码与响应体。
func (s *taskServer) request(
	t *testing.T,
	method, path string,
	body any,
) (int, http.Header, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, s.baseURL+path, reader)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("请求 %s %s 失败: %v", method, path, err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	return response.StatusCode, response.Header, string(payload)
}

// createBody 构造合法的建任务请求体。
func (s *taskServer) createBody(name string) map[string]any {
	return map[string]any{
		"name":              name,
		"description":       "接入真实接口",
		"host_id":           s.host.ID.String(),
		"repository_url":    "ssh://git.example.com/team/repo",
		"base_branch":       "main",
		"environment":       "go",
		"planner_role_id":   s.planner.ID.String(),
		"developer_role_id": s.developer.ID.String(),
	}
}

// TestTaskAPILifecycleOverHTTP 校验任务接口在真实 HTTP 服务上的完整生命周期。
func TestTaskAPILifecycleOverHTTP(t *testing.T) {
	t.Parallel()
	server := startTaskServer(t)

	status, _, body := server.request(t, http.MethodPost, httpapi.PathTasks, server.createBody("实现登录"))
	if status != http.StatusCreated {
		t.Fatalf("创建任务应返回 201，实际 %d: %s", status, body)
	}
	var created httpapi.TaskResponse
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("解析创建响应失败: %v", err)
	}
	if created.Status != string(domain.TaskStatusQueued) {
		t.Fatalf("新建任务状态应为 queued，实际 %s", created.Status)
	}

	// 列表
	status, _, body = server.request(t, http.MethodGet, httpapi.PathTasks, nil)
	if status != http.StatusOK {
		t.Fatalf("列表应返回 200，实际 %d: %s", status, body)
	}
	var page httpapi.TaskListResponse
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("解析列表响应失败: %v", err)
	}
	if page.Total != 1 || len(page.Records) != 1 {
		t.Fatalf("列表内容不符: %+v", page)
	}

	// 详情
	status, _, body = server.request(t, http.MethodGet, httpapi.PathTasks+"/"+created.ID, nil)
	if status != http.StatusOK {
		t.Fatalf("详情应返回 200，实际 %d: %s", status, body)
	}

	// 取消
	status, _, body = server.request(
		t,
		http.MethodPost,
		httpapi.PathTasks+"/"+created.ID+"/cancel",
		nil,
	)
	if status != http.StatusOK {
		t.Fatalf("取消应返回 200，实际 %d: %s", status, body)
	}
	var cancelled httpapi.TaskResponse
	if err := json.Unmarshal([]byte(body), &cancelled); err != nil {
		t.Fatalf("解析取消响应失败: %v", err)
	}
	if cancelled.Status != string(domain.TaskStatusCancelled) {
		t.Fatalf("等待中的任务应直接取消，实际 %s", cancelled.Status)
	}

	// 删除
	status, _, body = server.request(t, http.MethodDelete, httpapi.PathTasks+"/"+created.ID, nil)
	if status != http.StatusNoContent {
		t.Fatalf("删除应返回 204，实际 %d: %s", status, body)
	}

	// 删除后详情 404
	status, detailHeader, body := server.request(
		t,
		http.MethodGet,
		httpapi.PathTasks+"/"+created.ID,
		nil,
	)
	if status != http.StatusNotFound {
		t.Fatalf("已删除任务详情应返回 404，实际 %d", status)
	}
	if !strings.HasPrefix(detailHeader.Get("Content-Type"), "application/json") {
		t.Fatalf("404 必须返回 JSON，实际 %q", detailHeader.Get("Content-Type"))
	}
}

// TestUnknownTaskAPIReturnsJSONError 校验未知任务 API 路径返回 JSON 而不是前端页面。
func TestUnknownTaskAPIReturnsJSONError(t *testing.T) {
	t.Parallel()
	server := startTaskServer(t)

	for _, path := range []string{
		httpapi.PathTasks + "/nope/unknown",
		httpapi.APIPrefix + "/tasks-not-exist",
	} {
		status, header, body := server.request(t, http.MethodGet, path, nil)
		if status != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d", path, status)
		}
		if !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
			t.Fatalf("%s 必须返回 JSON，实际 %q", path, header.Get("Content-Type"))
		}
		if strings.Contains(body, "<div id=\"root\">") {
			t.Fatalf("%s 不应回退到前端页面: %s", path, body)
		}
		var failure httpapi.ErrorResponse
		if err := json.Unmarshal([]byte(body), &failure); err != nil {
			t.Fatalf("%s 解析错误响应失败: %v", path, err)
		}
		if failure.Error.Code != httpapi.CodeNotFound {
			t.Fatalf("%s 错误代码应为 %q，实际 %q", path, httpapi.CodeNotFound, failure.Error.Code)
		}
	}
}

// TestTaskAPIConflictStatusOverHTTP 校验状态冲突在真实服务上返回 409。
func TestTaskAPIConflictStatusOverHTTP(t *testing.T) {
	t.Parallel()
	server := startTaskServer(t)

	status, _, body := server.request(t, http.MethodPost, httpapi.PathTasks, server.createBody("任务"))
	if status != http.StatusCreated {
		t.Fatalf("创建任务失败: %d %s", status, body)
	}
	var created httpapi.TaskResponse
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("解析创建响应失败: %v", err)
	}
	// 未结束的任务不允许删除。
	status, _, body = server.request(t, http.MethodDelete, httpapi.PathTasks+"/"+created.ID, nil)
	if status != http.StatusConflict {
		t.Fatalf("删除等待中的任务应返回 409，实际 %d: %s", status, body)
	}
	var failure httpapi.ErrorResponse
	if err := json.Unmarshal([]byte(body), &failure); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if failure.Error.Code != httpapi.CodeConflict {
		t.Fatalf("错误代码应为 %q，实际 %q", httpapi.CodeConflict, failure.Error.Code)
	}
}

// testPrivateKeyPath 返回测试工作空间中的一个私钥文件路径。
//
// 本版本只支持私钥认证，构造执行节点时必须提供该路径。
func testPrivateKeyPath(t *testing.T) string {
	t.Helper()
	return testkit.PrivateKeyPath(t)
}
