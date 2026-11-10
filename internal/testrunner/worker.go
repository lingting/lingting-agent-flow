package testrunner

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"
)

// DefaultCaseTimeout 是用例的默认执行上限，对齐原 go test -timeout 10m 的口径。
const DefaultCaseTimeout = 10 * time.Minute

// WorkerRequest 是编排进程发给 worker 子进程的完整执行请求。
//
// 字段与命令行参数一一对应：请求只经由参数与句柄文件传递，不依赖环境变量。
type WorkerRequest struct {
	CaseID          string
	ResultPath      string
	ArtifactDir     string
	HandlePath      string
	Timeout         time.Duration
	RealTests       bool
	RealTestsReason string
}

// ExecuteWorker 在 worker 子进程中执行单个用例并写出结果文件。
//
// 返回的 error 只表示"结果文件都没能写出"的编排级失败；用例本身的结论在 CaseResult 的
// Status 里，由调用方映射为退出码。
func ExecuteWorker(req WorkerRequest) (CaseResult, error) {
	entryAt := time.Now()
	result := CaseResult{ID: req.CaseID, Timings: Timings{WorkerEntryAt: entryAt}}
	c, err := Lookup(req.CaseID)
	if err != nil {
		result.Status = StatusError
		result.Message = err.Error()
		return finalizeWorkerResult(req, result)
	}
	result.Group = c.Group
	handles, warnings, err := workerHandles(c, req.HandlePath)
	if err != nil {
		result.Status = StatusError
		result.Message = err.Error()
		return finalizeWorkerResult(req, result)
	}
	if err := os.MkdirAll(req.ArtifactDir, 0o750); err != nil {
		result.Status = StatusError
		result.Message = fmt.Sprintf("创建用例产物目录失败: %v", err)
		return finalizeWorkerResult(req, result)
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = c.Timeout
	}
	if timeout <= 0 {
		timeout = DefaultCaseTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	outcome := make(chan CaseResult, 1)
	startedAt := make(chan time.Time, 1)
	go func() {
		outcome <- runCaseBody(ctx, c, req, handles, entryAt, startedAt)
	}()
	select {
	case result = <-outcome:
	case <-ctx.Done():
		// 用例主体不响应取消时，worker 以超时结论退出，编排进程仍会按宽限期终止进程树。
		startAt := entryAt
		select {
		case startAt = <-startedAt:
		default:
		}
		endAt := time.Now()
		result = CaseResult{
			ID:      c.ID,
			Group:   c.Group,
			Status:  StatusTimeout,
			Message: fmt.Sprintf("用例超过 %s 未结束", timeout),
			Timings: Timings{
				WorkerEntryAt: entryAt,
				CaseStartAt:   startAt,
				CaseEndAt:     endAt,
			},
			DurationMS: durationMS(startAt, endAt),
		}
	}
	// 句柄校验的警告来自 worker 入口，结论本身由用例主体产出，这里统一合并。
	result.Warnings = append(warnings, result.Warnings...)
	return finalizeWorkerResult(req, result)
}

// runCaseBody 执行用例主体，把 panic、失败与跳过统一转换成结论。
func runCaseBody(
	ctx context.Context,
	c Case,
	req WorkerRequest,
	handles []Handle,
	entryAt time.Time,
	startedAt chan<- time.Time,
) (result CaseResult) {
	startAt := time.Now()
	startedAt <- startAt
	result = CaseResult{ID: c.ID, Group: c.Group, Timings: Timings{WorkerEntryAt: entryAt}}
	cctx := newContext(ctx, c, req.ArtifactDir, handles, req.RealTests, req.RealTestsReason)
	defer func() {
		result.Warnings = append(result.Warnings, cctx.Warnings()...)
		result.Timings.CaseStartAt = startAt
		result.Timings.CaseEndAt = time.Now()
		result.DurationMS = durationMS(startAt, result.Timings.CaseEndAt)
	}()
	defer func() {
		recovered := recover()
		if recovered == nil {
			skipped, reason := cctx.Skipped()
			switch {
			case skipped:
				result.Status = StatusSkipped
				result.SkipReason = reason
			case cctx.Failed():
				result.Status = StatusFailed
				result.Message = strings.Join(cctx.Failures(), "\n")
			default:
				result.Status = StatusPassed
			}
			return
		}
		if signal, ok := recovered.(fatalSignal); ok {
			if signal.skip {
				_, reason := cctx.Skipped()
				result.Status = StatusSkipped
				result.SkipReason = reason
				return
			}
			result.Status = StatusFailed
			result.Message = strings.Join(cctx.Failures(), "\n")
			return
		}
		result.Status = StatusPanic
		result.Message = fmt.Sprintf("用例 panic: %v", recovered)
		result.StackTrace = string(debug.Stack())
	}()
	c.Run(cctx)
	return result
}

// workerHandles 读取并校验注入的资源句柄；声明了资源却没有句柄文件一律按错误处理。
func workerHandles(c Case, path string) ([]Handle, []string, error) {
	if len(c.Resources) == 0 {
		return nil, nil, nil
	}
	if path == "" {
		return nil, nil, fmt.Errorf("用例 %q 声明了资源，但 worker 未收到句柄文件", c.ID)
	}
	file, err := ReadHandleFile(path)
	if err != nil {
		return nil, nil, err
	}
	if file.CaseID != "" && file.CaseID != c.ID {
		return nil, nil, fmt.Errorf("句柄文件属于用例 %q，与当前用例 %q 不一致", file.CaseID, c.ID)
	}
	warnings, err := VerifyHandles(c, file.Handles)
	if err != nil {
		return nil, nil, err
	}
	return file.Handles, warnings, nil
}

// finalizeWorkerResult 补齐退出码并写出结果文件。
func finalizeWorkerResult(req WorkerRequest, result CaseResult) (CaseResult, error) {
	if result.Status == "" {
		result.Status = StatusError
		result.Message = "worker 未给出用例结论"
	}
	result.ExitCode = ExitCodeFor(result.Status)
	if err := writeJSONFile(req.ResultPath, result); err != nil {
		return result, fmt.Errorf("写出用例结果文件失败: %w", err)
	}
	return result, nil
}
