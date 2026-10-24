package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// ResumeEligibilityCodeSave 是一条代码保存记录在续跑判定中的对外表示。
type ResumeEligibilityCodeSave struct {
	// ID 是记录标识，续跑被拒绝时用户据此执行只读核验。
	ID domain.ID
	// TaskBranch 是任务分支名。
	TaskBranch string
	// CommitStatus / PushStatus / MergeStatus 是三个阶段的当前状态。
	CommitStatus domain.CommitStatus
	PushStatus   domain.PushStatus
	MergeStatus  domain.MergeStatus
	// PersistStatus 表示记录本身是否可信。
	PersistStatus domain.PersistStatus
	// RecoveryStatus 是已落盘的恢复结论。
	RecoveryStatus domain.RecoveryStatus
	// Action / ErrorClass / Summary 是该记录自身的续跑判定。
	Action     domain.CodeSaveResumeAction
	ErrorClass string
	Summary    string
}

// ResumeEligibility 是"该任务现在能否继续推进"的只读结论。
//
// 它把此前分散在三个入口的前置条件（代码保存未决副作用、容器可用性、执行节点预检、
// 任务与执行记录状态）汇总成一份可读结论，使用户不必先猜"为什么不能恢复"。
//
// 它只读：不写任何状态，也不触发任何远程操作；真正的前置校验仍由
// StartWorkflow / ResumeWorkflow 在推进前重新执行一次。
type ResumeEligibility struct {
	TaskID domain.ID
	// TaskStatus 是任务业务状态。
	TaskStatus domain.TaskStatus
	// Run 是最近一次工作流执行记录，没有时为空。
	Run *WorkflowRunView
	// CanResume 表示当前没有阻塞项，可以再次请求恢复推进。
	CanResume bool
	// Blocking 是阻塞项分类列表（稳定字符串）。
	Blocking []string
	// CodeSave 是按任务判定的代码保存续跑结论。
	CodeSave domain.CodeSaveResumeDecision
	// CodeSaveRecords 是需要关注的代码保存记录。
	CodeSaveRecords []ResumeEligibilityCodeSave
	// NextActions 是给用户的下一步操作提示（可直接执行）。
	NextActions []string
	// Summary 是整体可读说明。
	Summary string
}

// ResumeEligibilityStore 是续跑资格查询依赖的存储能力。
type ResumeEligibilityStore interface {
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	ListCodeSaveRecordsByTask(ctx context.Context, taskID string) ([]domain.CodeSaveRecord, error)
	ListWorkflowRunsByTask(ctx context.Context, taskID domain.ID) ([]domain.WorkflowRun, error)
}

// ResumeEligibilityOptions 是构造续跑资格服务的输入。
type ResumeEligibilityOptions struct {
	// Store 是持久化入口，必填。
	Store ResumeEligibilityStore
	// Containers 是容器核验能力，为空时跳过容器前置校验。
	Containers WorkflowContainerPort
}

// ResumeEligibilityService 计算任务的恢复续跑资格。
//
// 职责边界：只读判定。它不推进工作流、不执行核验、不修改任何记录；所有规则都复用
// 既有领域判定（domain.DecideTaskCodeSaveResume 与既有工作流前置校验）。
type ResumeEligibilityService struct {
	store      ResumeEligibilityStore
	containers WorkflowContainerPort
}

// NewResumeEligibilityService 构造续跑资格服务。
func NewResumeEligibilityService(
	options ResumeEligibilityOptions,
) (*ResumeEligibilityService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("续跑资格服务初始化失败: 存储不能为空")
	}
	return &ResumeEligibilityService{
		store:      options.Store,
		containers: options.Containers,
	}, nil
}

// Get 返回任务的续跑资格。
func (s *ResumeEligibilityService) Get(
	ctx context.Context,
	taskID string,
) (ResumeEligibility, error) {
	id := domain.ID(strings.TrimSpace(taskID))
	if err := id.Validate(); err != nil {
		return ResumeEligibility{}, fmt.Errorf("%w: 任务标识不合法: %w", ErrWorkflowInvalidInput, err)
	}
	task, err := s.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ResumeEligibility{}, fmt.Errorf("%w: 任务 %s", ErrWorkflowNotFound, id)
		}
		return ResumeEligibility{}, fmt.Errorf("读取任务失败: %w", err)
	}
	result := ResumeEligibility{
		TaskID:     task.ID,
		TaskStatus: task.Status,
	}
	runs, err := s.store.ListWorkflowRunsByTask(ctx, task.ID)
	if err != nil {
		return ResumeEligibility{}, fmt.Errorf("读取工作流执行记录失败: %w", err)
	}
	if len(runs) > 0 {
		view := workflowRunView(runs[0], false)
		result.Run = &view
	}
	records, err := s.store.ListCodeSaveRecordsByTask(ctx, task.ID.String())
	if err != nil {
		return ResumeEligibility{}, fmt.Errorf("读取代码保存记录失败: %w", err)
	}
	result.CodeSave = domain.DecideTaskCodeSaveResume(records)
	result.CodeSaveRecords = codeSaveEligibilityViews(records)

	result.Blocking = s.blockingReasons(ctx, *task, result.CodeSave)
	result.CanResume = len(result.Blocking) == 0
	result.NextActions = resumeNextActions(*task, result)
	result.Summary = resumeSummary(result)
	return result, nil
}

// blockingReasons 收集当前阻塞项。
func (s *ResumeEligibilityService) blockingReasons(
	ctx context.Context,
	task domain.Task,
	codeSave domain.CodeSaveResumeDecision,
) []string {
	blocking := make([]string, 0, 3)
	if task.Deleted() {
		blocking = append(blocking, "task_deleted")
		return blocking
	}
	if task.CancelRequested {
		// 取消请求已经落盘：恢复推进与取消意图冲突，必须由用户显式处置。
		blocking = append(blocking, "cancel_requested")
	}
	if codeSave.Action != domain.CodeSaveResumeProceed {
		blocking = append(blocking, codeSave.ErrorClass)
	}
	if err := s.containerBlocking(ctx, task); err != nil {
		blocking = append(blocking, errBlockingClass(err))
	}
	return blocking
}

// containerBlocking 判断任务的容器前置条件是否满足。
//
// 与 checkContainerReady 使用同一份判定输入（任务记录的容器标识 + 真实核验），
// 但只回答"是否阻塞"，不返回面向 HTTP 的错误对象。
func (s *ResumeEligibilityService) containerBlocking(
	ctx context.Context,
	task domain.Task,
) error {
	if strings.TrimSpace(task.ContainerID) == "" {
		return errContainerMissing
	}
	if s.containers == nil {
		return nil
	}
	detail, err := s.containers.InspectContainer(ctx, InspectContainerRequest{
		ContainerID: task.ContainerID,
		Sync:        false,
	})
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return errContainerMissing
		}
		return errContainerUnreachable
	}
	if detail.Container.Detached {
		return errContainerDetached
	}
	if detail.Container.State == domain.ContainerStateRemoved {
		return errContainerMissing
	}
	if detail.Inspect == nil || !detail.Inspect.Running {
		return errContainerNotRunning
	}
	return nil
}

// 续跑资格使用的阻塞分类错误。
var (
	errContainerMissing     = errors.New("container_missing")
	errContainerDetached    = errors.New("container_detached")
	errContainerNotRunning  = errors.New("container_not_running")
	errContainerUnreachable = errors.New("container_unreachable")
)

// errBlockingClass 返回阻塞分类的稳定字符串。
func errBlockingClass(err error) string {
	switch {
	case errors.Is(err, errContainerMissing):
		return "container_missing"
	case errors.Is(err, errContainerDetached):
		return "container_detached"
	case errors.Is(err, errContainerNotRunning):
		return "container_not_running"
	case errors.Is(err, errContainerUnreachable):
		return "container_unreachable"
	default:
		return "prerequisite_unmet"
	}
}

// codeSaveEligibilityViews 把代码保存记录转换为对外表示。
//
// 只输出需要关注的记录（仍有未决副作用或记录不可信）：全部收敛的记录不会产生
// 任何操作提示，把历史记录全量列出只会淹没真正需要处理的那一条。
func codeSaveEligibilityViews(records []domain.CodeSaveRecord) []ResumeEligibilityCodeSave {
	views := make([]ResumeEligibilityCodeSave, 0, len(records))
	for _, record := range records {
		if !record.HasUnresolvedSideEffect() && !record.PersistStatus.IsUnconfirmed() {
			continue
		}
		decision := domain.DecideCodeSaveResume(record)
		views = append(views, ResumeEligibilityCodeSave{
			ID:             record.ID,
			TaskBranch:     record.TaskBranch,
			CommitStatus:   record.CommitStatus,
			PushStatus:     record.PushStatus,
			MergeStatus:    record.MergeStatus,
			PersistStatus:  record.PersistStatus,
			RecoveryStatus: record.RecoveryStatus,
			Action:         decision.Action,
			ErrorClass:     decision.ErrorClass,
			Summary:        decision.Summary,
		})
	}
	return views
}

// resumeNextActions 生成下一步操作提示。
//
// 提示必须可直接执行：只写"请核验"而没有记录标识，用户仍然无从下手。
func resumeNextActions(task domain.Task, result ResumeEligibility) []string {
	actions := make([]string, 0, 3)
	for _, record := range result.CodeSaveRecords {
		if record.Action == domain.CodeSaveResumeProceed {
			continue
		}
		actions = append(actions, fmt.Sprintf(
			"agentflow code verify --record %s", record.ID,
		))
	}
	if result.CodeSave.Action == domain.CodeSaveResumeBlocked {
		actions = append(actions,
			"核验后若仍为 unverifiable，请人工检查容器内仓库与远程分支后再决定是否重试")
	}
	if task.CancelRequested && !actionsContain(actions, "cancel") {
		actions = append(actions,
			"任务已请求取消：如需继续推进请先确认取消是否仍然适用，再重新启动任务")
	}
	if len(actions) == 0 && result.CanResume {
		actions = append(actions,
			"agentflow task resume "+task.ID.String())
	}
	return actions
}

// resumeSummary 生成整体可读说明。
func resumeSummary(result ResumeEligibility) string {
	if result.CanResume {
		return "没有阻塞项，可以再次请求恢复推进"
	}
	return "存在阻塞项（" + strings.Join(result.Blocking, "、") + "），" +
		"请按 next_actions 处理后重试"
}

// actionsContain 判断提示列表是否已经包含给定关键字。
func actionsContain(actions []string, keyword string) bool {
	for _, action := range actions {
		if strings.Contains(action, keyword) {
			return true
		}
	}
	return false
}
