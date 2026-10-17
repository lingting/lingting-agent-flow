// Package agent 定义与具体 Agent 实现无关的统一执行接口。
//
// 业务层只依赖本包的 Runner、RunRequest、RunResult 与 Event，
// 不感知具体 Agent 的命令行参数、子进程或协议字段。
package agent

import "context"

// Runner 是统一的 Agent 执行接口。
//
// emit 必须被同步调用且应快速返回：实现方不得在回调中执行模型调用或阻塞
// 子进程输出读取。回调为 nil 时实现方应忽略事件。
type Runner interface {
	Run(ctx context.Context, request RunRequest, emit func(Event)) (RunResult, error)
}
