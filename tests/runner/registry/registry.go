// Package registry 聚合全部用例包。
//
// agentflow-test 通过空导入本包完成用例登记；迁移期新增用例包（R5 集成、R6 前端）只需在
// 这里追加一行导入，不在命令行入口里散落多个空导入。
package registry

import (
	_ "github.com/lingting/agentflow/tests/runner/resourcecheck"
	_ "github.com/lingting/agentflow/tests/runner/selfcheck"
)
