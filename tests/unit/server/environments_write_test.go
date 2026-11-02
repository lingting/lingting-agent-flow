package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/server"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// environmentWriteConfig 构造环境写路径装配验证使用的配置。
func environmentWriteConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := testConfig(t)
	cfg.Roles = defaultRoles()
	return cfg
}

// sshFactoryForTest 构造一个不连接任何主机的 SSH 服务。
//
// SSH 服务在构造阶段只读取 known_hosts 与私钥路径，不建立连接：因此它足以让 Server
// 装配真实的环境写路径，而测试不会访问任何外部主机。
func sshFactoryForTest(t *testing.T) func(
	store storage.Store,
	cfg *config.Config,
) (*app.SSHService, error) {
	t.Helper()
	return func(store storage.Store, cfg *config.Config) (*app.SSHService, error) {
		logger, err := testkit.Logger(logging.LevelError)
		if err != nil {
			return nil, err
		}
		return app.NewSSHService(store, logger, app.SSHOptions{
			KnownHostsPath: filepath.Join(testkit.Dir(t), "known_hosts"),
		})
	}
}

// TestServerWiresEnvironmentManagement 验证统一服务装配了环境写路径与后台构建管理器。
//
// 判定依据来自服务端真实行为，而不是内部字段：
//   - 启动时后台构建管理器把遗留的 building 记录收敛为 uncertain（证明它被注册进生命周期）；
//   - 构建入口返回 404 而不是 503（证明环境管理服务已装配，且受理前校验真的执行了）。
func TestServerWiresEnvironmentManagement(t *testing.T) {
	t.Parallel()
	cfg := environmentWriteConfig(t)
	hostID := seedEnvironmentVersions(t, cfg)
	// 把语言环境版本改写成"遗留构建中"现场：进程重启后没有执行者能证明构建仍在进行。
	markVersionBuilding(t, cfg, hostID)
	instance := newServer(t, server.Options{
		Address:      "127.0.0.1:0",
		OpenStore:    realStoreOpener(t),
		SSHFactory:   sshFactoryForTest(t),
		SystemInfo:   func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		RequireWebUI: false,
	})
	cancel, done := runUntilHealthy(t, instance, cfg)
	defer stopServer(t, cancel, done)

	baseURL := "http://" + instance.Addr()
	hostQuery := "?host_id=" + url.QueryEscape(hostID.String())

	// 遗留的 building 记录必须收敛为 uncertain：既不能报告成功，也不能伪造失败。
	status, body := getJSON(t, baseURL+httpapi.PathEnvironments+"/language/go/versions/2"+hostQuery)
	if status != http.StatusOK {
		t.Fatalf("版本查询应返回 200，实际 %d，响应: %s", status, body)
	}
	var converged httpapi.EnvironmentVersionResponse
	if err := json.Unmarshal([]byte(body), &converged); err != nil {
		t.Fatalf("解析版本响应失败: %v，响应: %s", err, body)
	}
	if converged.BuildStatus != domain.BuildStatusUncertain.String() {
		t.Fatalf("遗留构建应收敛为 uncertain，实际 %s", converged.BuildStatus)
	}
	if converged.Usable {
		t.Fatalf("uncertain 版本不得标记为可用: %+v", converged)
	}

	// 构建入口：版本不存在时必须是 404，而不是"服务未装配"的 503。
	target := baseURL + httpapi.PathEnvironments + "/language/go/build"
	payload := `{"host_id":"` + hostID.String() + `","version":99}`
	response, err := http.Post(target, "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("请求构建接口失败: %v", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在的版本应返回 404，实际 %d", response.StatusCode)
	}
}

// markVersionBuilding 把已存在的语言环境版本号 2 改写成构建中状态。
func markVersionBuilding(t *testing.T, cfg *config.Config, hostID domain.ID) {
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
	ref, refErr := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if refErr != nil {
		t.Fatalf("构造语言环境引用失败: %v", refErr)
	}
	version, err := store.GetEnvironmentVersion(ctx, hostID, ref, 2)
	if err != nil {
		t.Fatalf("读取环境版本失败: %v", err)
	}
	if err := version.MarkBuilding(); err != nil {
		t.Fatalf("标记构建中失败: %v", err)
	}
	if err := store.UpdateEnvironmentVersion(ctx, *version); err != nil {
		t.Fatalf("更新环境版本失败: %v", err)
	}
}
