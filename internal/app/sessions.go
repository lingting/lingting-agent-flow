package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 会话服务错误类别。
var (
	// ErrSessionScopeInvalid 表示会话的任务、步骤与尝试归属不自洽。
	ErrSessionScopeInvalid = errors.New("app: 会话归属不合法")
	// ErrSessionAlreadyStarted 表示同一个会话被重复启动。
	ErrSessionAlreadyStarted = errors.New("app: 会话已经启动")
)

// SessionService 负责把 Agent 执行的 Session 元数据写入数据库。
//
// Runner 只报告执行结果，不承担数据库业务：元数据写入集中在这里，保证即使
// Session 导出失败，失败状态与已保存的原始文件路径也不会丢失。
//
// 生命周期分两步，顺序固定：
//  1. Start 在启动进程之前创建记录并把状态推进到 running；
//  2. Finish 在进程结束后写入产出元数据与归档结果。
//
// 两步分离的意义是：启动本身失败时记录已经存在于数据库中（可以推进到 failed），
// 不会出现"进程启动失败但数据库里没有任何痕迹"或永久 running 的残留状态。
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

// StartRequest 是一次会话启动所需的输入。
type StartRequest struct {
	// Scope 是会话的任务、步骤与尝试归属。
	Scope domain.SessionScope
	// Role 是执行本次调用的角色。
	Role domain.Role
	// SessionID 是内部会话标识；为空时自动生成。
	SessionID string
	// InvocationID 是本次调用的唯一标识，用于区分同一会话下的多次调用。
	InvocationID string
	// ContainerID 与 DockerID 是容器内执行时的归属，本机执行时为空。
	ContainerID string
	DockerID    string
	// ExternalSessionID 是 Agent 运行时侧的会话标识，通常在执行前为空。
	ExternalSessionID string
}

// Start 创建会话记录并推进到 running。
//
// 记录先写入数据库再置为 running：如果写入失败，调用方不得启动进程，否则会出现
// 一次没有任何记录的真实执行。返回的会话已经持久化，可以安全用于后续 Finish。
func (s *SessionService) Start(ctx context.Context, request StartRequest) (domain.Session, error) {
	if err := request.Scope.Validate(); err != nil {
		return domain.Session{}, fmt.Errorf("%w: %w", ErrSessionScopeInvalid, err)
	}
	session, err := domain.NewSession(
		request.Scope,
		request.Role,
		ProviderOf(request.Role.Model),
		request.Role.Model,
		time.Now(),
	)
	if err != nil {
		return domain.Session{}, fmt.Errorf("创建会话失败: %w", err)
	}
	if id := strings.TrimSpace(request.SessionID); id != "" {
		session.ID = domain.ID(id)
	}
	session.InvocationID = strings.TrimSpace(request.InvocationID)
	session.ContainerID = strings.TrimSpace(request.ContainerID)
	session.DockerID = strings.TrimSpace(request.DockerID)
	// 先把起始状态落库：此时状态仍是 starting，代表"已登记但尚未确认运行"。
	if err := s.store.CreateSession(ctx, session); err != nil {
		return domain.Session{}, fmt.Errorf("创建会话记录失败: %w", err)
	}
	if err := session.MarkRunning(request.ExternalSessionID); err != nil {
		return domain.Session{}, fmt.Errorf("更新会话状态失败: %w", err)
	}
	if err := s.store.UpdateSession(ctx, session); err != nil {
		return domain.Session{}, fmt.Errorf("更新会话状态失败: %w", err)
	}
	s.logger.DebugContext(
		ctx,
		"会话已启动",
		slog.String("session_id", session.ID.String()),
		slog.String("task_id", session.TaskID.String()),
		slog.String("status", string(session.Status)),
	)
	return session, nil
}

// FinishRequest 是一次会话收尾所需的输入。
type FinishRequest struct {
	// SessionID 是 Start 返回的会话标识。
	SessionID domain.ID
	// Result 是 Runner 返回的执行结果。
	Result agent.RunResult
	// Archive 是归档结论；零值表示本次没有归档结论，不写归档字段。
	Archive ArchiveOutcome
}

// ArchiveOutcome 原始 Session 文件的归档结论。
//
// 归档与执行结果、HTML 导出状态相互独立：归档失败不会把成功的调用改写为失败，
// 归档成功也不会把失败的调用改写为成功。
type ArchiveOutcome struct {
	// Status 是归档状态，空值表示未产生归档结论。
	Status domain.SessionArchiveStatus
	// Path 是归档后的原始 Session 文件位置。
	Path string
	// Error 是归档失败说明。
	Error string
}

// List 按创建时间返回任务下的会话分页。
//
// 该入口用于业务层核对执行记录与诊断，不实现独立的查询语义：分页、筛选与排序
// 全部由存储层提供，避免出现第二套查询规则。
func (s *SessionService) List(
	ctx context.Context,
	taskID domain.ID,
	limit, offset int,
) (storage.SessionPage, error) {
	page, err := s.store.ListSessionPage(ctx, storage.SessionFilter{
		TaskID: taskID,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return storage.SessionPage{}, fmt.Errorf("查询会话列表失败: %w", err)
	}
	return page, nil
}

// Load 读取单个会话记录。
//
// 供调用方在执行结束后核对真实状态：数据库是唯一真相来源，不能用内存对象代替。
func (s *SessionService) Load(ctx context.Context, sessionID domain.ID) (domain.Session, error) {
	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		return domain.Session{}, fmt.Errorf("读取会话失败: %w", err)
	}
	return *session, nil
}

// Finish 写入会话的产出元数据与归档结果。
//
// 无论执行成功、失败、取消还是超时都会调用：Session 文件路径与完整性状态是排查
// 问题的唯一入口，不能因为执行失败而丢失。归档结果单独写入，且只在产生结论时写入。
func (s *SessionService) Finish(ctx context.Context, request FinishRequest) (domain.Session, error) {
	if err := request.SessionID.Validate(); err != nil {
		return domain.Session{}, err
	}
	session, err := s.store.GetSession(ctx, request.SessionID)
	if err != nil {
		return domain.Session{}, fmt.Errorf("结束会话失败: %w", err)
	}
	if session.Status.IsTerminal() {
		// 终态不重复改写：迟到的取消或结束信号不能覆盖已经确认的结果。
		return domain.Session{}, fmt.Errorf(
			"%w: 会话 %s 已结束（%s）",
			domain.ErrInvalidState,
			session.ID,
			session.Status,
		)
	}
	if processID := request.Result.ProcessID; processID > 0 {
		if err := session.SetProcessID(processID); err != nil {
			return domain.Session{}, fmt.Errorf("记录会话进程失败: %w", err)
		}
	}
	if err := session.ApplyArtifacts(artifactsOf(request.Result), request.Result.FinishedAt); err != nil {
		return domain.Session{}, fmt.Errorf("记录会话产出失败: %w", err)
	}
	if err := s.applyArchive(session, request.Archive); err != nil {
		return domain.Session{}, err
	}
	if err := s.store.UpdateSession(ctx, *session); err != nil {
		return domain.Session{}, fmt.Errorf("结束会话失败: %w", err)
	}
	s.logger.DebugContext(
		ctx,
		"会话已结束",
		slog.String("session_id", session.ID.String()),
		slog.String("status", string(session.Status)),
		slog.String("run_status", string(session.RunStatus)),
		slog.String("completeness", string(session.Completeness)),
		slog.String("export_status", string(session.ExportStatus)),
		slog.String("archive_status", string(session.ArchiveStatus)),
	)
	return *session, nil
}

// Fail 把已创建但尚未结束的会话标记为失败。
//
// 用于"进程启动失败""工作目录不可用"这类在 Runner 返回之前就终止的路径：
// 这些情况没有 RunResult，但仍然必须让记录收敛，不能残留 starting 或 running。
func (s *SessionService) Fail(
	ctx context.Context,
	sessionID domain.ID,
	at time.Time,
	summary string,
) (domain.Session, error) {
	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		return domain.Session{}, fmt.Errorf("标记会话失败状态失败: %w", err)
	}
	if session.Status.IsTerminal() {
		return domain.Session{}, fmt.Errorf(
			"%w: 会话 %s 已结束（%s）",
			domain.ErrInvalidState,
			session.ID,
			session.Status,
		)
	}
	if err := session.Fail(at); err != nil {
		return domain.Session{}, fmt.Errorf("标记会话失败状态失败: %w", err)
	}
	// 失败路径没有真实产出：完整性、导出与归档状态都必须给出明确取值，
	// 不能留空，否则会话记录校验会拒绝写入。
	session.RunStatus = domain.SessionRunFailed
	session.FailureSummary = strings.TrimSpace(summary)
	session.Completeness = domain.SessionCompletenessMissing
	session.ExportStatus = domain.SessionExportNotAttempted
	session.ArchiveStatus = domain.SessionArchiveFailed
	session.ArchivePath = ""
	exitCode := -1
	session.ExitCode = &exitCode
	if err := s.store.UpdateSession(ctx, *session); err != nil {
		return domain.Session{}, fmt.Errorf("标记会话失败状态失败: %w", err)
	}
	return *session, nil
}

// applyArchive 把归档结论写入会话。
func (s *SessionService) applyArchive(session *domain.Session, archive ArchiveOutcome) error {
	if archive.Status == "" {
		return nil
	}
	path := strings.TrimSpace(archive.Path)
	if err := session.ApplyArchive(archive.Status, path); err != nil {
		return fmt.Errorf("记录会话归档结果失败: %w", err)
	}
	if archive.Error != "" {
		session.FailureSummary = joinSummary(session.FailureSummary, archive.Error)
	}
	return nil
}

// joinSummary 合并两段摘要，忽略空值。
func joinSummary(existing, addition string) string {
	switch {
	case strings.TrimSpace(existing) == "":
		return strings.TrimSpace(addition)
	case strings.TrimSpace(addition) == "":
		return strings.TrimSpace(existing)
	default:
		return strings.TrimSpace(existing) + "；" + strings.TrimSpace(addition)
	}
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
		StdoutPath:     result.StdoutPath,
		StderrPath:     result.StderrPath,
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

// ProviderOf 返回模型所属的 Provider。
//
// Pi 的模型标识形如 provider/model-id；无法拆分时把整个标识当作 Provider，
// 避免 Provider 为空导致会话记录无法写入。
func ProviderOf(model string) string {
	identifier := strings.TrimSpace(model)
	if index := strings.Index(identifier, "/"); index > 0 {
		return identifier[:index]
	}
	return identifier
}
