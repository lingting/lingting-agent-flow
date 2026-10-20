package server_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/server"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// recorder 记录后台服务的启动与停止顺序。
type recorder struct {
	mu    sync.Mutex
	order []string
}

// add 记录一次事件。
func (r *recorder) add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, event)
}

// events 返回事件序列。
func (r *recorder) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

// fakeService 是可控的后台服务替身。
type fakeService struct {
	name     string
	recorder *recorder
	startErr error
	faults   chan error
}

// Name 实现 server.Service。
func (f *fakeService) Name() string { return f.name }

// Start 实现 server.Service。
func (f *fakeService) Start(ctx context.Context) error {
	if f.recorder != nil {
		f.recorder.add("start:" + f.name)
	}
	return f.startErr
}

// Stop 实现 server.Service。
func (f *fakeService) Stop(ctx context.Context) error {
	if f.recorder != nil {
		f.recorder.add("stop:" + f.name)
	}
	return nil
}

// Faults 实现 lifecycle.FaultNotifier。
func (f *fakeService) Faults() <-chan error { return f.faults }

// testConfig 构造指向测试工作空间的配置。
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	workspace := testkit.New(t)
	return &config.Config{
		SourcePath: workspace.ConfigPath(),
		App:        config.AppConfig{Name: "agentflow", LogLevel: "info"},
		Server:     config.ServerConfig{Host: "127.0.0.1", Port: 0, ShutdownTimeoutSeconds: 2},
		Storage:    config.StorageConfig{DatabasePath: workspace.DatabasePath()},
		Workspace:  config.WorkspaceConfig{WorktreeRoot: workspace.Path("worktrees")},
		Scheduler:  config.SchedulerConfig{MaxConcurrentTasks: 1},
	}
}

// loader 返回固定配置的加载器。
func loader(cfg *config.Config) server.ConfigLoader {
	return func(ctx context.Context) (*config.Config, error) {
		return cfg, nil
	}
}

// realStoreOpener 打开真实 SQLite，用于验证正常关闭路径。
func realStoreOpener(t *testing.T) server.StoreOpener {
	t.Helper()
	return func(ctx context.Context, cfg *config.Config) (storage.Store, error) {
		return sqlite.Open(ctx, sqlite.Options{Path: cfg.Storage.DatabasePath})
	}
}

// newServer 构造被测服务，日志写入测试输出。
func newServer(t *testing.T, options server.Options) *server.Server {
	t.Helper()
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError}))
	}
	instance, err := server.New(options)
	if err != nil {
		t.Fatalf("构造服务失败: %v", err)
	}
	return instance
}

// testWriter 把日志写入测试输出。
type testWriter struct{ t *testing.T }

// Write 实现 io.Writer。
func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

// TestConfigErrorStartsNothing 校验配置加载失败时不会启动任何后台服务。
func TestConfigErrorStartsNothing(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	instance := newServer(t, server.Options{
		OpenStore: realStoreOpener(t),
		SystemInfo: func() app.SystemInfo {
			return app.SystemInfo{Version: "test"}
		},
		Services: []server.Service{&fakeService{name: "worker", recorder: rec}},
	})
	loadErr := errors.New("配置文件无效")
	err := instance.Run(context.Background(), func(ctx context.Context) (*config.Config, error) {
		return nil, loadErr
	})
	if err == nil {
		t.Fatal("配置加载失败时应返回错误")
	}
	if !errors.Is(err, loadErr) {
		t.Fatalf("错误应包含配置错误，实际 %v", err)
	}
	if len(rec.events()) != 0 {
		t.Fatalf("配置失败时不应启动后台服务，实际 %v", rec.events())
	}
}

// TestDatabaseFailureStopsNothing 校验数据库初始化失败时不会启动后台服务。
func TestDatabaseFailureStopsNothing(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	openErr := errors.New("数据库不可用")
	instance := newServer(t, server.Options{
		OpenStore: func(ctx context.Context, cfg *config.Config) (storage.Store, error) {
			return nil, openErr
		},
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{} },
		Services:   []server.Service{&fakeService{name: "worker", recorder: rec}},
	})
	err := instance.Run(context.Background(), loader(testConfig(t)))
	if err == nil {
		t.Fatal("数据库初始化失败时应返回错误")
	}
	if !errors.Is(err, openErr) {
		t.Fatalf("错误应包含数据库错误，实际 %v", err)
	}
	if !errors.Is(err, server.ErrStartup) {
		t.Fatalf("错误应标记为启动失败，实际 %v", err)
	}
	if len(rec.events()) != 0 {
		t.Fatalf("数据库失败时不应启动后台服务，实际 %v", rec.events())
	}
	if instance.OpenedStore() != nil {
		t.Fatal("数据库打开失败时不应保留存储实例")
	}
}

// TestServiceFailureStopsEarlierServices 校验后台服务启动失败时逆序清理。
func TestServiceFailureStopsEarlierServices(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	startErr := errors.New("服务启动失败")
	instance := newServer(t, server.Options{
		OpenStore:  realStoreOpener(t),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{} },
		Services: []server.Service{
			&fakeService{name: "first", recorder: rec},
			&fakeService{name: "broken", recorder: rec, startErr: startErr},
		},
	})
	err := instance.Run(context.Background(), loader(testConfig(t)))
	if err == nil {
		t.Fatal("后台服务启动失败时应返回错误")
	}
	if !errors.Is(err, startErr) {
		t.Fatalf("错误应包含服务启动错误，实际 %v", err)
	}
	want := []string{"start:first", "start:broken", "stop:first"}
	got := rec.events()
	if len(got) != len(want) {
		t.Fatalf("事件序列应为 %v，实际 %v", want, got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("事件序列应为 %v，实际 %v", want, got)
		}
	}
	if instance.OpenedStore() != nil {
		t.Fatal("启动失败后数据库应已关闭")
	}
}

// TestListenFailureStopsServices 校验 HTTP 监听失败时后台服务被停止。
func TestListenFailureStopsServices(t *testing.T) {
	t.Parallel()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用端口失败: %v", err)
	}
	t.Cleanup(func() { _ = occupied.Close() })

	rec := &recorder{}
	instance := newServer(t, server.Options{
		Address:    occupied.Addr().String(),
		OpenStore:  realStoreOpener(t),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{} },
		Services:   []server.Service{&fakeService{name: "worker", recorder: rec}},
	})
	cfg := testConfig(t)
	err = instance.Run(context.Background(), loader(cfg))
	if err == nil {
		t.Fatal("端口被占用时应返回错误")
	}
	want := []string{"start:worker", "stop:worker"}
	got := rec.events()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("事件序列应为 %v，实际 %v", want, got)
	}
	if instance.OpenedStore() != nil {
		t.Fatal("监听失败后数据库应已关闭")
	}
}

// TestGracefulShutdownStopsEverything 校验取消 ctx 后 HTTP 与后台服务、数据库都退出。
func TestGracefulShutdownStopsEverything(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	cfg := testConfig(t)
	instance := newServer(t, server.Options{
		Address:    "127.0.0.1:0",
		OpenStore:  realStoreOpener(t),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		Services:   []server.Service{&fakeService{name: "worker", recorder: rec}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- instance.Run(ctx, loader(cfg))
	}()
	waitHealthy(t, instance)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("正常关闭不应返回错误: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("等待服务关闭超时")
	}
	want := []string{"start:worker", "stop:worker"}
	got := rec.events()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("事件序列应为 %v，实际 %v", want, got)
	}
	if instance.OpenedStore() != nil {
		t.Fatal("关闭后数据库应已关闭")
	}
}

// TestStepExecutorStopsWithServer 校验步骤执行协调器被纳入统一生命周期。
//
// 关闭时必须先收到停止信号再关闭数据库，否则在途执行会在已关闭的数据库上写状态。
func TestStepExecutorStopsWithServer(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	instance := newServer(t, server.Options{
		Address:    "127.0.0.1:0",
		OpenStore:  realStoreOpener(t),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		StepExecutorFactory: func(
			store storage.Store,
			cfg *config.Config,
		) (*app.StepExecutor, error) {
			sessions, err := app.NewSessionService(
				store,
				app.SessionRoot(cfg),
				slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError})),
			)
			if err != nil {
				return nil, err
			}
			return app.NewStepExecutor(app.StepExecutorOptions{
				Store:      store,
				Containers: &unusableContainers{},
				Sessions:   sessions,
				Logger:     slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError})),
			})
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- instance.Run(ctx, loader(cfg))
	}()
	waitHealthy(t, instance)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("正常关闭不应返回错误: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("等待服务关闭超时")
	}
	if instance.OpenedStore() != nil {
		t.Fatal("关闭后数据库应已关闭")
	}
}

// unusableContainers 是只用于构造协调器的容器替身。
//
// 该用例不发起执行，因此两个方法都直接失败：任何意外调用都会让测试立刻暴露。
type unusableContainers struct{}

// InspectContainer 实现容器核验接口。
func (u *unusableContainers) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	return app.ContainerDetail{}, errors.New("测试未配置容器")
}

// RunAgent 实现容器内 Agent 调用接口。
func (u *unusableContainers) RunAgent(
	ctx context.Context,
	request app.ContainerAgentRequest,
) (app.ContainerAgentResult, error) {
	return app.ContainerAgentResult{}, errors.New("测试未配置容器")
}

// TestFaultTriggersShutdown 校验后台服务运行期故障会触发统一关闭并返回错误。
func TestFaultTriggersShutdown(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	faults := make(chan error, 1)
	cfg := testConfig(t)
	instance := newServer(t, server.Options{
		Address:    "127.0.0.1:0",
		OpenStore:  realStoreOpener(t),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{} },
		Services:   []server.Service{&fakeService{name: "worker", recorder: rec, faults: faults}},
	})
	done := make(chan error, 1)
	go func() {
		done <- instance.Run(context.Background(), loader(cfg))
	}()
	waitHealthy(t, instance)

	cause := errors.New("后台崩溃")
	faults <- cause
	select {
	case err := <-done:
		if !errors.Is(err, cause) {
			t.Fatalf("关闭错误应包含后台故障，实际 %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("等待故障关闭超时")
	}
	want := []string{"start:worker", "stop:worker"}
	got := rec.events()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("事件序列应为 %v，实际 %v", want, got)
	}
}

// TestRepeatedStopIsSafe 校验重复调用 Stop 不会 panic，也不会重复停止服务。
func TestRepeatedStopIsSafe(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	instance := newServer(t, server.Options{
		Address:    "127.0.0.1:0",
		OpenStore:  realStoreOpener(t),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{} },
		Services:   []server.Service{&fakeService{name: "worker", recorder: rec}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- instance.Run(ctx, loader(testConfig(t)))
	}()
	waitHealthy(t, instance)
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("等待服务关闭超时")
	}
	for i := 0; i < 3; i++ {
		if err := instance.Stop(context.Background()); err != nil {
			t.Fatalf("第 %d 次重复停止失败: %v", i+1, err)
		}
	}
	stops := 0
	for _, event := range rec.events() {
		if event == "stop:worker" {
			stops++
		}
	}
	if stops != 1 {
		t.Fatalf("后台服务应只停止一次，实际 %d 次", stops)
	}
}

// TestNewRequiresDependencies 校验缺少必需依赖时构造失败。
func TestNewRequiresDependencies(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	cases := []struct {
		name    string
		options server.Options
	}{
		{name: "缺少日志器", options: server.Options{OpenStore: realStoreOpener(t), SystemInfo: func() app.SystemInfo { return app.SystemInfo{} }}},
		{name: "缺少数据库打开器", options: server.Options{Logger: logger, SystemInfo: func() app.SystemInfo { return app.SystemInfo{} }}},
		{name: "缺少系统信息", options: server.Options{Logger: logger, OpenStore: realStoreOpener(t)}},
	}
	for _, item := range cases {
		if _, err := server.New(item.options); err == nil {
			t.Fatalf("%s 时构造应失败", item.name)
		}
	}
}

// waitHealthy 轮询健康检查接口，直到服务可用。
//
// 监听端口配置为 0 时实际端口在启动阶段才确定，因此每轮都重新读取实际地址。
func waitHealthy(t *testing.T, instance *server.Server) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	lastURL := ""
	for time.Now().Before(deadline) {
		address := instance.Addr()
		if strings.HasSuffix(address, ":0") {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		lastURL = "http://" + address + "/api/v1/health"
		response, err := http.Get(lastURL)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待服务健康检查超时: %s", lastURL)
}
