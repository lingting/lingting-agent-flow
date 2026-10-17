package app

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// SessionService 负责把 Agent 执行的 Session 元数据写入数据库。
//
// Runner 只报告执行结果，不承担数据库业务：元数据写入集中在这里，
// 保证即使 Session 导出失败，失败状态与已保存的原始文件路径也不会丢失。
type SessionService struct {
	store storage.Store
	// localRoot 是本机 Session 持久化根目录。
	localRoot string
	logger    *slog.Logger
}

// NewSessionService 构造会话服务。
func NewSessionService(store storage.Store, localRoot string, logger *slog.Logger) (*SessionService, error) {
	if store == nil {
		return nil, fmt.Errorf("会话服务初始化失败: 存储不能为空")
	}
	root := strings.TrimSpace(localRoot)
	if root == "" {
		return nil, fmt.Errorf("会话服务初始化失败: 本机 Session 根目录不能为空")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("会话服务初始化失败: 解析 Session 根目录失败: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionService{store: store, localRoot: absolute, logger: logger}, nil
}

// LocalRoot 返回本机 Session 持久化根目录。
func (s *SessionService) LocalRoot() string {
	return s.localRoot
}

// RecordRequest 是一次会话元数据写入所需的输入。
type RecordRequest struct {
	// Scope 是会话的任务、步骤与尝试归属。
	Scope domain.SessionScope
	// Role 是执行本次调用的角色。
	Role domain.Role
	// SessionID 是内部会话标识，为空时自动生成。
	SessionID string
	// Result 是 Runner 返回的执行结果。
	Result agent.RunResult
}

// Record 把一次执行的会话元数据写入数据库。
//
// 无论执行成功、失败、取消还是超时，都会写入一条终态记录：
// Session 文件路径与完整性状态是排查问题的唯一入口，不能因为执行失败而丢失。
func (s *SessionService) Record(ctx context.Context, request RecordRequest) (domain.Session, error) {
	session, err := domain.NewSession(
		request.Scope,
		request.Role,
		providerOf(request.Role.Model),
		request.Role.Model,
		request.Result.StartedAt,
	)
	if err != nil {
		return domain.Session{}, fmt.Errorf("记录会话失败: %w", err)
	}
	if id := strings.TrimSpace(request.SessionID); id != "" {
		session.ID = domain.ID(id)
	}
	if processID := request.Result.ProcessID; processID > 0 {
		if err := session.SetProcessID(processID); err != nil {
			return domain.Session{}, fmt.Errorf("记录会话失败: %w", err)
		}
	}
	if err := session.MarkRunning(request.Result.ExternalSessionID); err != nil {
		return domain.Session{}, fmt.Errorf("记录会话失败: %w", err)
	}
	if err := session.ApplyArtifacts(artifactsOf(request.Result), request.Result.FinishedAt); err != nil {
		return domain.Session{}, fmt.Errorf("记录会话失败: %w", err)
	}
	if err := s.store.CreateSession(ctx, session); err != nil {
		return domain.Session{}, fmt.Errorf("记录会话失败: %w", err)
	}
	s.logger.DebugContext(
		ctx,
		"会话元数据已记录",
		slog.String("session_id", session.ID.String()),
		slog.String("run_status", string(session.RunStatus)),
		slog.String("completeness", string(session.Completeness)),
		slog.String("export_status", string(session.ExportStatus)),
	)
	return session, nil
}

// artifactsOf 把 Runner 的执行结果转换为领域层的会话产出元数据。
func artifactsOf(result agent.RunResult) domain.SessionArtifacts {
	return domain.SessionArtifacts{
		JSONLPath:      result.Session.JSONLPath,
		HTMLPath:       result.Session.HTMLPath,
		RunStatus:      runStatusOf(result.Status),
		Completeness:   completenessOf(result.Session.Completeness),
		ExportStatus:   exportStatusOf(result.Session.ExportStatus),
		ExitCode:       result.ExitCode,
		FailureSummary: result.Failure,
	}
}

// runStatusOf 把执行状态映射为领域层的运行结果。
func runStatusOf(status agent.RunStatus) domain.SessionRunStatus {
	switch status {
	case agent.RunCompleted:
		return domain.SessionRunCompleted
	case agent.RunCancelled:
		return domain.SessionRunCancelled
	case agent.RunTimedOut:
		return domain.SessionRunTimedOut
	default:
		return domain.SessionRunFailed
	}
}

// completenessOf 把 Session 完整性映射为领域层取值。
func completenessOf(value agent.SessionCompleteness) domain.SessionCompleteness {
	switch value {
	case agent.SessionComplete:
		return domain.SessionCompletenessComplete
	case agent.SessionPartial:
		return domain.SessionCompletenessPartial
	case agent.SessionExportFailed:
		return domain.SessionCompletenessExportFailed
	default:
		return domain.SessionCompletenessMissing
	}
}

// exportStatusOf 把导出状态映射为领域层取值。
func exportStatusOf(value agent.ExportStatus) domain.SessionExportStatus {
	switch value {
	case agent.ExportSucceeded:
		return domain.SessionExportSucceeded
	case agent.ExportFailed:
		return domain.SessionExportFailed
	default:
		return domain.SessionExportNotAttempted
	}
}

// providerOf 返回模型所属的 Provider。
//
// Pi 的模型标识形如 provider/model-id；无法拆分时把整个标识当作 Provider，
// 避免 Provider 为空导致会话记录无法写入。
func providerOf(model string) string {
	identifier := strings.TrimSpace(model)
	if index := strings.Index(identifier, "/"); index > 0 {
		return identifier[:index]
	}
	return identifier
}
