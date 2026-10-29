package testkit_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// fakeContainerRuntime 是 ContainerRuntime 的可控替身。
//
// 它只模拟远程 Docker 的观测结果，使"删除返回成功但容器仍在""检查因连接错误无法
// 确认"等分支可以被稳定复现，不依赖真实远程机器。
type fakeContainerRuntime struct {
	mu         sync.Mutex
	containers map[string]*fakeContainerState

	// 注入的错误。
	listErr    error
	inspectErr error
	stopErr    error
	removeErr  error
	// ignoreRemove 表示删除返回成功但没有真正删除容器。
	ignoreRemove bool
	// deleteBeforeError 表示删除先真正生效，再返回错误。
	deleteBeforeError bool

	stopCalls   int
	removeCalls int
}

// fakeContainerState 是一个受控的远程容器状态。
type fakeContainerState struct {
	id      string
	name    string
	running bool
	labels  map[string]string
}

// newFakeContainerRuntime 创建一个空的受控 Docker 替身。
func newFakeContainerRuntime() *fakeContainerRuntime {
	return &fakeContainerRuntime{containers: make(map[string]*fakeContainerState)}
}

// add 登记一个远程容器。
func (f *fakeContainerRuntime) add(state fakeContainerState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers[state.id] = &state
}

// has 判断容器是否仍然存在。
func (f *fakeContainerRuntime) has(dockerID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.containers[dockerID]
	return ok
}

// InspectContainer 返回受控的容器观测结果。
func (f *fakeContainerRuntime) InspectContainer(
	ctx context.Context,
	dockerID string,
) (docker.ContainerInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspectErr != nil {
		return docker.ContainerInspect{}, f.inspectErr
	}
	state, ok := f.containers[dockerID]
	if !ok {
		return docker.ContainerInspect{}, &docker.Error{
			Code:   docker.CodeContainerNotFound,
			Op:     "docker inspect",
			Detail: "容器不存在",
		}
	}
	containerState := domain.DockerStateExited
	if state.running {
		containerState = domain.DockerStateRunning
	}
	return docker.ContainerInspect{
		ID:      state.id,
		Name:    state.name,
		State:   containerState,
		Running: state.running,
		Labels:  state.labels,
	}, nil
}

// StopContainer 把容器置为已停止。
func (f *fakeContainerRuntime) StopContainer(
	ctx context.Context,
	dockerID string,
	timeoutSeconds int,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	if f.stopErr != nil {
		return f.stopErr
	}
	if state, ok := f.containers[dockerID]; ok {
		state.running = false
	}
	return nil
}

// RemoveContainer 删除容器，或按注入的行为只返回成功。
func (f *fakeContainerRuntime) RemoveContainer(
	ctx context.Context,
	dockerID string,
) (docker.RemoveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls++
	if f.removeErr != nil {
		if f.deleteBeforeError {
			delete(f.containers, dockerID)
		}
		return docker.RemoveResult{}, f.removeErr
	}
	if _, ok := f.containers[dockerID]; !ok {
		return docker.RemoveResult{Removed: true, Existed: false}, nil
	}
	if !f.ignoreRemove {
		delete(f.containers, dockerID)
	}
	return docker.RemoveResult{Removed: true, Existed: true}, nil
}

// ListContainers 返回受控的容器清单。
func (f *fakeContainerRuntime) ListContainers(
	ctx context.Context,
	options docker.ListContainersOptions,
) ([]docker.ContainerListEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	entries := make([]docker.ContainerListEntry, 0, len(f.containers))
	for _, state := range f.containers {
		if !options.All && !state.running {
			continue
		}
		if !matchNameFilters(state.name, options.NameFilters) {
			continue
		}
		entries = append(entries, docker.ContainerListEntry{ID: state.id, Name: state.name})
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name < entries[right].Name })
	return entries, nil
}

// matchNameFilters 按 Docker 语义做名称子串匹配。
func matchNameFilters(name string, filters []string) bool {
	for _, filter := range filters {
		if !strings.Contains(name, filter) {
			return false
		}
	}
	return true
}

// newManagedTestContainer 构造一个带完整所有权标签的测试容器现场。
func newManagedTestContainer(t *testing.T, dockerID, name string) (domain.Container, fakeContainerState) {
	t.Helper()
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造环境引用失败: %v", err)
	}
	record, err := domain.NewContainer(
		domain.NewID(),
		domain.ID("remote-test-host"),
		name,
		ref,
		"v0001",
		strings.Repeat("a", 64),
		"sha256:"+strings.Repeat("b", 64),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	record.DockerID = dockerID
	state := fakeContainerState{
		id:      dockerID,
		name:    name,
		running: true,
		labels: map[string]string{
			docker.OwnershipLabelKey: "true",
			docker.HostLabelKey:      record.HostID.String(),
			docker.ContainerLabelKey: record.ID.String(),
		},
	}
	return record, state
}

// TestCleanupContainerRemovesNotStartedContainer 验证"创建成功但未启动/启动失败"也能清理。
func TestCleanupContainerRemovesNotStartedContainer(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	record, state := newManagedTestContainer(t, "docker-created", "agentflow-test-created")
	state.running = false
	runtime.add(state)

	err := testkit.CleanupContainer(context.Background(), runtime, testkit.TrackedContainer{
		Name:     record.Name,
		DockerID: record.DockerID,
		Record:   &record,
	})
	if err != nil {
		t.Fatalf("未启动的测试容器应能被清理: %v", err)
	}
	if runtime.has("docker-created") {
		t.Fatal("清理后容器必须已不存在")
	}
}

// TestCleanupContainerStopsRunningContainer 验证运行中的容器先停止再删除。
func TestCleanupContainerStopsRunningContainer(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	record, state := newManagedTestContainer(t, "docker-running", "agentflow-test-running")
	runtime.add(state)

	if err := testkit.CleanupContainer(context.Background(), runtime, testkit.TrackedContainer{
		Name:     record.Name,
		DockerID: record.DockerID,
		Record:   &record,
	}); err != nil {
		t.Fatalf("运行中的测试容器应能被清理: %v", err)
	}
	if runtime.stopCalls != 1 {
		t.Fatalf("运行中的容器应先停止 1 次，实际 %d 次", runtime.stopCalls)
	}
	if runtime.has("docker-running") {
		t.Fatal("清理后容器必须已不存在")
	}
}

// TestCleanupContainerSucceedsWhenStopFails 验证停止失败时以删除与复核结果判定。
func TestCleanupContainerSucceedsWhenStopFails(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	record, state := newManagedTestContainer(t, "docker-stopfail", "agentflow-test-stopfail")
	runtime.add(state)
	runtime.stopErr = errors.New("注入的停止失败")

	if err := testkit.CleanupContainer(context.Background(), runtime, testkit.TrackedContainer{
		Name:     record.Name,
		DockerID: record.DockerID,
		Record:   &record,
	}); err != nil {
		t.Fatalf("停止失败但删除成功后应以真实状态判定成功: %v", err)
	}
	if runtime.has("docker-stopfail") {
		t.Fatal("删除成功后容器必须已不存在")
	}
}

// TestCleanupContainerIsIdempotent 验证容器已被业务逻辑删除时清理仍然成功。
func TestCleanupContainerIsIdempotent(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	record, _ := newManagedTestContainer(t, "docker-gone", "agentflow-test-gone")

	err := testkit.CleanupContainer(context.Background(), runtime, testkit.TrackedContainer{
		Name:     record.Name,
		DockerID: record.DockerID,
		Record:   &record,
	})
	if err != nil {
		t.Fatalf("容器已不存在时清理必须幂等成功: %v", err)
	}
	if runtime.removeCalls != 0 {
		t.Fatalf("容器不存在时不应发出删除请求，实际 %d 次", runtime.removeCalls)
	}
}

// TestCleanupContainerVerifiesPostCondition 验证删除结果不足以证明清理成功。
func TestCleanupContainerVerifiesPostCondition(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	record, state := newManagedTestContainer(t, "docker-survives", "agentflow-test-survives")
	runtime.add(state)
	runtime.ignoreRemove = true

	err := testkit.CleanupContainer(context.Background(), runtime, testkit.TrackedContainer{
		Name:     record.Name,
		DockerID: record.DockerID,
		Record:   &record,
	})
	if err == nil {
		t.Fatal("删除后容器仍存在时必须判定清理失败")
	}
	if !strings.Contains(err.Error(), "仍然存在") {
		t.Fatalf("错误信息应说明容器仍然存在，实际: %v", err)
	}
	if !runtime.has("docker-survives") {
		t.Fatal("替身本应保留容器用于验证后置条件")
	}
}

// TestCleanupContainerAcceptsRemoveErrorWhenContainerIsGone 验证以真实状态而非删除返回值判定。
func TestCleanupContainerAcceptsRemoveErrorWhenContainerIsGone(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	record, state := newManagedTestContainer(t, "docker-removed", "agentflow-test-removed")
	runtime.add(state)
	// 删除调用返回错误，但容器实际已消失：结论必须以真实状态为依据，而不是删除返回值。
	runtime.deleteBeforeError = true
	runtime.removeErr = &docker.Error{
		Code:   docker.CodeStateUncertain,
		Op:     "docker rm",
		Detail: "删除结果不确定",
	}
	if err := testkit.CleanupContainer(context.Background(), runtime, testkit.TrackedContainer{
		Name:     record.Name,
		DockerID: record.DockerID,
		Record:   &record,
	}); err != nil {
		t.Fatalf("容器已确认不存在时清理应成功: %v", err)
	}
}

// TestCleanupContainerFailsWhenStateUnknown 验证状态无法确认时不得判定清理成功。
func TestCleanupContainerFailsWhenStateUnknown(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	record, state := newManagedTestContainer(t, "docker-unknown", "agentflow-test-unknown")
	runtime.add(state)
	runtime.inspectErr = &docker.Error{
		Code:   docker.CodeSSHUnavailable,
		Op:     "docker inspect",
		Detail: "注入的连接不可用",
		Cause:  errors.New("注入的连接不可用"),
	}

	err := testkit.CleanupContainer(context.Background(), runtime, testkit.TrackedContainer{
		Name:     record.Name,
		DockerID: record.DockerID,
		Record:   &record,
	})
	if err == nil {
		t.Fatal("状态无法确认时必须判定清理失败")
	}
	if !strings.Contains(err.Error(), "无法确认") {
		t.Fatalf("错误信息应说明无法确认状态，实际: %v", err)
	}
	if !runtime.has("docker-unknown") {
		t.Fatal("无法确认状态时不得删除容器")
	}
}

// TestCleanupContainerRefusesOwnershipMismatch 验证所有权不符时禁止删除。
func TestCleanupContainerRefusesOwnershipMismatch(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	mine, state := newManagedTestContainer(t, "docker-other", "agentflow-test-other")
	runtime.add(state)
	// 用另一个登记记录去清理：标签必然不匹配。
	other := mine
	other.ID = domain.NewID()

	err := testkit.CleanupContainer(context.Background(), runtime, testkit.TrackedContainer{
		Name:     mine.Name,
		DockerID: mine.DockerID,
		Record:   &other,
	})
	if err == nil {
		t.Fatal("所有权不符时必须拒绝删除并返回错误")
	}
	if !strings.Contains(err.Error(), "所有权") {
		t.Fatalf("错误信息应说明所有权核验失败，实际: %v", err)
	}
	if runtime.removeCalls != 0 {
		t.Fatalf("所有权不符时不得发出删除请求，实际 %d 次", runtime.removeCalls)
	}
	if !runtime.has("docker-other") {
		t.Fatal("所有权不符的容器必须保留")
	}
}

// TestCleanupContainerFindsContainerByName 验证未取得 Docker ID 时按名称找回容器。
func TestCleanupContainerFindsContainerByName(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	_, state := newManagedTestContainer(t, "docker-byname", "agentflow-test-byname")
	runtime.add(state)

	err := testkit.CleanupContainer(context.Background(), runtime, testkit.TrackedContainer{
		Name: "agentflow-test-byname",
	})
	if err != nil {
		t.Fatalf("按名称清理应成功: %v", err)
	}
	if runtime.has("docker-byname") {
		t.Fatal("按名称清理后容器必须已不存在")
	}
}

// TestCleanupContainerRequiresIdentity 验证缺少名称与 ID 时拒绝执行。
func TestCleanupContainerRequiresIdentity(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	if err := testkit.CleanupContainer(
		context.Background(), runtime, testkit.TrackedContainer{},
	); err == nil {
		t.Fatal("缺少名称与 Docker ID 时必须返回错误")
	}
}

// TestContainerRunTrackCleansUpOnTestEnd 验证登记后用例结束即完成清理与核验。
func TestContainerRunTrackCleansUpOnTestEnd(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	record, state := newManagedTestContainer(t, "docker-tracked", "agentflow-test-tracked")
	runtime.add(state)
	run := testkit.NewContainerRun("unit/tracked")

	tracked := false
	t.Run("登记并清理", func(t *testing.T) {
		target := run.Track(t, runtime, testkit.TrackedContainer{
			Name:     record.Name,
			DockerID: record.DockerID,
			Record:   &record,
		})
		if target == nil {
			t.Fatal("登记应返回可补写 Docker ID 的句柄")
		}
		tracked = true
	})
	if !tracked {
		t.Fatal("子用例未执行")
	}
	if runtime.has("docker-tracked") {
		t.Fatal("用例结束时应已删除测试容器")
	}
}

// TestContainerFinalizerPassesWhenTrackedContainersAreGone 验证零残留时最终核验通过。
func TestContainerFinalizerPassesWhenTrackedContainersAreGone(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	record, state := newManagedTestContainer(t, "docker-final-pass", "agentflow-test-final-pass")
	runtime.add(state)
	run := testkit.NewContainerRun("unit/final-pass")
	t.Run("登记并清理", func(t *testing.T) {
		run.Track(t, runtime, testkit.TrackedContainer{
			Name:     record.Name,
			DockerID: record.DockerID,
			Record:   &record,
		})
	})
	finalizer := testkit.ContainerFinalizer{Run: run, Runtime: runtime}
	if err := finalizer.Finalize(context.Background()); err != nil {
		t.Fatalf("所有登记容器都已消失时最终核验应通过: %v", err)
	}
}

// TestContainerFinalizerFailsOnLeftoverContainer 验证残留登记容器会让最终核验失败。
func TestContainerFinalizerFailsOnLeftoverContainer(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	record, state := newManagedTestContainer(t, "docker-final-left", "agentflow-test-final-left")
	run := testkit.NewContainerRun("unit/final-left")
	t.Run("登记但不清理", func(t *testing.T) {
		run.Track(t, runtime, testkit.TrackedContainer{
			Name:     record.Name,
			DockerID: record.DockerID,
			Record:   &record,
		})
	})
	// 清理阶段已经删除了该容器；这里重新放回，验证最终核验本身能发现残留。
	runtime.add(state)
	finalizer := testkit.ContainerFinalizer{Run: run, Runtime: runtime}
	err := finalizer.Finalize(context.Background())
	if err == nil {
		t.Fatal("存在登记的残留容器时最终核验必须失败")
	}
	if !strings.Contains(err.Error(), "仍然存在") {
		t.Fatalf("错误信息应说明容器仍然存在，实际: %v", err)
	}
}

// TestContainerFinalizerFailsOnUntrackedTestContainer 验证未登记的测试容器残留会让核验失败。
func TestContainerFinalizerFailsOnUntrackedTestContainer(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	run := testkit.NewContainerRun("unit/final-untracked")
	record, state := newManagedTestContainer(t, "docker-base", "agentflow-test-base")
	runtime.add(state)
	t.Run("建立基线并清理", func(t *testing.T) {
		run.Track(t, runtime, testkit.TrackedContainer{
			Name:     record.Name,
			DockerID: record.DockerID,
			Record:   &record,
		})
	})
	// 基线之后出现一个未登记的测试容器：归属无法确认，必须报告为残留且不删除。
	runtime.add(fakeContainerState{
		id:      "docker-untracked",
		name:    "agentflow-test-untracked",
		running: false,
		labels:  map[string]string{docker.OwnershipLabelKey: "true"},
	})
	finalizer := testkit.ContainerFinalizer{Run: run, Runtime: runtime}
	err := finalizer.Finalize(context.Background())
	if err == nil {
		t.Fatal("出现未登记测试容器时最终核验必须失败")
	}
	if !strings.Contains(err.Error(), "未登记") {
		t.Fatalf("错误信息应说明出现未登记容器，实际: %v", err)
	}
	if !runtime.has("docker-untracked") {
		t.Fatal("归属无法确认的容器不得被删除")
	}
}

// TestContainerFinalizerReportsPreexistingResidue 验证运行前的历史残留只报告不处置。
func TestContainerFinalizerReportsPreexistingResidue(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	legacy := fakeContainerState{
		id:      "docker-legacy",
		name:    "agentflow-test-legacy",
		running: false,
		labels:  map[string]string{docker.OwnershipLabelKey: "true"},
	}
	runtime.add(legacy)
	run := testkit.NewContainerRun("unit/final-legacy")
	record, state := newManagedTestContainer(t, "docker-own", "agentflow-test-own")
	runtime.add(state)
	t.Run("建立基线并清理", func(t *testing.T) {
		run.Track(t, runtime, testkit.TrackedContainer{
			Name:     record.Name,
			DockerID: record.DockerID,
			Record:   &record,
		})
	})
	finalizer := testkit.ContainerFinalizer{Run: run, Runtime: runtime}
	if err := finalizer.Finalize(context.Background()); err != nil {
		t.Fatalf("历史残留不属于本次运行，不得让核验失败: %v", err)
	}
	if !runtime.has("docker-legacy") {
		t.Fatal("运行前的历史残留不得被自动删除")
	}
}

// TestContainerFinalizerSkipsWhenNothingCreated 验证没有创建容器时如实报告跳过。
//
// 只运行不创建容器的用例（例如只跑远程前置检查）不应因为缺少基线而被判失败。
func TestContainerFinalizerSkipsWhenNothingCreated(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	runtime.add(fakeContainerState{id: "docker-prod", name: "pgsql-server", running: true})
	run := testkit.NewContainerRun("unit/final-nothing")
	finalizer := testkit.ContainerFinalizer{Run: run, Runtime: runtime}
	if err := finalizer.Finalize(context.Background()); err != nil {
		t.Fatalf("没有登记容器时不应判定失败: %v", err)
	}
	if !runtime.has("docker-prod") {
		t.Fatal("没有创建容器时不得触碰任何已有容器")
	}
}

// TestContainerFinalizerKeepsNonTestContainers 验证生产/其他容器不被误删。
func TestContainerFinalizerKeepsNonTestContainers(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	production := fakeContainerState{
		id:      "docker-pgsql",
		name:    "pgsql-server",
		running: true,
		labels:  map[string]string{},
	}
	runtime.add(production)
	run := testkit.NewContainerRun("unit/final-production")
	record, state := newManagedTestContainer(t, "docker-own2", "agentflow-test-own2")
	runtime.add(state)
	t.Run("建立基线并清理", func(t *testing.T) {
		run.Track(t, runtime, testkit.TrackedContainer{
			Name:     record.Name,
			DockerID: record.DockerID,
			Record:   &record,
		})
	})
	finalizer := testkit.ContainerFinalizer{Run: run, Runtime: runtime}
	if err := finalizer.Finalize(context.Background()); err != nil {
		t.Fatalf("最终核验不应因非测试容器失败: %v", err)
	}
	if !runtime.has("docker-pgsql") {
		t.Fatal("非测试容器不得被删除")
	}
}

// TestContainerFinalizerFailsWhenListingFails 验证无法读取清单时不得判定零残留。
func TestContainerFinalizerFailsWhenListingFails(t *testing.T) {
	t.Parallel()
	runtime := newFakeContainerRuntime()
	record, state := newManagedTestContainer(t, "docker-list", "agentflow-test-list")
	runtime.add(state)
	run := testkit.NewContainerRun("unit/final-list")
	t.Run("建立基线并清理", func(t *testing.T) {
		run.Track(t, runtime, testkit.TrackedContainer{
			Name:     record.Name,
			DockerID: record.DockerID,
			Record:   &record,
		})
	})
	runtime.listErr = &docker.Error{
		Code:   docker.CodeSSHUnavailable,
		Op:     "docker ps",
		Detail: "注入的连接不可用",
	}
	finalizer := testkit.ContainerFinalizer{Run: run, Runtime: runtime}
	if err := finalizer.Finalize(context.Background()); err == nil {
		t.Fatal("无法读取容器清单时最终核验必须失败")
	}
}

// TestIsTestContainerNameOnlyMatchesTestResources 验证测试容器识别不会误判生产容器。
func TestIsTestContainerNameOnlyMatchesTestResources(t *testing.T) {
	t.Parallel()
	testNames := []string{
		"agentflow-test-lifecycle-abc",
		"agentflow-test-agent-abc",
		"agentflow-codesaver-abc",
	}
	for _, name := range testNames {
		if !testkit.IsTestContainerName(name) {
			t.Fatalf("应识别为测试容器: %s", name)
		}
	}
	productionNames := []string{
		"agentflow-1a2b3c4d",
		"pgsql-server",
		"agentflow",
	}
	for _, name := range productionNames {
		if testkit.IsTestContainerName(name) {
			t.Fatalf("不得把生产容器识别为测试容器: %s", name)
		}
	}
}
