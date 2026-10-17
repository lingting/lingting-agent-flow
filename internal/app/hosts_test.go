package app_test

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// newService 在临时目录中打开数据库并构造执行节点服务。
func newService(t *testing.T) (*app.ExecutionHostService, storage.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: filepath.Join(t.TempDir(), "agentflow.db")})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	logger, err := logging.New(nopWriter{}, logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	service, err := app.NewExecutionHostService(db, logger)
	if err != nil {
		t.Fatalf("构造执行节点服务失败: %v", err)
	}
	return service, db
}

// nopWriter 丢弃全部写入，避免测试输出噪声。
type nopWriter struct{}

// Write 丢弃内容。
func (nopWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

// testContext 返回带超时的测试上下文。
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestCreateExecutionHostWithDefaults 验证默认端口与默认工作空间。
func TestCreateExecutionHostWithDefaults(t *testing.T) {
	service, _ := newService(t)
	created, err := service.Create(testContext(t), app.CreateExecutionHostRequest{
		Name:     "构建节点",
		Host:     "10.0.0.11",
		Username: "deploy",
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	if created.Port != domain.DefaultSSHPort {
		t.Fatalf("默认端口不符: %d", created.Port)
	}
	if created.WorkspaceRoot != domain.DefaultWorkspaceRoot {
		t.Fatalf("默认工作空间不符: %s", created.WorkspaceRoot)
	}
	if created.IsDefault {
		t.Fatal("未指定 --default 时不应成为默认节点")
	}
	if !created.Enabled {
		t.Fatal("新建节点应当默认启用")
	}
}

// TestCreateExecutionHostWithExplicitWorkspace 验证工作空间可被单独覆盖。
func TestCreateExecutionHostWithExplicitWorkspace(t *testing.T) {
	service, _ := newService(t)
	created, err := service.Create(testContext(t), app.CreateExecutionHostRequest{
		ID:            "node-a",
		Name:          "构建节点",
		Host:          "10.0.0.11",
		Port:          2222,
		Username:      "ci",
		WorkspaceRoot: "/srv/agentflow",
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	if created.ID != domain.ID("node-a") {
		t.Fatalf("显式标识未生效: %s", created.ID)
	}
	if created.WorkspaceRoot != "/srv/agentflow" || created.Port != 2222 {
		t.Fatalf("显式配置未生效: %+v", created)
	}
}

// TestCreateExecutionHostAsDefault 验证新增时可直接设为默认节点。
func TestCreateExecutionHostAsDefault(t *testing.T) {
	service, _ := newService(t)
	ctx := testContext(t)
	first, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name:     "第一个",
		Host:     "10.0.0.1",
		Username: "deploy",
		Default:  true,
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	second, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name:     "第二个",
		Host:     "10.0.0.2",
		Username: "deploy",
		Default:  true,
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	hosts, err := service.List(ctx)
	if err != nil {
		t.Fatalf("查询执行节点失败: %v", err)
	}
	defaults := make([]domain.ID, 0, 2)
	for _, host := range hosts {
		if host.IsDefault {
			defaults = append(defaults, host.ID)
		}
	}
	if len(defaults) != 1 || defaults[0] != second.ID {
		t.Fatalf("默认节点应当唯一且为最后一个，实际: %v", defaults)
	}
	if first.ID == second.ID {
		t.Fatal("节点标识不应重复")
	}
}

// TestCreateExecutionHostRejectsInvalidInput 验证非法输入被拒绝。
func TestCreateExecutionHostRejectsInvalidInput(t *testing.T) {
	service, _ := newService(t)
	cases := map[string]app.CreateExecutionHostRequest{
		"缺少主机":   {Username: "deploy"},
		"缺少用户名":  {Host: "10.0.0.1"},
		"端口非法":   {Host: "10.0.0.1", Username: "deploy", Port: 70000},
		"工作空间非法": {Host: "10.0.0.1", Username: "deploy", WorkspaceRoot: "relative/path"},
	}
	for name, request := range cases {
		if _, err := service.Create(testContext(t), request); err == nil {
			t.Fatalf("%s 应当被拒绝", name)
		}
	}
}

// TestSetDefaultRejectsDisabledHost 验证已禁用节点不能成为默认节点。
func TestSetDefaultRejectsDisabledHost(t *testing.T) {
	service, _ := newService(t)
	ctx := testContext(t)
	created, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name:     "构建节点",
		Host:     "10.0.0.11",
		Username: "deploy",
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	disabled := false
	if _, err := service.Update(ctx, app.UpdateExecutionHostRequest{
		ID:      created.ID.String(),
		Enabled: &disabled,
	}); err != nil {
		t.Fatalf("更新执行节点失败: %v", err)
	}
	if _, err := service.SetDefault(ctx, created.ID.String()); err == nil {
		t.Fatal("已禁用节点不应成为默认节点")
	}
}

// TestSetDefaultRequiresExistingHost 验证节点不存在时返回记录不存在。
func TestSetDefaultRequiresExistingHost(t *testing.T) {
	service, _ := newService(t)
	_, err := service.SetDefault(testContext(t), domain.NewID().String())
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

// TestUpdateExecutionHostChangesFields 验证更新只修改显式给出的字段。
func TestUpdateExecutionHostChangesFields(t *testing.T) {
	service, _ := newService(t)
	ctx := testContext(t)
	created, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name:     "构建节点",
		Host:     "10.0.0.11",
		Username: "deploy",
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	name := "集成节点"
	workspace := "/srv/agentflow/ci"
	updated, err := service.Update(ctx, app.UpdateExecutionHostRequest{
		ID:            created.ID.String(),
		Name:          &name,
		WorkspaceRoot: &workspace,
	})
	if err != nil {
		t.Fatalf("更新执行节点失败: %v", err)
	}
	if updated.Name != name || updated.WorkspaceRoot != workspace {
		t.Fatalf("更新未生效: %+v", updated)
	}
	if updated.Host != created.Host || updated.Port != created.Port {
		t.Fatalf("未指定的字段不应被修改: %+v", updated)
	}
	if updated.Username != created.Username {
		t.Fatalf("未指定的字段不应被修改: %+v", updated)
	}
}

// TestUpdateExecutionHostSwitchesDefault 验证更新可切换默认节点。
func TestUpdateExecutionHostSwitchesDefault(t *testing.T) {
	service, _ := newService(t)
	ctx := testContext(t)
	first, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name: "第一个", Host: "10.0.0.1", Username: "deploy", Default: true,
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	second, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name: "第二个", Host: "10.0.0.2", Username: "deploy",
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	asDefault := true
	if _, err := service.Update(ctx, app.UpdateExecutionHostRequest{
		ID:      second.ID.String(),
		Default: &asDefault,
	}); err != nil {
		t.Fatalf("更新执行节点失败: %v", err)
	}
	reloaded, err := service.Get(ctx, first.ID.String())
	if err != nil {
		t.Fatalf("查询执行节点失败: %v", err)
	}
	if reloaded.IsDefault {
		t.Fatal("原默认节点应当被取消默认标记")
	}
	current, err := service.Get(ctx, second.ID.String())
	if err != nil {
		t.Fatalf("查询执行节点失败: %v", err)
	}
	if !current.IsDefault {
		t.Fatal("新节点应当成为默认节点")
	}
}

// TestRemoveExecutionHost 验证删除节点配置后不可再查询。
func TestRemoveExecutionHost(t *testing.T) {
	service, _ := newService(t)
	ctx := testContext(t)
	created, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name: "构建节点", Host: "10.0.0.11", Username: "deploy", Default: true,
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	removed, err := service.Remove(ctx, created.ID.String())
	if err != nil {
		t.Fatalf("删除执行节点失败: %v", err)
	}
	if removed.ID != created.ID {
		t.Fatalf("删除结果不符: %+v", removed)
	}
	if _, err := service.Get(ctx, created.ID.String()); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("删除后应当不存在，实际: %v", err)
	}
	if _, err := service.Remove(ctx, created.ID.String()); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("重复删除应当返回记录不存在，实际: %v", err)
	}
}

// startSSHServer 启动一个返回固定协议标识的本地 TCP 服务。
func startSSHServer(t *testing.T, banner string) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动测试服务失败: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() {
					_ = conn.Close()
				}()
				if _, err := conn.Write([]byte(banner)); err != nil {
					return
				}
				<-time.After(200 * time.Millisecond)
			}(conn)
		}
	}()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址类型不符: %T", listener.Addr())
	}
	return address.IP.String(), address.Port
}

// TestProbeExecutionHostDetectsSSH 验证探测识别到 SSH 服务但不标记为已认证。
func TestProbeExecutionHostDetectsSSH(t *testing.T) {
	service, _ := newService(t)
	ctx := testContext(t)
	host, port := startSSHServer(t, "SSH-2.0-OpenSSH_9.6\r\n")
	created, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name: "本地节点", Host: host, Port: port, Username: "deploy",
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	result, err := service.Probe(ctx, created.ID.String())
	if err != nil {
		t.Fatalf("探测应当成功返回结果: %v", err)
	}
	if !result.Probe.SSHDetected || !result.Probe.Reachable {
		t.Fatalf("应当识别到 SSH 服务: %+v", result.Probe)
	}
	if result.Probe.Authenticated {
		t.Fatal("探测不得标记为已完成身份认证")
	}
	if !result.Probe.Succeeded() {
		t.Fatalf("探测结果应当判定为成功: %+v", result.Probe)
	}
	if result.Host.ID != created.ID {
		t.Fatalf("探测结果应关联节点: %+v", result.Host)
	}
}

// TestProbeExecutionHostNotSSH 验证非 SSH 服务被准确区分。
func TestProbeExecutionHostNotSSH(t *testing.T) {
	service, _ := newService(t)
	ctx := testContext(t)
	host, port := startSSHServer(t, "HTTP/1.1 400 Bad Request\r\n\r\n")
	created, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name: "本地节点", Host: host, Port: port, Username: "deploy",
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	result, err := service.Probe(ctx, created.ID.String())
	if err != nil {
		t.Fatalf("探测应当返回结果而非业务错误: %v", err)
	}
	if result.Probe.SSHDetected {
		t.Fatalf("非 SSH 服务不应识别为 SSH: %+v", result.Probe)
	}
	if result.Probe.ErrorCode == "" || result.Probe.Message == "" {
		t.Fatalf("失败结果应给出错误类别与说明: %+v", result.Probe)
	}
}

// TestProbeExecutionHostMissing 验证探测不存在的节点返回记录不存在。
func TestProbeExecutionHostMissing(t *testing.T) {
	service, _ := newService(t)
	_, err := service.Probe(testContext(t), domain.NewID().String())
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}
