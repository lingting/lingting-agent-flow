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

// repairCycleColumns 是执行周期表的完整列顺序。
const repairCycleColumns = `id, task_id, step_id, sequence, max_attempts,
	opened_by, opened_at, closed_at`

// repairCycleInsertCount 是写入周期时的参数个数。
const repairCycleInsertCount = 8

// repairCycleStore 实现 storage.RepairCycleStore。
//
// 周期只追加不覆盖：结束周期只写 closed_at，历史登记一律保留，因此"这一步执行过
// 几次周期、每次的上限是多少"可以事后追溯。
type repairCycleStore struct {
	q queryer
}

// StartRepairCycle 在一个事务内分配周期序号并写入新的活动周期。
//
// 序号分配与插入共享同一个事务（连接以 immediate 语义开启，见 DSN 的 _txlock），
// 因此并发开启不会拿到相同序号；唯一索引仍然是写入路径被绕过时的最后防线。
// 同一步骤已有活动周期时由部分唯一索引拒绝，返回 ErrConflict。
func (s repairCycleStore) StartRepairCycle(
	ctx context.Context,
	request storage.StartRepairCycleRequest,
) (domain.RepairCycle, error) {
	if err := request.TaskID.Validate(); err != nil {
		return domain.RepairCycle{}, err
	}
	if err := request.StepID.Validate(); err != nil {
		return domain.RepairCycle{}, err
	}
	at := request.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	withTx, ok := s.q.(transactioner)
	if !ok {
		// 已在事务内：直接复用当前事务，调用方负责事务边界。
		return s.assignAndInsertCycle(ctx, request, at)
	}
	var started domain.RepairCycle
	err := withTx.runInTx(ctx, func(tx queryer) error {
		inner := repairCycleStore{q: tx}
		cycle, err := inner.assignAndInsertCycle(ctx, request, at)
		if err != nil {
			return err
		}
		started = cycle
		return nil
	})
	if err != nil {
		return domain.RepairCycle{}, err
	}
	return started, nil
}

// assignAndInsertCycle 分配周期序号、构造周期并写入。
func (s repairCycleStore) assignAndInsertCycle(
	ctx context.Context,
	request storage.StartRepairCycleRequest,
	at time.Time,
) (domain.RepairCycle, error) {
	if err := s.ensureRepairCycleScope(ctx, request.TaskID, request.StepID); err != nil {
		return domain.RepairCycle{}, err
	}
	sequence, err := s.nextRepairCycleSequence(ctx, request.StepID)
	if err != nil {
		return domain.RepairCycle{}, err
	}
	cycle, err := domain.NewRepairCycle(
		request.TaskID,
		request.StepID,
		sequence,
		request.MaxAttempts,
		request.OpenedBy,
		at,
	)
	if err != nil {
		return domain.RepairCycle{}, err
	}
	statement := `INSERT INTO repair_cycles (` + repairCycleColumns + `)
		VALUES (` + placeholders(repairCycleInsertCount) + `)`
	if _, err := s.q.ExecContext(
		ctx,
		statement,
		toIDParam(cycle.ID),
		toIDParam(cycle.TaskID),
		toIDParam(cycle.StepID),
		cycle.Sequence,
		cycle.MaxAttempts,
		cycle.OpenedBy,
		toMillis(cycle.OpenedAt),
		toMillisPtr(cycle.ClosedAt),
	); err != nil {
		return domain.RepairCycle{}, wrapError("写入执行周期", err)
	}
	return cycle, nil
}

// ensureRepairCycleScope 校验周期的任务归属与步骤归属一致。
//
// 步骤记录已不存在时放行：数据库不声明引用完整性，允许保留指向已删除步骤的历史
// 标识；但只要步骤存在，任务归属就必须一致。
func (s repairCycleStore) ensureRepairCycleScope(
	ctx context.Context,
	taskID, stepID domain.ID,
) error {
	const statement = `SELECT task_id FROM steps WHERE id = ?`
	var stored string
	err := s.q.QueryRowContext(ctx, statement, toIDParam(stepID)).Scan(&stored)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return wrapError("读取步骤归属", err)
	}
	if stored != taskID.String() {
		return fmt.Errorf(
			"sqlite: 执行周期 %s 的任务 %s 与步骤所属任务 %s 不一致: %w",
			stepID,
			taskID,
			stored,
			storage.ErrInvalidData,
		)
	}
	return nil
}

// nextRepairCycleSequence 返回该步骤下一个可用的周期序号。
func (s repairCycleStore) nextRepairCycleSequence(
	ctx context.Context,
	stepID domain.ID,
) (int, error) {
	const statement = `SELECT COALESCE(MAX(sequence), 0) FROM repair_cycles WHERE step_id = ?`
	var latest int
	if err := s.q.QueryRowContext(ctx, statement, toIDParam(stepID)).Scan(&latest); err != nil {
		return 0, wrapError("查询周期序号", err)
	}
	return latest + 1, nil
}

// CloseRepairCycle 结束一个活动周期。
//
// 只写 closed_at：重复结束是幂等的，已结束的周期不会被重新打开，也不会改写
// 上限快照与开启时间。
func (s repairCycleStore) CloseRepairCycle(
	ctx context.Context,
	id domain.ID,
	at time.Time,
) error {
	if err := id.Validate(); err != nil {
		return err
	}
	closedAt := at
	if closedAt.IsZero() {
		closedAt = time.Now().UTC()
	}
	const statement = `UPDATE repair_cycles SET closed_at = ?
		WHERE id = ? AND closed_at IS NULL`
	result, err := s.q.ExecContext(ctx, statement, toMillis(closedAt), toIDParam(id))
	if err != nil {
		return wrapError("结束执行周期", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapError("结束执行周期", err)
	}
	if affected > 0 {
		return nil
	}
	// 没有更新到行有两种可能：记录不存在，或已经结束。前者是错误，后者幂等。
	if _, readErr := s.getRepairCycle(ctx, id); readErr != nil {
		return readErr
	}
	return nil
}

// GetActiveRepairCycle 返回步骤当前的活动周期。
func (s repairCycleStore) GetActiveRepairCycle(
	ctx context.Context,
	stepID domain.ID,
) (*domain.RepairCycle, error) {
	if err := stepID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + repairCycleColumns + ` FROM repair_cycles
		WHERE step_id = ? AND closed_at IS NULL LIMIT 1`
	cycle, err := scanRepairCycle(s.q.QueryRowContext(ctx, statement, toIDParam(stepID)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("执行周期", stepID.String())
		}
		return nil, wrapError("读取执行周期", err)
	}
	return cycle, nil
}

// ListRepairCyclesByStep 按周期序号返回步骤的全部周期登记。
func (s repairCycleStore) ListRepairCyclesByStep(
	ctx context.Context,
	stepID domain.ID,
) ([]domain.RepairCycle, error) {
	if err := stepID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + repairCycleColumns + ` FROM repair_cycles
		WHERE step_id = ? ORDER BY sequence`
	rows, err := s.q.QueryContext(ctx, statement, toIDParam(stepID))
	if err != nil {
		return nil, wrapError("查询执行周期", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	cycles := make([]domain.RepairCycle, 0, 4)
	for rows.Next() {
		cycle, scanErr := scanRepairCycle(rows)
		if scanErr != nil {
			return nil, wrapError("读取执行周期", scanErr)
		}
		cycles = append(cycles, *cycle)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询执行周期", err)
	}
	return cycles, nil
}

// getRepairCycle 按主键读取周期登记。
func (s repairCycleStore) getRepairCycle(
	ctx context.Context,
	id domain.ID,
) (*domain.RepairCycle, error) {
	const statement = `SELECT ` + repairCycleColumns + ` FROM repair_cycles WHERE id = ?`
	cycle, err := scanRepairCycle(s.q.QueryRowContext(ctx, statement, toIDParam(id)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("执行周期", id.String())
		}
		return nil, wrapError("读取执行周期", err)
	}
	return cycle, nil
}

// scanRepairCycle 从一行结果读取执行周期。
func scanRepairCycle(row scanner) (*domain.RepairCycle, error) {
	var (
		cycle    domain.RepairCycle
		rawID    string
		taskID   string
		stepID   string
		openedBy string
		openedAt int64
		closedAt sql.NullInt64
	)
	if err := row.Scan(
		&rawID,
		&taskID,
		&stepID,
		&cycle.Sequence,
		&cycle.MaxAttempts,
		&openedBy,
		&openedAt,
		&closedAt,
	); err != nil {
		return nil, err
	}
	cycle.ID = domain.ID(strings.TrimSpace(rawID))
	cycle.TaskID = domain.ID(taskID)
	cycle.StepID = domain.ID(stepID)
	cycle.OpenedBy = openedBy
	cycle.OpenedAt = fromMillis(openedAt)
	cycle.ClosedAt = fromMillisPtr(closedAt)
	return &cycle, nil
}
