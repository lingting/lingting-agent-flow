// Command agentflow-test-inventory 盘点现有测试用例与资源依赖。
//
// 它产出测试执行器重构（V3 R0）的两份冻结资产：
//
//	.steps/V3/R0/legacy-cases.manifest.json   全部既有用例清单（迁移覆盖对账基准）
//	.steps/V3/R0/case-resource-matrix.json    资源需求矩阵（调度器的输入）
//
// 工具只读仓库已跟踪文件（git ls-files + Go AST），不执行任何测试，也不连接外部设备。
package main

import (
	"os"

	"github.com/lingting/agentflow/internal/caseinventory"
)

// main 执行盘点并以退出码结束进程。
func main() {
	os.Exit(caseinventory.Run(os.Args[1:], os.Stdout, os.Stderr))
}
