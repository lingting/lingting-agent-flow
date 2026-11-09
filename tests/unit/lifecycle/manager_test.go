package lifecycle_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/lifecycle"
)

// 本文件验证后台服务的统一生命周期：启动顺序、逆序停止、故障传播与超时边界。
//
// 这些行为是进程停止时"取消执行上下文后等待现场收敛"的前提：停止顺序错误会把仍在
// 使用数据库的服务留在后面，超时缺失会让进程退出被单个服务无限拖住。

// eventLog 是启动与停止顺序的记录器。
type eventLog struct {
	mu     sync.Mutex
	events []string
}

// add 记录一条事件。
func (l *eventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

// snapshot 返回事件快照。
func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// testService 是一个受控的后台服务替身。
type testService struct {
	name string
	log  *eventLog
	// startErr 使启动失败。
	startErr error
	// stopErr 使停止失败。
	stopErr error
	// stopBlocks 为真时停止阻塞到 ctx 结束，用于验证超时保护。
	stopBlocks bool
	// faults 用于上报运行期故障。
	faults chan error
}

// Name 返回服务名称。
func (s *testService) Name() string { return s.name }

// Start 记录启动并返回预设结果。
func (s *testService) Start(ctx context.Context) error {
	s.log.add("start:" + s.name)
	return s.startErr
}

// Stop 记录停止并返回预设结果。
func (s *testService) Stop(ctx context.Context) error {
	s.log.add("stop:" + s.name)
	if s.stopBlocks {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.stopErr
}

// Faults 返回运行期故障通道。
func (s *testService) Faults() <-chan error { return s.faults }

// newServices 构造 n 个按名称顺序排列的服务。
func newServices(log *eventLog, names ...string) []*testService {
	services := make([]*testService, 0, len(names))
	for _, name := range names {
		services = append(services, &testService{name: name, log: log})
	}
	return services
}

// asServices 把测试替身转换为管理器输入。
func asServices(services []*testService) []lifecycle.Service {
	converted := make([]lifecycle.Service, 0, len(services))
	for _, service := range services {
		converted = append(converted, service)
	}
	return converted
}

// TestStopRunsInReverseStartOrder 校验停止顺序与启动顺序相反。
func TestStopRunsInReverseStartOrder(t *testing.T) {
	t.Parallel()
	log := &eventLog{}
	services := newServices(log, "first", "second", "third")
	manager := lifecycle.New(lifecycle.Options{StopTimeout: time.Second})
	if err := manager.Register(asServices(services)...); err != nil {
		t.Fatalf("注册后台服务失败: %v", err)
	}
	if err := manager.Start(t.Context()); err != nil {
		t.Fatalf("启动后台服务失败: %v", err)
	}
	if err := manager.Stop(t.Context()); err != nil {
		t.Fatalf("停止后台服务失败: %v", err)
	}

	want := []string{"start:first", "start:second", "start:third", "stop:third", "stop:second", "stop:first"}
	got := log.snapshot()
	if len(got) != len(want) {
		t.Fatalf("事件数量不符: %v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("第 %d 个事件应为 %s，实际 %s（全部: %v）", index, want[index], got[index], got)
		}
	}
}

// TestStartFailureStopsOnlyStartedServicesInReverse 校验启动失败只回滚已启动的服务。
func TestStartFailureStopsOnlyStartedServicesInReverse(t *testing.T) {
	t.Parallel()
	log := &eventLog{}
	services := newServices(log, "first", "broken", "never-started")
	services[1].startErr = errors.New("启动失败")
	manager := lifecycle.New(lifecycle.Options{StopTimeout: time.Second})
	if err := manager.Register(asServices(services)...); err != nil {
		t.Fatalf("注册后台服务失败: %v", err)
	}
	err := manager.Start(t.Context())
	if err == nil {
		t.Fatal("服务启动失败时 Manager.Start 必须返回错误")
	}
	if !errors.Is(err, services[1].startErr) {
		t.Fatalf("错误链应保留启动失败原因，实际: %v", err)
	}
	want := []string{"start:first", "start:broken", "stop:first"}
	got := log.snapshot()
	if len(got) != len(want) {
		t.Fatalf("事件序列不符: %v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("第 %d 个事件应为 %s，实际 %s（全部: %v）", index, want[index], got[index], got)
		}
	}
}

// TestStopIsIdempotentAndReportsFailures 校验重复停止只执行一次且失败可诊断。
func TestStopIsIdempotentAndReportsFailures(t *testing.T) {
	t.Parallel()
	log := &eventLog{}
	services := newServices(log, "failing")
	services[0].stopErr = errors.New("停止失败")
	manager := lifecycle.New(lifecycle.Options{StopTimeout: time.Second})
	if err := manager.Register(asServices(services)...); err != nil {
		t.Fatalf("注册后台服务失败: %v", err)
	}
	if err := manager.Start(t.Context()); err != nil {
		t.Fatalf("启动后台服务失败: %v", err)
	}
	first := manager.Stop(t.Context())
	if !errors.Is(first, services[0].stopErr) {
		t.Fatalf("停止失败必须原样返回，实际: %v", first)
	}
	if second := manager.Stop(t.Context()); second != nil {
		t.Fatalf("重复停止必须返回 nil，实际: %v", second)
	}
	stops := 0
	for _, event := range log.snapshot() {
		if event == "stop:failing" {
			stops++
		}
	}
	if stops != 1 {
		t.Fatalf("停止不得重复调用，实际 %d 次", stops)
	}
}

// TestStopTimeoutIsBounded 校验不响应的服务不会让停止无限等待。
func TestStopTimeoutIsBounded(t *testing.T) {
	t.Parallel()
	log := &eventLog{}
	services := newServices(log, "stuck")
	services[0].stopBlocks = true
	manager := lifecycle.New(lifecycle.Options{StopTimeout: 50 * time.Millisecond})
	if err := manager.Register(asServices(services)...); err != nil {
		t.Fatalf("注册后台服务失败: %v", err)
	}
	if err := manager.Start(t.Context()); err != nil {
		t.Fatalf("启动后台服务失败: %v", err)
	}
	started := time.Now()
	err := manager.Stop(t.Context())
	if err == nil {
		t.Fatal("停止超时必须以错误上报")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("停止必须在超时预算内返回，实际耗时 %s", elapsed)
	}
}

// TestRuntimeFaultIsForwarded 校验运行期致命错误带有服务归属。
func TestRuntimeFaultIsForwarded(t *testing.T) {
	t.Parallel()
	log := &eventLog{}
	services := newServices(log, "unstable")
	services[0].faults = make(chan error, 1)
	manager := lifecycle.New(lifecycle.Options{StopTimeout: time.Second})
	if err := manager.Register(asServices(services)...); err != nil {
		t.Fatalf("注册后台服务失败: %v", err)
	}
	if err := manager.Start(t.Context()); err != nil {
		t.Fatalf("启动后台服务失败: %v", err)
	}
	cause := errors.New("后台循环异常")
	services[0].faults <- cause

	select {
	case fault := <-manager.Faults():
		if fault.Name != "unstable" {
			t.Fatalf("故障归属应为服务名称，实际 %s", fault.Name)
		}
		if !errors.Is(fault, cause) {
			t.Fatalf("故障必须保留原始错误，实际: %v", fault)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("运行期故障必须通过 Faults 上报")
	}
	if err := manager.Stop(t.Context()); err != nil {
		t.Fatalf("停止后台服务失败: %v", err)
	}
}

// TestRegisterRejectsInvalidServices 校验重复名称与启动后注册被拒绝。
func TestRegisterRejectsInvalidServices(t *testing.T) {
	t.Parallel()
	log := &eventLog{}
	services := newServices(log, "same", "same")
	manager := lifecycle.New(lifecycle.Options{StopTimeout: time.Second})
	if err := manager.Register(asServices(services)...); !errors.Is(err, lifecycle.ErrDuplicateName) {
		t.Fatalf("重复名称必须被拒绝，实际: %v", err)
	}

	valid := newServices(log, "first")
	other := lifecycle.New(lifecycle.Options{StopTimeout: time.Second})
	if err := other.Register(asServices(valid)...); err != nil {
		t.Fatalf("注册后台服务失败: %v", err)
	}
	if err := other.Start(t.Context()); err != nil {
		t.Fatalf("启动后台服务失败: %v", err)
	}
	if err := other.Register(asServices(newServices(log, "late"))...); !errors.Is(err, lifecycle.ErrStarted) {
		t.Fatalf("启动后注册必须被拒绝，实际: %v", err)
	}
	if err := other.Stop(t.Context()); err != nil {
		t.Fatalf("停止后台服务失败: %v", err)
	}
}
