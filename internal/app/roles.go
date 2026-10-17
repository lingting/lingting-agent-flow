package app

import (
	"fmt"

	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// RoleSeeds 把配置中的角色转换为存储层的初始化定义。
//
// 该转换只在首次初始化数据库时使用；日常任务以数据库中的角色记录与快照为准。
// 已有角色记录的显式同步必须作为独立操作实现，不能隐含在普通启动流程中。
func RoleSeeds(cfg *config.Config) ([]storage.RoleSeed, error) {
	if cfg == nil {
		return nil, fmt.Errorf("角色初始化失败: 配置为空")
	}
	seeds := make([]storage.RoleSeed, 0, len(cfg.Roles))
	seen := make(map[domain.ID]struct{}, len(cfg.Roles))
	for index, role := range cfg.Roles {
		seed, err := roleSeed(role)
		if err != nil {
			return nil, fmt.Errorf("角色初始化失败: roles[%d]: %w", index, err)
		}
		if _, duplicated := seen[seed.ID]; duplicated {
			return nil, fmt.Errorf("角色初始化失败: roles[%d].id: 角色 ID %q 重复", index, seed.ID)
		}
		seen[seed.ID] = struct{}{}
		seeds = append(seeds, seed)
	}
	return seeds, nil
}

// roleSeed 把单个配置角色转换为初始化定义。
//
// 工具策略由角色类型推导，与领域层的默认最小权限保持一致。
func roleSeed(role config.RoleConfig) (storage.RoleSeed, error) {
	id := domain.ID(role.ID)
	if err := id.Validate(); err != nil {
		return storage.RoleSeed{}, err
	}
	if err := role.Type.Validate(); err != nil {
		return storage.RoleSeed{}, err
	}
	runner := role.Runner
	if runner == "" {
		runner = domain.RoleRunnerPi
	}
	return storage.RoleSeed{
		ID:           id,
		Name:         role.Name,
		Type:         role.Type,
		Runner:       runner,
		Model:        role.Model,
		SystemPrompt: role.SystemPrompt,
		ToolsPolicy:  domain.DefaultToolsPolicy(role.Type),
	}, nil
}
