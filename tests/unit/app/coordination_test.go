// Package app_test 提供进程内协调机制的单元测试。
//
// 本文件覆盖两套 keyed mutex 集合的关键正确性要求：同键串行、异键并行、锁表自动回收，
// 以及引用计数必须同时覆盖持锁者与等待者。用例只使用同步屏障与有序断言，不连接任何
// 外部依赖。
package app_test

import (
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// 并发断言使用的观察窗口与等待上限。
const (
	// notEnteredProbeTimeout 是"确认某个调用没有提前进入临界区"的观察窗口。
	//
	// 它只是补充断言：真正的正确性由进入顺序与锁表条目数决定，不以固定等待作为唯一
	// 证据。
	notEnteredProbeTimeout = 200 * time.Millisecond
	// enterWaitTimeout 是等待某个调用取得锁的上限，超时即说明互斥未能按预期释放。
	enterWaitTimeout = 5 * time.Second
)

// keyedLockSubject 描述一组待测的"按字符串键互斥"集合。
type keyedLockSubject struct {
	// name 用于失败信息。
	name string
	// keys 是可用键的数量。
	keys int
	// tableLen 返回当前锁表条目数。
	tableLen func(t *testing.T) int
	// lock 取得第 index 个键的锁并返回解锁函数，同一 index 对应同一个键。
	lock func(index int) func()
}

// TestPlanningSessionCoordinatorReclaimsIdleLocks 验证规划会话锁表能回收到空状态。
func TestPlanningSessionCoordinatorReclaimsIdleLocks(t *testing.T) {
	t.Parallel()
	runLockReclaim(t, newPlanningSubject(t, 48))
}

// TestEnvironmentBuildLocksReclaimsIdleLocks 验证环境构建锁表能回收到空状态。
func TestEnvironmentBuildLocksReclaimsIdleLocks(t *testing.T) {
	t.Parallel()
	runLockReclaim(t, newEnvironmentSubject(t, 48))
}

// TestPlanningSessionCoordinatorSerializesSameSession 验证同一规划会话的调用严格串行。
func TestPlanningSessionCoordinatorSerializesSameSession(t *testing.T) {
	t.Parallel()
	runSameKeySerialization(t, newPlanningSubject(t, 2))
}

// TestEnvironmentBuildLocksSerializesSameEnvironment 验证同一节点同一环境的构建严格串行。
func TestEnvironmentBuildLocksSerializesSameEnvironment(t *testing.T) {
	t.Parallel()
	runSameKeySerialization(t, newEnvironmentSubject(t, 2))
}

// TestPlanningSessionCoordinatorAllowsParallelSessions 验证不同规划会话可以并行。
func TestPlanningSessionCoordinatorAllowsParallelSessions(t *testing.T) {
	t.Parallel()
	runDifferentKeyParallelism(t, newPlanningSubject(t, 2))
}

// TestEnvironmentBuildLocksAllowsParallelEnvironments 验证不同节点或环境可以并行构建。
func TestEnvironmentBuildLocksAllowsParallelEnvironments(t *testing.T) {
	t.Parallel()
	runDifferentKeyParallelism(t, newEnvironmentSubject(t, 2))
}

// runLockReclaim 验证大量键申请释放之后锁表不残留条目。
func runLockReclaim(t *testing.T, subject keyedLockSubject) {
	t.Helper()
	if got := subject.tableLen(t); got != 0 {
		t.Fatalf("%s: 初始锁表必须为空，实际 %d 条", subject.name, got)
	}

	// 顺序申请并释放大量不同键。
	for index := 0; index < subject.keys; index++ {
		unlock := subject.lock(index)
		unlock()
	}
	if got := subject.tableLen(t); got != 0 {
		t.Fatalf("%s: 顺序申请释放后锁表必须回收为空，实际 %d 条", subject.name, got)
	}

	// 同一个键反复申请释放同样不得残留条目。
	for index := 0; index < subject.keys; index++ {
		subject.lock(0)()
	}
	if got := subject.tableLen(t); got != 0 {
		t.Fatalf("%s: 重复申请同一个键后锁表必须回收为空，实际 %d 条", subject.name, got)
	}

	// 并发申请大量不同键，全部退出后同样回收到空。
	var wait sync.WaitGroup
	for index := 0; index < subject.keys; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			unlock := subject.lock(index)
			unlock()
		}(index)
	}
	wait.Wait()
	if got := subject.tableLen(t); got != 0 {
		t.Fatalf("%s: 并发申请释放后锁表必须回收为空，实际 %d 条", subject.name, got)
	}
}

// runSameKeySerialization 验证同键调用串行，且引用计数覆盖持锁者与等待者。
func runSameKeySerialization(t *testing.T, subject keyedLockSubject) {
	t.Helper()
	tracker := newHolderTracker()

	firstUnlock := subject.lock(0)
	tracker.enter("first")

	waiterEntered := make(chan struct{})
	waiterRelease := make(chan struct{})
	waiterDone := make(chan struct{})
	go func() {
		defer close(waiterDone)
		unlock := subject.lock(0)
		defer unlock()
		tracker.enter("waiter")
		close(waiterEntered)
		<-waiterRelease
		tracker.leave()
	}()

	assertNotEntered(t, waiterEntered, subject.name+"：第一个调用释放前等待者不得进入同一键的临界区")
	tracker.leave()
	firstUnlock()
	awaitEntered(t, waiterEntered, subject.name+"：第一个调用释放后等待者必须取得同一键的锁")

	// 等待者持锁时条目必须仍然存在：若等待者不计入引用计数，第一个调用释放就会删除条目，
	// 等待者与后来的调用者会各自持有一把不同的锁。
	if got := subject.tableLen(t); got != 1 {
		t.Fatalf("%s: 等待者持锁期间锁表必须保留该键条目，实际 %d 条", subject.name, got)
	}

	// 等待者持锁期间，新调用仍然不能并发进入同一键。
	thirdEntered := make(chan struct{})
	thirdRelease := make(chan struct{})
	thirdDone := make(chan struct{})
	go func() {
		defer close(thirdDone)
		unlock := subject.lock(0)
		defer unlock()
		tracker.enter("third")
		close(thirdEntered)
		<-thirdRelease
		tracker.leave()
	}()
	assertNotEntered(t, thirdEntered, subject.name+"：等待者持锁期间新调用不得进入同一键的临界区")

	close(waiterRelease)
	<-waiterDone
	awaitEntered(t, thirdEntered, subject.name+"：等待者释放后新调用必须取得同一键的锁")
	close(thirdRelease)
	<-thirdDone

	// 最后一个持有者退出后条目必须回收。
	if got := subject.tableLen(t); got != 0 {
		t.Fatalf("%s: 全部调用退出后锁表必须回收为空，实际 %d 条", subject.name, got)
	}
	tracker.assertMaxActive(t, 1)
	tracker.assertOrder(t, []string{"first", "waiter", "third"})
}

// runDifferentKeyParallelism 验证不同键可以同时持锁，且全部退出后锁表回收到空。
func runDifferentKeyParallelism(t *testing.T, subject keyedLockSubject) {
	t.Helper()
	tracker := newHolderTracker()

	firstHeld := make(chan struct{})
	firstRelease := make(chan struct{})
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		unlock := subject.lock(0)
		defer unlock()
		tracker.enter("first")
		close(firstHeld)
		<-firstRelease
		tracker.leave()
	}()
	awaitEntered(t, firstHeld, subject.name+"：第一个键必须能够取得锁")

	secondHeld := make(chan struct{})
	secondRelease := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		unlock := subject.lock(1)
		defer unlock()
		tracker.enter("second")
		close(secondHeld)
		<-secondRelease
		tracker.leave()
	}()
	awaitEntered(t, secondHeld, subject.name+"：不同键必须可以并行取得各自的锁")
	if got := subject.tableLen(t); got != 2 {
		t.Fatalf("%s: 两个键同时持锁时锁表必须有 2 条条目，实际 %d 条", subject.name, got)
	}

	close(firstRelease)
	close(secondRelease)
	<-firstDone
	<-secondDone
	if got := subject.tableLen(t); got != 0 {
		t.Fatalf("%s: 并行调用退出后锁表必须回收为空，实际 %d 条", subject.name, got)
	}
	tracker.assertMaxActive(t, 2)
}

// newPlanningSubject 构造以规划会话标识为键的被测集合。
func newPlanningSubject(t *testing.T, keyCount int) keyedLockSubject {
	t.Helper()
	coordinator := app.NewPlanningSessionCoordinator()
	sessionIDs := make([]domain.ID, keyCount)
	for index := range sessionIDs {
		sessionIDs[index] = domain.NewID()
	}
	return keyedLockSubject{
		name:     "PlanningSessionCoordinator",
		keys:     keyCount,
		tableLen: func(t *testing.T) int { return lockTableLen(t, coordinator) },
		lock:     func(index int) func() { return coordinator.Lock(sessionIDs[index]) },
	}
}

// newEnvironmentSubject 构造以"节点 + 环境"为键的被测集合。
func newEnvironmentSubject(t *testing.T, keyCount int) keyedLockSubject {
	t.Helper()
	buildLocks := app.NewEnvironmentBuildLocks()
	hostID := domain.NewID()
	refs := make([]domain.EnvironmentRef, keyCount)
	for index := range refs {
		ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "env-"+strconv.Itoa(index))
		if err != nil {
			t.Fatalf("构造环境引用失败: %v", err)
		}
		refs[index] = ref
	}
	return keyedLockSubject{
		name:     "EnvironmentBuildLocks",
		keys:     keyCount,
		tableLen: func(t *testing.T) int { return lockTableLen(t, buildLocks) },
		lock:     func(index int) func() { return buildLocks.Lock(hostID, refs[index]) },
	}
}

// lockTableLen 读取被测集合内部的锁表条目数。
//
// 锁表回收不需要额外的生产接口：这里只读取已存在的未导出字段长度，不修改任何状态。
// 读取必须发生在所有持有者都已退出、或其余持有者都已停在同步屏障上的时刻，否则会与
// 锁表写入并发。
func lockTableLen(t *testing.T, target any) int {
	t.Helper()
	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		t.Fatalf("读取锁表失败: 目标必须是非空指针，实际 %T", target)
	}
	holder := value.Elem().FieldByName("locks")
	if !holder.IsValid() {
		t.Fatalf("读取锁表失败: %T 没有 locks 字段", target)
	}
	if holder.Kind() == reflect.Struct {
		holder = holder.FieldByName("locks")
		if !holder.IsValid() {
			t.Fatalf("读取锁表失败: %T 的 locks 字段没有内部锁表", target)
		}
	}
	if holder.Kind() != reflect.Map {
		t.Fatalf("读取锁表失败: %T 的内部锁表不是 map，实际 %s", target, holder.Kind())
	}
	return holder.Len()
}

// holderTracker 记录临界区进入顺序与并发度。
type holderTracker struct {
	mu        sync.Mutex
	order     []string
	active    int
	maxActive int
}

// newHolderTracker 构造空的进入顺序记录器。
func newHolderTracker() *holderTracker {
	return &holderTracker{}
}

// enter 记录一次临界区进入。
func (h *holderTracker) enter(label string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.order = append(h.order, label)
	h.active++
	if h.active > h.maxActive {
		h.maxActive = h.active
	}
}

// leave 记录一次临界区退出。
func (h *holderTracker) leave() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active--
}

// assertMaxActive 断言观察到的最大并发持有者数量。
func (h *holderTracker) assertMaxActive(t *testing.T, want int) {
	t.Helper()
	h.mu.Lock()
	got := h.maxActive
	h.mu.Unlock()
	if got != want {
		t.Fatalf("临界区最大并发持有者应为 %d，实际 %d", want, got)
	}
}

// assertOrder 断言临界区进入顺序。
func (h *holderTracker) assertOrder(t *testing.T, want []string) {
	t.Helper()
	h.mu.Lock()
	got := append([]string(nil), h.order...)
	h.mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("临界区进入顺序应为 %v，实际 %v", want, got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("临界区进入顺序应为 %v，实际 %v", want, got)
		}
	}
}

// awaitEntered 等待某个调用取得锁，超时即失败。
func awaitEntered(t *testing.T, entered <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(enterWaitTimeout):
		t.Fatalf("%s（等待 %s 仍未取得锁）", message, enterWaitTimeout)
	}
}

// assertNotEntered 断言某个调用在观察窗口内没有进入临界区。
func assertNotEntered(t *testing.T, entered <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-entered:
		t.Fatalf("%s", message)
	case <-time.After(notEnteredProbeTimeout):
	}
}
