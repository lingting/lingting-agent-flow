package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 规划会话业务编排的错误类别。
//
// 与其余应用服务一致：调用方通过 errors.Is 判断类别并映射 HTTP 状态码，不解析
// 错误文本。
var (
	// ErrPlanningSessionInvalidInput 表示请求参数或提交的结构化结果不合法。
	ErrPlanningSessionInvalidInput = errors.New("app: 规划会话请求参数非法")
	// ErrPlanningSessionNotFound 表示规划会话、方案版本或所属项目不存在，或项目已被逻辑删除。
	ErrPlanningSessionNotFound = errors.New("app: 规划会话目标不存在")
	// ErrPlanningSessionRevisionNotFound 表示方案版本不属于目标规划会话。
	ErrPlanningSessionRevisionNotFound = errors.New("app: 规划方案版本不存在")
	// ErrPlanningSessionStateConflict 表示当前状态不允许该操作，或并发操作已推进状态。
	ErrPlanningSessionStateConflict = errors.New("app: 规划会话状态不允许该操作")
)

// PlanningSessionService 是规划会话的业务编排入口。
//
// 职责边界：
//   - 创建与查询规划会话，以及读取它的消息与方案版本；
//   - 在单个事务内追加用户消息并启动一轮分析；
//   - 在单个事务内追加方案版本与角色消息，并把会话收敛为待确认；
//   - 收敛分析失败、确认某一版方案与放弃会话。
//
// 它不创建正式 Requirement / Task / Step / Attempt，不启动 Pi、SSH 或 Docker，也不
// 准备规划容器：运行环境仍由 PlanningRuntimeService 负责。状态迁移一律调用 domain 的
// 语义方法，本层不直接给状态字段赋值，也不重复定义状态白名单。
type PlanningSessionService struct {
	store storage.Store
	now   func() time.Time
}

// PlanningSessionServiceOptions 是构造规划会话服务的输入。
type PlanningSessionServiceOptions struct {
	// Store 是持久化入口；跨实体的原子写入使用它的 WithTx。
	Store storage.Store
	// Now 是时间源，默认返回 UTC 当前时间。
	Now func() time.Time
}

// NewPlanningSessionService 构造规划会话服务。
func NewPlanningSessionService(
	options PlanningSessionServiceOptions,
) (*PlanningSessionService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("规划会话服务初始化失败: 存储不能为空")
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PlanningSessionService{store: options.Store, now: now}, nil
}

// CreatePlanningSessionRequest 是创建规划会话的输入。
//
// 它只接受项目标识：规划会话标识、初始状态、创建时间与运行期字段（执行节点、容器、
// 代码快照、两个角色的 Pi 会话标识）全部由服务端生成，调用方无法指定。
type CreatePlanningSessionRequest struct {
	// ProjectID 是会话所属项目标识。
	ProjectID string
}

// Create 校验项目并创建一条 active 规划会话。
//
// 项目必须真实存在且未被逻辑删除：会话的项目标识决定代码快照来源与缓存共享范围，
// 引用一个不存在或已下线的项目会让后续所有分析落在错误的位置。
func (s *PlanningSessionService) Create(
	ctx context.Context,
	request CreatePlanningSessionRequest,
) (domain.PlanningSession, error) {
	projectID, err := parsePlanningProjectID(request.ProjectID)
	if err != nil {
		return domain.PlanningSession{}, err
	}
	if _, err := s.loadUsableProject(ctx, s.store, projectID); err != nil {
		return domain.PlanningSession{}, err
	}
	session, err := domain.NewPlanningSession(projectID, s.now())
	if err != nil {
		return domain.PlanningSession{}, fmt.Errorf("%w: %w", ErrPlanningSessionInvalidInput, err)
	}
	if err := s.store.CreatePlanningSession(ctx, session); err != nil {
		return domain.PlanningSession{}, fmt.Errorf("创建规划会话失败: %w", err)
	}
	return session, nil
}

// Get 读取单个规划会话的持久化事实。
//
// 读取路径不调用领域校验，也不做状态映射：数据库不限定状态取值集合，未知状态必须
// 原样返回，不能被改写为默认状态或当作损坏数据丢弃。
func (s *PlanningSessionService) Get(
	ctx context.Context,
	sessionIDValue string,
) (domain.PlanningSession, error) {
	sessionID, err := parsePlanningSessionID(sessionIDValue)
	if err != nil {
		return domain.PlanningSession{}, err
	}
	return readPlanningSession(ctx, s.store, sessionID)
}

// ListByProject 按项目列出规划会话，最近的在前。
//
// 项目必须真实存在且未被逻辑删除：项目被删除后不再允许从该项目的入口继续规划，
// 因此这里先把"项目不可用"报成明确错误，而不是返回一个看起来可用的会话列表。
func (s *PlanningSessionService) ListByProject(
	ctx context.Context,
	projectIDValue string,
) ([]domain.PlanningSession, error) {
	projectID, err := parsePlanningProjectID(projectIDValue)
	if err != nil {
		return nil, err
	}
	if _, err := s.loadUsableProject(ctx, s.store, projectID); err != nil {
		return nil, err
	}
	sessions, err := s.store.ListPlanningSessionsByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("查询规划会话列表失败: %w", err)
	}
	return sessions, nil
}

// Messages 按序号升序返回会话的全部消息。
//
// 发送方类型原样返回：未知取值是历史事实，不在读取路径被映射为默认发送方。会话不存在
// 时返回 ErrPlanningSessionNotFound，而不是一个空列表——否则调用方无法区分"没有消息"
// 与"会话标识写错"。
func (s *PlanningSessionService) Messages(
	ctx context.Context,
	sessionIDValue string,
) ([]domain.PlanningMessage, error) {
	sessionID, err := parsePlanningSessionID(sessionIDValue)
	if err != nil {
		return nil, err
	}
	if _, err := readPlanningSession(ctx, s.store, sessionID); err != nil {
		return nil, err
	}
	messages, err := s.store.ListPlanningMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("查询规划消息失败: %w", err)
	}
	return messages, nil
}

// Revisions 按版本号升序返回会话的全部方案版本。
//
// 历史版本只追加不覆盖：返回结果包含每一轮生成的快照，最新版本不替代旧版本。会话不存在
// 时与 Messages 使用同一个错误类别。
func (s *PlanningSessionService) Revisions(
	ctx context.Context,
	sessionIDValue string,
) ([]domain.PlanningRevision, error) {
	sessionID, err := parsePlanningSessionID(sessionIDValue)
	if err != nil {
		return nil, err
	}
	if _, err := readPlanningSession(ctx, s.store, sessionID); err != nil {
		return nil, err
	}
	revisions, err := s.store.ListPlanningRevisions(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("查询规划版本失败: %w", err)
	}
	return revisions, nil
}

// loadUsableProject 读取规划会话必须关联的项目。
//
// GetProject 默认过滤已逻辑删除的记录，这里再显式确认一次：调用方不应依赖"删除后
// 返回空项目"这类隐式行为，错误信息也必须能区分"标识写错"与"项目已下线"。
func (s *PlanningSessionService) loadUsableProject(
	ctx context.Context,
	tx storage.Tx,
	projectID string,
) (domain.Project, error) {
	project, err := tx.GetProject(ctx, domain.ID(projectID))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Project{}, fmt.Errorf(
				"%w: 项目 %s 不存在或已删除",
				ErrPlanningSessionNotFound,
				projectID,
			)
		}
		return domain.Project{}, fmt.Errorf("读取项目失败: %w", err)
	}
	if project.Deleted() {
		return domain.Project{}, fmt.Errorf(
			"%w: 项目 %s 已删除",
			ErrPlanningSessionNotFound,
			projectID,
		)
	}
	return *project, nil
}

// readPlanningSession 读取规划会话，并把"记录不存在"映射为业务错误。
//
// 事务内外读取共用同一套映射：store 与 WithTx 传入的 Tx 都实现 storage.Tx。
func readPlanningSession(
	ctx context.Context,
	tx storage.Tx,
	sessionID domain.ID,
) (domain.PlanningSession, error) {
	session, err := tx.GetPlanningSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.PlanningSession{}, fmt.Errorf(
				"%w: %s",
				ErrPlanningSessionNotFound,
				sessionID,
			)
		}
		return domain.PlanningSession{}, fmt.Errorf("读取规划会话失败: %w", err)
	}
	return *session, nil
}

// parsePlanningSessionID 校验规划会话标识的书写形式。
func parsePlanningSessionID(value string) (domain.ID, error) {
	sessionID := domain.ID(strings.TrimSpace(value))
	if err := sessionID.Validate(); err != nil {
		return "", fmt.Errorf("%w: 规划会话标识不能为空", ErrPlanningSessionInvalidInput)
	}
	return sessionID, nil
}

// parsePlanningProjectID 校验并归一化项目标识。
//
// 项目标识统一小写：它与缓存共享范围、远程管理工作目录名绑定，大小写不同会被当成
// 两个不同的项目。
func parsePlanningProjectID(value string) (string, error) {
	projectID := strings.ToLower(strings.TrimSpace(value))
	if projectID == "" {
		return "", fmt.Errorf("%w: 项目标识不能为空", ErrPlanningSessionInvalidInput)
	}
	if err := domain.ValidateProjectID(projectID); err != nil {
		return "", fmt.Errorf("%w: %w", ErrPlanningSessionInvalidInput, err)
	}
	return projectID, nil
}

// mapPlanningTransitionFailure 把领域状态机的拒绝映射为业务错误。
//
// 状态机规则只在 domain 定义一次，这里只做类别翻译，不重新判断当前状态是否允许迁移。
func mapPlanningTransitionFailure(err error) error {
	if errors.Is(err, domain.ErrInvalidState) || errors.Is(err, domain.ErrInvalidTransition) {
		return fmt.Errorf("%w: %w", ErrPlanningSessionStateConflict, err)
	}
	return err
}

// mapPlanningUpdateFailure 把条件更新的失败映射为业务错误。
//
// storage.ErrConflict 表示状态已被其他操作推进：迟到的结果不能覆盖已确认、已放弃或
// 已失败的结论，也不能把写入失败报告为成功。
func mapPlanningUpdateFailure(err error, sessionID domain.ID) error {
	switch {
	case errors.Is(err, storage.ErrConflict):
		return fmt.Errorf(
			"%w: 规划会话 %s 的状态已变更，拒绝过期写入",
			ErrPlanningSessionStateConflict,
			sessionID,
		)
	case errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("%w: %s", ErrPlanningSessionNotFound, sessionID)
	default:
		return fmt.Errorf("更新规划会话失败: %w", err)
	}
}
