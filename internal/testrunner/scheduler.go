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
// 资源容量、独占键与串行作用域都可能把实际并发压得更低，取更严格的一方。默认值不取
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
// 派发是单线程的确定性扫描：每一步按 ID 升序检查待派发用例，满足全部约束就立即派发，
// 因此同一批输入的结论与串行执行一致，只有完成顺序会随并发变化。取消时停止派发、
// 等待在跑 worker 结束，并把从未派发的用例如实记为 error。
func runSchedule(
	ctx context.Context,
	template runCaseOptions,
	cases []Case,
	parallel int,
	cancelOn string,
	output io.Writer,
) scheduleOutcome {
	startedAt := time.Now()
	outcome := scheduleOutcome{
		Cases:       make([]CaseResult, len(cases)),
		Concurrency: ConcurrencyReport{ParallelLimit: parallel},
	}
	dispatchedAt := make([]time.Time, len(cases))

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	state := newScheduleState()
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
					kept = append(kept, index)
					continue
				}
				c := cases[index]
				requirement := planCase(c, state)
				if requirement.impossible != nil {
					result := unexecutedCaseResult(template.Layout, c, requirement.impossible.Error())
					result.Timings.DispatchAt = time.Now()
					outcome.Cases[index] = result
					continue
				}
				if !state.admits(requirement) {
					kept = append(kept, index)
					continue
				}
				reserved := state.reserve(requirement)
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
				result := unexecutedCaseResult(template.Layout, cases[index], caseUndispatchedMessage)
				if !cancelling {
					result = unexecutedCaseResult(
						template.Layout,
						cases[index],
						"调度器无法在剩余容量内派发该用例，未执行",
					)
				}
				outcome.Cases[index] = result
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
}

// reservation 是一条用例占用的份额，用例结束时按它释放。
type reservation struct {
	scope    string
	parallel bool
	keys     []string
	permits  map[string]int
}

func newScheduleState() *scheduleState {
	return &scheduleState{
		scopes:       make(map[string]int),
		serialScopes: make(map[string]bool),
		keys:         make(map[string]int),
		permits:      make(map[string]int),
		shared:       make(map[string]struct{}),
	}
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
	return s.fits(requirement)
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
	return reservation{
		scope:    requirement.scope,
		parallel: requirement.parallel,
		keys:     requirement.keys,
		permits:  requirement.permits,
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

// sortedKeys 按名称排序，避免容量判定受 map 迭代顺序影响。
func sortedKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
