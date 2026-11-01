package cigate_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lingting/agentflow/internal/cigate"
)

// permissionRecorder 记录权限查询收到的请求，供用例断言。
type permissionRecorder struct {
	mutex   sync.Mutex
	method  string
	path    string
	headers http.Header
}

// record 保存一次请求的可见信息。
func (r *permissionRecorder) record(request *http.Request) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.method = request.Method
	r.path = request.URL.Path
	r.headers = request.Header.Clone()
}

// snapshot 返回记录到的请求信息。
func (r *permissionRecorder) snapshot() (string, string, http.Header) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.method, r.path, r.headers
}

// newPermissionServer 返回一个按给定状态码与响应体应答的 API 替身。
func newPermissionServer(t *testing.T, status int, body string) (*httptest.Server, *permissionRecorder) {
	t.Helper()
	recorder := &permissionRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		w.WriteHeader(status)
		if body != "" {
			_, _ = io.WriteString(w, body)
		}
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

// TestGitHubPermissionCheckerPermissions 校验权限等级到写入能力的映射。
func TestGitHubPermissionCheckerPermissions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		permission string
		want       bool
	}{
		{permission: "admin", want: true},
		{permission: "maintain", want: true},
		{permission: "write", want: true},
		{permission: "triage", want: false},
		{permission: "read", want: false},
		{permission: "none", want: false},
		{permission: "WRITE", want: true},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.permission, func(t *testing.T) {
			t.Parallel()
			server, _ := newPermissionServer(t, http.StatusOK,
				`{"permission":"`+testCase.permission+`"}`)
			checker := cigate.GitHubPermissionChecker{
				APIURL:     server.URL,
				Repository: "owner/repo",
				Token:      "token",
			}
			hasWrite, err := checker.HasWritePermission(context.Background(), "someone")
			if err != nil {
				t.Fatalf("查询权限失败: %v", err)
			}
			if hasWrite != testCase.want {
				t.Fatalf("权限 %q 的写入结论为 %v，期望 %v",
					testCase.permission, hasWrite, testCase.want)
			}
		})
	}
}

// TestGitHubPermissionCheckerRequest 校验查询请求的路径与请求头。
func TestGitHubPermissionCheckerRequest(t *testing.T) {
	t.Parallel()
	server, recorded := newPermissionServer(t, http.StatusOK, `{"permission":"write"}`)
	checker := cigate.GitHubPermissionChecker{
		APIURL:     server.URL + "/",
		Repository: "owner/repo",
		Token:      "token",
	}
	if _, err := checker.HasWritePermission(context.Background(), "someone"); err != nil {
		t.Fatalf("查询权限失败: %v", err)
	}
	method, path, headers := recorded.snapshot()
	if method != http.MethodGet {
		t.Fatalf("权限查询必须只读，实际方法 %s", method)
	}
	expectedPath := "/repos/owner/repo/collaborators/someone/permission"
	if path != expectedPath {
		t.Fatalf("权限查询路径为 %s，期望 %s", path, expectedPath)
	}
	if got := headers.Get("Authorization"); got != "Bearer token" {
		t.Fatalf("权限查询令牌头错误: %q", got)
	}
	if got := headers.Get("X-GitHub-Api-Version"); got != cigate.PermissionAPIVersion {
		t.Fatalf("权限查询 API 版本头错误: %q", got)
	}
	if got := headers.Get("Accept"); !strings.Contains(got, "github+json") {
		t.Fatalf("权限查询 Accept 头错误: %q", got)
	}
}

// TestGitHubPermissionCheckerErrors 校验无法确认权限时返回错误且不泄露令牌。
func TestGitHubPermissionCheckerErrors(t *testing.T) {
	t.Parallel()
	t.Run("接口返回非 200", func(t *testing.T) {
		t.Parallel()
		server, _ := newPermissionServer(t, http.StatusForbidden, `{"message":"forbidden"}`)
		checker := cigate.GitHubPermissionChecker{
			APIURL:     server.URL,
			Repository: "owner/repo",
			Token:      "secret-token",
		}
		_, err := checker.HasWritePermission(context.Background(), "someone")
		if err == nil {
			t.Fatal("非 200 响应必须返回错误")
		}
		if strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("错误信息不得包含令牌: %v", err)
		}
	})
	t.Run("响应缺少 permission 字段", func(t *testing.T) {
		t.Parallel()
		server, _ := newPermissionServer(t, http.StatusOK, `{}`)
		checker := cigate.GitHubPermissionChecker{
			APIURL:     server.URL,
			Repository: "owner/repo",
			Token:      "token",
		}
		if _, err := checker.HasWritePermission(context.Background(), "someone"); err == nil {
			t.Fatal("缺少 permission 字段必须返回错误")
		}
	})
	t.Run("响应不是合法 JSON", func(t *testing.T) {
		t.Parallel()
		server, _ := newPermissionServer(t, http.StatusOK, `not-json`)
		checker := cigate.GitHubPermissionChecker{
			APIURL:     server.URL,
			Repository: "owner/repo",
			Token:      "token",
		}
		if _, err := checker.HasWritePermission(context.Background(), "someone"); err == nil {
			t.Fatal("非法响应必须返回错误")
		}
	})
	t.Run("缺少令牌", func(t *testing.T) {
		t.Parallel()
		checker := cigate.GitHubPermissionChecker{
			APIURL:     "https://api.github.com",
			Repository: "owner/repo",
		}
		if _, err := checker.HasWritePermission(context.Background(), "someone"); err == nil {
			t.Fatal("缺少令牌必须返回错误")
		}
	})
	t.Run("仓库格式非法", func(t *testing.T) {
		t.Parallel()
		checker := cigate.GitHubPermissionChecker{
			APIURL:     "https://api.github.com",
			Repository: "owner",
			Token:      "token",
		}
		if _, err := checker.HasWritePermission(context.Background(), "someone"); err == nil {
			t.Fatal("仓库全名格式非法必须返回错误")
		}
	})
	t.Run("账号为空", func(t *testing.T) {
		t.Parallel()
		checker := cigate.GitHubPermissionChecker{
			APIURL:     "https://api.github.com",
			Repository: "owner/repo",
			Token:      "token",
		}
		if _, err := checker.HasWritePermission(context.Background(), "  "); err == nil {
			t.Fatal("账号为空必须返回错误")
		}
	})
	t.Run("上下文已取消", func(t *testing.T) {
		t.Parallel()
		server, _ := newPermissionServer(t, http.StatusOK, `{"permission":"write"}`)
		checker := cigate.GitHubPermissionChecker{
			APIURL:     server.URL,
			Repository: "owner/repo",
			Token:      "token",
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := checker.HasWritePermission(ctx, "someone"); err == nil {
			t.Fatal("上下文取消必须返回错误")
		}
	})
}
