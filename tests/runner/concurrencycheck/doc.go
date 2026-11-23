// Package concurrencycheck 是调度与 worker 池的自检组（V3 R3、V4 R2 / R3）。
//
// 场景分两组：concurrencycheck 只包含必须通过的用例（V3 R3 的场景 1–5、V4 R2 的并发额度，
// 以及 V4 R3 的复用、独占与状态隔离）；concurrencycheck-fault 故意产生 timeout / panic /
// 取消 / 迟到结果 / worker 崩溃（V3 R3 的场景 6–9 与 V4 R3 的场景 17）。
//
// 只有编排进程能看到的部分（峰值在飞数、排队时长、额度上限与排队原因、worker 进程数、
// 复用次数、独占用例数、每条用例的 worker 号）由报告校验：读取字段见 report.go 的
// ConcurrencyReport 与 WorkerPoolReport。
//
// 自检用例不用 sleep 猜时序：占用窗口只用来让调度器有机会同时派发其他用例，
// 断言一律看报告字段或用例之间的阻塞点，不看等待的真实长度。
package concurrencycheck
