package domain_test

import (
	"testing"

	"github.com/lingting/agentflow/internal/domain"
)

func TestParseRequirementAnalysis(t *testing.T) {
	raw := []byte(`{
		"summary":"实现规划工作台",
		"goals":["支持多轮需求澄清"],
		"current_state":"已有规划会话持久化",
		"scope":["需求分析","实施方案预览"],
		"out_of_scope":["正式任务创建"],
		"constraints":["确认前不得写入正式任务"],
		"risks":["模型输出可能不是合法 JSON"],
		"acceptance_criteria":["解析失败时不产生 Revision"]
	}`)
	if _, err := domain.ParseRequirementAnalysis(raw); err != nil {
		t.Fatalf("ParseRequirementAnalysis() error = %v", err)
	}
}

func TestParseRequirementAnalysisRejectsUnknownFields(t *testing.T) {
	raw := []byte(`{
		"summary":"summary",
		"goals":["goal"],
		"current_state":"state",
		"scope":["scope"],
		"out_of_scope":["out"],
		"constraints":["constraint"],
		"risks":["risk"],
		"acceptance_criteria":["criterion"],
		"unexpected":"value"
	}`)
	if _, err := domain.ParseRequirementAnalysis(raw); err == nil {
		t.Fatal("ParseRequirementAnalysis() accepted an unknown field")
	}
}

func TestParseImplementationPlanValidatesDependencies(t *testing.T) {
	raw := []byte(`{
		"summary":"按依赖顺序实施",
		"tasks":[
			{"id":"design","title":"设计契约","description":"定义输出结构","acceptance_criteria":["字段明确"]},
			{"id":"implement","title":"实现解析","description":"严格解析 JSON","depends_on":["design"],"acceptance_criteria":["非法 JSON 被拒绝"]}
		]
	}`)
	if _, err := domain.ParseImplementationPlan(raw); err != nil {
		t.Fatalf("ParseImplementationPlan() error = %v", err)
	}
}

func TestParseImplementationPlanRejectsForwardDependency(t *testing.T) {
	raw := []byte(`{
		"summary":"invalid order",
		"tasks":[
			{"id":"first","title":"first","description":"first","depends_on":["later"],"acceptance_criteria":["done"]},
			{"id":"later","title":"later","description":"later","acceptance_criteria":["done"]}
		]
	}`)
	if _, err := domain.ParseImplementationPlan(raw); err == nil {
		t.Fatal("ParseImplementationPlan() accepted a forward dependency")
	}
}

func TestParseImplementationPlanRejectsTrailingDocument(t *testing.T) {
	raw := []byte(`{"summary":"plan","tasks":[{"id":"one","title":"one","description":"one","acceptance_criteria":["done"]}]} {}`)
	if _, err := domain.ParseImplementationPlan(raw); err == nil {
		t.Fatal("ParseImplementationPlan() accepted multiple JSON documents")
	}
}
