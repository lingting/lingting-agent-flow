package storage

import (
	"context"

	"github.com/lingting/agentflow/internal/domain"
)

// WorkflowRunStore 保存任务级工作流执行的进度记录。
//
// 记录是编排进度的唯一真相来源：服务重启后据此判断停在哪一步，而不是靠进程内状态。
// 状态写入一律以版本号为前置条件，迟到的结果不得覆盖已确认的终态。
type WorkflowRunStore interface {
	// CreateWorkflowRun 写入一条新的执行记录。
	//
	// 同一任务已存在未结束的执行记录时返回 ErrConflict：唯一性由数据库的部分唯一
	// 索引保证，不是"先查询再插入"。
	CreateWorkflowRun(ctx context.Context, run domain.WorkflowRun) error
	// UpdateWorkflowRun 以版本号为条件写入执行记录。
	//
	// 版本号不匹配时返回 ErrConflict：恢复不得覆盖较新的状态。
	UpdateWorkflowRun(ctx context.Context, run domain.WorkflowRun) error
	// GetWorkflowRun 读取单条执行记录。
	GetWorkflowRun(ctx context.Context, id domain.ID) (*domain.WorkflowRun, error)
	// GetActiveWorkflowRun 返回任务当前未结束的执行记录。
	//
	// 没有未结束记录时返回 ErrNotFound。
	GetActiveWorkflowRun(ctx context.Context, taskID domain.ID) (*domain.WorkflowRun, error)
	// ListWorkflowRunsByTask 按创建时间倒序返回任务的执行记录。
	ListWorkflowRunsByTask(ctx context.Context, taskID domain.ID) ([]domain.WorkflowRun, error)
	// ListUnfinishedWorkflowRuns 返回全部未结束的执行记录，用于服务启动恢复。
	ListUnfinishedWorkflowRuns(ctx context.Context) ([]domain.WorkflowRun, error)
}
