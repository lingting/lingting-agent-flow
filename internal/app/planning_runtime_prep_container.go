package app

import (
	"context"
	"fmt"

	"github.com/lingting/agentflow/internal/domain"
)

// CleanupPlanningSnapshotPreparationRequest 是一次性准备容器的清理输入。
type CleanupPlanningSnapshotPreparationRequest struct {
	// PlanningSessionID 是规划会话标识。
	PlanningSessionID string
}

// CleanupPlanningSnapshotPreparationResult 是一次性准备容器的清理结果。
type CleanupPlanningSnapshotPreparationResult struct {
	// Session 是清理前的规划会话记录：清理不改写数据库绑定。
	Session domain.PlanningSession
	// ContainerName 是由会话与执行节点推导出的准备容器名称。
	ContainerName string
	// Removed 表示本次确实删除了准备容器。
	Removed bool
	// AlreadyAbsent 表示核验时准备容器已不存在。
	AlreadyAbsent bool
}

// CleanupPlanningSnapshotPreparation 清理本会话残留的一次性快照准备容器。
//
// 这是一个只处理"准备容器"的操作：它不停止容器、不删除快照卷、不修改会话状态与运行时
// 绑定，也不重新准备快照。容器名称由 planningPrepContainerName 依据会话与执行节点生成，
// 调用方无法指定任意 Docker 容器名称或 ID。
//
// 规则固定为：
//   - 容器不存在：按已清理处理，不执行删除；
//   - 容器仍在运行：拒绝自动停止与删除，返回状态冲突；
//   - 容器状态无法确认（含读取失败）：拒绝操作并按不确定上报；
//   - 容器已退出或已停止：所有权与标签全部核验通过后才按 Docker 容器 ID 删除；
//   - 删除结果不确定：保留现场并立即返回，不自动重试；
//   - 删除成功后再次按名称核验，只有确认容器确实不存在才报告已清理。
//
// 名称相同不足以证明所有权：节点标签、规划会话标签、AgentFlow 管理标签、正式容器的登记
// 标签与 Docker 容器 ID 必须同时满足，否则拒绝删除并返回冲突。
func (s *PlanningRuntimeService) CleanupPlanningSnapshotPreparation(
	ctx context.Context,
	request CleanupPlanningSnapshotPreparationRequest,
) (CleanupPlanningSnapshotPreparationResult, error) {
	result := CleanupPlanningSnapshotPreparationResult{}
	stage := PlanningRuntimeStageCleanupPreparation
	sessionID, err := parsePlanningRuntimeSessionID(request.PlanningSessionID)
	if err != nil {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeInvalidInput,
			PlanningRuntimeStageValidate,
			"规划运行时请求非法",
			err,
		)
	}
	unlock := s.coordination.Lock(sessionID)
	defer unlock()
	// 排队期间调用方取消：还没有取得执行权，不得执行任何远程动作。
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("清理快照准备容器在取得执行权之前被取消: %w", err)
	}
	session, err := s.loadPlanningSession(ctx, sessionID)
	if err != nil {
		return result, err
	}
	result.Session = session
	if err := session.Validate(); err != nil {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageValidate,
			"规划会话记录不满足领域约束",
			err,
		)
	}
	if session.HostID == nil {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			stage,
			"规划会话没有绑定执行节点，无法定位一次性准备容器",
			nil,
		)
	}
	host, err := s.loadRuntimeHost(ctx, *session.HostID)
	if err != nil {
		return result, err
	}
	name, err := planningPrepContainerName(session.ID, host.ID)
	if err != nil {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			stage,
			"生成一次性准备容器名称失败",
			err,
		)
	}
	result.ContainerName = name
	inspect, found, err := s.remote.InspectPrepContainer(ctx, host.ID, name)
	if err != nil {
		return result, planningRuntimeRemoteFailure(err, stage, "核验一次性准备容器失败")
	}
	if !found {
		result.AlreadyAbsent = true
		return result, nil
	}
	if err := verifyPlanningPrepContainer(inspect, name, session.ID, host.ID); err != nil {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			"一次性准备容器的归属与当前规划会话不一致，拒绝删除",
			err,
		)
	}
	if inspect.IsActivelyRunning() {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			"一次性准备容器仍在运行，必须先人工停止，拒绝自动停止或删除",
			nil,
		)
	}
	switch inspect.State {
	case domain.DockerStateExited, domain.DockerStateDead:
	default:
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeUncertain,
			stage,
			"一次性准备容器状态 "+inspect.State.String()+" 无法确认已经退出，拒绝删除",
			nil,
		)
	}
	existed, err := s.remote.RemovePrepContainer(ctx, host.ID, inspect.ID)
	if err != nil {
		return result, planningRuntimeRemoteFailure(err, stage, "删除一次性准备容器失败")
	}
	_, stillFound, err := s.remote.InspectPrepContainer(ctx, host.ID, name)
	if err != nil {
		return result, planningRuntimeRemoteFailure(err, stage, "删除一次性准备容器后未能核验")
	}
	if stillFound {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			stage,
			"一次性准备容器删除后仍然存在",
			nil,
		)
	}
	result.Removed = existed
	result.AlreadyAbsent = !existed
	return result, nil
}
