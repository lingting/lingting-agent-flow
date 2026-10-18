package workflow_test

import (
	"context"
	"testing"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/workflow"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"github.com/lingting/agentflow/tests/internal/workflowfake"
)

// agentFailedStatus 返回表示执行失败的运行状态。
func agentFailedStatus() agent.RunStatus {
	return agent.RunFailed
}

// agentCancelledStatus 返回表示调用方取消的运行状态。
func agentCancelledStatus() agent.RunStatus {
	return agent.RunCancelled
}

// agentTimedOutStatus 返回表示执行超时的运行状态。
func agentTimedOutStatus() agent.RunStatus {
	return agent.RunTimedOut
}

// developmentSteps 过滤出计划中的开发步骤。
//
// 内部阶段（规划等）使用独立序号区间，因此不会被当作计划步骤。
func developmentSteps(steps []domain.Step) []domain.Step {
	development := make([]domain.Step, 0, len(steps))
	for _, step := range steps {
		if step.Type == domain.StepTypeDevelop {
			development = append(development, step)
		}
	}
	return development
}

// loadVerifications 读取任务全部验证结果。
func loadVerifications(
	t *testing.T,
	fixture *testkit.WorkflowFixture,
	ctx context.Context,
) []domain.VerificationResult {
	t.Helper()
	results := make([]domain.VerificationResult, 0, 4)
	for _, step := range fixture.Steps(t, ctx) {
		for _, attempt := range fixture.Attempts(t, ctx, step.ID) {
			loaded, err := fixture.DB.ListVerificationResultsByAttempt(ctx, attempt.ID)
			if err != nil {
				t.Fatalf("读取验证结果失败: %v", err)
			}
			results = append(results, loaded...)
		}
	}
	return results
}

// loadAttempts 读取任务全部执行尝试。
func loadAttempts(
	t *testing.T,
	fixture *testkit.WorkflowFixture,
	ctx context.Context,
) []domain.Attempt {
	t.Helper()
	attempts := make([]domain.Attempt, 0, 4)
	for _, step := range fixture.Steps(t, ctx) {
		attempts = append(attempts, fixture.Attempts(t, ctx, step.ID)...)
	}
	return attempts
}

// withoutValidator 关闭任务的辅助评估角色。
//
// 用于隔离"确定性验证"与"辅助评估"两类断言：关闭后验证判定只依据确定性命令。
func withoutValidator(t *testing.T, fixture *testkit.WorkflowFixture) {
	t.Helper()
	ctx := context.Background()
	task := fixture.Reload(t, ctx)
	task.Config.ValidatorRoleID = ""
	if err := fixture.DB.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
}

// newRecovery 构造一个绑定到测试数据的恢复核验器。
func newRecovery(
	t *testing.T,
	fixture *testkit.WorkflowFixture,
	ports *workflowfake.Ports,
) *workflow.Recovery {
	t.Helper()
	recovery, err := workflow.NewRecovery(workflow.RecoveryOptions{
		Store: fixture.DB,
		Ports: ports.Ports(),
	})
	if err != nil {
		t.Fatalf("构造恢复核验器失败: %v", err)
	}
	return recovery
}

// newWorkflowEngineWithOwner 构造指定执行者标识的工作流引擎。
func newWorkflowEngineWithOwner(
	t *testing.T,
	fixture *testkit.WorkflowFixture,
	ports *workflowfake.Ports,
	owner string,
) (*workflow.Engine, error) {
	t.Helper()
	ports.Agents.Store = fixture.DB
	return workflow.NewEngine(workflow.Options{
		Store:       fixture.DB,
		Ports:       ports.Ports(),
		Owner:       owner,
		MaxParallel: 4,
	})
}
