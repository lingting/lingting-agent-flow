package planningagent_test

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// renewFailureStore 是可注入续租失败的租约存储包装。
//
// 它只在"续租"上做故障注入，其余能力原样转交给真实数据库：用例因此可以复现
// "执行者已不能确认自己持有执行权"的现场，而不需要缩短 TTL 或长时间等待。
type renewFailureStore struct {
	storage.PlanningSessionLeaseStore
	// failRenew 为 true 时下一次续租失败。
	failRenew atomic.Bool
}

// RenewPlanningSessionLease 在启用故障注入后返回数据库错误。
func (s *renewFailureStore) RenewPlanningSessionLease(
	ctx context.Context,
	request storage.PlanningSessionLeaseRenewRequest,
) error {
	if s.failRenew.Load() {
		return errors.New("模拟租约续期失败：数据库暂时不可用")
	}
	return s.PlanningSessionLeaseStore.RenewPlanningSessionLease(ctx, request)
}

// readPlanningSession 直接读取数据库中的规划会话，用于断言租约拒绝没有改写业务事实。
func readPlanningSession(
	t *testing.T,
	db *sqlite.DB,
	sessionID domain.ID,
) domain.PlanningSession {
	t.Helper()
	session, err := db.GetPlanningSession(testContextForLease(t), sessionID)
	if err != nil {
		t.Fatalf("读取规划会话失败: %v", err)
	}
	return *session
}

// testContextForLease 返回带超时的上下文。
func testContextForLease(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// leaseExpiryMillis 读取租约行的到期时间。
func leaseExpiryMillis(t *testing.T, db *sqlite.DB, sessionID domain.ID) int64 {
	t.Helper()
	raw := testkit.OpenSQLite(t, db.Path())
	var expiresAt sql.NullInt64
	const statement = `SELECT lease_expires_at FROM planning_session_leases WHERE planning_session_id = ?`
	if err := raw.QueryRowContext(testContextForLease(t), statement, sessionID.String()).Scan(&expiresAt); err != nil {
		t.Fatalf("读取租约到期时间失败: %v", err)
	}
	if !expiresAt.Valid {
		t.Fatal("租约行没有记录到期时间")
	}
	return expiresAt.Int64
}

// tryAcquireLease 以另一个执行者身份在指定时刻尝试领取租约。
func tryAcquireLease(
	t *testing.T,
	db *sqlite.DB,
	sessionID domain.ID,
	owner string,
	at time.Time,
	ttl time.Duration,
) (domain.PlanningSessionLeaseHandle, error) {
	t.Helper()
	return db.AcquirePlanningSessionLease(
		testContextForLease(t),
		storage.PlanningSessionLeaseRequest{
			PlanningSessionID: sessionID,
			Owner:             owner,
			Operation:         domain.PlanningLeaseOperationRunRole,
			TTL:               ttl,
			At:                at,
		},
	)
}

// TestPlanningAgentServiceCrossInstanceLease 校验两个服务实例对同一会话只有一个能进入执行。
func TestPlanningAgentServiceCrossInstanceLease(t *testing.T) {
	t.Parallel()
	fixture := newPlanningFixture(t, fixtureOptions{start: true})
	secondPort := fixture.newPort(0, false)
	second := fixture.newInstance(t, secondPort, nil)
	sessionID := fixture.targets[0].session.ID

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := fixture.call(firstCtx, 0)
	fixture.port.waitEntered(t, 1)
	// 第一个实例已经取得租约并进入 Pi 执行：此刻的角色会话绑定必须被保留。
	before := readPlanningSession(t, fixture.db, sessionID)

	_, err := second.RunRole(context.Background(), fixture.request(0))
	if !errors.Is(err, app.ErrPlanningAgentBusy) {
		t.Fatalf("第二个实例应因租约冲突被拒绝，实际: %v", err)
	}
	if calls := secondPort.callCount(); calls != 0 {
		t.Fatalf("被租约拒绝的调用不得进入自己的 RunAgent，实际进入 %d 次", calls)
	}
	after := readPlanningSession(t, fixture.db, sessionID)
	if after.AnalystSessionID != before.AnalystSessionID ||
		after.PlannerSessionID != before.PlannerSessionID {
		t.Fatal("被租约拒绝的调用不得改写稳定 Session 绑定")
	}

	cancelFirst()
	if err := waitCall(t, firstDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("第一个实例应按调用方取消返回，实际: %v", err)
	}
	// 租约随第一个实例的完整调用一起释放：第二个实例现在可以取得租约。
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	secondDone := fixture.callService(secondCtx, second, 0)
	secondPort.waitEntered(t, 1)
	cancelSecond()
	if err := waitCall(t, secondDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("第二个实例取得租约后应按取消返回，实际: %v", err)
	}
}

// TestPlanningAgentServiceLeaseRenewal 校验长时间调用期间租约持续续期，不会被其他执行者接管。
func TestPlanningAgentServiceLeaseRenewal(t *testing.T) {
	t.Parallel()
	const ttl = 300 * time.Millisecond
	fixture := newPlanningFixture(t, fixtureOptions{
		start:        true,
		ignoreCancel: true,
		leaseTTL:     ttl,
	})
	sessionID := fixture.targets[0].session.ID
	done := fixture.call(context.Background(), 0)
	fixture.port.waitEntered(t, 1)
	initialExpiry := leaseExpiryMillis(t, fixture.db, sessionID)

	// 越过 TTL 的等待：如果没有续期，租约此时已经过期并可以被接管。
	time.Sleep(3 * fixture.leases.TTL() / 2)
	renewedExpiry := leaseExpiryMillis(t, fixture.db, sessionID)
	if renewedExpiry <= initialExpiry {
		t.Fatalf("长时间调用期间租约应被续期，到期时间没有前移: %d", renewedExpiry)
	}
	if renewedExpiry <= time.Now().UTC().UnixMilli() {
		t.Fatalf("续期后租约应仍在有效期内，到期时间 %d", renewedExpiry)
	}
	if _, err := tryAcquireLease(
		t,
		fixture.db,
		sessionID,
		"intruder",
		time.Now().UTC(),
		ttl,
	); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("续期中的租约不得被其他执行者接管，实际: %v", err)
	}

	fixture.port.releaseAll()
	if err := waitCall(t, done); err == nil {
		t.Fatal("替身显式释放后调用应返回错误")
	}
	// 调用完整返回后租约已释放：新的执行者可以领取，代次继续递增。
	handle, err := tryAcquireLease(t, fixture.db, sessionID, "intruder", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("调用返回后租约应可被重新领取: %v", err)
	}
	if handle.Epoch < 2 {
		t.Fatalf("重新领取的代次应严格递增，实际 %d", handle.Epoch)
	}
}

// TestPlanningAgentServiceLeaseLoss 校验续租失败会取消执行、等待归档收尾并如实上报丢失。
func TestPlanningAgentServiceLeaseLoss(t *testing.T) {
	t.Parallel()
	const ttl = time.Second
	store := &renewFailureStore{}
	fixture := newPlanningFixture(t, fixtureOptions{
		start:    true,
		leaseTTL: ttl,
		leaseStore: func(inner storage.PlanningSessionLeaseStore) storage.PlanningSessionLeaseStore {
			store.PlanningSessionLeaseStore = inner
			return store
		},
	})
	archiving, closeArchive := fixture.port.holdArchive(t)
	sessionID := fixture.targets[0].session.ID
	done := fixture.call(context.Background(), 0)
	fixture.port.waitEntered(t, 1)

	// 此后本次执行者已不能确认自己仍持有执行权。
	store.failRenew.Store(true)
	select {
	case <-archiving:
	case <-time.After(10 * time.Second):
		t.Fatal("等待 Session 归档阶段超时")
	}
	// (1) 续租失败必须取消传给 RunAgent 的上下文。
	if err := fixture.port.contextAt(t, 0).Err(); err == nil {
		t.Fatal("续租失败必须取消传给 RunAgent 的上下文")
	}
	// (2) 归档尚未完成：RunRole 不得提前返回，租约也不得提前释放。
	select {
	case err := <-done:
		t.Fatalf("归档收尾完成之前 RunRole 不得返回，实际: %v", err)
	default:
	}
	if _, err := tryAcquireLease(
		t,
		fixture.db,
		sessionID,
		"intruder",
		time.Now().UTC(),
		time.Minute,
	); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("归档收尾期间租约仍属于本次调用，其他执行者不得接管: %v", err)
	}

	closeArchive()
	err := waitCall(t, done)
	if !errors.Is(err, app.ErrPlanningAgentLeaseLost) {
		t.Fatalf("租约丢失必须以 ErrPlanningAgentLeaseLost 上报，实际: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("本轮远程执行因租约丢失被取消，错误链应保留取消原因，实际: %v", err)
	}
	// 收敛之后租约已被释放：其他执行者可以正常领取。
	if _, err := tryAcquireLease(
		t,
		fixture.db,
		sessionID,
		"intruder",
		time.Now().UTC(),
		time.Minute,
	); err != nil {
		t.Fatalf("收敛后租约应可被重新领取: %v", err)
	}
}

// TestPlanningAgentServiceLeaseTakeoverKeepsNewLease 校验旧持有者不得清空新代次的租约。
func TestPlanningAgentServiceLeaseTakeoverKeepsNewLease(t *testing.T) {
	t.Parallel()
	const ttl = 300 * time.Millisecond
	store := &renewFailureStore{}
	fixture := newPlanningFixture(t, fixtureOptions{
		start:    true,
		leaseTTL: ttl,
		leaseStore: func(inner storage.PlanningSessionLeaseStore) storage.PlanningSessionLeaseStore {
			store.PlanningSessionLeaseStore = inner
			return store
		},
	})
	archiving, closeArchive := fixture.port.holdArchive(t)
	sessionID := fixture.targets[0].session.ID
	done := fixture.call(context.Background(), 0)
	fixture.port.waitEntered(t, 1)
	store.failRenew.Store(true)
	select {
	case <-archiving:
	case <-time.After(10 * time.Second):
		t.Fatal("等待 Session 归档阶段超时")
	}

	// 模拟"旧租约已过期并被新执行者接管"：领取发生在旧租约到期之后。
	takeoverAt := time.Now().UTC().Add(ttl + time.Second)
	newHandle, err := tryAcquireLease(t, fixture.db, sessionID, "new-owner", takeoverAt, time.Minute)
	if err != nil {
		t.Fatalf("过期租约应允许被接管: %v", err)
	}
	closeArchive()
	if err := waitCall(t, done); !errors.Is(err, app.ErrPlanningAgentLeaseLost) {
		t.Fatalf("旧持有者必须以租约丢失上报，实际: %v", err)
	}
	// 旧持有者的迟到释放不得清空新代次的租约。
	err = fixture.db.RenewPlanningSessionLease(
		testContextForLease(t),
		storage.PlanningSessionLeaseRenewRequest{
			Handle: newHandle,
			TTL:    time.Minute,
			At:     takeoverAt.Add(time.Second),
		},
	)
	if err != nil {
		t.Fatalf("旧持有者不得清空新代次的租约: %v", err)
	}
}
