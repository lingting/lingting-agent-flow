package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// CodeSaveQueryStore 是代码保存记录只读查询依赖的存储能力。
//
// 只声明查询所需的最小集合：它不包含写入、核验或恢复方法，因此用该接口构造的
// 查询服务在编译期就无法修改记录。
type CodeSaveQueryStore interface {
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	ListCodeSaveRecordsByTask(ctx context.Context, taskID string) ([]domain.CodeSaveRecord, error)
}

// CodeSaveQueryOptions 是构造代码保存记录查询服务的输入。
type CodeSaveQueryOptions struct {
	// Store 是持久化入口，必填。
	Store CodeSaveQueryStore
}

// CodeSaveQueryService 提供任务下代码保存记录的只读查询。
//
// 它与 `ContainerService.CodeSaveStatus` 的区别不是分层形式，而是真实依赖：
// `ContainerService` 由 `newRemoteDockerService` 构造，强制要求 SSH 与存储同时
// 存在（`internal/app/docker_session.go:73`），HTTP 层仅为了读一张表就要求远程
// 能力会把只读查询变成"需要 SSH 才能看代码保存记录"。本服务只依赖存储，因此
// 查询路径上不存在任何建立远程会话、核验容器或执行 Git 命令的可能。
//
// 职责边界：只读。它不提交、不推送、不合并、不核验、不推进任务状态，也不创建
// Attempt 或 Session。
type CodeSaveQueryService struct {
	store CodeSaveQueryStore
}

// NewCodeSaveQueryService 构造代码保存记录查询服务。
func NewCodeSaveQueryService(options CodeSaveQueryOptions) (*CodeSaveQueryService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("代码保存记录查询服务初始化失败: 存储不能为空")
	}
	return &CodeSaveQueryService{store: options.Store}, nil
}

// ListByTask 返回任务下的代码保存记录，最新创建的在前。
//
// 先校验任务存在且未被逻辑删除：任务不存在或已删除时返回 `ErrTaskNotFound`，
// 而不是返回一个看似正常的空列表——空列表意味着"任务存在但没有保存过代码"，
// 与"任务本身不存在"是两件不同的事实。
//
// 排序沿用存储层的真实语义（`created_at DESC, id DESC`），本方法不重新排序，
// 也不在应用层拼接第二套排序规则。
func (s *CodeSaveQueryService) ListByTask(
	ctx context.Context,
	taskID string,
) ([]domain.CodeSaveRecord, error) {
	id, err := parseCodeSaveQueryTaskID(taskID)
	if err != nil {
		return nil, err
	}
	task, err := s.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrTaskNotFound, id.String())
		}
		return nil, fmt.Errorf("读取任务失败: %w", err)
	}
	if task.Deleted() {
		// 已逻辑删除的任务与不存在返回同一语义：删除后不得再暴露它的保存历史。
		return nil, fmt.Errorf("%w: %s", ErrTaskNotFound, id.String())
	}
	records, err := s.store.ListCodeSaveRecordsByTask(ctx, id.String())
	if err != nil {
		// 存储错误必须向上返回：把它折叠成空列表会让"读取失败"看起来像"没有记录"。
		return nil, fmt.Errorf("读取代码保存记录失败: %w", err)
	}
	return records, nil
}

// parseCodeSaveQueryTaskID 校验路径中的任务标识。
//
// 空标识与含 `/` 的标识按未找到处理：它们不可能对应一条真实任务，也不应该
// 通过 400 与 404 的差异暴露"哪些标识形状被服务端识别"。
func parseCodeSaveQueryTaskID(raw string) (domain.ID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.Contains(trimmed, "/") {
		return "", fmt.Errorf("%w: %s", ErrTaskNotFound, trimmed)
	}
	return domain.ID(trimmed), nil
}
