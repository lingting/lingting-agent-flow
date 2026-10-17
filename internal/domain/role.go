package domain

import "strings"

// RoleType 表示编排器识别角色职责时使用的类型。
//
// 角色名称由用户自定义，编排器只依据类型决定该角色参与的阶段。
type RoleType string

// 第一版支持的三种角色类型。
const (
	RoleTypePlanner   RoleType = "planner"
	RoleTypeDeveloper RoleType = "developer"
	RoleTypeValidator RoleType = "validator"
)

// String 返回角色类型的字符串形式。
func (t RoleType) String() string {
	return string(t)
}

// Validate 校验角色类型是否受支持。
func (t RoleType) Validate() error {
	switch t {
	case RoleTypePlanner, RoleTypeDeveloper, RoleTypeValidator:
		return nil
	default:
		return invalidField("Role", "type", "不支持的角色类型 "+string(t))
	}
}

// ToolPermission 表示角色在 Agent 运行时中被授予的能力类别。
type ToolPermission string

// 第一版支持的能力类别。
const (
	ToolPermissionRead    ToolPermission = "read"
	ToolPermissionWrite   ToolPermission = "write"
	ToolPermissionExecute ToolPermission = "execute"
)

// Validate 校验能力类别是否受支持。
func (p ToolPermission) Validate() error {
	switch p {
	case ToolPermissionRead, ToolPermissionWrite, ToolPermissionExecute:
		return nil
	default:
		return invalidField("ToolsPolicy", "permissions", "不支持的能力类别 "+string(p))
	}
}

// ToolsPolicy 描述角色可使用的工具范围。
//
// 权限采用最小化原则：允许列表为空表示不按名称限制，但拒绝列表始终优先。
type ToolsPolicy struct {
	Permissions []ToolPermission `json:"permissions"`
	AllowTools  []string         `json:"allow_tools,omitempty"`
	DenyTools   []string         `json:"deny_tools,omitempty"`
}

// DefaultToolsPolicy 返回角色类型的默认最小权限策略。
//
// Planner 只读；Developer 可读写并执行命令；Validator 只读并执行验证命令。
func DefaultToolsPolicy(roleType RoleType) ToolsPolicy {
	switch roleType {
	case RoleTypeDeveloper:
		return ToolsPolicy{
			Permissions: []ToolPermission{ToolPermissionRead, ToolPermissionWrite, ToolPermissionExecute},
		}
	case RoleTypeValidator:
		return ToolsPolicy{
			Permissions: []ToolPermission{ToolPermissionRead, ToolPermissionExecute},
		}
	case RoleTypePlanner:
		return ToolsPolicy{Permissions: []ToolPermission{ToolPermissionRead}}
	default:
		return ToolsPolicy{Permissions: []ToolPermission{ToolPermissionRead}}
	}
}

// Has 判断策略是否包含某项能力。
func (p ToolsPolicy) Has(permission ToolPermission) bool {
	for _, candidate := range p.Permissions {
		if candidate == permission {
			return true
		}
	}
	return false
}

// AllowsTool 判断指定工具是否在策略允许范围内，拒绝列表优先于允许列表。
func (p ToolsPolicy) AllowsTool(tool string) bool {
	name := strings.TrimSpace(tool)
	if name == "" {
		return false
	}
	if containsName(p.DenyTools, name) {
		return false
	}
	if len(p.AllowTools) == 0 {
		return true
	}
	return containsName(p.AllowTools, name)
}

// Validate 校验策略内容。
func (p ToolsPolicy) Validate() error {
	for _, permission := range p.Permissions {
		if err := permission.Validate(); err != nil {
			return err
		}
	}
	for _, tool := range p.AllowTools {
		if strings.TrimSpace(tool) == "" {
			return invalidField("ToolsPolicy", "allow_tools", "工具名称不能为空")
		}
	}
	for _, tool := range p.DenyTools {
		if strings.TrimSpace(tool) == "" {
			return invalidField("ToolsPolicy", "deny_tools", "工具名称不能为空")
		}
	}
	return nil
}

// containsName 判断名称是否存在于列表中。
func containsName(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}

// Role 是用户定义的角色，用于驱动某一阶段的一次 Agent 执行。
type Role struct {
	ID           ID          `json:"id"`
	Name         string      `json:"name"`
	Type         RoleType    `json:"type"`
	Model        string      `json:"model"`
	SystemPrompt string      `json:"system_prompt"`
	ToolsPolicy  ToolsPolicy `json:"tools_policy"`
}

// NewRole 创建并校验角色。
//
// Model 允许为空，表示沿用 Pi 侧配置的默认模型。
func NewRole(name string, roleType RoleType, model, systemPrompt string, policy ToolsPolicy) (Role, error) {
	role := Role{
		ID:           NewID(),
		Name:         strings.TrimSpace(name),
		Type:         roleType,
		Model:        strings.TrimSpace(model),
		SystemPrompt: strings.TrimSpace(systemPrompt),
		ToolsPolicy:  policy,
	}
	if err := role.Validate(); err != nil {
		return Role{}, err
	}
	return role, nil
}

// Validate 校验角色定义是否完整。
func (r Role) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Name) == "" {
		return invalidField("Role", "name", "角色名称不能为空")
	}
	if err := r.Type.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.SystemPrompt) == "" {
		return invalidField("Role", "system_prompt", "系统提示词不能为空")
	}
	return r.ToolsPolicy.Validate()
}

// IsPlanner 判断角色是否为规划角色。
func (r Role) IsPlanner() bool {
	return r.Type == RoleTypePlanner
}

// IsDeveloper 判断角色是否为开发角色。
func (r Role) IsDeveloper() bool {
	return r.Type == RoleTypeDeveloper
}

// IsValidator 判断角色是否为验证角色。
func (r Role) IsValidator() bool {
	return r.Type == RoleTypeValidator
}
