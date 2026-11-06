package app

import (
	"sync"

	"github.com/lingting/agentflow/internal/domain"
)

// 本文件实现规划会话与规划运行环境的进程内协调机制。
//
// 服务端进程锁保证同一份数据目录只有一个 Server 进程运行，因此"谁在操作这个规划会话"
// 不再是数据库事实：角色调用（RunRole）与运行环境准备/释放（Prepare / Release）必须
// 共用同一个协调器实例，否则同一会话可能在一个入口执行 Pi 时被另一个入口删除容器。
//
// 不同规划会话之间完全隔离，可以并行；协调器不引入跨资源加锁顺序或队列。

// PlanningSessionCoordinator 是同一 Server 进程内按规划会话串行化操作的协调器。
type PlanningSessionCoordinator struct {
	locks keyedMutex
}

// NewPlanningSessionCoordinator 构造规划会话协调器。
func NewPlanningSessionCoordinator() *PlanningSessionCoordinator {
	return &PlanningSessionCoordinator{}
}

// Lock 取得指定规划会话的互斥锁，返回解锁函数。
//
// 同一会话的第二个操作会等待第一个操作结束，而不是立即失败：规划角色调用与运行环境
// 准备/释放是同一份现场上的顺序操作，排队比返回"正被占用"更符合实际。
func (c *PlanningSessionCoordinator) Lock(sessionID domain.ID) func() {
	if c == nil {
		return func() {}
	}
	return c.locks.Lock(sessionID.String())
}

// EnvironmentBuildLocks 是同一 Server 进程内共享的"节点 + 环境"构建互斥集合。
//
// 同一 Server 进程内可能同时存在多个 EnvironmentService 实例（HTTP 后台构建与任务启动
// 中的镜像准备），它们必须共享同一份互斥集合，否则同一环境版本会在本进程内被并发构建，
// 互相覆盖远程构建日志与元数据。
type EnvironmentBuildLocks struct {
	locks keyedMutex
}

// NewEnvironmentBuildLocks 构造环境构建互斥集合。
func NewEnvironmentBuildLocks() *EnvironmentBuildLocks {
	return &EnvironmentBuildLocks{}
}

// Lock 取得指定节点与环境的构建锁，返回解锁函数。
func (l *EnvironmentBuildLocks) Lock(hostID domain.ID, ref domain.EnvironmentRef) func() {
	if l == nil {
		return func() {}
	}
	return l.locks.Lock(lockKey(hostID, ref))
}

// keyedMutex 是按字符串键提供互斥锁的集合。
//
// 它只被进程内协调机制使用：键相同的操作串行，键不同的操作完全并发；一个键没有任何
// 持有者与等待者之后对应条目自动回收，因此长期运行不会让锁表无界增长。
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

// keyedLock 是一个带引用计数的互斥锁。
//
// refs 统计当前持有者与已经登记、正在等待同一把锁的调用者：两者都退出之后条目才可以
// 回收，否则新的调用者可能拿到与等待者不同的锁对象，同一把键就不再互斥。
type keyedLock struct {
	mu   sync.Mutex
	refs int
}

// Lock 取得指定键的互斥锁并返回解锁函数。
//
// 引用计数必须在等待实际互斥锁之前增加：等待者同样占着条目，条目在它退出之前不能被
// 回收，否则后来的调用者会拿到另一把锁，与等待者并发进入同一段临界区。
func (m *keyedMutex) Lock(key string) func() {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = make(map[string]*keyedLock)
	}
	entry, ok := m.locks[key]
	if !ok {
		entry = &keyedLock{}
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
