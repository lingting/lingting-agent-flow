// Package resourcecheck 提供 V3 R2 的本机资源自检用例。
//
// 用例只消费编排进程分配的句柄：可用性判断在 worker 内完成，释放与自证由提供者完成，
// 结论写入统一报告。它们只使用本机资源，不触碰真实 Pi 会话、真实 SSH 主机与远程 Docker；
// 真实设备资源的第一次验证放在 R4 / R4.5。
package resourcecheck
