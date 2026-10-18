package container_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// newContainerRecord 构造一个合法的容器登记记录。
func newContainerRecord(t *testing.T) domain.Container {
	t.Helper()
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	container, err := domain.NewContainer(
		"container-1",
		"host-1",
		"agentflow-abcdef123456",
		ref,
		"v0001",
		strings.Repeat("a", 64),
		"sha256:"+strings.Repeat("b", 64),
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	return container
}

func TestNewContainerValidatesFields(t *testing.T) {
	t.Parallel()
	container := newContainerRecord(t)
	if container.State != domain.ContainerStateCreating {
		t.Fatalf("新建容器状态应为 creating，实际 %s", container.State)
	}
	if container.DockerState != domain.DockerStateUnknown {
		t.Fatalf("新建容器的 Docker 状态应为 unknown，实际 %s", container.DockerState)
	}
	if container.Detached {
		t.Fatal("新建容器不应处于脱离管理状态")
	}
	if err := container.Validate(); err != nil {
		t.Fatalf("合法容器记录校验失败: %v", err)
	}

	invalidName := container
	invalidName.Name = "agent flow"
	if err := invalidName.Validate(); err == nil {
		t.Fatal("非法容器名称应当被拒绝")
	}

	invalidImage := container
	invalidImage.ImageID = "latest"
	if err := invalidImage.Validate(); err == nil {
		t.Fatal("非法镜像 ID 应当被拒绝")
	}

	invalidAttempt := container
	attemptID := domain.NewID()
	invalidAttempt.AttemptID = &attemptID
	if err := invalidAttempt.Validate(); err == nil {
		t.Fatal("只有尝试而没有任务时应当被拒绝")
	}
}

func TestContainerLifecycleTransitions(t *testing.T) {
	t.Parallel()
	container := newContainerRecord(t)
	dockerID := strings.Repeat("c", 64)
	if err := container.MarkCreated(dockerID, domain.DockerStateCreated, time.Now()); err != nil {
		t.Fatalf("标记创建完成失败: %v", err)
	}
	if container.DockerID != dockerID {
		t.Fatalf("容器 ID 不符: %s", container.DockerID)
	}
	if err := container.MarkRunning(time.Now()); err != nil {
		t.Fatalf("标记运行中失败: %v", err)
	}
	if container.StartedAt == nil {
		t.Fatal("运行中的容器应记录启动时间")
	}
	if err := container.MarkStopped(time.Now()); err != nil {
		t.Fatalf("标记停止失败: %v", err)
	}
	if container.State != domain.ContainerStateStopped {
		t.Fatalf("容器状态不符: %s", container.State)
	}
	// 停止后可以重新启动。
	if err := container.MarkRunning(time.Now()); err != nil {
		t.Fatalf("停止后重新启动失败: %v", err)
	}
	if err := container.MarkRemoved(domain.ContainerEndReasonTaskSucceeded, time.Now()); err != nil {
		t.Fatalf("标记删除失败: %v", err)
	}
	if container.State != domain.ContainerStateRemoved {
		t.Fatalf("容器状态不符: %s", container.State)
	}
	if container.FinishedAt == nil {
		t.Fatal("已删除的容器应记录结束时间")
	}
	if container.EndReason != domain.ContainerEndReasonTaskSucceeded {
		t.Fatalf("结束原因不符: %s", container.EndReason)
	}
	// 删除后不允许再次启动。
	if err := container.MarkRunning(time.Now()); err == nil {
		t.Fatal("已删除的容器不应允许重新启动")
	}
}

func TestDetachedContainerRejectsAutomaticOperations(t *testing.T) {
	t.Parallel()
	container := newContainerRecord(t)
	if err := container.MarkCreated(strings.Repeat("d", 64), domain.DockerStateRunning, time.Now()); err != nil {
		t.Fatalf("标记创建完成失败: %v", err)
	}
	if err := container.MarkRunning(time.Now()); err != nil {
		t.Fatalf("标记运行中失败: %v", err)
	}
	if err := container.Detach(time.Now()); err != nil {
		t.Fatalf("脱离管理失败: %v", err)
	}
	if !container.Detached || container.State != domain.ContainerStateDetached {
		t.Fatalf("脱离管理状态不符: detached=%t state=%s", container.Detached, container.State)
	}
	if container.DetachedAt == nil {
		t.Fatal("脱离管理应记录时间")
	}
	if container.State.Managed() {
		t.Fatal("已脱离管理的容器不应处于自动管理范围")
	}
	// 脱离管理是终态：不能启动、停止、删除，也不能重复脱离。
	if err := container.MarkRunning(time.Now()); err == nil {
		t.Fatal("已脱离管理的容器不应允许自动启动")
	}
	if err := container.MarkStopped(time.Now()); err == nil {
		t.Fatal("已脱离管理的容器不应允许自动停止")
	}
	if err := container.MarkRemoved(domain.ContainerEndReasonManual, time.Now()); err == nil {
		t.Fatal("已脱离管理的容器不应允许自动删除")
	}
	if err := container.Detach(time.Now()); err == nil {
		t.Fatal("重复脱离管理应当失败")
	}
	// 只读观测仍然可以记录，但不会恢复管理权。
	if err := container.SyncDockerState(domain.DockerStateExited, time.Now()); err != nil {
		t.Fatalf("已脱离管理的容器仍应允许只读观测: %v", err)
	}
	if !container.Detached {
		t.Fatal("只读观测不得恢复管理权")
	}
}

func TestContainerStateValidation(t *testing.T) {
	t.Parallel()
	for _, state := range []domain.ContainerState{
		domain.ContainerStateCreating,
		domain.ContainerStateCreated,
		domain.ContainerStateRunning,
		domain.ContainerStateStopped,
		domain.ContainerStateRemoved,
		domain.ContainerStateDetached,
		domain.ContainerStateUnknown,
	} {
		if err := state.Validate(); err != nil {
			t.Fatalf("状态 %q 应当合法: %v", state, err)
		}
	}
	if err := domain.ContainerState("weird").Validate(); err == nil {
		t.Fatal("未知状态应当被拒绝")
	}
	if !domain.ContainerStateRemoved.IsTerminal() || !domain.ContainerStateDetached.IsTerminal() {
		t.Fatal("removed 与 detached 应为终态")
	}
	if domain.ContainerStateRunning.IsTerminal() {
		t.Fatal("running 不应是终态")
	}
}

func TestParseDockerContainerState(t *testing.T) {
	t.Parallel()
	cases := map[string]domain.DockerContainerState{
		"running":    domain.DockerStateRunning,
		"RUNNING":    domain.DockerStateRunning,
		"exited":     domain.DockerStateExited,
		"created":    domain.DockerStateCreated,
		"paused":     domain.DockerStatePaused,
		"restarting": domain.DockerStateRestarting,
		"dead":       domain.DockerStateDead,
		"removing":   domain.DockerStateRemoving,
		"unknown":    domain.DockerStateUnknown,
		"whatever":   domain.DockerStateUnknown,
		"":           domain.DockerStateUnknown,
	}
	for raw, expected := range cases {
		if got := domain.ParseDockerContainerState(raw); got != expected {
			t.Fatalf("状态 %q 解析不符: 期望 %s，实际 %s", raw, expected, got)
		}
	}
}

func TestDecideContainerLifecycle(t *testing.T) {
	t.Parallel()
	succeeded, err := domain.DecideContainerLifecycle(
		domain.ContainerOutcomeSucceeded,
		domain.ContainerLifecycleOptions{},
	)
	if err != nil {
		t.Fatalf("成功结果计算策略失败: %v", err)
	}
	if succeeded.Retention != domain.ContainerRetentionDelete {
		t.Fatalf("成功后应删除容器，实际 %s", succeeded.Retention)
	}
	if !succeeded.RemoveTaskResources {
		t.Fatal("成功清理应同时清理任务级临时资源")
	}

	failed, err := domain.DecideContainerLifecycle(
		domain.ContainerOutcomeFailed,
		domain.ContainerLifecycleOptions{},
	)
	if err != nil {
		t.Fatalf("失败结果计算策略失败: %v", err)
	}
	if failed.Retention != domain.ContainerRetentionKeep {
		t.Fatalf("失败后应保留容器，实际 %s", failed.Retention)
	}
	if failed.RemoveTaskResources {
		t.Fatal("失败时不得清理任务级临时资源")
	}

	// 中断默认保留。
	interrupted, err := domain.DecideContainerLifecycle(
		domain.ContainerOutcomeInterrupted,
		domain.ContainerLifecycleOptions{KeepInterrupted: true},
	)
	if err != nil {
		t.Fatalf("中断结果计算策略失败: %v", err)
	}
	if interrupted.Retention != domain.ContainerRetentionKeep {
		t.Fatalf("中断默认应保留容器，实际 %s", interrupted.Retention)
	}

	// 调用方显式要求不保留时才删除。
	discarded, err := domain.DecideContainerLifecycle(
		domain.ContainerOutcomeInterrupted,
		domain.ContainerLifecycleOptions{KeepInterrupted: false},
	)
	if err != nil {
		t.Fatalf("中断结果计算策略失败: %v", err)
	}
	if discarded.Retention != domain.ContainerRetentionDelete {
		t.Fatalf("中断不保留时应删除容器，实际 %s", discarded.Retention)
	}

	if _, err := domain.DecideContainerLifecycle("weird", domain.ContainerLifecycleOptions{}); err == nil {
		t.Fatal("未知结果分类应当被拒绝")
	}
}

func TestPlanContainerRestartRequiresNewContainer(t *testing.T) {
	t.Parallel()
	container := newContainerRecord(t)
	if _, err := domain.PlanContainerRestart(
		container,
		domain.RestartDispositionDetach,
		container.Name,
		time.Now(),
	); err == nil {
		t.Fatal("新容器名称与旧容器相同时应当被拒绝")
	}
	plan, err := domain.PlanContainerRestart(
		container,
		domain.RestartDispositionDetach,
		"agentflow-0123456789ab",
		time.Now(),
	)
	if err != nil {
		t.Fatalf("重新开始计划应当合法: %v", err)
	}
	if plan.NewContainerName == container.Name {
		t.Fatal("重新开始必须使用新的容器名称")
	}
	if _, err := domain.PlanContainerRestart(
		container,
		"weird",
		"agentflow-0123456789ab",
		time.Now(),
	); err == nil {
		t.Fatal("未知处置方式应当被拒绝")
	}
	if _, err := domain.ParseRestartDisposition("reuse"); err == nil {
		t.Fatal("复用旧容器不是合法处置方式")
	}
	parsed, err := domain.ParseRestartDisposition(" Detach ")
	if err != nil {
		t.Fatalf("解析处置方式失败: %v", err)
	}
	if parsed != domain.RestartDispositionDetach {
		t.Fatalf("处置方式不符: %s", parsed)
	}
}

func TestCacheMountValidation(t *testing.T) {
	t.Parallel()
	valid := domain.CacheMount{
		Type:          "go-mod",
		ContainerPath: "/var/cache/agentflow/go/pkg/mod",
		Scope:         domain.CacheScopeMachine,
		ABI:           "go124",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("合法缓存定义校验失败: %v", err)
	}
	for _, invalid := range []domain.CacheMount{
		{Type: "Bad Type", ContainerPath: "/var/cache/x", Scope: domain.CacheScopeMachine},
		{Type: "go-mod", ContainerPath: "relative/path", Scope: domain.CacheScopeMachine},
		{Type: "go-mod", ContainerPath: "/var/cache/../etc", Scope: domain.CacheScopeMachine},
		{Type: "go-mod", ContainerPath: "/workspace/project", Scope: domain.CacheScopeMachine},
		{Type: "go-mod", ContainerPath: "/root/.cache", Scope: domain.CacheScopeMachine},
		{Type: "go-mod", ContainerPath: "/var/cache/x", Scope: "global"},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("非法缓存定义应当被拒绝: %+v", invalid)
		}
	}
}

func TestSelectCachesRequiresExplicitAllowList(t *testing.T) {
	t.Parallel()
	available := []domain.CacheMount{
		{Type: "go-mod", ContainerPath: "/var/cache/go/mod", Scope: domain.CacheScopeMachine},
		{Type: "go-build", ContainerPath: "/var/cache/go/build", Scope: domain.CacheScopeProject},
	}
	selected, err := domain.SelectCaches(available, nil)
	if err != nil {
		t.Fatalf("空允许列表不应失败: %v", err)
	}
	if len(selected) != 0 {
		t.Fatalf("未显式允许时不应挂载任何缓存，实际 %d 个", len(selected))
	}
	selected, err = domain.SelectCaches(available, []string{"go-mod"})
	if err != nil {
		t.Fatalf("选择缓存失败: %v", err)
	}
	if len(selected) != 1 || selected[0].Type != "go-mod" {
		t.Fatalf("选择结果不符: %+v", selected)
	}
	if _, err := domain.SelectCaches(available, []string{"unknown"}); err == nil {
		t.Fatal("选择未定义的缓存类型应当失败")
	}
}

func TestProjectConfigRejectsBaseEnvironment(t *testing.T) {
	t.Parallel()
	baseRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	if _, err := domain.NewProjectConfig("proj", "项目", baseRef, "v0001", nil, time.Now()); err == nil {
		t.Fatal("项目配置不应允许基础环境")
	}
	languageRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "node")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	config, err := domain.NewProjectConfig("proj", "项目", languageRef, "v0001", []string{"node-npm", "node-npm"}, time.Now())
	if err != nil {
		t.Fatalf("合法项目配置校验失败: %v", err)
	}
	if config.WorkDir != domain.DefaultContainerWorkDir {
		t.Fatalf("项目工作目录应为容器内目录，实际 %s", config.WorkDir)
	}
	if len(config.Caches) != 1 {
		t.Fatalf("缓存类型应去重，实际 %+v", config.Caches)
	}
	invalid := config
	invalid.WorkDir = "/root"
	if err := invalid.Validate(); err == nil {
		t.Fatal("项目工作目录使用保留路径时应当被拒绝")
	}
}

func TestContainerErrorsAreDistinguishable(t *testing.T) {
	t.Parallel()
	container := newContainerRecord(t)
	if err := container.Detach(time.Now()); err != nil {
		t.Fatalf("脱离管理失败: %v", err)
	}
	err := container.MarkRunning(time.Now())
	if err == nil {
		t.Fatal("已脱离管理的容器不应允许启动")
	}
	if !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("应可用 ErrInvalidState 判断: %v", err)
	}
}
