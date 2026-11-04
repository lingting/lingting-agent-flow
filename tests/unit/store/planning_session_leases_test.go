package sqlite_test

import (
	"context"
	"database/sql"
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

// 规划会话租约用例使用固定的时间基准：租约全部以显式 At 计算，不依赖真实时钟，
// 因此"过期""接管""续期"都在可控时间点上判定，不需要长时间 Sleep。
const leaseTTL = time.Minute

// leaseBase 返回租约用例的时间基准。
//
// 基准落在整分钟上，使"领取 + TTL"的毫秒值与断言保持一致，不受毫秒截断影响。
func leaseBase() time.Time {
	return time.Now().UTC().Truncate(time.Minute)
}

// newLeaseSession 返回一个随机的规划会话标识。
//
// 租约表不声明外键：领取先于"会话是否存在"的校验发生，因此不需要先写入会话记录。
func newLeaseSession() domain.ID {
	return domain.NewID()
}

// acquireLease 以指定 owner 在给定时间点领取租约。
func acquireLease(
	t *testing.T,
	db *sqlite.DB,
	sessionID domain.ID,
	owner string,
	at time.Time,
	ttl time.Duration,
) domain.PlanningSessionLeaseHandle {
	t.Helper()
	handle, err := db.AcquirePlanningSessionLease(testContext(t), storage.PlanningSessionLeaseRequest{
		PlanningSessionID: sessionID,
		Owner:             owner,
		Operation:         domain.PlanningLeaseOperationRunRole,
		TTL:               ttl,
		At:                at,
	})
	if err != nil {
		t.Fatalf("领取规划会话租约失败: %v", err)
	}
	return handle
}

// leaseRow 记录租约行的原始列，用于断言释放后保留代次、清空归属。
type leaseRow struct {
	owner      sql.NullString
	operation  sql.NullString
	epoch      int64
	expiresAt  sql.NullInt64
	updatedAt  int64
	rowExists  bool
	planningID string
}

// readLeaseRow 用独立连接读取指定规划会话的租约行。
func readLeaseRow(t *testing.T, db *sqlite.DB, sessionID domain.ID) leaseRow {
	t.Helper()
	raw := rawDB(t, db)
	const statement = `
SELECT planning_session_id, owner, operation, epoch, lease_expires_at, updated_at
FROM planning_session_leases WHERE planning_session_id = ?`
	var row leaseRow
	err := raw.QueryRowContext(testContext(t), statement, sessionID.String()).Scan(
		&row.planningID,
		&row.owner,
		&row.operation,
		&row.epoch,
		&row.expiresAt,
		&row.updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return leaseRow{}
		}
		t.Fatalf("读取租约行失败: %v", err)
	}
	row.rowExists = true
	return row
}

// TestPlanningSessionLeaseAcquireStartsAtEpochOne 校验首次领取成功并返回代次 1。
func TestPlanningSessionLeaseAcquireStartsAtEpochOne(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	sessionID := newLeaseSession()
	at := leaseBase()
	handle := acquireLease(t, db, sessionID, "owner-a", at, leaseTTL)
	if handle.PlanningSessionID != sessionID {
		t.Fatalf("租约身份应指向被领取的会话，实际 %s", handle.PlanningSessionID)
	}
	if handle.Owner != "owner-a" {
		t.Fatalf("租约 owner 不符: %s", handle.Owner)
	}
	if handle.Epoch != 1 {
		t.Fatalf("首次领取的代次应为 1，实际 %d", handle.Epoch)
	}
	row := readLeaseRow(t, db, sessionID)
	if !row.rowExists {
		t.Fatal("领取成功后应存在租约行")
	}
	if row.epoch != 1 {
		t.Fatalf("数据库中的代次应为 1，实际 %d", row.epoch)
	}
	if row.owner.String != "owner-a" || row.operation.String != "run_role" {
		t.Fatalf("租约行应记录 owner 与操作类型，实际 %q / %q", row.owner.String, row.operation.String)
	}
	if !row.expiresAt.Valid || row.expiresAt.Int64 != at.Add(leaseTTL).UnixMilli() {
		t.Fatalf("到期时间应与领取时间加 TTL 一致，实际 %v", row.expiresAt)
	}
}

// TestPlanningSessionLeaseRejectsSecondOwner 校验同一规划会话只有第一个领取者成功。
func TestPlanningSessionLeaseRejectsSecondOwner(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	sessionID := newLeaseSession()
	at := leaseBase()
	first := acquireLease(t, db, sessionID, "owner-a", at, leaseTTL)
	_, err := db.AcquirePlanningSessionLease(testContext(t), storage.PlanningSessionLeaseRequest{
		PlanningSessionID: sessionID,
		Owner:             "owner-b",
		Operation:         domain.PlanningLeaseOperationRunRole,
		TTL:               leaseTTL,
		At:                at.Add(time.Second),
	})
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("第二个领取者应得到 storage.ErrConflict，实际: %v", err)
	}
	row := readLeaseRow(t, db, sessionID)
	if row.owner.String != "owner-a" || row.epoch != first.Epoch {
		t.Fatalf("冲突不得改变租约归属，实际 owner %q / 代次 %d", row.owner.String, row.epoch)
	}
}

// TestPlanningSessionLeaseAcrossSeparateConnections 校验两个独立连接只能有一个领取成功。
func TestPlanningSessionLeaseAcrossSeparateConnections(t *testing.T) {
	t.Parallel()
	path := testkit.New(t).SQLitePath()
	firstDB, err := sqlite.Open(testContext(t), sqlite.Options{Path: path, MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("打开第一个数据库连接失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := firstDB.Close(); closeErr != nil {
			t.Errorf("关闭第一个数据库连接失败: %v", closeErr)
		}
	})
	secondDB, err := sqlite.Open(testContext(t), sqlite.Options{Path: path, MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("打开第二个数据库连接失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := secondDB.Close(); closeErr != nil {
			t.Errorf("关闭第二个数据库连接失败: %v", closeErr)
		}
	})
	sessionID := newLeaseSession()
	at := leaseBase()
	stores := []*sqlite.DB{firstDB, secondDB}
	results := make(chan error, len(stores))
	var group sync.WaitGroup
	for index, db := range stores {
		group.Add(1)
		go func() {
			defer group.Done()
			_, acquireErr := db.AcquirePlanningSessionLease(
				context.Background(),
				storage.PlanningSessionLeaseRequest{
					PlanningSessionID: sessionID,
					Owner:             "owner-" + string(rune('a'+index)),
					Operation:         domain.PlanningLeaseOperationPrepare,
					TTL:               leaseTTL,
					At:                at,
				},
			)
			results <- acquireErr
		}()
	}
	group.Wait()
	close(results)
	succeeded := 0
	conflicted := 0
	for acquireErr := range results {
		switch {
		case acquireErr == nil:
			succeeded++
		case errors.Is(acquireErr, storage.ErrConflict):
			conflicted++
		default:
			t.Fatalf("并发领取只应出现成功或冲突，实际: %v", acquireErr)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("两个独立连接应恰好一个成功，实际成功 %d 次、冲突 %d 次", succeeded, conflicted)
	}
	row := readLeaseRow(t, firstDB, sessionID)
	if row.epoch != 1 {
		t.Fatalf("并发领取只应建立一次有效领取，代次应为 1，实际 %d", row.epoch)
	}
}

// TestPlanningSessionLeaseRenewExtendsExpiry 校验持有者可以续期，且续期后不会被过早接管。
func TestPlanningSessionLeaseRenewExtendsExpiry(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	sessionID := newLeaseSession()
	at := leaseBase()
	handle := acquireLease(t, db, sessionID, "owner-a", at, leaseTTL)
	renewAt := at.Add(30 * time.Second)
	if err := db.RenewPlanningSessionLease(testContext(t), storage.PlanningSessionLeaseRenewRequest{
		Handle: handle,
		TTL:    leaseTTL,
		At:     renewAt,
	}); err != nil {
		t.Fatalf("持有者续期应成功: %v", err)
	}
	row := readLeaseRow(t, db, sessionID)
	if !row.expiresAt.Valid || row.expiresAt.Int64 != renewAt.Add(leaseTTL).UnixMilli() {
		t.Fatalf("续期后到期时间应被延长，实际 %v", row.expiresAt)
	}
	// 初始 TTL 已过、续期后的有效期未到：其他执行者不得接管。
	_, err := db.AcquirePlanningSessionLease(testContext(t), storage.PlanningSessionLeaseRequest{
		PlanningSessionID: sessionID,
		Owner:             "owner-b",
		Operation:         domain.PlanningLeaseOperationRunRole,
		TTL:               leaseTTL,
		At:                at.Add(80 * time.Second),
	})
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("续期后的有效租约不应被接管，实际: %v", err)
	}
}

// TestPlanningSessionLeaseExpiredLeaseCannotRenew 校验过期租约不能被旧持有者续期。
func TestPlanningSessionLeaseExpiredLeaseCannotRenew(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	sessionID := newLeaseSession()
	at := leaseBase()
	handle := acquireLease(t, db, sessionID, "owner-a", at, leaseTTL)
	err := db.RenewPlanningSessionLease(testContext(t), storage.PlanningSessionLeaseRenewRequest{
		Handle: handle,
		TTL:    leaseTTL,
		At:     at.Add(leaseTTL + time.Second),
	})
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("过期租约的迟到续期应返回 storage.ErrConflict，实际: %v", err)
	}
}

// TestPlanningSessionLeaseTakeoverRejectsPreviousOwner 校验新代次接管后旧持有者失去全部权力。
func TestPlanningSessionLeaseTakeoverRejectsPreviousOwner(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	sessionID := newLeaseSession()
	at := leaseBase()
	previous := acquireLease(t, db, sessionID, "owner-a", at, leaseTTL)
	takeoverAt := at.Add(leaseTTL + time.Second)
	current := acquireLease(t, db, sessionID, "owner-b", takeoverAt, leaseTTL)
	if current.Epoch != previous.Epoch+1 {
		t.Fatalf("接管后代次应在同一语句内递增，前 %d 后 %d", previous.Epoch, current.Epoch)
	}
	renewAt := takeoverAt.Add(time.Second)
	if err := db.RenewPlanningSessionLease(testContext(t), storage.PlanningSessionLeaseRenewRequest{
		Handle: previous,
		TTL:    leaseTTL,
		At:     renewAt,
	}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("旧代次不得续期，实际: %v", err)
	}
	released, err := db.ReleasePlanningSessionLease(testContext(t), previous, renewAt)
	if !errors.Is(err, storage.ErrConflict) || released {
		t.Fatalf("旧代次不得释放新租约，实际 released=%v err=%v", released, err)
	}
	if err := db.RenewPlanningSessionLease(testContext(t), storage.PlanningSessionLeaseRenewRequest{
		Handle: current,
		TTL:    leaseTTL,
		At:     renewAt,
	}); err != nil {
		t.Fatalf("旧持有者的迟到释放不得影响新租约: %v", err)
	}
}

// TestPlanningSessionLeaseReleaseKeepsEpoch 校验释放清空归属与到期时间但保留代次。
func TestPlanningSessionLeaseReleaseKeepsEpoch(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	sessionID := newLeaseSession()
	at := leaseBase()
	handle := acquireLease(t, db, sessionID, "owner-a", at, leaseTTL)
	released, err := db.ReleasePlanningSessionLease(testContext(t), handle, at.Add(time.Second))
	if err != nil {
		t.Fatalf("持有者释放应成功: %v", err)
	}
	if !released {
		t.Fatal("释放应返回 true")
	}
	row := readLeaseRow(t, db, sessionID)
	if !row.rowExists {
		t.Fatal("释放不得删除租约行")
	}
	if row.owner.Valid || row.operation.Valid || row.expiresAt.Valid {
		t.Fatalf("释放后应清空 owner / operation / 到期时间，实际 %+v", row)
	}
	if row.epoch != handle.Epoch {
		t.Fatalf("释放必须保留代次，实际 %d", row.epoch)
	}
	// 显式释放后可以立即重新领取，且代次继续递增。
	next := acquireLease(t, db, sessionID, "owner-b", at.Add(2*time.Second), leaseTTL)
	if next.Epoch != handle.Epoch+1 {
		t.Fatalf("重新领取的代次应严格递增，前 %d 后 %d", handle.Epoch, next.Epoch)
	}
}

// TestPlanningSessionLeaseExpiredReleaseIsRejected 校验过期租约不能被迟到释放清空。
func TestPlanningSessionLeaseExpiredReleaseIsRejected(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	sessionID := newLeaseSession()
	at := leaseBase()
	handle := acquireLease(t, db, sessionID, "owner-a", at, leaseTTL)
	released, err := db.ReleasePlanningSessionLease(
		testContext(t),
		handle,
		at.Add(leaseTTL+time.Second),
	)
	if !errors.Is(err, storage.ErrConflict) || released {
		t.Fatalf("过期租约的迟到释放应被拒绝，实际 released=%v err=%v", released, err)
	}
	row := readLeaseRow(t, db, sessionID)
	if !row.rowExists || row.owner.String != "owner-a" {
		t.Fatalf("被拒绝的释放不得改变租约行，实际 %+v", row)
	}
}

// TestPlanningSessionLeaseReacquireIncrementsEpoch 校验多轮释放与领取后代次单调递增。
func TestPlanningSessionLeaseReacquireIncrementsEpoch(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	sessionID := newLeaseSession()
	at := leaseBase()
	var lastEpoch int64
	for round := 0; round < 3; round++ {
		moment := at.Add(time.Duration(round) * 10 * time.Second)
		handle := acquireLease(t, db, sessionID, "owner-"+string(rune('a'+round)), moment, leaseTTL)
		if handle.Epoch != lastEpoch+1 {
			t.Fatalf("第 %d 轮领取的代次应严格递增，前 %d 后 %d", round+1, lastEpoch, handle.Epoch)
		}
		lastEpoch = handle.Epoch
		released, err := db.ReleasePlanningSessionLease(
			testContext(t),
			handle,
			moment.Add(time.Second),
		)
		if err != nil || !released {
			t.Fatalf("第 %d 轮释放失败: released=%v err=%v", round+1, released, err)
		}
	}
}

// TestPlanningSessionLeaseDistinguishesStorageErrors 校验数据库故障不会被当成租约竞争。
func TestPlanningSessionLeaseDistinguishesStorageErrors(t *testing.T) {
	t.Parallel()
	path := filepath.Join(testkit.Dir(t), "lease-closed.db")
	db, err := sqlite.Open(testContext(t), sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("关闭数据库失败: %v", closeErr)
	}
	sessionID := newLeaseSession()
	_, acquireErr := db.AcquirePlanningSessionLease(testContext(t), storage.PlanningSessionLeaseRequest{
		PlanningSessionID: sessionID,
		Owner:             "owner-a",
		Operation:         domain.PlanningLeaseOperationRunRole,
		TTL:               leaseTTL,
		At:                leaseBase(),
	})
	if acquireErr == nil {
		t.Fatal("关闭数据库后领取应当失败")
	}
	if errors.Is(acquireErr, storage.ErrConflict) {
		t.Fatalf("数据库不可用不得被映射为租约竞争: %v", acquireErr)
	}
}

// TestPlanningSessionLeaseRejectsInvalidRequest 校验非法请求按数据错误返回而不是竞争。
func TestPlanningSessionLeaseRejectsInvalidRequest(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	sessionID := newLeaseSession()
	_, err := db.AcquirePlanningSessionLease(testContext(t), storage.PlanningSessionLeaseRequest{
		PlanningSessionID: sessionID,
		Owner:             "   ",
		Operation:         domain.PlanningLeaseOperationRunRole,
		TTL:               leaseTTL,
		At:                leaseBase(),
	})
	if !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("空 owner 应按 storage.ErrInvalidData 拒绝，实际: %v", err)
	}
	_, err = db.AcquirePlanningSessionLease(testContext(t), storage.PlanningSessionLeaseRequest{
		PlanningSessionID: sessionID,
		Owner:             "owner-a",
		Operation:         domain.PlanningLeaseOperationRunRole,
		TTL:               0,
		At:                leaseBase(),
	})
	if !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("非正 TTL 应按 storage.ErrInvalidData 拒绝，实际: %v", err)
	}
}
