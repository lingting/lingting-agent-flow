package storage

import (
	"context"
	"time"

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

// WorkflowRunLeaseRenewRequest 是一次"延长执行权"的输入。
//
// 它只描述续租需要的最小事实：续租既不改变执行状态，也不推进版本号，因此不能让
// 调用方顺带修改其他字段。
type WorkflowRunLeaseRenewRequest struct {
	// RunID 是待续租的执行记录。
	RunID domain.ID
	// WorkerID 是执行者标识，必须与数据库中的值一致。
	//
	// 不一致说明执行权已经转移给其他进程，此时续租必须失败而不是覆盖新执行者。
	WorkerID string
	// LeaseExpiresAt 是新的租约到期时间。
	LeaseExpiresAt time.Time
	// At 是本次续租的时间，零值使用当前时间。
	At time.Time
}

// WorkflowRunLeaseStore 提供执行权的延长能力。
//
// 它与 WorkflowRunStore 分开：续租是一条只改租约字段的条件更新，不参与状态机、
// 不推进版本号，因此不能与整体写入混为一谈。
type WorkflowRunLeaseStore interface {
	// RenewWorkflowRunLease 在"仍是同一个执行者且仍在 running"时延长租约。
	//
	// 记录不存在、状态不是 running 或执行者不匹配时返回 ErrConflict：
	// 已停止、已取消或已完成的记录不得被后台心跳重新延长租约。
	RenewWorkflowRunLease(ctx context.Context, request WorkflowRunLeaseRenewRequest) error
}
