package httpserver_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/httpserver"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// environmentServerFixture 是环境查询接口的真实服务端现场。
type environmentServerFixture struct {
	baseURL string
	store   storage.Store
	host    domain.ExecutionHost
}

// startEnvironmentServer 打开真实数据库、写入环境现场并启动真实 HTTP 服务。
//
// 依赖注入走正式路径：环境查询服务由真实存储构造，再经 httpserver.Options 传入，
// 因此这里验证的是"路由 + 依赖装配 + 应用层 + 存储"的完整链路。
func startEnvironmentServer(t *testing.T) *environmentServerFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	store, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	workspace.Cleanup(store.Close)
	host, err := domain.NewExecutionHost(
		"host-1",
		"构建机",
		"192.0.2.10",
		22,
		"deploy",
		"/home/deploy/.lingting/agentflow",
		time.Now().UTC(),
		domain.WithPrivateKey(testkit.PrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := store.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	baseRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	goRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	writeServerVersion(t, ctx, store, host.ID, baseRef, 1, domain.BuildStatusSucceeded, true, "")
	writeServerVersion(
		t,
		ctx,
		store,
		host.ID,
		goRef,
		1,
		domain.BuildStatusSucceeded,
		true,
		domain.FormatVersionLabel(1),
	)
	writeServerVersion(
		t,
		ctx,
		store,
		host.ID,
		goRef,
		2,
		domain.BuildStatusFailed,
		false,
		domain.FormatVersionLabel(1),
	)
	environments, err := app.NewEnvironmentQueryService(store)
	if err != nil {
		t.Fatalf("构造环境查询服务失败: %v", err)
	}
	server, err := httpserver.New(httpserver.Options{
		Address:      "127.0.0.1:0",
		Files:        roleWebFiles(),
		Health:       app.NewHealthService(store),
		SystemInfo:   func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		Environments: environments,
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
	return &environmentServerFixture{
		baseURL: "http://" + listener.Addr().String(),
		store:   store,
		host:    host,
	}
}

// writeServerVersion 写入一个合法的环境版本记录。
func writeServerVersion(
	t *testing.T,
	ctx context.Context,
	store storage.Store,
	hostID domain.ID,
	ref domain.EnvironmentRef,
	number int,
	status domain.BuildStatus,
	usable bool,
	parent string,
) {
	t.Helper()
	label := ref.Label() + "-" + domain.FormatVersionLabel(number)
	version := domain.EnvironmentVersion{
		HostID:            hostID,
		Ref:               ref,
		Number:            number,
		DockerfileSHA256:  serverHash("dockerfile-" + label),
		BuildConfigSHA256: serverHash("config-" + label),
		ContextSHA256:     serverHash("context-" + ref.Label()),
		ParentVersion:     parent,
		ImageRef:          "agentflow-" + string(ref.Type) + "-" + ref.Name + ":v0001-abcdefabcdef",
		Status:            status,
		CreatedAt:         time.Now().UTC().Truncate(time.Millisecond),
	}
	if usable {
		version.ImageID = "sha256:" + strings.Repeat("b", 64)
		builtAt := time.Now().UTC().Truncate(time.Millisecond)
		version.BuiltAt = &builtAt
	}
	if status == domain.BuildStatusFailed {
		version.ErrorSummary = "构建失败: 镜像核实未通过"
	}
	if err := version.Validate(); err != nil {
		t.Fatalf("构造的版本应当合法: %v", err)
	}
	if err := store.CreateEnvironmentVersion(ctx, version); err != nil {
		t.Fatalf("写入环境版本失败: %v", err)
	}
}

// serverHash 返回测试用的 64 位十六进制摘要。
func serverHash(seed string) string {
	digits := []byte(strings.Repeat("0", 64))
	for index, char := range []byte(seed) {
		digits[index%64] = "0123456789abcdef"[int(char)%16]
	}
	return string(digits)
}

// TestEnvironmentListOverRealServer 校验真实 HTTP 服务上的环境列表返回真实记录。
func TestEnvironmentListOverRealServer(t *testing.T) {
	t.Parallel()
	fixture := startEnvironmentServer(t)
	target := httpapi.PathEnvironments + "?host_id=" + url.QueryEscape(fixture.host.ID.String())
	status, header, body := get(t, fixture.baseURL, target)
	if status != http.StatusOK {
		t.Fatalf("环境列表应返回 200，实际 %d，响应: %s", status, body)
	}
	if header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("环境列表应为 JSON，实际 %q", header.Get("Content-Type"))
	}
	var response httpapi.EnvironmentListResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("解析环境列表失败: %v，响应: %s", err, body)
	}
	if response.Total != 2 || len(response.Records) != 2 {
		t.Fatalf("应返回 2 个环境，实际 %+v", response)
	}
	seen := make(map[string]httpapi.EnvironmentResponse, len(response.Records))
	for _, record := range response.Records {
		seen[record.Type+"/"+record.Name] = record
	}
	goEnv, ok := seen["language/go"]
	if !ok {
		t.Fatalf("响应缺少 language/go: %+v", response.Records)
	}
	if goEnv.LatestVersion != "v0002" || goEnv.CurrentVersion != "v0001" {
		t.Fatalf("语言环境版本语义不符: %+v", goEnv)
	}
	if len(goEnv.Versions) != 2 {
		t.Fatalf("语言环境版本数应为 2，实际 %d", len(goEnv.Versions))
	}
	if goEnv.Versions[0].BuildStatus != domain.BuildStatusSucceeded.String() ||
		!goEnv.Versions[0].Usable {
		t.Fatalf("v0001 应为成功且可用: %+v", goEnv.Versions[0])
	}
	if goEnv.Versions[1].BuildStatus != domain.BuildStatusFailed.String() ||
		goEnv.Versions[1].Usable {
		t.Fatalf("v0002 应为失败且不可用: %+v", goEnv.Versions[1])
	}
}

// TestEnvironmentVersionOverRealServer 校验真实 HTTP 服务上的单版本查询。
func TestEnvironmentVersionOverRealServer(t *testing.T) {
	t.Parallel()
	fixture := startEnvironmentServer(t)
	target := httpapi.PathEnvironments + "/language/go/versions/2?host_id=" +
		url.QueryEscape(fixture.host.ID.String())
	status, _, body := get(t, fixture.baseURL, target)
	if status != http.StatusOK {
		t.Fatalf("版本查询应返回 200，实际 %d，响应: %s", status, body)
	}
	var response httpapi.EnvironmentVersionResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("解析版本响应失败: %v，响应: %s", err, body)
	}
	if response.Version != "v0002" || response.BuildStatus != domain.BuildStatusFailed.String() {
		t.Fatalf("版本字段不符: %+v", response)
	}
	if response.Usable {
		t.Fatalf("构建失败的版本不得标记为可用: %+v", response)
	}
	if response.ParentVersion != domain.FormatVersionLabel(1) {
		t.Fatalf("语言环境必须如实给出父版本: %+v", response)
	}
	if response.ErrorSummary == "" {
		t.Fatalf("失败版本应给出可诊断摘要: %+v", response)
	}
}

// TestEnvironmentQueryDoesNotMutateRemoteOrStore 校验查询不会改动持久化记录。
func TestEnvironmentQueryDoesNotMutateRemoteOrStore(t *testing.T) {
	t.Parallel()
	fixture := startEnvironmentServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	before, err := fixture.store.ListEnvironmentVersions(ctx, storage.EnvironmentVersionFilter{
		HostID: fixture.host.ID,
	})
	if err != nil {
		t.Fatalf("读取环境版本失败: %v", err)
	}
	target := httpapi.PathEnvironments + "?host_id=" + url.QueryEscape(fixture.host.ID.String())
	if status, _, body := get(t, fixture.baseURL, target); status != http.StatusOK {
		t.Fatalf("环境列表应返回 200，实际 %d，响应: %s", status, body)
	}
	versionTarget := httpapi.PathEnvironments + "/base/base/versions/1?host_id=" +
		url.QueryEscape(fixture.host.ID.String())
	if status, _, body := get(t, fixture.baseURL, versionTarget); status != http.StatusOK {
		t.Fatalf("版本查询应返回 200，实际 %d，响应: %s", status, body)
	}
	after, err := fixture.store.ListEnvironmentVersions(ctx, storage.EnvironmentVersionFilter{
		HostID: fixture.host.ID,
	})
	if err != nil {
		t.Fatalf("读取环境版本失败: %v", err)
	}
	if len(before) != len(after) {
		t.Fatalf("查询不得写入版本记录：之前 %d 条，之后 %d 条", len(before), len(after))
	}
	for index := range before {
		if before[index].Status != after[index].Status ||
			before[index].ImageID != after[index].ImageID ||
			before[index].Number != after[index].Number {
			t.Fatalf("查询不得修改版本记录: %+v vs %+v", before[index], after[index])
		}
	}
}

// TestEnvironmentQueryErrorsOverRealServer 校验真实服务上的错误映射。
func TestEnvironmentQueryErrorsOverRealServer(t *testing.T) {
	t.Parallel()
	fixture := startEnvironmentServer(t)
	hostID := fixture.host.ID.String()
	cases := []struct {
		name   string
		method string
		target string
		status int
	}{
		{
			name:   "缺少 host_id",
			method: http.MethodGet,
			target: httpapi.PathEnvironments,
			status: http.StatusBadRequest,
		},
		{
			name:   "节点不存在",
			method: http.MethodGet,
			target: httpapi.PathEnvironments + "?host_id=missing-host",
			status: http.StatusNotFound,
		},
		{
			name:   "版本不存在",
			method: http.MethodGet,
			target: httpapi.PathEnvironments + "/base/base/versions/9?host_id=" + hostID,
			status: http.StatusNotFound,
		},
		{
			name:   "环境类型非法",
			method: http.MethodGet,
			target: httpapi.PathEnvironments + "/unknown/base/versions/1?host_id=" + hostID,
			status: http.StatusBadRequest,
		},
		{
			name:   "方法不支持",
			method: http.MethodPost,
			target: httpapi.PathEnvironments + "?host_id=" + hostID,
			status: http.StatusMethodNotAllowed,
		},
	}
	for _, testCase := range cases {
		status, header, body := do(t, fixture.baseURL, testCase.method, testCase.target)
		if status != testCase.status {
			t.Fatalf("%s 应返回 %d，实际 %d，响应: %s", testCase.name, testCase.status, status, body)
		}
		if testCase.status == http.StatusMethodNotAllowed {
			if allow := header.Get("Allow"); allow != "GET, HEAD" {
				t.Fatalf("%s 的 Allow 应为 GET, HEAD，实际 %q", testCase.name, allow)
			}
		}
		var response httpapi.ErrorResponse
		if err := json.Unmarshal([]byte(body), &response); err != nil {
			t.Fatalf("%s 的响应不是统一错误结构: %v，响应: %s", testCase.name, err, body)
		}
		if response.Error.Message == "" {
			t.Fatalf("%s 的错误响应应包含可读说明: %s", testCase.name, body)
		}
	}
}

// TestEnvironmentQueryWithoutServiceOverRealServer 校验缺少依赖时真实服务返回 503。
func TestEnvironmentQueryWithoutServiceOverRealServer(t *testing.T) {
	t.Parallel()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	store, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	workspace.Cleanup(store.Close)
	server, err := httpserver.New(httpserver.Options{
		Address:    "127.0.0.1:0",
		Files:      roleWebFiles(),
		Health:     app.NewHealthService(store),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		// 刻意不注入 Environments：接口必须返回 503，而不是伪造空列表。
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
	status, _, body := get(
		t,
		"http://"+listener.Addr().String(),
		httpapi.PathEnvironments+"?host_id=host-1",
	)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("缺少依赖时应返回 503，实际 %d，响应: %s", status, body)
	}
}
