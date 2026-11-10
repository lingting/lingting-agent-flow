package testrunner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RunOptions 是一次运行的全部输入。
//
// 授权结论、产物根目录与提供者集合都由调用方决定；编排进程不读取环境变量，也不自行
// 扩大授权范围。
type RunOptions struct {
	Cases          []Case
	Selectors      []string
	DefaultTimeout time.Duration
	Grace          time.Duration
	// Parallel 是同时在飞的 worker 上限；零值取 DefaultParallel。
	Parallel     int
	ArtifactRoot string
	RunID        string
	RealTests    RealTestsDecision
	Providers    *Providers
	// ProviderOptions 在 Providers 为空时用于构造默认提供者集合；Root 与 RunID 由编排进程补全。
	ProviderOptions ProviderOptions
	// FaultInjection 只用于自检，正常运行保持零值。
	FaultInjection FaultInjection
	Executable     string
	Output         io.Writer
}

// Run 执行选中的用例并写出统一报告；返回的报告即使运行失败也包含已完成的结论。
func Run(ctx context.Context, opts RunOptions) (Report, error) {
	cases := append([]Case(nil), opts.Cases...)
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	if len(cases) == 0 {
		return Report{}, fmt.Errorf("注册表为空：没有可执行的用例")
	}
	selectors, err := ParseSelectors(opts.Selectors)
	if err != nil {
		return Report{}, err
	}
	selected := Select(cases, selectors)
	if len(selected) == 0 {
		return Report{}, fmt.Errorf("选择条件 %q 没有命中任何用例", strings.Join(opts.Selectors, ","))
	}
	cancelOn := strings.TrimSpace(opts.FaultInjection.CancelOnCase)
	if cancelOn != "" && !selectedContains(selected, cancelOn) {
		return Report{}, fmt.Errorf("自检取消注入的目标用例 %q 不在本次选择内", cancelOn)
	}
	parallel := opts.Parallel
	if parallel <= 0 {
		parallel = DefaultParallel
	}
	defaultTimeout := opts.DefaultTimeout
	if defaultTimeout <= 0 {
		defaultTimeout = DefaultCaseTimeout
	}
	grace := opts.Grace
	if grace <= 0 {
		grace = DefaultWorkerGrace
	}
	executable := opts.Executable
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return Report{}, fmt.Errorf("解析 worker 可执行文件失败: %w", err)
		}
	}
	runID := opts.RunID
	if runID == "" {
		runID, err = NewRunID(time.Now())
		if err != nil {
			return Report{}, err
		}
	}
	layout, err := NewRunLayout(opts.ArtifactRoot, runID)
	if err != nil {
		return Report{}, err
	}
	if err := layout.CreateRunDir(); err != nil {
		return Report{}, err
	}
	providers := opts.Providers
	if providers == nil {
		providerOptions := opts.ProviderOptions
		providerOptions.Root = layout.WorkspaceRoot()
		providerOptions.RunID = runID
		if providerOptions.SelfExecutable == "" {
			providerOptions.SelfExecutable = executable
		}
		providers, err = DefaultProviders(providerOptions)
		if err != nil {
			return Report{}, err
		}
	}
	if err := providers.applyFaultInjection(opts.FaultInjection); err != nil {
		return Report{}, err
	}
	residual := newResidualVerifier(ctx, providers)
	if injected := strings.TrimSpace(opts.FaultInjection.FailReleaseType); injected != "" {
		residual.addInjection("自检故障注入 --selfcheck-fail-release=" + injected)
	}
	output := opts.Output
	if output == nil {
		output = os.Stdout
	}

	startedAt := time.Now()
	shared := newSharedResources()
	report := Report{
		SchemaVersion:    ReportSchemaVersion,
		RunID:            runID,
		ArtifactDir:      layout.RunDir,
		StartedAt:        startedAt,
		Selection:        selectorStrings(selectors),
		DefaultTimeoutMS: defaultTimeout.Milliseconds(),
		RealTests:        opts.RealTests,
	}
	scheduled := runSchedule(ctx, runCaseOptions{
		Layout:         layout,
		Providers:      providers,
		Shared:         shared,
		Executable:     executable,
		DefaultTimeout: defaultTimeout,
		Grace:          grace,
		RunID:          runID,
		RealTests:      opts.RealTests,
	}, selected, parallel, cancelOn, output)
	report.Cases = scheduled.Cases
	report.Concurrency = scheduled.Concurrency
	report.Cancellation = scheduled.Cancellation
	for _, result := range report.Cases {
		if caseAllocated(result) {
			residual.markAllocated()
		}
	}
	// 用例结论在这里已经定型：之后写出的结果文件只能留痕，不能改写结论。
	detectLateResults(report.Cases, opts.FaultInjection.LateResult)
	// 取消后仍要释放资源并执行终检：清理阶段使用与取消解耦的上下文。
	cleanupCtx := context.WithoutCancel(ctx)
	report.Resources = shared.releaseAll(cleanupCtx, providers)
	report.BuildArtifacts = []BuildArtifact{}
	report.Residual = residual.finalize(cleanupCtx, providers)
	report.Totals = summarize(report.Cases)
	report.FinishedAt = time.Now()
	report.DurationMS = durationMS(startedAt, report.FinishedAt)
	report.ExitCode = exitCodeForReport(report)
	if err := WriteReport(layout.ReportPath(), report); err != nil {
		return report, err
	}
	fmt.Fprint(output, report.Summary())
	return report, nil
}

// runCaseOptions 是单条用例执行所需的全部输入。
type runCaseOptions struct {
	Case           Case
	Layout         RunLayout
	Providers      *Providers
	Shared         *sharedResources
	Executable     string
	DefaultTimeout time.Duration
	Grace          time.Duration
	RunID          string
	RealTests      RealTestsDecision
	// OnWorkerSpawn 在 worker 真正启动后回调，用于观测启动次数与触发取消自检。
	OnWorkerSpawn func(caseID string)
}

// runCase 分配资源、启动 worker 并汇总单条用例结论。
func runCase(ctx context.Context, opts runCaseOptions) CaseResult {
	c := opts.Case
	// 释放与自证不能被取消中断：取消路径同样必须先回收已分配的资源，再写报告。
	cleanupCtx := context.WithoutCancel(ctx)
	paths := opts.Layout.CaseArtifactPaths(c.ID)
	result := CaseResult{ID: c.ID, Group: c.Group, Status: StatusError, Artifacts: paths}
	if err := opts.Layout.CreateCaseDir(paths); err != nil {
		result.Message = err.Error()
		return result
	}
	// 用例工作空间在分配资源之前就建好：服务的子进程要在这里落配置、数据库与输出。
	workspaceDir, err := opts.Layout.CreateCaseWorkspace(c.ID)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	handles, resources, err := acquireCaseResources(withCaseWorkspace(ctx, workspaceDir), opts)
	result.Resources = resources
	if err != nil {
		if ctx.Err() != nil {
			// 取消发生在分配资源期间：结论统一按取消口径记录，具体失败原因留在日志里。
			result.Message = caseCancelledMessage
			releaseExclusive(cleanupCtx, opts.Providers, handles, result.Resources)
			return result
		}
		result.Message = err.Error()
		releaseExclusive(cleanupCtx, opts.Providers, handles, result.Resources)
		return result
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = opts.DefaultTimeout
	}
	handlePath := ""
	if len(c.Resources) > 0 {
		handlePath = handleFilePath(paths)
		file := HandleFile{RunID: opts.RunID, CaseID: c.ID, Handles: handles}
		if err := WriteHandleFile(handlePath, file); err != nil {
			result.Message = err.Error()
			releaseExclusive(cleanupCtx, opts.Providers, handles, result.Resources)
			return result
		}
	}
	observation, err := runWorkerProcess(ctx, workerInvocation{
		Executable:      opts.Executable,
		Case:            c,
		Timeout:         timeout,
		Grace:           opts.Grace,
		HandlePath:      handlePath,
		ResultPath:      paths.Result,
		OnSpawn:         spawnObserver(opts.OnWorkerSpawn, c.ID),
		ArtifactDir:     paths.ArtifactDir,
		WorkspaceDir:    workspaceDir,
		LogPaths:        paths,
		RealTests:       opts.RealTests.Allowed,
		RealTestsReason: opts.RealTests.Reason,
	})
	if err != nil {
		result.Message = err.Error()
	} else {
		applyObservation(&result, observation)
	}
	releaseExclusive(cleanupCtx, opts.Providers, handles, result.Resources)
	return result
}

// spawnObserver 把用例的 worker 启动事件转成回调；没有回调时返回 nil，不产生额外开销。
func spawnObserver(observer func(caseID string), caseID string) func() {
	if observer == nil {
		return nil
	}
	return func() { observer(caseID) }
}

// applyObservation 把 worker 观测结果写入用例结论；没有可信结果时如实标为异常。
func applyObservation(result *CaseResult, observation workerObservation) {
	result.ExitCode = observation.ExitCode
	result.DurationMS = durationMS(observation.SpawnAt, observation.FinishedAt)
	if observation.HasResult {
		result.Timings = observation.Result.Timings
	}
	result.Timings.WorkerSpawnAt = observation.SpawnAt
	switch {
	case observation.Cancelled:
		// 取消不伪造结论：被中断的用例一律记 error 并注明运行被取消（R3 D2）。
		result.Status = StatusError
		result.Message = caseCancelledMessage
	case observation.TimedOut:
		result.Status = StatusTimeout
		result.Message = "用例超过执行上限，编排进程已终止 worker 进程树"
	case observation.ReadError != nil:
		result.Status = StatusError
		result.Message = observation.ReadError.Error()
	case !observation.HasResult:
		result.Status = StatusError
		result.Message = fmt.Sprintf("worker 未写出结果文件（退出码 %d）", observation.ExitCode)
	default:
		result.Status = observation.Result.Status
		result.Message = observation.Result.Message
		result.SkipReason = observation.Result.SkipReason
		result.StackTrace = observation.Result.StackTrace
		result.Warnings = append(result.Warnings, observation.Result.Warnings...)
	}
	if result.Status == "" {
		result.Status = StatusError
		result.Message = "worker 结果缺少用例结论"
	}
}

// handleFilePath 返回用例的句柄文件路径。
func handleFilePath(paths ArtifactPaths) string {
	return filepath.Join(paths.CaseDir, "handles.json")
}

// acquireCaseResources 按作用域分配资源：共享资源在整次运行内只分配一次。
func acquireCaseResources(
	ctx context.Context,
	opts runCaseOptions,
) ([]Handle, []ResourceResult, error) {
	handles := make([]Handle, 0, len(opts.Case.Resources))
	results := make([]ResourceResult, 0, len(opts.Case.Resources))
	for _, decl := range opts.Case.Resources {
		if decl.Scope == ScopeRunShared {
			handle, result, err := opts.Shared.acquire(ctx, opts.Providers, decl)
			if err != nil {
				return handles, results, err
			}
			handles = append(handles, handle)
			results = append(results, result)
			continue
		}
		provider, err := opts.Providers.ProviderFor(decl.Type)
		if err != nil {
			return handles, results, err
		}
		handle, err := provider.Acquire(ctx, decl)
		if err != nil {
			return handles, results, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
		}
		handles = append(handles, handle)
		results = append(results, newResourceResult(provider, decl, handle))
	}
	return handles, results, nil
}

// newResourceResult 记录一次成功分配的结论；提供者若写下了子进程输出，路径一并写入报告。
func newResourceResult(provider Provider, decl ResourceDecl, handle Handle) ResourceResult {
	result := ResourceResult{
		Name:        decl.Name,
		Type:        decl.Type,
		Scope:       decl.Scope,
		Fingerprint: handle.Fingerprint,
		Acquired:    true,
	}
	if reporter, ok := provider.(ProcessLogReporter); ok {
		result.LogPath = reporter.ProcessLogPath(handle)
	}
	if len(handle.Payload) > 0 {
		payload := make(map[string]string, len(handle.Payload))
		for key, value := range handle.Payload {
			payload[key] = value
		}
		result.Payload = payload
	}
	return result
}

// caseAllocated 报告该用例是否成功分配过资源。
func caseAllocated(result CaseResult) bool {
	for _, resource := range result.Resources {
		if resource.Acquired {
			return true
		}
	}
	return false
}

// releaseExclusive 释放用例独享资源并自证消失，把结论写回资源结果。
func releaseExclusive(
	ctx context.Context,
	providers *Providers,
	handles []Handle,
	results []ResourceResult,
) {
	byName := make(map[string]Handle, len(handles))
	for _, handle := range handles {
		byName[handle.Name] = handle
	}
	for index := range results {
		if results[index].Scope != ScopeCaseExclusive || !results[index].Acquired {
			continue
		}
		handle := byName[results[index].Name]
		provider, err := providers.ProviderFor(results[index].Type)
		if err != nil {
			results[index].ReleaseError = err.Error()
			continue
		}
		if err := provider.Release(ctx, handle); err != nil {
			results[index].ReleaseError = err.Error()
			continue
		}
		results[index].Released = true
		verifyResourceRelease(ctx, providers, &results[index], handle)
		if repeatErr := provider.Release(ctx, handle); repeatErr != nil {
			results[index].ReleaseError = fmt.Sprintf("重复释放失败: %v", repeatErr)
		}
	}
}

// sharedEntry 是运行级共享资源的一次分配记录。
type sharedEntry struct {
	handle Handle
	result ResourceResult
}

// sharedResources 持有整次运行共享的资源，运行结束时统一释放。
//
// 并发下可能有多个用例同时声明同一份共享资源：获取过程整体串行化（R3 D4），
// 同一声明只会分配一次，不会产生两份句柄。
type sharedResources struct {
	mu      sync.Mutex
	entries map[string]*sharedEntry
	order   []string
}

func newSharedResources() *sharedResources {
	return &sharedResources{entries: make(map[string]*sharedEntry)}
}

// acquire 返回共享资源句柄：同一声明在整次运行内只分配一次。
func (s *sharedResources) acquire(
	ctx context.Context,
	providers *Providers,
	decl ResourceDecl,
) (Handle, ResourceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sharedResourceKey(decl)
	if entry, ok := s.entries[key]; ok {
		return entry.handle, entry.result, nil
	}
	provider, err := providers.ProviderFor(decl.Type)
	if err != nil {
		return Handle{}, ResourceResult{}, err
	}
	handle, err := provider.Acquire(ctx, decl)
	if err != nil {
		return Handle{}, ResourceResult{}, fmt.Errorf("分配共享资源 %q 失败: %w", decl.Name, err)
	}
	result := newResourceResult(provider, decl, handle)
	s.entries[key] = &sharedEntry{handle: handle, result: result}
	s.order = append(s.order, key)
	return handle, result, nil
}

// releaseAll 按分配顺序的逆序释放全部共享资源，并逐条自证消失。
//
// 逆序释放是共享资源之间的依赖要求：后分配的资源依赖先分配的连接或服务，必须先撤销。
func (s *sharedResources) releaseAll(ctx context.Context, providers *Providers) []ResourceResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	results := make([]ResourceResult, 0, len(s.order))
	for index := len(s.order) - 1; index >= 0; index-- {
		entry := s.entries[s.order[index]]
		result := entry.result
		provider, err := providers.ProviderFor(result.Type)
		if err != nil {
			result.ReleaseError = err.Error()
			results = append(results, result)
			continue
		}
		if err := provider.Release(ctx, entry.handle); err != nil {
			result.ReleaseError = err.Error()
			results = append(results, result)
			continue
		}
		result.Released = true
		verifyResourceRelease(ctx, providers, &result, entry.handle)
		if repeatErr := provider.Release(ctx, entry.handle); repeatErr != nil {
			result.ReleaseError = fmt.Sprintf("重复释放失败: %v", repeatErr)
		}
		results = append(results, result)
	}
	return results
}

// selectorStrings 返回选择条件的原始写法，写入报告以便复现。
func selectorStrings(selectors []Selector) []string {
	values := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		values = append(values, selector.String())
	}
	return values
}

// exitCodeForReport 汇总用例结论、资源释放与自证结论以及零残留终检得到运行退出码。
func exitCodeForReport(report Report) int {
	if len(report.Cases) == 0 {
		return ExitFailure
	}
	for _, result := range report.Cases {
		if result.Failed() {
			return ExitFailure
		}
		for _, resource := range result.Resources {
			if resource.Failed() {
				return ExitFailure
			}
		}
	}
	for _, resource := range report.Resources {
		if resource.Failed() {
			return ExitFailure
		}
	}
	if report.Residual.Failed() {
		return ExitFailure
	}
	return ExitOK
}

// selectedContains 报告选择结果里是否包含该用例 ID。
func selectedContains(cases []Case, id string) bool {
	for _, c := range cases {
		if c.ID == id {
			return true
		}
	}
	return false
}
