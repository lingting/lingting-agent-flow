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
