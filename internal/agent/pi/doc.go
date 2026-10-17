// Package pi 实现基于 Pi 非交互式 CLI 的统一 Agent Runner。
//
// 一次 Run 对应一个全新的 Pi 子进程，使用 --print 运行单次提示词并退出：
//
//	pi --print --session <session.jsonl> --model provider/model-id
//
// 提示词通过标准输入写入并在写入后立即关闭，避免 Pi 继续等待输入。
// 每次调用使用独立的 Session 文件，退出后校验该文件并把原始 JSONL 保留在
// 调用方给定的 Session 根目录下，随后尝试通过 Pi 官方导出能力生成 HTML。
//
// 本包不依赖 RPC 协议，也不承担数据库业务：Session 元数据由应用服务持久化。
package pi
