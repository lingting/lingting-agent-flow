// Package app_test 提供验证失败修复闭环的单元测试辅助代码。
//
// 用例复用 step_workflow_test.go 的夹具：开发阶段由受控 Agent 替身返回结果，验证阶段
// 由受控检查结果驱动，数据库、状态机与持久化写入全部走产品代码，因此断言的是数据库
// 里真实留下的现场。
package app_test

import (
	"context"
	"errors"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// workflowStepTypeOf 返回用例指定的步骤类型，零值使用开发步骤。
func workflowStepTypeOf(stepType domain.StepType) domain.StepType {
	if stepType == "" {
		return domain.StepTypeDevelop
	}
	return stepType
}

// workflowRetryOf 返回用例指定的重试策略，零值使用上限 1 的默认策略。
//
// 默认上限为 1 使既有用例保持"单轮"语义：只有显式提高上限的用例才会进入修复循环。
func workflowRetryOf(retry domain.RetryConfig) domain.RetryConfig {
	if retry.IsZero() {
		return domain.RetryConfig{MaxAttempts: 1}
	}
	return retry
}

// workflowRoleTypeOf 返回步骤类型对应的角色类型。
//
// 角色类型必须与步骤类型一致，否则步骤在校验阶段就会被拒绝：用例构造规划步骤时
// 必须使用规划角色。
func workflowRoleTypeOf(stepType domain.StepType) domain.RoleType {
	if stepType == domain.StepTypePlan {
		return domain.RoleTypePlanner
	}
	return domain.RoleTypeDeveloper
}

// workflowStore 是闭环协调器的测试存储：真实存储上加一个可注入的失败点。
//
// 失败来自存储层而不是业务代码，因此验证的是真实的错误传播与"没有修复依据就不重放
// Agent 调用"的语义。
type workflowStore struct {
	app.StepWorkflowStore
	createFeedbackErr error
}

// CreateValidationFailureFeedback 在配置了错误时返回该错误。
func (s *workflowStore) CreateValidationFailureFeedback(
	ctx context.Context,
	feedback domain.ValidationFailureFeedback,
) error {
	if s.createFeedbackErr != nil {
		return s.createFeedbackErr
	}
	return s.StepWorkflowStore.CreateValidationFailureFeedback(ctx, feedback)
}

// workflowStoreOf 构造闭环协调器使用的存储，按需注入反馈写入失败。
func workflowStoreOf(store app.StepWorkflowStore, createErr error) app.StepWorkflowStore {
	workflow := &workflowStore{StepWorkflowStore: store, createFeedbackErr: createErr}
	return workflow
}

// errFeedbackInjected 是反馈写入失败的注入错误。
var errFeedbackInjected = errors.New("注入的反馈写入失败")

// 编译期确认替身仍满足闭环协调器的存储契约。
var _ app.StepWorkflowStore = (*workflowStore)(nil)
