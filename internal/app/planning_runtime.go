package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 规划运行时错误类别。
//
// 类别只表达"调用方应该怎么处理"，具体失败位置由 PlanningRuntimeError.Stage 给出：
// 参数问题、目标不存在、前置条件、现场冲突与结果不确定必须能区分，不能压缩成
// 同一个"准备失败"。
var (
	// ErrPlanningRuntimeInvalidInput 表示请求参数不合法。
	ErrPlanningRuntimeInvalidInput = errors.New("app: 规划运行时请求参数非法")
	// ErrPlanningRuntimeNotFound 表示规划会话、项目或执行节点不存在。
	ErrPlanningRuntimeNotFound = errors.New("app: 规划运行时目标不存在")
	// ErrPlanningRuntimePrerequisite 表示执行节点、环境版本或工作区前置条件不满足。
	ErrPlanningRuntimePrerequisite = errors.New("app: 规划运行时前置条件未满足")
	// ErrPlanningRuntimeConflict 表示会话记录与远程实际资源不一致，或资源归属不成立。
	ErrPlanningRuntimeConflict = errors.New("app: 规划运行环境现场不一致")
	// ErrPlanningRuntimeUncertain 表示远程命令未能确认结束，现场必须保留并先核验。
	ErrPlanningRuntimeUncertain = errors.New("app: 规划运行时远程状态无法确认")
	// ErrPlanningRuntimeFailed 表示执行已明确失败。
	ErrPlanningRuntimeFailed = errors.New("app: 规划运行时执行失败")
	// ErrPlanningRuntimeNotPersisted 表示远程副作用已发生，但会话关联未能写入数据库。
	ErrPlanningRuntimeNotPersisted = errors.New("app: 规划运行时结果未能持久化")
)

// PlanningRuntimeStage 是规划运行环境的失败阶段。
type PlanningRuntimeStage string

// 规划运行环境的失败阶段取值。
const (
	// PlanningRuntimeStageValidate 表示读取与校验规划会话。
	PlanningRuntimeStageValidate PlanningRuntimeStage = "validate"
	// PlanningRuntimeStageHost 表示校验与定位执行节点。
	PlanningRuntimeStageHost PlanningRuntimeStage = "host"
	// PlanningRuntimeStageEnvironment 表示解析项目语言环境与版本。
	PlanningRuntimeStageEnvironment PlanningRuntimeStage = "environment"
	// PlanningRuntimeStagePersistHost 表示持久化已选择的执行节点。
	PlanningRuntimeStagePersistHost PlanningRuntimeStage = "persist_host"
	// PlanningRuntimeStageSnapshot 表示准备固定 commit 快照卷。
	PlanningRuntimeStageSnapshot PlanningRuntimeStage = "snapshot"
	// PlanningRuntimeStageSnapshotVerify 表示只读核验快照内容。
	PlanningRuntimeStageSnapshotVerify PlanningRuntimeStage = "snapshot_verify"
	// PlanningRuntimeStagePersistSnapshot 表示持久化固定 commit SHA。
	PlanningRuntimeStagePersistSnapshot PlanningRuntimeStage = "persist_snapshot"
	// PlanningRuntimeStageContainer 表示创建只读规划容器。
	PlanningRuntimeStageContainer PlanningRuntimeStage = "container"
	// PlanningRuntimeStageContainerReuse 表示核验并复用已登记的规划容器。
	PlanningRuntimeStageContainerReuse PlanningRuntimeStage = "container_reuse"
	// PlanningRuntimeStagePersistContainer 表示持久化规划容器登记 ID。
	PlanningRuntimeStagePersistContainer PlanningRuntimeStage = "persist_container"
	// PlanningRuntimeStageReleaseContainer 表示删除规划容器。
	PlanningRuntimeStageReleaseContainer PlanningRuntimeStage = "release_container"
	// PlanningRuntimeStageReleaseSnapshot 表示删除快照卷。
	PlanningRuntimeStageReleaseSnapshot PlanningRuntimeStage = "release_snapshot"
	// PlanningRuntimeStageClearBinding 表示清空会话的运行时绑定。
	PlanningRuntimeStageClearBinding PlanningRuntimeStage = "clear_binding"
)

// PlanningRuntimeError 是带阶段的规划运行时错误。
//
// 它只保存可公开的诊断信息：阶段名、说明与底层错误；不包含凭据、私钥或完整日志。
type PlanningRuntimeError struct {
	// Stage 是失败阶段。
	Stage PlanningRuntimeStage
	// Detail 是可读的失败原因。
	Detail string
	// Cause 是底层错误，可通过 errors.Is 判断超时、取消或远程状态不确定。
	Cause error
}

// Error 实现 error 接口。
func (e *PlanningRuntimeError) Error() string {
	detail := strings.TrimSpace(e.Detail)
	if detail == "" {
		detail = "规划运行环境操作失败"
	}
	if e.Cause != nil {
		return fmt.Sprintf("规划运行环境 %s 阶段失败: %s: %v", e.Stage, detail, e.Cause)
	}
	return fmt.Sprintf("规划运行环境 %s 阶段失败: %s", e.Stage, detail)
}

// Unwrap 暴露底层错误。
func (e *PlanningRuntimeError) Unwrap() error {
	return e.Cause
}

// planningRuntimeFailure 构造"错误类别 + 阶段"的复合错误。
func planningRuntimeFailure(
	kind error,
	stage PlanningRuntimeStage,
	detail string,
	cause error,
) error {
	return fmt.Errorf("%w: %w", kind, &PlanningRuntimeError{Stage: stage, Detail: detail, Cause: cause})
}

// PlanningRuntimeService 负责规划会话的运行环境。
//
// 职责边界：
//   - 按固定 commit SHA 准备项目代码快照卷，并创建只读规划容器；
//   - 同一会话复用同一份快照与同一个规划容器，不同会话互相隔离；
//   - 只释放本会话自己拥有的容器与快照卷；
//   - Prepare 与 Release 都按规划会话串行，与 PlanningAgentService 的 RunRole 共享
//     同一个协调器实例，因此同一会话不会同时准备、执行或释放。
//
// 它不推进 PlanningSession 状态、不执行 AI 调用、不写 PlanningMessage / Revision，
// 也不进入任务执行、代码保存或提交合并流程。运行环境准备成功不等于会话进入
// analyzing 或 preview_ready。
type PlanningRuntimeService struct {
	// store 是持久化入口：会话与项目读取、运行时绑定写入都经由它。
	store storage.Store
	// containers 提供规划容器的创建、复用与核验能力。
	containers *ContainerService
	// release 是释放路径使用的容器能力：生产中包装 containers，测试可注入受控替身。
	release PlanningRuntimeReleasePort
	// coordination 按规划会话串行化 Prepare / Release：同一会话不得并发创建第二个容器。
	//
	// 它与 PlanningAgentService 共用同一个实例，因此角色调用与运行环境操作互斥：一个
	// 入口执行 Pi 时另一个入口不能删除容器与快照卷。
	coordination *PlanningSessionCoordinator
}

// PlanningRuntimeServiceOptions 是构造规划运行时服务的输入。
type PlanningRuntimeServiceOptions struct {
	// Store 是持久化入口，必填。
	Store storage.Store
	// Containers 是受管理容器服务，提供规划容器的创建、复用与核验能力。
	//
	// 释放路径只有一种情况可以缺省它：调用方显式提供了 ReleasePort。
	Containers *ContainerService
	// Coordination 是规划会话的进程内协调器，必填。
	//
	// 它与 PlanningAgentService 共用同一个实例：RunRole、Prepare 与 Release 必须串行
	// 同一会话，否则一个入口准备容器时另一个入口可以执行 Pi 或释放资源。
	Coordination *PlanningSessionCoordinator
	// ReleasePort 覆盖释放路径使用的容器能力，零值时包装 Containers。
	//
	// 它只是依赖测试缝隙：生产装配总是留空，释放路径因此在生产中始终由 ContainerService
	// 执行，不存在第二条容器或 SSH 调用链。
	ReleasePort PlanningRuntimeReleasePort
}

// NewPlanningRuntimeService 构造规划运行时服务。
func NewPlanningRuntimeService(
	options PlanningRuntimeServiceOptions,
) (*PlanningRuntimeService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("规划运行时初始化失败: 存储不能为空")
	}
	if options.Coordination == nil {
		return nil, fmt.Errorf("规划运行时初始化失败: 规划会话协调器不能为空")
	}
	service := &PlanningRuntimeService{
		store:        options.Store,
		containers:   options.Containers,
		coordination: options.Coordination,
	}
	switch {
	case options.ReleasePort != nil:
		service.release = options.ReleasePort
	case options.Containers != nil:
		service.release = planningRuntimeContainerRelease{containers: options.Containers}
	default:
		return nil, fmt.Errorf("规划运行时初始化失败: 必须提供容器服务或释放能力")
	}
	return service, nil
}

// PreparePlanningRuntimeRequest 是准备规划运行环境的输入。
//
// 它只接受标识：容器名、卷名、镜像引用、工作目录与远程命令全部由服务端根据数据库
// 记录生成，调用方无法指定。固定 commit 也不由调用方提供。
type PreparePlanningRuntimeRequest struct {
	// PlanningSessionID 是规划会话标识。
	PlanningSessionID string
	// HostID 是本次使用的执行节点；会话已绑定时必须与已绑定节点一致。
	HostID string
}

// PlanningRuntimeDetail 是规划运行环境的当前事实。
type PlanningRuntimeDetail struct {
	// Session 是写入运行期字段后的规划会话。
	Session domain.PlanningSession
	// Host 是承载规划容器的执行节点。
	Host domain.ExecutionHost
	// Container 是受管理的规划容器记录。
	Container domain.Container
	// CodeVolumeName 是承载固定 commit 快照的命名卷。
	CodeVolumeName string
	// SourceCommit 是快照固定的完整 commit SHA。
	SourceCommit string
	// EnvironmentVersion 是规划容器使用的语言环境版本。
	EnvironmentVersion domain.EnvironmentVersion
	// SnapshotCreated 表示本次新建了快照卷。
	SnapshotCreated bool
	// SnapshotReused 表示复用并核验了已有快照卷。
	SnapshotReused bool
	// ContainerReused 表示复用并核验了已有规划容器。
	ContainerReused bool
}

// ReleasePlanningRuntimeRequest 是释放规划运行环境的输入。
type ReleasePlanningRuntimeRequest struct {
	// PlanningSessionID 是规划会话标识。
	PlanningSessionID string
}

// ReleasePlanningRuntimeResult 是释放结果。
type ReleasePlanningRuntimeResult struct {
	// Session 是清空运行时绑定后的规划会话。
	Session domain.PlanningSession
	// ContainerReleased 表示规划容器已确认删除或已不存在。
	ContainerReleased bool
	// SnapshotReleased 表示快照卷已确认删除或已不存在。
	SnapshotReleased bool
}

// Prepare 准备或复用规划运行环境。
//
// 执行顺序固定为：按会话串行 → 校验会话与项目 → 校验执行节点与环境版本 → 持久化执行
// 节点 → 准备并核验固定 commit 快照 → 持久化 commit → 创建或复用规划容器 → 持久化
// 容器关联 → 释放会话锁。远程副作用之前的失败不会留下任何资源，远程副作用之后的失败
// 不会清空已写入的定位信息。
//
// 会话锁覆盖整个准备过程：另一个入口既不能同时准备同一个会话的运行环境，也不能在准备
// 期间释放它的容器与快照卷。已经持有会话锁的调用方（`RunRole`）走 prepareHeld，不会
// 重复加锁。
func (s *PlanningRuntimeService) Prepare(
	ctx context.Context,
	request PreparePlanningRuntimeRequest,
) (detail PlanningRuntimeDetail, err error) {
	sessionID, hostID, err := parsePlanningRuntimeTarget(request.PlanningSessionID, request.HostID)
	if err != nil {
		return detail, planningRuntimeFailure(
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
		return detail, fmt.Errorf("准备规划运行环境在取得执行权之前被取消: %w", err)
	}
	return s.prepareRuntime(ctx, sessionID, hostID)
}

// prepareHeld 在调用方已经持有本会话锁时准备运行环境。
//
// 它既不取锁也不解锁：RunRole 已经持有会话锁，内部准备只负责原有准备流程，因此不会
// 出现重复加锁或自锁。
func (s *PlanningRuntimeService) prepareHeld(
	ctx context.Context,
	request PreparePlanningRuntimeRequest,
) (PlanningRuntimeDetail, error) {
	detail := PlanningRuntimeDetail{}
	sessionID, hostID, err := parsePlanningRuntimeTarget(request.PlanningSessionID, request.HostID)
	if err != nil {
		return detail, planningRuntimeFailure(
			ErrPlanningRuntimeInvalidInput,
			PlanningRuntimeStageValidate,
			"规划运行时请求非法",
			err,
		)
	}
	return s.prepareRuntime(ctx, sessionID, hostID)
}

// prepareRuntime 执行已取得会话锁的准备流程。
func (s *PlanningRuntimeService) prepareRuntime(
	ctx context.Context,
	sessionID domain.ID,
	hostID domain.ID,
) (PlanningRuntimeDetail, error) {
	detail := PlanningRuntimeDetail{}
	if s.containers == nil {
		return detail, planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			PlanningRuntimeStageValidate,
			"规划运行时没有配置容器服务，不能准备运行环境",
			nil,
		)
	}
	session, err := s.loadPlanningSession(ctx, sessionID)
	if err != nil {
		return detail, err
	}
	if err := session.Validate(); err != nil {
		return detail, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageValidate,
			"规划会话记录不满足领域约束",
			err,
		)
	}
	if session.Status.IsTerminal() {
		return detail, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageValidate,
			"规划会话已结束（"+string(session.Status)+"），不能再准备运行环境",
			nil,
		)
	}
	if session.ContainerID != "" && session.HostID == nil {
		return detail, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageValidate,
			"规划会话已登记容器但缺少执行节点，无法定位资源",
			nil,
		)
	}
	if session.ContainerID != "" && strings.TrimSpace(session.SourceCommit) == "" {
		return detail, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageValidate,
			"规划会话已登记容器但缺少固定 commit，不能复用运行环境",
			nil,
		)
	}
	project, err := s.loadPlanningProject(ctx, session)
	if err != nil {
		return detail, err
	}
	host, err := s.planningHost(ctx, session, hostID)
	if err != nil {
		return detail, err
	}
	version, err := s.planningEnvironmentVersion(ctx, project, host)
	if err != nil {
		return detail, err
	}
	workspace, err := planningReadOnlyWorkspace(project)
	if err != nil {
		return detail, planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			PlanningRuntimeStageEnvironment,
			"规划容器工作区配置非法",
			err,
		)
	}
	// 远程副作用之前先落盘执行节点，后续失败才能定位本会话的资源。
	if session.HostID == nil {
		updated, persistErr := s.persistRuntime(ctx, session, &host.ID, "", "")
		if persistErr != nil {
			return detail, planningRuntimeBindingFailure(
				persistErr,
				PlanningRuntimeStagePersistHost,
				"执行节点未能写入规划会话",
			)
		}
		session = updated
	}
	detail = PlanningRuntimeDetail{Session: session, Host: host, EnvironmentVersion: version}

	snapshot, err := s.ensurePlanningSnapshot(ctx, planningSnapshotRequest{
		session: session,
		project: project,
		host:    host,
		version: version,
	})
	if err != nil {
		return detail, err
	}
	detail.CodeVolumeName = snapshot.VolumeName
	detail.SourceCommit = snapshot.Commit
	detail.SnapshotCreated = snapshot.Created
	detail.SnapshotReused = snapshot.Reused

	if snapshot.Commit != session.SourceCommit {
		updated, persistErr := s.persistRuntime(
			context.WithoutCancel(ctx),
			session,
			&host.ID,
			session.ContainerID,
			snapshot.Commit,
		)
		if persistErr != nil {
			return detail, planningRuntimeBindingFailure(
				persistErr,
				PlanningRuntimeStagePersistSnapshot,
				"快照已准备但固定 commit 未能写入规划会话",
			)
		}
		session = updated
		detail.Session = session
	}

	containerRequest := planningContainerRequest{
		session:    session,
		project:    project,
		host:       host,
		version:    version,
		volumeName: snapshot.VolumeName,
		workspace:  workspace,
	}
	var container domain.Container
	if session.ContainerID != "" {
		container, err = s.reusePlanningContainer(ctx, containerRequest)
		if err != nil {
			return detail, err
		}
		detail.ContainerReused = true
	} else {
		container, err = s.createPlanningContainer(ctx, containerRequest)
		if container.ID == "" || container.State != domain.ContainerStateRunning {
			detail.Container = container
			if docker.ErrorCodeOf(err) == docker.CodeStateUncertain ||
				docker.ErrorCodeOf(err) == docker.CodeSSHUnavailable {
				return detail, planningRuntimeFailure(
					ErrPlanningRuntimeUncertain,
					PlanningRuntimeStageContainer,
					"规划容器的创建结果无法确认，请先核验远程容器",
					err,
				)
			}
			return detail, planningRuntimeFailure(
				ErrPlanningRuntimeFailed,
				PlanningRuntimeStageContainer,
				"规划容器未能创建并进入运行状态",
				err,
			)
		}
		updated, persistErr := s.persistRuntime(
			context.WithoutCancel(ctx),
			session,
			&host.ID,
			container.ID.String(),
			snapshot.Commit,
		)
		if persistErr != nil {
			detail.Container = container
			// 本次创建的容器尚未被任何会话引用：按确定失败清理，避免留下孤儿容器。
			cleanupErr := s.deleteUnboundPlanningContainer(context.WithoutCancel(ctx), container)
			return detail, planningRuntimeBindingFailure(
				errors.Join(persistErr, cleanupErr),
				PlanningRuntimeStagePersistContainer,
				"规划容器的会话关联未能持久化",
			)
		}
		session = updated
		detail.Session = session
		if err != nil {
			// 容器已核验运行且关联已写入，只有远程登记清单同步失败：如实返回错误，
			// 但保留会话关联，调用方再次 Prepare 会复用同一个容器。
			detail.Container = container
			return detail, planningRuntimeFailure(
				ErrPlanningRuntimeFailed,
				PlanningRuntimeStageContainer,
				"规划容器已就绪，但远程登记清单同步失败",
				err,
			)
		}
	}
	detail.Container = container
	detail.Session = session
	return detail, nil
}

// Release 释放本规划会话自己拥有的规划容器与快照卷。
//
// 顺序固定为：按会话串行 → 删除规划容器 → 确认已删除或已不存在 → 删除本会话快照卷 →
// 确认删除 → 清空会话的运行时绑定 → 释放会话锁。任一步失败都保留数据库中的定位信息
// （HostID / ContainerID / SourceCommit），不做自动重试，也不删除任何不属于本会话的资源。
//
// 会话锁必须先取得再动手：另一个入口正在执行 Pi 或归档时不得删除容器与快照卷，而"先删除
// 再检查"会让在途调用在半途丢失运行环境。
func (s *PlanningRuntimeService) Release(
	ctx context.Context,
	request ReleasePlanningRuntimeRequest,
) (result ReleasePlanningRuntimeResult, err error) {
	sessionID := domain.ID(strings.TrimSpace(request.PlanningSessionID))
	if err := sessionID.Validate(); err != nil {
		return result, planningRuntimeFailure(
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
		return result, fmt.Errorf("释放规划运行环境在取得执行权之前被取消: %w", err)
	}
	return s.releaseRuntime(ctx, sessionID)
}

// releaseRuntime 执行已取得会话锁的释放流程。
func (s *PlanningRuntimeService) releaseRuntime(
	ctx context.Context,
	sessionID domain.ID,
) (ReleasePlanningRuntimeResult, error) {
	result := ReleasePlanningRuntimeResult{}

	session, err := s.loadPlanningSession(ctx, sessionID)
	if err != nil {
		return result, err
	}
	if err := session.Validate(); err != nil {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageValidate,
			"规划会话记录不满足领域约束",
			err,
		)
	}
	result.Session = session
	if session.HostID == nil {
		if session.ContainerID != "" || strings.TrimSpace(session.SourceCommit) != "" {
			return result, planningRuntimeFailure(
				ErrPlanningRuntimeConflict,
				PlanningRuntimeStageValidate,
				"规划会话记录了容器或快照但没有执行节点，无法定位资源",
				nil,
			)
		}
		return result, nil
	}
	host, err := s.release.LoadExecutionHost(ctx, session.HostID.String())
	if err != nil {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			PlanningRuntimeStageReleaseContainer,
			"读取规划会话的执行节点失败",
			err,
		)
	}
	var registryWarning error
	if session.ContainerID != "" {
		released, warning, releaseErr := s.releasePlanningContainer(ctx, session)
		if releaseErr != nil {
			return result, releaseErr
		}
		result.ContainerReleased = released
		registryWarning = warning
	}
	releasedVolume, releaseErr := s.release.RemoveSnapshotVolume(ctx, session, host)
	if releaseErr != nil {
		return result, errors.Join(registryWarning, releaseErr)
	}
	result.SnapshotReleased = releasedVolume

	updated, persistErr := s.persistRuntime(context.WithoutCancel(ctx), session, nil, "", "")
	if persistErr != nil {
		return result, planningRuntimeBindingFailure(
			persistErr,
			PlanningRuntimeStageClearBinding,
			"资源已释放但会话的运行时绑定未能清空",
		)
	}
	result.Session = updated
	return result, registryWarning
}

// parsePlanningRuntimeTarget 校验会话与执行节点标识的书写形式。
func parsePlanningRuntimeTarget(sessionIDValue, hostIDValue string) (domain.ID, domain.ID, error) {
	sessionID := domain.ID(strings.TrimSpace(sessionIDValue))
	if err := sessionID.Validate(); err != nil {
		return "", "", err
	}
	hostID := domain.ID(strings.TrimSpace(hostIDValue))
	if err := hostID.Validate(); err != nil {
		return "", "", err
	}
	return sessionID, hostID, nil
}

// loadPlanningSession 读取并返回规划会话。
func (s *PlanningRuntimeService) loadPlanningSession(
	ctx context.Context,
	sessionID domain.ID,
) (domain.PlanningSession, error) {
	session, err := s.store.GetPlanningSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.PlanningSession{}, planningRuntimeFailure(
				ErrPlanningRuntimeNotFound,
				PlanningRuntimeStageValidate,
				"规划会话不存在 "+sessionID.String(),
				err,
			)
		}
		return domain.PlanningSession{}, fmt.Errorf("读取规划会话失败: %w", err)
	}
	return *session, nil
}

// loadPlanningProject 读取并校验规划会话所属项目。
func (s *PlanningRuntimeService) loadPlanningProject(
	ctx context.Context,
	session domain.PlanningSession,
) (domain.Project, error) {
	projectID := domain.ID(strings.TrimSpace(session.ProjectID))
	if err := projectID.Validate(); err != nil {
		return domain.Project{}, planningRuntimeFailure(
			ErrPlanningRuntimeInvalidInput,
			PlanningRuntimeStageValidate,
			"规划会话的项目标识非法",
			err,
		)
	}
	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Project{}, planningRuntimeFailure(
				ErrPlanningRuntimeNotFound,
				PlanningRuntimeStageValidate,
				"规划会话所属项目不存在 "+projectID.String(),
				err,
			)
		}
		return domain.Project{}, fmt.Errorf("读取规划会话所属项目失败: %w", err)
	}
	if project.Deleted() {
		return domain.Project{}, planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			PlanningRuntimeStageValidate,
			"项目 "+projectID.String()+" 已删除，不能准备规划运行环境",
			nil,
		)
	}
	if project.ID != session.ProjectID {
		return domain.Project{}, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageValidate,
			"规划会话与项目不一致，拒绝把草案落到另一个项目",
			nil,
		)
	}
	return *project, nil
}

// planningHost 校验执行节点可用，并拒绝把已绑定的会话迁移到另一个节点。
func (s *PlanningRuntimeService) planningHost(
	ctx context.Context,
	session domain.PlanningSession,
	hostID domain.ID,
) (domain.ExecutionHost, error) {
	host, err := s.containers.loadHost(ctx, hostID.String())
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.ExecutionHost{}, planningRuntimeFailure(
				ErrPlanningRuntimeNotFound,
				PlanningRuntimeStageHost,
				"执行节点不存在 "+hostID.String(),
				err,
			)
		}
		return domain.ExecutionHost{}, fmt.Errorf("读取执行节点失败: %w", err)
	}
	if err := EnsureHostUsableForRemoteTask(host); err != nil {
		return domain.ExecutionHost{}, planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			PlanningRuntimeStageHost,
			"执行节点不可用于真实远程操作",
			err,
		)
	}
	if session.HostID != nil && *session.HostID != host.ID {
		return domain.ExecutionHost{}, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageHost,
			"规划会话已绑定执行节点 "+session.HostID.String()+"，不能改用 "+host.ID.String(),
			nil,
		)
	}
	return host, nil
}

// planningEnvironmentVersion 解析项目语言环境在本节点上的可用版本。
//
// 没有构建成功的版本时直接失败：不构建、不修改版本，也不回退到宿主机执行。
func (s *PlanningRuntimeService) planningEnvironmentVersion(
	ctx context.Context,
	project domain.Project,
	host domain.ExecutionHost,
) (domain.EnvironmentVersion, error) {
	number, err := planningProjectVersionNumber(project)
	if err != nil {
		return domain.EnvironmentVersion{}, planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			PlanningRuntimeStageEnvironment,
			"项目环境版本标签非法",
			err,
		)
	}
	if project.Environment.IsBase() {
		return domain.EnvironmentVersion{}, planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			PlanningRuntimeStageEnvironment,
			"项目必须使用语言环境，基础环境不能用于规划容器",
			nil,
		)
	}
	version, err := s.containers.resolveUsableVersion(ctx, host.ID, project.Environment, number)
	if err != nil {
		return domain.EnvironmentVersion{}, planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			PlanningRuntimeStageEnvironment,
			"项目语言环境没有可用版本，不能准备规划运行环境",
			err,
		)
	}
	return version, nil
}

// planningProjectVersionNumber 返回项目锁定的环境版本号，空值表示当前可用版本。
func planningProjectVersionNumber(project domain.Project) (int, error) {
	label := strings.TrimSpace(project.EnvironmentVersion)
	if label == "" {
		return 0, nil
	}
	return domain.ParseVersionLabel(label)
}

// persistRuntime 把运行时绑定写入规划会话。
//
// 它只写 HostID / ContainerID / SourceCommit 与更新时间，不改变会话状态：运行环境准备
// 本身不等于会话进入 analyzing 或 preview_ready。写入在单个事务内完成，并且先在事务内
// 重新读取会话——返回的对象因此带有数据库当前的业务状态（状态、采纳的方案版本、角色
// 会话标识与失败摘要），不会因为调用方手里的是旧对象而丢失并发业务更新。
//
// 更新以调用方读到的运行时绑定为前置条件：绑定已被其他操作改写时返回错误，绝不用旧
// 绑定覆盖新事实。清空绑定不受会话是否结束的限制（已确认或已放弃的会话同样需要释放
// 运行时资源），绑定新资源则拒绝已结束的会话——确认或放弃之后不得再把资源绑回去。
func (s *PlanningRuntimeService) persistRuntime(
	ctx context.Context,
	session domain.PlanningSession,
	hostID *domain.ID,
	containerID string,
	sourceCommit string,
) (domain.PlanningSession, error) {
	expected := storage.PlanningSessionRuntimeBindingOf(session)
	var updated domain.PlanningSession
	err := s.store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		current, err := tx.GetPlanningSession(ctx, session.ID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return fmt.Errorf("%w: 规划会话 %s 不存在", ErrPlanningRuntimeNotFound, session.ID)
			}
			return fmt.Errorf("读取规划会话失败: %w", err)
		}
		candidate := *current
		at := time.Now()
		if err := candidate.BindRuntime(hostID, containerID, sourceCommit, at); err != nil {
			return err
		}
		next := storage.PlanningSessionRuntimeBindingOf(candidate)
		if !next.Empty() && candidate.Status.IsTerminal() {
			return fmt.Errorf(
				"%w: 规划会话处于 %s，不能再绑定运行时资源",
				ErrPlanningRuntimeConflict,
				candidate.Status,
			)
		}
		if err := tx.UpdatePlanningSessionRuntimeBinding(
			ctx,
			session.ID,
			expected,
			next,
			at,
		); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return fmt.Errorf("%w: 规划会话 %s 不存在", ErrPlanningRuntimeNotFound, session.ID)
			}
			return err
		}
		updated = candidate
		return nil
	})
	if err != nil {
		return session, err
	}
	return updated, nil
}

// planningRuntimeBindingFailure 把运行时绑定写入失败映射为运行时错误类别。
//
// 绑定已被其他操作改写不是"写不进去"，而是"不能用旧快照覆盖新事实"：调用方必须重新
// 读取会话与远程现场，因此归入冲突。会话在写入过程中被删除时保持 NotFound 语义；其余
// 失败才是真正未能持久化。
func planningRuntimeBindingFailure(
	err error,
	stage PlanningRuntimeStage,
	detail string,
) error {
	switch {
	case errors.Is(err, ErrPlanningRuntimeConflict), errors.Is(err, storage.ErrConflict):
		return planningRuntimeFailure(ErrPlanningRuntimeConflict, stage, detail, err)
	case errors.Is(err, ErrPlanningRuntimeNotFound), errors.Is(err, storage.ErrNotFound):
		return planningRuntimeFailure(ErrPlanningRuntimeNotFound, stage, detail, err)
	default:
		return planningRuntimeFailure(ErrPlanningRuntimeNotPersisted, stage, detail, err)
	}
}
