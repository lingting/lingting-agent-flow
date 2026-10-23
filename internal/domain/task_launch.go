package domain

import (
	"fmt"
)

// TaskLaunchStage 是统一任务启动流程所处的阶段。
//
// 阶段划分是诊断契约的一部分：失败信息必须说明"准备环境失败"与"生成计划失败"
// 是两件不同的事。阶段名只描述这一次启动尝试走到了哪一步，不表示任务成功。
type TaskLaunchStage string

// 启动阶段取值。
const (
	// TaskLaunchStageValidate 校验任务记录与现场。
	TaskLaunchStageValidate TaskLaunchStage = "validate"
	// TaskLaunchStageContainer 准备任务容器。
	TaskLaunchStageContainer TaskLaunchStage = "container"
	// TaskLaunchStageWorkspace 在任务容器内初始化代码工作区。
	//
	// 它发生在任何 Step / Attempt 存在之前，因此由任务级工作区初始化承担，
	// 不复用尝试级的 initialize-code 契约。
	TaskLaunchStageWorkspace TaskLaunchStage = "workspace"
	// TaskLaunchStagePlan 在容器内生成并持久化计划。
	TaskLaunchStagePlan TaskLaunchStage = "plan"
	// TaskLaunchStageWorkflow 启动任务级工作流执行。
	TaskLaunchStageWorkflow TaskLaunchStage = "workflow"
)

// String 返回阶段名的字符串形式。
func (s TaskLaunchStage) String() string {
	return string(s)
}

// Validate 校验阶段是否属于已知取值。
func (s TaskLaunchStage) Validate() error {
	switch s {
	case TaskLaunchStageValidate,
		TaskLaunchStageContainer,
		TaskLaunchStageWorkspace,
		TaskLaunchStagePlan,
		TaskLaunchStageWorkflow:
		return nil
	default:
		return invalidField("TaskLaunch", "stage", "未知的启动阶段 "+string(s))
	}
}

// TaskLaunchInput 是计算"统一启动下一步该做什么"所需的持久化事实。
//
// 它只包含从数据库读到的字段，不包含任何推测值：字段缺失与字段为零值必须能被区分，
// 因此这里的每一项都对应一条真实记录。
type TaskLaunchInput struct {
	// Task 是任务记录。
	Task Task
	// Steps 是任务已持久化的计划步骤，按序号升序排列。
	Steps []Step
	// HasActiveWorkflowRun 表示该任务存在未结束的工作流执行记录。
	HasActiveWorkflowRun bool
	// ContainerUsable 表示任务记录关联的容器当前受管理且运行中。
	//
	// 只有 Task.ContainerID 非空时才可能有意义：容器标识为空时该字段必须为 false。
	ContainerUsable bool
	// Workspace 是任务级工作区初始化现场。
	//
	// 它只包含从数据库读到的记录与上面的容器核验结论：判定是否需要初始化时
	// 不得用"目录看起来是空的"之类推断替代这条事实。
	Workspace TaskWorkspaceState
}

// CheckTaskLaunchStart 校验任务是否允许开始一次统一启动。
//
// 校验只读：它不修改任何状态，也不创建任何记录。真正的占用由后续各阶段自身的
// 条件更新与唯一索引保证，本函数只负责在产生任何远程副作用之前给出明确结论。
//
// 返回的错误类别用于接口映射：状态不允许属于冲突，配置或环境缺失属于前置条件未满足。
func CheckTaskLaunchStart(input TaskLaunchInput) error {
	task := input.Task
	if task.Deleted() {
		return fmt.Errorf("%w: 任务 %s 已被删除", ErrInvalidState, task.ID)
	}
	if task.CancelRequested {
		return fmt.Errorf(
			"%w: 任务 %s 已请求取消，不能开始启动流程",
			ErrInvalidState,
			task.ID,
		)
	}
	switch task.Status {
	case TaskStatusQueued,
		TaskStatusPreparing,
		TaskStatusPlanned,
		TaskStatusRetrying,
		TaskStatusPaused,
		TaskStatusRecoveryRequired:
	default:
		return fmt.Errorf(
			"%w: 任务 %s 当前状态 %s 不允许开始启动流程",
			ErrInvalidState,
			task.ID,
			task.Status,
		)
	}
	if input.HasActiveWorkflowRun {
		// 未结束的执行记录说明已有编排在推进同一任务：启动会与之竞争同一份现场。
		return fmt.Errorf(
			"%w: 任务 %s 已有未结束的工作流执行，不能重复启动",
			ErrInvalidState,
			task.ID,
		)
	}
	for _, step := range input.Steps {
		if step.Status == StepStatusRecoveryRequired {
			return fmt.Errorf(
				"%w: 步骤 %s（序号 %d）的执行结果不确定，必须先核验现场再启动",
				ErrInvalidState,
				step.ID,
				step.Sequence,
			)
		}
	}
	if task.HostID == nil {
		return fmt.Errorf(
			"%w: 任务 %s 没有绑定执行节点，无法准备执行环境",
			ErrInvalidState,
			task.ID,
		)
	}
	if task.Config.Environment.IsZero() {
		return fmt.Errorf(
			"%w: 任务 %s 没有配置语言环境，无法准备执行环境",
			ErrInvalidState,
			task.ID,
		)
	}
	if task.Config.PlannerRoleID.IsEmpty() {
		return fmt.Errorf("%w: 任务 %s 未配置规划角色", ErrInvalidState, task.ID)
	}
	if task.Config.DeveloperRoleID.IsEmpty() {
		return fmt.Errorf("%w: 任务 %s 未配置开发角色", ErrInvalidState, task.ID)
	}
	return nil
}

// DecideTaskLaunchNext 依据持久化事实计算统一启动的下一步。
//
// 规则（顺序固定，只在 domain 中定义一次）：
//  1. 存在未结束的工作流执行 → 不再推进，返回 ErrInvalidState；
//  2. 任务容器不可用 → 准备容器；
//  3. 任务容器内的工作区尚未就绪 → 初始化工作区；
//  4. 没有已持久化的计划步骤 → 生成计划；
//  5. 其余情况 → 启动任务级工作流。
//
// 该函数是纯函数：不读数据库、不启动进程、不产生任何远程副作用。调用方不得在
// 别处复制同一套判定，否则"要不要建容器""要不要初始化工作区""要不要生成计划"
// 会出现第二份实现。
func DecideTaskLaunchNext(input TaskLaunchInput) (TaskLaunchStage, error) {
	if err := CheckTaskLaunchStart(input); err != nil {
		return "", err
	}
	if !input.ContainerUsable {
		return TaskLaunchStageContainer, nil
	}
	needed, err := DecideTaskWorkspaceNext(input.Workspace)
	if err != nil {
		return "", err
	}
	if needed {
		return TaskLaunchStageWorkspace, nil
	}
	if len(input.Steps) == 0 {
		return TaskLaunchStagePlan, nil
	}
	return TaskLaunchStageWorkflow, nil
}

// TaskLaunchStepOccupancyInput 是一次"步骤占用残留"收敛所需的持久化事实。
type TaskLaunchStepOccupancyInput struct {
	// Step 是步骤记录。
	Step Step
	// ActiveWorkflowRun 表示该步骤所属任务仍有未结束的工作流执行记录。
	ActiveWorkflowRun bool
}

// DecideStepOccupancyRecovery 依据持久化事实判断被占用步骤是否需要收敛。
//
// 被占用（running / developing / verifying）只说明"曾经有一次执行拿到了占用"。
// 进程在 Pi 调用期间退出时，占用会永久留在数据库里，而占用本身会阻止该步骤再次
// 执行（NextStepToExecute 一律拒绝），使任务既无法推进也无法自动收敛。
//
// 规则（顺序固定）：
//   - 步骤未被占用 → 不处理；
//   - 所属任务仍有未结束的执行记录 → 执行者可能仍在推进，不抢占；
//   - 其余情况 → 收敛为 recovery_required，等待人工核验现场。
//
// 该函数不重放任何 Agent 调用，也不删除容器：现场一律保留。
func DecideStepOccupancyRecovery(input TaskLaunchStepOccupancyInput) (bool, StepStatus) {
	switch input.Step.Status {
	case StepStatusRunning, StepStatusDeveloping, StepStatusVerifying:
	default:
		return false, input.Step.Status
	}
	if input.ActiveWorkflowRun {
		return false, input.Step.Status
	}
	return true, StepStatusRecoveryRequired
}

// StepOccupancyRecoverySummary 返回步骤占用收敛时写入的固定摘要。
//
// 摘要必须说明"结论来自启动恢复"，否则用户无法区分自动收敛与真实执行结果。
func StepOccupancyRecoverySummary() string {
	return "上一次执行的进程已退出，步骤仍处于执行占用状态，无法确认执行结果，需要人工核验现场。"
}
