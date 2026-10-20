package sqlite

import (
	"context"
	"strings"
)

// placeholders 返回 count 个以逗号分隔的 SQL 占位符。
func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.Repeat("?, ", count-1) + "?"
}

// appendConditions 把条件列表拼接到查询语句上。
//
// 条件内容由调用方使用占位符构造，用户输入只通过参数绑定传入。
func appendConditions(builder *strings.Builder, conditions []string) {
	if len(conditions) == 0 {
		return
	}
	builder.WriteString(` WHERE `)
	builder.WriteString(strings.Join(conditions, ` AND `))
}

// transactioner 表示能够自行开启事务的连接池。
//
// sqlPool 满足该接口；*sql.Tx 不满足，因此事务内的仓储会直接复用当前事务。
type transactioner interface {
	queryer
	runInTx(ctx context.Context, fn func(tx queryer) error) error
}

// appendPaging 为查询附加分页子句并返回追加后的参数列表。
//
// 分页参数必须与筛选参数分开处理：先追加全部筛选参数，再追加 LIMIT / OFFSET 参数，
// 顺序必须与占位符在语句中出现的顺序一致。
func appendPaging(
	builder *strings.Builder,
	args []any,
	limit, offset int,
) []any {
	if limit > 0 {
		builder.WriteString(` LIMIT ?`)
		args = append(args, limit)
	}
	if offset > 0 {
		// SQLite 要求 LIMIT 与 OFFSET 成对出现，无上限时用 -1 表示不限。
		if limit <= 0 {
			builder.WriteString(` LIMIT -1`)
		}
		builder.WriteString(` OFFSET ?`)
		args = append(args, offset)
	}
	return args
}
