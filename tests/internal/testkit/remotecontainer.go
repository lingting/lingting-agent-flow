// Package testkit 提供 AgentFlow 测试统一使用的工作空间与环境辅助组件。
//
// 该文件集中处理真实远程测试容器的生命周期：登记清理责任、删除后核验实际状态、
// 以及在全部测试结束后对远程 docker ps -a 做零残留核验。
//
// 三条硬性语义：
//
//  1. 删除方法返回成功不等于容器已不存在：每次清理都必须重新检查真实状态；
//  2. 无法确认状态（连接不可用、超时、权限错误）一律判定为清理未通过；
//  3. 所有权无法确认时禁止删除，只记录冲突信息并让测试失败。
package testkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// 测试容器清理与最终核验的固定取值。
const (
	// ContainerCleanupTimeout 是单个测试容器清理（停止 + 删除 + 复核）的超时上限。
	ContainerCleanupTimeout = 3 * time.Minute
	// ContainerBaselineTimeout 是测试容器基线快照的超时上限。
	ContainerBaselineTimeout = time.Minute
	// ContainerVerifyTimeout 是包级最终核验的超时上限。
	ContainerVerifyTimeout = 5 * time.Minute
	// containerStopTimeoutSeconds 是删除前停止容器时传给 docker stop 的秒数。
	containerStopTimeoutSeconds = 10
	// containerVerifyMaxListed 是逐条打印远程容器清单的条目上限。
	containerVerifyMaxListed = 50
)

// testContainerNamePrefixes 是可以确认属于测试资源的容器名称前缀。
//
// 生产任务容器的名称由 docker.ContainerNameFor 生成，形如 agentflow-<8 位十六进制>，
// 因此不会与这里的前缀重叠：这里只用于识别测试资源，不能反过来用于识别生产容器。
var testContainerNamePrefixes = []string{"agentflow-test-", "agentflow-codesaver-"}

// IsTestContainerName 判断容器名称是否属于测试资源。
//
// 该判断只用于"报告与核验"，不能单独作为删除依据：删除前仍必须核验所有权。
func IsTestContainerName(name string) bool {
	trimmed := strings.TrimSpace(name)
	for _, prefix := range testContainerNamePrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// ContainerRuntime 是测试容器清理与核验所需的最小 Docker 能力。
//
// *docker.Runner 实现了该接口；单元测试可以用受控替身验证清理语义。
type ContainerRuntime interface {
	InspectContainer(ctx context.Context, dockerID string) (docker.ContainerInspect, error)
	StopContainer(ctx context.Context, dockerID string, timeoutSeconds int) error
	RemoveContainer(ctx context.Context, dockerID string) (docker.RemoveResult, error)
	ListContainers(ctx context.Context, options docker.ListContainersOptions) ([]docker.ContainerListEntry, error)
}

// TrackedContainer 描述一个测试容器的身份与归属证据。
type TrackedContainer struct {
	// Name 是容器名称。已知时在创建之前就该提供，使创建失败也能按名称找回。
	Name string
	// DockerID 是实际 Docker 容器 ID，创建成功后立即补写。
	DockerID string
	// Record 是容器登记记录，用于 docker.VerifyOwnership 归属核验。
	//
	// 为空时按容器名称核验归属；两者都无法提供时禁止删除。
	Record *domain.Container
	// Resolve 在清理时才解析容器身份（例如由生产服务创建、名称不预先确定的容器）。
	Resolve func(ctx context.Context) ([]TrackedContainer, error)
}

// ContainerRun 记录一次测试运行创建的容器，负责逐容器清理与包级最终核验。
//
// 每个测试包创建自己的 ContainerRun，并在 TestMain 里通过
// RegisterContainerFinalizer 注册最终核验。
type ContainerRun struct {
	// label 是本次运行的标识，出现在清理与核验日志中。
	label string

	mu           sync.Mutex
	entries      []*containerEntry
	baseline     []docker.ContainerListEntry
	baselineErr  error
	baselineDone bool
}

// containerEntry 是一个已登记的清理责任。
type containerEntry struct {
	runtime  ContainerRuntime
	resolve  func(ctx context.Context) ([]TrackedContainer, error)
	resolved bool

	mu      sync.Mutex
	targets []*TrackedContainer
}

// NewContainerRun 创建一个运行级别的测试容器登记表。
func NewContainerRun(label string) *ContainerRun {
	return &ContainerRun{label: strings.TrimSpace(label)}
}

// Label 返回本次运行的标识。
func (r *ContainerRun) Label() string {
	if r == nil {
		return ""
	}
	return r.label
}

// Track 登记一个测试容器的清理责任，并在用例结束时清理并核验。
//
// 调用时机固定：在容器创建之前（名称已确定）或取得 Docker ID 之后的第一时间。
// 返回的指针可以在创建成功后补写 DockerID。
func (r *ContainerRun) Track(
	t *testing.T,
	runtime ContainerRuntime,
	target TrackedContainer,
) *TrackedContainer {
	t.Helper()
	if runtime == nil {
		t.Fatalf("缺少 Docker 执行器，无法登记测试容器清理责任")
	}
	stored := target
	entry := &containerEntry{
		runtime: runtime,
		resolve: target.Resolve,
		targets: []*TrackedContainer{&stored},
	}
	r.register(entry)
	r.captureBaseline(t, runtime)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), ContainerCleanupTimeout)
		defer cancel()
		if err := r.cleanupEntry(ctx, entry); err != nil {
			t.Errorf("测试容器清理未通过（运行 %s）: %v", r.label, err)
		}
	})
	return &stored
}

// register 把登记项加入本次运行。
func (r *ContainerRun) register(entry *containerEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry)
}

// captureBaseline 在本次运行第一次登记容器之前记录远程容器清单。
//
// 基线用于区分"本次运行创建"与"运行前就存在的历史残留"，也用于确认原有非测试
// 容器没有被删除。快照失败必须让测试失败：没有基线就无法证明零残留。
func (r *ContainerRun) captureBaseline(t *testing.T, runtime ContainerRuntime) {
	t.Helper()
	r.mu.Lock()
	if r.baselineDone {
		r.mu.Unlock()
		return
	}
	r.baselineDone = true
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), ContainerBaselineTimeout)
	defer cancel()
	entries, err := runtime.ListContainers(ctx, docker.ListContainersOptions{All: true})
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.baselineErr = err
		t.Errorf("读取远程容器基线失败，无法证明测试容器零残留: %v", err)
		return
	}
	r.baseline = entries
}

// entryTargets 返回登记项当前已知的容器身份，必要时调用延迟解析。
func (e *containerEntry) entryTargets(ctx context.Context) ([]TrackedContainer, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.resolve != nil && !e.resolved {
		resolved, err := e.resolve(ctx)
		if err != nil {
			return nil, fmt.Errorf("解析待清理容器身份失败: %w", err)
		}
		for index := range resolved {
			e.targets = append(e.targets, &resolved[index])
		}
		e.resolved = true
	}
	targets := make([]TrackedContainer, 0, len(e.targets))
	for _, target := range e.targets {
		targets = append(targets, *target)
	}
	return targets, nil
}

// cleanupEntry 清理并核验一个登记项。
func (r *ContainerRun) cleanupEntry(ctx context.Context, entry *containerEntry) error {
	targets, err := entry.entryTargets(ctx)
	if err != nil {
		return err
	}
	var failures []string
	for _, target := range targets {
		if strings.TrimSpace(target.Name) == "" && strings.TrimSpace(target.DockerID) == "" {
			continue
		}
		cleanupErr := CleanupContainer(ctx, entry.runtime, target)
		report := fmt.Sprintf(
			"REMOTE_CONTAINER_CLEANUP run=%s name=%s docker_id=%s",
			r.label,
			target.Name,
			target.DockerID,
		)
		if cleanupErr != nil {
			report += " result=failed"
			failures = append(failures, fmt.Sprintf("%s: %v", containerLabel(target), cleanupErr))
		} else {
			report += " result=absent"
		}
		fmt.Fprintln(os.Stdout, report)
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

// containerLabel 返回容器在日志中的可读标识。
func containerLabel(target TrackedContainer) string {
	name := strings.TrimSpace(target.Name)
	id := strings.TrimSpace(target.DockerID)
	switch {
	case name != "" && id != "":
		return name + "/" + id
	case name != "":
		return name
	case id != "":
		return id
	default:
		return "<未知容器>"
	}
}

// CleanupContainer 停止并删除一个测试容器，并核验它确实已不存在。
//
// 语义固定：
//   - 删除前必须核验所有权，核验失败时禁止删除并返回错误；
//   - 容器本来就不存在视为已达清理目标（幂等）；
//   - 删除结果不足以判定成功，必须重新检查真实状态；
//   - 无法确认状态（连接不可用、超时、权限错误）一律返回错误。
func CleanupContainer(
	ctx context.Context,
	runtime ContainerRuntime,
	target TrackedContainer,
) error {
	if runtime == nil {
		return errors.New("缺少 Docker 执行器，无法清理测试容器")
	}
	name := strings.TrimSpace(target.Name)
	dockerID := strings.TrimSpace(target.DockerID)
	if name == "" && dockerID == "" {
		return errors.New("测试容器缺少名称与 Docker ID，无法核验清理结果")
	}
	inspect, found, err := locateContainer(ctx, runtime, name, dockerID)
	if err != nil {
		return fmt.Errorf("清理前无法确认容器状态: %w", err)
	}
	if !found {
		return nil
	}
	if ownershipErr := verifyTestContainerOwnership(inspect, target); ownershipErr != nil {
		return fmt.Errorf("拒绝删除 %s：所有权核验失败: %w", containerLabel(target), ownershipErr)
	}
	observedID := inspect.ID
	if inspect.Running {
		// 停止失败不直接判定失败：删除会强制停止，最终结论以复核结果为准。
		_ = runtime.StopContainer(ctx, observedID, containerStopTimeoutSeconds)
	}
	_, removeErr := runtime.RemoveContainer(ctx, observedID)
	if removeErr != nil && docker.ErrorCodeOf(removeErr) != docker.CodeContainerNotFound {
		// 删除报错同样不直接判定失败，但错误会随复核结果一起报告。
		fmt.Fprintf(
			os.Stdout,
			"WARN 删除测试容器 %s 返回错误，将按实际状态判定: %v\n",
			observedID,
			removeErr,
		)
	}
	after, stillFound, verifyErr := locateContainer(ctx, runtime, name, observedID)
	if verifyErr != nil {
		return fmt.Errorf("删除后无法确认容器状态: %w", verifyErr)
	}
	if stillFound {
		return fmt.Errorf(
			"容器 %s 仍然存在（状态 %s），清理未完成",
			observedID,
			after.State,
		)
	}
	return nil
}

// locateContainer 按 Docker ID 与名称定位容器，返回真实观测结果。
//
// 优先使用准确的 Docker ID；ID 已不存在时再按名称查找，覆盖"创建返回 ID 与实际
// 名称不完全对应"的极端情况。无法确认状态时返回错误，不做猜测。
func locateContainer(
	ctx context.Context,
	runtime ContainerRuntime,
	name, dockerID string,
) (docker.ContainerInspect, bool, error) {
	if dockerID != "" {
		inspect, err := runtime.InspectContainer(ctx, dockerID)
		if err == nil {
			return inspect, true, nil
		}
		if docker.ErrorCodeOf(err) != docker.CodeContainerNotFound {
			return docker.ContainerInspect{}, false, err
		}
	}
	if name == "" {
		return docker.ContainerInspect{}, false, nil
	}
	entries, err := runtime.ListContainers(ctx, docker.ListContainersOptions{
		All:         true,
		NameFilters: []string{name},
	})
	if err != nil {
		return docker.ContainerInspect{}, false, err
	}
	for _, entry := range entries {
		// Docker 的 name 过滤是子串匹配，必须再核对全名。
		if entry.Name != name {
			continue
		}
		inspect, inspectErr := runtime.InspectContainer(ctx, entry.ID)
		if inspectErr != nil {
			if docker.ErrorCodeOf(inspectErr) == docker.CodeContainerNotFound {
				continue
			}
			return docker.ContainerInspect{}, false, inspectErr
		}
		return inspect, true, nil
	}
	return docker.ContainerInspect{}, false, nil
}

// verifyTestContainerOwnership 在删除前核验容器归属。
func verifyTestContainerOwnership(inspect docker.ContainerInspect, target TrackedContainer) error {
	if target.Record != nil {
		return docker.VerifyOwnership(inspect, *target.Record)
	}
	name := strings.TrimSpace(target.Name)
	if name == "" {
		return errors.New("缺少容器登记记录与名称，无法核验所有权")
	}
	if inspect.Name != name {
		return fmt.Errorf("容器名称与登记不符: 期望 %q，实际 %q", name, inspect.Name)
	}
	return nil
}

// ContainerFinalizer 是所有真实远程测试结束时执行的容器零残留核验。
type ContainerFinalizer struct {
	// Run 是本次运行登记的容器集合。
	Run *ContainerRun
	// Runtime 是用于核验的 Docker 执行器。
	Runtime ContainerRuntime
}

// Finalize 核验本次运行创建的测试容器是否已经全部从远程 Docker 消失。
//
// 核验使用包含已停止容器的容器清单（docker ps -a），并区分三种结论：
//   - 本次运行仍有登记容器存在 → 失败；
//   - 出现未登记、但明确属于测试资源的新容器 → 失败（无法确认归属，不删除）；
//   - 所有登记容器都已消失且没有未登记残留 → 通过。
func (f *ContainerFinalizer) Finalize(ctx context.Context) error {
	if f == nil || f.Run == nil || f.Runtime == nil {
		return errors.New("最终核验缺少运行登记或 Docker 执行器")
	}
	run := f.Run
	run.mu.Lock()
	label := run.label
	baseline := append([]docker.ContainerListEntry(nil), run.baseline...)
	baselineErr := run.baselineErr
	baselineDone := run.baselineDone
	entries := append([]*containerEntry(nil), run.entries...)
	run.mu.Unlock()

	if len(entries) == 0 {
		// 本次运行没有创建任何测试容器（例如只运行了不创建容器的用例，或全部跳过）：
		// 没有可核验的容器，如实报告 SKIP，既不算通过也不判失败。
		fmt.Fprintf(
			os.Stdout,
			"REMOTE_CONTAINER_VERIFY run=%s status=SKIP reason=no_test_container_created\n",
			label,
		)
		return nil
	}
	if !baselineDone || baselineErr != nil {
		return fmt.Errorf("缺少运行前的容器基线，无法证明零残留: %v", baselineErr)
	}
	var targets []TrackedContainer
	for _, entry := range entries {
		resolved, err := entry.entryTargets(ctx)
		if err != nil {
			return err
		}
		targets = append(targets, resolved...)
	}
	snapshot, err := f.Runtime.ListContainers(ctx, docker.ListContainersOptions{All: true})
	if err != nil {
		return fmt.Errorf("读取远程容器清单失败，无法确认零残留: %w", err)
	}

	reportContainerEvidence(label, baseline, snapshot, len(targets))
	issues := make([]string, 0, 4)
	issues = append(issues, f.verifyTrackedContainers(ctx, label, targets)...)
	issues = append(issues, verifyUntrackedTestContainers(label, baseline, snapshot, targets)...)
	reportBaselineResidue(label, baseline, snapshot)
	reportDisappearedBaseline(label, baseline, snapshot)

	if len(issues) > 0 {
		for _, issue := range issues {
			fmt.Fprintf(os.Stdout, "REMOTE_CONTAINER_RESIDUE run=%s %s\n", label, issue)
		}
		fmt.Fprintf(os.Stdout, "REMOTE_CONTAINER_VERIFY run=%s status=FAIL\n", label)
		return fmt.Errorf("测试容器零残留核验未通过: %s", strings.Join(issues, "; "))
	}
	fmt.Fprintf(os.Stdout, "REMOTE_CONTAINER_VERIFY run=%s status=PASS\n", label)
	return nil
}

// verifyTrackedContainers 逐个核验本次运行登记的容器已经不存在。
func (f *ContainerFinalizer) verifyTrackedContainers(
	ctx context.Context,
	label string,
	targets []TrackedContainer,
) []string {
	issues := make([]string, 0, len(targets))
	for _, target := range targets {
		name := strings.TrimSpace(target.Name)
		dockerID := strings.TrimSpace(target.DockerID)
		if name == "" && dockerID == "" {
			continue
		}
		inspect, found, err := locateContainer(ctx, f.Runtime, name, dockerID)
		if err != nil {
			issues = append(issues, fmt.Sprintf(
				"运行 %s 的容器 %s 状态无法确认: %v",
				label,
				containerLabel(target),
				err,
			))
			continue
		}
		if found {
			issues = append(issues, fmt.Sprintf(
				"运行 %s 登记的容器 %s 仍然存在（ID %s，状态 %s）",
				label,
				containerLabel(target),
				inspect.ID,
				inspect.State,
			))
		}
	}
	return issues
}

// verifyUntrackedTestContainers 检查是否出现未登记但明确属于测试资源的新容器。
//
// 这类容器不会被删除：无法确认归属时删除可能破坏其他测试运行或生产资源。
func verifyUntrackedTestContainers(
	label string,
	baseline, snapshot []docker.ContainerListEntry,
	targets []TrackedContainer,
) []string {
	baselineNames := make(map[string]struct{}, len(baseline))
	for _, entry := range baseline {
		baselineNames[entry.Name] = struct{}{}
	}
	trackedNames := make(map[string]struct{}, len(targets))
	trackedIDs := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if name := strings.TrimSpace(target.Name); name != "" {
			trackedNames[name] = struct{}{}
		}
		if id := strings.TrimSpace(target.DockerID); id != "" {
			trackedIDs[id] = struct{}{}
		}
	}
	issues := make([]string, 0, 2)
	for _, entry := range snapshot {
		if !IsTestContainerName(entry.Name) {
			continue
		}
		if _, ok := trackedNames[entry.Name]; ok {
			continue
		}
		if _, ok := trackedIDs[entry.ID]; ok {
			continue
		}
		if _, ok := baselineNames[entry.Name]; ok {
			// 运行前就存在的历史残留：只报告，不处置。
			continue
		}
		issues = append(issues, fmt.Sprintf(
			"出现未登记的测试容器 %s（ID %s），无法确认归属，未删除",
			entry.Name,
			entry.ID,
		))
	}
	return issues
}

// reportContainerEvidence 输出测试前后容器清单的证据与差异。
func reportContainerEvidence(
	label string,
	baseline, snapshot []docker.ContainerListEntry,
	trackedCount int,
) {
	fmt.Fprintf(
		os.Stdout,
		"REMOTE_CONTAINER_EVIDENCE run=%s baseline=%d final=%d tracked=%d\n",
		label,
		len(baseline),
		len(snapshot),
		trackedCount,
	)
	printContainerList("REMOTE_CONTAINER_BASELINE", label, baseline)
	printContainerList("REMOTE_CONTAINER_FINAL", label, snapshot)
}

// printContainerList 逐条打印容器清单，条目过多时只打印数量。
func printContainerList(kind, label string, entries []docker.ContainerListEntry) {
	if len(entries) > containerVerifyMaxListed {
		fmt.Fprintf(
			os.Stdout,
			"%s run=%s total=%d detail=truncated\n",
			kind,
			label,
			len(entries),
		)
		return
	}
	sorted := make([]docker.ContainerListEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left].Name < sorted[right].Name })
	for _, entry := range sorted {
		fmt.Fprintf(os.Stdout, "%s run=%s id=%s name=%s\n", kind, label, entry.ID, entry.Name)
	}
}

// reportBaselineResidue 报告运行前就存在的测试容器残留。
//
// 这些容器不属于本次运行，因此只报告、不删除；需要人工确认后再处置。
func reportBaselineResidue(label string, baseline, snapshot []docker.ContainerListEntry) {
	finalNames := make(map[string]struct{}, len(snapshot))
	for _, entry := range snapshot {
		finalNames[entry.Name] = struct{}{}
	}
	for _, entry := range baseline {
		if !IsTestContainerName(entry.Name) {
			continue
		}
		suffix := "retained"
		if _, ok := finalNames[entry.Name]; !ok {
			suffix = "gone"
		}
		fmt.Fprintf(
			os.Stdout,
			"REMOTE_CONTAINER_PREEXISTING run=%s id=%s name=%s status=%s\n",
			label,
			entry.ID,
			entry.Name,
			suffix,
		)
	}
}

// reportDisappearedBaseline 报告运行前存在、运行后消失的非测试容器。
//
// 测试只会删除本次登记并核验过所有权的容器，因此这里的差异不代表测试误删：
// 运行契约探测等一次性容器（docker run --rm）也会产生同样的差异。输出该差异的
// 目的是让"原有容器是否被误删"可以复核，而不是判定失败。
func reportDisappearedBaseline(label string, baseline, snapshot []docker.ContainerListEntry) {
	finalNames := make(map[string]struct{}, len(snapshot))
	for _, entry := range snapshot {
		finalNames[entry.Name] = struct{}{}
	}
	for _, entry := range baseline {
		if IsTestContainerName(entry.Name) {
			continue
		}
		if _, ok := finalNames[entry.Name]; ok {
			continue
		}
		fmt.Fprintf(
			os.Stdout,
			"WARN REMOTE_CONTAINER_BASELINE_GONE run=%s id=%s name=%s"+
				"（非测试容器，可能是运行契约探测等一次性容器，需人工确认未被误删）\n",
			label,
			entry.ID,
			entry.Name,
		)
	}
}
