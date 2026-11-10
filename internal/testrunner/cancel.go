package testrunner

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// 取消语义（V3 R3 §4.4 / D2 冻结）。
//
// 取消不新增第七种用例状态：被中断的用例记 error 并在 message 里注明「运行被取消」，
// 从未派发的用例同样记 error 并注明未派发执行；运行级取消原因写进报告的 cancellation。
// 取消后仍然释放已分配资源并执行零残留终检，再写报告。
const (
	// CancelReasonInterrupt 是收到中断信号时的运行级取消原因。
	CancelReasonInterrupt = "收到中断信号"
	// caseCancelledMessage 是被中断的在跑用例的结论文案。
	caseCancelledMessage = "运行被取消"
	// caseUndispatchedMessage 是取消后从未派发的用例结论文案。
	caseUndispatchedMessage = "运行被取消，用例未派发执行"
)

// SignalContext 返回收到中断信号即取消的上下文；stop 用于恢复默认信号处理。
func SignalContext() (context.Context, func()) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// cancellationReason 决定运行级取消原因：自检注入优先，其次才是中断信号。
func cancellationReason(injected string) string {
	if reason := strings.TrimSpace(injected); reason != "" {
		return reason
	}
	return CancelReasonInterrupt
}

// awaitWorkerAfterCancel 在取消后先给 worker 一段宽限期自行收尾，到期再终止整棵进程树。
//
// 编排进程与 worker 之间没有取消通道：worker 只能通过自己死亡结束，因此"发取消"在这里
// 就是停止等待并给出宽限；进程树回收失败必须如实返回错误，不能当作已终止。
func awaitWorkerAfterCancel(command *exec.Cmd, wait <-chan error, grace time.Duration) error {
	select {
	case <-wait:
		return nil
	case <-time.After(grace):
	}
	return terminateWorker(command, wait, grace)
}
