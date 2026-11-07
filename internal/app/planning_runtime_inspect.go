package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// PlanningRuntimeResourcePresence 是单个远程资源的核验结论。
//
// 四个取值必须能区分"事实"与"推断"：远程读取失败、超时与解析失败只能落到 unknown，
// 不得被映射为 absent；标签与记录不一致落到 mismatch，它说明现场存在需要人工处置的冲突。
type PlanningRuntimeResourcePresence string

// 远程资源核验结论取值。
const (
	// PlanningRuntimePresencePresent 表示已确认资源存在。
	PlanningRuntimePresencePresent PlanningRuntimeResourcePresence = "present"
	// PlanningRuntimePresenceAbsent 表示已确认资源不存在。
	PlanningRuntimePresenceAbsent PlanningRuntimeResourcePresence = "absent"
	// PlanningRuntimePresenceUnknown 表示无法确认资源是否存在。
	PlanningRuntimePresenceUnknown PlanningRuntimeResourcePresence = "unknown"
	// PlanningRuntimePresenceMismatch 表示资源存在或记录存在，但归属不成立。
	PlanningRuntimePresenceMismatch PlanningRuntimeResourcePresence = "mismatch"
)

// InspectPlanningRuntimeRequest 是规划运行时现场核验的输入。
type InspectPlanningRuntimeRequest struct {
	// PlanningSessionID 是规划会话标识。
	PlanningSessionID string
}

// PlanningRuntimeContainerInspection 是单个容器的现场核验结论。
type PlanningRuntimeContainerInspection struct {
	// Presence 是核验结论。
	Presence PlanningRuntimeResourcePresence
	// Name 是关键定位名称：规划容器用登记名称，准备容器用服务端生成的固定名称。
	Name string
	// DockerID 是远端实际容器 ID，未能确认时为空。
	DockerID string
	// DockerState 是远端 Docker 状态。
	DockerState domain.DockerContainerState
	// Running 表示远端容器已确认处于运行状态（State.Status 为 running 且 State.Running 为真）。
	Running bool
	// Detail 是补充说明，只包含可公开的诊断信息。
	Detail string
}

// PlanningRuntimeSnapshotInspection 是固定 commit 快照卷的现场核验结论。
type PlanningRuntimeSnapshotInspection struct {
	// Presence 是核验结论。
	Presence PlanningRuntimeResourcePresence
	// VolumeName 是由会话与执行节点推导出的快照卷名称。
	VolumeName string
	// Commit 是只读核验得到的实际 commit，未能确认时为空。
	Commit string
	// CommitVerified 表示远端实际 commit 已经核验。
	CommitVerified bool
	// CommitMatches 表示实际 commit 与数据库 SourceCommit 一致。
	//
	// 数据库尚未记录固定 commit 时该字段为 false：此时 Commit 仍然有效，恢复入口据此
	// 写入固定 commit。
	CommitMatches bool
	// Detail 是补充说明，只包含可公开的诊断信息。
	Detail string
}

// PlanningRuntimeInspection 是规划运行时的现场核验结果。
//
// 即使返回错误，结果中已经确认的事实仍然有效：冲突与不确定分别通过错误类别表达，资源
// 的存在性由每个资源的 Presence 单独给出，调用方不需要从错误文本推断现场。
type PlanningRuntimeInspection struct {
	// Session 是数据库中的规划会话记录（核验不改写它）。
	Session domain.PlanningSession
	// Host 是会话绑定的执行节点；会话未绑定节点时为空。
	Host *domain.ExecutionHost
	// Container 是登记规划容器的现场。
	Container PlanningRuntimeContainerInspection
	// PrepContainer 是一次性快照准备容器的现场。
	PrepContainer PlanningRuntimeContainerInspection
	// Snapshot 是快照卷的现场。
	Snapshot PlanningRuntimeSnapshotInspection
}

// InspectPlanningRuntime 只读核验规划运行时的远端现场。
//
// 执行顺序：按会话串行 → 读取会话与运行时绑定 → 解析执行节点 → 核验规划容器、一次性准备
// 容器与快照卷 → 释放会话锁。核验覆盖数据库绑定、规划容器实际状态、快照卷存在性与标签、
// 一次性准备容器的存在与运行状态，以及远端快照实际 commit 与 SourceCommit 的一致性。
//
// 本方法不创建、启动、停止或删除规划资源，也不修改规划会话状态与运行时绑定：快照 commit
// 的核验以前台 `--rm` 只读容器执行，结束后容器自动删除，不留下需要清理的远程资源。
//
// 错误语义：参数、会话与节点不存在、数据库绑定自相矛盾、标签不一致与 commit 不一致分别
// 返回对应类别；远程读取失败、超时与解析失败按"状态无法确认"上报，绝不被当作资源不存在。
// 返回结果同时携带已经确认的事实，调用方可以据此决定恢复、清理或释放。
func (s *PlanningRuntimeService) InspectPlanningRuntime(
	ctx context.Context,
	request InspectPlanningRuntimeRequest,
) (PlanningRuntimeInspection, error) {
	inspection := PlanningRuntimeInspection{}
	sessionID, err := parsePlanningRuntimeSessionID(request.PlanningSessionID)
	if err != nil {
		return inspection, planningRuntimeFailure(
			ErrPlanningRuntimeInvalidInput,
			PlanningRuntimeStageValidate,
			"规划运行时请求非法",
			err,
		)
	}
	unlock := s.coordination.Lock(sessionID)
	defer unlock()
	// 排队期间调用方取消：还没有取得执行权，如实按取消返回。
	if err := ctx.Err(); err != nil {
		return inspection, fmt.Errorf("核验规划运行时现场在取得执行权之前被取消: %w", err)
	}
	session, err := s.loadPlanningSession(ctx, sessionID)
	if err != nil {
		return inspection, err
	}
	inspection.Session = session
	if err := session.Validate(); err != nil {
		return inspection, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageValidate,
			"规划会话记录不满足领域约束",
			err,
		)
	}
	if session.ContainerID != "" && session.HostID == nil {
		return inspection, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageValidate,
			"规划会话已登记容器但缺少执行节点，无法定位资源",
			nil,
		)
	}
	if session.HostID == nil {
		// 准备流程在创建任何远程资源之前先落盘执行节点，因此未绑定节点时本会话不可能拥有
		// 远程资源。这里给出的是数据库事实，不是把探测失败当成不存在。
		inspection.Container = PlanningRuntimeContainerInspection{
			Presence: PlanningRuntimePresenceAbsent,
			Detail:   "规划会话未绑定执行节点",
		}
		inspection.PrepContainer = PlanningRuntimeContainerInspection{
			Presence: PlanningRuntimePresenceAbsent,
			Detail:   "规划会话未绑定执行节点",
		}
		inspection.Snapshot = PlanningRuntimeSnapshotInspection{
			Presence: PlanningRuntimePresenceAbsent,
			Detail:   "规划会话未绑定执行节点",
		}
		return inspection, nil
	}
	host, err := s.loadRuntimeHost(ctx, *session.HostID)
	if err != nil {
		return inspection, err
	}
	inspection.Host = &host

	var problems []error
	container, containerErr := s.inspectRuntimeContainer(ctx, session, host)
	inspection.Container = container
	if containerErr != nil {
		problems = append(problems, containerErr)
	}
	prep, prepErr := s.inspectRuntimePrepContainer(ctx, session, host, PlanningRuntimeStageInspect)
	inspection.PrepContainer = prep
	if prepErr != nil {
		problems = append(problems, prepErr)
	}
	snapshot, snapshotErr := s.inspectRuntimeSnapshot(ctx, session, host)
	inspection.Snapshot = snapshot
	if snapshotErr != nil {
		problems = append(problems, snapshotErr)
	}
	return inspection, errors.Join(problems...)
}

// inspectRuntimeContainer 核验登记规划容器的远端现场。
func (s *PlanningRuntimeService) inspectRuntimeContainer(
	ctx context.Context,
	session domain.PlanningSession,
	host domain.ExecutionHost,
) (PlanningRuntimeContainerInspection, error) {
	stage := PlanningRuntimeStageInspect
	if strings.TrimSpace(session.ContainerID) == "" {
		return PlanningRuntimeContainerInspection{
			Presence: PlanningRuntimePresenceAbsent,
			Detail:   "规划会话未登记规划容器",
		}, nil
	}
	inspection := PlanningRuntimeContainerInspection{Presence: PlanningRuntimePresenceUnknown}
	containerID := domain.ID(strings.TrimSpace(session.ContainerID))
	if err := containerID.Validate(); err != nil {
		inspection.Presence = PlanningRuntimePresenceMismatch
		inspection.Detail = "规划会话的容器标识非法"
		return inspection, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			inspection.Detail,
			err,
		)
	}
	record, err := s.store.GetContainer(ctx, containerID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			inspection.Presence = PlanningRuntimePresenceMismatch
			inspection.Detail = "规划容器登记记录不存在，无法确认远端容器的归属"
			return inspection, planningRuntimeFailure(
				ErrPlanningRuntimeConflict,
				stage,
				inspection.Detail,
				err,
			)
		}
		inspection.Detail = "读取规划容器登记记录失败"
		return inspection, planningRuntimeFailure(ErrPlanningRuntimeFailed, stage, inspection.Detail, err)
	}
	inspection.Name = record.Name
	if record.HostID != host.ID {
		inspection.Presence = PlanningRuntimePresenceMismatch
		inspection.Detail = "规划容器登记记录与规划会话的执行节点不一致"
		return inspection, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			inspection.Detail,
			nil,
		)
	}
	detail, err := s.remote.InspectManagedContainer(ctx, record.ID.String())
	if err != nil {
		switch docker.ErrorCodeOf(err) {
		case docker.CodeContainerNotFound:
			inspection.Presence = PlanningRuntimePresenceAbsent
			inspection.Detail = "远端已不存在该规划容器"
			return inspection, nil
		case docker.CodeOwnershipMismatch:
			inspection.Presence = PlanningRuntimePresenceMismatch
			inspection.Detail = "规划容器的远端标签与登记记录不一致"
			return inspection, planningRuntimeFailure(
				ErrPlanningRuntimeConflict,
				stage,
				inspection.Detail,
				err,
			)
		}
		inspection.Detail = "核验规划容器状态失败"
		return inspection, planningRuntimeRemoteFailure(err, stage, inspection.Detail)
	}
	if detail.Inspect == nil {
		inspection.Detail = "未能取得规划容器的实际状态"
		return inspection, planningRuntimeFailure(
			ErrPlanningRuntimeUncertain,
			stage,
			inspection.Detail,
			nil,
		)
	}
	inspect := detail.Inspect
	if actual := inspect.Labels[docker.PlanningSessionLabelKey]; actual != session.ID.String() {
		inspection.Presence = PlanningRuntimePresenceMismatch
		inspection.Detail = "规划容器的规划会话标签与当前会话不一致"
		return inspection, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			inspection.Detail,
			fmt.Errorf("期望 %s，实际 %s", session.ID, actual),
		)
	}
	inspection.Presence = PlanningRuntimePresencePresent
	inspection.DockerID = inspect.ID
	inspection.DockerState = inspect.State
	inspection.Running = inspect.IsActivelyRunning()
	return inspection, nil
}

// inspectRuntimePrepContainer 核验一次性准备容器的远端现场。
func (s *PlanningRuntimeService) inspectRuntimePrepContainer(
	ctx context.Context,
	session domain.PlanningSession,
	host domain.ExecutionHost,
	stage PlanningRuntimeStage,
) (PlanningRuntimeContainerInspection, error) {
	name, err := planningPrepContainerName(session.ID, host.ID)
	if err != nil {
		return PlanningRuntimeContainerInspection{Presence: PlanningRuntimePresenceUnknown}, planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			stage,
			"生成一次性准备容器名称失败",
			err,
		)
	}
	inspection := PlanningRuntimeContainerInspection{
		Presence: PlanningRuntimePresenceUnknown,
		Name:     name,
	}
	inspect, found, err := s.remote.InspectPrepContainer(ctx, host.ID, name)
	if err != nil {
		inspection.Detail = "核验一次性准备容器失败"
		return inspection, planningRuntimeRemoteFailure(err, stage, inspection.Detail)
	}
	if !found {
		inspection.Presence = PlanningRuntimePresenceAbsent
		return inspection, nil
	}
	if err := verifyPlanningPrepContainer(inspect, name, session.ID, host.ID); err != nil {
		inspection.Presence = PlanningRuntimePresenceMismatch
		inspection.Detail = "一次性准备容器的归属与当前规划会话不一致"
		return inspection, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			inspection.Detail,
			err,
		)
	}
	inspection.Presence = PlanningRuntimePresencePresent
	inspection.DockerID = inspect.ID
	inspection.DockerState = inspect.State
	inspection.Running = inspect.IsActivelyRunning()
	return inspection, nil
}

// inspectRuntimeSnapshot 核验快照卷并比对远端实际 commit 与数据库记录。
func (s *PlanningRuntimeService) inspectRuntimeSnapshot(
	ctx context.Context,
	session domain.PlanningSession,
	host domain.ExecutionHost,
) (PlanningRuntimeSnapshotInspection, error) {
	inspection, err := s.verifyRuntimeSnapshot(ctx, session, host, PlanningRuntimeStageInspect, true)
	if err != nil || inspection.Presence != PlanningRuntimePresencePresent || !inspection.CommitVerified {
		return inspection, err
	}
	expected := strings.TrimSpace(session.SourceCommit)
	if expected == "" {
		inspection.Detail = "规划会话尚未记录固定 commit，远端快照实际 commit 已核验"
		return inspection, nil
	}
	if inspection.Commit != expected {
		inspection.Detail = "远端快照 commit " + inspection.Commit + " 与已记录的 " + expected + " 不一致"
		return inspection, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageInspect,
			inspection.Detail,
			nil,
		)
	}
	inspection.CommitMatches = true
	return inspection, nil
}

// verifyRuntimeSnapshot 核验快照卷的存在性、归属，并按需核验远端实际 commit。
//
// 它只读取远端事实：不存在时返回 absent 且没有错误，归属不成立返回冲突，读取失败按无法
// 确认上报。commit 核验需要项目工作目录与可用环境版本，两者都只用于定位已有资源，因此
// 项目被逻辑删除不影响核验。
func (s *PlanningRuntimeService) verifyRuntimeSnapshot(
	ctx context.Context,
	session domain.PlanningSession,
	host domain.ExecutionHost,
	stage PlanningRuntimeStage,
	verifyCommit bool,
) (PlanningRuntimeSnapshotInspection, error) {
	volumeName := planningSnapshotVolumeName(session.ID, host.ID)
	inspection := PlanningRuntimeSnapshotInspection{
		Presence:   PlanningRuntimePresenceUnknown,
		VolumeName: volumeName,
	}
	volume, found, err := s.remote.InspectSnapshotVolume(ctx, host.ID, volumeName)
	if err != nil {
		inspection.Detail = "核验快照卷失败"
		return inspection, planningRuntimeRemoteFailure(err, stage, inspection.Detail)
	}
	if !found {
		inspection.Presence = PlanningRuntimePresenceAbsent
		return inspection, nil
	}
	if err := verifyPlanningSnapshotVolume(volume, volumeName, session.ID, host.ID); err != nil {
		inspection.Presence = PlanningRuntimePresenceMismatch
		inspection.Detail = "快照卷归属与当前规划会话不一致"
		return inspection, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			inspection.Detail,
			err,
		)
	}
	inspection.Presence = PlanningRuntimePresencePresent
	if !verifyCommit {
		return inspection, nil
	}
	target, err := s.planningSnapshotTarget(ctx, session, host, volumeName, stage)
	if err != nil {
		inspection.Detail = "无法定位快照核验所需的项目与环境版本"
		return inspection, err
	}
	commit, verifyErr := s.remote.VerifySnapshotCommit(ctx, target)
	if verifyErr != nil {
		inspection.Detail = "快照实际 commit 无法确认"
		return inspection, planningRuntimeStagedOrFailed(verifyErr, stage, inspection.Detail)
	}
	inspection.Commit = commit
	inspection.CommitVerified = true
	return inspection, nil
}

// planningSnapshotTarget 构造只读快照核验的定位信息。
//
// 项目被逻辑删除后仍然需要核验与清理现场，因此这里直接读取项目记录，不套用 Prepare 的
// "项目必须可用"准入：工作目录与镜像只用于定位已有资源。
func (s *PlanningRuntimeService) planningSnapshotTarget(
	ctx context.Context,
	session domain.PlanningSession,
	host domain.ExecutionHost,
	volumeName string,
	stage PlanningRuntimeStage,
) (PlanningRuntimeSnapshotTarget, error) {
	projectID := domain.ID(strings.TrimSpace(session.ProjectID))
	if err := projectID.Validate(); err != nil {
		return PlanningRuntimeSnapshotTarget{}, planningRuntimeFailure(
			ErrPlanningRuntimeInvalidInput,
			stage,
			"规划会话的项目标识非法",
			err,
		)
	}
	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return PlanningRuntimeSnapshotTarget{}, planningRuntimeFailure(
				ErrPlanningRuntimeNotFound,
				stage,
				"规划会话所属项目不存在 "+projectID.String(),
				err,
			)
		}
		return PlanningRuntimeSnapshotTarget{}, fmt.Errorf("读取规划会话所属项目失败: %w", err)
	}
	version, err := s.planningEnvironmentVersion(ctx, *project, host)
	if err != nil {
		return PlanningRuntimeSnapshotTarget{}, err
	}
	return PlanningRuntimeSnapshotTarget{
		SessionID:  session.ID,
		HostID:     host.ID,
		VolumeName: volumeName,
		ImageRef:   version.ImageRef,
		WorkDir:    project.EffectiveWorkDir(),
	}, nil
}
