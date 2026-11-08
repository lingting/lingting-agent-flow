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

// 本文件是项目执行协调的读取与投影部分。
//
// 它只做"读持久化事实并交给领域层"这件事：项目、执行意图、执行权与项目任务的读取都在
// 这里，状态判定一律复用 domain.DecideProjectExecution / DecideProjectExecutionPlan，
// 不在应用层重写顺序、阻塞或进度规则。
//
// 写入路径（取得 / 续租 / 释放执行权、写执行意图）分别位于 project_execution.go 与
// project_execution_claim.go，两个文件共享同一份读取函数。
// parseProjectID 规范化并校验项目标识。
func (s *ProjectExecutionService) parseProjectID(projectIDValue string) (domain.ID, error) {
	trimmed := strings.ToLower(strings.TrimSpace(projectIDValue))
	if err := domain.RequireProjectID(trimmed); err != nil {
		return "", fmt.Errorf("%w: %w", ErrProjectExecutionInvalidInput, err)
	}
	return domain.ID(trimmed), nil
}

// loadProject 读取项目与其执行事实。
func (s *ProjectExecutionService) loadProject(
	ctx context.Context,
	projectID domain.ID,
) (domain.Project, []storage.ProjectTaskFact, int, error) {
	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Project{}, nil, 0, fmt.Errorf(
				"%w: %s",
				ErrProjectExecutionNotFound,
				projectID,
			)
		}
		return domain.Project{}, nil, 0, fmt.Errorf("读取项目失败: %w", err)
	}
	facts, err := s.store.ListProjectTaskFacts(ctx, projectID)
	if err != nil {
		return domain.Project{}, nil, 0, fmt.Errorf("读取项目任务失败: %w", err)
	}
	requirements, err := s.store.ListRequirements(
		ctx,
		storage.RequirementFilter{ProjectID: projectID.String()},
	)
	if err != nil {
		return domain.Project{}, nil, 0, fmt.Errorf("读取项目需求失败: %w", err)
	}
	return *project, facts, len(requirements), nil
}

// loadExecutionState 读取项目执行意图，没有记录时返回空值。
func (s *ProjectExecutionService) loadExecutionState(
	ctx context.Context,
	projectID domain.ID,
) (*storage.ProjectExecutionState, error) {
	state, err := s.store.GetProjectExecutionState(ctx, projectID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return &storage.ProjectExecutionState{ProjectID: projectID}, nil
		}
		return nil, fmt.Errorf("读取项目执行意图失败: %w", err)
	}
	return state, nil
}

// loadLease 读取项目执行权，没有登记时返回空值。
func (s *ProjectExecutionService) loadLease(
	ctx context.Context,
	projectID domain.ID,
) (*domain.ProjectExecutionLease, error) {
	lease, err := s.store.GetProjectExecutionLease(ctx, projectID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取项目执行权失败: %w", err)
	}
	return lease, nil
}

// executionOf 把持久化事实转换为项目执行结果。
func (s *ProjectExecutionService) executionOf(
	ctx context.Context,
	project domain.Project,
	requirementCount int,
	facts []storage.ProjectTaskFact,
	lease *domain.ProjectExecutionLease,
	state *storage.ProjectExecutionState,
	now time.Time,
) (ProjectExecution, error) {
	if err := ctx.Err(); err != nil {
		return ProjectExecution{}, err
	}
	stopRequested := state != nil && state.StopRequested()
	progress := domain.DecideProjectExecution(domain.ProjectExecutionInput{
		ProjectID:     domain.ID(project.ID),
		Tasks:         domainFacts(facts),
		Requirements:  requirementCount,
		Lease:         lease,
		Now:           now,
		StopRequested: stopRequested,
	})
	return ProjectExecution{
		Project:  project,
		Progress: progress,
		Tasks:    s.taskViews(facts),
		State:    state,
	}, nil
}

// taskViews 按项目内执行顺序构造任务视图。
func (s *ProjectExecutionService) taskViews(facts []storage.ProjectTaskFact) []ProjectTaskView {
	plan := domain.DecideProjectExecutionPlan(domainFacts(facts))
	views := make([]ProjectTaskView, 0, len(plan.Tasks))
	for index, fact := range plan.Tasks {
		view := ProjectTaskView{
			Task:        fact.Task,
			Requirement: fact.Requirement,
			Position:    index + 1,
		}
		if plan.BlockedTaskID != nil && *plan.BlockedTaskID == fact.Task.ID {
			view.Blocking = true
			view.BlockingReason = plan.BlockedReason
		}
		views = append(views, view)
	}
	return views
}

// domainFacts 把存储层事实转换为领域事实。
func domainFacts(facts []storage.ProjectTaskFact) []domain.ProjectTaskFact {
	converted := make([]domain.ProjectTaskFact, 0, len(facts))
	for _, fact := range facts {
		converted = append(converted, domain.ProjectTaskFact{
			Requirement: fact.Requirement,
			Task:        fact.Task,
		})
	}
	return converted
}

// firstActiveTask 返回计划中第一个仍在执行的任务标识。
func firstActiveTask(plan domain.ProjectExecutionPlan) *domain.ID {
	for _, fact := range plan.Tasks {
		if fact.Task.Status.IsActive() {
			taskID := fact.Task.ID
			return &taskID
		}
	}
	return nil
}

// taskIDText 把可空任务标识转换为可读文本。
func taskIDText(id *domain.ID) string {
	if id == nil {
		return "未知任务"
	}
	return id.String()
}
