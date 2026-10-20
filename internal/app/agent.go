package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// agentEventLogLevel 返回内部事件对应的日志级别。
func agentEventLogLevel(eventType agent.EventType) slog.Level {
	switch eventType {
	case agent.EventRunFailed:
		return slog.LevelError
	case agent.EventRunCancelled, agent.EventRunTimedOut, agent.EventRunSettled:
		return slog.LevelWarn
	default:
		return slog.LevelDebug
	}
}

// SessionRoot 返回本机 Session 持久化根目录。
//
// 目录与数据库放在同一数据目录下，避免 Session 文件散落在系统临时目录。
func SessionRoot(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	database := strings.TrimSpace(cfg.Storage.DatabasePath)
	if database == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(database), "sessions")
}

// AgentCheck 检查 Pi 可执行程序与非交互式执行所需的参数支持。
//
// model 为空时只检查安装与参数支持，不查询模型。
func (a *App) AgentCheck(ctx context.Context, model string) (pi.CheckReport, error) {
	report, err := pi.Check(ctx, pi.Options{}, model)
	if err != nil {
		return report, fmt.Errorf("Agent 检查失败: %w", err)
	}
	a.logger.Slog().DebugContext(
		ctx,
		"Agent 检查通过",
		slog.String("executable", report.Executable),
		slog.String("version", report.Version),
		slog.String("model", report.Model),
	)
	return report, nil
}

// AgentTestRequest 是开发用测试命令的输入。
type AgentTestRequest struct {
	// ConfigPath 是配置文件路径，用于读取角色与数据目录。
	ConfigPath string
	// RoleID 是使用的角色标识，为空时使用配置中第一个角色。
	RoleID string
	// Model 覆盖角色配置中的模型，为空时使用角色配置的模型。
	Model string
	// Dir 是执行时的工作目录，为空时使用临时目录。
	Dir string
	// Prompt 是发送给 Agent 的提示词。
	Prompt string
	// Timeout 是本次调用的超时时间，零值使用执行器默认值。
	Timeout time.Duration
}

// AgentTestResult 是开发用测试命令的执行结果。
type AgentTestResult struct {
	// SessionID 是本次执行创建的内部会话标识。
	SessionID domain.ID
	// Role 是本次执行使用的角色。
	Role domain.Role
	// Result 是执行器返回的最终结果。
	Result agent.RunResult
	// SessionRoot 是本次调用使用的 Session 根目录。
	SessionRoot string
	// EventCounts 统计各类内部事件的数量，便于确认事件链路可用。
	EventCounts map[agent.EventType]int
}

// EventSummary 返回事件数量摘要文本。
func (r AgentTestResult) EventSummary() string {
	return FormatEventCounts(r.EventCounts)
}

// AgentTest 通过统一 Runner 执行一次真实的 Agent 调用。
//
// 该命令只用于验证执行链路：每次调用使用独立 Session，Session 文件写入配置的
// 数据目录，但不写入数据库，也不代表工作流已实现。事件逐条写入日志，
// 不会污染命令的标准输出。
func (a *App) AgentTest(ctx context.Context, request AgentTestRequest) (AgentTestResult, error) {
	cfg, err := a.ValidateConfig(ctx, request.ConfigPath)
	if err != nil {
		return AgentTestResult{}, fmt.Errorf("Agent 测试失败: %w", err)
	}
	role, err := selectRole(cfg, request.RoleID)
	if err != nil {
		return AgentTestResult{}, err
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = role.Model
	}
	if model == "" {
		return AgentTestResult{}, fmt.Errorf("Agent 测试失败: 角色 %s 未配置模型", role.ID)
	}
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		return AgentTestResult{}, fmt.Errorf("Agent 测试失败: 提示词不能为空")
	}
	root := SessionRoot(cfg)
	if root == "" {
		return AgentTestResult{}, fmt.Errorf("Agent 测试失败: 无法确定 Session 根目录")
	}
	sessionID := domain.NewID()
	counts := make(map[agent.EventType]int)
	emit := func(event agent.Event) {
		counts[event.Type]++
		a.logEvent(ctx, event)
	}
	// 始终通过统一 Runner 接口执行：命令层不直接拼接 Pi 进程，也不再各自实现
	// 一套"启动子进程 + 收尾"的逻辑。
	runner := pi.NewRunner(pi.Options{})
	result, runErr := runner.Run(ctx, agent.RunRequest{
		SessionID:    sessionID.String(),
		RoleType:     role.Type.String(),
		Model:        model,
		SystemPrompt: role.SystemPrompt,
		Prompt:       prompt,
		WorkingDir:   strings.TrimSpace(request.Dir),
		SessionRoot:  root,
		Timeout:      request.Timeout,
	}, emit)
	response := AgentTestResult{
		SessionID:   sessionID,
		Role:        role,
		Result:      result,
		SessionRoot: root,
		EventCounts: counts,
	}
	if runErr != nil {
		return response, runErr
	}
	return response, nil
}

// selectRole 按标识选择角色，未指定标识时返回第一个角色。
func selectRole(cfg *config.Config, roleID string) (domain.Role, error) {
	if cfg == nil {
		return domain.Role{}, fmt.Errorf("Agent 测试失败: 配置为空")
	}
	seeds, err := RoleSeeds(cfg)
	if err != nil {
		return domain.Role{}, fmt.Errorf("Agent 测试失败: %w", err)
	}
	wanted := strings.TrimSpace(roleID)
	for _, seed := range seeds {
		if wanted != "" && seed.ID.String() != wanted {
			continue
		}
		return roleFromSeed(seed), nil
	}
	if wanted == "" {
		return domain.Role{}, fmt.Errorf("Agent 测试失败: 配置中没有可用角色")
	}
	return domain.Role{}, fmt.Errorf("Agent 测试失败: 配置中不存在角色 %s", wanted)
}

// roleFromSeed 把角色初始化定义还原为领域角色。
//
// 测试命令不写入数据库，因此直接使用配置构造内存角色；时间戳只用于满足校验。
func roleFromSeed(seed storage.RoleSeed) domain.Role {
	now := time.Now().UTC()
	return domain.Role{
		ID:           seed.ID,
		Name:         seed.Name,
		Type:         seed.Type,
		Runner:       seed.Runner,
		Model:        seed.Model,
		SystemPrompt: seed.SystemPrompt,
		ToolsPolicy:  seed.ToolsPolicy,
		Enabled:      true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

// logEvent 把内部事件写入日志。
//
// 流式输出使用独立字段记录，避免与结构化诊断信息混淆。
func (a *App) logEvent(ctx context.Context, event agent.Event) {
	a.logger.Slog().Log(
		ctx,
		agentEventLogLevel(event.Type),
		"Agent 事件",
		slog.String("session_id", event.SessionID),
		slog.String("event_type", string(event.Type)),
		slog.String("data", summarizeEventData(event.Data)),
	)
}

// summarizeEventData 返回事件的紧凑表示，用于日志输出。
func summarizeEventData(data json.RawMessage) string {
	return agent.TruncateText(strings.TrimSpace(string(data)), 256)
}

// FormatEventCounts 生成事件数量摘要，按固定顺序输出已出现的事件类型。
func FormatEventCounts(counts map[agent.EventType]int) string {
	order := []agent.EventType{
		agent.EventRunStarted,
		agent.EventRunOutput,
		agent.EventSessionSaved,
		agent.EventSessionExported,
		agent.EventRunSettled,
		agent.EventRunFailed,
		agent.EventRunCancelled,
		agent.EventRunTimedOut,
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
