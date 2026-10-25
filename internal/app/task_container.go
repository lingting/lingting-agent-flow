package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 任务容器的加载与归属核验。
//
// 本文件把"任务记录的 container_id 是否真的指向属于该任务的容器登记记录"
// 收敛为唯一实现：代码获取、工作区初始化、步骤执行、验证、计划生成与工作流编排
// 都必须经过它，而不是各自用 task.ContainerID 直接去核验远程容器。
//
// 为什么需要它：数据库里的 container_id 只是一个普通文本列，没有外键约束。仅凭
// "任务记着一个容器 ID"就执行命令，会在以下现场把命令送进别的任务或别的代次：
//   - 容器记录登记的是另一个任务（跨任务串扰）；
//   - 容器记录没有登记任务归属（无法证明归属）；
//   - 任务在并发准备时被写入了另一条容器记录（两条关联互相矛盾）；
//   - 容器已脱离管理或已删除，但任务记录还没有更新。

// 任务容器归属的错误类别。
//
// 两者分开表达：缺少容器是"还没有准备环境"（用户可以先准备），归属不成立是
// "关联指向了不属于该任务的容器"（必须人工核验，不得靠重试解决）。
var (
	// ErrTaskContainerMissing 表示任务没有可用容器记录。
	ErrTaskContainerMissing = errors.New("app: 任务容器不存在")
	// ErrTaskContainerOwnership 表示容器记录不属于该任务。
	ErrTaskContainerOwnership = errors.New("app: 容器归属不成立")
)

// TaskContainerStore 是任务容器归属核验所需的最小存储能力。
type TaskContainerStore interface {
	GetContainer(ctx context.Context, id domain.ID) (*domain.Container, error)
}

// loadOwnedContainer 读取任务引用的容器登记记录并核验归属。
//
// 返回值是数据库里真实的容器记录（不是任务记录里的 ID 字符串），因此后续所有
// 远程操作都以这条记录的 Docker ID 与所属节点为准。
//
// 错误语义：
//   - 任务没有容器引用 → ErrTaskContainerMissing；
//   - 容器记录不存在或已被删除 → ErrTaskContainerMissing；
//   - 归属不成立（另一个任务 / 未登记归属 / 节点不一致 / 已脱离管理 / 已删除 /
//     缺少 Docker ID）→ ErrTaskContainerOwnership，包装 domain.ErrInvalidState；
//   - 数据库读取失败 → 原始错误（可重试）。
func loadOwnedContainer(
	ctx context.Context,
	store TaskContainerStore,
	task domain.Task,
) (domain.Container, error) {
	referenced := strings.TrimSpace(task.ContainerID)
	if referenced == "" {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务 %s 还没有任务容器",
			ErrTaskContainerMissing,
			task.ID,
		)
	}
	containerID := domain.ID(referenced)
	if err := containerID.Validate(); err != nil {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务 %s 的容器引用 %q 不合法: %w",
			ErrTaskContainerOwnership,
			task.ID,
			referenced,
			err,
		)
	}
	container, err := store.GetContainer(ctx, containerID)
	if err != nil {
		if isContainerMissing(err) || isNotFound(err) {
			return domain.Container{}, fmt.Errorf(
				"%w: 任务 %s 引用的容器记录 %s 不存在",
				ErrTaskContainerMissing,
				task.ID,
				referenced,
			)
		}
		return domain.Container{}, fmt.Errorf("读取任务 %s 的容器记录失败: %w", task.ID, err)
	}
	if err := domain.CheckContainerOwnership(task, *container); err != nil {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务 %s 的容器归属不成立: %w",
			ErrTaskContainerOwnership,
			task.ID,
			err,
		)
	}
	return *container, nil
}

// isNotFound 判断错误是否表示记录不存在。
func isNotFound(err error) bool {
	return errors.Is(err, storage.ErrNotFound)
}
