package sqlite

import (
	"context"

	"github.com/lingting/agentflow/internal/storage"
)

// queryStatusCounts 执行 `SELECT <状态列>, COUNT(*) ... GROUP BY <状态列>` 形式的分组统计。
//
// statement 必须正好返回两列：第一列是状态原值，第二列是该状态的记录条数。状态取值
// 原样作为结果的键，读取层不做归一化：不认识的取值既不能丢弃，也不能并入"其他"分组。
func queryStatusCounts(
	ctx context.Context,
	q queryer,
	operation string,
	statement string,
) (storage.StatusCounts, error) {
	rows, err := q.QueryContext(ctx, statement)
	if err != nil {
		return nil, wrapError(operation, err)
	}
	defer func() {
		_ = rows.Close()
	}()
	counts := make(storage.StatusCounts, 4)
	for rows.Next() {
		var (
			status string
			count  int
		)
		if scanErr := rows.Scan(&status, &count); scanErr != nil {
			return nil, wrapError(operation, scanErr)
		}
		counts[status] += count
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError(operation, err)
	}
	return counts, nil
}
