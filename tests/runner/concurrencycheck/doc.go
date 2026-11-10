// Package concurrencycheck 是 V3 R3 的调度自检组。
//
// 9 个调度场景分两组：concurrencycheck 只包含必须通过的用例（场景 1–5），
// concurrencycheck-fault 故意产生 timeout / panic / 取消 / 迟到结果（场景 6–9）。
// 只有编排进程能看到的部分（峰值在飞数、排队时长、区间是否重叠、是否再次派发）
// 由报告校验，读取方式写在 .steps/V3/R3/README.md 的验收记录里。
//
// 自检用例不用 sleep 猜时序：占用窗口只用来让调度器有机会同时派发其他用例，
// 断言一律看报告字段或用例之间的阻塞点，不看等待的真实长度。
package concurrencycheck
