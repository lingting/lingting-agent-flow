package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 两阶段分析编排的错误类别。
//
// 调用方通过 errors.Is 判断类别并映射 HTTP 状态码，不解析错误文本。
var (
	// ErrPlanningAnalysisInvalidInput 表示请求参数不合法，或提示词超出长度上限。
	ErrPlanningAnalysisInvalidInput = errors.New("app: 规划分析请求参数非法")
	// ErrPlanningAnalysisNotFound 表示规划会话或所属项目不存在。
	ErrPlanningAnalysisNotFound = errors.New("app: 规划分析目标不存在")
	// ErrPlanningAnalysisConflict 表示会话状态不允许分析，或已被其他执行者占用。
	ErrPlanningAnalysisConflict = errors.New("app: 规划分析被占用或状态不允许")
	// ErrPlanningAnalysisAgentFailed 表示某一阶段的角色调用没有产出可用结果。
	ErrPlanningAnalysisAgentFailed = errors.New("app: 规划角色调用未产出可用结果")
	// ErrPlanningAnalysisOutputInvalid 表示某一阶段的输出没有通过结构化校验。
	ErrPlanningAnalysisOutputInvalid = errors.New("app: 规划角色输出不合法")
	// ErrPlanningAnalysisNotPersisted 表示两阶段已成功但持久化没有完成。
	ErrPlanningAnalysisNotPersisted = errors.New("app: 规划分析结果未能保存")
)

// 两阶段分析单阶段的默认调用超时。
const (
	defaultPlanningAnalystTimeout = 30 * time.Minute
	defaultPlanningPlannerTimeout = 30 * time.Minute
)

// PlanningAnalysisAgentPort 是两阶段分析对规划角色调用能力的需求。
//
// 它只声明"在规划容器内执行一次规划角色调用"：容器准备、角色配置、Session 绑定与
// Pi 执行都由 PlanningAgentService 负责，本服务不重复实现 Agent 运行时，也不直接
// 拼接命令、模型或容器标识。
type PlanningAnalysisAgentPort interface {
	RunRole(ctx context.Context, request RunPlanningRoleRequest) (PlanningAgentResult, error)
}

// PlanningAnalysisService 是两阶段规划分析的总编排入口。
//
// 职责边界：
//   - 以数据库条件更新占用一轮分析，防止同一会话并发跑两轮模型调用；
//   - 顺序调用需求分析师与方案规划师，两个阶段各自校验结构化输出；
//   - 两个阶段都成功后，在一个事务内追加 Revision、角色消息并把会话推进为待确认；
//   - 任何阶段失败都记录阶段与脱敏原因，不产生半成品 Revision。
//
// 它不创建正式 Requirement / Task / Step / Attempt，也不写规划容器内的项目文件：
// 项目内容对模型是只读的，本服务也没有任何写工作区的能力。
type PlanningAnalysisService struct {
	store  storage.Store
	agent  PlanningAnalysisAgentPort
	logger *slog.Logger
	now    func() time.Time
	// analystTimeout / plannerTimeout 是单阶段的调用超时。
	analystTimeout time.Duration
	plannerTimeout time.Duration
}

// PlanningAnalysisServiceOptions 是构造两阶段分析服务的输入。
type PlanningAnalysisServiceOptions struct {
	// Store 是持久化入口；跨实体的原子写入使用它的 WithTx。
	Store storage.Store
	// Agent 是规划角色调用入口，必填。
	Agent PlanningAnalysisAgentPort
	// Logger 是日志器，为空时使用 slog.Default。
	Logger *slog.Logger
	// Now 是时间源，默认返回 UTC 当前时间。
	Now func() time.Time
	// AnalystTimeout 是需求分析阶段的调用超时，零值使用默认值。
	AnalystTimeout time.Duration
	// PlannerTimeout 是方案规划阶段的调用超时，零值使用默认值。
	PlannerTimeout time.Duration
}

// NewPlanningAnalysisService 构造两阶段分析服务。
func NewPlanningAnalysisService(
	options PlanningAnalysisServiceOptions,
) (*PlanningAnalysisService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("规划分析服务初始化失败: 存储不能为空")
	}
	if options.Agent == nil {
		return nil, fmt.Errorf("规划分析服务初始化失败: 规划角色调用能力不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	analystTimeout := options.AnalystTimeout
	if analystTimeout <= 0 {
		analystTimeout = defaultPlanningAnalystTimeout
	}
	plannerTimeout := options.PlannerTimeout
	if plannerTimeout <= 0 {
		plannerTimeout = defaultPlanningPlannerTimeout
	}
	return &PlanningAnalysisService{
		store:          options.Store,
		agent:          options.Agent,
		logger:         logger,
		now:            now,
		analystTimeout: analystTimeout,
		plannerTimeout: plannerTimeout,
	}, nil
}

// RunPlanningAnalysisRequest 是一次两阶段分析的输入。
//
// 它只接受规划会话与可选的执行节点：提示词、角色、模型、容器与会话路径全部由服务端
// 按当前配置与记录决定。
type RunPlanningAnalysisRequest struct {
	// PlanningSessionID 是目标规划会话标识。
	PlanningSessionID string
	// HostID 仅在规划运行环境尚未准备时用于准备容器；已准备时不得指向其他执行节点。
	HostID string
}

// PlanningAnalysisResult 是一轮两阶段分析的持久化事实。
type PlanningAnalysisResult struct {
	// Session 是推进到 preview_ready 的规划会话。
	Session domain.PlanningSession
	// Revision 是本次追加的方案版本，版本号由存储层在事务内分配。
	Revision domain.PlanningRevision
	// Messages 是本次追加的两个角色消息，序号由存储层在事务内分配。
	Messages []domain.PlanningMessage
	// Analysis 是通过校验的需求分析。
	Analysis domain.RequirementAnalysis
	// Plan 是通过校验的实施方案。
	Plan domain.ImplementationPlan
	// AnalystSessionID / PlannerSessionID 是本轮实际使用的角色 Pi 会话标识。
	AnalystSessionID string
	PlannerSessionID string
	// StartedAt / FinishedAt 是本轮分析的起止时间。
	StartedAt  time.Time
	FinishedAt time.Time
}

// Analyze 执行一轮完整的两阶段分析。
//
// 执行顺序固定为：校验请求 → 以数据库条件更新占用本轮分析 → 读取会话与所属项目 →
// 需求分析阶段（调用 + 结构化校验）→ 方案规划阶段（调用 + 结构化校验）→ 单事务持久化。
//
// 占用在返回之前一定会被释放：成功路径在持久化事务内释放，失败路径在失败事务内释放，
// 其余提前返回的路径由 defer 兜底释放。占用未取得时不做任何模型调用。
func (s *PlanningAnalysisService) Analyze(
	ctx context.Context,
	request RunPlanningAnalysisRequest,
) (PlanningAnalysisResult, error) {
	sessionID, err := parsePlanningAnalysisSessionID(request.PlanningSessionID)
	if err != nil {
		return PlanningAnalysisResult{}, err
	}
	startedAt := s.now()
	claimID := domain.NewID().String()
	if err := s.store.ClaimPlanningAnalysis(ctx, sessionID, claimID, startedAt); err != nil {
		return PlanningAnalysisResult{}, s.mapClaimFailure(ctx, err, sessionID)
	}
	// 兜底释放：成功与失败路径都会在事务内释放占用，这里只处理"提前返回"的路径。
	defer s.releaseClaim(sessionID, claimID)

	session, project, err := s.loadAnalysisTarget(ctx, sessionID)
	if err != nil {
		return PlanningAnalysisResult{}, s.failAnalysis(ctx, sessionID, claimID, err)
	}
	messages, err := s.store.ListPlanningMessages(ctx, sessionID)
	if err != nil {
		return PlanningAnalysisResult{}, s.failAnalysis(
			ctx,
			sessionID,
			claimID,
			fmt.Errorf("读取规划消息失败: %w", err),
		)
	}

	analysis, err := s.runRequirementAnalysis(ctx, session, project, messages, request.HostID)
	if err != nil {
		return PlanningAnalysisResult{}, s.failAnalysis(ctx, sessionID, claimID, err)
	}
	plan, err := s.runImplementationPlan(ctx, session, project, messages, analysis, request.HostID)
	if err != nil {
		return PlanningAnalysisResult{}, s.failAnalysis(ctx, sessionID, claimID, err)
	}
	result, err := s.completeAnalysis(ctx, sessionID, claimID, analysis, plan, startedAt)
	if err != nil {
		return PlanningAnalysisResult{}, err
	}
	s.logger.InfoContext(
		ctx,
		"规划分析已完成",
		slog.String("planning_session_id", sessionID.String()),
		slog.String("revision_id", result.Revision.ID.String()),
		slog.Int("revision_number", result.Revision.RevisionNumber),
		slog.Int("task_count", len(plan.Plan.Tasks)),
	)
	return result, nil
}

// runRequirementAnalysis 执行需求分析阶段。
//
// 返回的 error 只用于表示"本阶段失败"这一事实：调用方据此把会话收敛为 failed 并带着
// 阶段说明重试，不会因为返回值而把阶段输出当成结论。
func (s *PlanningAnalysisService) runRequirementAnalysis(
	ctx context.Context,
	session domain.PlanningSession,
	project domain.Project,
	messages []domain.PlanningMessage,
	hostID string,
) (planningStageSnapshot, error) {
	stage := PlanningAnalysisStageRequirementAnalysis
	prompt, err := analystPrompt(project, session.SourceCommit, messages)
	if err != nil {
		return planningStageSnapshot{}, fmt.Errorf(
			"%w: %s提示词构造失败: %w",
			ErrPlanningAnalysisInvalidInput,
			stage.Label(),
			err,
		)
	}
	stageResult := s.runStage(ctx, session, hostID, stage, prompt)
	if !stageResult.usable() {
		return planningStageSnapshot{}, fmt.Errorf(
			"%w: %s",
			ErrPlanningAnalysisAgentFailed,
			stageResult.Failure,
		)
	}
	snapshot, parseErr := parsePlanningStageOutput(stage, stageResult.Text)
	if parseErr != nil {
		return planningStageSnapshot{}, fmt.Errorf(
			"%w: %s输出的结构化校验未通过: %w",
			ErrPlanningAnalysisOutputInvalid,
			stage.Label(),
			parseErr,
		)
	}
	return snapshot, nil
}

// runImplementationPlan 执行方案规划阶段。
//
// 它必须以需求分析快照为输入：规划师看到的是分析师产出的完整结构化结论，约束与非目标
// 因此不会被摘要省略。
func (s *PlanningAnalysisService) runImplementationPlan(
	ctx context.Context,
	session domain.PlanningSession,
	project domain.Project,
	messages []domain.PlanningMessage,
	analysis planningStageSnapshot,
	hostID string,
) (planningStageSnapshot, error) {
	stage := PlanningAnalysisStageImplementationPlan
	prompt, err := plannerPrompt(project, session.SourceCommit, analysis.Snapshot, messages)
	if err != nil {
		return planningStageSnapshot{}, fmt.Errorf(
			"%w: %s提示词构造失败: %w",
			ErrPlanningAnalysisInvalidInput,
			stage.Label(),
			err,
		)
	}
	stageResult := s.runStage(ctx, session, hostID, stage, prompt)
	if !stageResult.usable() {
		return planningStageSnapshot{}, fmt.Errorf(
			"%w: %s",
			ErrPlanningAnalysisAgentFailed,
			stageResult.Failure,
		)
	}
	snapshot, parseErr := parsePlanningStageOutput(stage, stageResult.Text)
	if parseErr != nil {
		return planningStageSnapshot{}, fmt.Errorf(
			"%w: %s输出的结构化校验未通过: %w",
			ErrPlanningAnalysisOutputInvalid,
			stage.Label(),
			parseErr,
		)
	}
	return snapshot, nil
}

// runStage 执行一次已准备好提示词的阶段调用。
//
// 超时由本服务施加在上下文上：RunRole 不接受超时参数，也不允许调用方覆盖模型、容器或
// 会话路径，因此"这一阶段最多跑多久"由编排层决定。
func (s *PlanningAnalysisService) runStage(
	ctx context.Context,
	session domain.PlanningSession,
	hostID string,
	stage PlanningAnalysisStage,
	prompt string,
) planningStageResult {
	roleType, err := stage.RoleType()
	if err != nil {
		return planningStageResult{Stage: stage, Failure: err.Error()}
	}
	stageCtx, cancel := context.WithTimeout(ctx, s.stageTimeout(stage))
	defer cancel()
	result, runErr := s.agent.RunRole(stageCtx, RunPlanningRoleRequest{
		PlanningSessionID: session.ID.String(),
		RoleType:          roleType.String(),
		Prompt:            prompt,
		HostID:            hostID,
	})
	classified := classifyPlanningStage(stage, result, runErr, planningStopReason(stageCtx))
	if !classified.usable() {
		s.logger.WarnContext(
			ctx,
			"规划分析阶段未产出可用结果",
			slog.String("planning_session_id", session.ID.String()),
			slog.String("stage", string(stage)),
			slog.String("failure", classified.Failure),
		)
	}
	return classified
}

// stageTimeout 返回阶段的调用超时。
func (s *PlanningAnalysisService) stageTimeout(stage PlanningAnalysisStage) time.Duration {
	switch stage {
	case PlanningAnalysisStageImplementationPlan:
		return s.plannerTimeout
	default:
		return s.analystTimeout
	}
}

// loadAnalysisTarget 读取并校验本轮分析的目标会话与项目。
func (s *PlanningAnalysisService) loadAnalysisTarget(
	ctx context.Context,
	sessionID domain.ID,
) (domain.PlanningSession, domain.Project, error) {
	session, err := readPlanningSession(ctx, s.store, sessionID)
	if err != nil {
		if errors.Is(err, ErrPlanningSessionNotFound) {
			return domain.PlanningSession{}, domain.Project{}, fmt.Errorf(
				"%w: 规划会话 %s 不存在",
				ErrPlanningAnalysisNotFound,
				sessionID,
			)
		}
		return domain.PlanningSession{}, domain.Project{}, err
	}
	// 占用已经证明状态是 analyzing；这里再校验一次，避免读取路径与占用路径出现分歧时
	// 用过期对象继续跑模型调用。
	if session.Status != domain.PlanningSessionAnalyzing {
		return domain.PlanningSession{}, domain.Project{}, fmt.Errorf(
			"%w: 规划会话处于 %s，只有 analyzing 会话可以执行分析",
			ErrPlanningAnalysisConflict,
			session.Status,
		)
	}
	projectID := strings.TrimSpace(session.ProjectID)
	project, err := s.store.GetProject(ctx, domain.ID(projectID))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.PlanningSession{}, domain.Project{}, fmt.Errorf(
				"%w: 规划会话所属项目 %s 不存在或已删除",
				ErrPlanningAnalysisNotFound,
				projectID,
			)
		}
		return domain.PlanningSession{}, domain.Project{}, fmt.Errorf("读取规划会话所属项目失败: %w", err)
	}
	if project.Deleted() {
		return domain.PlanningSession{}, domain.Project{}, fmt.Errorf(
			"%w: 项目 %s 已删除，不能继续分析",
			ErrPlanningAnalysisNotFound,
			projectID,
		)
	}
	return session, *project, nil
}

// completeAnalysis 在一个事务内保存两个阶段的结果。
//
// 执行顺序固定为：读取并确认会话处于 analyzing → 追加方案版本 → 追加两个角色消息 →
// MarkPreviewReady → 以 analyzing 为前置条件写回 → 释放占用。任一步失败都整体回滚：
// 不会留下没有状态的版本，也不会出现"未完成的输出被标记为待确认"。
func (s *PlanningAnalysisService) completeAnalysis(
	ctx context.Context,
	sessionID domain.ID,
	claimID string,
	analysis planningStageSnapshot,
	plan planningStageSnapshot,
	startedAt time.Time,
) (PlanningAnalysisResult, error) {
	validation, err := domain.PlanningValidationResult{
		SnapshotVersion:          domain.PlanningRevisionSnapshotVersion,
		RequirementAnalysisValid: true,
		ImplementationPlanValid:  true,
		TaskCount:                len(plan.Plan.Tasks),
	}.Snapshot()
	if err != nil {
		return PlanningAnalysisResult{}, s.failAnalysis(
			ctx,
			sessionID,
			claimID,
			fmt.Errorf("生成结构化校验结论失败: %w", err),
		)
	}
	result := PlanningAnalysisResult{Analysis: analysis.Analysis, Plan: plan.Plan}
	at := s.now()
	err = s.store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		loaded, err := readPlanningSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if loaded.Status != domain.PlanningSessionAnalyzing {
			return fmt.Errorf(
				"%w: 规划会话处于 %s，只有运行中的分析才能收敛结果",
				ErrPlanningAnalysisConflict,
				loaded.Status,
			)
		}
		revision, err := tx.AppendPlanningRevision(ctx, storage.AppendPlanningRevisionRequest{
			PlanningSessionID:       sessionID,
			RequirementAnalysisJSON: analysis.Snapshot,
			ImplementationPlanJSON:  plan.Snapshot,
			ValidationResultJSON:    validation,
			At:                      at,
		})
		if err != nil {
			return mapPlanningRevisionFailure(err)
		}
		messages, err := s.appendStageMessages(ctx, tx, sessionID, analysis, plan, at)
		if err != nil {
			return err
		}
		if err := loaded.MarkPreviewReady(at); err != nil {
			return mapPlanningTransitionFailure(err)
		}
		// 迟到结果不能覆盖其他操作已产生的状态：条件更新失败时版本与消息一起回滚。
		if err := tx.UpdatePlanningSessionIfStatus(
			ctx,
			loaded,
			[]domain.PlanningSessionStatus{domain.PlanningSessionAnalyzing},
		); err != nil {
			return mapPlanningUpdateFailure(err, sessionID)
		}
		if err := tx.ReleasePlanningAnalysis(ctx, sessionID, claimID, at); err != nil {
			return mapPlanningClaimFailure(err, sessionID)
		}
		result.Session = loaded
		result.Revision = revision
		result.Messages = messages
		return nil
	})
	if err != nil {
		s.logger.ErrorContext(
			ctx,
			"规划分析结果保存失败",
			slog.String("planning_session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		return PlanningAnalysisResult{}, fmt.Errorf("%w: %w", ErrPlanningAnalysisNotPersisted, err)
	}
	result.AnalystSessionID = result.Session.AnalystSessionID
	result.PlannerSessionID = result.Session.PlannerSessionID
	result.StartedAt = startedAt
	result.FinishedAt = at
	return result, nil
}

// appendStageMessages 追加两个阶段的角色消息。
//
// 消息正文是规范化快照文本：它与 Revision 中保存的 JSON 完全一致，用户界面可以直接展示，
// 也不会出现"消息里是模型原文、版本里是另一次解析结果"的两套事实。
func (s *PlanningAnalysisService) appendStageMessages(
	ctx context.Context,
	tx storage.Tx,
	sessionID domain.ID,
	analysis planningStageSnapshot,
	plan planningStageSnapshot,
	at time.Time,
) ([]domain.PlanningMessage, error) {
	stages := []planningStageSnapshot{analysis, plan}
	messages := make([]domain.PlanningMessage, 0, len(stages))
	for _, stage := range stages {
		roleType, err := stage.Stage.RoleType()
		if err != nil {
			return nil, err
		}
		roleID, err := planningRoleID(roleType)
		if err != nil {
			return nil, err
		}
		message, err := tx.AppendPlanningMessage(ctx, storage.AppendPlanningMessageRequest{
			PlanningSessionID: sessionID,
			SenderType:        senderTypeOf(roleType),
			RoleID:            &roleID,
			Content:           string(stage.Snapshot),
			At:                at,
		})
		if err != nil {
			return nil, fmt.Errorf("追加规划消息失败: %w", err)
		}
		messages = append(messages, message)
	}
	return messages, nil
}

// senderTypeOf 把规划角色类型映射为消息发送方。
func senderTypeOf(roleType domain.RoleType) domain.PlanningSenderType {
	if roleType == domain.RoleTypePlanner {
		return domain.PlanningSenderPlanner
	}
	return domain.PlanningSenderRequirementAnalyst
}

// failAnalysis 在一个事务内记录本轮失败并释放占用。
//
// 失败摘要经 domain.ClipFeedbackText 脱敏与裁剪：只保留定位本轮失败所需的信息，不保存
// 模型密钥、私钥、SSH 凭据或完整环境变量。失败不写方案版本，也不写成功消息。
//
// 写入使用未取消的上下文：取消与超时恰恰是最需要留下失败结论的场景。
func (s *PlanningAnalysisService) failAnalysis(
	ctx context.Context,
	sessionID domain.ID,
	claimID string,
	cause error,
) error {
	summary := cause.Error()
	writeCtx := context.WithoutCancel(ctx)
	at := s.now()
	err := s.store.WithTx(writeCtx, func(ctx context.Context, tx storage.Tx) error {
		loaded, err := readPlanningSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if err := loaded.Fail(clipAnalysisFailure(summary), at); err != nil {
			return mapPlanningTransitionFailure(err)
		}
		if err := tx.UpdatePlanningSessionIfStatus(
			ctx,
			loaded,
			[]domain.PlanningSessionStatus{domain.PlanningSessionAnalyzing},
		); err != nil {
			return mapPlanningUpdateFailure(err, sessionID)
		}
		if err := tx.ReleasePlanningAnalysis(ctx, sessionID, claimID, at); err != nil {
			return mapPlanningClaimFailure(err, sessionID)
		}
		return nil
	})
	if err != nil {
		s.logger.ErrorContext(
			writeCtx,
			"记录规划分析失败状态失败",
			slog.String("planning_session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		return errors.Join(cause, fmt.Errorf("%w: %w", ErrPlanningAnalysisNotPersisted, err))
	}
	s.logger.WarnContext(
		writeCtx,
		"规划分析失败",
		slog.String("planning_session_id", sessionID.String()),
		slog.String("summary", clipAnalysisFailure(summary)),
	)
	return cause
}

// releaseClaim 兜底释放本轮分析的占用。
//
// 占用已被事务内释放时返回 storage.ErrConflict，这不是异常：它恰好说明释放已经完成。
func (s *PlanningAnalysisService) releaseClaim(sessionID domain.ID, claimID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 5*time.Second)
	defer cancel()
	err := s.store.ReleasePlanningAnalysis(ctx, sessionID, claimID, s.now())
	if err == nil || errors.Is(err, storage.ErrConflict) || errors.Is(err, storage.ErrNotFound) {
		return
	}
	s.logger.Warn(
		"释放规划分析占用失败",
		slog.String("planning_session_id", sessionID.String()),
		slog.String("error", err.Error()),
	)
}

// mapClaimFailure 把抢占失败映射为业务错误。
//
// 抢占条件不成立有两种原因：会话状态不允许（不在 analyzing），或已有其他执行者占用。
// 为让调用方区分"会话不存在"，这里在冲突时重新读取一次会话。
func (s *PlanningAnalysisService) mapClaimFailure(
	ctx context.Context,
	err error,
	sessionID domain.ID,
) error {
	if !errors.Is(err, storage.ErrConflict) {
		return fmt.Errorf("抢占规划分析执行权失败: %w", err)
	}
	session, readErr := s.store.GetPlanningSession(ctx, sessionID)
	if readErr != nil {
		if errors.Is(readErr, storage.ErrNotFound) {
			return fmt.Errorf("%w: 规划会话 %s 不存在", ErrPlanningAnalysisNotFound, sessionID)
		}
		return fmt.Errorf("读取规划会话失败: %w", readErr)
	}
	return fmt.Errorf(
		"%w: 规划会话 %s 处于 %s，无法开始新一轮分析（正在运行或其他操作已推进状态）",
		ErrPlanningAnalysisConflict,
		sessionID,
		session.Status,
	)
}

// mapPlanningClaimFailure 把占用释放的失败映射为业务错误。
func mapPlanningClaimFailure(err error, sessionID domain.ID) error {
	switch {
	case errors.Is(err, storage.ErrConflict):
		return fmt.Errorf("%w: 规划会话 %s 的分析占用已不属于本次运行", ErrPlanningAnalysisConflict, sessionID)
	case errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("%w: %s", ErrPlanningAnalysisNotFound, sessionID)
	default:
		return fmt.Errorf("释放规划分析执行权失败: %w", err)
	}
}

// clipAnalysisFailure 脱敏并裁剪失败摘要。
func clipAnalysisFailure(summary string) string {
	clipped := domain.ClipFeedbackText(summary, domain.MaxFeedbackReason)
	if clipped == "" {
		return "本轮分析失败"
	}
	return clipped
}

// planningStopReason 依据阶段上下文判断调用被中断的原因。
//
// 它只解释取消与超时：其余情况由 Pi 的真实执行状态描述，不由编排层推断。
func planningStopReason(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		return "阶段执行超时"
	case errors.Is(cause, context.Canceled):
		return "调用方取消分析"
	default:
		return ""
	}
}

// parsePlanningAnalysisSessionID 校验规划会话标识的书写形式。
func parsePlanningAnalysisSessionID(value string) (domain.ID, error) {
	sessionID := domain.ID(strings.TrimSpace(value))
	if err := sessionID.Validate(); err != nil {
		return "", fmt.Errorf("%w: 规划会话标识不能为空", ErrPlanningAnalysisInvalidInput)
	}
	return sessionID, nil
}
