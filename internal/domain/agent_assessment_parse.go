package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 辅助评估解析的边界常量。
const (
	// MaxAgentAssessmentBytes 是 Validator 输出保留解析的最大字节数。
	//
	// 评估只是结构化结论，不承载完整会话内容：超出该上限的输出按"不可解释"处理，
	// 而不是把大段文本塞进数据库。
	MaxAgentAssessmentBytes = 16 << 10
)

// 辅助评估不可用的两种原因。
//
// 两者都必须让验证无法通过，但诊断信息不同：前者说明输出根本不是可解析的结构，
// 后者说明结构合法却没有通过领域校验（例如问题条数超限、类型不符）。分开表达是为了
// 让调用方能够区分"模型没按协议输出"与"输出违反领域约束"。
var (
	// ErrAssessmentUnparseable 表示输出无法解析为结构化评估。
	ErrAssessmentUnparseable = fmt.Errorf("%w: 辅助评估输出无法解析", ErrInvalidValue)
	// ErrAssessmentInvalid 表示结构合法但没有通过领域校验。
	ErrAssessmentInvalid = fmt.Errorf("%w: 辅助评估输出不合法", ErrInvalidValue)
)

// ParseAgentAssessment 把 Validator Agent 的最终文本解析为结构化辅助评估。
//
// 解析规则：
//   - 只接受一个完整的 JSON 对象，允许前后存在说明文字与代码块围栏；
//   - 未知字段一律拒绝，避免把额外结构当成评估结论；
//   - passed 必须显式出现：缺少该字段意味着模型没有给出可解释结论；
//   - 解析后的结构还必须通过 AgentAssessment.Validate 的领域校验。
//
// 该函数是纯函数：不读数据库、不启动进程，因此可以被完整地单元测试。
func ParseAgentAssessment(text string) (AgentAssessment, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return AgentAssessment{}, fmt.Errorf("%w: Validator 没有输出任何内容", ErrAssessmentUnparseable)
	}
	if len(trimmed) > MaxAgentAssessmentBytes {
		return AgentAssessment{}, fmt.Errorf(
			"%w: 输出长度 %d 超过上限 %d",
			ErrAssessmentUnparseable,
			len(trimmed),
			MaxAgentAssessmentBytes,
		)
	}
	candidate, err := ExtractJSONObject(trimmed)
	if err != nil {
		return AgentAssessment{}, fmt.Errorf("%w: %w", ErrAssessmentUnparseable, err)
	}
	var presence map[string]json.RawMessage
	if err := json.Unmarshal([]byte(candidate), &presence); err != nil {
		return AgentAssessment{}, fmt.Errorf(
			"%w: 输出不是合法的 JSON 对象: %w",
			ErrAssessmentUnparseable,
			err,
		)
	}
	if _, ok := presence["passed"]; !ok {
		return AgentAssessment{}, fmt.Errorf(
			"%w: 输出缺少 passed 字段，无法判断评估结论",
			ErrAssessmentUnparseable,
		)
	}
	var assessment AgentAssessment
	decoder := json.NewDecoder(strings.NewReader(candidate))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&assessment); err != nil {
		return AgentAssessment{}, fmt.Errorf(
			"%w: 输出字段不符合评估结构: %w",
			ErrAssessmentUnparseable,
			err,
		)
	}
	if err := assessment.Validate(); err != nil {
		return AgentAssessment{}, fmt.Errorf("%w: %w", ErrAssessmentInvalid, err)
	}
	return assessment, nil
}

// ExtractJSONObject 从文本中提取第一个完整的 JSON 对象。
//
// 只做括号配平扫描，不做语义解释：这样既能容忍代码块围栏与前后说明文字，
// 也不会把任意自由文本当作 JSON 接受。返回的错误只描述"找不到对象"或"对象未闭合"，
// 由调用方按自身语义包装，因此计划生成与辅助评估共用同一份扫描实现。
func ExtractJSONObject(text string) (string, error) {
	start := strings.Index(text, "{")
	if start < 0 {
		return "", fmt.Errorf("输出中找不到 JSON 对象")
	}
	depth := 0
	inString := false
	escaped := false
	for index := start; index < len(text); index++ {
		char := text[index]
		if inString {
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : index+1], nil
			}
		}
	}
	return "", fmt.Errorf("JSON 对象未闭合")
}
