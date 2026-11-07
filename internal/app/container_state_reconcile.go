package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// 本文件承载容器启动前后的状态对账：远端 inspect 是"容器是否仍在运行"的最终依据，本地管理
// 状态在启动之前必须先与之一致，启动之后再由核验结果推进到 running。
//
// 对账只复用领域状态机与既有存储 / 登记清单写入，不新增状态字段、缓存表、租约或额外后台恢复
// 进程：下一次调用重新 inspect 就是重试，不需要跨进程仲裁。

// ensureObservedDockerID 核验远端容器 ID 与本地记录一致。
//
// 只有本地已经记录 Docker 容器 ID 时才比较：记录缺少 ID 时 observe 按名称核验，此时所有权
// 标签仍然保证容器属于本记录。ID 不一致说明本地记录指向的已经不是现场那个容器，不能据它启动。
func ensureObservedDockerID(container domain.Container, inspect docker.ContainerInspect) error {
	expected := strings.TrimSpace(container.DockerID)
	if expected == "" {
		return nil
	}
	if inspect.ID != expected {
		return fmt.Errorf(
			"远程容器 ID 与本地记录不一致: 远程 %s，记录 %s",
			inspect.ID,
			expected,
		)
	}
	return nil
}

// reconcileBeforeStart 在远端容器确认未运行时，把过期的本地管理状态收敛为可启动状态。
//
// 顺序固定为远端状态已核实 → 管理状态落库 → 远程登记清单收敛 → 才允许调用方执行 Docker
// start。管理状态迁移只走领域状态机：
//
//   - stopped / created：本身允许直接启动，不需要额外写入；
//   - running / unknown：与远端事实不一致，先 MarkStopped 再 SyncDockerState 记录真实观测；
//   - 其他管理状态（例如 creating、detached、removed）：不猜测、不覆盖，直接拒绝启动。
//
// 任一写入失败都立即返回：容器不会被启动，也不会被报告为已恢复。此时远端"容器已退出"这一
// 事实与可诊断错误一起保留，下一次调用会重新 inspect 并重复同一收敛过程。
func (s *ContainerService) reconcileBeforeStart(
	ctx context.Context,
	session *remoteDocker,
	container domain.Container,
	inspect docker.ContainerInspect,
) (domain.Container, error) {
	if !inspect.IsStartable() {
		return container, fmt.Errorf(
			"启动容器失败: 容器 %s 当前处于 Docker 状态 %s，不在可安全启动的退出状态",
			container.ID,
			inspect.State,
		)
	}
	switch container.State {
	case domain.ContainerStateStopped, domain.ContainerStateCreated:
		return container, nil
	case domain.ContainerStateRunning, domain.ContainerStateUnknown:
		// 本地状态与远端事实不一致：先经领域状态机收敛为已停止。
	default:
		return container, fmt.Errorf(
			"启动容器失败: 管理状态 %s 不能自动收敛为已停止，请先核验容器现场",
			container.State,
		)
	}
	now := time.Now()
	updated := container
	if err := updated.MarkStopped(now); err != nil {
		return container, fmt.Errorf("启动前收敛容器管理状态失败: %w", err)
	}
	if err := updated.SyncDockerState(inspect.State, now); err != nil {
		return container, fmt.Errorf("启动前记录容器观测状态失败: %w", err)
	}
	if err := s.store.UpdateContainer(ctx, updated); err != nil {
		return updated, fmt.Errorf("启动前保存已停止状态失败: %w", err)
	}
	if err := s.syncRegistry(ctx, session, updated); err != nil {
		// 已落库的 stopped 是真实事实：同步失败只报告清单错误，不改回 running。
		return updated, fmt.Errorf("启动前同步远程登记清单失败: %w", err)
	}
	return updated, nil
}

// recordStartedContainer 记录容器已确认启动，并同步远程登记清单。
//
// 只有在 Docker start 之后重新 inspect 确认容器确实在运行时才调用它，因此管理状态推进到
// running 依据的是实际观测结果，而不是启动命令的返回码。
func (s *ContainerService) recordStartedContainer(
	ctx context.Context,
	session *remoteDocker,
	container domain.Container,
	inspect *docker.ContainerInspect,
) (ContainerDetail, error) {
	updated := container
	if err := updated.MarkRunning(time.Now()); err != nil {
		return ContainerDetail{Container: container, Inspect: inspect}, err
	}
	if err := s.store.UpdateContainer(ctx, updated); err != nil {
		return ContainerDetail{Container: updated, Inspect: inspect}, err
	}
	if err := s.syncRegistry(ctx, session, updated); err != nil {
		return ContainerDetail{Container: updated, Inspect: inspect}, err
	}
	return ContainerDetail{Container: updated, Inspect: inspect}, nil
}
