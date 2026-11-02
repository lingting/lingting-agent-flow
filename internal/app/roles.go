package app

import (
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// RoleSeeds 把内置的固定角色定义转换为存储层的初始化定义。
//
// 角色定义随代码内置，不再来自配置文件：服务启动时用它们补齐数据库中缺失的固定角色，
// 已有角色的 Prompt、模型与启用状态保持数据库中的值，不会被这些默认值覆盖。
// 工具策略由角色类型推导，与领域层的默认最小权限保持一致。
func RoleSeeds() []storage.RoleSeed {
	defaults := domain.DefaultRoles()
	seeds := make([]storage.RoleSeed, 0, len(defaults))
	for _, role := range defaults {
		seeds = append(seeds, storage.RoleSeed{
			ID:           role.ID,
			Name:         role.Name,
			Type:         role.Type,
			Runner:       role.Runner,
			Model:        role.Model,
			SystemPrompt: role.SystemPrompt,
			ToolsPolicy:  domain.DefaultToolsPolicy(role.Type),
		})
	}
	return seeds
}
