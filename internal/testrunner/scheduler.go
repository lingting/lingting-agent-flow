package testrunner

import (
	"context"
	"fmt"
	"io"
	"runtime/debug"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultParallel 是同时在飞的 worker 上限（V3 R3 D1 冻结）。
//
// 资源容量、独占键、串行作用域与并发额度都可能把实际并发压得更低，取更严格的一方。默认值不取
// runtime.NumCPU()：同一份输入在不同机器上必须跑出同样的并发。
const DefaultParallel = 8

// scheduleOutcome 是一次调度的结果。
type scheduleOutcome struct {
	// Cases 与输入顺序（ID 升序）一一对应，与用例完成顺序无关。
	Cases        []CaseResult
	Concurrency  ConcurrencyReport
	Cancellation CancellationRecord
}

// caseCompletion 是一条用例执行完成后的回传。
type caseCompletion struct {
	index       int
	reservation reservation
	result      CaseResult
}

// runSchedule 按调度决策执行全部用例。
//
// 派发是单线程的确定性扫描：每一步按 ID 升序检查待派发用例，满足全部约束就立即派发，被某个
// 额度挡住的用例不阻塞后面的用例（I6），因此同一批输入的结论与串行执行一致，只有完成顺序会
// 随并发变化。取消时停止派发、等待在跑 worker 结束，并把从未派发的用例如实记为 error。
func runSchedule(
	ctx context.Context,
	template runCaseOptions,
	cases []Case,
	parallel int,
	limits ConcurrencyLimits,
	cancelOn string,
	output io.Writer,
) scheduleOutcome {
	startedAt := time.Now()
	outcome := scheduleOutcome{
		Cases:       make([]CaseResult, len(cases)),
		Concurrency: ConcurrencyReport{ParallelLimit: parallel},
	}
	dispatchedAt := make([]time.Time, len(cases))
	// blockedAt 记录用例第一次没能被派发的时刻：排队时长按它计入该用例所属额度（I9）。
	blockedAt := make([]time.Time, len(cases))

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	state := newScheduleState(limits)
	for _, warning := range limits.Warnings() {
		fmt.Fprintf(output, "WARN: %s\n", warning)
	}
	pending := make([]int, len(cases))
	for index := range pending {
		pending[index] = index
	}
	done := make(chan caseCompletion, len(cases))
	finished := 0
	cancelling := false
	injectedCancel := ""
	spawned := make(chan string, len(cases))
	var launches atomic.Int64
	// 启动事件既用于统计 worker 启动次数，也用于在指定用例真正跑起来之后再触发取消。
	template.OnWorkerSpawn = func(caseID string) {
		launches.Add(1)
		spawned <- caseID
	}

	for {
		if !cancelling && runCtx.Err() != nil {
			cancelling = true
			outcome.Cancellation = CancellationRecord{
				Cancelled: true,
				Reason:    cancellationReason(injectedCancel),
				At:        time.Now(),
			}
			fmt.Fprintf(output, "取消：%s，停止派发并终止在跑 worker\n", outcome.Cancellation.Reason)
		}
		if !cancelling {
			kept := make([]int, 0, len(pending))
			for _, index := range pending {
				if state.inFlight >= parallel {
					// 全局槽位满与额度满都是"被挡住"：排队时长按第一次被挡住的时刻计入额度（I9）。
					if blockedAt[index].IsZero() {
						blockedAt[index] = time.Now()
					}
					kept = append(kept, index)
					continue
				}
				c := cases[index]
				requirement := planCase(c, state)
				if requirement.impossible != nil {
					result := unexecutedCaseResult(template.Layout, c, requirement.impossible.Error())
					result.Timings.DispatchAt = time.Now()
					outcome.Cases[index] = result
					state.recordQuotaUndispatched(requirement, requirement.impossible.Error())
					continue
				}
				if !state.admits(requirement) {
					if blockedAt[index].IsZero() {
						blockedAt[index] = time.Now()
					}
					kept = append(kept, index)
					continue
				}
				reserved := state.reserve(requirement)
				state.recordQuotaQueueWait(reserved, blockedAt[index], time.Now())
				if state.inFlight > outcome.Concurrency.MaxInFlight {
					outcome.Concurrency.MaxInFlight = state.inFlight
				}
				dispatchedAt[index] = time.Now()
				options := template
				options.Case = c
				go startCase(runCtx, options, index, reserved, done)
			}
			pending = kept
		}

		if state.inFlight == 0 {
			// 队列里剩下的用例本轮无法执行：取消后不再派发，其余情况说明约束已不可能满足。
			for _, index := range pending {
				c := cases[index]
				requirement := planCase(c, state)
				reason := caseUndispatchedMessage
				if !cancelling {
					reason = "调度器无法在剩余容量内派发该用例，未执行"
					if blockers := state.quotaBlockers(requirement); len(blockers) > 0 {
						reason += "：" + strings.Join(blockers, "；")
					}
				}
				outcome.Cases[index] = unexecutedCaseResult(template.Layout, c, reason)
				state.recordQuotaUndispatched(requirement, reason)
				if cancelling {
					outcome.Cancellation.UndispatchedCases++
				}
			}
			pending = nil
			break
		}

		select {
		case completion := <-done:
			state.release(completion.reservation)
			state.recordQuotaCompletion(completion.reservation)
			result := completion.result
			result.Timings.QueueEnterAt = startedAt
			result.Timings.DispatchAt = dispatchedAt[completion.index]
			outcome.Cases[completion.index] = result
			finished++
			queued := durationMS(startedAt, dispatchedAt[completion.index])
			outcome.Concurrency.QueuedTotalMS += queued
			if queued > outcome.Concurrency.QueuedMaxMS {
				outcome.Concurrency.QueuedMaxMS = queued
			}
			if result.Message == caseCancelledMessage {
				outcome.Cancellation.CancelledCases++
			}
			fmt.Fprintf(
				output,
				"[%d/%d] %-7s %s (%dms)\n",
				finished,
				len(cases),
				strings.ToUpper(result.Status),
				result.ID,
				result.DurationMS,
			)
		case caseID := <-spawned:
			if cancelOn != "" && caseID == cancelOn && !cancelling {
				// 自检注入：命名用例的 worker 真正跑起来之后才取消，覆盖"在跑被终止"的路径。
				injectedCancel = fmt.Sprintf("自检注入：用例 %q 的 worker 启动后取消运行", caseID)
				cancelRun()
			}
		case <-runCtx.Done():
			// 下一轮开头记录取消原因并停止派发；在跑用例由自己的取消分支收尾。
		}
	}
	outcome.Concurrency.WorkerLaunches = int(launches.Load())
	outcome.Concurrency.Package = state.quotaReports(quotaKindPackage)
	outcome.Concurrency.Quotas = state.quotaReports(quotaKindPool)
	outcome.Concurrency.Warnings = limits.Warnings()
	return outcome
}

// startCase 在独立 goroutine 里执行一条用例，完成后把结论与占用份额交回调度器。
//
// 编排进程侧的 panic 必须转成用例级 error：调度器崩溃会让整次运行无法解释。
func startCase(
	ctx context.Context,
	options runCaseOptions,
	index int,
	reserved reservation,
	done chan<- caseCompletion,
) {
	completion := caseCompletion{index: index, reservation: reserved}
	defer func() {
		if recovered := recover(); recovered != nil {
			completion.result = CaseResult{
				ID:         options.Case.ID,
				Group:      options.Case.Group,
				Status:     StatusError,
				Message:    fmt.Sprintf("编排进程执行用例时 panic: %v", recovered),
				StackTrace: string(debug.Stack()),
				Artifacts:  options.Layout.CaseArtifactPaths(options.Case.ID),
			}
		}
		done <- completion
	}()
	completion.result = runCase(ctx, options)
}

// unexecutedCaseResult 为没有真正执行过的用例给出如实结论：未执行永远不是通过。
func unexecutedCaseResult(layout RunLayout, c Case, reason string) CaseResult {
	paths := layout.CaseArtifactPaths(c.ID)
	result := CaseResult{ID: c.ID, Group: c.Group, Status: StatusError, Message: reason, Artifacts: paths}
	if err := layout.CreateCaseDir(paths); err != nil {
		result.Message = fmt.Sprintf("%s（创建产物目录失败: %v）", reason, err)
	}
	return result
}

// scheduleState 记录当前占用；只有派发线程访问，因此不需要锁。
type scheduleState struct {
	inFlight int
	// scopes 记录每个串行作用域内的在跑用例数。
	scopes map[string]int
	// serialScopes 记录仍有独占用例在跑的作用域。
	serialScopes map[string]bool
	// keys 记录独占键的持有用例数。
	keys map[string]int
	// permits 记录资源类型的已占容量份数。
	permits map[string]int
	// shared 记录已认领的运行级共享资源键。
	shared map[string]struct{}
	// limits 是解析后的生效额度：包级上限与命名池（V4 R2）。
	limits ConcurrencyLimits
	// pkgInFlight 记录每个包的在跑用例数。
	pkgInFlight map[string]int
	// poolInFlight 记录每个命名并发池的在跑用例数。
	poolInFlight map[string]int
	// quotas 记录每个额度条目的观测数据，键为 "<kind>|<name>"。
	quotas map[string]*quotaStat
}

// reservation 是一条用例占用的份额，用例结束时按它释放。
type reservation struct {
	scope    string
	parallel bool
	keys     []string
	permits  map[string]int
	// pkg 与 pools 是该用例占用的包级上限与命名池，释放与统计都按它们进行。
	pkg   string
	pools []string
}

// quotaStat 是一个额度条目的观测数据：峰值、完成数、排队时间与未派发原因（D-08）。
type quotaStat struct {
	kind               string
	name               string
	limit              int
	declared           int
	peak               int
	completed          int
	queuedTotalMS      int64
	queuedMaxMS        int64
	undispatched       int
	undispatchedReason string
}

func newScheduleState(limits ConcurrencyLimits) *scheduleState {
	state := &scheduleState{
		scopes:       make(map[string]int),
		serialScopes: make(map[string]bool),
		keys:         make(map[string]int),
		permits:      make(map[string]int),
		shared:       make(map[string]struct{}),
		limits:       limits,
		pkgInFlight:  make(map[string]int),
		poolInFlight: make(map[string]int),
		quotas:       make(map[string]*quotaStat),
	}
	// 本次选择出现过的每个包与每个池都必须有条目：从未被阻塞的额度峰值是 0，不是"不存在"。
	for _, seed := range limits.seeds() {
		state.quotas[quotaKeyOf(seed.kind, seed.name)] = &quotaStat{
			kind:     seed.kind,
			name:     seed.name,
			limit:    seed.limit,
			declared: seed.declared,
		}
	}
	return state
}

// caseRequirement 是一条用例对调度器提出的全部约束。
type caseRequirement struct {
	parallel bool
	scope    string
	// keys 是占用期间必须独占的键，随用例结束释放。
	keys []string
	// permits 是占用期间需要的容量份数，随用例结束释放。
	permits map[string]int
	// runKeys 是整次运行都要独占的键（首次认领的运行级共享资源）。
	runKeys []string
	// runPermits 是整次运行都要占用的容量（首次认领的运行级共享资源）。
	runPermits map[string]int
	// sharedClaims 是本次占用会认领的运行级共享资源键。
	sharedClaims []string
	// pkg 是用例所属包，pkgLimit 是它的生效上限（0 表示不限）。
	pkg      string
	pkgLimit int
	// pools 是参与的命名并发池名称，按名称排序。
	pools []string
	// impossible 非空表示该用例的约束永远不可能满足，必须当场判错而不是排队等待。
	impossible error
}

// planCase 把用例声明翻译成调度约束。
//
// 已认领的运行级共享资源不再重复占用容量与独占键：同一份资源整次运行只分配一次，
// 其余用例复用同一句柄。容量不足在派发前判错，不做"先启动再发现冲突再重试"。
func planCase(c Case, state *scheduleState) caseRequirement {
	requirement := caseRequirement{
		parallel:   c.Parallel,
		scope:      serialScopeOf(c),
		permits:    make(map[string]int),
		runPermits: make(map[string]int),
	}
	requirement.pkg = strings.TrimSpace(c.Package)
	requirement.pkgLimit = state.limits.packageLimitOf(requirement.pkg)
	requirement.pools = sortedKeys(c.Concurrency.Pools)
	if SerialPackageReason(c.Package) != "" {
		// 包级串行是整包互斥：声明来自 R2 的冻结表，不写死在调度器里。
		requirement.parallel = false
	}
	for _, decl := range c.Resources {
		if decl.Scope == ScopeRunShared {
			key := sharedResourceKey(decl)
			if _, claimed := state.shared[key]; claimed {
				continue
			}
			requirement.sharedClaims = append(requirement.sharedClaims, key)
			if CapacityOf(decl.Type) > 0 {
				requirement.runPermits[decl.Type]++
			}
			if exclusive := ExclusiveKeyOf(decl); exclusive != "" {
				requirement.runKeys = append(requirement.runKeys, exclusive)
			}
			continue
		}
		if CapacityOf(decl.Type) > 0 {
			requirement.permits[decl.Type]++
		}
		if exclusive := ExclusiveKeyOf(decl); exclusive != "" {
			requirement.keys = append(requirement.keys, exclusive)
		}
	}
	demand := requirement.demand()
	for _, resourceType := range sortedKeys(demand) {
		limit := CapacityOf(resourceType)
		if limit > 0 && demand[resourceType] > limit {
			requirement.impossible = fmt.Errorf(
				"资源类型 %q 的容量上限 %d 小于用例需要的 %d 份，无法调度",
				resourceType,
				limit,
				demand[resourceType],
			)
		}
	}
	return requirement
}

// demand 返回该约束对每种资源类型需要的容量份数。
func (r caseRequirement) demand() map[string]int {
	demand := make(map[string]int, len(r.permits)+len(r.runPermits))
	for resourceType, need := range r.permits {
		demand[resourceType] += need
	}
	for resourceType, need := range r.runPermits {
		demand[resourceType] += need
	}
	return demand
}

// admits 判断当前占用状态下能否立即派发该约束。
func (s *scheduleState) admits(requirement caseRequirement) bool {
	if requirement.scope != "" {
		if requirement.parallel {
			if s.serialScopes[requirement.scope] {
				return false
			}
		} else if s.scopes[requirement.scope] > 0 {
			return false
		}
	}
	for _, key := range requirement.keys {
		if s.keys[key] > 0 {
			return false
		}
	}
	for _, key := range requirement.runKeys {
		if s.keys[key] > 0 {
			return false
		}
	}
	if requirement.pkgLimit > 0 && s.pkgInFlight[requirement.pkg] >= requirement.pkgLimit {
		return false
	}
	for _, name := range requirement.pools {
		limit := s.limits.poolLimitOf(name)
		if limit > 0 && s.poolInFlight[name] >= limit {
			return false
		}
	}
	return s.fits(requirement)
}

// quotaBlockers 返回当前被哪些额度挡住，用于排队记账与未派发原因（I7 / I9）。
func (s *scheduleState) quotaBlockers(requirement caseRequirement) []string {
	var blockers []string
	if requirement.pkg != "" &&
		requirement.pkgLimit > 0 &&
		s.pkgInFlight[requirement.pkg] >= requirement.pkgLimit {
		blockers = append(
			blockers,
			fmt.Sprintf("包 %q 的上限 %d 已占满", requirement.pkg, requirement.pkgLimit),
		)
	}
	for _, name := range requirement.pools {
		limit := s.limits.poolLimitOf(name)
		if limit > 0 && s.poolInFlight[name] >= limit {
			blockers = append(blockers, fmt.Sprintf("命名池 %q 的容量 %d 已占满", name, limit))
		}
	}
	return blockers
}

// fits 判断容量是否还装得下该约束。
func (s *scheduleState) fits(requirement caseRequirement) bool {
	demand := requirement.demand()
	for _, resourceType := range sortedKeys(demand) {
		limit := CapacityOf(resourceType)
		if limit > 0 && s.permits[resourceType]+demand[resourceType] > limit {
			return false
		}
	}
	return true
}

// reserve 占用该约束要求的份额，返回用例结束时用于释放的记录。
func (s *scheduleState) reserve(requirement caseRequirement) reservation {
	s.inFlight++
	if requirement.scope != "" {
		s.scopes[requirement.scope]++
		if !requirement.parallel {
			s.serialScopes[requirement.scope] = true
		}
	}
	for _, key := range requirement.keys {
		s.keys[key]++
	}
	for _, key := range requirement.runKeys {
		s.keys[key]++
	}
	for resourceType, need := range requirement.permits {
		s.permits[resourceType] += need
	}
	for resourceType, need := range requirement.runPermits {
		s.permits[resourceType] += need
	}
	for _, key := range requirement.sharedClaims {
		s.shared[key] = struct{}{}
	}
	if requirement.pkg != "" {
		s.pkgInFlight[requirement.pkg]++
		if stat := s.quotaOf(quotaKindPackage, requirement.pkg); stat != nil {
			if s.pkgInFlight[requirement.pkg] > stat.peak {
				stat.peak = s.pkgInFlight[requirement.pkg]
			}
		}
	}
	for _, name := range requirement.pools {
		s.poolInFlight[name]++
		if stat := s.quotaOf(quotaKindPool, name); stat != nil {
			if s.poolInFlight[name] > stat.peak {
				stat.peak = s.poolInFlight[name]
			}
		}
	}
	return reservation{
		scope:    requirement.scope,
		parallel: requirement.parallel,
		keys:     requirement.keys,
		permits:  requirement.permits,
		pkg:      requirement.pkg,
		pools:    requirement.pools,
	}
}

// release 释放用例占用期间独占的份额；运行级共享资源的占用随运行结束统一消失。
func (s *scheduleState) release(reserved reservation) {
	s.inFlight--
	if reserved.scope != "" {
		s.scopes[reserved.scope]--
		if s.scopes[reserved.scope] <= 0 {
			delete(s.scopes, reserved.scope)
			if !reserved.parallel {
				delete(s.serialScopes, reserved.scope)
			}
		}
	}
	for _, key := range reserved.keys {
		s.keys[key]--
		if s.keys[key] <= 0 {
			delete(s.keys, key)
		}
	}
	for resourceType, need := range reserved.permits {
		s.permits[resourceType] -= need
		if s.permits[resourceType] <= 0 {
			delete(s.permits, resourceType)
		}
	}
	if reserved.pkg != "" {
		s.pkgInFlight[reserved.pkg]--
		if s.pkgInFlight[reserved.pkg] <= 0 {
			delete(s.pkgInFlight, reserved.pkg)
		}
	}
	for _, name := range reserved.pools {
		s.poolInFlight[name]--
		if s.poolInFlight[name] <= 0 {
			delete(s.poolInFlight, name)
		}
	}
}

// recordQuotaQueueWait 把排队时长计入该用例所属的额度：只有被额度挡住过的用例才计入（I9）。
func (s *scheduleState) recordQuotaQueueWait(reserved reservation, blockedAt, dispatchedAt time.Time) {
	if blockedAt.IsZero() {
		return
	}
	wait := durationMS(blockedAt, dispatchedAt)
	for _, stat := range s.reservationQuotas(reserved) {
		stat.queuedTotalMS += wait
		if wait > stat.queuedMaxMS {
			stat.queuedMaxMS = wait
		}
	}
}

// recordQuotaCompletion 记录该额度下已经结束的用例数（含失败、超时与取消）。
func (s *scheduleState) recordQuotaCompletion(reserved reservation) {
	for _, stat := range s.reservationQuotas(reserved) {
		stat.completed++
	}
}

// recordQuotaUndispatched 记录因调度约束从未执行的用例；原因只保留第一条，便于按额度定位。
func (s *scheduleState) recordQuotaUndispatched(requirement caseRequirement, reason string) {
	stats := make([]*quotaStat, 0, 1+len(requirement.pools))
	if requirement.pkg != "" {
		if stat := s.quotaOf(quotaKindPackage, requirement.pkg); stat != nil {
			stats = append(stats, stat)
		}
	}
	for _, name := range requirement.pools {
		if stat := s.quotaOf(quotaKindPool, name); stat != nil {
			stats = append(stats, stat)
		}
	}
	for _, stat := range stats {
		stat.undispatched++
		if stat.undispatchedReason == "" {
			stat.undispatchedReason = reason
		}
	}
}

// reservationQuotas 返回该占用涉及的额度条目，顺序为包在前、池按名称排序。
func (s *scheduleState) reservationQuotas(reserved reservation) []*quotaStat {
	stats := make([]*quotaStat, 0, 1+len(reserved.pools))
	if reserved.pkg != "" {
		if stat := s.quotaOf(quotaKindPackage, reserved.pkg); stat != nil {
			stats = append(stats, stat)
		}
	}
	for _, name := range reserved.pools {
		if stat := s.quotaOf(quotaKindPool, name); stat != nil {
			stats = append(stats, stat)
		}
	}
	return stats
}

// quotaOf 返回该额度条目的观测记录；本次运行没有出现的额度返回 nil。
func (s *scheduleState) quotaOf(kind, name string) *quotaStat {
	return s.quotas[quotaKeyOf(kind, name)]
}

// quotaReports 把观测数据转成报告条目：按名称排序，保证同一输入的输出一致。
func (s *scheduleState) quotaReports(kind string) []QuotaReport {
	reports := make([]QuotaReport, 0, len(s.quotas))
	for _, stat := range s.quotas {
		if stat.kind != kind {
			continue
		}
		reports = append(reports, QuotaReport{
			Name:               stat.name,
			Limit:              stat.limit,
			DeclaredLimit:      stat.declared,
			Peak:               stat.peak,
			Completed:          stat.completed,
			QueuedTotalMS:      stat.queuedTotalMS,
			QueuedMaxMS:        stat.queuedMaxMS,
			Undispatched:       stat.undispatched,
			UndispatchedReason: stat.undispatchedReason,
		})
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].Name < reports[j].Name })
	return reports
}

// quotaKeyOf 是额度条目在状态表里的键。
func quotaKeyOf(kind, name string) string {
	return kind + "|" + name
}

// serialScopeOf 返回用例的串行作用域：包路径优先，其次分组。
func serialScopeOf(c Case) string {
	if pkg := strings.TrimSpace(c.Package); pkg != "" {
		return pkg
	}
	if c.Group != "" {
		return c.Group
	}
	return groupOf(c.ID)
}

// sharedResourceKey 是运行级共享资源的唯一键：同类型同名声明整次运行只分配一次。
func sharedResourceKey(decl ResourceDecl) string {
	return decl.Type + "|" + decl.Name
}

// sortedKeys 按名称排序，避免容量判定与报告生成受 map 迭代顺序影响。
func sortedKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
