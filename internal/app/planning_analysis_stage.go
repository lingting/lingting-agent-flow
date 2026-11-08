package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
)

// PlanningAnalysisStage 表示两阶段分析中的一个阶段。
//
// 阶段是编排的显式事实而不是隐含的调用顺序：失败摘要必须能指出"哪一阶段没有产出可用
// 结果"，重试与诊断也按阶段定位。
type PlanningAnalysisStage string

// 两阶段分析的阶段取值。
const (
	// PlanningAnalysisStageRequirementAnalysis 是需求分析阶段，由需求分析师执行。
	PlanningAnalysisStageRequirementAnalysis PlanningAnalysisStage = "requirement_analysis"
	// PlanningAnalysisStageImplementationPlan 是方案规划阶段，由方案规划师执行。
	PlanningAnalysisStageImplementationPlan PlanningAnalysisStage = "implementation_plan"
)

// Validate 校验阶段取值是否受支持。
func (s PlanningAnalysisStage) Validate() error {
	switch s {
	case PlanningAnalysisStageRequirementAnalysis, PlanningAnalysisStageImplementationPlan:
		return nil
	default:
		return fmt.Errorf("规划分析阶段 %q 不受支持", string(s))
	}
}

// RoleType 返回该阶段使用的固定角色类型。
//
// 阶段与角色一一对应：需求分析阶段只能由需求分析师执行，方案规划阶段只能由方案规划师
// 执行，不存在"用同一个角色跑完两个阶段"的降级路径。
func (s PlanningAnalysisStage) RoleType() (domain.RoleType, error) {
	switch s {
	case PlanningAnalysisStageRequirementAnalysis:
		return domain.RoleTypeRequirementAnalyst, nil
	case PlanningAnalysisStageImplementationPlan:
		return domain.RoleTypePlanner, nil
	default:
		return "", fmt.Errorf("规划分析阶段 %q 不受支持", string(s))
	}
}

// Label 返回阶段的中文说明，用于失败摘要。
func (s PlanningAnalysisStage) Label() string {
	switch s {
	case PlanningAnalysisStageRequirementAnalysis:
		return "需求分析阶段"
	case PlanningAnalysisStageImplementationPlan:
		return "方案规划阶段"
	default:
		return "未知阶段"
	}
}

// planningStageResult 是一个阶段的真实执行事实。
//
// 它把"Pi 怎么结束的"与"输出能不能用"分开记录：调用成功但输出不可解析同样是本阶段
// 失败，而调用失败永远不会被当成"产出了可用结果"。
type planningStageResult struct {
	// Stage 是阶段。
	Stage PlanningAnalysisStage
	// Text 是模型的最终文本。
	Text string
	// RoleSessionID 是本次调用实际使用的角色 Pi 会话标识。
	RoleSessionID string
	// Failure 是"本阶段没有产出可用结果"的脱敏说明；为空表示调用正常结束。
	Failure string
}

// usable 判断本阶段是否产出了可供解析的文本。
func (r planningStageResult) usable() bool {
	return strings.TrimSpace(r.Failure) == ""
}

// planningStageSnapshot 是一份通过领域校验的阶段输出及其持久化快照。
type planningStageSnapshot struct {
	// Stage 是产生它的阶段。
	Stage PlanningAnalysisStage
	// Analysis 是需求分析阶段的结果；其他阶段为零值。
	Analysis domain.RequirementAnalysis
	// Plan 是方案规划阶段的结果；其他阶段为零值。
	Plan domain.ImplementationPlan
	// Snapshot 是规范化 JSON 快照，写入 Revision 与角色消息的都是这一份文本。
	Snapshot json.RawMessage
}

// classifyPlanningStage 把一个阶段的调用结果归类为可解析文本或明确失败。
//
// 归类顺序固定：先看 Pi 是否正常结束，再确认"成功"留下了可核验的运行痕迹，最后确认
// 最终文本非空。任何一步不成立都不会产出文本，因此不可能出现"调用失败但结果被当成
// 分析结论"。
func classifyPlanningStage(
	stage PlanningAnalysisStage,
	result PlanningAgentResult,
	runErr error,
	stopReason string,
) planningStageResult {
	classified := planningStageResult{
		Stage:         stage,
		Text:          result.Result.FinalText,
		RoleSessionID: result.RoleSessionID,
	}
	switch result.Result.Status {
	case agent.RunCompleted:
		if err := checkPlanningStageStarted(result, runErr); err != nil {
			classified.Failure = clipReviewText(err.Error(), planningStageFailureLength)
			return classified
		}
	case agent.RunTimedOut:
		classified.Failure = stage.Label() + "执行超时"
		return classified
	case agent.RunCancelled:
		if stopReason != "" {
			classified.Failure = stage.Label() + "被中断: " + stopReason
			return classified
		}
		classified.Failure = stage.Label() + "被调用方取消"
		return classified
	default:
		classified.Failure = planningStageFailureText(stage, result, runErr)
		return classified
	}
	if strings.TrimSpace(result.Result.FinalText) == "" {
		classified.Failure = stage.Label() + "没有输出任何内容"
		return classified
	}
	return classified
}

// checkPlanningStageStarted 核验"成功"是否留下了可核验的运行痕迹。
//
// 与其余角色调用一致：没有完整会话也没有进程标识的"成功"无法核验，不能据此解析结论。
func checkPlanningStageStarted(result PlanningAgentResult, runErr error) error {
	if errorSummary(runErr) != "" {
		return nil
	}
	if result.Result.Session.Completeness == agent.SessionComplete {
		return nil
	}
	if result.Result.ProcessID > 0 {
		return nil
	}
	return fmt.Errorf("报告成功，但没有可核验的运行痕迹（会话不完整且缺少进程标识）")
}

// planningStageFailureText 组合阶段失败的可诊断说明。
func planningStageFailureText(
	stage PlanningAnalysisStage,
	result PlanningAgentResult,
	runErr error,
) string {
	candidates := []string{result.Result.Failure, errorSummary(runErr)}
	for _, candidate := range candidates {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return stage.Label() + "未正常结束: " +
				clipReviewText(trimmed, planningStageFailureLength)
		}
	}
	return stage.Label() + "未正常结束"
}

// parsePlanningStageOutput 按阶段把最终文本解析为结构化快照。
//
// 两个阶段共用同一个入口，解析规则只在 domain 定义一次：这里只负责按阶段选择契约类型，
// 并把领域错误原样返回，让调用方按类别区分"输出无法解析"与"输出违反领域约束"。
func parsePlanningStageOutput(
	stage PlanningAnalysisStage,
	text string,
) (planningStageSnapshot, error) {
	switch stage {
	case PlanningAnalysisStageRequirementAnalysis:
		analysis, err := domain.ParseRequirementAnalysis(text)
		if err != nil {
			return planningStageSnapshot{}, err
		}
		snapshot, err := analysis.Snapshot()
		if err != nil {
			return planningStageSnapshot{}, err
		}
		return planningStageSnapshot{Stage: stage, Analysis: analysis, Snapshot: snapshot}, nil
	case PlanningAnalysisStageImplementationPlan:
		plan, err := domain.ParseImplementationPlan(text)
		if err != nil {
			return planningStageSnapshot{}, err
		}
		snapshot, err := plan.Snapshot()
		if err != nil {
			return planningStageSnapshot{}, err
		}
		return planningStageSnapshot{Stage: stage, Plan: plan, Snapshot: snapshot}, nil
	default:
		return planningStageSnapshot{}, fmt.Errorf("规划分析阶段 %q 不受支持", string(stage))
	}
}
