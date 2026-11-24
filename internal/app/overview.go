package app

import (
	"context"
	"fmt"

	"github.com/lingting/agentflow/internal/storage"
)

// OverviewStatusCounts 是概览汇总中按状态分组的记录条数。
//
// 键是持久化的状态原值：不认识的取值原样保留，不归一化成已知状态。它是应用层自己的
// 读模型，不把存储层类型暴露给 HTTP 层。
type OverviewStatusCounts struct {
	// Total 是参与统计的记录总数。
	Total int
	// Counts 是状态到记录条数的映射。
	Counts map[string]int
}

// OverviewProjectCounts 是概览汇总中项目的配置维度计数。
//
// 三个数字都是已登记的配置事实：设置了默认执行节点的项目仍可能指向已删除的节点，
// 配置了缓存类型的项目也可能从未挂载过缓存卷。
type OverviewProjectCounts struct {
	// Total 是未删除的项目总数。
	Total int
	// WithDefaultHost 是设置了默认执行节点的项目数量。
	WithDefaultHost int
	// WithCaches 是配置了至少一种缓存类型的项目数量。
	WithCaches int
}

// OverviewService 是概览汇总的应用服务入口。
//
// 它只做只读计数：不读取需求正文，不建立 SSH / Docker 会话，不触发校验，也不推进任何
// 状态。四类汇总各自独立查询，某一类失败不会被伪装成 0，也不影响其他类的结论。
type OverviewService struct {
	store storage.Store
}

// NewOverviewService 构造概览汇总服务。
//
// 它只依赖存储：概览汇总不接触远程资源，因此容器能力缺失时仍然可以装配。
func NewOverviewService(store storage.Store) (*OverviewService, error) {
	if store == nil {
		return nil, fmt.Errorf("概览汇总服务初始化失败: 存储不能为空")
	}
	return &OverviewService{store: store}, nil
}

// RequirementStatusCounts 统计未删除需求按状态的数量分布。
//
// 状态键是持久化原值，未知取值同样返回；这里不推导需求完成度或进度。
func (s *OverviewService) RequirementStatusCounts(
	ctx context.Context,
) (OverviewStatusCounts, error) {
	counts, err := s.store.CountRequirementsByStatus(ctx)
	if err != nil {
		return OverviewStatusCounts{}, fmt.Errorf("统计需求状态分布失败: %w", err)
	}
	return overviewStatusCounts(counts), nil
}

// ProjectCounts 统计未删除项目的总数与配置维度计数。
//
// 计数只反映已登记的配置事实，不代表默认执行节点可用或缓存卷存在。
func (s *OverviewService) ProjectCounts(
	ctx context.Context,
) (OverviewProjectCounts, error) {
	counts, err := s.store.CountProjects(ctx)
	if err != nil {
		return OverviewProjectCounts{}, fmt.Errorf("统计项目配置分布失败: %w", err)
	}
	return OverviewProjectCounts{
		Total:           counts.Total,
		WithDefaultHost: counts.WithDefaultHost,
		WithCaches:      counts.WithCaches,
	}, nil
}

// TaskStatusCounts 统计未删除任务按状态的数量分布。
//
// 它只统计任务自身的状态列，不读取步骤、尝试、会话或工作流记录。
func (s *OverviewService) TaskStatusCounts(ctx context.Context) (OverviewStatusCounts, error) {
	counts, err := s.store.CountTasksByStatus(ctx)
	if err != nil {
		return OverviewStatusCounts{}, fmt.Errorf("统计任务状态分布失败: %w", err)
	}
	return overviewStatusCounts(counts), nil
}

// HostValidationStatusCounts 统计未删除执行节点按校验状态的数量分布。
//
// 它只统计持久化的校验状态，不重新执行预检，也不判断节点是否可用。
func (s *OverviewService) HostValidationStatusCounts(
	ctx context.Context,
) (OverviewStatusCounts, error) {
	counts, err := s.store.CountExecutionHostsByValidationStatus(ctx)
	if err != nil {
		return OverviewStatusCounts{}, fmt.Errorf("统计执行节点校验状态分布失败: %w", err)
	}
	return overviewStatusCounts(counts), nil
}

// overviewStatusCounts 把存储层分组计数转换为应用层读模型。
//
// 总数由分组条数求和，与存储层使用的是同一批行；这里不做修正，也不补齐缺失的状态。
func overviewStatusCounts(counts storage.StatusCounts) OverviewStatusCounts {
	return OverviewStatusCounts{Total: counts.Total(), Counts: counts}
}
