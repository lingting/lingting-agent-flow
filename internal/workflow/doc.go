// Package workflow 是 AgentFlow 的任务执行控制层。
//
// 职责边界：
//   - 只有本包能依据持久化状态、验证结果与代码保存结果推进 Task 到最终状态；
//   - Pi 不更新数据库中的任务状态，也不决定是否重试；
//   - Validator Agent 的结论不能覆盖必需确定性检查的失败结果；
//   - CodeSaver 不决定任务是否最终成功；
//   - Runner 不自行重试完整的 Agent 调用；
//   - SourceProvider 不负责 Push 或 Merge；
//   - Container Manager 不自行判断任务成功。
//
// 所有下层能力都通过 Ports 中的窄接口注入，因此工作流顺序可以在没有真实
// Pi、SSH 与 Docker 的情况下被完整测试。
package workflow
