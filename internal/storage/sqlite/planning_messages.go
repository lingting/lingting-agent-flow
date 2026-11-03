package sqlite

import (
	"context"
	"database/sql"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// planningMessageColumns 是规划消息表的完整列顺序。
const planningMessageColumns = `id, planning_session_id, sequence, sender_type, role_id,
	content, created_at`

// planningMessageInsertCount 是写入规划消息时的参数个数。
const planningMessageInsertCount = 7

// planningMessageStore 实现 storage.PlanningSessionStore 的消息部分。
type planningMessageStore struct {
	q queryer
}

// AppendPlanningMessage 在一个事务内分配消息序号并追加一条规划消息。
//
// 序号分配与插入共享同一个事务：事务以 immediate 语义开启（DSN 的 _txlock），因此
// 并发追加会被串行化，不会出现两个调用读到同一个"最大序号"。唯一索引
// idx_planning_messages_sequence 仍然保留，作为写入路径被绕过时的最后防线。
func (s planningMessageStore) AppendPlanningMessage(
	ctx context.Context,
	request storage.AppendPlanningMessageRequest,
) (domain.PlanningMessage, error) {
	if err := request.PlanningSessionID.Validate(); err != nil {
		return domain.PlanningMessage{}, err
	}
	withTx, ok := s.q.(transactioner)
	if !ok {
		// 已在事务内：直接复用当前事务，原子性由调用方的事务边界保证。
		return s.append(ctx, request)
	}
	var appended domain.PlanningMessage
	err := withTx.runInTx(ctx, func(tx queryer) error {
		message, err := planningMessageStore{q: tx}.append(ctx, request)
		if err != nil {
			return err
		}
		appended = message
		return nil
	})
	if err != nil {
		return domain.PlanningMessage{}, err
	}
	return appended, nil
}

// append 在已开启的事务内分配序号并写入消息。
func (s planningMessageStore) append(
	ctx context.Context,
	request storage.AppendPlanningMessageRequest,
) (domain.PlanningMessage, error) {
	sessions := planningSessionStore{q: s.q}
	if err := sessions.ensureSessionExists(ctx, request.PlanningSessionID); err != nil {
		return domain.PlanningMessage{}, err
	}
	sequence, err := s.nextSequence(ctx, request.PlanningSessionID)
	if err != nil {
		return domain.PlanningMessage{}, err
	}
	message, err := domain.NewPlanningMessage(
		request.PlanningSessionID,
		sequence,
		request.SenderType,
		request.RoleID,
		request.Content,
		request.At,
	)
	if err != nil {
		return domain.PlanningMessage{}, err
	}
	statement := `INSERT INTO planning_messages (` + planningMessageColumns + `)
		VALUES (` + placeholders(planningMessageInsertCount) + `)`
	_, err = s.q.ExecContext(
		ctx,
		statement,
		toIDParam(message.ID),
		toIDParam(message.PlanningSessionID),
		message.Sequence,
		string(message.SenderType),
		toIDPtrParam(message.RoleID),
		message.Content,
		toMillis(message.CreatedAt),
	)
	if err != nil {
		return domain.PlanningMessage{}, wrapError("追加规划消息", err)
	}
	return message, nil
}

// nextSequence 返回会话下一个可用的消息序号。
//
// 它只在写入事务内使用：事务外的"先查最大值再插入"会让并发追加拿到相同序号。
func (s planningMessageStore) nextSequence(ctx context.Context, sessionID domain.ID) (int, error) {
	const statement = `SELECT COALESCE(MAX(sequence), 0) + 1 FROM planning_messages
		WHERE planning_session_id = ?`
	var next int
	if err := s.q.QueryRowContext(ctx, statement, toIDParam(sessionID)).Scan(&next); err != nil {
		return 0, wrapError("分配规划消息序号", err)
	}
	return next, nil
}

// ListPlanningMessages 按序号升序返回会话的全部消息。
//
// 顺序来自持久化的序号而不是插入顺序或时间戳：同一毫秒内的消息必须仍然可复现。
func (s planningMessageStore) ListPlanningMessages(
	ctx context.Context,
	sessionID domain.ID,
) ([]domain.PlanningMessage, error) {
	if err := sessionID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + planningMessageColumns + ` FROM planning_messages
		WHERE planning_session_id = ?
		ORDER BY sequence ASC`
	rows, err := s.q.QueryContext(ctx, statement, toIDParam(sessionID))
	if err != nil {
		return nil, wrapError("查询规划消息列表", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	messages := make([]domain.PlanningMessage, 0, 16)
	for rows.Next() {
		message, scanErr := scanPlanningMessage(rows)
		if scanErr != nil {
			return nil, wrapError("读取规划消息", scanErr)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询规划消息列表", err)
	}
	return messages, nil
}

// planningMessageScanner 抽象单行与多行查询结果。
type planningMessageScanner interface {
	Scan(dest ...any) error
}

// scanPlanningMessage 从一行结果还原规划消息。
//
// 发送方类型原样读回：数据库不限定取值集合，未知取值必须保留而不是被映射为默认
// 发送方。正文不裁剪，写入与读回保持同一份文本。
func scanPlanningMessage(row planningMessageScanner) (domain.PlanningMessage, error) {
	var (
		id         string
		sessionID  string
		sequence   int
		senderType string
		roleID     sql.NullString
		content    string
		createdAt  int64
	)
	if err := row.Scan(
		&id,
		&sessionID,
		&sequence,
		&senderType,
		&roleID,
		&content,
		&createdAt,
	); err != nil {
		return domain.PlanningMessage{}, err
	}
	return domain.PlanningMessage{
		ID:                domain.ID(id),
		PlanningSessionID: domain.ID(sessionID),
		Sequence:          sequence,
		SenderType:        domain.PlanningSenderType(senderType),
		RoleID:            fromIDPtr(roleID),
		Content:           content,
		CreatedAt:         fromMillis(createdAt),
	}, nil
}
