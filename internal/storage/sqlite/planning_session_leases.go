package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// planningSessionLeaseStore 实现 storage.PlanningSessionLeaseStore。
//
// 三个操作都是单条条件语句：领取使用原子 UPSERT，续期与释放使用带 owner + epoch 条件的
// UPDATE。因此并发领取最多只有一个成功，旧代次的迟到续期与迟到释放都会被拒绝。
type planningSessionLeaseStore struct {
	q queryer
}

// AcquirePlanningSessionLease 以原子 UPSERT 领取规划会话租约。
//
// 语义固定为：
//   - 没有记录时插入一行，代次为 1；
//   - 记录存在且 owner 为空（已显式释放）、到期时间为空或已过期时接管，代次在同一语句内
//     递增；
//   - 记录存在且租约仍有效时 DO UPDATE 的 WHERE 条件不成立，影响行数为 0，按
//     storage.ErrConflict 返回。
//
// 代次由数据库在同一语句内递增，调用方无法预先知道结果，因此领取成功后必须回读；回读
// 条件同时覆盖 owner 与到期时间，若在回读之前租约已被他人接管，领取按失败处理，绝不返回
// 一个不属于本次调用的代次。
func (s planningSessionLeaseStore) AcquirePlanningSessionLease(
	ctx context.Context,
	request storage.PlanningSessionLeaseRequest,
) (domain.PlanningSessionLeaseHandle, error) {
	if err := request.PlanningSessionID.Validate(); err != nil {
		return domain.PlanningSessionLeaseHandle{}, err
	}
	owner := strings.TrimSpace(request.Owner)
	if owner == "" {
		return domain.PlanningSessionLeaseHandle{}, wrapError(
			"领取规划会话租约",
			errors.Join(storage.ErrInvalidData, errors.New("执行者标识不能为空")),
		)
	}
	operation := strings.TrimSpace(request.Operation.String())
	if operation == "" {
		return domain.PlanningSessionLeaseHandle{}, wrapError(
			"领取规划会话租约",
			errors.Join(storage.ErrInvalidData, errors.New("操作类型不能为空")),
		)
	}
	if request.TTL <= 0 {
		return domain.PlanningSessionLeaseHandle{}, wrapError(
			"领取规划会话租约",
			errors.Join(storage.ErrInvalidData, errors.New("租约有效期必须大于 0")),
		)
	}
	at := request.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	nowMillis := toMillis(at)
	expiresAt := toMillis(at.Add(request.TTL))
	const statement = `
INSERT INTO planning_session_leases (
	planning_session_id, owner, operation, epoch, lease_expires_at, updated_at
) VALUES (?, ?, ?, 1, ?, ?)
ON CONFLICT (planning_session_id) DO UPDATE SET
	owner = excluded.owner,
	operation = excluded.operation,
	epoch = epoch + 1,
	lease_expires_at = excluded.lease_expires_at,
	updated_at = excluded.updated_at
WHERE owner IS NULL OR lease_expires_at IS NULL OR lease_expires_at <= ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		toIDParam(request.PlanningSessionID),
		owner,
		operation,
		expiresAt,
		nowMillis,
		nowMillis,
	)
	if err != nil {
		return domain.PlanningSessionLeaseHandle{}, wrapError("领取规划会话租约", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.PlanningSessionLeaseHandle{}, wrapError("领取规划会话租约", err)
	}
	if affected == 0 {
		return domain.PlanningSessionLeaseHandle{}, wrapError(
			"领取规划会话租约",
			errors.Join(
				storage.ErrConflict,
				errors.New("规划会话仍被其他执行者持有: "+request.PlanningSessionID.String()),
			),
		)
	}
	epoch, err := s.readPlanningSessionLeaseEpoch(ctx, request.PlanningSessionID, owner)
	if err != nil {
		return domain.PlanningSessionLeaseHandle{}, err
	}
	return domain.PlanningSessionLeaseHandle{
		PlanningSessionID: request.PlanningSessionID,
		Owner:             owner,
		Epoch:             epoch,
	}, nil
}

// readPlanningSessionLeaseEpoch 读取刚写入的领取代次。
func (s planningSessionLeaseStore) readPlanningSessionLeaseEpoch(
	ctx context.Context,
	id domain.ID,
	owner string,
) (int64, error) {
	const statement = `
SELECT epoch FROM planning_session_leases
WHERE planning_session_id = ? AND owner = ? AND lease_expires_at IS NOT NULL`
	var epoch int64
	err := s.q.QueryRowContext(ctx, statement, toIDParam(id), owner).Scan(&epoch)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, wrapError(
				"读取规划会话租约代次",
				errors.Join(
					storage.ErrConflict,
					errors.New("领取成功后租约已被接管: "+id.String()),
				),
			)
		}
		return 0, wrapError("读取规划会话租约代次", err)
	}
	if epoch < 1 {
		return 0, wrapError(
			"读取规划会话租约代次",
			errors.Join(storage.ErrInvalidData, errors.New("领取代次必须大于 0")),
		)
	}
	return epoch, nil
}

// RenewPlanningSessionLease 以 owner + epoch 为条件延长规划会话租约。
//
// 只有"同一 owner、同一代次、仍未过期"才允许续期：租约一旦过期，旧持有者就不得通过迟到
// 续期把执行权抢回来，必须由新的原子领取建立新代次。影响行数为 0 返回 storage.ErrConflict。
func (s planningSessionLeaseStore) RenewPlanningSessionLease(
	ctx context.Context,
	request storage.PlanningSessionLeaseRenewRequest,
) error {
	if err := request.Handle.Validate(); err != nil {
		return err
	}
	if request.TTL <= 0 {
		return wrapError(
			"续期规划会话租约",
			errors.Join(storage.ErrInvalidData, errors.New("租约有效期必须大于 0")),
		)
	}
	at := request.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	nowMillis := toMillis(at)
	expiresAt := toMillis(at.Add(request.TTL))
	const statement = `
UPDATE planning_session_leases SET lease_expires_at = ?, updated_at = ?
WHERE planning_session_id = ? AND owner = ? AND epoch = ?
  AND lease_expires_at IS NOT NULL AND lease_expires_at > ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		expiresAt,
		nowMillis,
		toIDParam(request.Handle.PlanningSessionID),
		request.Handle.Owner,
		request.Handle.Epoch,
		nowMillis,
	)
	if err != nil {
		return wrapError("续期规划会话租约", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapError("续期规划会话租约", err)
	}
	if affected == 0 {
		return wrapError(
			"续期规划会话租约",
			errors.Join(
				storage.ErrConflict,
				errors.New("租约已过期或已转移到其他代次: "+request.Handle.PlanningSessionID.String()),
			),
		)
	}
	return nil
}

// ReleasePlanningSessionLease 释放由指定租约身份持有的规划会话租约。
//
// 条件同时覆盖 owner、epoch 与"仍未过期"：过期租约不得通过迟到释放重新清空，旧代次也
// 不得释放新持有者的租约。释放只清空 owner / operation / lease_expires_at 并保留 epoch，
// 因此下一次领取继续递增，旧持有者的迟到写入仍被拒绝。没有命中任何记录时返回
// storage.ErrConflict 与 false。
func (s planningSessionLeaseStore) ReleasePlanningSessionLease(
	ctx context.Context,
	handle domain.PlanningSessionLeaseHandle,
	at time.Time,
) (bool, error) {
	if err := handle.Validate(); err != nil {
		return false, err
	}
	nowMillis := toMillis(at)
	const statement = `
UPDATE planning_session_leases
SET owner = NULL, operation = NULL, lease_expires_at = NULL, updated_at = ?
WHERE planning_session_id = ? AND owner = ? AND epoch = ?
  AND lease_expires_at IS NOT NULL AND lease_expires_at > ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		nowMillis,
		toIDParam(handle.PlanningSessionID),
		handle.Owner,
		handle.Epoch,
		nowMillis,
	)
	if err != nil {
		return false, wrapError("释放规划会话租约", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, wrapError("释放规划会话租约", err)
	}
	if affected == 0 {
		return false, wrapError(
			"释放规划会话租约",
			errors.Join(
				storage.ErrConflict,
				errors.New("租约已不属于本次领取: "+handle.PlanningSessionID.String()),
			),
		)
	}
	return true, nil
}
