package testrunner

import "time"

// 常驻 worker 的控制协议（V4 R3 §3）：
//
//	编排 → worker：stdin 一行 JSON 指令（run / shutdown）
//	worker → 编排：stdout 一行 JSON 事件（ready / started / done / shutdown）
//
// 控制通道占用 worker 的标准输入与标准输出，因此 worker 在执行用例期间把标准流指向该
// 用例的日志文件（W3），控制通道靠进程启动时的原始标准输出。一个 worker 同一时刻只执行
// 一条用例：派发、等待与收尾都在派发它的槽位 goroutine 内完成。
const (
	// workerCommandRun 表示执行一条用例。
	workerCommandRun = "run"
	// workerCommandShutdown 表示 worker 自行退出，不再领取用例。
	workerCommandShutdown = "shutdown"
	// workerEventReady 表示 worker 已就绪，可以接收指令。
	workerEventReady = "ready"
	// workerEventStarted 表示 worker 即将执行该用例；取消注入等"是否真的跑起来"的观测以它为准。
	workerEventStarted = "started"
	// workerEventDone 表示该用例已结束，结果文件已经写出。
	workerEventDone = "done"
	// workerEventShutdown 表示 worker 收到 shutdown 并即将退出。
	workerEventShutdown = "shutdown"
	// workerEventInvalid 表示控制通道出现了无法解析的输出：通道不再可信，worker 按已失效处理。
	workerEventInvalid = "invalid"
)

// controlChannelMaxLine 是控制通道单行上限：一条指令只携带用例 ID、路径与超时。
const controlChannelMaxLine = 1 << 20

// workerCommand 是编排进程发给常驻 worker 的一条指令。
type workerCommand struct {
	// Kind 取 run 或 shutdown。
	Kind string `json:"kind"`
	// CaseID 是被派发的用例；shutdown 时为空。
	CaseID string `json:"case_id,omitempty"`
	// ResultPath 是该用例的结果文件，由 worker 写出。
	ResultPath string `json:"result_path,omitempty"`
	// ArtifactDir 是该用例的产物目录，worker 只写不改。
	ArtifactDir string `json:"artifact_dir,omitempty"`
	// HandlePath 是资源句柄文件；用例没有资源时为空。
	HandlePath string `json:"handle_path,omitempty"`
	// WorkspaceDir 是该用例的工作空间目录。
	WorkspaceDir string `json:"workspace_dir,omitempty"`
	// StdoutPath 与 StderrPath 是该用例的日志文件：worker 每条用例重新打开（W3）。
	StdoutPath string `json:"stdout_path,omitempty"`
	StderrPath string `json:"stderr_path,omitempty"`
	// Timeout 是该用例的执行上限；零值表示按用例声明或默认上限。
	Timeout time.Duration `json:"timeout_ns,omitempty"`
	// RealTests 与 RealTestsReason 是编排进程下发的真实依赖授权结论，worker 不再自行判定。
	RealTests       bool   `json:"real_tests"`
	RealTestsReason string `json:"real_tests_reason,omitempty"`
}

// workerEvent 是 worker 发回编排进程的一条事件。
type workerEvent struct {
	// Kind 取 ready / started / done / shutdown / invalid。
	Kind string `json:"kind"`
	// CaseID 是该事件对应的用例；ready 与 shutdown 为空。
	CaseID string `json:"case_id,omitempty"`
	// Message 说明"为什么没有可信结果"（例如结果文件写不出来、控制通道被污染）。
	Message string `json:"message,omitempty"`
}
