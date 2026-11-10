package testrunner

import "time"

// 用例结论取值：跳过不等于通过，未执行必须如实显示为 SKIP。
const (
	StatusPassed  = "passed"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
	StatusPanic   = "panic"
	StatusTimeout = "timeout"
	// StatusError 表示 worker 没能产出可信结果：启动失败、异常退出或结果文件缺失。
	StatusError = "error"
)

// 进程退出码：只区分"全部通过（含跳过）"与"存在不可信或失败结果"。
const (
	ExitOK      = 0
	ExitFailure = 1
)

// ExitCodeFor 把用例结论映射为 worker 与编排进程的退出码。
func ExitCodeFor(status string) int {
	switch status {
	case StatusPassed, StatusSkipped:
		return ExitOK
	default:
		return ExitFailure
	}
}

// Timings 记录 worker 的关键时间点，用于观测 worker 启动开销。
type Timings struct {
	// QueueEnterAt 是用例进入调度队列的时刻，由编排进程填写。
	QueueEnterAt time.Time `json:"queue_enter_at,omitempty"`
	// DispatchAt 是调度器决定派发（或判定无法派发）的时刻，由编排进程填写。
	DispatchAt time.Time `json:"dispatch_at,omitempty"`
	// WorkerSpawnAt 是编排进程启动 worker 的时刻。
	WorkerSpawnAt time.Time `json:"worker_spawn_at,omitempty"`
	// WorkerEntryAt 是 worker 进程进入执行入口的时刻。
	WorkerEntryAt time.Time `json:"worker_entry_at,omitempty"`
	// CaseStartAt 是 worker 开始执行用例主体的时刻。
	CaseStartAt time.Time `json:"case_start_at,omitempty"`
	// CaseEndAt 是用例主体结束（或 worker 判定超时）的时刻。
	CaseEndAt time.Time `json:"case_end_at,omitempty"`
}

// ResourceResult 是一条资源声明在用例中的分配与释放结论。
// ResourceResult 是一条资源声明在用例中的分配、释放与自证结论。
type ResourceResult struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Scope        string `json:"scope"`
	Fingerprint  string `json:"fingerprint,omitempty"`
	Acquired     bool   `json:"acquired"`
	Released     bool   `json:"released"`
	ReleaseError string `json:"release_error,omitempty"`
	// Verified 表示提供者在释放后自证资源确实消失。
	Verified bool `json:"verified"`
	// VerifyError 是自证失败或无法确认的说明；它与 release_error 各自独立。
	VerifyError string `json:"verify_error,omitempty"`
	// LogPath 是提供者记录的进程输出文件；只有会启动子进程的资源类型会填。
	LogPath string `json:"log_path,omitempty"`
	// Payload 是分配到的句柄内容（例如 service-process 的 pid 与 base_url），供报告直接定位现场。
	Payload map[string]string `json:"payload,omitempty"`
}

// Failed 报告该资源是否让整次运行失败。
func (r ResourceResult) Failed() bool {
	return r.ReleaseError != "" || r.VerifyError != ""
}

// ArtifactPaths 是一条用例的全部产物位置，缺失时该字段为空。
type ArtifactPaths struct {
	CaseDir     string `json:"case_dir"`
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	Result      string `json:"result"`
	ArtifactDir string `json:"artifact_dir"`
}

// CaseResult 是一条用例的执行结论，也是 worker 结果文件的内容。
type CaseResult struct {
	ID         string           `json:"id"`
	Group      string           `json:"group"`
	Status     string           `json:"status"`
	Message    string           `json:"message,omitempty"`
	SkipReason string           `json:"skip_reason,omitempty"`
	Warnings   []string         `json:"warnings,omitempty"`
	StackTrace string           `json:"stack_trace,omitempty"`
	ExitCode   int              `json:"exit_code"`
	DurationMS int64            `json:"duration_ms"`
	Timings    Timings          `json:"timings"`
	Resources  []ResourceResult `json:"resources"`
	Artifacts  ArtifactPaths    `json:"artifacts"`
}

// Failed 报告该结果是否属于失败结论（跳过不算失败）。
func (r CaseResult) Failed() bool {
	return ExitCodeFor(r.Status) != ExitOK
}

// durationMS 从时间点推导毫秒时长，时间点缺失时返回零。
func durationMS(start, end time.Time) int64 {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}
