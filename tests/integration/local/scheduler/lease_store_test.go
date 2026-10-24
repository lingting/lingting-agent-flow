// Package scheduler_test 用真实 SQLite 验证任务租约的原子性、fencing 与可追溯性。
//
// 用例共享同一个实际数据库文件并各自打开独立连接：并发领取的互斥必须来自数据库的
// 条件更新，而不是同一个对象上的进程内锁。断言对象是数据库里的真实记录与受影响
// 行数，不是方法的返回值。
package scheduler_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// leaseFixture 是一套共享同一数据库文件的租约测试现场。
type leaseFixture struct {
	ctx  context.Context
	path string
	now  time.Time
}

// newLeaseFixture 在工作空间中创建数据库并写入一个 queued 任务。
func newLeaseFixture(t *testing.T) *leaseFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	workspace := testkit.New(t)
	path := filepath.Join(workspace.Root(), "agentflow.db")
	fixture := &leaseFixture{ctx: ctx, path: path, now: time.Now().UTC()}
	// 先打开一次完成迁移，后续连接复用同一份 schema。
	db := fixture.open(t)
	requirement, err := domain.NewRequirement(
		"需求标题",
		"需求正文",
		domain.PriorityNormal,
		fixture.now,
	)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(
		requirement.ID,
		"ssh://git.example.com/team/repo",
		"main",
		"",
		fixture.now,
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	return fixture
}

// open 打开一条指向同一数据库文件的独立连接。
func (f *leaseFixture) open(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(f.ctx, sqlite.Options{Path: f.path})
	if err != nil {
		t.Fatalf("打开数据库失败: %s: %v", f.path, err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭数据库失败: %v", closeErr)
		}
	})
	return db
}

// createTask 写入一个新的 queued 任务并返回它。
func (f *leaseFixture) createTask(
	t *testing.T,
	db *sqlite.DB,
	priority domain.Priority,
) domain.Task {
	t.Helper()
	requirement, err := domain.NewRequirement(
		"需求 "+string(priority),
		"需求正文",
		priority,
		f.now,
	)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(f.ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(
		requirement.ID,
		"ssh://git.example.com/team/repo",
		"main",
		"",
		f.now,
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := db.CreateTask(f.ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	return task
}

// queuedOnly 返回自动调度允许领取的状态集合。
func queuedOnly() []domain.TaskStatus {
	return []domain.TaskStatus{domain.TaskStatusQueued}
}

// TestAcquireTaskLeaseIsExclusiveAcrossConnections 验证并发领取只有一个成功。
//
// 多个独立连接同时领取同一 queued 任务：数据库的单条条件 UPDATE 必须让其中恰好
// 一个成功，其余得到 storage.ErrConflict。
func TestAcquireTaskLeaseIsExclusiveAcrossConnections(t *testing.T) {
	t.Parallel()
	fixture := newLeaseFixture(t)
	shared := fixture.open(t)
	task := fixture.createTask(t, shared, domain.PriorityNormal)

	const contenders = 8
	dbs := make([]*sqlite.DB, 0, contenders)
	for index := 0; index < contenders; index++ {
		dbs = append(dbs, fixture.open(t))
	}
	var (
		mu        sync.Mutex
		successes int
		conflicts int
		others    []error
		wg        sync.WaitGroup
	)
	start := make(chan struct{})
	for index := 0; index < contenders; index++ {
		wg.Add(1)
		go func(worker int, db *sqlite.DB) {
			defer wg.Done()
			<-start
			_, err := db.AcquireTaskLease(fixture.ctx, storage.TaskLeaseRequest{
				TaskID:   task.ID,
				Owner:    "worker-" + domain.NewID().String(),
				TTL:      time.Minute,
				Statuses: queuedOnly(),
				At:       fixture.now,
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, storage.ErrConflict):
				conflicts++
			default:
				others = append(others, err)
			}
		}(index, dbs[index])
	}
	close(start)
	wg.Wait()
	if len(others) > 0 {
		t.Fatalf("并发领取出现非冲突错误: %v", others)
	}
	if successes != 1 {
		t.Fatalf("并发领取必须只有一个成功，实际 %d", successes)
	}
	if conflicts != contenders-1 {
		t.Fatalf("其余请求应得到冲突，实际 %d", conflicts)
	}
	// 数据库里必须只有一个有效持有者，且代次为 1。
	stored, err := shared.GetTask(fixture.ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if stored.LeaseOwner == "" || !stored.LeaseActive(fixture.now) {
		t.Fatalf("领取成功后必须留下有效租约: %+v", stored)
	}
	if stored.LeaseEpoch != 1 {
		t.Fatalf("首次领取的代次应为 1，实际 %d", stored.LeaseEpoch)
	}
}

// TestAcquireTaskLeaseRequiresExplicitStatuses 验证空状态集合被拒绝。
//
// 默认集合包含 failed / paused / recovery_required：允许回退会让自动调度绕过 P2 的
// 核验门控，因此存储层必须直接拒绝。
func TestAcquireTaskLeaseRequiresExplicitStatuses(t *testing.T) {
	t.Parallel()
	fixture := newLeaseFixture(t)
	db := fixture.open(t)
	task := fixture.createTask(t, db, domain.PriorityNormal)

	_, err := db.AcquireTaskLease(fixture.ctx, storage.TaskLeaseRequest{
		TaskID: task.ID,
		Owner:  "worker-a",
		TTL:    time.Minute,
		At:     fixture.now,
	})
	if !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("空状态集合必须返回参数错误，实际 %v", err)
	}
	stored, loadErr := db.GetTask(fixture.ctx, task.ID)
	if loadErr != nil {
		t.Fatalf("读取任务失败: %v", loadErr)
	}
	if stored.LeaseOwner != "" {
		t.Fatalf("参数错误不得留下租约: %+v", stored)
	}
}

// TestAcquireTaskLeaseRejectsNonQueuedStatuses 验证领取只接受显式指定的状态。
//
// failed / paused / recovery_required 都不能借默认集合进入自动调度。
func TestAcquireTaskLeaseRejectsNonQueuedStatuses(t *testing.T) {
	t.Parallel()
	fixture := newLeaseFixture(t)

	cases := []struct {
		name   string
		status domain.TaskStatus
	}{
		{name: "失败", status: domain.TaskStatusFailed},
		{name: "暂停", status: domain.TaskStatusPaused},
		{name: "需要人工核验", status: domain.TaskStatusRecoveryRequired},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			// 每个子用例使用独立数据库与独立任务：状态迁移是单向的，共享一条记录
			// 会让用例互相依赖执行顺序。
			db := fixture.open(t)
			task := fixture.createTask(t, db, domain.PriorityNormal)
			stored, err := db.GetTask(fixture.ctx, task.ID)
			if err != nil {
				t.Fatalf("读取任务失败: %v", err)
			}
			previous := stored.Status
			// paused 只能从执行中的状态进入：先经 preparing 再暂停，才能构造
			// "人工暂停"的真实现场。
			for _, hop := range transitionPathTo(testCase.status) {
				if err := stored.Transition(hop, fixture.now); err != nil {
					t.Fatalf("迁移任务状态到 %s 失败: %v", hop, err)
				}
			}
			if err := db.UpdateTaskIfStatus(
				fixture.ctx,
				*stored,
				[]domain.TaskStatus{previous},
			); err != nil {
				t.Fatalf("写入任务状态失败: %v", err)
			}
			_, acquireErr := db.AcquireTaskLease(fixture.ctx, storage.TaskLeaseRequest{
				TaskID:   task.ID,
				Owner:    "worker-" + testCase.name,
				TTL:      time.Minute,
				Statuses: queuedOnly(),
				At:       fixture.now,
			})
			if !errors.Is(acquireErr, storage.ErrConflict) {
				t.Fatalf("%s 状态不得被 queued 集合领取，实际 %v", testCase.status, acquireErr)
			}
			// 参数错误不得留下任何租约痕迹。
			after, reloadErr := db.GetTask(fixture.ctx, task.ID)
			if reloadErr != nil {
				t.Fatalf("读取任务失败: %v", reloadErr)
			}
			if after.LeaseOwner != "" || after.LeaseEpoch != 0 {
				t.Fatalf("领取失败不得留下租约: %+v", after)
			}
		})
	}
}

// TestTaskLeaseEpochIsFenced 验证旧代次的续期、释放与写入都被拒绝。
func TestTaskLeaseEpochIsFenced(t *testing.T) {
	t.Parallel()
	fixture := newLeaseFixture(t)
	db := fixture.open(t)
	task := fixture.createTask(t, db, domain.PriorityNormal)

	first, err := db.AcquireTaskLease(fixture.ctx, storage.TaskLeaseRequest{
		TaskID:   task.ID,
		Owner:    "worker-a",
		TTL:      time.Minute,
		Statuses: queuedOnly(),
		At:       fixture.now,
	})
	if err != nil {
		t.Fatalf("首次领取失败: %v", err)
	}
	if first.Epoch != 1 {
		t.Fatalf("首次领取代次应为 1，实际 %d", first.Epoch)
	}
	// 租约过期后由另一个执行者接管，代次递增。
	expiredAt := fixture.now.Add(2 * time.Minute)
	second, err := db.AcquireTaskLease(fixture.ctx, storage.TaskLeaseRequest{
		TaskID:   task.ID,
		Owner:    "worker-b",
		TTL:      time.Minute,
		Statuses: queuedOnly(),
		At:       expiredAt,
	})
	if err != nil {
		t.Fatalf("接管过期租约失败: %v", err)
	}
	if second.Epoch != 2 {
		t.Fatalf("接管后代次应为 2，实际 %d", second.Epoch)
	}

	// 旧代次的续期必须被拒绝。
	renewErr := db.RenewTaskLease(fixture.ctx, storage.TaskLeaseRenewRequest{
		Handle: first,
		TTL:    time.Minute,
		At:     expiredAt.Add(10 * time.Second),
	})
	if !errors.Is(renewErr, storage.ErrConflict) {
		t.Fatalf("旧代次续期必须返回冲突，实际 %v", renewErr)
	}
	// 旧代次的释放必须被拒绝，且不得清除新持有者的租约。
	released, releaseErr := db.ReleaseTaskLease(
		fixture.ctx,
		first,
		expiredAt.Add(10*time.Second),
	)
	if released {
		t.Fatal("旧代次不得释放新持有者的租约")
	}
	if !errors.Is(releaseErr, storage.ErrConflict) {
		t.Fatalf("旧代次释放必须返回冲突，实际 %v", releaseErr)
	}
	stored, err := db.GetTask(fixture.ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if stored.LeaseOwner != "worker-b" || stored.LeaseEpoch != 2 {
		t.Fatalf("新持有者的租约不得被旧代次清除: %+v", stored)
	}
	// 旧代次的状态写入必须被拒绝。
	stale := *stored
	stale.LeaseOwner = first.Owner
	stale.LeaseEpoch = first.Epoch
	guardErr := db.UpdateTaskLeaseGuard(
		fixture.ctx,
		stale,
		[]domain.TaskStatus{domain.TaskStatusQueued},
		first,
	)
	if !errors.Is(guardErr, storage.ErrConflict) {
		t.Fatalf("旧代次的迟到写入必须返回冲突，实际 %v", guardErr)
	}
	// 新代次可以正常写入。
	if err := db.UpdateTaskLeaseGuard(
		fixture.ctx,
		*stored,
		[]domain.TaskStatus{domain.TaskStatusQueued},
		second,
	); err != nil {
		t.Fatalf("新代次写入应成功: %v", err)
	}
}

// TestTaskLeaseEpochSurvivesRestart 验证代次在重启后仍可读取并继续递增。
func TestTaskLeaseEpochSurvivesRestart(t *testing.T) {
	t.Parallel()
	fixture := newLeaseFixture(t)
	first := fixture.open(t)
	task := fixture.createTask(t, first, domain.PriorityNormal)
	handle, err := first.AcquireTaskLease(fixture.ctx, storage.TaskLeaseRequest{
		TaskID:   task.ID,
		Owner:    "worker-a",
		TTL:      time.Minute,
		Statuses: queuedOnly(),
		At:       fixture.now,
	})
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	if handle.Epoch != 1 {
		t.Fatalf("代次应为 1，实际 %d", handle.Epoch)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
	// 重新打开同一路径：代次必须从数据库读回，而不是从零开始。
	reopened := fixture.open(t)
	stored, err := reopened.GetTask(fixture.ctx, task.ID)
	if err != nil {
		t.Fatalf("重启后读取任务失败: %v", err)
	}
	if stored.LeaseEpoch != 1 || stored.LeaseOwner != "worker-a" {
		t.Fatalf("重启后租约身份不符: %+v", stored)
	}
	next, err := reopened.AcquireTaskLease(fixture.ctx, storage.TaskLeaseRequest{
		TaskID:   task.ID,
		Owner:    "worker-b",
		TTL:      time.Minute,
		Statuses: queuedOnly(),
		At:       fixture.now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("重启后接管失败: %v", err)
	}
	if next.Epoch != 2 {
		t.Fatalf("重启后接管代次应为 2，实际 %d", next.Epoch)
	}
}

// TestRenewTaskLeaseRejectsExpiredLease 验证过期租约不能被迟到续期恢复。
func TestRenewTaskLeaseRejectsExpiredLease(t *testing.T) {
	t.Parallel()
	fixture := newLeaseFixture(t)
	db := fixture.open(t)
	task := fixture.createTask(t, db, domain.PriorityNormal)
	handle, err := db.AcquireTaskLease(fixture.ctx, storage.TaskLeaseRequest{
		TaskID:   task.ID,
		Owner:    "worker-a",
		TTL:      time.Minute,
		Statuses: queuedOnly(),
		At:       fixture.now,
	})
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	// 续期发生在租约到期之后：必须被拒绝，否则"过期即失去执行权"就不成立。
	renewErr := db.RenewTaskLease(fixture.ctx, storage.TaskLeaseRenewRequest{
		Handle: handle,
		TTL:    time.Minute,
		At:     fixture.now.Add(2 * time.Minute),
	})
	if !errors.Is(renewErr, storage.ErrConflict) {
		t.Fatalf("过期后续期必须返回冲突，实际 %v", renewErr)
	}
	// 有效期内续期成功。
	if err := db.RenewTaskLease(fixture.ctx, storage.TaskLeaseRenewRequest{
		Handle: handle,
		TTL:    2 * time.Minute,
		At:     fixture.now.Add(30 * time.Second),
	}); err != nil {
		t.Fatalf("有效期内续期应成功: %v", err)
	}
}

// TestReleaseTaskLeaseChecksAffectedRows 验证未命中记录的释放不被当作成功。
func TestReleaseTaskLeaseChecksAffectedRows(t *testing.T) {
	t.Parallel()
	fixture := newLeaseFixture(t)
	db := fixture.open(t)
	task := fixture.createTask(t, db, domain.PriorityNormal)
	handle, err := db.AcquireTaskLease(fixture.ctx, storage.TaskLeaseRequest{
		TaskID:   task.ID,
		Owner:    "worker-a",
		TTL:      time.Minute,
		Statuses: queuedOnly(),
		At:       fixture.now,
	})
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	// 错误 owner 不得释放。
	wrongOwner := handle
	wrongOwner.Owner = "worker-b"
	released, err := db.ReleaseTaskLease(fixture.ctx, wrongOwner, fixture.now)
	if released {
		t.Fatal("错误 owner 不得释放租约")
	}
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("错误 owner 必须返回冲突，实际 %v", err)
	}
	// 错误代次不得释放。
	wrongEpoch := handle
	wrongEpoch.Epoch = handle.Epoch + 1
	released, err = db.ReleaseTaskLease(fixture.ctx, wrongEpoch, fixture.now)
	if released {
		t.Fatal("错误代次不得释放租约")
	}
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("错误代次必须返回冲突，实际 %v", err)
	}
	// 正确身份释放成功。
	released, err = db.ReleaseTaskLease(fixture.ctx, handle, fixture.now)
	if err != nil {
		t.Fatalf("释放失败: %v", err)
	}
	if !released {
		t.Fatal("正确身份释放应命中记录")
	}
	// 重复释放必须返回冲突，而不是虚假的成功。
	released, err = db.ReleaseTaskLease(fixture.ctx, handle, fixture.now)
	if released {
		t.Fatal("重复释放不得返回成功")
	}
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("重复释放必须返回冲突，实际 %v", err)
	}
}

// TestTaskLeaseEventIsTraceable 验证领取与释放都留下可追溯事件。
func TestTaskLeaseEventIsTraceable(t *testing.T) {
	t.Parallel()
	fixture := newLeaseFixture(t)
	db := fixture.open(t)
	task := fixture.createTask(t, db, domain.PriorityNormal)
	handle, err := db.AcquireTaskLease(fixture.ctx, storage.TaskLeaseRequest{
		TaskID:   task.ID,
		Owner:    "worker-a",
		TTL:      time.Minute,
		Statuses: queuedOnly(),
		At:       fixture.now,
	})
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	event, err := domain.NewEvent(
		task.ID,
		nil,
		nil,
		nil,
		domain.EventTypeTaskClaimed,
		[]byte(`{"owner":"worker-a","epoch":1}`),
		fixture.now,
	)
	if err != nil {
		t.Fatalf("构造事件失败: %v", err)
	}
	if _, err := db.AppendTaskLeaseEvent(fixture.ctx, event); err != nil {
		t.Fatalf("写入领取事件失败: %v", err)
	}
	if _, err := db.ReleaseTaskLease(fixture.ctx, handle, fixture.now); err != nil {
		t.Fatalf("释放失败: %v", err)
	}
	events, err := db.ListEventsByTask(fixture.ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	claimed := 0
	for _, item := range events {
		if item.Type == domain.EventTypeTaskClaimed {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("领取事件应恰好一条，实际 %d", claimed)
	}
}

// TestAcquireTaskLeaseRejectsDeletedTask 验证逻辑删除的任务不能被领取。
func TestAcquireTaskLeaseRejectsDeletedTask(t *testing.T) {
	t.Parallel()
	fixture := newLeaseFixture(t)
	db := fixture.open(t)
	task := fixture.createTask(t, db, domain.PriorityNormal)
	if err := db.DeleteTask(
		fixture.ctx,
		task.ID,
		[]domain.TaskStatus{domain.TaskStatusQueued},
		fixture.now,
	); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}
	_, err := db.AcquireTaskLease(fixture.ctx, storage.TaskLeaseRequest{
		TaskID:   task.ID,
		Owner:    "worker-a",
		TTL:      time.Minute,
		Statuses: queuedOnly(),
		At:       fixture.now,
	})
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("已删除任务必须返回冲突，实际 %v", err)
	}
}

// transitionPathTo 返回从 queued 到达目标状态的合法迁移路径。
//
// 状态机不允许所有状态互相直达：paused 只能从执行中的状态进入，recovery_required
// 可以从 queued 直接进入。路径显式列出，避免用例依赖状态机的隐式放宽。
func transitionPathTo(target domain.TaskStatus) []domain.TaskStatus {
	switch target {
	case domain.TaskStatusPaused:
		return []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusPlanning,
			domain.TaskStatusPaused,
		}
	default:
		return []domain.TaskStatus{target}
	}
}
