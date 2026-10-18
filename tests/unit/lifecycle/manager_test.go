package lifecycle_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/lifecycle"
)

// recorder 记录服务的启动与停止顺序。
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

// events 返回当前记录的事件序列。
func (r *recorder) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

// fakeService 是可控的后台服务替身。
type fakeService struct {
	name       string
	recorder   *recorder
	startErr   error
	stopErr    error
	stopDelay  time.Duration
	faults     chan error
	startCount int
	stopCount  int
	mu         sync.Mutex
}

// Name 实现 lifecycle.Service。
func (f *fakeService) Name() string {
	return f.name
}

// Start 实现 lifecycle.Service。
func (f *fakeService) Start(ctx context.Context) error {
	f.mu.Lock()
	f.startCount++
	f.mu.Unlock()
	if f.recorder != nil {
		f.recorder.add("start:" + f.name)
	}
	return f.startErr
}

// Stop 实现 lifecycle.Service。
func (f *fakeService) Stop(ctx context.Context) error {
	f.mu.Lock()
	f.stopCount++
	f.mu.Unlock()
	if f.stopDelay > 0 {
		select {
		case <-time.After(f.stopDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.recorder != nil {
		f.recorder.add("stop:" + f.name)
	}
	return f.stopErr
}

// Faults 实现 lifecycle.FaultNotifier。
func (f *fakeService) Faults() <-chan error {
	return f.faults
}

// counts 返回启动与停止次数。
func (f *fakeService) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startCount, f.stopCount
}

// equalOrder 比较两个事件序列。
func equalOrder(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// TestStartFollowsRegistrationOrder 校验启动顺序与注册顺序一致。
func TestStartFollowsRegistrationOrder(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	manager := lifecycle.New(lifecycle.Options{})
	first := &fakeService{name: "first", recorder: rec}
	second := &fakeService{name: "second", recorder: rec}
	if err := manager.Register(first, second); err != nil {
		t.Fatalf("注册后台服务失败: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("启动后台服务失败: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })

	if got := rec.events(); !equalOrder(got, []string{"start:first", "start:second"}) {
		t.Fatalf("启动顺序应为注册顺序，实际 %v", got)
	}
}

// TestStopRunsInReverseOrder 校验停止顺序与启动顺序相反。
func TestStopRunsInReverseOrder(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	manager := lifecycle.New(lifecycle.Options{})
	if err := manager.Register(
		&fakeService{name: "first", recorder: rec},
		&fakeService{name: "second", recorder: rec},
		&fakeService{name: "third", recorder: rec},
	); err != nil {
		t.Fatalf("注册后台服务失败: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("启动后台服务失败: %v", err)
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("停止后台服务失败: %v", err)
	}

	want := []string{
		"start:first", "start:second", "start:third",
		"stop:third", "stop:second", "stop:first",
	}
	if got := rec.events(); !equalOrder(got, want) {
		t.Fatalf("事件顺序应为 %v，实际 %v", want, got)
	}
}

// TestStartFailureStopsStartedServices 校验启动失败时按逆序清理已启动服务。
func TestStartFailureStopsStartedServices(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	manager := lifecycle.New(lifecycle.Options{})
	failing := &fakeService{name: "broken", recorder: rec, startErr: errors.New("启动失败")}
	if err := manager.Register(
		&fakeService{name: "first", recorder: rec},
		failing,
		&fakeService{name: "never", recorder: rec},
	); err != nil {
		t.Fatalf("注册后台服务失败: %v", err)
	}
	err := manager.Start(context.Background())
	if err == nil {
		t.Fatal("后台服务启动失败时应返回错误")
	}
	if !errors.Is(err, failing.startErr) {
		t.Fatalf("错误应包含原始启动错误，实际 %v", err)
	}

	want := []string{"start:first", "start:broken", "stop:first"}
	if got := rec.events(); !equalOrder(got, want) {
		t.Fatalf("事件顺序应为 %v，实际 %v", want, got)
	}
}

// TestStopIsIdempotent 校验重复停止不会重复调用服务，也不会 panic。
func TestStopIsIdempotent(t *testing.T) {
	t.Parallel()
	service := &fakeService{name: "once"}
	manager := lifecycle.New(lifecycle.Options{})
	if err := manager.Register(service); err != nil {
		t.Fatalf("注册后台服务失败: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("启动后台服务失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := manager.Stop(context.Background()); err != nil {
			t.Fatalf("第 %d 次停止失败: %v", i+1, err)
		}
	}
	starts, stops := service.counts()
	if starts != 1 || stops != 1 {
		t.Fatalf("服务应只启动与停止一次，实际启动 %d 次、停止 %d 次", starts, stops)
	}
}

// TestStopTimeoutIsEnforced 校验停止超时通过 context 传递，超时错误被返回。
func TestStopTimeoutIsEnforced(t *testing.T) {
	t.Parallel()
	service := &fakeService{name: "slow", stopDelay: 5 * time.Second}
	manager := lifecycle.New(lifecycle.Options{StopTimeout: 50 * time.Millisecond})
	if err := manager.Register(service); err != nil {
		t.Fatalf("注册后台服务失败: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("启动后台服务失败: %v", err)
	}
	started := time.Now()
	err := manager.Stop(context.Background())
	if err == nil {
		t.Fatal("停止超时应返回错误")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误应包含超时原因，实际 %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("停止超时未生效，耗时 %s", elapsed)
	}
}

// TestRegisterRejectsDuplicatesAndLateRegistration 校验重名与启动后注册被拒绝。
func TestRegisterRejectsDuplicatesAndLateRegistration(t *testing.T) {
	t.Parallel()
	manager := lifecycle.New(lifecycle.Options{})
	if err := manager.Register(&fakeService{name: "same"}); err != nil {
		t.Fatalf("首次注册失败: %v", err)
	}
	if err := manager.Register(&fakeService{name: "same"}); !errors.Is(err, lifecycle.ErrDuplicateName) {
		t.Fatalf("重名注册应返回 ErrDuplicateName，实际 %v", err)
	}
	if err := manager.Register(nil); !errors.Is(err, lifecycle.ErrInvalidService) {
		t.Fatalf("nil 服务应返回 ErrInvalidService，实际 %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("启动后台服务失败: %v", err)
	}
	if err := manager.Register(&fakeService{name: "late"}); !errors.Is(err, lifecycle.ErrStarted) {
		t.Fatalf("启动后注册应返回 ErrStarted，实际 %v", err)
	}
	if err := manager.Start(context.Background()); !errors.Is(err, lifecycle.ErrAlreadyStarted) {
		t.Fatalf("重复启动应返回 ErrAlreadyStarted，实际 %v", err)
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("停止后台服务失败: %v", err)
	}
}

// TestFaultIsPropagated 校验运行期故障会被转发到管理器。
func TestFaultIsPropagated(t *testing.T) {
	t.Parallel()
	faults := make(chan error, 1)
	service := &fakeService{name: "unstable", faults: faults}
	manager := lifecycle.New(lifecycle.Options{})
	if err := manager.Register(service); err != nil {
		t.Fatalf("注册后台服务失败: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("启动后台服务失败: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })

	cause := errors.New("后台故障")
	faults <- cause
	select {
	case fault := <-manager.Faults():
		if fault.Name != "unstable" {
			t.Fatalf("故障来源应为 unstable，实际 %q", fault.Name)
		}
		if !errors.Is(fault, cause) {
			t.Fatalf("故障应保留原始错误，实际 %v", fault.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待故障转发超时")
	}
}

// TestStopAfterStartFailureDoesNotPanic 校验启动失败后再次停止是安全的。
func TestStopAfterStartFailureDoesNotPanic(t *testing.T) {
	t.Parallel()
	manager := lifecycle.New(lifecycle.Options{})
	if err := manager.Register(&fakeService{name: "broken", startErr: errors.New("启动失败")}); err != nil {
		t.Fatalf("注册后台服务失败: %v", err)
	}
	if err := manager.Start(context.Background()); err == nil {
		t.Fatal("启动失败时应返回错误")
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("启动失败后的停止不应返回错误: %v", err)
	}
}
