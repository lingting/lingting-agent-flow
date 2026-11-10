package testrunner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	ArtifactRoot   string
	RunID          string
	RealTests      RealTestsDecision
	Providers      *Providers
	// ProviderOptions 在 Providers 为空时用于构造默认提供者集合；Root 与 RunID 由编排进程补全。
	ProviderOptions ProviderOptions
	// FaultInjection 只用于自检零残留核验本身，正常运行保持零值。
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
		providerOptions.Root = layout.ResourcesDir()
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
		Cases:            make([]CaseResult, 0, len(selected)),
	}
	for index, c := range selected {
		result := runCase(ctx, runCaseOptions{
			Case:           c,
			Layout:         layout,
			Providers:      providers,
			Shared:         shared,
			Executable:     executable,
			DefaultTimeout: defaultTimeout,
			Grace:          grace,
			RunID:          runID,
			RealTests:      opts.RealTests,
		})
		report.Cases = append(report.Cases, result)
		if caseAllocated(result) {
			residual.markAllocated()
		}
		fmt.Fprintf(
			output,
			"[%d/%d] %-7s %s (%dms)\n",
			index+1,
			len(selected),
			strings.ToUpper(result.Status),
			result.ID,
			result.DurationMS,
		)
	}
	report.Resources = shared.releaseAll(ctx, providers)
	report.BuildArtifacts = []BuildArtifact{}
	report.Residual = residual.finalize(ctx, providers)
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
}

// runCase 分配资源、启动 worker 并汇总单条用例结论。
func runCase(ctx context.Context, opts runCaseOptions) CaseResult {
	c := opts.Case
	paths := opts.Layout.CaseArtifactPaths(c.ID)
	result := CaseResult{ID: c.ID, Group: c.Group, Status: StatusError, Artifacts: paths}
	if err := opts.Layout.CreateCaseDir(paths); err != nil {
		result.Message = err.Error()
		return result
	}
	handles, resources, err := acquireCaseResources(ctx, opts)
	result.Resources = resources
	if err != nil {
		result.Message = err.Error()
		releaseExclusive(ctx, opts.Providers, handles, result.Resources)
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
			releaseExclusive(ctx, opts.Providers, handles, result.Resources)
			return result
		}
	}
	observation, err := runWorkerProcess(workerInvocation{
		Executable:      opts.Executable,
		Case:            c,
		Timeout:         timeout,
		Grace:           opts.Grace,
		HandlePath:      handlePath,
		ResultPath:      paths.Result,
		ArtifactDir:     paths.ArtifactDir,
		LogPaths:        paths,
		RealTests:       opts.RealTests.Allowed,
		RealTestsReason: opts.RealTests.Reason,
	})
	if err != nil {
		result.Message = err.Error()
	} else {
		applyObservation(&result, observation)
	}
	releaseExclusive(ctx, opts.Providers, handles, result.Resources)
	return result
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
		results = append(results, ResourceResult{
			Name:        decl.Name,
			Type:        decl.Type,
			Scope:       decl.Scope,
			Fingerprint: handle.Fingerprint,
			Acquired:    true,
		})
	}
	return handles, results, nil
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
type sharedResources struct {
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
	key := decl.Type + "|" + decl.Name
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
	result := ResourceResult{
		Name:        decl.Name,
		Type:        decl.Type,
		Scope:       decl.Scope,
		Fingerprint: handle.Fingerprint,
		Acquired:    true,
	}
	s.entries[key] = &sharedEntry{handle: handle, result: result}
	s.order = append(s.order, key)
	return handle, result, nil
}

// releaseAll 按分配顺序的逆序释放全部共享资源，并逐条自证消失。
//
// 逆序释放是共享资源之间的依赖要求：后分配的资源依赖先分配的连接或服务，必须先撤销。
func (s *sharedResources) releaseAll(ctx context.Context, providers *Providers) []ResourceResult {
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
