package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
)

// 本文件实现两类互相独立的快照现场处置：
//
//   - RecoverPlanningSnapshot：快照已经完整存在，但 Server 在持久化 SourceCommit 之前退出；
//   - DiscardIncompletePlanningSnapshot：快照没有完成，必须显式废弃。
//
// 两者不得合并成一次"自动修复"：恢复只核验并落库一个已经完整的快照，绝不重新 Clone 或
// checkout；废弃只删除未固定 commit、未被容器引用且准备容器已确认不存在的快照卷。

// RecoverPlanningSnapshotRequest 是快照恢复的输入。
type RecoverPlanningSnapshotRequest struct {
	// PlanningSessionID 是规划会话标识。
	PlanningSessionID string
}

// RecoverPlanningSnapshotResult 是快照恢复结果。
type RecoverPlanningSnapshotResult struct {
	// Session 是本次恢复后的规划会话记录。
	Session domain.PlanningSession
	// VolumeName 是由会话与执行节点推导出的快照卷名称。
	VolumeName string
	// Commit 是只读核验得到的远端实际 commit。
	Commit string
	// Persisted 表示本次把实际 commit 写入了规划会话。
	Persisted bool
	// AlreadyRecorded 表示数据库已记录同一个 commit，本次只做核验。
	AlreadyRecorded bool
}

// RecoverPlanningSnapshot 恢复一个已经完整存在但未落库的快照。
//
// 适用情况：快照卷已经创建并完成 Clone / checkout，但 Server 在持久化 SourceCommit 之前
// 退出。执行顺序固定为：
//
//  1. 取得会话锁；
//  2. 读取会话与运行时绑定；
//  3. 核验一次性准备容器：只要它仍然存在（无论是否运行）就拒绝，因为远程可能仍在写快照，
//     必须先通过 CleanupPlanningSnapshotPreparation 显式清理；
//  4. 通过已有的只读核验逻辑检查快照卷所有权、worktree、HEAD 与实际 commit；
//  5. 数据库尚未记录 SourceCommit 时，用条件更新写入实际 commit；
//  6. 数据库已有 SourceCommit 时，远端实际 commit 必须完全一致，否则返回冲突；
//  7. 远程状态不确定时保留原有绑定并如实上报。
//
// 本方法不重新执行 Clone 或 checkout，也不把不完整的快照标记为恢复成功；运行时绑定的写入
// 走既有条件更新，因此不会覆盖其他并发操作已经写入的新事实。
func (s *PlanningRuntimeService) RecoverPlanningSnapshot(
	ctx context.Context,
	request RecoverPlanningSnapshotRequest,
) (RecoverPlanningSnapshotResult, error) {
	result := RecoverPlanningSnapshotResult{}
	stage := PlanningRuntimeStageRecoverSnapshot
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
		return result, fmt.Errorf("恢复规划快照在取得执行权之前被取消: %w", err)
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
			"规划会话没有绑定执行节点，无法核验快照",
			nil,
		)
	}
	host, err := s.loadRuntimeHost(ctx, *session.HostID)
	if err != nil {
		return result, err
	}
	result.VolumeName = planningSnapshotVolumeName(session.ID, host.ID)
	prep, err := s.inspectRuntimePrepContainer(ctx, session, host, stage)
	if err != nil {
		return result, err
	}
	if prep.Presence != PlanningRuntimePresenceAbsent {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			"一次性准备容器仍然存在，可能仍在写入快照，必须先显式清理才能核验",
			nil,
		)
	}
	snapshot, err := s.verifyRuntimeSnapshot(ctx, session, host, stage, true)
	if err != nil {
		return result, err
	}
	if snapshot.Presence != PlanningRuntimePresencePresent {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			"快照卷不存在，不能恢复",
			nil,
		)
	}
	result.Commit = snapshot.Commit
	expected := strings.TrimSpace(session.SourceCommit)
	if expected != "" {
		if snapshot.Commit != expected {
			return result, planningRuntimeFailure(
				ErrPlanningRuntimeConflict,
				stage,
				"远端快照 commit "+snapshot.Commit+" 与已记录的 "+expected+" 不一致，拒绝覆盖",
				nil,
			)
		}
		result.AlreadyRecorded = true
		return result, nil
	}
	updated, persistErr := s.persistRuntime(
		context.WithoutCancel(ctx),
		session,
		&host.ID,
		session.ContainerID,
		snapshot.Commit,
	)
	if persistErr != nil {
		return result, planningRuntimeBindingFailure(
			persistErr,
			PlanningRuntimeStagePersistSnapshot,
			"快照实际 commit 未能写入规划会话",
		)
	}
	result.Session = updated
	result.Persisted = true
	return result, nil
}

// DiscardIncompletePlanningSnapshotRequest 是不完整快照废弃的输入。
type DiscardIncompletePlanningSnapshotRequest struct {
	// PlanningSessionID 是规划会话标识。
	PlanningSessionID string
}

// DiscardIncompletePlanningSnapshotResult 是不完整快照的废弃结果。
type DiscardIncompletePlanningSnapshotResult struct {
	// Session 是废弃前的规划会话记录：废弃不清空 SourceCommit，也不改业务状态。
	Session domain.PlanningSession
	// VolumeName 是由会话与执行节点推导出的快照卷名称。
	VolumeName string
	// Removed 表示本次确实删除了快照卷。
	Removed bool
	// AlreadyAbsent 表示核验时快照卷已不存在。
	AlreadyAbsent bool
}

// DiscardIncompletePlanningSnapshot 显式废弃一个不完整的快照卷。
//
// 这是破坏性操作，只有同时满足以下条件才允许删除：SourceCommit 为空（尚未固定为正式
// 快照）、ContainerID 为空（没有正式规划容器引用它）、一次性准备容器已确认不存在、快照卷
// 存在且卷名、管理标签、执行节点标签与规划会话标签全部匹配。
//
// 执行顺序固定为：取得会话锁 → 读取数据库绑定 → 确认准备容器不存在 → 核验快照卷 →
// 核验完整所有权 → 删除快照卷 → 再次核验卷确实不存在 → 返回废弃结果。
//
// 本方法不清除数据库 SourceCommit、不修改会话业务状态、不重新准备快照；远程结果不确定时
// 保留数据库绑定，不通过第二次删除命令猜测结果。判断"快照是否完整"、恢复一个有效快照属于
// RecoverPlanningSnapshot，两者不合并。
func (s *PlanningRuntimeService) DiscardIncompletePlanningSnapshot(
	ctx context.Context,
	request DiscardIncompletePlanningSnapshotRequest,
) (DiscardIncompletePlanningSnapshotResult, error) {
	result := DiscardIncompletePlanningSnapshotResult{}
	stage := PlanningRuntimeStageDiscardSnapshot
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
		return result, fmt.Errorf("废弃规划快照在取得执行权之前被取消: %w", err)
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
	if commit := strings.TrimSpace(session.SourceCommit); commit != "" {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			"规划会话已记录固定 commit "+commit+"，只能走快照恢复或运行时释放，不能废弃",
			nil,
		)
	}
	if containerID := strings.TrimSpace(session.ContainerID); containerID != "" {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			"规划会话仍登记规划容器 "+containerID+"，快照被运行环境引用，不能废弃",
			nil,
		)
	}
	if session.HostID == nil {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			stage,
			"规划会话没有绑定执行节点，无法定位快照卷",
			nil,
		)
	}
	host, err := s.loadRuntimeHost(ctx, *session.HostID)
	if err != nil {
		return result, err
	}
	result.VolumeName = planningSnapshotVolumeName(session.ID, host.ID)
	prep, err := s.inspectRuntimePrepContainer(ctx, session, host, stage)
	if err != nil {
		return result, err
	}
	if prep.Presence != PlanningRuntimePresenceAbsent {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			"一次性准备容器仍然存在，可能仍在写入快照，不能废弃快照卷",
			nil,
		)
	}
	snapshot, err := s.verifyRuntimeSnapshot(ctx, session, host, stage, false)
	if err != nil {
		return result, err
	}
	if snapshot.Presence != PlanningRuntimePresencePresent {
		result.AlreadyAbsent = true
		return result, nil
	}
	if _, err := s.remote.RemoveSnapshotVolume(ctx, host.ID, result.VolumeName); err != nil {
		return result, planningRuntimeRemoteFailure(err, stage, "删除快照卷失败")
	}
	_, stillFound, inspectErr := s.remote.InspectSnapshotVolume(ctx, host.ID, result.VolumeName)
	if inspectErr != nil {
		return result, planningRuntimeRemoteFailure(inspectErr, stage, "删除快照卷后未能核验")
	}
	if stillFound {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			stage,
			"快照卷删除后仍然存在",
			nil,
		)
	}
	result.Removed = true
	return result, nil
}
