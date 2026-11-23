// Package frontend 是前端适配器用例包：把 lingting-agent-flow-ui 的 13 个 Vitest
// 文件与 24 个 Playwright spec 接入统一执行器，并按 9 个产品领域分组供 CI matrix 并行调度。
//
// 粒度保持一个文件一条 Case，文件内串行；领域组只是执行器选择条件，不改变 Case 数量与断言。
// CI 分别运行 frontend-shell、frontend-planning、frontend-projects、frontend-requirements、
// frontend-settings、frontend-task-intake、frontend-task-execution、frontend-task-recovery
// 与 frontend-task-resources。调用方式按
// R6 D2：一条 Case 调一次 CLI 子进程（`pnpm exec playwright test <file>` / `pnpm exec vitest
// run <file>`），CLI 的并发固定为 1，产物落在该 Case 自己的产物目录里。
//
// 用例不声明远程资源：dev server 由 frontend-dev-server 提供者启动并在整次运行内共享，
// browser / node-worker 只负责会话目录与实际启动进程的登记，全部断言仍由前端测试自身承担。
package frontend
