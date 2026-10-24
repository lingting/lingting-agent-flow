package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/execenv"
	"github.com/lingting/agentflow/internal/storage"
)

// TaskEnvironmentRequest 是依据任务记录准备执行环境的输入。
type TaskEnvironmentRequest struct {
	// TaskID 是任务标识。
	TaskID string
	// AttemptID 是本次执行所属尝试，可为空。
	//
	// 显式给出时它会写入容器登记记录，使容器与执行尝试一一对应；
	// 为空时不写入，避免伪造关联。
	AttemptID string
	// SkipRunContractCheck 跳过镜像常驻运行契约预检。
	SkipRunContractCheck bool
}

// PrepareForTask 依据任务配置准备本次执行的执行环境。
//
// 关联规则集中在这里，而不是散落在 Handler 或工作流中：
//   - 执行节点、语言环境、环境版本、缓存与工作目录全部来自任务记录；
//   - 尝试标识必须属于该任务，且必须与任务同时给出；
//   - 每次调用都会创建全新的容器，不复用旧容器。
func (s *ExecutionEnvironmentService) PrepareForTask(
	ctx context.Context,
	request TaskEnvironmentRequest,
) (execenv.Prepared, error) {
	taskID := domain.ID(strings.TrimSpace(request.TaskID))
	if err := taskID.Validate(); err != nil {
		return execenv.Prepared{}, fmt.Errorf("%w: 任务标识不合法: %w", execenv.ErrInvalidValue, err)
	}
	task, err := s.loadTaskForEnvironment(ctx, taskID)
	if err != nil {
		return execenv.Prepared{}, err
	}
	attemptID, err := s.resolveAttemptID(ctx, task, request.AttemptID)
	if err != nil {
		return execenv.Prepared{}, err
	}
	if task.HostID == nil {
		return execenv.Prepared{}, fmt.Errorf(
			"%w: 任务 %s 没有绑定执行节点，无法准备执行环境",
			execenv.ErrNotReady,
			task.ID,
		)
	}
	if task.Config.Environment.IsZero() {
		return execenv.Prepared{}, fmt.Errorf(
			"%w: 任务 %s 没有配置语言环境，无法准备执行环境",
			execenv.ErrNotReady,
			task.ID,
		)
	}
	versionNumber := 0
	if label := strings.TrimSpace(task.Config.EnvironmentVersion); label != "" {
		parsed, parseErr := domain.ParseVersionLabel(label)
		if parseErr != nil {
			return execenv.Prepared{}, parseErr
		}
		versionNumber = parsed
	}
	// 创建新容器之前必须确认任务没有仍受管理的旧容器记录：容器停止、状态不明或
	// 残留（孤儿）时直接建第二个容器，会让第一代容器变成永远不会被回收的孤儿，
	// 同时把两代容器的工作区混进同一条任务链。旧容器只能由用户显式 delete / detach。
	if err := s.ensureNoPendingContainer(ctx, task); err != nil {
		return execenv.Prepared{}, err
	}
	spec := execenv.PrepareSpec{
		HostID:               task.HostID.String(),
		Environment:          task.Config.Environment,
		VersionNumber:        versionNumber,
		ProjectID:            task.Config.ProjectID,
		Caches:               task.Config.Caches,
		WorkDir:              task.Config.EffectiveWorkDir(),
		TaskID:               &task.ID,
		AttemptID:            attemptID,
		SkipRunContractCheck: request.SkipRunContractCheck,
	}
	prepared, err := s.Prepare(ctx, spec)
	if err != nil {
		return prepared, err
	}
	// 容器与任务的关联在准备成功后写入任务记录：容器创建失败时不会留下
	// "数据库标记为可用、实际不存在"的容器引用。
	if err := s.attachContainerToTask(ctx, task, prepared); err != nil {
		return prepared, err
	}
	return prepared, nil
}

// loadTaskForEnvironment 读取任务并拒绝已删除的记录。
func (s *ExecutionEnvironmentService) loadTaskForEnvironment(
	ctx context.Context,
	taskID domain.ID,
) (domain.Task, error) {
	task, err := s.tasks.GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Task{}, fmt.Errorf(
				"%w: 任务 %s 不存在",
				execenv.ErrNotReady,
				taskID,
			)
		}
		return domain.Task{}, fmt.Errorf("读取任务失败: %w", err)
	}
	if task.Deleted() {
		return domain.Task{}, fmt.Errorf(
			"%w: 任务 %s 已删除",
			execenv.ErrNotReady,
			taskID,
		)
	}
	return *task, nil
}

// resolveAttemptID 校验尝试归属并返回可写入容器的尝试标识。
func (s *ExecutionEnvironmentService) resolveAttemptID(
	ctx context.Context,
	task domain.Task,
	raw string,
) (*domain.ID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	attemptID := domain.ID(trimmed)
	if err := attemptID.Validate(); err != nil {
		return nil, fmt.Errorf("%w: 尝试标识不合法: %w", execenv.ErrInvalidValue, err)
	}
	attempt, err := s.tasks.GetAttempt(ctx, attemptID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, fmt.Errorf(
				"%w: 尝试 %s 不存在",
				execenv.ErrNotReady,
				attemptID,
			)
		}
		return nil, fmt.Errorf("读取执行尝试失败: %w", err)
	}
	if attempt.TaskID != task.ID {
		return nil, fmt.Errorf(
			"%w: 尝试 %s 不属于任务 %s",
			execenv.ErrInvalidValue,
			attemptID,
			task.ID,
		)
	}
	return &attemptID, nil
}

// attachContainerToTask 把新容器登记记录写回任务。
//
// 写入使用状态前置条件：任务状态在此期间被其他操作推进时不覆盖，而是返回
// ErrStateUncertain 并说明容器仍处于受管理状态。此时容器本身已经创建并登记，
// 只是没有关联到任务，因此用户可以按容器记录检查或删除它，不会被当作
// "数据库中标记为可用、实际不存在"的容器。
func (s *ExecutionEnvironmentService) attachContainerToTask(
	ctx context.Context,
	task domain.Task,
	prepared execenv.Prepared,
) error {
	containerID := prepared.Environment.Container().ContainerID
	updated := task
	if err := updated.SetContainer(containerID); err != nil {
		return err
	}
	if err := s.tasks.UpdateTaskIfStatus(
		ctx,
		updated,
		[]domain.TaskStatus{task.Status},
	); err != nil {
		// 容器已经创建并登记，只是没有关联到任务：留下可追溯的现场记录，
		// 否则这个容器既不会被任何执行链路使用，也不会出现在任务时间线上。
		s.recordOrphanContainer(ctx, task, containerID, err)
		if errors.Is(err, storage.ErrConflict) {
			return fmt.Errorf(
				"%w: 任务 %s 状态已变更，容器 %s 未写入任务记录",
				execenv.ErrStateUncertain,
				task.ID,
				containerID,
			)
		}
		return fmt.Errorf("写入任务容器关联失败: %w", err)
	}
	s.debug(
		ctx,
		"任务容器关联已写入",
		slog.String("task_id", task.ID.String()),
		slog.String("container", containerID),
	)
	return nil
}

// recordOrphanContainer 记录"容器已创建但未写回任务"的现场。
//
// 事件写入失败只记录日志：它不能替代已经发生的事实，也不应把一次关联失败
// 变成两个错误。容器本身保留，由用户按容器记录核验或处置，绝不自动删除。
func (s *ExecutionEnvironmentService) recordOrphanContainer(
	ctx context.Context,
	task domain.Task,
	containerID string,
	cause error,
) {
	event, err := domain.NewEvent(
		task.ID,
		nil,
		nil,
		nil,
		domain.EventTypeTaskRecoveryRequired,
		marshalStepExecutionPayload(map[string]any{
			"stage":        "attach_container",
			"container_id": containerID,
			"task_status":  string(task.Status),
			"summary":      errorSummary(cause),
		}),
		time.Now().UTC(),
	)
	if err != nil {
		s.debug(
			ctx,
			"构造容器关联失败事件失败",
			slog.String("task_id", task.ID.String()),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	if _, appendErr := s.tasks.AppendEvent(ctx, event); appendErr != nil {
		s.debug(
			ctx,
			"写入容器关联失败事件失败",
			slog.String("task_id", task.ID.String()),
			slog.String("container", containerID),
			slog.String("error", errorSummary(appendErr)),
		)
	}
}

// ensureNoPendingContainer 确认任务没有仍受管理的旧容器记录。
//
// 允许创建新容器的唯一前提是"任务没有任何可能存在的容器"，或那些容器已被
// 显式处置（detached / removed）。其余情况必须先由用户处置旧容器：
//   - 停止的容器：它的工作区可能仍保存着未推送的代码，直接丢弃会覆盖现场；
//   - 状态不明（unknown / creating）的容器：远程可能仍然存在，新容器会让两代
//     容器同时存在，任务关联只能指向其中一个；
//   - 孤儿容器（登记了任务关联但未被任务引用）：它的工作区状态未知。
//
// 判定只读：本方法不修改任何状态，也不执行远程操作。
func (s *ExecutionEnvironmentService) ensureNoPendingContainer(
	ctx context.Context,
	task domain.Task,
) error {
	blocking, err := s.pendingContainerRecords(ctx, task)
	if err != nil {
		return err
	}
	if len(blocking) == 0 {
		return nil
	}
	first := blocking[0]
	return fmt.Errorf(
		"%w: 任务 %s 仍有未处置的容器记录 %s（状态 %s），"+
			"请先执行 agentflow docker container inspect %s 核验现场，"+
			"再显式 delete 或 detach 后再重新准备执行环境",
		execenv.ErrStateUncertain,
		task.ID,
		first.ID,
		first.State,
		first.ID,
	)
}

// pendingContainerRecords 返回阻止创建新容器的容器记录。
//
// 它把"任务当前使用的容器"与"登记了任务关联但未被引用的容器"一并纳入判断，
// 因为两者都可能是真实存在的现场。
func (s *ExecutionEnvironmentService) pendingContainerRecords(
	ctx context.Context,
	task domain.Task,
) ([]domain.Container, error) {
	if s.containerRecords == nil {
		// 无容器查询能力时不做该判定：调用方仍会在容器核验阶段得到更具体的错误。
		return nil, nil
	}
	current := strings.TrimSpace(task.ContainerID)
	pending := make([]domain.Container, 0, 2)
	records, err := s.containerRecords.ListContainers(ctx, domain.ContainerFilter{
		TaskID: &task.ID,
		// 只关心仍可能存在的容器：已删除与已脱离管理的容器不阻止新容器。
		IncludeDetached: false,
	})
	if err != nil {
		return nil, fmt.Errorf("读取任务 %s 的容器记录失败: %w", task.ID, err)
	}
	for _, container := range records {
		if container.State == domain.ContainerStateRemoved ||
			container.State == domain.ContainerStateDetached ||
			container.Detached {
			continue
		}
		if current != "" && container.ID.String() == current {
			// 任务当前使用的容器由调用方的可用性判定负责：它不可用时同样不允许
			// 直接新建，因此一并视为阻止项。
			pending = append(pending, container)
			continue
		}
		pending = append(pending, container)
	}
	return pending, nil
}
