package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 本文件收敛"启动失败后仍停在 cancelling"的任务现场。
//
// 背景：取消入口只记录取消请求并把任务推进到 cancelling，真实收敛由执行者确认。启动
// 流程在容器 / 工作区 / 计划阶段失败时会释放执行权，此时任务可能已经因为用户的取消请求
// 变成 cancelling，却没有任何活动 WorkflowRun 能观察到这个请求——既有的 cancelling
// 恢复只在服务启动时扫描一次，进程不重启就再没有人收敛它，任务会永久停在 cancelling：
// 需求无法删除、项目也无法重新领取它。
//
// 规则与既有启动恢复完全一致：复用 domain.DecideCancellationRecovery 与
// convergeCancellingTask，不新增第二套取消判定；收敛只写任务状态与事件，不删除容器、
// 不重放任何远程操作。

// SettleCancelledLaunch 在启动失败后按持久化事实收敛取消现场，返回是否真的收敛。
//
// 调用点必须仍然持有该任务的执行权：那时可以确定没有别的执行者在推进同一个任务，
// 因此"没有活动执行记录"就是可靠结论。两个前提下不改写状态：
//   - 任务不在 cancelling：不需要收口；
//   - 仍有未结束的执行记录：收敛责任在执行者，改写任务状态会覆盖在途结论，因此直接
//     返回，由执行者的取消收敛或进程重启后的启动恢复处理。
func (s *TaskLaunchService) SettleCancelledLaunch(ctx context.Context, taskID domain.ID) bool {
	if s == nil || taskID.IsEmpty() {
		return false
	}
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"读取任务失败，保留取消现场",
			slog.String("task_id", taskID.String()),
			slog.String("error", errorSummary(err)),
		)
		return false
	}
	if task.Status != domain.TaskStatusCancelling {
		return false
	}
	activeRunID, runErr := s.activeCancellationRunID(ctx, taskID)
	if runErr != nil {
		// 读取失败不能当作"没有未结束记录"：那会在没有证据的情况下判定取消已生效。
		s.logger.WarnContext(
			ctx,
			"读取活动执行记录失败，保留取消现场",
			slog.String("task_id", taskID.String()),
			slog.String("error", errorSummary(runErr)),
		)
		return false
	}
	if activeRunID != "" {
		// 仍有未结束的执行记录：收敛责任在执行者，改写任务状态会覆盖在途结论。
		return false
	}
	next := domain.DecideCancellationRecovery(domain.CancellationRecoveryInput{
		CancelRequested: task.CancelRequested,
		ActiveRunID:     activeRunID,
	})
	recovered, convergeErr := convergeCancellingTask(*task, next, s.now())
	if convergeErr != nil {
		s.logger.WarnContext(
			ctx,
			"收敛取消现场失败，保留原状态",
			slog.String("task_id", taskID.String()),
			slog.String("target", string(next)),
			slog.String("error", errorSummary(convergeErr)),
		)
		return false
	}
	updated, writeErr := writeConvergedTask(ctx, s.store, recovered, domain.TaskStatusCancelling)
	if writeErr != nil {
		s.logger.WarnContext(
			ctx,
			"写入取消收敛结果失败",
			slog.String("task_id", taskID.String()),
			slog.String("error", errorSummary(writeErr)),
		)
		return false
	}
	if !updated {
		// 同一次收敛已由其他入口写入：不重复写事件，但现场确实已经收敛。
		return task.Status != next
	}
	eventType := domain.EventTypeTaskCancelled
	if next == domain.TaskStatusRecoveryRequired {
		eventType = domain.EventTypeTaskRecoveryRequired
	}
	s.appendEvent(ctx, taskID, eventType, map[string]any{
		"stage":   "launch_failed",
		"status":  string(next),
		"summary": "启动流程在执行前失败，取消请求已按持久化事实收敛",
	})
	s.logger.InfoContext(
		ctx,
		"启动失败后已收敛取消现场",
		slog.String("task_id", taskID.String()),
		slog.String("status", string(next)),
	)
	return true
}

// activeCancellationRunID 返回任务当前未结束的执行记录标识，没有时返回空字符串。
func (s *TaskLaunchService) activeCancellationRunID(
	ctx context.Context,
	taskID domain.ID,
) (string, error) {
	run, err := s.store.GetActiveWorkflowRun(ctx, taskID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	if run == nil {
		return "", nil
	}
	return run.ID.String(), nil
}
