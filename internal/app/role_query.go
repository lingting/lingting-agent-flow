package app

import (
	"context"
	"fmt"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// RoleService 提供角色配置的只读查询。
//
// 本阶段只有查询能力：角色导入只发生在首次初始化（app.RoleSeeds +
// storage.RoleSeeder.SeedRoles），普通启动不会同步或覆盖已有角色，
// 也不存在角色 CRUD 入口。
type RoleService struct {
	store storage.Store
}

// NewRoleService 构造角色查询服务。
func NewRoleService(store storage.Store) (*RoleService, error) {
	if store == nil {
		return nil, fmt.Errorf("角色查询服务初始化失败: 存储不能为空")
	}
	return &RoleService{store: store}, nil
}

// List 返回全部未删除的角色，已禁用角色也在其中。
//
// 禁用角色不在这里过滤：接口保留它们用于诊断，创建任务时的选择器必须自行排除
// enabled=false 的记录，而任务创建本身仍由服务端校验角色可用性。
func (s *RoleService) List(ctx context.Context) ([]domain.Role, error) {
	roles, err := s.store.ListRoles(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询角色失败: %w", err)
	}
	return roles, nil
}
