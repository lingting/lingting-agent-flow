package planning_remote

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// planningPiProcessPattern 是规划容器内 Pi 主进程的匹配模式。
//
// 容器内 `pi` 是 Node 包装脚本，进程命令行显示为可执行文件名本身（`pi`），因此按可执行
// 文件名匹配即可确认"Pi 已经真正启动"，不必依赖本地事件或模型输出。
const planningPiProcessPattern = "pi"

// runRoleWithContext 在指定上下文内执行一次规划角色调用。
func (f *fixture) runRoleWithContext(
	ctx context.Context,
	session domain.PlanningSession,
	roleType domain.RoleType,
	prompt string,
) (app.PlanningAgentResult, error) {
	return f.agent.RunRole(ctx, app.RunPlanningRoleRequest{
		PlanningSessionID: session.ID.String(),
		RoleType:          roleType.String(),
		Prompt:            prompt,
	})
}

// waitForPiProcess 轮询等待规划容器内出现 Pi 主进程。
//
// 等待只用于确认"远程真的已经启动 Pi"，超时即让测试失败：不能靠本地事件推断远程进程。
func (f *fixture) waitForPiProcess(t *testing.T, dockerID string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		alive, output := f.remoteProcessAlive(t, dockerID, planningPiProcessPattern)
		if alive {
			t.Logf("REMOTE_PI_RUNNING output=%s", truncate(output, 300))
			return output
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待规划容器内 Pi 主进程超时（%s）", timeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// cancelledRoleCall 是一次被取消的规划角色调用结果。
type cancelledRoleCall struct {
	// Result 是被取消调用返回的结果。
	Result app.PlanningAgentResult
	// Error 是被取消调用返回的错误。
	Error error
}

// runCancelledRole 启动一次真实 Pi 调用，在确认 Pi 已经开始执行后取消它。
//
// 取消只作用于本次调用的上下文；它模拟"调用方在 Pi 执行期间中断"。方法返回时的现场
// 由调用方核验：容器是否按规划终止策略停止、归档是否被覆盖。
func (f *fixture) runCancelledRole(
	t *testing.T,
	session domain.PlanningSession,
	detail app.PlanningRuntimeDetail,
	roleType domain.RoleType,
	prompt string,
) *cancelledRoleCall {
	t.Helper()
	callCtx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	type outcome struct {
		result app.PlanningAgentResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := f.runRoleWithContext(callCtx, session, roleType, prompt)
		done <- outcome{result: result, err: err}
	}()
	f.waitForPiProcess(t, detail.Container.DockerID, 2*time.Minute)
	cancel()
	select {
	case collected := <-done:
		t.Logf(
			"CANCELLED_CALL err=%v termination_confirmed=%t termination_error=%s",
			collected.err,
			collected.result.Result.RemoteTerminationConfirmed,
			truncate(collected.result.Result.RemoteTerminationError, 300),
		)
		return &cancelledRoleCall{Result: collected.result, Error: collected.err}
	case <-time.After(5 * time.Minute):
		t.Fatalf("取消后的规划调用未在 5 分钟内收敛")
		return nil
	}
}

// restartPlanningContainer 用生产准备流程重新启动已确认退出的规划容器。
//
// 它同时核验"复用同一容器"这一事实：容器记录 ID 与 Docker 容器 ID 都不允许变化。
func (f *fixture) restartPlanningContainer(
	t *testing.T,
	session domain.PlanningSession,
	previous app.PlanningRuntimeDetail,
) app.PlanningRuntimeDetail {
	t.Helper()
	restarted, err := f.runtime.Prepare(f.ctx, app.PreparePlanningRuntimeRequest{
		PlanningSessionID: session.ID.String(),
		HostID:            f.host.ID.String(),
	})
	if err != nil {
		t.Fatalf("重新启动规划容器失败: %v", err)
	}
	if restarted.Container.ID != previous.Container.ID {
		t.Fatalf("重新启动时更换了容器记录: %s → %s",
			previous.Container.ID, restarted.Container.ID)
	}
	if restarted.Container.DockerID != previous.Container.DockerID {
		t.Fatalf("重新启动时更换了 Docker 容器: %s → %s",
			previous.Container.DockerID, restarted.Container.DockerID)
	}
	if restarted.CodeVolumeName != previous.CodeVolumeName {
		t.Fatalf("重新启动时更换了快照卷: %s → %s",
			previous.CodeVolumeName, restarted.CodeVolumeName)
	}
	if restarted.SourceCommit != previous.SourceCommit {
		t.Fatalf("重新启动时更换了固定 commit: %s → %s",
			previous.SourceCommit, restarted.SourceCommit)
	}
	return restarted
}

// requireContainerStopped 通过独立控制连接核验容器已退出。
func (f *fixture) requireContainerStopped(t *testing.T, dockerID string) {
	t.Helper()
	inspect := f.controlInspectCondition(t, dockerID)
	if !inspect.IsConfirmedExited() {
		t.Fatalf(
			"容器 %s 未被确认退出: Docker 状态 %s，Running=%t，判定 %s",
			dockerID,
			inspect.State,
			inspect.Running,
			inspect.RuntimeCondition(),
		)
	}
	t.Logf("REMOTE_CONTAINER_STOPPED docker_id=%s state=%s", dockerID, inspect.State)
}

// requirePiProcessGone 通过独立控制连接核验容器内已没有 Pi 进程。
//
// 它只在容器仍可执行时有效；容器已退出时直接由容器状态给出结论。
func (f *fixture) requirePiProcessGone(t *testing.T, dockerID string) {
	t.Helper()
	inspect := f.controlInspectCondition(t, dockerID)
	if !inspect.IsActivelyRunning() {
		t.Logf("REMOTE_PI_ABSENT reason=container_not_running state=%s", inspect.State)
		return
	}
	alive, output := f.remoteProcessAlive(t, dockerID, planningPiProcessPattern)
	if alive {
		t.Fatalf("连接中断后远端 Pi 进程仍在运行: %s", truncate(output, 300))
	}
	t.Logf("REMOTE_PI_ABSENT reason=process_gone")
}

// assertLocalContainerState 核验本地容器记录与远端事实一致。
func (f *fixture) assertLocalContainerState(
	t *testing.T,
	containerID domain.ID,
	expected domain.ContainerState,
) domain.Container {
	t.Helper()
	record := f.reloadContainer(t, containerID)
	if record.State != expected {
		t.Fatalf("本地容器管理状态与远端事实不一致: 期望 %s，实际 %s（Docker 状态 %s）",
			expected, record.State, record.DockerState)
	}
	t.Logf("LOCAL_CONTAINER_STATE state=%s docker_state=%s", record.State, record.DockerState)
	return record
}

// assertBindingIntact 核验规划会话的运行环境绑定没有被终止流程清空或替换。
func (f *fixture) assertBindingIntact(
	t *testing.T,
	session domain.PlanningSession,
	detail app.PlanningRuntimeDetail,
) {
	t.Helper()
	current := f.reloadSession(t, session.ID)
	if current.HostID == nil || *current.HostID != f.host.ID {
		t.Fatalf("规划会话的执行节点绑定被改变: %v", current.HostID)
	}
	if current.ContainerID != detail.Container.ID.String() {
		t.Fatalf("规划会话的容器绑定被改变: 期望 %s，实际 %s",
			detail.Container.ID, current.ContainerID)
	}
	if current.SourceCommit != detail.SourceCommit {
		t.Fatalf("规划会话的固定 commit 被改变: 期望 %s，实际 %s",
			detail.SourceCommit, current.SourceCommit)
	}
	t.Logf(
		"RUNTIME_BINDING host=%s container=%s commit=%s",
		current.HostID,
		current.ContainerID,
		current.SourceCommit,
	)
}

// describeCallResult 返回一次调用的简短描述，用于日志与失败信息。
func describeCallResult(result app.PlanningAgentResult) string {
	return fmt.Sprintf(
		"status=%s exit=%d termination_confirmed=%t archive=%s",
		result.Result.Status,
		result.Result.ExitCode,
		result.Result.RemoteTerminationConfirmed,
		result.ArchivePath,
	)
}

// containsAny 判断文本是否包含任一候选片段。
func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
