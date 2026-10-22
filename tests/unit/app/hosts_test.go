package app_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// newService 在临时目录中打开数据库并构造执行节点服务。
func newService(t *testing.T) (*app.ExecutionHostService, storage.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	t.Parallel()
	service, _ := newService(t)
	created, err := service.Create(testContext(t), app.CreateExecutionHostRequest{
		Name:           "构建节点",
		Host:           "10.0.0.11",
		Username:       "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
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
	t.Parallel()
	service, _ := newService(t)
	created, err := service.Create(testContext(t), app.CreateExecutionHostRequest{
		ID:             "node-a",
		Name:           "构建节点",
		Host:           "10.0.0.11",
		Port:           2222,
		Username:       "ci",
		WorkspaceRoot:  "/srv/agentflow",
		PrivateKeyPath: testPrivateKeyPath(t),
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
	t.Parallel()
	service, _ := newService(t)
	ctx := testContext(t)
	first, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name:           "第一个",
		Host:           "10.0.0.1",
		Username:       "deploy",
		Default:        true,
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	second, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name:           "第二个",
		Host:           "10.0.0.2",
		Username:       "deploy",
		Default:        true,
		PrivateKeyPath: testPrivateKeyPath(t),
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
	t.Parallel()
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
	t.Parallel()
	service, _ := newService(t)
	ctx := testContext(t)
	created, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name:           "构建节点",
		Host:           "10.0.0.11",
		Username:       "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
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
	t.Parallel()
	service, _ := newService(t)
	_, err := service.SetDefault(testContext(t), domain.NewID().String())
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

// TestUpdateExecutionHostChangesFields 验证更新只修改显式给出的字段。
func TestUpdateExecutionHostChangesFields(t *testing.T) {
	t.Parallel()
	service, _ := newService(t)
	ctx := testContext(t)
	created, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name:           "构建节点",
		Host:           "10.0.0.11",
		Username:       "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
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
	t.Parallel()
	service, _ := newService(t)
	ctx := testContext(t)
	first, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name: "第一个", Host: "10.0.0.1", Username: "deploy", Default: true,
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	second, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name: "第二个", Host: "10.0.0.2", Username: "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
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
	t.Parallel()
	service, _ := newService(t)
	ctx := testContext(t)
	created, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name: "构建节点", Host: "10.0.0.11", Username: "deploy", Default: true,
		PrivateKeyPath: testPrivateKeyPath(t),
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

// TestRemoveExecutionHostRejectedWithActiveTask 验证节点上仍有未结束任务时拒绝删除。
func TestRemoveExecutionHostRejectedWithActiveTask(t *testing.T) {
	t.Parallel()
	service, store := newService(t)
	ctx := testContext(t)
	created, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name: "构建节点", Host: "10.0.0.12", Username: "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	requirement, err := domain.NewRequirement(
		"运行中需求",
		"节点仍有未结束任务",
		domain.PriorityNormal,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := store.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "/repo", "main", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	hostID := created.ID
	task.HostID = &hostID
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	if _, err := service.Remove(ctx, created.ID.String()); err == nil {
		t.Fatal("节点上存在未结束任务时必须拒绝删除")
	}
	if _, err := service.Get(ctx, created.ID.String()); err != nil {
		t.Fatalf("拒绝删除后节点应当仍然存在: %v", err)
	}

	// 任务进入终态后允许删除。
	if err := task.Transition(domain.TaskStatusPreparing, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusFailed, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := store.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	if _, err := service.Remove(ctx, created.ID.String()); err != nil {
		t.Fatalf("任务结束后应当允许删除节点: %v", err)
	}
}

// TestRemoveExecutionHostKeepsHistoryReferences 验证逻辑删除不级联删除历史记录。
func TestRemoveExecutionHostKeepsHistoryReferences(t *testing.T) {
	t.Parallel()
	service, store := newService(t)
	ctx := testContext(t)
	created, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name: "构建节点", Host: "10.0.0.13", Username: "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造环境引用失败: %v", err)
	}
	version := domain.EnvironmentVersion{
		HostID:            created.ID,
		Ref:               ref,
		Number:            1,
		DockerfileSHA256:  strings.Repeat("a", 64),
		BuildConfigSHA256: strings.Repeat("b", 64),
		ContextSHA256:     strings.Repeat("c", 64),
		ImageRef:          "agentflow-base:v0001-aaaaaaaaaaaa",
		Status:            domain.BuildStatusPending,
		CreatedAt:         time.Now().UTC(),
	}
	if err := store.CreateEnvironmentVersion(ctx, version); err != nil {
		t.Fatalf("写入环境版本失败: %v", err)
	}
	if _, err := service.Remove(ctx, created.ID.String()); err != nil {
		t.Fatalf("删除执行节点失败: %v", err)
	}
	versions, err := store.ListEnvironmentVersions(
		ctx,
		storage.EnvironmentVersionFilter{HostID: created.ID},
	)
	if err != nil {
		t.Fatalf("读取环境版本失败: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("逻辑删除不应级联删除环境版本，实际 %d 条", len(versions))
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
	t.Parallel()
	service, _ := newService(t)
	ctx := testContext(t)
	host, port := startSSHServer(t, "SSH-2.0-OpenSSH_9.6\r\n")
	created, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name: "本地节点", Host: host, Port: port, Username: "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
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
	t.Parallel()
	service, _ := newService(t)
	ctx := testContext(t)
	host, port := startSSHServer(t, "HTTP/1.1 400 Bad Request\r\n\r\n")
	created, err := service.Create(ctx, app.CreateExecutionHostRequest{
		Name: "本地节点", Host: host, Port: port, Username: "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
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
	t.Parallel()
	service, _ := newService(t)
	_, err := service.Probe(testContext(t), domain.NewID().String())
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

// newSSHService 在临时目录中打开数据库并构造 SSH 服务。
func newSSHService(t *testing.T) (*app.SSHService, storage.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	dir := testkit.Dir(t)
	databasePath := filepath.Join(dir, "agentflow.db")
	db, err := sqlite.Open(ctx, sqlite.Options{Path: databasePath})
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
	service, err := app.NewSSHService(db, logger, app.SSHOptions{
		KnownHostsPath: filepath.Join(dir, "known_hosts"),
	})
	if err != nil {
		t.Fatalf("构造 SSH 服务失败: %v", err)
	}
	return service, db
}

// mustHost 写入一个测试执行节点并返回其标识。
func mustHost(t *testing.T, store storage.Store, request app.CreateExecutionHostRequest) domain.ID {
	t.Helper()
	ctx := testContext(t)
	method := request.AuthMethod
	if method == "" {
		// 本版本只支持私钥认证。
		method = domain.AuthMethodPrivateKey
	}
	privateKeyPath := request.PrivateKeyPath
	if privateKeyPath == "" {
		privateKeyPath = writeTestPrivateKey(t)
	}
	host, err := domain.NewExecutionHost(
		domain.ID(request.ID),
		request.Name,
		request.Host,
		request.Port,
		request.Username,
		request.WorkspaceRoot,
		time.Now(),
		domain.WithAuth(method, privateKeyPath),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := store.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	return host.ID
}

// TestSSHServiceRejectsMissingStore 验证缺少存储时构造失败。
func TestSSHServiceRejectsMissingStore(t *testing.T) {
	t.Parallel()
	if _, err := app.NewSSHService(nil, nil, app.SSHOptions{}); err == nil {
		t.Fatal("缺少存储时应当返回错误")
	}
}

// TestSSHServiceKnownHostsPaths 验证只读 known_hosts 路径可被诊断输出。
func TestSSHServiceKnownHostsPaths(t *testing.T) {
	t.Parallel()
	service, _ := newSSHService(t)
	paths := service.KnownHostsPaths()
	if len(paths) == 0 {
		t.Fatal("应当给出只读 known_hosts 路径")
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			t.Fatalf("known_hosts 路径应为绝对路径: %s", path)
		}
	}
}

// TestTestConnectionUnreachable 验证连接测试报告 TCP 不可达并记录结果。
func TestTestConnectionUnreachable(t *testing.T) {
	t.Parallel()
	service, store := newSSHService(t)
	ctx := testContext(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动测试监听失败: %v", err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址类型不符: %T", listener.Addr())
	}
	port := address.Port
	if closeErr := listener.Close(); closeErr != nil {
		t.Fatalf("关闭测试监听失败: %v", closeErr)
	}
	id := mustHost(t, store, app.CreateExecutionHostRequest{
		ID:             "node-unreachable",
		Name:           "不可达节点",
		Host:           address.IP.String(),
		Port:           port,
		Username:       "deploy",
		AuthMethod:     domain.AuthMethodPrivateKey,
		PrivateKeyPath: writeTestPrivateKey(t),
	})
	result, testErr := service.TestConnection(ctx, id.String(), app.TestConnectionOptions{})
	if testErr == nil {
		t.Fatal("不可达地址应当返回错误")
	}
	if result.Authenticated {
		t.Fatal("不可达时不应标记为已认证")
	}
	if result.Code != sshclient.CodeUnreachable {
		t.Fatalf("结果类别应为 unreachable，实际: %s", result.Code)
	}
	reloaded, err := store.GetExecutionHost(ctx, id)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	if reloaded.LastConnectStatus != domain.ConnectStatusFailed {
		t.Fatalf("应当记录连接失败，实际: %s", reloaded.LastConnectStatus)
	}
	if reloaded.LastConnectError != sshclient.CodeUnreachable.String() {
		t.Fatalf("应当记录错误类别，实际: %s", reloaded.LastConnectError)
	}
}

// TestTestConnectionRejectsDisabledHost 验证禁用节点不能被测试。
func TestTestConnectionRejectsDisabledHost(t *testing.T) {
	t.Parallel()
	service, store := newSSHService(t)
	ctx := testContext(t)
	id := mustHost(t, store, app.CreateExecutionHostRequest{
		ID: "node-disabled", Name: "禁用节点", Host: "10.0.0.31", Username: "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	host, err := store.GetExecutionHost(ctx, id)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	host.Enabled = false
	if err := store.UpdateExecutionHost(ctx, *host); err != nil {
		t.Fatalf("更新执行节点失败: %v", err)
	}
	if _, testErr := service.TestConnection(ctx, id.String(), app.TestConnectionOptions{}); testErr == nil {
		t.Fatal("禁用节点应当返回错误")
	}
	if _, connectErr := service.Connect(ctx, id.String()); connectErr == nil {
		t.Fatal("禁用节点不应允许建立连接")
	}
}

// TestTestConnectionMissingHost 验证节点不存在时返回记录不存在。
func TestTestConnectionMissingHost(t *testing.T) {
	t.Parallel()
	service, _ := newSSHService(t)
	_, err := service.TestConnection(
		testContext(t),
		domain.NewID().String(),
		app.TestConnectionOptions{},
	)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

// TestEnsureWorkspaceMissingHost 验证工作空间检查要求节点存在。
func TestEnsureWorkspaceMissingHost(t *testing.T) {
	t.Parallel()
	service, _ := newSSHService(t)
	_, err := service.EnsureWorkspace(testContext(t), domain.NewID().String(), false)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

// TestTrustHostKeyRequiresFingerprint 验证未给出指纹时拒绝记录指纹。
func TestTrustHostKeyRequiresFingerprint(t *testing.T) {
	t.Parallel()
	service, store := newSSHService(t)
	id := mustHost(t, store, app.CreateExecutionHostRequest{
		ID: "node-trust", Name: "信任节点", Host: "10.0.0.32", Username: "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	if _, err := service.TrustHostKey(testContext(t), id.String(), "  "); err == nil {
		t.Fatal("缺少指纹时应当返回错误")
	}
}

// TestProbeHostKeyMissingHost 验证主机密钥探测要求节点存在。
func TestProbeHostKeyMissingHost(t *testing.T) {
	t.Parallel()
	service, _ := newSSHService(t)
	if _, err := service.ProbeHostKey(testContext(t), domain.NewID().String()); !errors.Is(
		err,
		storage.ErrNotFound,
	) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

// writeTestPrivateKey 生成一个测试用未加密私钥并返回其路径。
func writeTestPrivateKey(t *testing.T) string {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成测试密钥失败: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, "agentflow-test")
	if err != nil {
		t.Fatalf("编码测试私钥失败: %v", err)
	}
	path := filepath.Join(testkit.Dir(t), "id_test")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("写入测试私钥失败: %v", err)
	}
	return path
}
