package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 本文件实现项目级执行权、项目执行意图与项目任务集合的存储能力。
//
// 并发正确性全部来自带条件的 SQL：
//   - 取得执行权只更新"已到期"的行，或插入一条全新行；未到期的执行权无法被抢占；
//   - 续租、释放都带 owner + epoch 条件，旧持有者的迟到写入影响行数为 0，按冲突返回；
//   - 释放只把 expires_at 置为当前时间，不删除行、不重置 epoch。
//
// 进程内互斥不是正确性保证：即使将来出现第二个写入者，条件写入仍会拒绝过期更新。

// leaseColumns 是项目执行权表的完整列顺序。
const leaseColumns = `project_id, owner, epoch, expires_at, acquired_at, updated_at`

// projectExecutionStateColumns 是项目执行意图表的完整列顺序。
const projectExecutionStateColumns = `project_id, state, stop_requested_at, updated_at`

// projectExecutionStore 实现 storage.ProjectExecutionStore。
type projectExecutionStore struct {
	q queryer
}

// leaseRowScanner 是扫描一行执行权记录所需的接口。
type leaseRowScanner interface {
	Scan(dest ...any) error
}

// AcquireProjectExecutionLease 以条件写入取得项目执行权。
//
// 语句顺序固定：
//  1. 更新已到期的登记（epoch 原子递增）；
//  2. 没有到期登记时插入一条全新登记（epoch = 1）；
//  3. 两步都没有影响行数，说明存在未到期的登记，返回 storage.ErrConflict。
//
// "到期"判定使用数据库中的 expires_at 与调用方给出的 now，不在进程内比较：这样即使
// 两个写入者在同一毫秒竞争，也只有一个能命中条件。
func (s projectExecutionStore) AcquireProjectExecutionLease(
	ctx context.Context,
	projectID domain.ID,
	owner string,
	now time.Time,
	expiresAt time.Time,
) (domain.ProjectExecutionLease, error) {
	if err := projectID.Validate(); err != nil {
		return domain.ProjectExecutionLease{}, err
	}
	normalizedOwner, err := domain.NormalizeProjectExecutionOwner(owner)
	if err != nil {
		return domain.ProjectExecutionLease{}, err
	}
	if !expiresAt.After(now) {
		return domain.ProjectExecutionLease{}, fmt.Errorf(
			"%w: 项目执行权到期时间必须晚于当前时间",
			storage.ErrInvalidData,
		)
	}
	nowMillis := toMillis(now)
	expiresMillis := toMillis(expiresAt)
	takeover, err := s.q.ExecContext(
		ctx,
		`UPDATE project_execution_leases
			SET owner = ?, epoch = epoch + 1, expires_at = ?, acquired_at = ?, updated_at = ?
			WHERE project_id = ? AND expires_at <= ?`,
		normalizedOwner,
		expiresMillis,
		nowMillis,
		nowMillis,
		toIDParam(projectID),
		nowMillis,
	)
	if err != nil {
		return domain.ProjectExecutionLease{}, wrapError("取得项目执行权", err)
	}
	affected, err := takeover.RowsAffected()
	if err != nil {
		return domain.ProjectExecutionLease{}, wrapError("统计项目执行权影响行数", err)
	}
	if affected == 0 {
		_, insertErr := s.q.ExecContext(
			ctx,
			`INSERT INTO project_execution_leases (`+leaseColumns+`)
				VALUES (?, ?, 1, ?, ?, ?)`,
			toIDParam(projectID),
			normalizedOwner,
			expiresMillis,
			nowMillis,
			nowMillis,
		)
		if insertErr != nil {
			if isConstraintConflict(insertErr) {
				return domain.ProjectExecutionLease{}, fmt.Errorf(
					"sqlite: 项目 %s 的执行权仍被其他持有者占用: %w: %w",
					projectID,
					storage.ErrConflict,
					insertErr,
				)
			}
			return domain.ProjectExecutionLease{}, wrapError("取得项目执行权", insertErr)
		}
	}
	lease, err := s.GetProjectExecutionLease(ctx, projectID)
	if err != nil {
		return domain.ProjectExecutionLease{}, err
	}
	return *lease, nil
}

// RenewProjectExecutionLease 以 owner + epoch 为条件延长执行权有效期。
func (s projectExecutionStore) RenewProjectExecutionLease(
	ctx context.Context,
	projectID domain.ID,
	owner string,
	epoch int64,
	now time.Time,
	expiresAt time.Time,
) error {
	if err := projectID.Validate(); err != nil {
		return err
	}
	if epoch <= 0 {
		return fmt.Errorf("%w: 执行权代数必须大于 0", storage.ErrInvalidData)
	}
	result, err := s.q.ExecContext(
		ctx,
		`UPDATE project_execution_leases
			SET expires_at = ?, updated_at = ?
			WHERE project_id = ? AND owner = ? AND epoch = ? AND expires_at > ?`,
		toMillis(expiresAt),
		toMillis(now),
		toIDParam(projectID),
		strings.TrimSpace(owner),
		epoch,
		toMillis(now),
	)
	if err != nil {
		return wrapError("续租项目执行权", err)
	}
	return ensureLeaseAffected(result, projectID, "续租")
}

// ReleaseProjectExecutionLease 以 owner + epoch 为条件释放执行权。
//
// 释放把有效期置为 now：行保留、epoch 不重置，下一次取得会在原 epoch 上递增，因此旧
// 持有者的迟到写入仍然被 epoch 条件拒绝。
func (s projectExecutionStore) ReleaseProjectExecutionLease(
	ctx context.Context,
	projectID domain.ID,
	owner string,
	epoch int64,
	now time.Time,
) error {
	if err := projectID.Validate(); err != nil {
		return err
	}
	if epoch <= 0 {
		return fmt.Errorf("%w: 执行权代数必须大于 0", storage.ErrInvalidData)
	}
	result, err := s.q.ExecContext(
		ctx,
		`UPDATE project_execution_leases
			SET expires_at = ?, updated_at = ?
			WHERE project_id = ? AND owner = ? AND epoch = ?`,
		toMillis(now),
		toMillis(now),
		toIDParam(projectID),
		strings.TrimSpace(owner),
		epoch,
	)
	if err != nil {
		return wrapError("释放项目执行权", err)
	}
	return ensureLeaseAffected(result, projectID, "释放")
}

// GetProjectExecutionLease 读取项目执行权。
func (s projectExecutionStore) GetProjectExecutionLease(
	ctx context.Context,
	projectID domain.ID,
) (*domain.ProjectExecutionLease, error) {
	if err := projectID.Validate(); err != nil {
		return nil, err
	}
	row := s.q.QueryRowContext(
		ctx,
		`SELECT `+leaseColumns+` FROM project_execution_leases WHERE project_id = ?`,
		toIDParam(projectID),
	)
	lease, err := scanProjectExecutionLease(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("项目执行权", projectID.String())
		}
		return nil, wrapError("读取项目执行权", err)
	}
	return lease, nil
}

// ListProjectExecutionLeases 返回指定持有者的全部执行权登记。
func (s projectExecutionStore) ListProjectExecutionLeases(
	ctx context.Context,
	owner string,
) ([]domain.ProjectExecutionLease, error) {
	normalizedOwner, err := domain.NormalizeProjectExecutionOwner(owner)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.QueryContext(
		ctx,
		`SELECT `+leaseColumns+` FROM project_execution_leases
			WHERE owner = ? ORDER BY project_id`,
		normalizedOwner,
	)
	if err != nil {
		return nil, wrapError("查询项目执行权", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	leases := make([]domain.ProjectExecutionLease, 0, 4)
	for rows.Next() {
		lease, scanErr := scanProjectExecutionLease(rows)
		if scanErr != nil {
			return nil, wrapError("读取项目执行权", scanErr)
		}
		leases = append(leases, *lease)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询项目执行权", err)
	}
	return leases, nil
}

// GetProjectExecutionState 读取项目执行意图。
func (s projectExecutionStore) GetProjectExecutionState(
	ctx context.Context,
	projectID domain.ID,
) (*storage.ProjectExecutionState, error) {
	if err := projectID.Validate(); err != nil {
		return nil, err
	}
	row := s.q.QueryRowContext(
		ctx,
		`SELECT `+projectExecutionStateColumns+` FROM project_execution_states WHERE project_id = ?`,
		toIDParam(projectID),
	)
	var (
		rawProjectID    string
		rawState        string
		stopRequestedAt sql.NullInt64
		updatedAt       int64
	)
	if err := row.Scan(&rawProjectID, &rawState, &stopRequestedAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("项目执行状态", projectID.String())
		}
		return nil, wrapError("读取项目执行状态", err)
	}
	return &storage.ProjectExecutionState{
		ProjectID: domain.ID(rawProjectID),
		// 未知状态原样保留：数据库不设取值白名单，读取层不映射为默认状态。
		State:           domain.ProjectExecutionState(rawState),
		StopRequestedAt: fromMillisPtr(stopRequestedAt),
		UpdatedAt:       fromMillis(updatedAt),
	}, nil
}

// SetProjectExecutionState 整体写入项目执行意图。
func (s projectExecutionStore) SetProjectExecutionState(
	ctx context.Context,
	state storage.ProjectExecutionState,
) error {
	if err := state.ProjectID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(string(state.State)) == "" {
		return fmt.Errorf("%w: 项目执行状态不能为空", storage.ErrInvalidData)
	}
	if state.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: 项目执行状态更新时间不能为空", storage.ErrInvalidData)
	}
	_, err := s.q.ExecContext(
		ctx,
		`INSERT INTO project_execution_states (`+projectExecutionStateColumns+`)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(project_id) DO UPDATE SET
				state = excluded.state,
				stop_requested_at = excluded.stop_requested_at,
				updated_at = excluded.updated_at`,
		toIDParam(state.ProjectID),
		string(state.State),
		toMillisPtr(state.StopRequestedAt),
		toMillis(state.UpdatedAt),
	)
	if err != nil {
		return wrapError("写入项目执行状态", err)
	}
	return nil
}

// ListProjectTaskFacts 返回项目内未逻辑删除的正式任务与所属需求。
//
// 两次只读查询（任务按项目筛选、需求按项目列出）后在内存中按需求标识合并：查询条件与
// 其余列表接口共用同一套筛选构造，不新增第二套过滤规则。顺序由领域层排序规则决定。
func (s projectExecutionStore) ListProjectTaskFacts(
	ctx context.Context,
	projectID domain.ID,
) ([]storage.ProjectTaskFact, error) {
	if err := projectID.Validate(); err != nil {
		return nil, err
	}
	tasks, err := taskStore{q: s.q}.ListTasks(ctx, storage.TaskFilter{ProjectID: projectID.String()})
	if err != nil {
		return nil, err
	}
	requirements, err := requirementStore{q: s.q}.ListRequirements(
		ctx,
		storage.RequirementFilter{ProjectID: projectID.String()},
	)
	if err != nil {
		return nil, err
	}
	index := make(map[domain.ID]domain.Requirement, len(requirements))
	for _, requirement := range requirements {
		index[requirement.ID] = requirement
	}
	facts := make([]storage.ProjectTaskFact, 0, len(tasks))
	for _, task := range tasks {
		requirement, ok := index[task.RequirementID]
		if !ok {
			// 需求已被逻辑删除或不属于该项目：任务不再出现在项目视图里。
			continue
		}
		facts = append(facts, storage.ProjectTaskFact{Requirement: requirement, Task: task})
	}
	return facts, nil
}

// scanProjectExecutionLease 扫描一行执行权记录。
func scanProjectExecutionLease(row leaseRowScanner) (*domain.ProjectExecutionLease, error) {
	var (
		rawProjectID string
		owner        string
		epoch        int64
		expiresAt    int64
		acquiredAt   int64
		updatedAt    int64
	)
	if err := row.Scan(
		&rawProjectID,
		&owner,
		&epoch,
		&expiresAt,
		&acquiredAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	return &domain.ProjectExecutionLease{
		ProjectID:  domain.ID(rawProjectID),
		Owner:      owner,
		Epoch:      epoch,
		ExpiresAt:  fromMillis(expiresAt),
		AcquiredAt: fromMillis(acquiredAt),
		UpdatedAt:  fromMillis(updatedAt),
	}, nil
}

// ensureLeaseAffected 确认带条件的执行权写入命中记录。
//
// 影响行数为 0 只说明条件不成立：执行权可能已经换人、已经过期，或者记录不存在。三种
// 情况都按冲突返回，调用方必须停止继续领取任务并重新读取现场。
func ensureLeaseAffected(result sql.Result, projectID domain.ID, operation string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapError("统计项目执行权影响行数", err)
	}
	if affected == 0 {
		return fmt.Errorf(
			"sqlite: %s项目 %s 的执行权失败，执行权已变更或已过期: %w",
			operation,
			projectID,
			storage.ErrConflict,
		)
	}
	return nil
}

// isConstraintConflict 判断错误是否为主键或唯一约束冲突。
//
// 取得执行权时插入失败只有一种业务含义：同一项目已存在未到期的执行权。这里按存储层
// 统一的错误分类判断，不解析驱动返回的文本。
func isConstraintConflict(err error) bool {
	return errors.Is(classify(err), storage.ErrConflict)
}
