package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// ContainerRecoveryStore 是容器残留收敛依赖的存储能力。
type ContainerRecoveryStore interface {
	// ListContainers 按筛选条件读取容器登记记录。
	ListContainers(ctx context.Context, filter domain.ContainerFilter) ([]domain.Container, error)
	// UpdateContainerIfState 在数据库中的状态仍属于 expected 时写入容器记录。
	UpdateContainerIfState(
		ctx context.Context,
		container domain.Container,
		expected []domain.ContainerState,
	) error
	// AppendEvent 追加任务事件。
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
}

// ContainerRecoveryContainerPort 是容器残留收敛对远程观测能力的需求。
//
// 它只做只读核验：恢复不会创建、启动、删除或接管容器；是否处置由领域判定给出，
// 破坏性操作仍必须由用户显式发起。
type ContainerRecoveryContainerPort interface {
	InspectContainer(ctx context.Context, request InspectContainerRequest) (ContainerDetail, error)
}

// ContainerRecoveryOptions 是构造容器残留收敛服务的输入。
type ContainerRecoveryOptions struct {
	// Store 是持久化入口，必填。
	Store ContainerRecoveryStore
	// Containers 是容器只读观测能力。
	//
	// 为空表示本次运行不提供远程观测：恢复只报告残留，不改变任何记录。
	Containers ContainerRecoveryContainerPort
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
}

// ContainerRecoveryService 收敛容器登记记录中无法自动解释的残留。
//
// 职责边界：
//   - 只处理 `creating` / `unknown` 与孤儿容器两类现场；
//   - 只用只读观测判断现场，不创建、不启动、不删除、不接管容器；
//   - 已脱离管理（detached）与已删除（removed）的容器一律不动；
//   - 无法确认时保留 `unknown` 与观测错误，不把不确定性写成确定状态；
//   - 孤儿容器只报告，不自动删除也不自动关联回任务。
//
// 观测失败不让启动失败：节点离线是正常运行状态，把服务启动绑在每台节点可达上会
// 让一个离线节点阻塞整个 AgentFlow。这与 HostValidationService 的"失败只记日志"
// 口径一致，且现场保持 `unknown`，不会伪装成已验证。
type ContainerRecoveryService struct {
	store      ContainerRecoveryStore
	containers ContainerRecoveryContainerPort
	logger     *slog.Logger
	now        func() time.Time
}

// NewContainerRecoveryService 构造容器残留收敛服务。
func NewContainerRecoveryService(
	options ContainerRecoveryOptions,
) (*ContainerRecoveryService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("容器残留收敛服务初始化失败: 存储不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &ContainerRecoveryService{
		store:      options.Store,
		containers: options.Containers,
		logger:     logger,
		now:        now,
	}, nil
}

// Name 返回后台服务名称。
func (s *ContainerRecoveryService) Name() string {
	return "container_recovery"
}

// Start 收敛容器残留并报告孤儿容器。
//
// 顺序固定为：先收敛"已知归属但状态不明"的记录，再报告孤儿容器。读取失败让启动
// 失败：无法判断是否存在状态不明的容器时继续启动，会让"数据库里有记录、远程可能
// 不存在"的现场被当作可用容器使用。
func (s *ContainerRecoveryService) Start(ctx context.Context) error {
	if err := s.recoverUncertain(ctx); err != nil {
		return err
	}
	return s.reportOrphans(ctx)
}

// Stop 不做事：收敛是启动时的一次性动作，没有长期 goroutine。
func (s *ContainerRecoveryService) Stop(ctx context.Context) error {
	return nil
}

// recoverUncertain 收敛状态不明或尚未确认创建的容器记录。
func (s *ContainerRecoveryService) recoverUncertain(ctx context.Context) error {
	containers, err := s.store.ListContainers(ctx, domain.ContainerFilter{
		States: []domain.ContainerState{
			domain.ContainerStateCreating,
			domain.ContainerStateUnknown,
		},
		IncludeDetached: true,
	})
	if err != nil {
		return fmt.Errorf("读取状态不明的容器记录失败: %w", err)
	}
	for _, container := range containers {
		if err := s.recoverContainer(ctx, container); err != nil {
			return fmt.Errorf("收敛容器 %s 失败: %w", container.ID, err)
		}
	}
	return nil
}

// recoverContainer 收敛单条容器记录。
func (s *ContainerRecoveryService) recoverContainer(
	ctx context.Context,
	container domain.Container,
) error {
	if container.Detached || container.State == domain.ContainerStateDetached ||
		container.State == domain.ContainerStateRemoved {
		// 已脱离管理与已删除的容器不参与收敛：用户已经显式处置或记录已终结。
		return nil
	}
	if s.containers == nil {
		// 本次运行没有远程观测能力：只报告，不改变记录。
		s.logger.WarnContext(
			ctx,
			"本次运行未提供容器观测能力，容器状态保持原样",
			slog.String("container", container.ID.String()),
			slog.String("state", string(container.State)),
		)
		return nil
	}
	detail, inspectErr := s.containers.InspectContainer(ctx, InspectContainerRequest{
		ContainerID: container.ID.String(),
		// 只读观测：不写回观测状态，收敛结论由本服务统一提交。
		Sync: false,
	})
	observation, observedDockerID := ClassifyContainerObservation(container, detail, inspectErr)
	decision := domain.DecideContainerRecovery(domain.ContainerRecoveryInput{
		Container:        container,
		Observation:      observation,
		ObservedDockerID: observedDockerID,
	})
	if !decision.Needed {
		s.reportDecision(ctx, container, decision)
		return nil
	}
	updated, err := container.ApplyContainerRecovery(decision, s.now())
	if err != nil {
		// 状态机拒绝说明本地副本已经过期：保留现场并记录日志，不覆盖数据库。
		s.logger.WarnContext(
			ctx,
			"容器收敛被状态机拒绝",
			slog.String("container", container.ID.String()),
			slog.String("state", string(container.State)),
			slog.String("target", string(decision.Next)),
			slog.String("error", errorSummary(err)),
		)
		return nil
	}
	if err := s.store.UpdateContainerIfState(
		context.WithoutCancel(ctx),
		updated,
		[]domain.ContainerState{container.State},
	); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			// 其他实例已经推进了这条记录：不是失败，也不重复写事件。
			return nil
		}
		return err
	}
	s.reportDecision(ctx, updated, decision)
	return nil
}

// reportDecision 记录一次容器收敛结论并追加可追溯事件。
//
// 事件写入失败只记日志：它不影响已经落库的收敛结论，也不能让启动失败。
func (s *ContainerRecoveryService) reportDecision(
	ctx context.Context,
	container domain.Container,
	decision domain.ContainerRecoveryDecision,
) {
	summary := domain.ContainerRecoverySummary(decision)
	level := slog.LevelInfo
	if decision.NeedsManual {
		level = slog.LevelWarn
	}
	s.logger.LogAttrs(
		ctx,
		level,
		"容器残留收敛结论",
		slog.String("container", container.ID.String()),
		slog.String("task_id", idValue(container.TaskID)),
		slog.String("state", string(container.State)),
		slog.String("error_class", decision.ErrorClass),
		slog.String("summary", summary),
		slog.Bool("needs_manual", decision.NeedsManual),
	)
	var taskID domain.ID
	if container.TaskID != nil {
		taskID = *container.TaskID
	}
	if taskID.IsEmpty() {
		// 容器没有任务关联：没有任务时间线可写，仅保留日志。
		return
	}
	event, err := domain.NewEvent(
		taskID,
		nil,
		nil,
		nil,
		domain.EventTypeTaskRecoveryRequired,
		marshalStepExecutionPayload(map[string]any{
			"stage":           "recover_container",
			"container_id":    container.ID.String(),
			"container_state": string(container.State),
			"error_class":     decision.ErrorClass,
			"summary":         summary,
			"needs_manual":    decision.NeedsManual,
			"recovery_auto":   true,
		}),
		s.now(),
	)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"构造容器收敛事件失败",
			slog.String("container", container.ID.String()),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	if _, err := s.store.AppendEvent(context.WithoutCancel(ctx), event); err != nil {
		s.logger.WarnContext(
			ctx,
			"写入容器收敛事件失败",
			slog.String("container", container.ID.String()),
			slog.String("error", errorSummary(err)),
		)
	}
}

// reportOrphans 报告孤儿容器。
//
// 孤儿容器是"创建成功、写回任务失败"的现场：它登记了任务关联，却不是该任务当前
// 使用的容器。它既不能被自动删除（工作区状态未知），也不能被自动重新关联（会让
// 未核验的现场重新进入执行链路），因此只报告。
func (s *ContainerRecoveryService) reportOrphans(ctx context.Context) error {
	containers, err := s.store.ListContainers(ctx, domain.ContainerFilter{
		Orphans:         true,
		IncludeDetached: true,
	})
	if err != nil {
		return fmt.Errorf("读取孤儿容器失败: %w", err)
	}
	for _, container := range containers {
		if container.State == domain.ContainerStateRemoved {
			// 已删除的容器不再是现场。
			continue
		}
		var taskID domain.ID
		if container.TaskID != nil {
			taskID = *container.TaskID
		}
		summary := domain.ContainerOrphanSummary(taskID)
		s.logger.WarnContext(
			ctx,
			"发现未被任务引用的容器记录",
			slog.String("container", container.ID.String()),
			slog.String("container_name", container.Name),
			slog.String("task_id", taskID.String()),
			slog.String("state", string(container.State)),
		)
		if taskID.IsEmpty() {
			continue
		}
		event, err := domain.NewEvent(
			taskID,
			nil,
			nil,
			nil,
			domain.EventTypeTaskRecoveryRequired,
			marshalStepExecutionPayload(map[string]any{
				"stage":           "recover_container_orphan",
				"container_id":    container.ID.String(),
				"container_name":  container.Name,
				"container_state": string(container.State),
				"summary":         summary,
				"recovery_auto":   false,
			}),
			s.now(),
		)
		if err != nil {
			s.logger.WarnContext(
				ctx,
				"构造孤儿容器事件失败",
				slog.String("container", container.ID.String()),
				slog.String("error", errorSummary(err)),
			)
			continue
		}
		if _, err := s.store.AppendEvent(context.WithoutCancel(ctx), event); err != nil {
			s.logger.WarnContext(
				ctx,
				"写入孤儿容器事件失败",
				slog.String("container", container.ID.String()),
				slog.String("error", errorSummary(err)),
			)
		}
	}
	return nil
}

// idValue 把可空标识渲染为字符串，空值返回空字符串。
func idValue(id *domain.ID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
