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
