package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/internal/domain"
)

// agentEventLogLevel 返回内部事件对应的日志级别。
func agentEventLogLevel(eventType agent.EventType) slog.Level {
	switch eventType {
	case agent.EventRunFailed:
		return slog.LevelError
	case agent.EventRunCancelled, agent.EventRunSettled:
		return slog.LevelWarn
	default:
		return slog.LevelDebug
	}
}

// AgentCheck 检查 Pi 可执行程序、RPC 模式支持与协议握手。
func (a *App) AgentCheck(ctx context.Context) (pi.CheckReport, error) {
	report, err := pi.Check(ctx, pi.Options{})
	if err != nil {
		return report, fmt.Errorf("Agent 检查失败: %w", err)
	}
	a.logger.Slog().DebugContext(
		ctx,
		"Agent 检查通过",
		slog.String("executable", report.Executable),
		slog.String("version", report.Version),
	)
	return report, nil
}

// AgentTestRequest 是开发用测试命令的输入。
type AgentTestRequest struct {
	// Model 是本次执行使用的模型标识。
	Model string
	// Dir 是执行时的工作目录。
	Dir string
	// Prompt 是发送给 Agent 的提示词。
	Prompt string
}

// AgentTestResult 是开发用测试命令的执行结果。
type AgentTestResult struct {
	// SessionID 是本次执行创建的内部会话标识。
	SessionID string
	// Result 是执行器返回的最终结果。
	Result agent.RunResult
	// EventCounts 统计各类内部事件的数量，便于确认事件链路可用。
	EventCounts map[agent.EventType]int
}

// EventSummary 返回事件数量摘要文本。
func (r AgentTestResult) EventSummary() string {
	return FormatEventCounts(r.EventCounts)
}

// AgentTest 通过统一 Runner 执行一次 Agent 调用。
//
// 该命令只用于验证执行链路，不写入数据库，也不代表工作流已实现。
// 事件逐条输出到标准错误流，避免污染命令的标准输出。
func (a *App) AgentTest(ctx context.Context, request AgentTestRequest) (AgentTestResult, error) {
	model := strings.TrimSpace(request.Model)
	if model == "" {
		return AgentTestResult{}, fmt.Errorf("Agent 测试失败: 模型不能为空")
	}
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		return AgentTestResult{}, fmt.Errorf("Agent 测试失败: 提示词不能为空")
	}
	sessionID := domain.NewID()
	runRequest := agent.RunRequest{
		SessionID:  sessionID.String(),
		Model:      model,
		Prompt:     prompt,
		WorkingDir: strings.TrimSpace(request.Dir),
	}
	counts := make(map[agent.EventType]int)
	emit := func(event agent.Event) {
		counts[event.Type]++
		a.logger.Slog().Log(
			ctx,
			agentEventLogLevel(event.Type),
			"Agent 事件",
			slog.String("session_id", event.SessionID),
			slog.String("event_type", string(event.Type)),
			slog.String("data", summarizeEventData(event.Data)),
		)
	}
	runner := pi.NewRunner(pi.Options{})
	result, err := runner.Run(ctx, runRequest, emit)
	if err != nil {
		return AgentTestResult{SessionID: sessionID.String(), Result: result, EventCounts: counts}, err
	}
	return AgentTestResult{SessionID: sessionID.String(), Result: result, EventCounts: counts}, nil
}

// summarizeEventData 返回事件的紧凑表示，用于日志输出。
func summarizeEventData(data json.RawMessage) string {
	return agent.TruncateText(strings.TrimSpace(string(data)), 256)
}

// FormatEventCounts 生成事件数量摘要，按固定顺序输出已出现的事件类型。
func FormatEventCounts(counts map[agent.EventType]int) string {
	order := []agent.EventType{
		agent.EventRunStarted,
		agent.EventMessageDelta,
		agent.EventMessageCompleted,
		agent.EventToolStarted,
		agent.EventToolUpdated,
		agent.EventToolCompleted,
		agent.EventRunSettled,
		agent.EventRunFailed,
		agent.EventRunCancelled,
	}
	var parts []string
	for _, eventType := range order {
		if count := counts[eventType]; count > 0 {
			parts = append(parts, fmt.Sprintf("%s×%d", eventType, count))
		}
	}
	if len(parts) == 0 {
		return "无"
	}
	return strings.Join(parts, "，")
}
