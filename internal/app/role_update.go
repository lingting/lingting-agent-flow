package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// RoleUpdate 描述允许修改的角色配置字段，字段为 nil 表示本次不修改。
//
// 角色标识、名称、类型与 Runner 不在可修改范围内：它们由固定角色定义确定；工具策略可以
// 修改，但必须落在角色的系统能力上限内，不能通过配置获得该角色类型不具备的能力。
type RoleUpdate struct {
	SystemPrompt *string
	Model        *string
	Enabled      *bool
	ToolsPolicy  *domain.ToolsPolicy
}

// Update 修改角色的可编辑字段并返回真实保存结果。
//
// 更新先读取当前记录，再只覆盖 RoleUpdate 中显式给出的字段，因此并发之外的其它配置不会
// 被本次请求改写。保存后重新读取数据库，返回值来自持久化记录而不是调用方提交的内容。
func (s *RoleService) Update(ctx context.Context, roleID string, update RoleUpdate) (domain.Role, error) {
	current, err := s.Get(ctx, roleID)
	if err != nil {
		return domain.Role{}, err
	}
	role := current
	if update.SystemPrompt != nil {
		role.SystemPrompt = strings.TrimSpace(*update.SystemPrompt)
	}
	if update.Model != nil {
		role.Model = strings.TrimSpace(*update.Model)
	}
	if update.ToolsPolicy != nil {
		// 只接受角色能力上限内的策略：越界配置必须报错，而不是保存后再静默裁剪。
		policy := update.ToolsPolicy.Normalize()
		if err := domain.ValidateToolsPolicyForRole(current.Type, policy); err != nil {
			return domain.Role{}, fmt.Errorf("%w: %w", ErrRoleInvalidInput, err)
		}
		role.ToolsPolicy = policy
	}
	if update.Enabled != nil {
		role.Enabled = *update.Enabled
	}
	role.UpdatedAt = time.Now().UTC()
	if err := role.Validate(); err != nil {
		return domain.Role{}, fmt.Errorf("%w: %w", ErrRoleInvalidInput, err)
	}
	if err := s.store.UpdateRole(ctx, role); err != nil {
		return domain.Role{}, fmt.Errorf("更新角色失败: %w", err)
	}
	return s.Get(ctx, roleID)
}
