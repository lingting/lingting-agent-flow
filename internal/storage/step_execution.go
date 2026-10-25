package storage

import (
	"context"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// StepExecutionStartRequest 是一次"占用步骤并创建执行尝试"的输入。
//
// 只包含调用方已经确认的信息：尝试序号由存储层在同一事务内分配，容器与摘要由
// 服务层在调用前从已核验的记录中取得。
type StepExecutionStartRequest struct {
	// TaskID 是任务标识，必须与步骤所属任务一致。
	TaskID domain.ID
	// StepID 是待执行的步骤。
	StepID domain.ID
	// ExpectedTaskStatus 是占用时数据库中任务必须处于的状态。
	//
	// 它由调用方按业务规则决定（通常是读取到的当前状态），存储层只做条件校验，
	// 不解释状态语义。
	ExpectedTaskStatus domain.TaskStatus
	// ExpectedStepStatus 是占用时数据库中步骤必须处于的状态。
	ExpectedStepStatus domain.StepStatus
	// ContainerID 是本次执行使用的容器登记记录 ID，本机执行为空。
	ContainerID string
	// DockerID 是本次执行使用的 Docker 容器 ID，本机执行为空。
	DockerID string
	// InputDigest 是本次尝试输入配置的摘要。
	InputDigest string
	// BudgetScope 是本次尝试消耗的预算作用域。
	BudgetScope domain.RepairBudgetScope
	// BudgetScopeID 是本次尝试所属的执行周期标识。
	//
	// 它由应用层在占用之前从已登记的周期读取：同一逻辑周期的恢复必须复用同一个
	// 标识，因此重启后的预算统计与退出前一致。
	BudgetScopeID domain.ID
	// Lease 是本次执行者持有的任务租约身份。
	//
	// 它非空时，事务内的任务写入附带 owner + epoch 条件：旧代次的执行者无法在
	// 任务已被接管之后占用步骤或推进任务状态。为空表示本次调用没有任务执行权
	// （当前只用于历史调用路径），此时只能用状态条件写入。
	Lease *domain.TaskLeaseHandle
	// At 是占用与尝试的开始时间，零值使用当前时间。
	At time.Time
}

// StepExecutionStart 是一次成功占用的结果。
type StepExecutionStart struct {
	// Attempt 是新建的执行尝试，状态为 running。
	Attempt domain.Attempt
	// Task 是写入后的任务，状态已推进并指向新的步骤与尝试。
	Task domain.Task
	// Step 是写入后的步骤，状态已推进并被新的尝试占用。
	Step domain.Step
}

// StepExecutionStore 提供单步骤执行的原子占用能力。
//
// 占用必须在一个事务内完成，并且每一步都以状态作为前置条件：只有数据库中的
// 任务与步骤仍处于调用方读到的状态时才写入，否则返回 ErrConflict。
// 这样并发请求不会各自创建一条"正在运行"的尝试，进程内互斥锁也不承担一致性职责。
type StepExecutionStore interface {
	// BeginStepExecution 占用步骤、创建尝试，并推进任务与步骤状态。
	BeginStepExecution(
		ctx context.Context,
		request StepExecutionStartRequest,
	) (StepExecutionStart, error)
}
