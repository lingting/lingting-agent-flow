package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// Validator 语义审查的错误类别。
//
// Handler 与协调器通过 errors.Is 判断类别，不解析错误文本。
var (
	// ErrReviewNotConfigured 表示任务没有配置验证角色，语义审查无法进行。
	ErrReviewNotConfigured = errors.New("app: 未配置验证角色")
)

// ReviewRequest 是一次语义审查调用的输入。
//
// 关联标识全部来自经过校验的数据库记录：调用方不能指定 Session 路径、
// 工作目录、模型或角色。
type ReviewRequest struct {
	// Task 是所属任务，提供目标、工作目录与角色配置。
	Task domain.Task
	// Step 是待审查的步骤。
	Step domain.Step
	// Attempt 是产生待审查代码的开发尝试：Validator 会话必须挂在该尝试之下。
	Attempt domain.Attempt
	// Container 是任务容器登记记录：审查在同一个容器工作区内进行。
	Container domain.Container
	// Results 是本轮确定性检查的真实结果，作为审查证据传给模型。
	Results []domain.ValidationCheckResult
	// Prompt 是可选的补充说明，只能追加在编排器生成的提示词之后。
	Prompt string
	// Timeout 是本次审查调用的超时，零值使用任务配置或服务默认值。
	Timeout time.Duration
}

// ReviewOutcome 是一次语义审查的真实结论。
//
// 它显式区分四件独立的事实：审查状态、可解释的评估、审查会话、以及失败原因。
// 调用方不得用"调用返回了文本"替代"拿到了可解释评估"。
type ReviewOutcome struct {
	// Status 是语义审查状态。
	Status domain.ReviewStatus
	// Assessment 是可解释的评估结果，仅在 Status 为 passed / failed /
	// needs_human_review 时非空。
	Assessment *domain.AgentAssessment
	// SessionID 是本次审查使用的会话标识，调用失败时同样存在（若已创建）。
	SessionID domain.ID
	// Summary 是审查说明（已脱敏并裁剪），失败时记录可诊断原因。
	Summary string
	// FinalText 是模型最终文本的摘要，仅用于诊断，不参与判定。
	FinalText string
	// StartedAt / FinishedAt 是本次审查的起止时间。
	StartedAt  time.Time
	FinishedAt time.Time
}

// ValidatorReviewer 执行一次 Validator Agent 语义审查。
//
// 职责边界：
//   - 读取任务配置的验证角色，并在调用之前完成全部前置校验；
//   - 为每次审查创建独立 Session，并在进程启动之前写入数据库；
//   - 只通过容器内 Agent 执行端口调用 Pi，不自行拼接命令；
//   - 把模型输出解析为结构化评估，绝不把自然语言结论当成通过。
//
// 它不做验证判定：最终结论只由 domain.DecideValidation 依据结构化事实计算。
type ValidatorReviewer struct {
	store      ValidatorReviewStore
	containers ReviewAgentPort
	sessions   *SessionService
	logger     *slog.Logger
	now        func() time.Time
	// defaultTimeout 是未配置超时时的单次审查调用超时。
	defaultTimeout time.Duration
}

// ReviewAgentPort 是语义审查对容器内 Agent 执行能力的需求。
//
// 它只声明"在受管理容器内执行一次 Agent 调用"：审查不核验也不操作容器生命周期，
// 前置校验由验证引擎完成，容器缺失时调用本身就会失败。方法名与容器服务的正式入口
// 保持一致，避免为同一能力引入第二个入口。
type ReviewAgentPort interface {
	RunAgent(ctx context.Context, request ContainerAgentRequest) (ContainerAgentResult, error)
}

// ValidatorReviewStore 是语义审查依赖的存储能力。
type ValidatorReviewStore interface {
	GetRole(ctx context.Context, id domain.ID) (*domain.Role, error)
}

// ValidatorReviewerOptions 是构造语义审查器的输入。
type ValidatorReviewerOptions struct {
	// Store 是持久化入口，必填。
	Store ValidatorReviewStore
	// Containers 是容器核验与容器内 Agent 调用能力，必填。
	Containers ReviewAgentPort
	// Sessions 是会话服务，必填：审查会话必须在进程启动之前写入数据库。
	Sessions *SessionService
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
	// DefaultTimeout 覆盖未配置超时时的默认调用超时。
	DefaultTimeout time.Duration
}

// 语义审查的默认超时。
const (
	// defaultReviewTimeout 是未配置超时时的单次审查调用超时。
	defaultReviewTimeout = 30 * time.Minute
	// maxReviewSummaryLength 是审查说明保留的字节上限。
	maxReviewSummaryLength = 1024
)

// NewValidatorReviewer 构造语义审查器。
func NewValidatorReviewer(options ValidatorReviewerOptions) (*ValidatorReviewer, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("语义审查器初始化失败: 存储不能为空")
	}
	if options.Containers == nil {
		return nil, fmt.Errorf("语义审查器初始化失败: 容器能力不能为空")
	}
	if options.Sessions == nil {
		return nil, fmt.Errorf("语义审查器初始化失败: 会话服务不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	timeout := options.DefaultTimeout
	if timeout <= 0 {
		timeout = defaultReviewTimeout
	}
	return &ValidatorReviewer{
		store:          options.Store,
		containers:     options.Containers,
		sessions:       options.Sessions,
		logger:         logger,
		now:            now,
		defaultTimeout: timeout,
	}, nil
}

// Review 执行一次语义审查。
//
// 它不返回 error：调用失败、超时、取消、输出不可解析都是"审查没有得到可用结论"
// 这一业务事实，必须如实写入 ReviewStatus 与 Summary，让调用方据此把最终结论收紧为
// inconclusive。只有前置条件本身无法满足（未配置验证角色）才返回错误，因为那说明
// 这一轮根本不应该发起审查。
func (r *ValidatorReviewer) Review(
	ctx context.Context,
	request ReviewRequest,
) (ReviewOutcome, error) {
	role, err := r.loadValidatorRole(ctx, request.Task)
	if err != nil {
		return ReviewOutcome{}, err
	}
	prompt, err := validatorPrompt(request.Task, request.Step, request.Results, request.Prompt)
	if err != nil {
		return ReviewOutcome{}, err
	}
	outcome := ReviewOutcome{Status: domain.ReviewStatusNotRequested, StartedAt: r.now()}
	session, err := r.sessions.Start(ctx, StartRequest{
		Scope: domain.SessionScope{
			TaskID:    request.Task.ID,
			StepID:    &request.Step.ID,
			AttemptID: &request.Attempt.ID,
		},
		Role:         role,
		InvocationID: agent.NewInvocationID(),
		ContainerID:  strings.TrimSpace(request.Container.ID.String()),
		DockerID:     request.Container.DockerID,
	})
	if err != nil {
		// 会话创建失败意味着无法留下可追溯的执行记录：这不属于"调用了但没有结论"，
		// 因此按调用失败收敛，也不启动进程。
		outcome.Status = domain.ReviewStatusCallFailed
		outcome.Summary = clipReviewText(err.Error(), maxReviewSummaryLength)
		outcome.FinishedAt = r.now()
		r.logFailure(ctx, request, outcome, err)
		return outcome, nil
	}
	outcome.SessionID = session.ID
	result, runErr := r.containers.RunAgent(ctx, ContainerAgentRequest{
		ContainerID:  request.Container.ID.String(),
		TaskID:       request.Task.ID.String(),
		StepID:       request.Step.ID.String(),
		AttemptID:    request.Attempt.ID.String(),
		RoleType:     role.Type.String(),
		Model:        role.Model,
		SystemPrompt: role.SystemPrompt,
		Prompt:       prompt,
		SessionRoot:  r.sessions.LocalRoot(),
		SessionID:    session.ID.String(),
		InvocationID: session.InvocationID,
		Timeout:      r.reviewTimeout(request.Task, request.Timeout),
		WorkDir:      request.Task.Config.EffectiveWorkDir(),
	})
	// 会话收尾必须使用未取消的上下文：取消与超时恰恰是最需要写入结果的场景。
	finished, finishErr := r.sessions.Finish(context.WithoutCancel(ctx), FinishRequest{
		SessionID: session.ID,
		Result:    result.Result,
		Archive:   archiveOf(result.Result),
	})
	if finishErr != nil {
		r.logger.WarnContext(
			ctx,
			"写入语义审查会话结果失败",
			slog.String("task_id", request.Task.ID.String()),
			slog.String("session_id", session.ID.String()),
			slog.String("error", errorSummary(finishErr)),
		)
	}
	if !finished.ID.IsEmpty() {
		outcome.SessionID = finished.ID
	}
	outcome.FinalText = agent.TruncateText(strings.TrimSpace(result.Result.FinalText), maxReviewSummaryLength)
	outcome.Status, outcome.Assessment, outcome.Summary = classifyReview(result, runErr, ctx)
	outcome.FinishedAt = r.now()
	r.logOutcome(ctx, request, outcome)
	return outcome, nil
}

// loadValidatorRole 读取并校验任务配置的验证角色。
//
// 类型不符、被禁用或未配置模型都返回前置条件错误：用开发者角色跑语义审查会让
// 模型以错误的权限与提示词运行，这不是可接受的降级。
func (r *ValidatorReviewer) loadValidatorRole(
	ctx context.Context,
	task domain.Task,
) (domain.Role, error) {
	roleID := task.Config.ValidatorRoleID
	if roleID.IsEmpty() {
		return domain.Role{}, fmt.Errorf("%w: 任务 %s 未配置验证角色", ErrReviewNotConfigured, task.ID)
	}
	role, err := r.store.GetRole(ctx, roleID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Role{}, fmt.Errorf(
				"%w: 任务 %s 关联的验证角色 %s 不存在",
				ErrReviewNotConfigured,
				task.ID,
				roleID,
			)
		}
		return domain.Role{}, fmt.Errorf("读取验证角色失败: %w", err)
	}
	if role.Type != domain.RoleTypeValidator {
		return domain.Role{}, fmt.Errorf(
			"%w: 角色 %s 的类型是 %s，语义审查需要 %s",
			ErrReviewNotConfigured,
			role.ID,
			role.Type,
			domain.RoleTypeValidator,
		)
	}
	if !role.Enabled {
		return domain.Role{}, fmt.Errorf("%w: 验证角色 %s 已被禁用", ErrReviewNotConfigured, role.ID)
	}
	if strings.TrimSpace(role.Model) == "" {
		return domain.Role{}, fmt.Errorf("%w: 验证角色 %s 未配置模型", ErrReviewNotConfigured, role.ID)
	}
	return *role, nil
}

// reviewTimeout 返回本次审查调用的超时时间。
//
// 优先级固定为：请求显式给出 > 任务配置 > 审查器默认值。
func (r *ValidatorReviewer) reviewTimeout(task domain.Task, requested time.Duration) time.Duration {
	if requested > 0 {
		return requested
	}
	if seconds := task.Config.AgentTimeoutSeconds; seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if r.defaultTimeout > 0 {
		return r.defaultTimeout
	}
	return defaultReviewTimeout
}

// classifyReview 把执行器结果映射为语义审查状态与评估。
//
// 顺序固定：先判断调用是否正常结束，再判断输出能否解析为可解释评估。任何一步
// 不成立都不会产出评估，因此也不可能有"通过"。
func classifyReview(
	result ContainerAgentResult,
	runErr error,
	ctx context.Context,
) (domain.ReviewStatus, *domain.AgentAssessment, string) {
	status := result.Result.Status
	switch status {
	case agent.RunCompleted:
		if startedErr := checkReviewStarted(result, runErr); startedErr != nil {
			return domain.ReviewStatusCallFailed, nil, clipReviewText(startedErr.Error(), maxReviewSummaryLength)
		}
	case agent.RunTimedOut:
		return domain.ReviewStatusTimedOut, nil, "语义审查调用超时"
	case agent.RunCancelled:
		if isServiceStop(ctx) {
			return domain.ReviewStatusInterrupted, nil, "服务已停止，语义审查被中断"
		}
		return domain.ReviewStatusCancelled, nil, "调用方取消语义审查"
	default:
		return domain.ReviewStatusCallFailed, nil, reviewFailureText(result, runErr)
	}
	assessment, err := domain.ParseAgentAssessment(result.Result.FinalText)
	if err != nil {
		// 领域校验失败与"根本不是结构化输出"分开记录：两者的处置动作不同
		// （前者是模型违反了协议约束，后者是模型没有按协议回答）。
		if errors.Is(err, domain.ErrAssessmentInvalid) {
			return domain.ReviewStatusInvalidOutput, nil, clipReviewText(err.Error(), maxReviewSummaryLength)
		}
		return domain.ReviewStatusUnparseable, nil, clipReviewText(err.Error(), maxReviewSummaryLength)
	}
	return reviewStatusOf(assessment), &assessment, reviewSummaryOf(assessment)
}

// checkReviewStarted 核验"成功"是否留下了可核验的运行痕迹。
//
// 与开发步骤一致：没有完整会话也没有进程标识的"成功"无法核验，不能据此解析评估。
func checkReviewStarted(result ContainerAgentResult, runErr error) error {
	if errorSummary(runErr) != "" {
		return nil
	}
	if result.Result.Session.Completeness == agent.SessionComplete {
		return nil
	}
	if result.Result.ProcessID > 0 {
		return nil
	}
	return fmt.Errorf("语义审查报告成功，但没有可核验的运行痕迹（会话不完整且缺少进程标识）")
}

// reviewFailureText 组合审查调用的失败原因。
func reviewFailureText(result ContainerAgentResult, runErr error) string {
	for _, candidate := range []string{result.Result.Failure, errorSummary(runErr)} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return clipReviewText(trimmed, maxReviewSummaryLength)
		}
	}
	return "语义审查调用未正常结束"
}

// reviewStatusOf 依据评估内容决定审查状态。
func reviewStatusOf(assessment domain.AgentAssessment) domain.ReviewStatus {
	switch {
	case !assessment.Passed:
		return domain.ReviewStatusFailed
	case assessment.NeedsHumanReview:
		return domain.ReviewStatusNeedsHumanReview
	default:
		return domain.ReviewStatusPassed
	}
}

// reviewSummaryOf 生成审查说明。
func reviewSummaryOf(assessment domain.AgentAssessment) string {
	summary := strings.TrimSpace(assessment.Summary)
	if summary == "" && assessment.Passed {
		summary = "语义审查未发现问题"
	}
	return clipReviewText(summary, maxReviewSummaryLength)
}

// logOutcome 记录语义审查结论，只写状态与摘要。
func (r *ValidatorReviewer) logOutcome(
	ctx context.Context,
	request ReviewRequest,
	outcome ReviewOutcome,
) {
	r.logger.InfoContext(
		ctx,
		"语义审查已结束",
		slog.String("task_id", request.Task.ID.String()),
		slog.String("step_id", request.Step.ID.String()),
		slog.String("attempt_id", request.Attempt.ID.String()),
		slog.String("session_id", outcome.SessionID.String()),
		slog.String("review_status", string(outcome.Status)),
		slog.Bool("has_assessment", outcome.Assessment != nil),
	)
}

// logFailure 记录语义审查前置失败。
func (r *ValidatorReviewer) logFailure(
	ctx context.Context,
	request ReviewRequest,
	outcome ReviewOutcome,
	err error,
) {
	r.logger.WarnContext(
		ctx,
		"语义审查未能完成",
		slog.String("task_id", request.Task.ID.String()),
		slog.String("step_id", request.Step.ID.String()),
		slog.String("review_status", string(outcome.Status)),
		slog.String("error", errorSummary(err)),
	)
}
