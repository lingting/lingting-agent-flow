package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// RecoverInterrupted 收敛进程退出留下的工作区初始化残留。
//
// 停留在 pending / running 的记录只说明"曾经有一次初始化拿到了占用"。进程在容器内
// Git 操作期间退出时，记录会永久留在数据库里，而占用本身会阻止同一任务重新初始化，
// 使任务既无法推进也无法自动收敛。
//
// 本方法只把这类记录收敛为 uncertain，等待只读核验：
//   - 不重放任何代码获取，不重新 Clone；
//   - 不删除容器与工作区，现场一律保留；
//   - 不改写已确认的终态，所有写入都带状态前置条件。
//
// 判定规则集中在 domain.DecideTaskWorkspaceRecovery，本方法不复制第二套规则。
func (s *TaskWorkspaceService) RecoverInterrupted(ctx context.Context) error {
	if s.store == nil {
		return nil
	}
	records, err := s.store.ListInterruptedTaskWorkspaceInits(ctx)
	if err != nil {
		return fmt.Errorf("读取中断的工作区初始化记录失败: %w", err)
	}
	for _, record := range records {
		if err := s.recoverInterrupted(ctx, record); err != nil {
			return fmt.Errorf(
				"收敛任务 %s 的工作区初始化记录 %s 失败: %w",
				record.TaskID,
				record.ID,
				err,
			)
		}
	}
	return nil
}

// recoverInterrupted 把一条中断的初始化记录收敛为需要核验现场。
//
// 写入带状态前置条件：状态已被其他进程推进时按"同一次收敛已完成"处理，不重复写入
// 事件，也不把本地对象覆盖到数据库。
func (s *TaskWorkspaceService) recoverInterrupted(
	ctx context.Context,
	record domain.TaskWorkspaceInitRecord,
) error {
	needed, next := domain.DecideTaskWorkspaceRecovery(domain.TaskWorkspaceRecoveryInput{
		Record: record,
	})
	if !needed {
		return nil
	}
	previous := record.Status
	if err := record.MarkFailed(
		next,
		domain.CodeFetchStagePersist,
		"interrupted",
		domain.TaskWorkspaceInterruptedSummary(),
		s.now(),
	); err != nil {
		return err
	}
	writeCtx := context.WithoutCancel(ctx)
	if err := s.store.UpdateTaskWorkspaceInit(writeCtx, record); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			// 另一个进程已经推进了该记录：不是失败，也不重复写入事件。
			return nil
		}
		return err
	}
	s.appendRecoveryEvent(writeCtx, record, previous, next)
	return nil
}

// appendRecoveryEvent 记录一次中断收敛事件。
//
// 事件写入失败只记录日志：它不能替代已经落库的收敛结论。
func (s *TaskWorkspaceService) appendRecoveryEvent(
	ctx context.Context,
	record domain.TaskWorkspaceInitRecord,
	previous, next domain.TaskWorkspaceInitStatus,
) {
	taskID := domain.ID(record.TaskID)
	event, err := domain.NewEvent(
		taskID,
		nil,
		nil,
		nil,
		domain.EventTypeTaskRecoveryRequired,
		marshalStepExecutionPayload(map[string]any{
			"stage":         "recover_task_workspace",
			"record_id":     record.ID.String(),
			"container_id":  record.ContainerID,
			"previous":      previous.String(),
			"status":        next.String(),
			"summary":       record.ErrorSummary,
			"recovery_auto": true,
		}),
		s.now(),
	)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"构造工作区初始化收敛事件失败",
			slog.String("task_id", record.TaskID),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	if _, err := s.store.AppendEvent(ctx, event); err != nil {
		s.logger.WarnContext(
			ctx,
			"写入工作区初始化收敛事件失败",
			slog.String("task_id", record.TaskID),
			slog.String("record_id", record.ID.String()),
			slog.String("error", errorSummary(err)),
		)
	}
}
