// Package app_test 提供任务级编排器与修复闭环协作的单元测试。
//
// 用例使用真实 app.TaskWorkflowService 与真实数据库，只把单步骤闭环替换为受控替身：
// 编排器必须把修复循环给出的结构化停止类别映射为正确的工作流状态与错误分类，不得
// 在编排层重新推导一套规则。
package app_test

import (
	"testing"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// repairStopCase 是一组"修复停止类别 → 工作流状态与错误分类"的期望。
type repairStopCase struct {
	name       string
	class      domain.RepairFailureClass
	status     domain.WorkflowRunStatus
	errorClass string
	// stepStatus 是该类别下修复循环写回步骤的终态。
	stepStatus domain.StepStatus
}

// TestWorkflowConsumesRepairStopClass 验证编排器消费修复循环的停止类别。
func TestWorkflowConsumesRepairStopClass(t *testing.T) {
	t.Parallel()
	cases := []repairStopCase{
		{
			name:       "开发失败",
			class:      domain.RepairClassDeveloperFailed,
			status:     domain.WorkflowRunBlocked,
			errorClass: "developer_failed",
			stepStatus: domain.StepStatusFailed,
		},
		{
			name:       "开发超时",
			class:      domain.RepairClassDeveloperTimedOut,
			status:     domain.WorkflowRunRecoveryRequired,
			errorClass: "developer_interrupted",
			stepStatus: domain.StepStatusFailed,
		},
		{
			name:       "开发中断",
			class:      domain.RepairClassDeveloperInterrupted,
			status:     domain.WorkflowRunRecoveryRequired,
			errorClass: "developer_interrupted",
			stepStatus: domain.StepStatusRecoveryRequired,
		},
		{
			name:       "达到尝试上限",
			class:      domain.RepairClassAttemptsExhausted,
			status:     domain.WorkflowRunBlocked,
			errorClass: "attempts_exhausted",
			stepStatus: domain.StepStatusFailed,
		},
		{
			name:       "结论不确定",
			class:      domain.RepairClassVerificationInconclusive,
			status:     domain.WorkflowRunRecoveryRequired,
			errorClass: "verification_inconclusive",
			stepStatus: domain.StepStatusRecoveryRequired,
		},
		{
			name:       "基础设施异常",
			class:      domain.RepairClassInfrastructure,
			status:     domain.WorkflowRunRecoveryRequired,
			errorClass: "verification_unconfirmed",
			stepStatus: domain.StepStatusRecoveryRequired,
		},
		{
			name:       "记录未落库",
			class:      domain.RepairClassNotPersisted,
			status:     domain.WorkflowRunRecoveryRequired,
			errorClass: "verification_unconfirmed",
			stepStatus: domain.StepStatusRecoveryRequired,
		},
		{
			name:       "反馈不可用",
			class:      domain.RepairClassFeedbackUnavailable,
			status:     domain.WorkflowRunRecoveryRequired,
			errorClass: "verification_unconfirmed",
			stepStatus: domain.StepStatusRecoveryRequired,
		},
		{
			name:       "验证未启动",
			class:      domain.RepairClassVerificationNotRun,
			status:     domain.WorkflowRunRecoveryRequired,
			errorClass: "verification_unconfirmed",
			stepStatus: domain.StepStatusRecoveryRequired,
		},
		{
			name:       "未开启修复",
			class:      domain.RepairClassRepairDisabled,
			status:     domain.WorkflowRunBlocked,
			errorClass: "verification_failed",
			stepStatus: domain.StepStatusFailed,
		},
		{
			name:       "步骤不适用修复",
			class:      domain.RepairClassUnsupportedStep,
			status:     domain.WorkflowRunBlocked,
			errorClass: "step_not_repairable",
			stepStatus: domain.StepStatusRecoveryRequired,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
				stepCount: 2,
				replies: []taskWorkflowReply{{
					kind:         taskReplyRepairStop,
					repairClass:  testCase.class,
					repairResult: testCase.stepStatus,
				}},
			})
			if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
				TaskID: fixture.task.ID.String(),
			}); err != nil {
				t.Fatalf("启动工作流失败: %v", err)
			}
			result := waitTaskRunStatus(t, fixture.service, fixture.task.ID, workflowSettled)
			if result.Run.Status != testCase.status {
				t.Fatalf("%s: 期望工作流状态 %s，实际 %s（%s）",
					testCase.name, testCase.status, result.Run.Status, result.Run.ErrorSummary)
			}
			if result.Run.ErrorClass != testCase.errorClass {
				t.Fatalf("%s: 期望错误分类 %q，实际 %q",
					testCase.name, testCase.errorClass, result.Run.ErrorClass)
			}
			// 修复循环停止时不得推进到下一步骤，也不得执行代码保存。
			if calls := fixture.runner.callCount(); calls != 1 {
				t.Fatalf("%s: 停止后不得继续执行下一步骤，实际 %d 次", testCase.name, calls)
			}
			if fixture.codeSaver.callCount() != 0 {
				t.Fatalf("%s: 停止时不得执行代码保存，实际 %d 次",
					testCase.name, fixture.codeSaver.callCount())
			}
			if result.Run.CompletedSteps != 0 {
				t.Fatalf("%s: 未通过的步骤不得计入已完成进度: %d",
					testCase.name, result.Run.CompletedSteps)
			}
		})
	}
}
