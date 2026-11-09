package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 本文件实现需求的聚合逻辑删除：需求与它的全部关联任务在同一个事务内软删除。
//
// 边界固定如下：
//   - 删除不自动取消任务：执行中的任务必须先经既有取消入口停止，并等真实收敛；
//   - 删除不清理执行历史：Step / Attempt / Session / Event / CodeSave / 容器记录全部保留；
//   - 删除不回滚 Git，也不处置容器：两者都属于用户的显式动作；
//   - 删除与执行入口的并发由共享 TaskExecutionCoordinator 与事务内条件写入共同收敛，
//     不依赖前端按钮禁用。

// RequirementDeleteResult 是一次需求聚合删除的真实结果。
//
// 三个字段都来自同一个事务：任务数量是本次真正软删除的任务条数，不是删除前列表长度
// 的猜测；删除时间与写进数据库的时间完全一致。
type RequirementDeleteResult struct {
	// Requirement 是本次被逻辑删除的需求，DeletedAt 与 UpdatedAt 已写入。
	Requirement domain.Requirement
	// DeletedTaskCount 是同一事务内被软删除的关联任务数量。
	DeletedTaskCount int
	// DeletedAt 是需求被删除的时间。
	DeletedAt time.Time
}

// Delete 逻辑删除需求与它的全部关联任务。
//
// 处理顺序：
//  1. 事务外读取需求与未删除的关联任务，按任务标识升序取得全部任务的执行权；
//  2. 在单个事务内重新读取需求与任务，核验需求未删除、任务集合未变化；
//  3. 逐个核验任务状态与执行现场（活动 WorkflowRun、未收敛的步骤占用）；
//  4. 逐个条件软删除任务，条件软删除需求，追加删除事件；
//  5. 事务提交后释放全部任务执行权。
//
// 任意一步失败都整体回滚：不会出现"需求已删除但部分任务仍可见"，也不会出现"任务已
// 删除但需求仍可见"。执行权只在事务外持有，用于阻止新的执行入口在删除期间进入；它不是
// 等待在途执行完成的机制——真正在途的任务会让删除返回冲突，由用户先显式停止。
func (s *RequirementService) Delete(
	ctx context.Context,
	requirementIDValue string,
) (RequirementDeleteResult, error) {
	if s.coordination == nil {
		return RequirementDeleteResult{}, fmt.Errorf(
			"%w: 需求删除未装配任务执行协调器",
			ErrRequirementDeleteUnavailable,
		)
	}
	requirement, err := s.readActiveRequirement(ctx, requirementIDValue)
	if err != nil {
		return RequirementDeleteResult{}, err
	}
	tasks, err := s.listRequirementTasks(ctx, s.store, requirement.ID)
	if err != nil {
		return RequirementDeleteResult{}, err
	}
	grants, err := s.lockRequirementTasks(tasks)
	if err != nil {
		return RequirementDeleteResult{}, err
	}
	defer releaseTaskGrants(s.coordination, grants)
	var result RequirementDeleteResult
	err = s.store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		deleted, deleteErr := s.deleteRequirementInTx(ctx, tx, requirement.ID, tasks)
		if deleteErr != nil {
			return deleteErr
		}
		result = deleted
		return nil
	})
	if err != nil {
		return RequirementDeleteResult{}, err
	}
	return result, nil
}

// lockRequirementTasks 按稳定顺序取得全部关联任务的执行权。
//
// 顺序固定为任务标识升序：多任务获取必须有确定性次序，否则两个并发的需求级操作可能以
// 相反顺序取锁并互相阻塞。任意一个任务已被占用时，先释放已经取得的执行权再返回冲突——
// 部分持有会把其他执行入口挡在门外，而本次删除已经不会成功。
func (s *RequirementService) lockRequirementTasks(
	tasks []domain.Task,
) ([]TaskExecutionGrant, error) {
	taskIDs := make([]domain.ID, 0, len(tasks))
	for _, task := range tasks {
		taskIDs = append(taskIDs, task.ID)
	}
	sort.Slice(taskIDs, func(left, right int) bool {
		return taskIDs[left].String() < taskIDs[right].String()
	})
	grants := make([]TaskExecutionGrant, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		grant, acquired := s.coordination.TryAcquire(taskID)
		if !acquired {
			releaseTaskGrants(s.coordination, grants)
			return nil, fmt.Errorf(
				"%w: 任务 %s 正被另一个执行入口推进，请先停止任务并等待其真实收敛",
				ErrRequirementStateConflict,
				taskID,
			)
		}
		grants = append(grants, grant)
	}
	return grants, nil
}

// releaseTaskGrants 释放本次删除取得的全部任务执行权。
//
// 重复释放是幂等的：协调器只在凭据仍持有登记时真正释放。
func releaseTaskGrants(coordination *TaskExecutionCoordinator, grants []TaskExecutionGrant) {
	for _, grant := range grants {
		coordination.Release(grant)
	}
}

// deleteRequirementInTx 在调用方给定的事务内完成全部删除与核验。
//
// 事务内重新读取而不是复用事务外的读取结果：执行权只阻止新的执行入口，任务的取消、
// 单任务删除与其他需求级删除仍可能在这两个时刻之间改动记录。
func (s *RequirementService) deleteRequirementInTx(
	ctx context.Context,
	tx storage.Tx,
	requirementID domain.ID,
	lockedTasks []domain.Task,
) (RequirementDeleteResult, error) {
	requirement, err := readRequirementForDeletion(ctx, tx, requirementID)
	if err != nil {
		return RequirementDeleteResult{}, err
	}
	tasks, err := s.listRequirementTasks(ctx, tx, requirementID)
	if err != nil {
		return RequirementDeleteResult{}, err
	}
	if err := verifyRequirementTaskSet(lockedTasks, tasks); err != nil {
		return RequirementDeleteResult{}, err
	}
	if err := verifyRequirementTasksDeletable(ctx, tx, tasks); err != nil {
		return RequirementDeleteResult{}, err
	}
	at := s.now()
	allowed := domain.RequirementDeletionTaskStatuses()
	for _, task := range tasks {
		if err := tx.DeleteTask(ctx, task.ID, allowed, at); err != nil {
			return RequirementDeleteResult{}, mapRequirementTaskDeletionFailure(err, task.ID)
		}
	}
	if err := requirement.MarkDeleted(at); err != nil {
		return RequirementDeleteResult{}, fmt.Errorf("%w: %w", ErrRequirementStateConflict, err)
	}
	if err := tx.DeleteRequirement(ctx, requirement.ID, at); err != nil {
		return RequirementDeleteResult{}, mapRequirementDeletionFailure(err, requirement.ID)
	}
	for _, task := range tasks {
		if err := appendRequirementDeletedEvent(ctx, tx, task, requirement.ID, at); err != nil {
			return RequirementDeleteResult{}, err
		}
	}
	return RequirementDeleteResult{
		Requirement:      *requirement,
		DeletedTaskCount: len(tasks),
		DeletedAt:        at,
	}, nil
}

// readRequirementForDeletion 在事务内读取待删除需求并核验它仍然可见。
func readRequirementForDeletion(
	ctx context.Context,
	tx storage.Tx,
	requirementID domain.ID,
) (*domain.Requirement, error) {
	requirement, err := tx.GetRequirement(ctx, requirementID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrRequirementNotFound, requirementID)
		}
		return nil, fmt.Errorf("读取需求失败: %w", err)
	}
	if requirement.Deleted() {
		return nil, fmt.Errorf("%w: 需求 %s 已删除", ErrRequirementNotFound, requirementID)
	}
	return requirement, nil
}

// verifyRequirementTaskSet 核验事务内的任务集合与取得执行权时完全一致。
//
// 两个方向都要检查：少了任务说明有并发单任务删除，多了任务说明任务集合被其他路径改动。
// 两种情况下本次删除取得的执行权都不再覆盖真实任务集合，必须回滚并让调用方重新读取。
func verifyRequirementTaskSet(lockedTasks []domain.Task, currentTasks []domain.Task) error {
	lockedIDs := make([]string, 0, len(lockedTasks))
	for _, task := range lockedTasks {
		lockedIDs = append(lockedIDs, task.ID.String())
	}
	currentIDs := make([]string, 0, len(currentTasks))
	for _, task := range currentTasks {
		currentIDs = append(currentIDs, task.ID.String())
	}
	sort.Strings(lockedIDs)
	sort.Strings(currentIDs)
	if slices.Equal(lockedIDs, currentIDs) {
		return nil
	}
	return fmt.Errorf(
		"%w: 需求的任务集合已变化，请重新读取后再删除",
		ErrRequirementStateConflict,
	)
}

// verifyRequirementTasksDeletable 核验全部任务都能随需求一起软删除。
//
// 任一任务不满足条件就整体拒绝：部分删除会把一个需求拆成"已删除"和"仍可见"两半。
func verifyRequirementTasksDeletable(
	ctx context.Context,
	tx storage.Tx,
	tasks []domain.Task,
) error {
	for _, task := range tasks {
		if !task.AllowsRequirementDeletion() {
			return fmt.Errorf(
				"%w: 任务 %s 处于 %s，需要先停止任务并等待真实收敛后才能删除需求",
				ErrRequirementStateConflict,
				task.ID,
				task.Status,
			)
		}
		if err := verifyTaskExecutionSettled(ctx, tx, task); err != nil {
			return err
		}
	}
	return nil
}

// verifyTaskExecutionSettled 核验单个任务的执行现场能否确认已经结束。
//
// 任务状态是必要条件而不是充分条件，因此逐项核验三类执行事实：
//   - 活动 WorkflowRun：说明编排仍在推进；
//   - 未收敛的步骤：`running` / `developing` / `verifying` / `recovery_required`；
//   - 结果不明的代码保存副作用：复用 domain.DecideTaskCodeSaveResume，与队列准入同一规则，
//     不重新实现 commit / push / merge 的核验判定。
//
// 状态显示终态但上述任一事实仍存在时，现场与状态矛盾，必须拒绝删除；读取失败按"无法确认"
// 处理，不得当作"没有执行"。
func verifyTaskExecutionSettled(
	ctx context.Context,
	tx storage.Tx,
	task domain.Task,
) error {
	active, err := tx.GetActiveWorkflowRun(ctx, task.ID)
	switch {
	case err == nil:
		return fmt.Errorf(
			"%w: 任务 %s 仍有活动的执行记录 %s（状态 %s），现场未收敛",
			ErrRequirementStateConflict,
			task.ID,
			active.ID,
			active.Status,
		)
	case !errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("读取任务 %s 的活动执行记录失败: %w", task.ID, err)
	}
	steps, err := tx.ListStepsByTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("读取任务 %s 的步骤失败: %w", task.ID, err)
	}
	for _, step := range steps {
		if !step.Status.RequiresManualVerification() {
			continue
		}
		return fmt.Errorf(
			"%w: 任务 %s 的步骤 %s 停在 %s，现场不能确认已结束",
			ErrRequirementStateConflict,
			task.ID,
			step.ID,
			step.Status,
		)
	}
	records, err := tx.ListCodeSaveRecordsByTask(ctx, task.ID.String())
	if err != nil {
		return fmt.Errorf("读取任务 %s 的代码保存记录失败: %w", task.ID, err)
	}
	if len(records) > 0 {
		decision := domain.DecideTaskCodeSaveResume(records)
		if decision.Action != domain.CodeSaveResumeProceed {
			return fmt.Errorf(
				"%w: 任务 %s 存在结果不明的外部副作用（%s），必须先完成核验",
				ErrRequirementStateConflict,
				task.ID,
				decision.Summary,
			)
		}
	}
	return nil
}

// appendRequirementDeletedEvent 在任务时间线上追加需求删除事件。
//
// 事件是任务时间线，没有独立的需求时间线，因此需求删除写进它每一个关联任务，并在内容
// 里带上 `requirement_id`。事件与软删除位于同一个事务：写不成就不报告删除成功。
func appendRequirementDeletedEvent(
	ctx context.Context,
	tx storage.Tx,
	task domain.Task,
	requirementID domain.ID,
	at time.Time,
) error {
	payload, err := json.Marshal(map[string]any{
		"requirement_id": requirementID.String(),
		"task_id":        task.ID.String(),
		"task_status":    string(task.Status),
	})
	if err != nil {
		return fmt.Errorf("构造需求删除事件失败: %w", err)
	}
	event, err := domain.NewEvent(
		task.ID,
		nil,
		nil,
		nil,
		domain.EventTypeRequirementDeleted,
		payload,
		at,
	)
	if err != nil {
		return fmt.Errorf("记录需求删除事件失败: %w", err)
	}
	if _, err := tx.AppendEvent(ctx, event); err != nil {
		return fmt.Errorf("记录需求删除事件失败: %w", err)
	}
	return nil
}

// mapRequirementDeletionFailure 把需求条件软删除的失败映射为需求业务错误。
func mapRequirementDeletionFailure(err error, requirementID domain.ID) error {
	switch {
	case errors.Is(err, storage.ErrConflict):
		return fmt.Errorf(
			"%w: 需求 %s 已被删除或状态已改变",
			ErrRequirementStateConflict,
			requirementID,
		)
	case errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("%w: %s", ErrRequirementNotFound, requirementID)
	default:
		return fmt.Errorf("删除需求失败: %w", err)
	}
}

// mapRequirementTaskDeletionFailure 把任务条件软删除的失败映射为需求业务错误。
//
// 影响行数为 0 意味着任务已被并发删除或状态已经改变：两种情况都不能继续删除需求，
// 否则剩下的记录会与需求状态不一致。
func mapRequirementTaskDeletionFailure(err error, taskID domain.ID) error {
	if errors.Is(err, storage.ErrConflict) {
		return fmt.Errorf(
			"%w: 任务 %s 已被删除或当前状态不允许删除",
			ErrRequirementStateConflict,
			taskID,
		)
	}
	return fmt.Errorf("删除需求内任务 %s 失败: %w", taskID, err)
}
