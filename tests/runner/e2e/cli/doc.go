// Package cli 是 V3 R5 批次 B1 的用例包：把 tests/e2e/cli 的既有端到端用例迁到统一
// 测试执行器。
//
// 迁移写法沿用 R4.5 试点冻结的约定（tests/runner/integration/local/scheduler/doc.go）：
// 一条既有 func Test* 对应一条 Case，ID 为 "e2e-cli/<原用例名 kebab-case>"，用例主体写成
// func run*(t *testrunner.Context)，断言与注释保持原样，t.Parallel() 改由 Case.Parallel
// 声明。
//
// 这批用例在进程内构造应用实例并直接执行 cobra 命令树，不启动子进程：CLI 的配置、数据库
// 与工作区都落在用例范围内——数据库路径只从注入的 sqlite-db 句柄读取，工作区路径落在用例
// 工作空间内。需要真实监听的用例（SSH 探测、主机密钥握手）自己向操作系统申请 127.0.0.1
// 的临时端口，编排进程不需要预先分配端口，因此不声明 local-port。
//
// 与源用例的唯一断言语义差异见 cases_cli.go 的 runAgentHelp：它原来要求 agent 帮助里出现
// 已被移除的 check 子命令，迁移时只保留仍然存在的 run 断言。
package cli
