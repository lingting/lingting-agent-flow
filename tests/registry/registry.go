// Package registry 聚合全部用例包。
//
// agentflow-test 通过空导入本包完成用例登记；迁移期新增用例包（R5 集成、R6 前端）只需在
// 这里追加一行导入，不在命令行入口里散落多个空导入。
package registry

import (
	_ "github.com/lingting/agentflow/tests/concurrencycheck"
	_ "github.com/lingting/agentflow/tests/e2e/cli"
	_ "github.com/lingting/agentflow/tests/frontend"
	_ "github.com/lingting/agentflow/tests/integration/local/codesaver"
	_ "github.com/lingting/agentflow/tests/integration/local/environment"
	_ "github.com/lingting/agentflow/tests/integration/local/execenv"
	_ "github.com/lingting/agentflow/tests/integration/local/scheduler"
	_ "github.com/lingting/agentflow/tests/integration/local/session"
	_ "github.com/lingting/agentflow/tests/integration/local/source"
	_ "github.com/lingting/agentflow/tests/integration/local/ssh"
	_ "github.com/lingting/agentflow/tests/integration/local/validation"
	_ "github.com/lingting/agentflow/tests/integration/local/workflow"
	_ "github.com/lingting/agentflow/tests/integration/remote/agent"
	_ "github.com/lingting/agentflow/tests/integration/remote/codesaver"
	_ "github.com/lingting/agentflow/tests/integration/remote/docker"
	_ "github.com/lingting/agentflow/tests/integration/remote/planning"
	_ "github.com/lingting/agentflow/tests/integration/remote/workflow"
	_ "github.com/lingting/agentflow/tests/integration/ssh"
	_ "github.com/lingting/agentflow/tests/pilot"
	_ "github.com/lingting/agentflow/tests/resourcecheck"
	_ "github.com/lingting/agentflow/tests/selfcheck"
	_ "github.com/lingting/agentflow/tests/unit/app/planningagent"
	_ "github.com/lingting/agentflow/tests/unit/app/planninganalysis"
	_ "github.com/lingting/agentflow/tests/unit/cigate"
	_ "github.com/lingting/agentflow/tests/unit/environment"
	_ "github.com/lingting/agentflow/tests/unit/execenv"
	_ "github.com/lingting/agentflow/tests/unit/image"
	_ "github.com/lingting/agentflow/tests/unit/lifecycle"
	_ "github.com/lingting/agentflow/tests/unit/server"
	_ "github.com/lingting/agentflow/tests/unit/source"
	_ "github.com/lingting/agentflow/tests/unit/testkit"
	_ "github.com/lingting/agentflow/tests/unit/testrunner"
)
