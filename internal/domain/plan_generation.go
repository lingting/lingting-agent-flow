package domain

import (
	"fmt"
	"strings"
	"time"
)

// 计划生成的边界常量。
//
// 这些上限描述"Planner 的输出结构能有多大"，与 Agent 的调用超时、模型输出长度
// 是不同层次的概念：后者约束一次进程调用，前者约束被接受为计划的内容。
const (
	// MaxPlanStepTitleLength 是计划步骤标题的长度上限。
	MaxPlanStepTitleLength = 200
	// MaxPlanStepObjectiveLength 是计划步骤目标的长度上限。
	MaxPlanStepObjectiveLength = 2000
	// MaxPlanStepInstructionsLength 是计划步骤执行要求的长度上限。
	MaxPlanStepInstructionsLength = 8000
	// MaxPlanStepIdentifierLength 是计划内步骤标识的长度上限。
	MaxPlanStepIdentifierLength = 128
)

// ErrPlanInvalid 表示 Planner 输出无法构成一份可执行计划。
//
// 它与"Planner 调用失败"是不同的事实：调用可能正常结束，但输出为空、不是结构化
// 计划或未通过确定性校验。两者都不能让任务进入 planned，但诊断信息必须可区分。
var ErrPlanInvalid = fmt.Errorf("%w: 计划内容不合法", ErrInvalidValue)

// CanStartPlanning 判断任务状态是否允许开始一次计划生成。
//
// 取值严格等于任务状态机中能够迁移到 planning 的来源集合：判定只在这里定义一次，
// 调用方不得另行复制一份状态白名单。
func (s TaskStatus) CanStartPlanning() bool {
	return taskStatusMachine.allows(string(s), string(TaskStatusPlanning))
}

// PlanStepDraft 是 Planner 输出的单个计划步骤。
//
// 它是 Planner 与编排器之间的唯一契约：自然语言输出不会被直接当成可执行计划，
// 必须先解析为该结构并通过 BuildPlan 的确定性校验。
type PlanStepDraft struct {
	// Identifier 是计划内的步骤标识，必须唯一，只用于定位重复步骤，不落库。
	Identifier string
	// Title 是步骤标题。
	Title string
	// Objective 是步骤目标。
	Objective string
	// Instructions 是交给 Developer 的具体要求。
	Instructions string
	// AcceptanceCriteria 是该步骤的验收条件，必须非空。
	AcceptanceCriteria []string
	// VerificationCommands 是该步骤额外的验证命令，可以为空。
	VerificationCommands []VerificationCommand
}

// PlanDraft 是 Planner 输出的完整计划。
type PlanDraft struct {
	// Summary 是一句话计划摘要，只写入事件负载，不进入步骤记录。
	Summary string
	// Steps 是计划步骤，顺序即执行顺序。
	Steps []PlanStepDraft
}

// CheckPlanGenerationStart 校验任务是否允许开始一次计划生成。
//
// 校验只读：它不修改任何状态，也不创建任何记录。真正的占用由调用方以任务状态
// 条件更新完成（planning 是跨进程的并发闸门）。
//
// 返回的错误类别用于接口映射：状态不允许属于冲突，配置或环境缺失属于前置条件未满足。
func CheckPlanGenerationStart(task Task, steps []Step) error {
	if task.Deleted() {
		return fmt.Errorf("%w: 任务 %s 已被删除", ErrInvalidState, task.ID)
	}
	if task.CancelRequested {
		return fmt.Errorf("%w: 任务 %s 已请求取消，不能开始计划生成", ErrInvalidState, task.ID)
	}
	if !task.Status.CanStartPlanning() {
		return fmt.Errorf(
			"%w: 任务 %s 当前状态 %s 不允许开始计划生成",
			ErrInvalidState,
			task.ID,
			task.Status,
		)
	}
	if strings.TrimSpace(task.ContainerID) == "" {
		return fmt.Errorf(
			"%w: 任务 %s 没有受管理的任务容器，无法在容器内生成计划",
			ErrInvalidState,
			task.ID,
		)
	}
	if len(steps) > 0 {
		// 计划是执行依据：覆盖已有计划会让历史执行记录失去对应关系，因此默认拒绝。
		// 重新规划必须作为独立能力设计（计划版本化），不能隐含在生成流程里。
		return fmt.Errorf(
			"%w: 任务 %s 已有 %d 个计划步骤，拒绝覆盖已有计划",
			ErrInvalidState,
			task.ID,
			len(steps),
		)
	}
	if task.Config.PlannerRoleID.IsEmpty() {
		return fmt.Errorf(
			"%w: 任务 %s 未配置规划角色",
			ErrInvalidState,
			task.ID,
		)
	}
	return nil
}

// BuildPlan 把 Planner 输出转换为经过确定性校验的可持久化计划。
//
// 规则集中在这里，不分散到调用方：
//   - 步骤数量必须在 1 到任务配置的步骤数上限之间；
//   - 每步的标识、标题、目标与验收条件都必须非空且有长度上限；
//   - 任务级必需验证命令与验收条件合并进每个步骤：Planner 不能通过省略命令
//     来取消任务级的必需检查；
//   - 产出序号从 1 开始连续递增的开发步骤，步骤类型固定为 develop，
//     因为任务级编排器只按顺序执行开发步骤。
func BuildPlan(
	task Task,
	draft PlanDraft,
	role Role,
	taskVerification VerificationSpec,
	at time.Time,
) (Plan, error) {
	if err := task.ID.Validate(); err != nil {
		return Plan{}, err
	}
	if role.Type != RoleTypePlanner {
		return Plan{}, fmt.Errorf(
			"%w: 角色 %s 的类型是 %s，计划生成需要 %s",
			ErrPlanInvalid,
			role.ID,
			role.Type,
			RoleTypePlanner,
		)
	}
	limit := task.Config.PlanStepLimit()
	if len(draft.Steps) == 0 {
		return Plan{}, fmt.Errorf("%w: 计划至少需要一个步骤", ErrPlanInvalid)
	}
	if len(draft.Steps) > limit {
		return Plan{}, fmt.Errorf(
			"%w: 计划步骤数 %d 超过上限 %d",
			ErrPlanInvalid,
			len(draft.Steps),
			limit,
		)
	}
	retry := task.Config.Retry
	steps := make([]Step, 0, len(draft.Steps))
	seen := make(map[string]struct{}, len(draft.Steps))
	for index, item := range draft.Steps {
		position := index + 1
		if err := validatePlanStepDraft(item, position, seen); err != nil {
			return Plan{}, err
		}
		merged := taskVerification.Merge(VerificationSpec{
			AcceptanceCriteria: item.AcceptanceCriteria,
			Commands:           item.VerificationCommands,
		})
		if err := merged.Validate(); err != nil {
			return Plan{}, fmt.Errorf(
				"%w: 第 %d 个步骤的验证规格非法: %w",
				ErrPlanInvalid,
				position,
				err,
			)
		}
		step, err := NewAgentStep(
			task.ID,
			position,
			StepTypeDevelop,
			item.Title,
			item.Objective,
			item.Instructions,
			role,
			merged,
			retry,
			at,
		)
		if err != nil {
			return Plan{}, fmt.Errorf("%w: 第 %d 个步骤非法: %w", ErrPlanInvalid, position, err)
		}
		steps = append(steps, step)
	}
	plan, err := NewPlan(task.ID, draft.Summary, steps)
	if err != nil {
		return Plan{}, fmt.Errorf("%w: %w", ErrPlanInvalid, err)
	}
	return plan, nil
}

// validatePlanStepDraft 校验单个计划步骤草稿的结构与长度边界。
func validatePlanStepDraft(
	item PlanStepDraft,
	position int,
	seen map[string]struct{},
) error {
	identifier := strings.TrimSpace(item.Identifier)
	if identifier == "" {
		return fmt.Errorf("%w: 第 %d 个步骤缺少标识", ErrPlanInvalid, position)
	}
	if len(identifier) > MaxPlanStepIdentifierLength {
		return fmt.Errorf("%w: 第 %d 个步骤的标识过长", ErrPlanInvalid, position)
	}
	if _, duplicated := seen[identifier]; duplicated {
		return fmt.Errorf("%w: 步骤标识重复 %q", ErrPlanInvalid, identifier)
	}
	seen[identifier] = struct{}{}
	if title := strings.TrimSpace(item.Title); title == "" {
		return fmt.Errorf("%w: 第 %d 个步骤缺少标题", ErrPlanInvalid, position)
	} else if len(title) > MaxPlanStepTitleLength {
		return fmt.Errorf("%w: 第 %d 个步骤的标题过长", ErrPlanInvalid, position)
	}
	if objective := strings.TrimSpace(item.Objective); objective == "" {
		return fmt.Errorf("%w: 第 %d 个步骤缺少目标", ErrPlanInvalid, position)
	} else if len(objective) > MaxPlanStepObjectiveLength {
		return fmt.Errorf("%w: 第 %d 个步骤的目标过长", ErrPlanInvalid, position)
	}
	if len(item.Instructions) > MaxPlanStepInstructionsLength {
		return fmt.Errorf("%w: 第 %d 个步骤的执行要求过长", ErrPlanInvalid, position)
	}
	if len(item.AcceptanceCriteria) == 0 {
		return fmt.Errorf("%w: 第 %d 个步骤缺少验收条件", ErrPlanInvalid, position)
	}
	for _, criterion := range item.AcceptanceCriteria {
		if strings.TrimSpace(criterion) == "" {
			return fmt.Errorf("%w: 第 %d 个步骤包含空的验收条件", ErrPlanInvalid, position)
		}
	}
	return nil
}

// DecidePlanGenerationFailure 依据执行结论计算任务在规划失败时的收敛状态。
//
// 规则（顺序固定）：
//  1. 用户取消 → cancelling（由调用方继续收敛为 cancelled）；
//  2. 服务停止导致的中断 → recovery_required，现场未知不能自动重放；
//  3. 超时、非零退出、输出非法、持久化失败 → failed。
//
// 该函数是纯函数：不读数据库、不启动进程。
func DecidePlanGenerationFailure(outcome StepExecutionOutcome) TaskStatus {
	switch outcome {
	case StepExecutionCancelled:
		return TaskStatusCancelling
	case StepExecutionInterrupted:
		return TaskStatusRecoveryRequired
	default:
		return TaskStatusFailed
	}
}

// DecidePlanGenerationRecovery 依据持久化事实计算 planning 残留任务的收敛状态。
//
// 进程在 Planner 调用期间退出时，任务会停留在 planning。此时不能仅凭状态就再次
// 调用 Planner：必须先核验现场。
//   - 已存在步骤：上一轮事务已经提交，只是状态推进未完成，收敛为 planned；
//   - 没有任何步骤：无法判断调用是否真的发生，收敛为 recovery_required。
func DecidePlanGenerationRecovery(hasSteps bool) TaskStatus {
	if hasSteps {
		return TaskStatusPlanned
	}
	return TaskStatusRecoveryRequired
}
