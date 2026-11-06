package agent_test

// 本文件验证容器内 Pi Runner 的受控终止语义。
//
// 用例只使用受控执行器与容器核验替身：命令构造、结果分类、Session 提取与稳定 Session
// 归档都经过真实代码路径，但不连接真实容器、SSH 或模型。终止是否发生、使用什么上下文、
// 结果如何表达都由这些用例固定下来。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
)

// planningRequest 构造一次启用规划容器终止策略的调用请求。
func (h *harness) planningRequest(invocationID string) agent.RunRequest {
	request := h.request
	request.InvocationID = invocationID
	request.PlanningSessionID = testPlanningSessionID
	request.StopContainerOnUncertain = true
	return request
}

// planningStableRequest 构造一次同时使用稳定 Session 与终止策略的调用请求。
func (h *harness) planningStableRequest(invocationID string) agent.RunRequest {
	request := h.stableRequest(invocationID)
	request.PlanningSessionID = testPlanningSessionID
	request.StopContainerOnUncertain = true
	return request
}

// uncertainMainResponse 模拟"Pi 主命令已经启动但结果无法确认"。
func uncertainMainResponse(
	executor *fakeExecutor,
	request agent.ContainerExecRequest,
) (agent.ContainerExecResult, error) {
	switch {
	case isVersionProbe(request.Command):
		return succeeded("9.9.9\n"), nil
	case isExportCall(request.Command):
		return succeeded(""), nil
	case sessionPathOf(request.Command) == "":
		// 恢复历史 Session 之前的目录准备等辅助命令不属于主调用。
		return succeeded(""), nil
	}
	return agent.ContainerExecResult{
		ExitCode:  -1,
		Started:   true,
		Cancelled: true,
	}, context.Canceled
}

// TestContainerRunnerSuccessDoesNotTerminateContainer 验证正常完成的规划调用不终止容器。
func TestContainerRunnerSuccessDoesNotTerminateContainer(t *testing.T) {
	t.Parallel()
	h := newHarness(t, successResponder)
	result, err := h.runner.Run(context.Background(), h.planningRequest("invoke-1"), nil)
	if err != nil {
		t.Fatalf("规划调用执行失败: %v", err)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
	if h.executor.terminationCount() != 0 {
		t.Fatalf("正常完成不应终止容器，实际终止 %d 次", h.executor.terminationCount())
	}
	if result.RemoteTerminationConfirmed || result.RemoteTerminationError != "" {
		t.Fatalf("未触发终止时不应报告终止结果: %+v", result)
	}
}

// TestContainerRunnerTaskUncertainDoesNotTerminateContainer 验证普通任务超时或取消都不终止容器。
func TestContainerRunnerTaskUncertainDoesNotTerminateContainer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		result agent.ContainerExecResult
		err    error
		want   agent.RunStatus
	}{
		{
			name: "取消",
			result: agent.ContainerExecResult{
				ExitCode:  -1,
				Started:   true,
				Cancelled: true,
			},
			err:  context.Canceled,
			want: agent.RunCancelled,
		},
		{
			name: "超时",
			result: agent.ContainerExecResult{
				ExitCode: -1,
				Started:  true,
				TimedOut: true,
			},
			err:  context.DeadlineExceeded,
			want: agent.RunTimedOut,
		},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, func(
				executor *fakeExecutor,
				request agent.ContainerExecRequest,
			) (agent.ContainerExecResult, error) {
				switch {
				case isVersionProbe(request.Command):
					return succeeded("9.9.9\n"), nil
				case isExportCall(request.Command):
					return succeeded(""), nil
				case sessionPathOf(request.Command) == "":
					return succeeded(""), nil
				}
				return item.result, item.err
			})
			result, err := h.runner.Run(context.Background(), h.request, nil)
			if err == nil {
				t.Fatal("结果不确定的调用必须返回错误")
			}
			if result.Status != item.want {
				t.Fatalf("期望状态 %s，实际 %s", item.want, result.Status)
			}
			if h.executor.terminationCount() != 0 {
				t.Fatalf(
					"普通任务没有终止策略，不应终止容器: %d 次",
					h.executor.terminationCount(),
				)
			}
			if result.RemoteTerminationConfirmed || result.RemoteTerminationError != "" {
				t.Fatalf("普通任务不应报告远端终止结果: %+v", result)
			}
		})
	}
}

// TestContainerRunnerTerminatesOnPlanningTimeout 验证规划调用超时同样终止独占容器。
func TestContainerRunnerTerminatesOnPlanningTimeout(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(
		executor *fakeExecutor,
		request agent.ContainerExecRequest,
	) (agent.ContainerExecResult, error) {
		if isVersionProbe(request.Command) {
			return succeeded("9.9.9\n"), nil
		}
		return agent.ContainerExecResult{
			ExitCode: -1,
			Started:  true,
			TimedOut: true,
		}, context.DeadlineExceeded
	})
	result, err := h.runner.Run(context.Background(), h.planningRequest("invoke-1"), nil)
	if err == nil {
		t.Fatal("超时的调用必须返回错误")
	}
	if result.Status != agent.RunTimedOut {
		t.Fatalf("执行状态应保持真实的 timed_out，实际 %s", result.Status)
	}
	if !result.RemoteTerminationConfirmed {
		t.Fatalf("超时后必须确认远端终止: %q", result.RemoteTerminationError)
	}
	if h.executor.terminationCount() != 1 {
		t.Fatalf("超时后应当只终止一次，实际 %d 次", h.executor.terminationCount())
	}
}

// TestContainerRunnerTerminatesOnceWhenResultUncertain 验证规划调用结果不确定时只终止一次。
func TestContainerRunnerTerminatesOnceWhenResultUncertain(t *testing.T) {
	t.Parallel()
	h := newHarness(t, uncertainMainResponse)
	result, err := h.runner.Run(context.Background(), h.planningRequest("invoke-1"), nil)
	if err == nil {
		t.Fatal("结果不确定的调用必须返回错误")
	}
	if result.Status != agent.RunCancelled {
		t.Fatalf("执行状态应保持真实的 cancelled，实际 %s", result.Status)
	}
	if !result.RemoteTerminationConfirmed {
		t.Fatalf("已确认停止时应报告远端终止: %q", result.RemoteTerminationError)
	}
	if result.RemoteTerminationError != "" {
		t.Fatalf("确认终止时不应带终止错误: %s", result.RemoteTerminationError)
	}
	if h.executor.terminationCount() != 1 {
		t.Fatalf("应当只终止一次，实际 %d 次", h.executor.terminationCount())
	}
	request := h.executor.terminationAt(t, 0)
	if request.ContainerID != testContainerID {
		t.Fatalf("终止请求的容器记录不符: %s", request.ContainerID)
	}
	if request.DockerID != testDockerID {
		t.Fatalf("终止请求的 Docker 容器 ID 不符: %s", request.DockerID)
	}
	if request.PlanningSessionID != testPlanningSessionID {
		t.Fatalf("终止请求的规划会话标识不符: %s", request.PlanningSessionID)
	}
	if request.GraceSeconds <= 0 {
		t.Fatalf("终止请求必须给出正的优雅停止宽限期，实际 %d", request.GraceSeconds)
	}
	// 容器已确认停止：不再导出 HTML，也不再有可复制的 Session。
	for _, executed := range h.executor.requests() {
		if isExportCall(executed.Command) {
			t.Fatalf("容器已停止时不得再导出 HTML: %v", executed.Command)
		}
	}
	if result.Session.Completeness != agent.SessionMissing {
		t.Fatalf("容器已停止时本轮不应有 Session，实际 %s", result.Session.Completeness)
	}
	if result.Session.JSONLPath != "" {
		t.Fatalf("容器已停止时不应写入本机 Session 文件: %s", result.Session.JSONLPath)
	}
}

// TestContainerRunnerTerminationUsesCleanupContext 验证终止使用独立且有上限的清理上下文。
func TestContainerRunnerTerminationUsesCleanupContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, func(
		executor *fakeExecutor,
		request agent.ContainerExecRequest,
	) (agent.ContainerExecResult, error) {
		if isVersionProbe(request.Command) {
			return succeeded("9.9.9\n"), nil
		}
		// 调用方在 Pi 主命令执行期间取消：终止清理不能沿用已经取消的上下文。
		cancel()
		return agent.ContainerExecResult{
			ExitCode:  -1,
			Started:   true,
			Cancelled: true,
		}, context.Canceled
	})
	result, err := h.runner.Run(ctx, h.planningRequest("invoke-1"), nil)
	if err == nil {
		t.Fatal("取消的调用必须返回错误")
	}
	if !result.RemoteTerminationConfirmed {
		t.Fatalf("取消后仍必须完成容器终止核验: %q", result.RemoteTerminationError)
	}
	if h.executor.terminationCount() != 1 {
		t.Fatalf("应当只终止一次，实际 %d 次", h.executor.terminationCount())
	}
	// 清理上下文的状态必须在终止发生时记录：Runner 返回后会立即释放它，事后再检查
	// 上下文本身只能看到"已释放"，看不到调用时是否已经被调用方取消。
	state := h.executor.terminationStateAt(t, 0)
	if state.err != nil {
		t.Fatalf("终止必须使用独立清理上下文，调用时却已结束: %v", state.err)
	}
	if !state.hasDeadline {
		t.Fatal("清理上下文必须有明确的时间上限")
	}
	if !state.deadline.After(time.Now()) {
		t.Fatalf("清理上下文的时间上限必须尚未到期: %s", state.deadline)
	}
}

// TestContainerRunnerSkipsTerminationWhenCommandNeverStarted 验证命令没有启动时不终止容器。
func TestContainerRunnerSkipsTerminationWhenCommandNeverStarted(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(
		executor *fakeExecutor,
		request agent.ContainerExecRequest,
	) (agent.ContainerExecResult, error) {
		if isVersionProbe(request.Command) {
			return succeeded("9.9.9\n"), nil
		}
		return agent.ContainerExecResult{
			ExitCode:  -1,
			Started:   false,
			Certain:   false,
			Cancelled: true,
		}, context.Canceled
	})
	result, err := h.runner.Run(context.Background(), h.planningRequest("invoke-1"), nil)
	if err == nil {
		t.Fatal("命令没有启动时调用必须返回错误")
	}
	if h.executor.terminationCount() != 0 {
		t.Fatalf("命令没有启动时不得终止容器，实际 %d 次", h.executor.terminationCount())
	}
	if result.RemoteTerminationConfirmed {
		t.Fatal("命令没有启动时不得报告远端终止已确认")
	}
}

// TestContainerRunnerSkipsTerminationOnExplicitExit 验证明确退出的命令不触发终止。
func TestContainerRunnerSkipsTerminationOnExplicitExit(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(
		executor *fakeExecutor,
		request agent.ContainerExecRequest,
	) (agent.ContainerExecResult, error) {
		if isVersionProbe(request.Command) {
			return succeeded("9.9.9\n"), nil
		}
		return agent.ContainerExecResult{
			ExitCode: 1,
			Stderr:   "模型不可用",
			Started:  true,
			Certain:  true,
		}, nil
	})
	result, err := h.runner.Run(context.Background(), h.planningRequest("invoke-1"), nil)
	if err == nil {
		t.Fatal("非零退出码必须返回错误")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
	if h.executor.terminationCount() != 0 {
		t.Fatalf("命令已明确退出时不得终止容器，实际 %d 次", h.executor.terminationCount())
	}
}

// TestContainerRunnerTerminationUncertainReported 验证无法确认停止时如实返回不确定结果。
func TestContainerRunnerTerminationUncertainReported(t *testing.T) {
	t.Parallel()
	h := newHarness(t, uncertainMainResponse)
	h.executor.terminateResponder = func(
		request agent.ContainerTerminationRequest,
	) (agent.ContainerTerminationResult, error) {
		return agent.ContainerTerminationResult{}, errors.New("模拟停止命令结果不确定")
	}
	result, err := h.runner.Run(context.Background(), h.planningRequest("invoke-1"), nil)
	if err == nil {
		t.Fatal("终止结果不确定时必须返回错误")
	}
	if !errors.Is(err, agent.ErrRemoteTermination) {
		t.Fatalf("错误应属于远端终止类别，实际 %v", err)
	}
	if result.RemoteTerminationConfirmed {
		t.Fatal("终止结果不确定时不得报告远端终止已确认")
	}
	if !strings.Contains(result.RemoteTerminationError, "不确定") {
		t.Fatalf("结果应说明远端终止不确定: %q", result.RemoteTerminationError)
	}
	// 执行状态仍然是真实的取消结论，不被终止结果改写。
	if result.Status != agent.RunCancelled {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCancelled, result.Status)
	}
}

// TestContainerRunnerTerminationKeepsLastArchive 验证停止后不覆盖本机最后一份完整归档。
func TestContainerRunnerTerminationKeepsLastArchive(t *testing.T) {
	t.Parallel()
	h := newHarness(t, uncertainMainResponse)
	h.writeArchive(t, testSessionJSONL)
	result, err := h.runner.Run(context.Background(), h.planningStableRequest("invoke-1"), nil)
	if err == nil {
		t.Fatal("取消的调用必须返回错误")
	}
	if !result.RemoteTerminationConfirmed {
		t.Fatalf("已确认停止时应报告远端终止: %q", result.RemoteTerminationError)
	}
	if result.StableSession.Archived {
		t.Fatalf("本轮没有可用 Session，不得报告已更新归档: %+v", result.StableSession)
	}
	if result.StableSession.Error == "" {
		t.Fatal("本轮未更新归档时应说明原因")
	}
	// 本机归档是跨容器恢复的权威副本：容器停止后既不得删除，也不得被本轮覆盖。
	if content := h.readArchive(t); content != testSessionJSONL {
		t.Fatalf("本机最后一份完整归档被改写: %q", content)
	}
}
