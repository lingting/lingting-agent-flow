// Package scheduler 是 V3 R4.5 的试点用例包：把 tests/integration/local/scheduler
// 的既有集成用例迁到统一测试执行器。
//
// 迁移写法（本阶段冻结，R5 照此批量执行）：
//
//   - 一条既有 func Test* 对应一条 Case，ID 为
//     "integration-local-scheduler/<原用例名转 kebab-case>"，顺序与源文件一致，便于与
//     R0 冻结清单逐条对账；
//   - 用例主体写成 func run*(t *testrunner.Context)，参数名沿用 t，使主体与源文件逐行
//     对应；t 提供与 testing.T 同名的 Helper/Logf/Errorf/Fatalf/Fatal/Skipf/Skip/
//     Cleanup，辅助函数签名也统一收它，因此断言与语句不需要改写；
//   - t.Parallel() 不再出现在主体里，并发属性由 Case.Parallel 声明，并发最终由调度器决定；
//   - 表驱动的循环与顺序断言原样保留为 Case 内的顺序块，不再另设子测试；
//   - 每条用到数据库的用例声明一条 case-exclusive 的 sqlite-db，数据库路径只从注入句柄
//     读取；不使用数据库的用例不声明资源；
//   - 用例工作空间取 t.WorkspaceDir()，辅助组件用 testkit.NewAt 挂在它下面，不走 go test
//     进程内的运行根目录；
//   - TestMain 的职责（日志落盘、最终核验）分别由 worker 输出与运行级终检承担，用例包里
//     不再有 TestMain。
//   - 每条用例的注释保留原 func Test* 名称（"原 TestXxx ..."），ID 与源文件一一对应时
//     不必再翻历史就能对账；
//   - Case.Package 沿用来源 go test 包路径 tests/integration/local/scheduler，而不是迁移
//     后的目录：串行作用域与 R2 冻结的包级串行表都按来源路径组织；
//   - 本包没有 t.Run 子测试，因此试点不引入子测试 API：表驱动循环保持 Case 内的顺序块，
//     子测试记录能力等到真正出现 t.Run 的包再补。
//
// 用例只消费资源句柄，释放与自证由编排进程的提供者承担。
package scheduler
