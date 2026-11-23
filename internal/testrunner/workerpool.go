package testrunner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// 本文件是 V4 R3 的编排进程侧 worker 池：槽位、常驻 worker 的复用、独占用例的一次性
// worker，以及 worker 池的观测统计。
//
// 并发仍然只由调度器决定（W1）：槽位是执行位，调度器把"槽位空闲 + 额度允许"的用例派给
// 槽位，槽位只负责把这条用例交给自己的 worker 并按回传结论交回结果。

// caseObservation 是编排进程执行一条用例时观测到的 worker 事实，只进入报告统计。
type caseObservation struct {
	// Launched 表示这次执行新启动了 worker 进程。
	Launched bool
	// Reused 表示这次执行复用了已经跑过用例的 worker 进程。
	Reused bool
	// Isolated 表示这次执行使用了独占用例的一次性 worker。
	Isolated bool
	// Restarted 表示该 worker 进程没有按协议收尾（崩溃、被污染或派发前已退出）。
	Restarted bool
}

// slotAssignment 是调度器派发给槽位的一条用例。
type slotAssignment struct {
	index    int
	Case     Case
	reserved reservation
}

// slotOutcome 是槽位完成一条用例后的回传。
type slotOutcome struct {
	slot     int
	index    int
	reserved reservation
	result   CaseResult
	facts    caseObservation
	// alive 表示槽位在执行完之后仍然持有一条可复用的 worker 进程。
	alive bool
}

// slotTable 记录槽位占用与进程存活状态，只有调度线程访问。
type slotTable struct {
	free  []bool
	alive []bool
}

// newSlotTable 构造全部空闲、且都还没有 worker 进程的槽位表。
func newSlotTable(size int) *slotTable {
	table := &slotTable{free: make([]bool, size), alive: make([]bool, size)}
	for index := range table.free {
		table.free[index] = true
	}
	return table
}

// freeCount 返回当前空闲槽位数。
func (t *slotTable) freeCount() int {
	count := 0
	for _, free := range t.free {
		if free {
			count++
		}
	}
	return count
}

// pick 选择派发目标槽位：可复用用例优先复用已有进程，独占用例优先使用还没有进程的槽位。
//
// 选中的槽位一定空闲，因此同一槽位不会同时接到两条用例（W1 / W7）。
func (t *slotTable) pick(dedicated bool) (int, bool) {
	preferred := !dedicated
	fallback := -1
	for index, free := range t.free {
		if !free {
			continue
		}
		if t.alive[index] == preferred {
			return index, true
		}
		if fallback < 0 {
			fallback = index
		}
	}
	if fallback < 0 {
		return 0, false
	}
	return fallback, true
}

// occupy 标记槽位被占用。
func (t *slotTable) occupy(index int) {
	t.free[index] = false
}

// release 标记槽位空闲，并记录它是否还持有可复用的 worker 进程。
func (t *slotTable) release(index int, alive bool) {
	t.free[index] = true
	t.alive[index] = alive
}

// workerPool 管理一次运行的 worker 槽位：派发、回收与收尾。
type workerPool struct {
	output io.Writer
	logDir string
	slots  []*workerSlot
	done   chan slotOutcome
	// nextWorkerID 为每条 worker 进程分配从 1 开始的序号；0 表示没有进入 worker 进程。
	nextWorkerID atomic.Int64
	wait         sync.WaitGroup
}

// newWorkerPool 构造槽位；进程只在第一次派发时才启动。
func newWorkerPool(options runCaseOptions, parallel int, output io.Writer) *workerPool {
	pool := &workerPool{
		output: output,
		logDir: options.Layout.WorkerLogRoot(),
		done:   make(chan slotOutcome, parallel),
		slots:  make([]*workerSlot, parallel),
	}
	for index := range pool.slots {
		pool.slots[index] = &workerSlot{
			id:      index,
			options: options,
			output:  output,
			assign:  make(chan slotAssignment, 1),
			done:    pool.done,
			executor: &pooledExecutor{
				executable: options.Executable,
				grace:      options.Grace,
				logDir:     pool.logDir,
				nextID:     pool.assignWorkerID,
			},
		}
	}
	return pool
}

// start 启动全部槽位 goroutine。
func (p *workerPool) start(ctx context.Context) {
	for _, slot := range p.slots {
		p.wait.Add(1)
		go func(slot *workerSlot) {
			defer p.wait.Done()
			slot.loop(ctx)
		}(slot)
	}
}

// dispatch 把一条用例交给选中的槽位；调用方保证该槽位空闲。
func (p *workerPool) dispatch(slot int, assignment slotAssignment) {
	p.slots[slot].assign <- assignment
}

// assignWorkerID 分配下一个 worker 序号。
func (p *workerPool) assignWorkerID() int {
	return int(p.nextWorkerID.Add(1))
}

// shutdown 结束全部槽位并等待它们收尾：每个槽位让常驻 worker 自行退出。
func (p *workerPool) shutdown() {
	for _, slot := range p.slots {
		close(slot.assign)
	}
	p.wait.Wait()
}

// workerSlot 是一个 worker 槽位：整次运行内固定存在，常驻 worker 在用例之间保留。
//
// 槽位自己持有常驻 worker：派发、等待与收尾都在槽位 goroutine 内完成，调度器只在槽位
// 空闲时派发，因此槽位不需要锁。
type workerSlot struct {
	id       int
	options  runCaseOptions
	output   io.Writer
	assign   chan slotAssignment
	done     chan<- slotOutcome
	executor *pooledExecutor
}

// loop 顺序执行派发到本槽位的用例，直到运行结束关闭派发通道。
func (s *workerSlot) loop(ctx context.Context) {
	for assignment := range s.assign {
		outcome := s.run(ctx, assignment)
		outcome.alive = s.executor.alive()
		s.done <- outcome
	}
	if err := s.executor.close(); err != nil {
		fmt.Fprintf(s.output, "WARN: %v\n", err)
	}
}

// run 执行一条用例；编排进程侧的 panic 必须转成用例级 error，槽位不能因此退出。
func (s *workerSlot) run(ctx context.Context, assignment slotAssignment) (outcome slotOutcome) {
	outcome = slotOutcome{slot: s.id, index: assignment.index, reserved: assignment.reserved}
	options := s.options
	options.Case = assignment.Case
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome.result = CaseResult{
				ID:         assignment.Case.ID,
				Group:      assignment.Case.Group,
				Status:     StatusError,
				Message:    fmt.Sprintf("编排进程执行用例时 panic: %v", recovered),
				StackTrace: string(debug.Stack()),
				Artifacts:  options.Layout.CaseArtifactPaths(assignment.Case.ID),
			}
			outcome.facts = caseObservation{}
		}
	}()
	outcome.result, outcome.facts = runCase(ctx, options, s.executor)
	return outcome
}

// pooledExecutor 复用常驻 worker 执行用例：进程在用例之间保留，独占用例使用一次性 worker。
type pooledExecutor struct {
	executable string
	grace      time.Duration
	logDir     string
	nextID     func() int
	// session 是本槽位当前持有的常驻 worker；nil 表示下一次执行需要启动新进程。
	session *persistentWorker
}

// execute 执行一条用例的主体，返回编排进程侧的观测结论。
func (e *pooledExecutor) execute(
	ctx context.Context,
	inv workerInvocation,
	dedicated bool,
) (workerObservation, error) {
	if dedicated {
		return e.executeIsolated(ctx, inv)
	}
	spawned := false
	if e.session == nil {
		workerID := e.nextID()
		stderrPath, err := e.workerStderrPath(workerID)
		if err != nil {
			return workerObservation{}, err
		}
		session, err := startPersistentWorker(e.executable, workerID, stderrPath)
		if err != nil {
			return workerObservation{}, err
		}
		e.session = session
		spawned = true
	}
	observation, err := e.session.exchange(ctx, inv)
	observation.Spawned = spawned
	if !e.session.alive() {
		// 被终止、崩溃或控制通道失效的 worker 不再复用：下一次执行重新启动进程（W5）。
		e.session = nil
	}
	return observation, err
}

// executeIsolated 用一次性 worker 执行独占用例：执行完即退出，不复用（W2）。
func (e *pooledExecutor) executeIsolated(ctx context.Context, inv workerInvocation) (workerObservation, error) {
	workerID := e.nextID()
	inv.WorkerID = workerID
	observation, err := runWorkerProcess(ctx, inv)
	if observation.Spawned {
		// 真正启动了一次性 worker 才算"使用了独占用例的专属进程"。
		observation.Isolated = true
		observation.WorkerID = workerID
	}
	return observation, err
}

// workerStderrPath 创建该 worker 的日志目录并返回它自己的标准错误日志路径。
func (e *pooledExecutor) workerStderrPath(workerID int) (string, error) {
	dir := filepath.Join(e.logDir, fmt.Sprintf("worker-%02d", workerID))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("创建 worker 日志目录失败: %w", err)
	}
	return filepath.Join(dir, "stderr.log"), nil
}

// alive 报告常驻 worker 进程是否还能复用。
func (e *pooledExecutor) alive() bool {
	return e.session != nil && e.session.alive()
}

// close 让常驻 worker 自行退出；回收失败如实返回错误，不当作已终止。
func (e *pooledExecutor) close() error {
	if e.session == nil {
		return nil
	}
	session := e.session
	e.session = nil
	return session.stop(e.grace)
}
