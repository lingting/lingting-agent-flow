package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// planningRevisionColumns 是规划版本表的完整列顺序。
const planningRevisionColumns = `id, planning_session_id, revision_number,
	requirement_analysis_json, implementation_plan_json, validation_result_json, created_at`

// planningRevisionInsertCount 是写入规划版本时的参数个数。
const planningRevisionInsertCount = 7

// planningRevisionStore 实现 storage.PlanningSessionStore 的方案版本部分。
type planningRevisionStore struct {
	q queryer
}

// AppendPlanningRevision 在一个事务内分配版本号并追加一个方案版本。
//
// 版本号分配与插入共享同一个事务：并发追加不会拿到相同版本号。唯一索引
// idx_planning_revisions_number 仍然保留，作为写入路径被绕过时的最后防线。
func (s planningRevisionStore) AppendPlanningRevision(
	ctx context.Context,
	request storage.AppendPlanningRevisionRequest,
) (domain.PlanningRevision, error) {
	if err := request.PlanningSessionID.Validate(); err != nil {
		return domain.PlanningRevision{}, err
	}
	withTx, ok := s.q.(transactioner)
	if !ok {
		// 已在事务内：直接复用当前事务，原子性由调用方的事务边界保证。
		return s.append(ctx, request)
	}
	var appended domain.PlanningRevision
	err := withTx.runInTx(ctx, func(tx queryer) error {
		revision, err := planningRevisionStore{q: tx}.append(ctx, request)
		if err != nil {
			return err
		}
		appended = revision
		return nil
	})
	if err != nil {
		return domain.PlanningRevision{}, err
	}
	return appended, nil
}

// append 在已开启的事务内分配版本号并写入快照。
func (s planningRevisionStore) append(
	ctx context.Context,
	request storage.AppendPlanningRevisionRequest,
) (domain.PlanningRevision, error) {
	sessions := planningSessionStore{q: s.q}
	if err := sessions.ensureSessionExists(ctx, request.PlanningSessionID); err != nil {
		return domain.PlanningRevision{}, err
	}
	number, err := s.nextRevisionNumber(ctx, request.PlanningSessionID)
	if err != nil {
		return domain.PlanningRevision{}, err
	}
	revision, err := domain.NewPlanningRevision(
		request.PlanningSessionID,
		number,
		request.RequirementAnalysisJSON,
		request.ImplementationPlanJSON,
		request.ValidationResultJSON,
		request.At,
	)
	if err != nil {
		return domain.PlanningRevision{}, err
	}
	statement := `INSERT INTO planning_revisions (` + planningRevisionColumns + `)
		VALUES (` + placeholders(planningRevisionInsertCount) + `)`
	_, err = s.q.ExecContext(
		ctx,
		statement,
		toIDParam(revision.ID),
		toIDParam(revision.PlanningSessionID),
		revision.RevisionNumber,
		string(revision.RequirementAnalysisJSON),
		string(revision.ImplementationPlanJSON),
		nullIfBlank(string(revision.ValidationResultJSON)),
		toMillis(revision.CreatedAt),
	)
	if err != nil {
		return domain.PlanningRevision{}, wrapError("追加规划版本", err)
	}
	return revision, nil
}

// nextRevisionNumber 返回会话下一个可用的版本号。
//
// 它只在写入事务内使用：事务外的"先查最大值再插入"会让并发追加拿到相同版本号。
func (s planningRevisionStore) nextRevisionNumber(
	ctx context.Context,
	sessionID domain.ID,
) (int, error) {
	const statement = `SELECT COALESCE(MAX(revision_number), 0) + 1 FROM planning_revisions
		WHERE planning_session_id = ?`
	var next int
	if err := s.q.QueryRowContext(ctx, statement, toIDParam(sessionID)).Scan(&next); err != nil {
		return 0, wrapError("分配规划版本号", err)
	}
	return next, nil
}

// ListPlanningRevisions 按版本号升序返回会话的全部方案版本。
//
// 历史版本只追加不覆盖：后续轮次产生的快照不会改写已生成的版本。
func (s planningRevisionStore) ListPlanningRevisions(
	ctx context.Context,
	sessionID domain.ID,
) ([]domain.PlanningRevision, error) {
	if err := sessionID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + planningRevisionColumns + ` FROM planning_revisions
		WHERE planning_session_id = ?
		ORDER BY revision_number ASC`
	rows, err := s.q.QueryContext(ctx, statement, toIDParam(sessionID))
	if err != nil {
		return nil, wrapError("查询规划版本列表", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	revisions := make([]domain.PlanningRevision, 0, 8)
	for rows.Next() {
		revision, scanErr := scanPlanningRevision(rows)
		if scanErr != nil {
			return nil, wrapError("读取规划版本", scanErr)
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询规划版本列表", err)
	}
	return revisions, nil
}

// planningRevisionScanner 抽象单行与多行查询结果。
type planningRevisionScanner interface {
	Scan(dest ...any) error
}

// scanPlanningRevision 从一行结果还原规划版本。
//
// JSON 快照按原始文本读回，不在这里解析内部字段：内部结构由后续里程碑定义，读取
// 路径只负责把已保存的快照原样交出。
func scanPlanningRevision(row planningRevisionScanner) (domain.PlanningRevision, error) {
	var (
		id         string
		sessionID  string
		number     int
		analysis   string
		plan       string
		validation sql.NullString
		createdAt  int64
	)
	if err := row.Scan(
		&id,
		&sessionID,
		&number,
		&analysis,
		&plan,
		&validation,
		&createdAt,
	); err != nil {
		return domain.PlanningRevision{}, err
	}
	var validationJSON json.RawMessage
	if validation.Valid {
		validationJSON = json.RawMessage(validation.String)
	}
	return domain.PlanningRevision{
		ID:                      domain.ID(id),
		PlanningSessionID:       domain.ID(sessionID),
		RevisionNumber:          number,
		RequirementAnalysisJSON: json.RawMessage(analysis),
		ImplementationPlanJSON:  json.RawMessage(plan),
		ValidationResultJSON:    validationJSON,
		CreatedAt:               fromMillis(createdAt),
	}, nil
}
