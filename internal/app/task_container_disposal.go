package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 任务容器处置服务的错误类别。
//
// Handler 通过 errors.Is 判断类别并映射 HTTP 状态码，不解析错误文本。
var (
	// ErrContainerInvalidInput 表示请求参数不合法（例如容器标识为空或格式非法）。
	ErrContainerInvalidInput = errors.New("app: 容器请求参数非法")
	// ErrContainerTaskNotFound 表示任务不存在或已被逻辑删除。
	ErrContainerTaskNotFound = errors.New("app: 任务不存在")
	// ErrContainerNotFound 表示容器登记记录不存在。
	ErrContainerNotFound = errors.New("app: 容器登记记录不存在")
	// ErrContainerOwnershipRejected 表示容器不属于该任务，或不属于该任务的执行节点。
	//
	// 它是跨任务操作的唯一可核验防线：绝不会因为请求里同时给出任务与容器两个
	// 标识就认为关联成立。
	ErrContainerOwnershipRejected = errors.New("app: 容器归属校验未通过")
	// ErrContainerOperationRejected 表示当前容器状态不允许该操作。
	//
	// 例如容器已脱离管理（拒绝重复 detach）、已删除（拒绝 detach），或容器服务
	// 因既有的自动管理约束拒绝删除。该错误不改写真实原因。
	ErrContainerOperationRejected = errors.New("app: 容器当前状态不允许该操作")
	// ErrContainerOperationFailed 表示操作已经执行但远程或本地环节失败。
	ErrContainerOperationFailed = errors.New("app: 容器操作未成功")
	// ErrContainerOperationUncertain 表示远程操作结果无法确认。
	//
	// 调用方必须按"结果不确定"处理：先核验容器真实状态，再由用户决定下一步，
	// 不得当作成功，也不得自动重发。
	ErrContainerOperationUncertain = errors.New("app: 容器操作结果无法确认")
)

// ContainerQueryStore 是任务容器查询所需的最小存储能力。
type ContainerQueryStore interface {
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	GetContainer(ctx context.Context, id domain.ID) (*domain.Container, error)
	ListContainers(ctx context.Context, filter domain.ContainerFilter) ([]domain.Container, error)
}

// ContainerDisposalPort 是任务容器处置依赖的容器操作能力。
//
// 它由 `app.ContainerService` 实现：删除与脱离管理仍由容器服务执行既有的所有权
// 核验、远程操作与记录更新，本服务不自行拼接 Docker 命令，也不直接写容器记录。
type ContainerDisposalPort interface {
	DeleteContainer(ctx context.Context, request DeleteContainerRequest) (ContainerDetail, error)
	DetachContainer(ctx context.Context, request DetachContainerRequest) (ContainerDetail, error)
}

// TaskContainerDisposalOptions 是构造任务容器处置服务的输入。
type TaskContainerDisposalOptions struct {
	// Store 是持久化入口，必填。
	Store ContainerQueryStore
	// Containers 是容器操作能力。
	//
	// 为空时查询仍然可用（只依赖存储），但两个处置接口返回服务不可用：
	// 没有容器服务就无法证明所有权，也无法执行远程操作。
	Containers ContainerDisposalPort
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
}

// TaskContainerDisposalService 提供任务容器的只读查询与显式处置（delete / detach）。
//
// 职责边界：
//   - 查询只依赖存储：不建立 SSH / Docker 会话，因此"看任务有哪些容器"不需要远程能力；
//   - 处置必须同时具备存储与容器服务：前者用于核验任务归属，后者用于核验远程所有权
//     并执行真实操作；
//   - 不创建、启动或接管容器，也不推进任务状态、不触发重试或工作流。
type TaskContainerDisposalService struct {
	store      ContainerQueryStore
	containers ContainerDisposalPort
	logger     *slog.Logger
}

// NewTaskContainerDisposalService 构造任务容器处置服务。
func NewTaskContainerDisposalService(
	options TaskContainerDisposalOptions,
) (*TaskContainerDisposalService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("任务容器处置服务初始化失败: 存储不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &TaskContainerDisposalService{
		store:      options.Store,
		containers: options.Containers,
		logger:     logger,
	}, nil
}

// TaskContainerView 是一条任务容器记录及其与任务的关联。
type TaskContainerView struct {
	// Container 是数据库中的真实容器登记记录。
	Container domain.Container
	// Origin 说明这条记录与任务的关联方式。
	Origin ContainerOrigin
}

// ContainerOrigin 是容器记录与任务的关联方式。
type ContainerOrigin string

const (
	// ContainerOriginCurrent 表示它是任务记录里 container_id 引用的那一条。
	ContainerOriginCurrent ContainerOrigin = "current"
	// ContainerOriginHistory 表示它登记了任务归属，但不是任务当前引用的那一条。
	//
	// 它通常是重新开始或重试之前的旧代次。
	ContainerOriginHistory ContainerOrigin = "history"
)

// String 返回关联方式的字符串形式。
func (o ContainerOrigin) String() string {
	return string(o)
}

// TaskContainerDisposalResult 是一次显式处置的结果。
//
// 结论与记录必须分别读：Outcome 表达"本次处置是否完成"，Container 是处置后从
// 数据库重新读到的真实记录（状态、脱离标记、结束原因）。调用方不得只看 Outcome
// 就推断容器已被删除，也不得用请求前的旧副本代替重读结果。
type TaskContainerDisposalResult struct {
	// TaskID 是任务标识。
	TaskID domain.ID
	// Container 是处置后的容器登记记录（以数据库中的真实值重新读取）。
	Container domain.Container
	// Outcome 是本次处置的应用层结论。
	Outcome ContainerDisposalOutcome
	// Detail 是可读说明；失败时保留真实原因。
	Detail string
}

// ContainerDisposalOutcome 是一次处置的应用层结论。
type ContainerDisposalOutcome string

const (
	// ContainerDisposalCompleted 表示容器服务报告处置成功。
	ContainerDisposalCompleted ContainerDisposalOutcome = "completed"
	// ContainerDisposalFailed 表示操作已经发起但失败（远程或本地环节）。
	ContainerDisposalFailed ContainerDisposalOutcome = "failed"
	// ContainerDisposalUncertain 表示操作结果无法确认，必须核验后再决定。
	ContainerDisposalUncertain ContainerDisposalOutcome = "uncertain"
)

// String 返回结论的字符串形式。
func (o ContainerDisposalOutcome) String() string {
	return string(o)
}

// ContainerDisposalAction 是本次处置的动作。
type ContainerDisposalAction string

const (
	// ContainerDisposalDelete 是删除动作。
	ContainerDisposalDelete ContainerDisposalAction = "delete"
	// ContainerDisposalDetach 是脱离管理动作。
	ContainerDisposalDetach ContainerDisposalAction = "detach"
)

// String 返回动作的字符串形式。
func (a ContainerDisposalAction) String() string {
	return string(a)
}

// Validate 校验动作是否受支持。
func (a ContainerDisposalAction) Validate() error {
	switch a {
	case ContainerDisposalDelete, ContainerDisposalDetach:
		return nil
	default:
		return fmt.Errorf(
			"%w: 只支持 delete 或 detach，实际 %q",
			ErrContainerInvalidInput,
			string(a),
		)
	}
}

// disposal 把动作转换为领域层的处置方式。
func (a ContainerDisposalAction) disposal() domain.RestartDisposition {
	if a == ContainerDisposalDetach {
		return domain.RestartDispositionDetach
	}
	return domain.RestartDispositionDelete
}

// TaskContainersRequest 是查询任务容器的输入。
type TaskContainersRequest struct {
	// TaskID 是任务标识。
	TaskID string
}

// TaskContainers 返回任务关联的容器记录。
//
// 只读：不建立远程会话、不修改记录、不推进任务状态。返回顺序沿用存储层语义
// （按创建时间升序），因此历史代次在前、当前代次在后。
func (s *TaskContainerDisposalService) TaskContainers(
	ctx context.Context,
	request TaskContainersRequest,
) ([]TaskContainerView, error) {
	task, err := s.loadTask(ctx, request.TaskID)
	if err != nil {
		return nil, err
	}
	records, err := s.store.ListContainers(ctx, domain.ContainerFilter{
		TaskID:          &task.ID,
		IncludeDetached: true,
	})
	if err != nil {
		return nil, fmt.Errorf("读取任务容器记录失败: %w", err)
	}
	current := strings.TrimSpace(task.ContainerID)
	views := make([]TaskContainerView, 0, len(records))
	for _, container := range records {
		origin := ContainerOriginHistory
		if current != "" && container.ID.String() == current {
			origin = ContainerOriginCurrent
		}
		views = append(views, TaskContainerView{Container: container, Origin: origin})
	}
	return views, nil
}

// TaskContainerDisposalRequest 是处置一条任务容器的输入。
//
// 标识全部来自经过校验的数据库记录：调用方只能指定任务、容器与动作，不能指定
// Session 路径、Docker 容器 ID、执行节点或远程命令。
type TaskContainerDisposalRequest struct {
	// TaskID 是任务标识。
	TaskID string
	// ContainerID 是容器登记记录标识。
	ContainerID string
	// Action 是 delete 或 detach。
	Action ContainerDisposalAction
	// Reason 是删除时写入的生命周期结束原因，空值使用 manual。
	//
	// detach 不使用该字段：脱离管理的原因由领域层固定为 detached。
	Reason domain.ContainerEndReason
}

// Disposal 显式处置任务的一条容器记录。
//
// 顺序固定：参数与动作校验 → 任务读取 → 归属校验（应用层，纯函数）→ 拒绝已脱离 /
// 已删除的重复操作 → 容器服务执行既有操作 → 重新读取真实记录。
//
// 任何一步失败都如实返回，不把"远程已执行但记录未更新"伪装成完全成功。
func (s *TaskContainerDisposalService) Disposal(
	ctx context.Context,
	request TaskContainerDisposalRequest,
) (TaskContainerDisposalResult, error) {
	if err := request.Action.Validate(); err != nil {
		return TaskContainerDisposalResult{}, err
	}
	task, err := s.loadTask(ctx, request.TaskID)
	if err != nil {
		return TaskContainerDisposalResult{}, err
	}
	containerID, err := s.parseContainerID(request.ContainerID)
	if err != nil {
		return TaskContainerDisposalResult{}, err
	}
	if s.containers == nil {
		return TaskContainerDisposalResult{
			TaskID: task.ID,
		}, fmt.Errorf("%w: 容器服务未初始化", ErrContainerOperationFailed)
	}
	container, err := s.loadForDisposal(ctx, containerID)
	if err != nil {
		return TaskContainerDisposalResult{}, err
	}
	// 归属校验先于任何远程操作：任务与容器两个标识同时出现在请求里不代表关联成立。
	if err := domain.CheckContainerDisposal(task, container, request.Action.disposal()); err != nil {
		return TaskContainerDisposalResult{
			Container: container,
			TaskID:    task.ID,
		}, fmt.Errorf("%w: %w", ErrContainerOwnershipRejected, err)
	}
	result := TaskContainerDisposalResult{Container: container, TaskID: task.ID}
	detail, runErr := s.run(ctx, containerID, request)
	if detail.Container.ID != "" {
		result.Container = detail.Container
	}
	if runErr != nil {
		result.Outcome = ContainerDisposalFailed
		result.Detail = strings.TrimSpace(runErr.Error())
		return result, s.reclassify(runErr)
	}
	// 处置已由容器服务落库：重新读取一次，使响应描述数据库中的真实状态，
	// 而不是调用方手里的旧副本。
	observed, loadErr := s.store.GetContainer(ctx, container.ID)
	if loadErr != nil {
		s.logger.WarnContext(
			ctx,
			"处置成功但重新读取容器记录失败",
			slog.String("task_id", task.ID.String()),
			slog.String("container", containerID.String()),
			slog.String("error", loadErr.Error()),
		)
		result.Outcome = ContainerDisposalFailed
		result.Detail = "容器已处置，但重新读取记录失败: " + loadErr.Error()
		return result, fmt.Errorf(
			"%w: 容器已处置，但重新读取记录失败: %w",
			ErrContainerOperationFailed,
			loadErr,
		)
	}
	result.Container = *observed
	result.Outcome = containerDisposalOutcome(*observed)
	result.Detail = disposalDetail(request.Action, *observed)
	s.logger.InfoContext(
		ctx,
		"任务容器已处置",
		slog.String("task_id", task.ID.String()),
		slog.String("container", containerID.String()),
		slog.String("action", request.Action.String()),
		slog.String("outcome", result.Outcome.String()),
	)
	return result, nil
}

// run 调用容器服务执行真实操作。
func (s *TaskContainerDisposalService) run(
	ctx context.Context,
	containerID domain.ID,
	request TaskContainerDisposalRequest,
) (ContainerDetail, error) {
	if request.Action == ContainerDisposalDetach {
		return s.containers.DetachContainer(ctx, DetachContainerRequest{
			ContainerID: containerID.String(),
		})
	}
	reason := request.Reason
	if reason == "" {
		reason = domain.ContainerEndReasonManual
	}
	return s.containers.DeleteContainer(ctx, DeleteContainerRequest{
		ContainerID: containerID.String(),
		Reason:      reason,
	})
}

// reclassify 把容器服务的错误归入处置服务的错误类别。
//
// 映射依据错误内容中可核验的事实（远程状态不确定 / 状态被领域层拒绝），其余
// 一律按"操作未成功"返回，并保留原始错误供错误文本使用。
func (s *TaskContainerDisposalService) reclassify(err error) error {
	if err == nil {
		return nil
	}
	if isContainerOperationUncertain(err) {
		return fmt.Errorf("%w: %w", ErrContainerOperationUncertain, err)
	}
	if isContainerOperationRejected(err) {
		return fmt.Errorf("%w: %w", ErrContainerOperationRejected, err)
	}
	return fmt.Errorf("%w: %w", ErrContainerOperationFailed, err)
}

// loadTask 读取任务，不存在或已删除时返回"任务不存在"。
func (s *TaskContainerDisposalService) loadTask(
	ctx context.Context,
	raw string,
) (domain.Task, error) {
	id := domain.ID(strings.TrimSpace(raw))
	if err := id.Validate(); err != nil {
		return domain.Task{}, fmt.Errorf("%w: 任务标识不合法: %w", ErrContainerInvalidInput, err)
	}
	task, err := s.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Task{}, fmt.Errorf("%w: %s", ErrContainerTaskNotFound, id.String())
		}
		return domain.Task{}, fmt.Errorf("读取任务失败: %w", err)
	}
	if task.Deleted() {
		return domain.Task{}, fmt.Errorf("%w: %s", ErrContainerTaskNotFound, id.String())
	}
	return *task, nil
}

// parseContainerID 校验容器登记记录标识。
func (s *TaskContainerDisposalService) parseContainerID(raw string) (domain.ID, error) {
	id := domain.ID(strings.TrimSpace(raw))
	if err := id.Validate(); err != nil {
		return "", fmt.Errorf("%w: 容器标识不合法: %w", ErrContainerInvalidInput, err)
	}
	return id, nil
}

// loadForDisposal 读取待处置的容器登记记录。
//
// 这里只负责把"记录不存在"与"读取失败"区分开：记录是否存在是 404 与 500 的分界；
// 归属判定由 `domain.CheckContainerDisposal` 在调用方完成，避免同一规则分散到两处。
func (s *TaskContainerDisposalService) loadForDisposal(
	ctx context.Context,
	containerID domain.ID,
) (domain.Container, error) {
	container, err := s.store.GetContainer(ctx, containerID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Container{}, fmt.Errorf(
				"%w: 容器 %s",
				ErrContainerNotFound,
				containerID.String(),
			)
		}
		return domain.Container{}, fmt.Errorf("读取容器记录失败: %w", err)
	}
	return *container, nil
}

// containerDisposalOutcome 把执行后的记录转换为应用层结论。
func containerDisposalOutcome(container domain.Container) ContainerDisposalOutcome {
	if container.Detached || container.State == domain.ContainerStateDetached {
		return ContainerDisposalCompleted
	}
	if container.State == domain.ContainerStateRemoved {
		return ContainerDisposalCompleted
	}
	// 记录既没有删除也没有脱离：容器服务没有真正完成处置，不得报告成功。
	return ContainerDisposalUncertain
}

// disposalDetail 生成处置结论的可读说明。
func disposalDetail(action ContainerDisposalAction, container domain.Container) string {
	switch action {
	case ContainerDisposalDetach:
		return fmt.Sprintf(
			"容器 %s 已脱离 AgentFlow 自动管理，容器现场保留；它不再接受自动启停与删除",
			container.ID,
		)
	default:
		return fmt.Sprintf(
			"容器 %s 已删除；共享镜像、缓存卷与远程项目配置目录未受影响",
			container.ID,
		)
	}
}

// isContainerOperationUncertain 判断错误是否表示远程操作结果无法确认。
//
// 只有两类事实算"不确定"：容器服务报告的远程状态不确定，以及 context 超时。
// 用户取消、连接失败与命令非零退出都是确定的失败，不能混进这一类别——否则调用方
// 会把"确定没删掉"当成"可能删掉了"。
func isContainerOperationUncertain(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return docker.ErrorCodeOf(err) == docker.CodeStateUncertain
}

// isContainerOperationRejected 判断错误是否表示当前状态不允许该操作。
//
// 依据是错误链中的领域状态错误与领域状态机错误：它们由 `domain` 判定产生，
// 不是远程故障。字符串匹配无法可靠区分这些类别，因此这里只按错误类型判断。
func isContainerOperationRejected(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, domain.ErrInvalidState) ||
		errors.Is(err, domain.ErrInvalidTransition) ||
		errors.Is(err, storage.ErrConflict)
}
