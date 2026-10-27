// Package workflow_test 验证真实编排链路上的步骤起止时间（P4-M3-6）。
//
// 断言对象一律是数据库里的步骤记录，而不是方法的返回值：时间字段必须由领域状态
// 迁移真正写入并落库，才能在刷新、重启与接口读取之后保持一致。
package workflow_test

import (
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
)

// TestE2ESuccessPersistsStepTimestamps 校验成功路径上每个步骤都有真实的起止时间。
func TestE2ESuccessPersistsStepTimestamps(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:    true,
		agentReplies: e2eHappyReplies(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("成功路径应完成，实际 %s", formatE2ERun(result.Run))
	}

	steps := fixture.reloadSteps(t)
	if len(steps) == 0 {
		t.Fatal("成功路径必须留下计划步骤")
	}
	for _, step := range steps {
		if step.Status != domain.StepStatusCompleted {
			t.Fatalf("步骤 %d 应为 completed，实际 %s", step.Sequence, step.Status)
		}
		if step.StartedAt == nil {
			t.Fatalf("步骤 %d 必须记录开始时间", step.Sequence)
		}
		if step.FinishedAt == nil {
			t.Fatalf("步骤 %d 必须记录结束时间", step.Sequence)
		}
		if step.FinishedAt.Before(*step.StartedAt) {
			t.Fatalf(
				"步骤 %d 的结束时间早于开始时间: %s < %s",
				step.Sequence,
				step.FinishedAt,
				step.StartedAt,
			)
		}
		// 开始时间不能等于计划创建时间：它表达"真正进入执行"，不是"计划已落库"。
		if step.StartedAt.Equal(step.CreatedAt) {
			t.Fatalf(
				"步骤 %d 的开始时间与创建时间相同，说明被填充而不是真实执行时间",
				step.Sequence,
			)
		}
	}
}

// TestE2ESuccessWorkflowStatusExposesStepTimestamps 校验状态查询结果带上真实时间。
//
// 编排器的进度快照是接口的直接数据源：它必须来自数据库中的步骤记录，否则即使
// 数据库有值，接口与页面仍然拿不到时间。
func TestE2ESuccessWorkflowStatusExposesStepTimestamps(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:    true,
		agentReplies: e2eHappyReplies(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)
	if len(result.Steps) == 0 {
		t.Fatal("状态查询必须返回步骤快照")
	}
	stored := fixture.reloadSteps(t)
	byID := make(map[domain.ID]domain.Step, len(stored))
	for _, step := range stored {
		byID[step.ID] = step
	}
	for _, view := range result.Steps {
		step, ok := byID[view.StepID]
		if !ok {
			t.Fatalf("状态快照中的步骤 %s 不在数据库中", view.StepID)
		}
		if step.StartedAt == nil || view.StartedAt == nil {
			t.Fatalf("步骤 %d 的开始时间缺失", view.Sequence)
		}
		if !view.StartedAt.Equal(*step.StartedAt) {
			t.Fatalf(
				"步骤 %d 的开始时间与数据库不一致: %s != %s",
				view.Sequence,
				view.StartedAt,
				step.StartedAt,
			)
		}
		if step.FinishedAt == nil || view.FinishedAt == nil {
			t.Fatalf("步骤 %d 的结束时间缺失", view.Sequence)
		}
		if !view.FinishedAt.Equal(*step.FinishedAt) {
			t.Fatalf(
				"步骤 %d 的结束时间与数据库不一致: %s != %s",
				view.Sequence,
				view.FinishedAt,
				step.FinishedAt,
			)
		}
	}
}

// TestE2EFailedStepKeepsFailureTimestamps 校验失败步骤保留真实起止时间与失败摘要。
//
// 失败不允许被序列化成成功：步骤必须处于 failed，结束时间真实落库，且失败摘要
// 保留下来供页面展示。
func TestE2EFailedStepKeepsFailureTimestamps(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver: true,
		agentReplies: []e2eAgentReply{
			// Planner 生成计划。
			{status: agent.RunCompleted, finalText: e2ePlanJSON},
			// 规划阶段步骤的 Developer 与 Validator。
			{status: agent.RunCompleted, finalText: "计划已生成"},
			{status: agent.RunCompleted, finalText: e2eValidatorPassJSON},
			// 第 1 个开发步骤的 Developer 非零退出。
			{status: agent.RunFailed, failure: "编译失败"},
		},
	})
	fixture.launchTask(t)
	result := fixture.settle(t)

	steps := fixture.reloadSteps(t)
	if len(steps) == 0 {
		t.Fatal("失败路径同样必须留下计划步骤")
	}
	failed := false
	for _, step := range steps {
		switch step.Status {
		case domain.StepStatusCompleted:
			if step.StartedAt == nil || step.FinishedAt == nil {
				t.Fatalf("已完成步骤 %d 必须同时有起止时间", step.Sequence)
			}
		case domain.StepStatusFailed, domain.StepStatusRecoveryRequired:
			failed = true
			if step.StartedAt == nil {
				t.Fatalf("失败步骤 %d 必须记录开始时间", step.Sequence)
			}
			if step.FinishedAt == nil {
				t.Fatalf("失败步骤 %d 必须记录结束时间", step.Sequence)
			}
			if step.FailureSummary == "" {
				t.Fatalf("失败步骤 %d 必须保留失败摘要", step.Sequence)
			}
		}
	}
	if !failed {
		t.Fatalf("失败路径必须留下失败或待核验的步骤，实际 %s", formatE2ERun(result.Run))
	}
	if result.Run.Status == domain.WorkflowRunCompleted {
		t.Fatal("存在失败步骤时工作流不得被判定为完成")
	}
}

// TestE2EPendingStepsHaveNoTimestamps 校验从未执行的步骤时间保持为空。
//
// 第一步失败后，后续步骤不会被推进：它们必须仍然没有起止时间，页面因此显示中性
// 占位符，而不是伪造的"已执行"时间。
func TestE2EPendingStepsHaveNoTimestamps(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver: true,
		agentReplies: []e2eAgentReply{
			{status: agent.RunCompleted, finalText: e2ePlanJSON},
			{status: agent.RunCompleted, finalText: "计划已生成"},
			{status: agent.RunCompleted, finalText: e2eValidatorPassJSON},
			{status: agent.RunFailed, failure: "编译失败"},
		},
	})
	fixture.launchTask(t)
	fixture.settle(t)

	pending := 0
	for _, step := range fixture.reloadSteps(t) {
		if step.Status != domain.StepStatusPending {
			continue
		}
		pending++
		if step.StartedAt != nil || step.FinishedAt != nil {
			t.Fatalf(
				"未执行的步骤 %d 不应有起止时间: %v / %v",
				step.Sequence,
				step.StartedAt,
				step.FinishedAt,
			)
		}
	}
	if pending == 0 {
		t.Fatal("第一步失败后应当仍有未执行的步骤")
	}
}

// TestE2EStepTimestampsUseUTCMillisecondPrecision 校验时间精度与时区约定。
//
// 持久化层使用 UTC Unix 毫秒：读回的时间必须是 UTC 且精度不高于毫秒，否则前端会
// 展示与数据库不一致的本地时间。
func TestE2EStepTimestampsUseUTCMillisecondPrecision(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:    true,
		agentReplies: e2eHappyReplies(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	fixture.settle(t)

	steps := fixture.reloadSteps(t)
	if len(steps) == 0 {
		t.Fatal("成功路径必须留下计划步骤")
	}
	for _, step := range steps {
		for name, value := range map[string]*time.Time{
			"started_at":  step.StartedAt,
			"finished_at": step.FinishedAt,
		} {
			if value == nil {
				t.Fatalf("步骤 %d 缺少 %s", step.Sequence, name)
			}
			if value.Location() != time.UTC {
				t.Fatalf("步骤 %d 的 %s 必须是 UTC: %s", step.Sequence, name, value.Location())
			}
			if value.Nanosecond()%int(time.Millisecond) != 0 {
				t.Fatalf(
					"步骤 %d 的 %s 精度高于毫秒: %d",
					step.Sequence,
					name,
					value.Nanosecond(),
				)
			}
		}
	}
}
