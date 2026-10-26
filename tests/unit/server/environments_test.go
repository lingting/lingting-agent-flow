package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/server"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// environmentConfig 构造只用于环境查询装配验证的配置。
func environmentConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := testConfig(t)
	// 角色与业务服务不需要在本用例中生效：这里只验证环境查询的装配链路。
	cfg.Roles = defaultRoles()
	return cfg
}

// seedEnvironmentVersions 用独立连接写入执行节点与环境版本现场。
//
// 返回的执行节点标识用于后续查询；写入发生在服务启动之前，与真实部署一致。
func seedEnvironmentVersions(t *testing.T, cfg *config.Config) domain.ID {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store, err := sqlite.Open(ctx, sqlite.Options{Path: cfg.Storage.DatabasePath})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	}()
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
	seedStoreVersion(t, ctx, store, host.ID, baseRef, 1, domain.BuildStatusSucceeded, true, "")
	seedStoreVersion(
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
	seedStoreVersion(
		t,
		ctx,
		store,
		host.ID,
		goRef,
		2,
		domain.BuildStatusPending,
		false,
		domain.FormatVersionLabel(1),
	)
	return host.ID
}

// seedStoreVersion 写入一个合法的环境版本记录。
func seedStoreVersion(
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
		DockerfileSHA256:  assemblyHash("dockerfile-" + label),
		BuildConfigSHA256: assemblyHash("config-" + label),
		ContextSHA256:     assemblyHash("context-" + ref.Label()),
		ParentVersion:     parent,
		ImageRef:          "agentflow-" + string(ref.Type) + "-" + ref.Name + ":v0001-abcdefabcdef",
		Status:            status,
		CreatedAt:         time.Now().UTC().Truncate(time.Millisecond),
	}
	if usable {
		version.ImageID = "sha256:" + strings.Repeat("c", 64)
		builtAt := time.Now().UTC().Truncate(time.Millisecond)
		version.BuiltAt = &builtAt
	}
	if err := version.Validate(); err != nil {
		t.Fatalf("构造的版本应当合法: %v", err)
	}
	if err := store.CreateEnvironmentVersion(ctx, version); err != nil {
		t.Fatalf("写入环境版本失败: %v", err)
	}
}

// assemblyHash 返回测试用的 64 位十六进制摘要。
func assemblyHash(seed string) string {
	digits := []byte(strings.Repeat("0", 64))
	for index, char := range []byte(seed) {
		digits[index%64] = "0123456789abcdef"[int(char)%16]
	}
	return string(digits)
}

// getJSON 发起一次 GET 请求并返回状态码与响应体。
func getJSON(t *testing.T, target string) (int, string) {
	t.Helper()
	response, err := http.Get(target)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", target, err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取 %s 响应体失败: %v", target, err)
	}
	return response.StatusCode, string(body)
}

// TestServerWiresEnvironmentQuery 校验统一服务装配后环境查询接口真实可用。
//
// 该用例走真实装配路径：server.New + Run 打开真实数据库与迁移，Server 内部按既有顺序
// 构造环境只读查询服务并注入 HTTP 服务。测试不注入任何环境相关替身。
func TestServerWiresEnvironmentQuery(t *testing.T) {
	t.Parallel()
	cfg := environmentConfig(t)
	hostID := seedEnvironmentVersions(t, cfg)
	instance := newServer(t, server.Options{
		Address:      "127.0.0.1:0",
		OpenStore:    realStoreOpener(t),
		SystemInfo:   func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		RequireWebUI: false,
	})
	cancel, done := runUntilHealthy(t, instance, cfg)
	defer stopServer(t, cancel, done)

	baseURL := "http://" + instance.Addr()
	target := baseURL + httpapi.PathEnvironments + "?host_id=" + url.QueryEscape(hostID.String())
	status, body := getJSON(t, target)
	if status != http.StatusOK {
		t.Fatalf("环境列表应返回 200，实际 %d，响应: %s", status, body)
	}
	var list httpapi.EnvironmentListResponse
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("解析环境列表失败: %v，响应: %s", err, body)
	}
	if list.Total != 2 || len(list.Records) != 2 {
		t.Fatalf("应返回 2 个环境，实际 %+v", list)
	}
	seen := make(map[string]httpapi.EnvironmentResponse, len(list.Records))
	for _, record := range list.Records {
		seen[record.Type+"/"+record.Name] = record
	}
	goEnv, ok := seen["language/go"]
	if !ok {
		t.Fatalf("响应缺少 language/go: %+v", list.Records)
	}
	if goEnv.LatestVersion != "v0002" || goEnv.CurrentVersion != "v0001" {
		t.Fatalf("语言环境版本语义不符: %+v", goEnv)
	}

	versionTarget := baseURL + httpapi.PathEnvironments + "/language/go/versions/2?host_id=" +
		url.QueryEscape(hostID.String())
	status, body = getJSON(t, versionTarget)
	if status != http.StatusOK {
		t.Fatalf("版本查询应返回 200，实际 %d，响应: %s", status, body)
	}
	var version httpapi.EnvironmentVersionResponse
	if err := json.Unmarshal([]byte(body), &version); err != nil {
		t.Fatalf("解析版本响应失败: %v，响应: %s", err, body)
	}
	if version.Version != "v0002" || version.BuildStatus != domain.BuildStatusPending.String() {
		t.Fatalf("版本字段不符: %+v", version)
	}
	if version.Usable {
		t.Fatalf("未构建完成的版本不得标记为可用: %+v", version)
	}
}
