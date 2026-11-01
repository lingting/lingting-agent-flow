// Command agentflow-ci 提供 GitHub Actions 门禁使用的判定工具。
//
// 当前只包含一个子命令：
//
//	real-tests-gate   判定本次运行是否获准执行真实外部依赖测试，并写出 CI 输出与摘要
//
// 判定规则集中在 internal/cigate，工作流只负责提供事件上下文，不把授权决策分散到
// YAML 表达式里。该工具只做只读判定：不连接真实测试设备，也不输出任何凭据。
package main

import (
	"os"

	"github.com/lingting/agentflow/internal/cigate"
)

// main 执行 CI 判定工具并以退出码结束进程。
func main() {
	os.Exit(cigate.Run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}
