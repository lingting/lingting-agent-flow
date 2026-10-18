package source_unit

import "github.com/lingting/agentflow/internal/sshclient"

// 测试替身使用的命令结果状态构造。
//
// 这些函数只用于让测试替身返回与真实 sshclient 一致的状态取值，
// 避免在测试中直接依赖具体的状态常量名。
func dockerStatusSucceeded() sshclient.CommandStatus {
	return sshclient.CommandStatusSucceeded
}

// dockerStatusExitError 返回非零退出码状态。
func dockerStatusExitError() sshclient.CommandStatus {
	return sshclient.CommandStatusExitError
}

// dockerStatusCancelled 返回取消状态。
func dockerStatusCancelled() sshclient.CommandStatus {
	return sshclient.CommandStatusCancelled
}

// dockerStatusTimeout 返回超时状态。
func dockerStatusTimeout() sshclient.CommandStatus {
	return sshclient.CommandStatusTimeout
}

// dockerStatusConnectionError 返回连接中断状态。
func dockerStatusConnectionError() sshclient.CommandStatus {
	return sshclient.CommandStatusConnectionError
}
