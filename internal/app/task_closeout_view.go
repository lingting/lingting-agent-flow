package app

import (
	"context"
	"fmt"

	"github.com/lingting/agentflow/internal/domain"
)

// TaskCloseoutViews 返回一批任务"结论是否闭环"的派生结论。
//
// 任务列表与详情必须能表达"结论存疑"：任务状态可能是 completed，而执行记录的收尾
// 写入曾经失败，此时调用方不能把它当作确定成功。派生规则集中在
// domain.DecideTaskCloseoutView，本方法只负责读取执行记录。
//
// 读取失败让整个查询失败：无法判断是否闭环时返回"看起来正常"的结果，会把不确定
// 现场伪装成成功。
func (s *TaskService) TaskCloseoutViews(
	ctx context.Context,
	tasks []domain.Task,
) (map[domain.ID]domain.TaskCloseoutState, error) {
	views := make(map[domain.ID]domain.TaskCloseoutState, len(tasks))
	if len(tasks) == 0 {
		return views, nil
	}
	now := s.now()
	for _, task := range tasks {
		runs, err := s.store.ListWorkflowRunsByTask(ctx, task.ID)
		if err != nil {
			return nil, fmt.Errorf("读取任务 %s 的工作流执行记录失败: %w", task.ID, err)
		}
		var latest *domain.WorkflowRun
		if len(runs) > 0 {
			value := runs[0]
			latest = &value
		}
		views[task.ID] = domain.DecideTaskCloseoutView(domain.TaskCloseoutViewInput{
			Task: task,
			Run:  latest,
			Now:  now,
		})
	}
	return views, nil
}
