package sqlite

import (
	"context"
	"encoding/json"

	"github.com/lingting/agentflow/internal/domain"
)

// validationGenerationValue 返回写入数据库的校验代次。
//
// 代次必须大于 0：数据库用 CHECK 约束保证取值范围，未显式初始化的实体按首代处理。
func validationGenerationValue(generation int64) int64 {
	if generation <= 0 {
		return 1
	}
	return generation
}

// validationStatusValue 返回写入数据库的校验状态文本。
//
// 未知取值原样写入：数据库不声明枚举白名单，读取层必须保留未知状态，
// 这里不做任何映射或回落。
func validationStatusValue(status domain.ValidationStatus) string {
	return status.String()
}

// validationDetailsJSON 把校验详情编码为 JSON 文本，空详情写入 NULL。
func validationDetailsJSON(details *domain.ValidationDetails) any {
	if details == nil {
		return nil
	}
	data, err := json.Marshal(details)
	if err != nil {
		// 详情只包含本程序构造的结构化数据，编码失败说明代码缺陷；
		// 此时写入 NULL 而不是让整个更新失败，总体状态与摘要仍然可用。
		return nil
	}
	return string(data)
}

// decodeValidationDetails 解析校验详情 JSON。
//
// 解析失败时返回 nil，调用方按"详情不可用"处理：总体状态与摘要仍然可信，
// 不能因为详情损坏就把状态当成正常。
func decodeValidationDetails(raw string) *domain.ValidationDetails {
	if raw == "" {
		return nil
	}
	var details domain.ValidationDetails
	if err := json.Unmarshal([]byte(raw), &details); err != nil {
		return nil
	}
	return &details
}

// UpdateExecutionHostValidation 以校验代次为条件写入校验状态与结果。
//
// 条件更新是防止旧校验覆盖新配置的关键：只有数据库中的代次仍等于本次校验代次时
// 才写入，否则返回 false 且不改动任何字段。该更新不涉及连接目标与认证字段，
// 因此不会与用户修改配置的写入互相覆盖。
func (s executionHostStore) UpdateExecutionHostValidation(
	ctx context.Context,
	host domain.ExecutionHost,
	generation int64,
) (bool, error) {
	if err := host.ID.Validate(); err != nil {
		return false, err
	}
	const statement = `
UPDATE execution_hosts SET
	validation_status = ?, validation_message = ?, validation_details_json = ?,
	validation_started_at = ?, validation_completed_at = ?, updated_at = ?
WHERE id = ? AND validation_generation = ? AND deleted_at IS NULL`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		validationStatusValue(host.ValidationStatus),
		nullIfBlank(host.ValidationMessage),
		validationDetailsJSON(host.ValidationDetails),
		toMillisPtr(host.ValidationStartedAt),
		toMillisPtr(host.ValidationCompletedAt),
		toMillis(host.UpdatedAt),
		toIDParam(host.ID),
		generation,
	)
	if err != nil {
		return false, wrapError("更新执行节点校验结果", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, wrapError("更新执行节点校验结果", err)
	}
	return affected > 0, nil
}

// ListExecutionHostsByValidationStatus 返回指定校验状态的全部未删除执行节点。
//
// 服务启动时用它找出仍停留在 checking 的记录并重新调度校验。
func (s executionHostStore) ListExecutionHostsByValidationStatus(
	ctx context.Context,
	status domain.ValidationStatus,
) ([]domain.ExecutionHost, error) {
	statement := `SELECT ` + executionHostColumns + ` FROM execution_hosts
		WHERE deleted_at IS NULL AND validation_status = ? ORDER BY created_at, rowid`
	rows, err := s.q.QueryContext(ctx, statement, validationStatusValue(status))
	if err != nil {
		return nil, wrapError("按校验状态查询执行节点", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	hosts := make([]domain.ExecutionHost, 0, 4)
	for rows.Next() {
		host, scanErr := scanExecutionHost(rows)
		if scanErr != nil {
			return nil, wrapError("读取执行节点", scanErr)
		}
		hosts = append(hosts, *host)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("按校验状态查询执行节点", err)
	}
	return hosts, nil
}
