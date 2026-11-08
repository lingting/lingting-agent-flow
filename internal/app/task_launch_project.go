package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 本文件实现"同一项目同时最多一个任务进入执行"在显式启动入口上的核验。
//
// 自动分派路径的串行由项目执行权（ProjectExecutionService 的闸门）保证：只有持有有效
// 执行权且位于项目队首的任务才会被调度器领取。显式启动入口（用户直接对某个任务点开始、
// 或既有重试入口复用统一启动）不经过调度器，因此需要在启动流程内部再核验一次项目内是否
// 已有在途任务，否则同一项目会出现两个并行执行的任务。
//
// 该核验只做一次只读查询，不取得项目执行权，也不改变任何任务状态：它拒绝的是"注定冲突"
// 的启动，而不是承担项目级调度职责。

// taskLaunchProjectStore 是判断"同项目是否已有任务在执行"所需的只读能力。
//
// 它单独声明而不并入 TaskLaunchStore：项目归属只有一条路径（需求的项目关联），而
// TaskLaunchStore 的每个方法都是启动流程必需的事实来源。生产存储同时满足两者；只提供
// 任务级事实的存储不会因为这条可选核验而被迫实现项目能力。
type taskLaunchProjectStore interface {
	GetRequirement(ctx context.Context, id domain.ID) (*domain.Requirement, error)
	ListProjectTaskFacts(ctx context.Context, projectID domain.ID) ([]storage.ProjectTaskFact, error)
}

// checkProjectSerialization 核验同一项目内没有其他任务仍在执行。
//
// 规则：
//   - 任务没有项目关联（历史手工任务）→ 不受约束，保持既有行为；
//   - 同项目内存在其他仍在执行的任务 → 拒绝本次启动，并指出正在执行的任务；
//   - 读取失败 → 拒绝启动（无法判断时不得制造同项目并行执行）。
//
// 它不判断任务顺序，也不判断是否持有项目执行权：顺序与执行权属于自动分派，显式启动
// 由用户决定"现在开始这个任务"，两者职责不同。
func (s *TaskLaunchService) checkProjectSerialization(ctx context.Context, task domain.Task) error {
	projectStore, ok := s.store.(taskLaunchProjectStore)
	if !ok {
		// 存储不提供项目事实：没有项目归属可以判定，保持既有行为。
		return nil
	}
	requirement, err := projectStore.GetRequirement(ctx, task.RequirementID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// 需求缺失时由启动前置条件按既有规则拒绝，这里不重复判定。
			return nil
		}
		return fmt.Errorf("%w: 读取任务 %s 的需求失败: %w", ErrTaskLaunchPrerequisite, task.ID, err)
	}
	projectID := domain.ID(requirement.ProjectID)
	if projectID.IsEmpty() {
		return nil
	}
	facts, err := projectStore.ListProjectTaskFacts(ctx, projectID)
	if err != nil {
		return fmt.Errorf(
			"%w: 读取项目 %s 的任务失败: %w",
			ErrTaskLaunchPrerequisite,
			projectID,
			err,
		)
	}
	for _, fact := range facts {
		if fact.Task.ID == task.ID {
			continue
		}
		if !fact.Task.Status.IsActive() {
			continue
		}
		return fmt.Errorf(
			"%w: 项目 %s 内已有任务 %s 正在执行（状态 %s），同一项目同时只允许一个任务执行",
			ErrTaskLaunchConflict,
			projectID,
			fact.Task.ID,
			fact.Task.Status,
		)
	}
	return nil
}
