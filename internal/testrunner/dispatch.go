package testrunner

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// 本文件是 V4 R3 的派发循环：队列分"独占段"与"可复用段"两段，派发决定只在这里做出
// （W1 / W4 / W7）。
//
// 派发是单线程的确定性扫描：每一步按 ID 升序检查待派发用例，满足全部约束就立即派发，
// 被某个约束挡住的用例不阻塞后面的用例（I6），因此同一批输入的结论与串行执行一致，
// 只有完成顺序会随并发变化。取消时停止派发、等待在跑用例结束，并把从未派发的用例如实
// 记为 error。

// runSchedule 按调度决策执行全部用例。
func runSchedule(
	ctx context.Context,
	template runCaseOptions,
	cases []Case,
	parallel int,
	limits ConcurrencyLimits,
	cancelOn string,
	output io.Writer,
) scheduleOutcome {
	// 用例真正开始执行的事件既用于取消自检，也用于核对"派发"与"跑起来"是两件事。
	started := make(chan string, len(cases))
	template.OnCaseStart = func(caseID string) { started <- caseID }
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	loop := newScheduleLoop(template, cases, parallel, limits, output)
	loop.pool.start(runCtx)
	for _, warning := range limits.Warnings() {
		fmt.Fprintf(output, "WARN: %s\n", warning)
	}
	injectedCancel := ""
	finished := 0
	for {
		if !loop.cancelling && runCtx.Err() != nil {
			loop.beginCancel(cancellationReason(injectedCancel))
		}
		if !loop.cancelling {
			loop.scheduleRound()
		}
		if loop.state.inFlight == 0 {
			loop.drainUnexecuted()
			break
		}
		select {
		case outcome := <-loop.pool.done:
			loop.complete(outcome)
			finished++
			fmt.Fprintf(
				output,
				"[%d/%d] %-7s %s (%dms)\n",
				finished,
				len(cases),
				strings.ToUpper(outcome.result.Status),
				outcome.result.ID,
				outcome.result.DurationMS,
			)
		case caseID := <-started:
			if cancelOn != "" && caseID == cancelOn && !loop.cancelling {
				// 自检注入：命名用例真正跑起来之后才取消，覆盖"在跑被终止"的路径。
				injectedCancel = fmt.Sprintf("自检注入：用例 %q 的 worker 启动后取消运行", caseID)
				cancelRun()
			}
		case <-runCtx.Done():
			// 下一轮开头记录取消原因并停止派发；在跑用例由自己的取消分支收尾。
		}
	}
	loop.pool.shutdown()
	outcome := loop.outcome
	outcome.Concurrency.WorkerLaunches = loop.stats.Total
	outcome.Concurrency.Package = loop.state.quotaReports(quotaKindPackage)
	outcome.Concurrency.Quotas = loop.state.quotaReports(quotaKindPool)
	outcome.Concurrency.Warnings = limits.Warnings()
	outcome.Workers = loop.stats
	return outcome
}

// scheduleLoop 是调度线程独占的一次运行状态：两段队列、槽位占用、额度状态与派发观测。
//
// 只有调度线程访问它；worker 槽位只通过通道回传结论，因此这里不需要锁。
type scheduleLoop struct {
	template runCaseOptions
	cases    []Case
	state    *scheduleState
	table    *slotTable
	pool     *workerPool
	outcome  scheduleOutcome
	stats    WorkerPoolReport
	output   io.Writer
	// startedAt 是整次运行的派发起始时刻，排队时长以它为起点。
	startedAt    time.Time
	dispatchedAt []time.Time
	// blockedAt 记录用例第一次没能被派发的时刻：排队时长按它计入该用例所属额度（I9）。
	blockedAt []time.Time
	// pendingDedicated 是声明 DedicatedWorker 的待派发用例（独占段）。
	pendingDedicated []int
	// pendingReusable 是其余待派发用例（可复用段）。
	pendingReusable []int
	cancelling      bool
}

// newScheduleLoop 构造调度状态：两段队列都保持 ID 升序，槽位与 worker 池一起建好。
func newScheduleLoop(
	template runCaseOptions,
	cases []Case,
	parallel int,
	limits ConcurrencyLimits,
	output io.Writer,
) *scheduleLoop {
	loop := &scheduleLoop{
		template:     template,
		cases:        cases,
		output:       output,
		state:        newScheduleState(limits),
		table:        newSlotTable(parallel),
		startedAt:    time.Now(),
		dispatchedAt: make([]time.Time, len(cases)),
		blockedAt:    make([]time.Time, len(cases)),
		outcome: scheduleOutcome{
			Cases:       make([]CaseResult, len(cases)),
			Concurrency: ConcurrencyReport{ParallelLimit: parallel},
		},
	}
	for index, c := range cases {
		if c.DedicatedWorker {
			loop.pendingDedicated = append(loop.pendingDedicated, index)
			continue
		}
		loop.pendingReusable = append(loop.pendingReusable, index)
	}
	loop.pool = newWorkerPool(template, parallel, output)
	return loop
}

// scheduleRound 派发本轮所有可派发的用例：独占段优先，并为可复用段保留一个槽位（W4）。
func (s *scheduleLoop) scheduleRound() {
	// W4：可复用队列非空、且已经有用例在跑时，独占派发不得拿走最后一个空闲槽位——那条
	// 槽位留给可复用用例，避免独占队列把 worker 全占走而让可复用队列空转。
	reserveReusable := s.state.inFlight > 0 && len(s.pendingReusable) > 0
	s.pendingDedicated = s.dispatchSegment(s.pendingDedicated, true, reserveReusable)
	s.pendingReusable = s.dispatchSegment(s.pendingReusable, false, false)
}

// dispatchSegment 尝试派发一段队列，返回仍然待派发的用例。
func (s *scheduleLoop) dispatchSegment(pending []int, dedicated, reserveSlot bool) []int {
	kept := make([]int, 0, len(pending))
	for position := 0; position < len(pending); position++ {
		if reserveSlot && s.table.freeCount() < 2 {
			return append(kept, pending[position:]...)
		}
		slot, ok := s.table.pick(dedicated)
		if !ok {
			// 槽位已满：剩下的用例这一轮都派不出去，排队起点按第一次被挡住的时刻记（I9）。
			s.markBlocked(pending[position:])
			return append(kept, pending[position:]...)
		}
		if !s.dispatchCase(pending[position], slot) {
			kept = append(kept, pending[position])
		}
	}
	return kept
}

// dispatchCase 尝试把一条用例派发到指定槽位；返回 false 表示这次没能派发。
func (s *scheduleLoop) dispatchCase(index, slot int) bool {
	c := s.cases[index]
	requirement := planCase(c, s.state)
	if requirement.impossible != nil {
		// 声明不可能满足：当场判错，不永久排队，也不改判跳过（I7）。
		result := unexecutedCaseResult(s.template.Layout, c, requirement.impossible.Error())
		result.Timings.DispatchAt = time.Now()
		s.outcome.Cases[index] = result
		s.state.recordQuotaUndispatched(requirement, requirement.impossible.Error())
		return true
	}
	if !s.state.admits(requirement) {
		if s.blockedAt[index].IsZero() {
			s.blockedAt[index] = time.Now()
		}
		return false
	}
	reserved := s.state.reserve(requirement)
	s.state.recordQuotaQueueWait(reserved, s.blockedAt[index], time.Now())
	if s.state.inFlight > s.outcome.Concurrency.MaxInFlight {
		s.outcome.Concurrency.MaxInFlight = s.state.inFlight
	}
	s.dispatchedAt[index] = time.Now()
	s.table.occupy(slot)
	s.pool.dispatch(slot, slotAssignment{index: index, Case: c, reserved: reserved})
	return true
}

// markBlocked 记录这批用例第一次被挡住的时刻：排队时长按它计入该用例所属额度（I9）。
func (s *scheduleLoop) markBlocked(indices []int) {
	now := time.Now()
	for _, index := range indices {
		if s.blockedAt[index].IsZero() {
			s.blockedAt[index] = now
		}
	}
}

// complete 记录一条用例的结论，并释放它占用的额度与槽位。
func (s *scheduleLoop) complete(outcome slotOutcome) {
	s.state.release(outcome.reserved)
	s.state.recordQuotaCompletion(outcome.reserved)
	s.table.release(outcome.slot, outcome.alive)
	s.accumulate(outcome.facts)
	result := outcome.result
	result.Timings.QueueEnterAt = s.startedAt
	result.Timings.DispatchAt = s.dispatchedAt[outcome.index]
	s.outcome.Cases[outcome.index] = result
	queued := durationMS(s.startedAt, s.dispatchedAt[outcome.index])
	s.outcome.Concurrency.QueuedTotalMS += queued
	if queued > s.outcome.Concurrency.QueuedMaxMS {
		s.outcome.Concurrency.QueuedMaxMS = queued
	}
	if result.Message == caseCancelledMessage {
		s.outcome.Cancellation.CancelledCases++
	}
}

// accumulate 汇总 worker 池观测：进程启动、复用、非预期退出与独占用例（D-08）。
func (s *scheduleLoop) accumulate(facts caseObservation) {
	if facts.Launched {
		s.stats.Total++
	}
	if facts.Reused {
		s.stats.Reuse++
	}
	if facts.Restarted {
		s.stats.Restarts++
	}
	if facts.Isolated {
		s.stats.IsolatedCases++
	}
}

// beginCancel 记录运行级取消结论并停止后续派发。
func (s *scheduleLoop) beginCancel(reason string) {
	s.cancelling = true
	s.outcome.Cancellation = CancellationRecord{Cancelled: true, Reason: reason, At: time.Now()}
	fmt.Fprintf(s.output, "取消：%s，停止派发并终止在跑 worker\n", reason)
}

// drainUnexecuted 为队列里剩下的用例给出如实结论：取消后未派发，或约束已不可能满足。
func (s *scheduleLoop) drainUnexecuted() {
	pending := make([]int, 0, len(s.pendingDedicated)+len(s.pendingReusable))
	pending = append(pending, s.pendingDedicated...)
	pending = append(pending, s.pendingReusable...)
	for _, index := range pending {
		c := s.cases[index]
		requirement := planCase(c, s.state)
		reason := caseUndispatchedMessage
		if !s.cancelling {
			reason = "调度器无法在剩余容量内派发该用例，未执行"
			if blockers := s.state.quotaBlockers(requirement); len(blockers) > 0 {
				reason += "：" + strings.Join(blockers, "；")
			}
		}
		s.outcome.Cases[index] = unexecutedCaseResult(s.template.Layout, c, reason)
		s.state.recordQuotaUndispatched(requirement, reason)
		if s.cancelling {
			s.outcome.Cancellation.UndispatchedCases++
		}
	}
	s.pendingDedicated = nil
	s.pendingReusable = nil
}
