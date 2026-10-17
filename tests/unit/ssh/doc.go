// Package ssh_unit 是 SSH 连接层的单元测试包。
//
// 该包只依赖本机进程内的替身，不启动真实 SSH 服务、不访问外部网络：
// 主机密钥结论、指纹计算、参数转义、远程路径、私钥路径规范化、结果状态与
// 超时取消状态都在这里覆盖；真实协议交互由 tests/integration/local/ssh 覆盖。
package ssh_unit
