package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// 本文件实现任务执行的进程内协调机制。
//
// 服务端进程锁保证同一份数据目录只有一个 Server 进程运行，因此"谁在执行这个任务"
// 不再是数据库事实，而是进程内的在途执行登记：调度器自动分派、显式启动、显式重试、
// 单步骤执行、单步骤闭环与任务级工作流入口都必须使用同一个协调器实例，否则同一个
// 任务可能被两个入口同时推进。
//
// 协调器只保证单进程内的互斥：不同任务之间完全并发，同一任务上的执行串行化。它不
// 替代数据库的条件更新与事务边界，那些仍然是并发写入正确性的基础。

// keyedTaskMutex 按任务标识提供互斥锁。
//
// 不同任务之间完全并发，同一任务上的执行串行化；无引用时自动回收条目，避免长时间
// 运行后无限增长。
type keyedTaskMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedTaskLock
}

// keyedTaskLock 是一个带引用计数的互斥锁。
type keyedTaskLock struct {
	mu   sync.Mutex
	refs int
}

// Lock 取得指定任务的锁，返回解锁函数。
func (m *keyedTaskMutex) Lock(key string) func() {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = make(map[string]*keyedTaskLock)
	}
	entry, ok := m.locks[key]
	if !ok {
		entry = &keyedTaskLock{}
		m.locks[key] = entry
	}
	entry.refs++
	m.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		m.mu.Lock()
		entry.refs--
		if entry.refs <= 0 {
			delete(m.locks, key)
		}
		m.mu.Unlock()
	}
}

// tryLock 非阻塞地取得任务级互斥锁。
//
// 它用于在产生副作用之前拒绝重复请求：并发入口不会各自写入一条运行记录。
func (m *keyedTaskMutex) tryLock(key string) (func(), bool) {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = make(map[string]*keyedTaskLock)
	}
	entry, ok := m.locks[key]
	if !ok {
		entry = &keyedTaskLock{}
		m.locks[key] = entry
	}
	if entry.refs > 0 {
		// 已有持有者：不等待，直接拒绝，避免 HTTP 请求长期阻塞。
		m.mu.Unlock()
		return nil, false
	}
	entry.refs++
	m.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		m.mu.Lock()
		entry.refs--
		if entry.refs <= 0 {
			delete(m.locks, key)
		}
		m.mu.Unlock()
	}, true
}

// lockWithin 在限定时间内取得任务级互斥锁。
//
// 它用于"恢复"这类可能与上一次执行收尾重叠的请求：状态已经落盘为 blocked 时，上一次
// 推进可能仍在释放锁，短暂等待比直接报冲突更符合用户预期；超过时限仍返回冲突。
func (m *keyedTaskMutex) lockWithin(
	ctx context.Context,
	key string,
	timeout time.Duration,
) (func(), bool) {
	deadline := time.Now().Add(timeout)
	for {
		if unlock, ok := m.tryLock(key); ok {
			return unlock, true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, false
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TaskExecutionCoordinator 是同一 Server 进程内按任务串行化执行入口的协调器。
//
// 它只有一个实例，由 Server 装配后注入所有可能推进任务的入口；各自持有互不共享的
// 锁表会让"互斥"失效。
type TaskExecutionCoordinator struct {
	locks keyedTaskMutex
}

// NewTaskExecutionCoordinator 构造任务执行协调器。
func NewTaskExecutionCoordinator() *TaskExecutionCoordinator {
	return &TaskExecutionCoordinator{}
}

// TaskExecutionGrant 是一次在途任务执行的进程内凭据。
//
// 它只在应用层内部传递，不参与任何请求结构体解析：HTTP 与 CLI 调用方不能构造它。
// 凭据可以被复制（按值传递），但只有同一份令牌，因此"是否仍持有执行权"的判定与
// 释放都指向同一次登记。
type TaskExecutionGrant struct {
	token *taskExecutionToken
}

// taskExecutionToken 是一次在途执行登记的令牌。
type taskExecutionToken struct {
	taskID domain.ID
	// release 释放协调器中的登记。
	release func()
	mu      sync.Mutex
	// released 表示本次登记已经释放，重复释放是幂等的。
	released bool
}

// TaskID 返回凭据绑定的任务标识，凭据为空时返回空标识。
func (g TaskExecutionGrant) TaskID() domain.ID {
	if g.token == nil {
		return ""
	}
	return g.token.taskID
}

// Valid 判断凭据是否对应一次真实的在途执行登记。
func (g TaskExecutionGrant) Valid() bool {
	return g.token != nil && g.token.taskID != ""
}

// Held 判断本次凭据是否仍持有任务执行权。
//
// 它只读进程内登记，不读数据库：失去执行权之后继续产生的副作用与状态写入必须由
// 调用方自行停止，因此每次有副作用的推进之前都要用它核验。
func (g TaskExecutionGrant) Held() bool {
	if g.token == nil {
		return false
	}
	g.token.mu.Lock()
	defer g.token.mu.Unlock()
	return !g.token.released
}

// TryAcquire 非阻塞地取得指定任务的执行权。
//
// 返回 false 表示该任务已有另一个入口在推进：调用方必须按"正在执行"处理，不得排队
// 等待，也不得继续产生副作用。
func (c *TaskExecutionCoordinator) TryAcquire(taskID domain.ID) (TaskExecutionGrant, bool) {
	if err := taskID.Validate(); err != nil {
		return TaskExecutionGrant{}, false
	}
	unlock, ok := c.locks.tryLock(taskID.String())
	if !ok {
		return TaskExecutionGrant{}, false
	}
	return newTaskExecutionGrant(taskID, unlock), true
}

// AcquireWithin 在限定时间内等待指定任务的执行权。
//
// 它只用于用户显式恢复：上一次推进可能正在收尾，短暂等待比直接报冲突更符合预期。
func (c *TaskExecutionCoordinator) AcquireWithin(
	ctx context.Context,
	taskID domain.ID,
	timeout time.Duration,
) (TaskExecutionGrant, bool) {
	if err := taskID.Validate(); err != nil {
		return TaskExecutionGrant{}, false
	}
	unlock, ok := c.locks.lockWithin(ctx, taskID.String(), timeout)
	if !ok {
		return TaskExecutionGrant{}, false
	}
	return newTaskExecutionGrant(taskID, unlock), true
}

// Held 判断指定任务当前是否有在途执行。
func (c *TaskExecutionCoordinator) Held(taskID domain.ID) bool {
	if c == nil {
		return false
	}
	c.locks.mu.Lock()
	defer c.locks.mu.Unlock()
	entry, ok := c.locks.locks[taskID.String()]
	return ok && entry.refs > 0
}

// Release 释放一次在途执行登记。
//
// 重复释放是幂等的；返回 false 表示本次调用没有真正释放（凭据为空或已经释放过）。
func (c *TaskExecutionCoordinator) Release(grant TaskExecutionGrant) bool {
	if grant.token == nil {
		return false
	}
	token := grant.token
	token.mu.Lock()
	if token.released {
		token.mu.Unlock()
		return false
	}
	token.released = true
	token.mu.Unlock()
	if token.release != nil {
		token.release()
	}
	return true
}

// newTaskExecutionGrant 用协调器返回的解锁函数构造凭据。
func newTaskExecutionGrant(taskID domain.ID, unlock func()) TaskExecutionGrant {
	return TaskExecutionGrant{
		token: &taskExecutionToken{taskID: taskID, release: unlock},
	}
}

// acquireTaskGrant 返回本次操作使用的执行凭据，并在需要时由服务自行取得执行权。
//
// provided 非空时必须属于同一任务：调用方已经持有了执行权（例如调度器、启动流程或
// 编排器），服务不得再申请第二个登记。返回的 ownsGrant 表示本次调用是否真的取得了
// 执行权，取值为 true 时调用方必须在结束时调用 Release。
func acquireTaskGrant(
	coordination *TaskExecutionCoordinator,
	taskID domain.ID,
	provided *TaskExecutionGrant,
	conflict error,
) (TaskExecutionGrant, bool, error) {
	if provided != nil {
		if !provided.Valid() {
			return TaskExecutionGrant{}, false, fmt.Errorf(
				"%w: 本次调用没有任务执行凭据",
				conflict,
			)
		}
		if provided.TaskID() != taskID {
			return TaskExecutionGrant{}, false, fmt.Errorf(
				"%w: 执行权属于任务 %s，不能用于任务 %s",
				conflict,
				provided.TaskID(),
				taskID,
			)
		}
		return *provided, false, nil
	}
	if coordination == nil {
		return TaskExecutionGrant{}, false, fmt.Errorf(
			"%w: 本次运行未装配任务执行协调器",
			conflict,
		)
	}
	acquired, ok := coordination.TryAcquire(taskID)
	if !ok {
		return TaskExecutionGrant{}, false, taskExecutionConflict(conflict, taskID)
	}
	return acquired, true, nil
}

// taskExecutionConflict 返回"任务正被另一个执行入口推进"的标准错误。
//
// 各个服务把冲突翻译成自己的错误类别，因此这里只提供统一的说明文本。
func taskExecutionConflict(kind error, taskID domain.ID) error {
	return fmt.Errorf("%w: 任务 %s 正被另一个执行入口推进", kind, taskID)
}
