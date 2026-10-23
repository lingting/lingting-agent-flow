package app

import (
	"context"
	"errors"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/source"
	"github.com/lingting/agentflow/internal/storage"
)

// 本文件提供"容器内代码获取"的两个入口（尝试级 CodeInitializeService 与任务级
// TaskWorkspaceService）共用的判定与转换。
//
// 提取的目的是让"工作目录解析""执行节点准入""执行错误到状态映射"只有一份实现：
// 两处各自复制一套判定，一旦规则变化就会出现"尝试级通过、任务级不通过"的差异。
//
// 这里的函数都是纯函数或只读操作：它们不写数据库、不执行容器内命令。

// resolveCodeFetchWorkDir 返回一次代码获取使用的容器内工作目录。
//
// 优先级固定为：请求显式给出 > 任务配置 > 容器默认项目目录。任何来源都必须落在
// 容器内 /workspace 之下，不允许把代码写到 AgentFlow 管理工作区。
func resolveCodeFetchWorkDir(task domain.Task, requested string) (string, error) {
	workDir := strings.TrimSpace(requested)
	if workDir == "" {
		workDir = strings.TrimSpace(task.Config.WorkDir)
	}
	if workDir == "" {
		workDir = domain.DefaultContainerWorkDir
	}
	if err := domain.ValidateContainerWorkDir(workDir); err != nil {
		return "", err
	}
	return workDir, nil
}

// checkHostUsableForCodeFetch 校验执行节点是否允许执行真实远程操作。
//
// 任务没有绑定执行节点时跳过：该情况会在容器核验阶段以更具体的错误报告。
// 返回的错误只是原因，调用方负责包装成自己的错误类别。
func checkHostUsableForCodeFetch(
	ctx context.Context,
	store codeFetchHostStore,
	task domain.Task,
) error {
	if task.HostID == nil {
		return nil
	}
	host, err := store.GetExecutionHost(ctx, *task.HostID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return errors.New("任务 " + task.ID.String() + " 关联的执行节点不存在")
		}
		return errors.New("读取执行节点失败: " + errorSummary(err))
	}
	return EnsureHostUsableForRemoteTask(*host)
}

// codeFetchHostStore 是执行节点准入校验所需的存储能力。
type codeFetchHostStore interface {
	GetExecutionHost(ctx context.Context, id domain.ID) (*domain.ExecutionHost, error)
}

// codeFetchStatusOf 把执行错误与上下文状态映射为一次代码获取的状态。
//
// 取消与超时必须与失败区分：前者说明调用方或时限终止了操作，后者说明操作本身失败。
// 结果无法确认必须单独表达：它既不是失败也不是成功，只能先核验现场。
func codeFetchStatusOf(err error, ctx context.Context) domain.CodeFetchStatus {
	if err != nil {
		switch source.ErrorCodeOf(err) {
		case source.CodeCloneTimeout:
			return domain.CodeFetchStatusTimedOut
		case source.CodeCloneInterrupted:
			return domain.CodeFetchStatusCancelled
		case source.CodeStateUncertain:
			return domain.CodeFetchStatusUncertain
		}
	}
	if ctx != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return domain.CodeFetchStatusTimedOut
		case errors.Is(ctx.Err(), context.Canceled):
			return domain.CodeFetchStatusCancelled
		}
	}
	return domain.CodeFetchStatusFailed
}

// taskWorkspaceInitStatusOf 把执行错误与上下文状态映射为任务级工作区初始化状态。
//
// 它与 codeFetchStatusOf 共用同一套错误分类，只是结果类型不同：任务级记录必须
// 表达"结果无法确认"，因此不确定状态不能被压缩成 failed。
func taskWorkspaceInitStatusOf(
	err error,
	ctx context.Context,
) domain.TaskWorkspaceInitStatus {
	switch codeFetchStatusOf(err, ctx) {
	case domain.CodeFetchStatusTimedOut:
		return domain.TaskWorkspaceInitStatusTimedOut
	case domain.CodeFetchStatusCancelled:
		return domain.TaskWorkspaceInitStatusCancelled
	case domain.CodeFetchStatusUncertain:
		return domain.TaskWorkspaceInitStatusUncertain
	default:
		return domain.TaskWorkspaceInitStatusFailed
	}
}

// codeFetchStageOrDefault 返回可写入记录的失败阶段。
//
// 代码获取实现没有给出阶段时按 Clone 记录：那是第一个产生远程副作用的阶段，
// 把它记成 probe 会让"目录被占用"与"仓库不可访问"无法区分。
func codeFetchStageOrDefault(stage domain.CodeFetchStage) domain.CodeFetchStage {
	if stage == "" {
		return domain.CodeFetchStageClone
	}
	return stage
}
