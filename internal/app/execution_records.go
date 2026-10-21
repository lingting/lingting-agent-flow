package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 执行记录查询接口的错误类别。
//
// 与任务管理接口一致：Handler 通过 errors.Is 判断类别并映射 HTTP 状态码，
// 不解析错误文本。
var (
	// ErrExecutionRecordNotFound 表示步骤、尝试或会话不存在，或不属于指定任务。
	//
	// "不属于指定任务"与"不存在"返回同一错误：两者都不能让调用方通过状态码差异
	// 探测其他任务是否存在执行记录。
	ErrExecutionRecordNotFound = errors.New("app: 执行记录不存在")
	// ErrExecutionRecordInvalidInput 表示分页或筛选参数不合法。
	ErrExecutionRecordInvalidInput = errors.New("app: 执行记录查询参数非法")
)

// ExecutionRecordService 提供 Step / Attempt / Session 的只读查询。
//
// 该服务不创建执行记录、不推进任何状态：本阶段没有真实执行器，任何"开始执行"
// 或"重试"入口都必须等执行协调器实现后再设计。
type ExecutionRecordService struct {
	store storage.Store
}

// NewExecutionRecordService 构造执行记录查询服务。
func NewExecutionRecordService(store storage.Store) (*ExecutionRecordService, error) {
	if store == nil {
		return nil, fmt.Errorf("执行记录服务初始化失败: 存储不能为空")
	}
	return &ExecutionRecordService{store: store}, nil
}

// ExecutionPageRequest 是执行记录列表的分页参数。
//
// 与任务列表使用同一套分页语义：Current 从 1 开始，Size 默认 20、上限 100。
type ExecutionPageRequest struct {
	// Current 是页号，从 1 开始；小于等于 0 时使用 1。
	Current int
	// Size 是页大小；小于等于 0 时使用默认值，超过上限时被拒绝。
	Size int
	// Statuses 按状态筛选，为空表示不限制。
	Statuses []string
}

// normalize 校验并归一化分页参数，返回生效的 limit 与 offset。
func (r ExecutionPageRequest) normalize() (limit, offset int, err error) {
	size := r.Size
	if size <= 0 {
		size = DefaultTaskPageSize
	}
	if size > MaxTaskPageSize {
		return 0, 0, fmt.Errorf(
			"%w: 页大小不能超过 %d",
			ErrExecutionRecordInvalidInput,
			MaxTaskPageSize,
		)
	}
	current := r.Current
	if current <= 0 {
		current = 1
	}
	return size, (current - 1) * size, nil
}

// ListSteps 返回任务下的步骤分页。
//
// 任务不存在或已逻辑删除时返回 ErrExecutionRecordNotFound：不存在的任务不应该
// 返回一个空列表，否则调用方无法区分"任务没有步骤"与"任务不存在"。
func (s *ExecutionRecordService) ListSteps(
	ctx context.Context,
	taskID string,
	request ExecutionPageRequest,
) (storage.StepPage, error) {
	id, err := s.requireTask(ctx, taskID)
	if err != nil {
		return storage.StepPage{}, err
	}
	limit, offset, err := request.normalize()
	if err != nil {
		return storage.StepPage{}, err
	}
	statuses := make([]domain.StepStatus, 0, len(request.Statuses))
	for _, raw := range request.Statuses {
		status := domain.StepStatus(strings.TrimSpace(raw))
		if err := status.Validate(); err != nil {
			return storage.StepPage{}, fmt.Errorf("%w: %w", ErrExecutionRecordInvalidInput, err)
		}
		statuses = append(statuses, status)
	}
	page, err := s.store.ListStepPage(ctx, storage.StepFilter{
		TaskID:   id,
		Statuses: statuses,
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		return storage.StepPage{}, fmt.Errorf("查询步骤列表失败: %w", err)
	}
	return page, nil
}

// ListAttempts 返回某步骤下的尝试分页。
//
// 步骤必须真实属于该任务：只校验步骤存在是不够的，否则可以用 A 任务的路径读取
// B 任务的步骤。
func (s *ExecutionRecordService) ListAttempts(
	ctx context.Context,
	taskID, stepID string,
	request ExecutionPageRequest,
) (storage.AttemptPage, error) {
	step, err := s.requireStep(ctx, taskID, stepID)
	if err != nil {
		return storage.AttemptPage{}, err
	}
	limit, offset, err := request.normalize()
	if err != nil {
		return storage.AttemptPage{}, err
	}
	statuses := make([]domain.AttemptStatus, 0, len(request.Statuses))
	for _, raw := range request.Statuses {
		status := domain.AttemptStatus(strings.TrimSpace(raw))
		if err := status.Validate(); err != nil {
			return storage.AttemptPage{}, fmt.Errorf("%w: %w", ErrExecutionRecordInvalidInput, err)
		}
		statuses = append(statuses, status)
	}
	page, err := s.store.ListAttemptPage(ctx, storage.AttemptFilter{
		StepID:   step.ID,
		Statuses: statuses,
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		return storage.AttemptPage{}, fmt.Errorf("查询执行尝试列表失败: %w", err)
	}
	return page, nil
}

// ValidationHistoryEntry 是一次验证记录及其检查结果。
type ValidationHistoryEntry struct {
	// Run 是验证记录。
	Run domain.ValidationRun
	// Checks 是该次验证的逐项检查结果。
	Checks []domain.ValidationCheckResult
}

// ValidationHistory 是某个步骤的验证历史。
//
// 历史记录只追加不覆盖：调用方取最后一条即为最新验证结果，取全部即为验证历史。
// State 是从最新记录派生出的步骤验证状态，使"是否有验证""验证是否通过"不需要调用方
// 自己从原始记录推断。
type ValidationHistory struct {
	// StepID 是步骤标识。
	StepID domain.ID
	// State 是派生出的步骤验证状态，没有记录时为 unverified。
	State domain.StepVerificationState
	// Entries 按创建时间升序排列，最后一条是最新验证。
	Entries []ValidationHistoryEntry
}

// ListValidations 返回某步骤的验证历史。
func (s *ExecutionRecordService) ListValidations(
	ctx context.Context,
	taskID, stepID string,
) (ValidationHistory, error) {
	step, err := s.requireStep(ctx, taskID, stepID)
	if err != nil {
		return ValidationHistory{}, err
	}
	runs, err := s.store.ListValidationRunsByStep(ctx, step.ID)
	if err != nil {
		return ValidationHistory{}, fmt.Errorf("查询验证记录失败: %w", err)
	}
	history := ValidationHistory{
		StepID:  step.ID,
		State:   domain.StepVerificationUnverified,
		Entries: make([]ValidationHistoryEntry, 0, len(runs)),
	}
	for _, run := range runs {
		checks, checkErr := s.store.ListValidationCheckResults(ctx, run.ID)
		if checkErr != nil {
			return ValidationHistory{}, fmt.Errorf("查询检查结果失败: %w", checkErr)
		}
		history.Entries = append(history.Entries, ValidationHistoryEntry{
			Run:    run,
			Checks: checks,
		})
	}
	if len(history.Entries) > 0 {
		latest := history.Entries[len(history.Entries)-1]
		history.State = domain.DeriveStepVerificationState(&latest.Run, latest.Checks)
	}
	return history, nil
}

// ListSessions 返回某次尝试下的会话分页。
//
// 每一次 Planner、Developer、Validator 调用都对应一个独立会话，因此同一次尝试下
// 可以出现多个不同角色的会话。
func (s *ExecutionRecordService) ListSessions(
	ctx context.Context,
	taskID, stepID, attemptID string,
	request ExecutionPageRequest,
) (storage.SessionPage, error) {
	attempt, err := s.requireAttempt(ctx, taskID, stepID, attemptID)
	if err != nil {
		return storage.SessionPage{}, err
	}
	limit, offset, err := request.normalize()
	if err != nil {
		return storage.SessionPage{}, err
	}
	statuses := make([]domain.SessionStatus, 0, len(request.Statuses))
	for _, raw := range request.Statuses {
		status := domain.SessionStatus(strings.TrimSpace(raw))
		if err := status.Validate(); err != nil {
			return storage.SessionPage{}, fmt.Errorf("%w: %w", ErrExecutionRecordInvalidInput, err)
		}
		statuses = append(statuses, status)
	}
	page, err := s.store.ListSessionPage(ctx, storage.SessionFilter{
		TaskID:    attempt.TaskID,
		StepID:    &attempt.StepID,
		AttemptID: &attempt.ID,
		Statuses:  statuses,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		return storage.SessionPage{}, fmt.Errorf("查询会话列表失败: %w", err)
	}
	return page, nil
}

// GetSession 返回属于指定任务的单个会话详情。
//
// 会话存在但属于其他任务时返回 ErrExecutionRecordNotFound，不泄露其他任务的数据。
func (s *ExecutionRecordService) GetSession(
	ctx context.Context,
	taskID, sessionID string,
) (domain.Session, error) {
	id, err := s.requireTask(ctx, taskID)
	if err != nil {
		return domain.Session{}, err
	}
	parsed, err := parseRecordID(sessionID)
	if err != nil {
		return domain.Session{}, err
	}
	session, err := s.store.GetSession(ctx, parsed)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Session{}, fmt.Errorf(
				"%w: %s",
				ErrExecutionRecordNotFound,
				parsed.String(),
			)
		}
		return domain.Session{}, fmt.Errorf("读取会话失败: %w", err)
	}
	if session.TaskID != id {
		return domain.Session{}, fmt.Errorf(
			"%w: %s",
			ErrExecutionRecordNotFound,
			parsed.String(),
		)
	}
	return *session, nil
}

// requireTask 读取任务并确认它未被逻辑删除。
func (s *ExecutionRecordService) requireTask(
	ctx context.Context,
	taskID string,
) (domain.ID, error) {
	id, err := parseRecordID(taskID)
	if err != nil {
		return "", err
	}
	task, err := s.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", fmt.Errorf("%w: 任务 %s", ErrExecutionRecordNotFound, id.String())
		}
		return "", fmt.Errorf("读取任务失败: %w", err)
	}
	if task.Deleted() {
		return "", fmt.Errorf("%w: 任务 %s", ErrExecutionRecordNotFound, id.String())
	}
	return id, nil
}

// requireStep 读取步骤并确认它属于指定任务。
func (s *ExecutionRecordService) requireStep(
	ctx context.Context,
	taskID, stepID string,
) (domain.Step, error) {
	id, err := s.requireTask(ctx, taskID)
	if err != nil {
		return domain.Step{}, err
	}
	parsed, err := parseRecordID(stepID)
	if err != nil {
		return domain.Step{}, err
	}
	step, err := s.store.GetStep(ctx, parsed)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Step{}, fmt.Errorf(
				"%w: 步骤 %s",
				ErrExecutionRecordNotFound,
				parsed.String(),
			)
		}
		return domain.Step{}, fmt.Errorf("读取步骤失败: %w", err)
	}
	if step.TaskID != id {
		return domain.Step{}, fmt.Errorf(
			"%w: 步骤 %s",
			ErrExecutionRecordNotFound,
			parsed.String(),
		)
	}
	return *step, nil
}

// requireAttempt 读取尝试并确认它的任务与步骤归属一致。
func (s *ExecutionRecordService) requireAttempt(
	ctx context.Context,
	taskID, stepID, attemptID string,
) (domain.Attempt, error) {
	step, err := s.requireStep(ctx, taskID, stepID)
	if err != nil {
		return domain.Attempt{}, err
	}
	parsed, err := parseRecordID(attemptID)
	if err != nil {
		return domain.Attempt{}, err
	}
	attempt, err := s.store.GetAttempt(ctx, parsed)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Attempt{}, fmt.Errorf(
				"%w: 执行尝试 %s",
				ErrExecutionRecordNotFound,
				parsed.String(),
			)
		}
		return domain.Attempt{}, fmt.Errorf("读取执行尝试失败: %w", err)
	}
	// 三个归属必须同时成立：每个标识独立存在并不代表它们属于同一条任务链。
	if attempt.StepID != step.ID || attempt.TaskID != step.TaskID {
		return domain.Attempt{}, fmt.Errorf(
			"%w: 执行尝试 %s",
			ErrExecutionRecordNotFound,
			parsed.String(),
		)
	}
	return *attempt, nil
}

// parseRecordID 解析路径中的标识，非法标识按"记录不存在"处理。
//
// 路径标识由客户端提供，语法错误不应该返回 400 之外的信息，也不应该泄露内部
// 校验细节；统一按不存在处理可以避免用错误信息探测标识格式。
func parseRecordID(raw string) (domain.ID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: 标识不能为空", ErrExecutionRecordNotFound)
	}
	if strings.Contains(trimmed, "/") {
		return "", fmt.Errorf("%w: %s", ErrExecutionRecordNotFound, trimmed)
	}
	return domain.ID(trimmed), nil
}
