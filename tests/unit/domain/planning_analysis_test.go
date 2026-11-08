// Package domain_test 提供规划结构化契约相关领域规则的单元测试。
//
// 需求分析与实施方案的解析与校验都是纯函数：不读数据库、不启动进程，因此这里直接构造
// 角色输出文本，覆盖"合法结构化输出被接受""任何不规范输出都被拒绝"与"持久化快照可以
// 独立读回"三类事实。
package domain_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// validRequirementAnalysisJSON 返回一份通过领域校验的需求分析 JSON。
func validRequirementAnalysisJSON() string {
	return `{"summary":"补齐规划会话的结构化分析",` +
		`"goals":["产出结构化需求分析"],` +
		`"current_state":["已存在规划会话与消息表"],` +
		`"scope":["internal/domain"],` +
		`"out_of_scope":["不创建正式任务"],` +
		`"constraints":["保持只读"],` +
		`"risks":["模型输出不稳定"],` +
		`"acceptance_criteria":["非法输出一律失败"]}`
}

// validImplementationPlanJSON 返回一份通过领域校验的实施方案 JSON。
func validImplementationPlanJSON() string {
	return `{"summary":"按两个阶段推进",` +
		`"tasks":[` +
		`{"id":"task-1","title":"领域契约","description":"定义并校验结构",` +
		`"depends_on":[],"acceptance_criteria":["非法字段被拒绝"]},` +
		`{"id":"task-2","title":"编排","description":"串联两个角色",` +
		`"depends_on":["task-1"],"acceptance_criteria":["失败不留下半成品"]}]}`
}

// TestParseRequirementAnalysisAcceptsStructuredOutput 验证合法需求分析可以被解析。
func TestParseRequirementAnalysisAcceptsStructuredOutput(t *testing.T) {
	t.Parallel()
	text := "```json\n" + validRequirementAnalysisJSON() + "\n```"
	analysis, err := domain.ParseRequirementAnalysis(text)
	if err != nil {
		t.Fatalf("解析需求分析失败: %v", err)
	}
	if analysis.Summary != "补齐规划会话的结构化分析" {
		t.Fatalf("摘要解析错误: %q", analysis.Summary)
	}
	if len(analysis.AcceptanceCriteria) != 1 {
		t.Fatalf("验收标准解析错误: %+v", analysis.AcceptanceCriteria)
	}
}

// TestParseRequirementAnalysisRejectsUnusableOutput 验证不可用的需求分析输出一律被拒绝。
func TestParseRequirementAnalysisRejectsUnusableOutput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want error
	}{
		{name: "空输出", text: "   ", want: domain.ErrPlanningOutputUnparseable},
		{
			name: "纯自然语言",
			text: "这次需求很清晰，直接实现即可。",
			want: domain.ErrPlanningOutputUnparseable,
		},
		{
			name: "非法 JSON",
			text: `{"summary":}`,
			want: domain.ErrPlanningOutputUnparseable,
		},
		{
			name: "未知字段",
			text: strings.Replace(
				validRequirementAnalysisJSON(),
				`"summary":`,
				`"confidence":0.9,"summary":`,
				1,
			),
			want: domain.ErrPlanningOutputUnparseable,
		},
		{
			name: "尾随第二份 JSON 文档",
			text: validRequirementAnalysisJSON() + "\n" + validRequirementAnalysisJSON(),
			want: domain.ErrPlanningOutputUnparseable,
		},
		{
			name: "尾随说明文字",
			text: validRequirementAnalysisJSON() + "\n以上是分析结果。",
			want: domain.ErrPlanningOutputUnparseable,
		},
		{
			name: "字面量 null",
			text: "null",
			want: domain.ErrPlanningOutputUnparseable,
		},
		{
			name: "数组而不是对象",
			text: `[]`,
			want: domain.ErrPlanningOutputUnparseable,
		},
		{
			name: "字段类型不符",
			text: `{"summary":1,"goals":["a"],"current_state":["b"],"scope":["c"],` +
				`"out_of_scope":["d"],"constraints":["e"],"risks":["f"],"acceptance_criteria":["g"]}`,
			want: domain.ErrPlanningOutputUnparseable,
		},
		{
			name: "缺少摘要",
			text: `{"goals":["a"],"current_state":["b"],"scope":["c"],` +
				`"out_of_scope":["d"],"constraints":["e"],"risks":["f"],"acceptance_criteria":["g"]}`,
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "缺少风险列表",
			text: `{"summary":"s","goals":["a"],"current_state":["b"],"scope":["c"],` +
				`"out_of_scope":["d"],"constraints":["e"],"acceptance_criteria":["g"]}`,
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "列表项为空",
			text: strings.Replace(
				validRequirementAnalysisJSON(),
				`"goals":["产出结构化需求分析"]`,
				`"goals":["  "]`,
				1,
			),
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "摘要超长",
			text: strings.Replace(
				validRequirementAnalysisJSON(),
				`"summary":"补齐规划会话的结构化分析"`,
				`"summary":"`+strings.Repeat("长", domain.MaxRequirementAnalysisSummaryLength)+`"`,
				1,
			),
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "列表条数超限",
			text: strings.Replace(
				validRequirementAnalysisJSON(),
				`"goals":["产出结构化需求分析"]`,
				`"goals":[`+strings.Repeat(`"目标",`, domain.MaxRequirementAnalysisItemCount)+`"最后一个"]`,
				1,
			),
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "输出超过字节上限",
			text: `{"summary":"` + strings.Repeat("x", domain.MaxPlanningOutputBytes) + `"}`,
			want: domain.ErrPlanningOutputUnparseable,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := domain.ParseRequirementAnalysis(testCase.text)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("错误类别不符: 期望 %v，实际 %v", testCase.want, err)
			}
		})
	}
}

// TestParseImplementationPlanAcceptsStructuredOutput 验证合法实施方案可以被解析。
func TestParseImplementationPlanAcceptsStructuredOutput(t *testing.T) {
	t.Parallel()
	plan, err := domain.ParseImplementationPlan(validImplementationPlanJSON())
	if err != nil {
		t.Fatalf("解析实施方案失败: %v", err)
	}
	if len(plan.Tasks) != 2 {
		t.Fatalf("任务数量解析错误: %d", len(plan.Tasks))
	}
	if len(plan.Tasks[1].DependsOn) != 1 || plan.Tasks[1].DependsOn[0] != "task-1" {
		t.Fatalf("依赖解析错误: %+v", plan.Tasks[1].DependsOn)
	}
}

// TestParseImplementationPlanRejectsUnusableOutput 验证不可用的实施方案输出一律被拒绝。
func TestParseImplementationPlanRejectsUnusableOutput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want error
	}{
		{name: "空输出", text: "", want: domain.ErrPlanningOutputUnparseable},
		{
			name: "未知字段",
			text: strings.Replace(
				validImplementationPlanJSON(),
				`"summary":`,
				`"version":2,"summary":`,
				1,
			),
			want: domain.ErrPlanningOutputUnparseable,
		},
		{
			name: "尾随第二份 JSON 文档",
			text: validImplementationPlanJSON() + validImplementationPlanJSON(),
			want: domain.ErrPlanningOutputUnparseable,
		},
		{
			name: "缺少任务列表",
			text: `{"summary":"没有任务"}`,
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "任务列表为空",
			text: `{"summary":"没有任务","tasks":[]}`,
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "缺少任务描述",
			text: `{"summary":"s","tasks":[{"id":"task-1","title":"标题",` +
				`"depends_on":[],"acceptance_criteria":["c"]}]}`,
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "任务缺少验收条件",
			text: `{"summary":"s","tasks":[{"id":"task-1","title":"标题",` +
				`"description":"描述","depends_on":[]}]}`,
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "任务标识重复",
			text: `{"summary":"s","tasks":[` +
				`{"id":"task-1","title":"A","description":"a","depends_on":[],` +
				`"acceptance_criteria":["c"]},` +
				`{"id":"task-1","title":"B","description":"b","depends_on":[],` +
				`"acceptance_criteria":["c"]}]}`,
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "依赖不存在的任务",
			text: `{"summary":"s","tasks":[` +
				`{"id":"task-1","title":"A","description":"a","depends_on":["task-9"],` +
				`"acceptance_criteria":["c"]}]}`,
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "依赖自己",
			text: `{"summary":"s","tasks":[` +
				`{"id":"task-1","title":"A","description":"a","depends_on":["task-1"],` +
				`"acceptance_criteria":["c"]}]}`,
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "依赖顺序颠倒",
			text: `{"summary":"s","tasks":[` +
				`{"id":"task-1","title":"A","description":"a","depends_on":["task-2"],` +
				`"acceptance_criteria":["c"]},` +
				`{"id":"task-2","title":"B","description":"b","depends_on":[],` +
				`"acceptance_criteria":["c"]}]}`,
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "依赖重复声明",
			text: `{"summary":"s","tasks":[` +
				`{"id":"task-1","title":"A","description":"a","depends_on":[],` +
				`"acceptance_criteria":["c"]},` +
				`{"id":"task-2","title":"B","description":"b",` +
				`"depends_on":["task-1","task-1"],"acceptance_criteria":["c"]}]}`,
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "依赖为空串",
			text: `{"summary":"s","tasks":[` +
				`{"id":"task-1","title":"A","description":"a","depends_on":[" "],` +
				`"acceptance_criteria":["c"]}]}`,
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "任务标识超长",
			text: `{"summary":"s","tasks":[{"id":"` +
				strings.Repeat("t", domain.MaxImplementationPlanTaskIDLength+1) +
				`","title":"A","description":"a","depends_on":[],"acceptance_criteria":["c"]}]}`,
			want: domain.ErrPlanningOutputInvalid,
		},
		{
			name: "任务数量超限",
			text: planWithManyTasks(domain.MaxImplementationPlanTaskCount + 1),
			want: domain.ErrPlanningOutputInvalid,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := domain.ParseImplementationPlan(testCase.text)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("错误类别不符: 期望 %v，实际 %v", testCase.want, err)
			}
		})
	}
}

// planWithManyTasks 构造包含 count 个无依赖任务的实施方案。
func planWithManyTasks(count int) string {
	var builder strings.Builder
	builder.WriteString(`{"summary":"s","tasks":[`)
	for index := 0; index < count; index++ {
		if index > 0 {
			builder.WriteString(",")
		}
		builder.WriteString(`{"id":"task-` + string(rune('a'+index%26)) + `-` +
			strings.Repeat("x", index) + `","title":"A","description":"a",` +
			`"depends_on":[],"acceptance_criteria":["c"]}`)
	}
	builder.WriteString(`]}`)
	return builder.String()
}

// TestPlanningSnapshotsRoundTrip 验证快照可以脱离产生它的运行时被独立解析。
func TestPlanningSnapshotsRoundTrip(t *testing.T) {
	t.Parallel()
	analysis, err := domain.ParseRequirementAnalysis(validRequirementAnalysisJSON())
	if err != nil {
		t.Fatalf("解析需求分析失败: %v", err)
	}
	analysisSnapshot, err := analysis.Snapshot()
	if err != nil {
		t.Fatalf("生成需求分析快照失败: %v", err)
	}
	restoredAnalysis, err := domain.ParseRequirementAnalysisSnapshot(analysisSnapshot)
	if err != nil {
		t.Fatalf("解析需求分析快照失败: %v", err)
	}
	if restoredAnalysis.Summary != analysis.Summary ||
		len(restoredAnalysis.Risks) != len(analysis.Risks) {
		t.Fatalf("需求分析快照往返不一致: %+v", restoredAnalysis)
	}
	plan, err := domain.ParseImplementationPlan(validImplementationPlanJSON())
	if err != nil {
		t.Fatalf("解析实施方案失败: %v", err)
	}
	planSnapshot, err := plan.Snapshot()
	if err != nil {
		t.Fatalf("生成实施方案快照失败: %v", err)
	}
	restoredPlan, err := domain.ParseImplementationPlanSnapshot(planSnapshot)
	if err != nil {
		t.Fatalf("解析实施方案快照失败: %v", err)
	}
	if len(restoredPlan.Tasks) != len(plan.Tasks) {
		t.Fatalf("实施方案快照往返不一致: %+v", restoredPlan)
	}
}

// TestPlanningRevisionCarriesSnapshotVersion 验证版本快照带有明确的结构版本。
func TestPlanningRevisionCarriesSnapshotVersion(t *testing.T) {
	t.Parallel()
	analysis, err := domain.ParseRequirementAnalysis(validRequirementAnalysisJSON())
	if err != nil {
		t.Fatalf("解析需求分析失败: %v", err)
	}
	plan, err := domain.ParseImplementationPlan(validImplementationPlanJSON())
	if err != nil {
		t.Fatalf("解析实施方案失败: %v", err)
	}
	analysisSnapshot, err := analysis.Snapshot()
	if err != nil {
		t.Fatalf("生成需求分析快照失败: %v", err)
	}
	planSnapshot, err := plan.Snapshot()
	if err != nil {
		t.Fatalf("生成实施方案快照失败: %v", err)
	}
	validationSnapshot, err := domain.PlanningValidationResult{
		SnapshotVersion:          domain.PlanningRevisionSnapshotVersion,
		RequirementAnalysisValid: true,
		ImplementationPlanValid:  true,
		TaskCount:                len(plan.Tasks),
	}.Snapshot()
	if err != nil {
		t.Fatalf("生成校验结论快照失败: %v", err)
	}
	sessionID := domain.NewID()
	revision, err := domain.NewPlanningRevision(
		sessionID,
		1,
		analysisSnapshot,
		planSnapshot,
		validationSnapshot,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造规划版本失败: %v", err)
	}
	if revision.SnapshotVersion != domain.PlanningRevisionSnapshotVersion {
		t.Fatalf("快照版本未写入: %d", revision.SnapshotVersion)
	}
	readBack, err := revision.RequirementAnalysis()
	if err != nil {
		t.Fatalf("读回需求分析失败: %v", err)
	}
	if readBack.Summary != analysis.Summary {
		t.Fatalf("读回的需求分析摘要不一致: %q", readBack.Summary)
	}
	readPlan, err := revision.ImplementationPlan()
	if err != nil {
		t.Fatalf("读回实施方案失败: %v", err)
	}
	if len(readPlan.Tasks) != len(plan.Tasks) {
		t.Fatalf("读回的实施方案任务数量不一致: %d", len(readPlan.Tasks))
	}
}

// TestPlanningRevisionRejectsUnknownSnapshotVersion 验证未知结构版本不会被强行解析。
func TestPlanningRevisionRejectsUnknownSnapshotVersion(t *testing.T) {
	t.Parallel()
	revision := domain.PlanningRevision{
		RequirementAnalysisJSON: json.RawMessage(validRequirementAnalysisJSON()),
		ImplementationPlanJSON:  json.RawMessage(validImplementationPlanJSON()),
		SnapshotVersion:         domain.PlanningRevisionSnapshotVersion + 1,
	}
	if _, err := revision.RequirementAnalysis(); !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatalf("未知快照版本未被拒绝: %v", err)
	}
	if _, err := revision.ImplementationPlan(); !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatalf("未知快照版本未被拒绝: %v", err)
	}
}

// TestValidateRejectsMissingSnapshotVersion 验证快照版本是落库的必要字段。
func TestValidateRejectsMissingSnapshotVersion(t *testing.T) {
	t.Parallel()
	revision := domain.PlanningRevision{
		ID:                      domain.NewID(),
		PlanningSessionID:       domain.NewID(),
		RevisionNumber:          1,
		RequirementAnalysisJSON: json.RawMessage(validRequirementAnalysisJSON()),
		ImplementationPlanJSON:  json.RawMessage(validImplementationPlanJSON()),
		CreatedAt:               time.Now().UTC(),
	}
	if err := revision.Validate(); !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatalf("缺少快照版本未被拒绝: %v", err)
	}
}
