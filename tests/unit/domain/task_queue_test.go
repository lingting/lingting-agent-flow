// 任务队列、领取、调度名额与执行权交接的领域规则单元测试。
//
// 这些用例只覆盖 domain 中的纯函数与纯类型：不读数据库、不启动进程、不访问网络，
// 因此可以并发执行。它们证明的是"规则本身是否正确"，不证明"并发领取是否安全"——
// 后者必须依赖 SQLite 的原子条件更新，属于 P3-M2 的存储层验收项，不能由本文件声称。
package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// queueAt 是队列用例使用的固定时刻。
var queueAt = time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

// queueRequirement 构造一条带指定优先级的需求记录。
func queueRequirement(t *testing.T, priority domain.Priority) domain.Requirement {
	t.Helper()
	requirement, err := domain.NewRequirement("需求标题", "需求描述", priority, queueAt)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	return requirement
}

// queueTask 构造一条可进入自动调度的任务记录。
func queueTask(t *testing.T, at time.Time) domain.Task {
	t.Helper()
	requirement := queueRequirement(t, domain.PriorityNormal)
	task, err := domain.NewTask(
		requirement.ID,
		"git@example.com:demo/repo.git",
		"main",
		"",
		at,
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	environment, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境失败: %v", err)
	}
	hostID := domain.NewID()
	task.HostID = &hostID
	task.Config = domain.TaskConfig{
		Goal:            "实现功能",
		Environment:     environment,
		PlannerRoleID:   domain.NewID(),
		DeveloperRoleID: domain.NewID(),
	}
	return task
}

// queueInput 构造一份默认"全部条件都满足"的队列判定输入。
func queueInput(t *testing.T, priority domain.Priority) domain.TaskQueueInput {
	t.Helper()
	task := queueTask(t, queueAt)
	requirement := queueRequirement(t, priority)
	requirement.ID = task.RequirementID
	return domain.TaskQueueInput{
		Task:        task,
		Requirement: &requirement,
		Now:         queueAt,
	}
}

// leaseTask 返回一条持有指定租约的任务记录。
func leaseTask(
	t *testing.T,
	owner string,
	expiresAt time.Time,
) domain.Task {
	t.Helper()
	task := queueTask(t, queueAt)
	task.LeaseOwner = owner
	task.LeaseExpiresAt = &expiresAt
	return task
}

// TestTaskStatusAllowsAutomaticScheduling 校验自动调度候选集合只包含 queued。
//
// 这是本里程碑最重要的一条语义：CanStart 允许引擎推进的状态远多于队列准入允许的
// 状态，若把两者混用，调度器会在失败、暂停与结果不确定的任务上自动重放外部操作。
func TestTaskStatusAllowsAutomaticScheduling(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		status   domain.TaskStatus
		expected bool
	}{
		{"queued 可以自动调度", domain.TaskStatusQueued, true},
		{"preparing 不能自动调度", domain.TaskStatusPreparing, false},
		{"planning 不能自动调度", domain.TaskStatusPlanning, false},
		{"planned 不能自动调度", domain.TaskStatusPlanned, false},
		{"developing 不能自动调度", domain.TaskStatusDeveloping, false},
		{"verifying 不能自动调度", domain.TaskStatusVerifying, false},
		{"saving 不能自动调度", domain.TaskStatusSaving, false},
		{"retrying 不能自动调度", domain.TaskStatusRetrying, false},
		{"paused 不能自动调度", domain.TaskStatusPaused, false},
		{"failed 不能自动调度", domain.TaskStatusFailed, false},
		{"recovery_required 不能自动调度", domain.TaskStatusRecoveryRequired, false},
		{"cancelling 不能自动调度", domain.TaskStatusCancelling, false},
		{"completed 不能自动调度", domain.TaskStatusCompleted, false},
		{"cancelled 不能自动调度", domain.TaskStatusCancelled, false},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if actual := testCase.status.AllowsAutomaticScheduling(); actual != testCase.expected {
				t.Fatalf(
					"状态 %s 的自动调度资格期望 %v，实际 %v",
					testCase.status,
					testCase.expected,
					actual,
				)
			}
		})
	}
}

// TestTaskStatusAllowsAutomaticSchedulingIsStricterThanCanStart 校验队列准入严格强于启动准入。
//
// 队列准入集合必须是 CanStart 的真子集：任何通过队列准入的状态都必须同时允许引擎
// 推进，反向不成立。两个集合一旦出现反向包含，说明队列规则绕过了状态机。
func TestTaskStatusAllowsAutomaticSchedulingIsStricterThanCanStart(t *testing.T) {
	t.Parallel()
	statuses := []domain.TaskStatus{
		domain.TaskStatusQueued,
		domain.TaskStatusPreparing,
		domain.TaskStatusPlanning,
		domain.TaskStatusPlanned,
		domain.TaskStatusDeveloping,
		domain.TaskStatusVerifying,
		domain.TaskStatusSaving,
		domain.TaskStatusCancelling,
		domain.TaskStatusRetrying,
		domain.TaskStatusPaused,
		domain.TaskStatusCompleted,
		domain.TaskStatusFailed,
		domain.TaskStatusCancelled,
		domain.TaskStatusRecoveryRequired,
	}
	queueable := 0
	for _, status := range statuses {
		if !status.AllowsAutomaticScheduling() {
			continue
		}
		queueable++
		if !status.CanStart() {
			t.Fatalf(
				"状态 %s 通过自动调度准入却不允许引擎推进：队列规则绕过了状态机",
				status,
			)
		}
	}
	if queueable == 0 {
		t.Fatal("自动调度候选集合为空，调度器将永远没有候选")
	}
	if queueable >= len(statuses) {
		t.Fatal("自动调度候选集合不应等于全部状态：它必须严于 CanStart")
	}
}

// TestDecideTaskQueueAdmissionAcceptsQueuedTask 校验普通新任务可以通过准入。
func TestDecideTaskQueueAdmissionAcceptsQueuedTask(t *testing.T) {
	t.Parallel()
	admission := domain.DecideTaskQueueAdmission(queueInput(t, domain.PriorityNormal))
	if !admission.Admitted {
		t.Fatalf("新任务应当通过准入，实际拒绝原因: %v", admission.Rejections)
	}
	if len(admission.Rejections) != 0 {
		t.Fatalf("通过的准入不应携带拒绝原因: %v", admission.Rejections)
	}
	if summary := admission.Summary(); summary != "" {
		t.Fatalf("通过的准入不应有拒绝说明: %s", summary)
	}
}

// TestDecideTaskQueueAdmissionRejectsNonQueuedStatus 校验非 queued 状态一律不能自动调度。
func TestDecideTaskQueueAdmissionRejectsNonQueuedStatus(t *testing.T) {
	t.Parallel()
	statuses := []domain.TaskStatus{
		domain.TaskStatusPreparing,
		domain.TaskStatusPlanned,
		domain.TaskStatusRetrying,
		domain.TaskStatusFailed,
		domain.TaskStatusPaused,
		domain.TaskStatusRecoveryRequired,
		domain.TaskStatusDeveloping,
		domain.TaskStatusCancelling,
		domain.TaskStatusCompleted,
	}
	for _, status := range statuses {
		status := status
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			input := queueInput(t, domain.PriorityNormal)
			input.Task.Status = status
			admission := domain.DecideTaskQueueAdmission(input)
			if admission.Admitted {
				t.Fatalf("状态 %s 不应通过自动调度准入", status)
			}
			if !admission.Reject(domain.TaskQueueRejectionStatusNotQueueable) {
				t.Fatalf("拒绝原因缺少 status_not_queueable: %v", admission.Rejections)
			}
		})
	}
}

// TestDecideTaskQueueAdmissionRejectsDeletedAndCancelled 校验删除与取消请求优先排除候选。
func TestDecideTaskQueueAdmissionRejectsDeletedAndCancelled(t *testing.T) {
	t.Parallel()
	t.Run("逻辑删除", func(t *testing.T) {
		t.Parallel()
		input := queueInput(t, domain.PriorityNormal)
		deletedAt := queueAt
		input.Task.DeletedAt = &deletedAt
		admission := domain.DecideTaskQueueAdmission(input)
		if admission.Admitted {
			t.Fatal("已删除任务不应通过准入")
		}
		if !admission.Reject(domain.TaskQueueRejectionDeleted) {
			t.Fatalf("拒绝原因缺少 task_deleted: %v", admission.Rejections)
		}
	})
	t.Run("取消请求", func(t *testing.T) {
		t.Parallel()
		input := queueInput(t, domain.PriorityNormal)
		requestedAt := queueAt
		input.Task.CancelRequested = true
		input.Task.CancelRequestedAt = &requestedAt
		admission := domain.DecideTaskQueueAdmission(input)
		if admission.Admitted {
			t.Fatal("已请求取消的任务不应通过准入")
		}
		if !admission.Reject(domain.TaskQueueRejectionCancelRequested) {
			t.Fatalf("拒绝原因缺少 cancel_requested: %v", admission.Rejections)
		}
	})
}

// TestDecideTaskQueueAdmissionIgnoresLeaseOwnership 校验准入不判定租约归属。
//
// 租约归属属于领取仲裁：把租约混进准入会让"同一执行者重复领取"无法表达。候选池
// 只回答"是否属于队列"，"这一次由谁拿到执行权"由 DecideTaskClaim 回答。
func TestDecideTaskQueueAdmissionIgnoresLeaseOwnership(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"有效租约", "过期租约"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input := queueInput(t, domain.PriorityNormal)
			expiresAt := queueAt.Add(time.Minute)
			if name == "过期租约" {
				expiresAt = queueAt.Add(-time.Minute)
			}
			input.Task = leaseTask(t, "worker-a", expiresAt)
			admission := domain.DecideTaskQueueAdmission(input)
			if !admission.Admitted {
				t.Fatalf("租约归属不应影响准入，实际拒绝: %v", admission.Rejections)
			}
		})
	}
}

// TestDecideTaskQueueAdmissionRejectsActiveRunAndResidue 校验执行现场残留排除候选。
func TestDecideTaskQueueAdmissionRejectsActiveRunAndResidue(t *testing.T) {
	t.Parallel()
	t.Run("未结束的执行记录", func(t *testing.T) {
		t.Parallel()
		input := queueInput(t, domain.PriorityNormal)
		run := domain.WorkflowRun{Status: domain.WorkflowRunRunning}
		input.ActiveWorkflowRun = &run
		admission := domain.DecideTaskQueueAdmission(input)
		if admission.Admitted {
			t.Fatal("存在未结束执行记录时不应通过准入")
		}
		if !admission.Reject(domain.TaskQueueRejectionActiveWorkflowRun) {
			t.Fatalf("拒绝原因缺少 active_workflow_run: %v", admission.Rejections)
		}
	})
	t.Run("结果不确定的步骤", func(t *testing.T) {
		t.Parallel()
		input := queueInput(t, domain.PriorityNormal)
		step := queueStep(t, input.Task.ID, 1, domain.StepStatusRecoveryRequired)
		input.Steps = []domain.Step{step}
		admission := domain.DecideTaskQueueAdmission(input)
		if admission.Admitted {
			t.Fatal("存在结果不确定的步骤时不应通过准入")
		}
		if !admission.Reject(domain.TaskQueueRejectionRecoveryResidue) {
			t.Fatalf("拒绝原因缺少 recovery_residue: %v", admission.Rejections)
		}
	})
	t.Run("仍被占用的步骤", func(t *testing.T) {
		t.Parallel()
		input := queueInput(t, domain.PriorityNormal)
		step := queueStep(t, input.Task.ID, 1, domain.StepStatusDeveloping)
		input.Steps = []domain.Step{step}
		admission := domain.DecideTaskQueueAdmission(input)
		if admission.Admitted {
			t.Fatal("存在占用步骤时不应通过准入")
		}
		if !admission.Reject(domain.TaskQueueRejectionStepOccupied) {
			t.Fatalf("拒绝原因缺少 step_occupied: %v", admission.Rejections)
		}
	})
}

// TestDecideTaskQueueAdmissionRejectsPendingSideEffect 校验未核验的外部副作用排除候选。
//
// 推送仍为 pending 时，"数据库里没有成功记录"不能证明推送没有发生，因此调度器
// 不得把它当作普通新任务重新执行。
func TestDecideTaskQueueAdmissionRejectsPendingSideEffect(t *testing.T) {
	t.Parallel()
	input := queueInput(t, domain.PriorityNormal)
	input.CodeSaveRecords = []domain.CodeSaveRecord{
		{
			ID:           domain.NewID(),
			TaskID:       input.Task.ID.String(),
			TaskBranch:   "agent/" + input.Task.ID.String(),
			ContainerID:  "container-1",
			RemoteName:   "origin",
			CommitStatus: domain.CommitStatusSucceeded,
			CommitSHA:    strings.Repeat("2", 40),
			PushStatus:   domain.PushStatusPending,
			MergeStatus:  domain.MergeStatusNotRequested,
		},
	}
	admission := domain.DecideTaskQueueAdmission(input)
	if admission.Admitted {
		t.Fatal("存在未核验的外部副作用时不应通过准入")
	}
	if !admission.Reject(domain.TaskQueueRejectionSideEffectPending) {
		t.Fatalf("拒绝原因缺少 external_side_effect_pending: %v", admission.Rejections)
	}
}

// TestDecideTaskQueueAdmissionRejectsMissingPrerequisite 校验启动前置条件缺失时排除候选。
//
// 前置条件由 CheckTaskLaunchStart 定义：队列准入只负责引用，不重新实现。
func TestDecideTaskQueueAdmissionRejectsMissingPrerequisite(t *testing.T) {
	t.Parallel()
	t.Run("缺少执行节点", func(t *testing.T) {
		t.Parallel()
		input := queueInput(t, domain.PriorityNormal)
		input.Task.HostID = nil
		admission := domain.DecideTaskQueueAdmission(input)
		if admission.Admitted {
			t.Fatal("缺少执行节点时不应通过准入")
		}
		if !admission.Reject(domain.TaskQueueRejectionPrerequisiteMissing) {
			t.Fatalf("拒绝原因缺少 prerequisite_missing: %v", admission.Rejections)
		}
		if admission.Prerequisite == nil {
			t.Fatal("前置条件拒绝必须携带原始错误，便于日志与接口映射")
		}
	})
	t.Run("缺少规划角色", func(t *testing.T) {
		t.Parallel()
		input := queueInput(t, domain.PriorityNormal)
		input.Task.Config.PlannerRoleID = ""
		admission := domain.DecideTaskQueueAdmission(input)
		if admission.Admitted {
			t.Fatal("缺少规划角色时不应通过准入")
		}
		if !admission.Reject(domain.TaskQueueRejectionPrerequisiteMissing) {
			t.Fatalf("拒绝原因缺少 prerequisite_missing: %v", admission.Rejections)
		}
	})
}

// TestDecideTaskQueueAdmissionRejectsUnknownPriority 校验优先级无法解析时拒绝而不是默认 normal。
func TestDecideTaskQueueAdmissionRejectsUnknownPriority(t *testing.T) {
	t.Parallel()
	t.Run("缺少需求记录", func(t *testing.T) {
		t.Parallel()
		input := queueInput(t, domain.PriorityNormal)
		input.Requirement = nil
		admission := domain.DecideTaskQueueAdmission(input)
		if admission.Admitted {
			t.Fatal("缺少需求记录时不应通过准入")
		}
		if !admission.Reject(domain.TaskQueueRejectionPriorityUnknown) {
			t.Fatalf("拒绝原因缺少 priority_unknown: %v", admission.Rejections)
		}
	})
	t.Run("未知优先级取值", func(t *testing.T) {
		t.Parallel()
		input := queueInput(t, domain.PriorityNormal)
		input.Requirement.Priority = domain.Priority("urgent")
		admission := domain.DecideTaskQueueAdmission(input)
		if admission.Admitted {
			t.Fatal("未知优先级取值时不应通过准入")
		}
		if !admission.Reject(domain.TaskQueueRejectionPriorityUnknown) {
			t.Fatalf("拒绝原因缺少 priority_unknown: %v", admission.Rejections)
		}
	})
}

// TestDecideTaskQueueAdmissionCollectsAllReasons 校验准入一次给出全部拒绝原因。
func TestDecideTaskQueueAdmissionCollectsAllReasons(t *testing.T) {
	t.Parallel()
	input := queueInput(t, domain.PriorityNormal)
	input.Task.Status = domain.TaskStatusFailed
	input.Task.CancelRequested = true
	requestedAt := queueAt
	input.Task.CancelRequestedAt = &requestedAt
	admission := domain.DecideTaskQueueAdmission(input)
	if admission.Admitted {
		t.Fatal("多重拒绝条件不应通过准入")
	}
	for _, reason := range []domain.TaskQueueRejection{
		domain.TaskQueueRejectionCancelRequested,
		domain.TaskQueueRejectionStatusNotQueueable,
	} {
		if !admission.Reject(reason) {
			t.Fatalf("拒绝原因缺少 %s: %v", reason, admission.Rejections)
		}
	}
	if summary := admission.Summary(); !strings.Contains(summary, "cancel_requested") {
		t.Fatalf("拒绝说明未包含具体原因: %s", summary)
	}
}

// TestCompareTaskQueueOrderByPriority 校验优先级顺序复用 Priority.Rank。
func TestCompareTaskQueueOrderByPriority(t *testing.T) {
	t.Parallel()
	high := domain.TaskQueueOrderKey{
		TaskID:    domain.NewID(),
		Priority:  domain.PriorityHigh,
		CreatedAt: queueAt.Add(time.Hour),
	}
	normal := domain.TaskQueueOrderKey{
		TaskID:    domain.NewID(),
		Priority:  domain.PriorityNormal,
		CreatedAt: queueAt,
	}
	low := domain.TaskQueueOrderKey{
		TaskID:    domain.NewID(),
		Priority:  domain.PriorityLow,
		CreatedAt: queueAt.Add(-time.Hour),
	}
	// 优先级优先于时间：高优先级任务即使创建更晚也必须排在前面。
	if domain.CompareTaskQueueOrder(high, normal) >= 0 {
		t.Fatal("高优先级必须排在普通优先级之前")
	}
	if domain.CompareTaskQueueOrder(normal, low) >= 0 {
		t.Fatal("普通优先级必须排在低优先级之前")
	}
	if domain.CompareTaskQueueOrder(low, high) <= 0 {
		t.Fatal("低优先级必须排在高优先级之后")
	}
}

// TestCompareTaskQueueOrderIsFIFOWithinPriority 校验同优先级按创建时间先到先服务。
func TestCompareTaskQueueOrderIsFIFOWithinPriority(t *testing.T) {
	t.Parallel()
	early := domain.TaskQueueOrderKey{
		TaskID:    domain.NewID(),
		Priority:  domain.PriorityNormal,
		CreatedAt: queueAt,
	}
	late := domain.TaskQueueOrderKey{
		TaskID:    domain.NewID(),
		Priority:  domain.PriorityNormal,
		CreatedAt: queueAt.Add(time.Second),
	}
	if domain.CompareTaskQueueOrder(early, late) >= 0 {
		t.Fatal("同优先级下先创建的任务必须排在前面")
	}
	if domain.CompareTaskQueueOrder(late, early) <= 0 {
		t.Fatal("同优先级下后创建的任务必须排在后面")
	}
}

// TestCompareTaskQueueOrderIsStableOnTiedTime 校验时间相同时按任务标识给出确定次序。
func TestCompareTaskQueueOrderIsStableOnTiedTime(t *testing.T) {
	t.Parallel()
	smaller := domain.TaskQueueOrderKey{
		TaskID:    domain.ID("00000000000000000000000000000001"),
		Priority:  domain.PriorityNormal,
		CreatedAt: queueAt,
	}
	larger := domain.TaskQueueOrderKey{
		TaskID:    domain.ID("00000000000000000000000000000002"),
		Priority:  domain.PriorityNormal,
		CreatedAt: queueAt,
	}
	if domain.CompareTaskQueueOrder(smaller, larger) >= 0 {
		t.Fatal("时间相同时较小标识必须排在前面")
	}
	if domain.CompareTaskQueueOrder(larger, smaller) <= 0 {
		t.Fatal("时间相同时较大标识必须排在后面")
	}
	if domain.CompareTaskQueueOrder(smaller, smaller) != 0 {
		t.Fatal("同一排序键必须相等")
	}
}

// TestSortTaskQueueOrderKeysIsDeterministic 校验排序结果与输入顺序无关。
//
// 同一份候选集合无论以何种顺序传入，都必须得到完全相同的次序；否则同一个队列
// 两次扫描会得到不同结果，调度器无法给出可复核的领取顺序。
func TestSortTaskQueueOrderKeysIsDeterministic(t *testing.T) {
	t.Parallel()
	keys := []domain.TaskQueueOrderKey{
		{
			TaskID:    domain.ID("00000000000000000000000000000003"),
			Priority:  domain.PriorityLow,
			CreatedAt: queueAt,
		},
		{
			TaskID:    domain.ID("00000000000000000000000000000002"),
			Priority:  domain.PriorityHigh,
			CreatedAt: queueAt.Add(time.Hour),
		},
		{
			TaskID:    domain.ID("00000000000000000000000000000001"),
			Priority:  domain.PriorityNormal,
			CreatedAt: queueAt,
		},
		{
			TaskID:    domain.ID("00000000000000000000000000000004"),
			Priority:  domain.PriorityNormal,
			CreatedAt: queueAt,
		},
	}
	expected := []domain.ID{
		keys[1].TaskID,
		keys[2].TaskID,
		keys[3].TaskID,
		keys[0].TaskID,
	}
	orders := [][]domain.TaskQueueOrderKey{
		{keys[0], keys[1], keys[2], keys[3]},
		{keys[3], keys[2], keys[1], keys[0]},
		{keys[2], keys[0], keys[3], keys[1]},
	}
	for index, order := range orders {
		sorted := make([]domain.TaskQueueOrderKey, len(order))
		copy(sorted, order)
		domain.SortTaskQueueOrderKeys(sorted)
		for position, want := range expected {
			if sorted[position].TaskID != want {
				t.Fatalf(
					"第 %d 种输入顺序在第 %d 位期望 %s，实际 %s",
					index,
					position,
					want,
					sorted[position].TaskID,
				)
			}
		}
	}
}

// TestNewTaskQueueOrderKeyRejectsUnknownPriority 校验排序键不能凭空构造。
func TestNewTaskQueueOrderKeyRejectsUnknownPriority(t *testing.T) {
	t.Parallel()
	input := queueInput(t, domain.PriorityNormal)
	input.Requirement = nil
	if _, err := domain.NewTaskQueueOrderKey(input); err == nil {
		t.Fatal("缺少需求记录时构造排序键必须失败")
	}
	key, err := domain.NewTaskQueueOrderKey(queueInput(t, domain.PriorityHigh))
	if err != nil {
		t.Fatalf("合法输入构造排序键失败: %v", err)
	}
	if key.Priority != domain.PriorityHigh {
		t.Fatalf("排序键优先级错误: %s", key.Priority)
	}
}

// TestDecideTaskClaimRejectsInvalidOwner 校验领取必须带执行者标识。
func TestDecideTaskClaimRejectsInvalidOwner(t *testing.T) {
	t.Parallel()
	for _, owner := range []string{"", "   "} {
		if _, err := domain.DecideTaskClaim(queueInput(t, domain.PriorityNormal), owner); err == nil {
			t.Fatalf("空执行者标识 %q 必须被拒绝", owner)
		}
	}
}

// TestDecideTaskClaimAllowsFreeAndExpiredLease 校验空闲与过期租约都可以领取。
func TestDecideTaskClaimAllowsFreeAndExpiredLease(t *testing.T) {
	t.Parallel()
	t.Run("无租约", func(t *testing.T) {
		t.Parallel()
		decision, err := domain.DecideTaskClaim(
			queueInput(t, domain.PriorityNormal),
			"worker-a",
		)
		if err != nil {
			t.Fatalf("领取判定失败: %v", err)
		}
		if !decision.Allowed {
			t.Fatalf("空闲租约应当允许领取，拒绝原因: %s", decision.Rejection)
		}
		if decision.Takeover {
			t.Fatal("没有上一个持有者时不应标记为接管")
		}
	})
	t.Run("过期租约", func(t *testing.T) {
		t.Parallel()
		input := queueInput(t, domain.PriorityNormal)
		input.Task = leaseTask(t, "worker-a", queueAt.Add(-time.Minute))
		decision, err := domain.DecideTaskClaim(input, "worker-b")
		if err != nil {
			t.Fatalf("领取判定失败: %v", err)
		}
		if !decision.Allowed {
			t.Fatalf("过期租约应当允许接管，拒绝原因: %s", decision.Rejection)
		}
		if !decision.Takeover {
			t.Fatal("上一个持有者的租约过期时必须标记为接管")
		}
	})
}

// TestDecideTaskClaimRejectsLiveLeaseOfOtherOwner 校验有效租约属于他人时拒绝领取。
func TestDecideTaskClaimRejectsLiveLeaseOfOtherOwner(t *testing.T) {
	t.Parallel()
	input := queueInput(t, domain.PriorityNormal)
	input.Task = leaseTask(t, "worker-a", queueAt.Add(time.Minute))
	decision, err := domain.DecideTaskClaim(input, "worker-b")
	if err != nil {
		t.Fatalf("领取判定失败: %v", err)
	}
	if decision.Allowed {
		t.Fatal("租约属于其他执行者时不得允许领取")
	}
	// 业务准入本身是通过的：拒绝来自租约仲裁而不是准入，两者必须能区分。
	if !decision.Admission.Admitted {
		t.Fatalf("本例的准入应当通过，实际拒绝: %v", decision.Admission.Rejections)
	}
	if decision.Rejection != domain.TaskQueueRejectionLeaseHeld {
		t.Fatalf("拒绝原因应为 lease_held，实际 %s", decision.Rejection)
	}
}

// TestDecideTaskClaimIsIdempotentForSameOwner 校验同一执行者重复领取是幂等的。
func TestDecideTaskClaimIsIdempotentForSameOwner(t *testing.T) {
	t.Parallel()
	input := queueInput(t, domain.PriorityNormal)
	input.Task = leaseTask(t, "worker-a", queueAt.Add(time.Minute))
	decision, err := domain.DecideTaskClaim(input, "worker-a")
	if err != nil {
		t.Fatalf("领取判定失败: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("同一执行者重复领取应当允许，拒绝原因: %s", decision.Rejection)
	}
	if decision.Takeover {
		t.Fatal("同一执行者重复领取不应标记为接管")
	}
}

// TestDecideTaskClaimRejectsInadmissibleTask 校验准入不通过时不得领取。
func TestDecideTaskClaimRejectsInadmissibleTask(t *testing.T) {
	t.Parallel()
	input := queueInput(t, domain.PriorityNormal)
	input.Task.Status = domain.TaskStatusRecoveryRequired
	decision, err := domain.DecideTaskClaim(input, "worker-a")
	if err != nil {
		t.Fatalf("领取判定失败: %v", err)
	}
	if decision.Allowed {
		t.Fatal("准入不通过的任务不得被领取")
	}
	if decision.Rejection != domain.TaskQueueRejectionStatusNotQueueable {
		t.Fatalf("拒绝原因应为 status_not_queueable，实际 %s", decision.Rejection)
	}
}

// TestDecideTaskClaimCompetingOwnersCannotBothHold 校验两个竞争者在同一快照上不可能都持有执行权。
//
// 这条用例只证明领域判据自洽：第二个竞争者在第一个竞争者真正写入租约后的记录上
// 必须被拒绝。它**不**证明并发领取在数据库中安全——两个进程可能读到同一份快照，
// 因此原子性必须由存储层的条件更新保证，见 P3-M2 的验收要求。
func TestDecideTaskClaimCompetingOwnersCannotBothHold(t *testing.T) {
	t.Parallel()
	snapshot := queueInput(t, domain.PriorityNormal)
	first, err := domain.DecideTaskClaim(snapshot, "worker-a")
	if err != nil {
		t.Fatalf("第一个执行者领取判定失败: %v", err)
	}
	if !first.Allowed {
		t.Fatalf("第一个执行者应当允许领取，拒绝原因: %s", first.Rejection)
	}
	// 模拟"第一个执行者的领取已经落库"。
	claimed := snapshot
	claimed.Task.LeaseOwner = "worker-a"
	expiresAt := queueAt.Add(time.Minute)
	claimed.Task.LeaseExpiresAt = &expiresAt
	second, err := domain.DecideTaskClaim(claimed, "worker-b")
	if err != nil {
		t.Fatalf("第二个执行者领取判定失败: %v", err)
	}
	if second.Allowed {
		t.Fatal("第一个执行者已持有租约时，第二个执行者不得被判定为可以领取")
	}
	if !domain.TaskHoldsExecution(claimed.Task, "worker-a", queueAt) {
		t.Fatal("第一个执行者必须被判定为持有执行权")
	}
	if domain.TaskHoldsExecution(claimed.Task, "worker-b", queueAt) {
		t.Fatal("第二个执行者不得被判定为持有执行权")
	}
}

// TestTaskHoldsExecution 覆盖空租约、有效租约、过期租约与执行者不匹配。
func TestTaskHoldsExecution(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		task     func(t *testing.T) domain.Task
		owner    string
		at       time.Time
		expected bool
	}{
		{
			name:     "空租约不持有执行权",
			task:     func(t *testing.T) domain.Task { return queueTask(t, queueAt) },
			owner:    "worker-a",
			at:       queueAt,
			expected: false,
		},
		{
			name: "有效租约且标识一致时持有",
			task: func(t *testing.T) domain.Task {
				return leaseTask(t, "worker-a", queueAt.Add(time.Minute))
			},
			owner:    "worker-a",
			at:       queueAt,
			expected: true,
		},
		{
			name: "有效租约但标识不一致时不持有",
			task: func(t *testing.T) domain.Task {
				return leaseTask(t, "worker-a", queueAt.Add(time.Minute))
			},
			owner:    "worker-b",
			at:       queueAt,
			expected: false,
		},
		{
			name: "租约到期后不再持有",
			task: func(t *testing.T) domain.Task {
				return leaseTask(t, "worker-a", queueAt)
			},
			owner:    "worker-a",
			at:       queueAt.Add(time.Millisecond),
			expected: false,
		},
		{
			name: "空执行者标识一律不持有",
			task: func(t *testing.T) domain.Task {
				return leaseTask(t, "", queueAt.Add(time.Minute))
			},
			owner:    "",
			at:       queueAt,
			expected: false,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			actual := domain.TaskHoldsExecution(
				testCase.task(t),
				testCase.owner,
				testCase.at,
			)
			if actual != testCase.expected {
				t.Fatalf("期望 %v，实际 %v", testCase.expected, actual)
			}
		})
	}
}

// TestDecideTaskExecutionHandover 覆盖"任务领取 → 工作流执行"的交接判定。
func TestDecideTaskExecutionHandover(t *testing.T) {
	t.Parallel()
	// held 返回"已领取且已进入可推进步骤的状态"，这是交接真正发生的现场：
	// queued 只是候选池成员，领取之后必须先完成环境准备与计划，才允许建立工作流。
	held := func(t *testing.T) domain.Task {
		t.Helper()
		task := leaseTask(t, "worker-a", queueAt.Add(time.Minute))
		task.Status = domain.TaskStatusPlanned
		return task
	}
	runningRun := func(t *testing.T, owner string) *domain.WorkflowRun {
		t.Helper()
		task := queueTask(t, queueAt)
		step := queueStep(t, task.ID, 1, domain.StepStatusPending)
		run, err := domain.NewWorkflowRun(task.ID, []domain.Step{step}, queueAt)
		if err != nil {
			t.Fatalf("构造执行记录失败: %v", err)
		}
		if err := run.Start(owner, time.Minute, queueAt); err != nil {
			t.Fatalf("启动执行记录失败: %v", err)
		}
		return &run
	}
	cases := []struct {
		name     string
		task     func(t *testing.T) domain.Task
		run      func(t *testing.T) *domain.WorkflowRun
		owner    string
		expected bool
		reason   domain.TaskQueueRejection
	}{
		{
			name:  "持有租约且无执行记录时允许交接",
			task:  held,
			run:   func(t *testing.T) *domain.WorkflowRun { return nil },
			owner: "worker-a", expected: true,
		},
		{
			name:  "不持有任务租约时拒绝交接",
			task:  func(t *testing.T) domain.Task { return queueTask(t, queueAt) },
			run:   func(t *testing.T) *domain.WorkflowRun { return nil },
			owner: "worker-a", expected: false,
			reason: domain.TaskQueueRejectionLeaseHeld,
		},
		{
			name: "租约已过期时拒绝交接",
			task: func(t *testing.T) domain.Task {
				return leaseTask(t, "worker-a", queueAt.Add(-time.Minute))
			},
			run:   func(t *testing.T) *domain.WorkflowRun { return nil },
			owner: "worker-a", expected: false,
			reason: domain.TaskQueueRejectionLeaseHeld,
		},
		{
			name: "已请求取消时拒绝交接",
			task: func(t *testing.T) domain.Task {
				task := held(t)
				requestedAt := queueAt
				task.CancelRequested = true
				task.CancelRequestedAt = &requestedAt
				return task
			},
			run:   func(t *testing.T) *domain.WorkflowRun { return nil },
			owner: "worker-a", expected: false,
			reason: domain.TaskQueueRejectionCancelRequested,
		},
		{
			name: "已被逻辑删除时拒绝交接",
			task: func(t *testing.T) domain.Task {
				task := held(t)
				task.Status = domain.TaskStatusCompleted
				deletedAt := queueAt
				task.DeletedAt = &deletedAt
				return task
			},
			run:   func(t *testing.T) *domain.WorkflowRun { return nil },
			owner: "worker-a", expected: false,
			reason: domain.TaskQueueRejectionDeleted,
		},
		{
			name:  "其他执行者持有有效执行权时拒绝交接",
			task:  held,
			run:   func(t *testing.T) *domain.WorkflowRun { return runningRun(t, "worker-b") },
			owner: "worker-a", expected: false,
			reason: domain.TaskQueueRejectionActiveWorkflowRun,
		},
		{
			name:  "同一执行者持有执行权时允许交接",
			task:  held,
			run:   func(t *testing.T) *domain.WorkflowRun { return runningRun(t, "worker-a") },
			owner: "worker-a", expected: true,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			handover := domain.DecideTaskExecutionHandover(
				testCase.task(t),
				testCase.run(t),
				testCase.owner,
				queueAt,
			)
			if handover.Allowed != testCase.expected {
				t.Fatalf(
					"期望允许 %v，实际 %v（原因 %s）",
					testCase.expected,
					handover.Allowed,
					handover.Reason,
				)
			}
			if !testCase.expected && handover.Reason != testCase.reason {
				t.Fatalf("拒绝原因期望 %s，实际 %s", testCase.reason, handover.Reason)
			}
		})
	}
}

// TestDecideTaskExecutionHandoverRejectsStatusWithoutStepExecution 校验状态与执行权不一致时拒绝。
func TestDecideTaskExecutionHandoverRejectsStatusWithoutStepExecution(t *testing.T) {
	t.Parallel()
	task := leaseTask(t, "worker-a", queueAt.Add(time.Minute))
	// 持有有效租约但状态不允许推进步骤：租约与状态不一致，交接必须拒绝。
	task.Status = domain.TaskStatusQueued
	handover := domain.DecideTaskExecutionHandover(task, nil, "worker-a", queueAt)
	if handover.Allowed {
		t.Fatal("queued 任务持有租约时仍不得直接建立工作流执行")
	}
	if handover.Reason != domain.TaskQueueRejectionStatusNotQueueable {
		t.Fatalf("拒绝原因期望 status_not_queueable，实际 %s", handover.Reason)
	}
}

// TestTaskSlotStateOccupancy 校验名额归属判定覆盖各阶段。
func TestTaskSlotStateOccupancy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		input    func(t *testing.T) domain.TaskSlotInput
		expected domain.TaskSlotState
	}{
		{
			name: "新任务不占名额",
			input: func(t *testing.T) domain.TaskSlotInput {
				return domain.TaskSlotInput{Task: queueTask(t, queueAt), Now: queueAt}
			},
			expected: domain.TaskSlotIdle,
		},
		{
			name: "终态任务不占名额",
			input: func(t *testing.T) domain.TaskSlotInput {
				task := queueTask(t, queueAt)
				task.Status = domain.TaskStatusCompleted
				return domain.TaskSlotInput{Task: task, Now: queueAt}
			},
			expected: domain.TaskSlotIdle,
		},
		{
			name: "领取成功但尚无执行记录时占名额",
			input: func(t *testing.T) domain.TaskSlotInput {
				task := leaseTask(t, "worker-a", queueAt.Add(time.Minute))
				task.Status = domain.TaskStatusPreparing
				return domain.TaskSlotInput{Task: task, Now: queueAt}
			},
			expected: domain.TaskSlotClaimed,
		},
		{
			name: "收尾阶段仍持有执行权时占名额",
			input: func(t *testing.T) domain.TaskSlotInput {
				task := leaseTask(t, "worker-a", queueAt.Add(time.Minute))
				task.Status = domain.TaskStatusSaving
				return domain.TaskSlotInput{Task: task, Now: queueAt}
			},
			expected: domain.TaskSlotFinalizing,
		},
		{
			name: "失败任务等待人工且不占名额",
			input: func(t *testing.T) domain.TaskSlotInput {
				task := queueTask(t, queueAt)
				task.Status = domain.TaskStatusFailed
				return domain.TaskSlotInput{Task: task, Now: queueAt}
			},
			expected: domain.TaskSlotAwaitingHuman,
		},
		{
			name: "暂停任务等待人工且不占名额",
			input: func(t *testing.T) domain.TaskSlotInput {
				task := queueTask(t, queueAt)
				task.Status = domain.TaskStatusPaused
				return domain.TaskSlotInput{Task: task, Now: queueAt}
			},
			expected: domain.TaskSlotAwaitingHuman,
		},
		{
			name: "租约失效仍停在中间状态时结论不明",
			input: func(t *testing.T) domain.TaskSlotInput {
				task := leaseTask(t, "worker-a", queueAt.Add(-time.Minute))
				task.Status = domain.TaskStatusDeveloping
				return domain.TaskSlotInput{Task: task, Now: queueAt}
			},
			expected: domain.TaskSlotIndeterminate,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			actual := domain.DecideTaskSlotState(testCase.input(t))
			if actual != testCase.expected {
				t.Fatalf("期望 %s，实际 %s", testCase.expected, actual)
			}
		})
	}
}

// TestTaskSlotStateOnlyIdleAllowsAutomaticClaim 校验只有空闲状态允许自动领取。
func TestTaskSlotStateOnlyIdleAllowsAutomaticClaim(t *testing.T) {
	t.Parallel()
	cases := []struct {
		state    domain.TaskSlotState
		occupies bool
		claim    bool
	}{
		{domain.TaskSlotIdle, false, true},
		{domain.TaskSlotClaimed, true, false},
		{domain.TaskSlotExecuting, true, false},
		{domain.TaskSlotFinalizing, true, false},
		{domain.TaskSlotAwaitingHuman, false, false},
		{domain.TaskSlotIndeterminate, false, false},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(string(testCase.state), func(t *testing.T) {
			t.Parallel()
			if actual := testCase.state.OccupiesSlot(); actual != testCase.occupies {
				t.Fatalf("占名额期望 %v，实际 %v", testCase.occupies, actual)
			}
			if actual := testCase.state.AllowsAutomaticClaim(); actual != testCase.claim {
				t.Fatalf("自动领取期望 %v，实际 %v", testCase.claim, actual)
			}
		})
	}
}

// TestDecideTaskSlotStateDoesNotTreatIndeterminateAsFailure 校验结论不明不得退化为普通失败。
//
// 结果无法确认的任务既不占名额也不能自动重试：把它当作 failed 会立刻重放未经核验的
// 外部副作用，正是 P2 明确禁止的行为。
func TestDecideTaskSlotStateDoesNotTreatIndeterminateAsFailure(t *testing.T) {
	t.Parallel()
	task := leaseTask(t, "worker-a", queueAt.Add(-time.Minute))
	task.Status = domain.TaskStatusVerifying
	state := domain.DecideTaskSlotState(domain.TaskSlotInput{Task: task, Now: queueAt})
	if state == domain.TaskSlotAwaitingHuman {
		t.Fatal("结论不明的现场不应被归类为等待人工处理的普通失败")
	}
	if state != domain.TaskSlotIndeterminate {
		t.Fatalf("期望 indeterminate，实际 %s", state)
	}
	if state.AllowsAutomaticClaim() {
		t.Fatal("结论不明的任务不得被自动领取")
	}
}

// TestDecideTaskSlotStateUsesWorkflowRunLease 校验执行阶段的名额由执行记录租约决定。
func TestDecideTaskSlotStateUsesWorkflowRunLease(t *testing.T) {
	t.Parallel()
	task := queueTask(t, queueAt)
	task.Status = domain.TaskStatusDeveloping
	step := queueStep(t, task.ID, 1, domain.StepStatusDeveloping)
	run, err := domain.NewWorkflowRun(task.ID, []domain.Step{step}, queueAt)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := run.Start("worker-a", time.Minute, queueAt); err != nil {
		t.Fatalf("启动执行记录失败: %v", err)
	}
	state := domain.DecideTaskSlotState(domain.TaskSlotInput{
		Task:              task,
		ActiveWorkflowRun: &run,
		Now:               queueAt,
	})
	if state != domain.TaskSlotExecuting {
		t.Fatalf("执行记录持有有效执行权时应为 executing，实际 %s", state)
	}
	// 执行权过期后名额不再被占用，但结论仍不是"可自动领取"。
	later := queueAt.Add(2 * time.Minute)
	expired := domain.DecideTaskSlotState(domain.TaskSlotInput{
		Task:              task,
		ActiveWorkflowRun: &run,
		Now:               later,
	})
	if expired.OccupiesSlot() {
		t.Fatalf("执行权过期后不应继续占用名额，实际 %s", expired)
	}
	if expired.AllowsAutomaticClaim() {
		t.Fatalf("执行权过期后不得自动领取，实际 %s", expired)
	}
}

// TestDecideTaskSlotStateBlockedRunAwaitsHuman 校验阻塞的执行记录等待人工处理。
func TestDecideTaskSlotStateBlockedRunAwaitsHuman(t *testing.T) {
	t.Parallel()
	task := queueTask(t, queueAt)
	task.Status = domain.TaskStatusDeveloping
	step := queueStep(t, task.ID, 1, domain.StepStatusFailed)
	run, err := domain.NewWorkflowRun(task.ID, []domain.Step{step}, queueAt)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := run.Start("worker-a", time.Minute, queueAt); err != nil {
		t.Fatalf("启动执行记录失败: %v", err)
	}
	if err := run.Stop(domain.WorkflowRunBlocked, "verification_failed", "验证未通过", queueAt); err != nil {
		t.Fatalf("停止执行记录失败: %v", err)
	}
	state := domain.DecideTaskSlotState(domain.TaskSlotInput{
		Task:              task,
		ActiveWorkflowRun: &run,
		Now:               queueAt,
	})
	if state != domain.TaskSlotAwaitingHuman {
		t.Fatalf("阻塞的执行记录应等待人工处理，实际 %s", state)
	}
	if state.AllowsAutomaticClaim() {
		t.Fatal("等待人工处理的任务不得被自动领取")
	}
}

// queueStep 构造一条属于指定任务、带指定状态的开发步骤。
func queueStep(
	t *testing.T,
	taskID domain.ID,
	sequence int,
	status domain.StepStatus,
) domain.Step {
	t.Helper()
	role := domain.Role{
		ID:           domain.NewID(),
		Name:         "开发角色",
		Type:         domain.RoleTypeDeveloper,
		Runner:       domain.RoleRunnerPi,
		Model:        "group/free",
		SystemPrompt: "你是开发者",
		Enabled:      true,
		CreatedAt:    queueAt,
		UpdatedAt:    queueAt,
	}
	step, err := domain.NewAgentStep(
		taskID,
		sequence,
		domain.StepTypeDevelop,
		"实现接口",
		"按需求实现接口",
		"",
		role,
		domain.VerificationSpec{
			Commands: []domain.VerificationCommand{
				{Name: "build", Command: "go", Args: []string{"build", "./..."}},
			},
		},
		domain.RetryConfig{MaxAttempts: 1},
		queueAt,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	step.Status = status
	return step
}
