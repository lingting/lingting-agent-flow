package domain

import (
	"fmt"
	"strings"
)

// Pi 内置工具名称。
//
// AgentFlow 托管的 Pi 调用只使用这些内置工具：MCP 工具、扩展注册的自定义工具与
// Pi 配置文件中的默认工具列表都不参与策略计算。
const (
	// PiToolRead 读取文本文件与受支持的图片。
	PiToolRead = "read"
	// PiToolGrep 搜索文件内容。
	PiToolGrep = "grep"
	// PiToolFind 按模式查找路径。
	PiToolFind = "find"
	// PiToolLs 列出目录内容。
	PiToolLs = "ls"
	// PiToolEdit 对已有文件做精确文本替换。
	PiToolEdit = "edit"
	// PiToolWrite 创建或覆盖文件。
	PiToolWrite = "write"
	// PiToolBash 执行 Shell 命令。
	PiToolBash = "bash"
)

// toolCapabilityOrder 是能力类别的固定顺序，决定 Pi 工具参数的稳定顺序。
var toolCapabilityOrder = []ToolPermission{
	ToolPermissionRead,
	ToolPermissionWrite,
	ToolPermissionExecute,
}

// toolPermissionTools 是能力类别到 Pi 内置工具的映射。
//
// 映射依据 Pi 官方内置工具定义：read 覆盖读取与检索类工具，write 覆盖写入类工具，
// execute 只对应 bash。bash 可以写文件，因此它不是只读工具。
var toolPermissionTools = map[ToolPermission][]string{
	ToolPermissionRead:    {PiToolRead, PiToolGrep, PiToolFind, PiToolLs},
	ToolPermissionWrite:   {PiToolEdit, PiToolWrite},
	ToolPermissionExecute: {PiToolBash},
}

// RoleToolCeiling 返回角色类型在系统层面的能力上限。
//
// 上限由角色身份决定，用户配置不能突破：需求分析师、方案规划师与验收者只读，
// 只有开发者可以写入并执行命令。未识别的角色类型返回错误，不回退到开发者权限。
func RoleToolCeiling(roleType RoleType) ([]ToolPermission, error) {
	switch roleType {
	case RoleTypeRequirementAnalyst, RoleTypePlanner, RoleTypeValidator:
		return []ToolPermission{ToolPermissionRead}, nil
	case RoleTypeDeveloper:
		return []ToolPermission{
			ToolPermissionRead,
			ToolPermissionWrite,
			ToolPermissionExecute,
		}, nil
	default:
		return nil, invalidField("Role", "type", "不支持的角色类型 "+roleType.String())
	}
}

// ToolsForPermissions 返回能力集合对应的 Pi 内置工具名称，未知能力类别被忽略。
//
// 输出顺序固定按只读、写入、执行排列，便于与 Pi 参数和展示逐项核对。
func ToolsForPermissions(permissions []ToolPermission) []string {
	tools := make([]string, 0, len(permissions))
	for _, capability := range toolCapabilityOrder {
		if !containsPermission(permissions, capability) {
			continue
		}
		tools = append(tools, toolPermissionTools[capability]...)
	}
	return tools
}

// IsPiToolName 判断名称是否为 AgentFlow 托管的 Pi 内置工具。
func IsPiToolName(name string) bool {
	for _, capability := range toolCapabilityOrder {
		if containsName(toolPermissionTools[capability], strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// EffectivePiTools 计算一次 Pi 调用实际可用的内置工具。
//
// 计算顺序固定：先取角色能力上限与策略授权能力的交集，再按能力映射为工具名称，
// 最后应用允许列表与拒绝列表。拒绝列表优先；allow_tools 为空表示不按名称限制已经
// 授权的工具；未识别的角色类型返回错误，不产生"默认开发权限"这类回退。
func EffectivePiTools(roleType RoleType, policy ToolsPolicy) ([]string, error) {
	ceiling, err := RoleToolCeiling(roleType)
	if err != nil {
		return nil, err
	}
	normalized := policy.Normalize()
	granted := make([]ToolPermission, 0, len(ceiling))
	for _, permission := range ceiling {
		if normalized.Has(permission) {
			granted = append(granted, permission)
		}
	}
	candidates := ToolsForPermissions(granted)
	effective := make([]string, 0, len(candidates))
	for _, tool := range candidates {
		if normalized.AllowsTool(tool) {
			effective = append(effective, tool)
		}
	}
	return effective, nil
}

// ValidateToolsPolicyForRole 校验策略是否在角色能力上限内。
//
// 用于配置写入：超出系统上限的能力、能力上限之外的工具与未知工具名一律拒绝，
// 而不是先保存再在执行时静默裁剪。
func ValidateToolsPolicyForRole(roleType RoleType, policy ToolsPolicy) error {
	ceiling, err := RoleToolCeiling(roleType)
	if err != nil {
		return err
	}
	normalized := policy.Normalize()
	if err := normalized.Validate(); err != nil {
		return err
	}
	for _, permission := range normalized.Permissions {
		if containsPermission(ceiling, permission) {
			continue
		}
		return invalidField(
			"ToolsPolicy",
			"permissions",
			fmt.Sprintf("角色 %s 的系统能力上限不包含 %s", roleType, permission),
		)
	}
	usable := ToolsForPermissions(ceiling)
	for _, tool := range normalized.AllowTools {
		if containsName(usable, tool) {
			continue
		}
		return invalidField(
			"ToolsPolicy",
			"allow_tools",
			fmt.Sprintf("角色 %s 不能使用工具 %s", roleType, tool),
		)
	}
	for _, tool := range normalized.DenyTools {
		if IsPiToolName(tool) {
			continue
		}
		return invalidField(
			"ToolsPolicy",
			"deny_tools",
			"不支持的工具名称 "+tool,
		)
	}
	return nil
}

// Normalize 去除工具名称首尾空白并去重，保持首次出现的顺序。
//
// 空白名称原样保留，使后续 Validate 仍能拒绝空工具名，而不是被归一化掩盖。
func (p ToolsPolicy) Normalize() ToolsPolicy {
	return ToolsPolicy{
		Permissions: normalizePermissions(p.Permissions),
		AllowTools:  normalizeToolNames(p.AllowTools),
		DenyTools:   normalizeToolNames(p.DenyTools),
	}
}

// normalizePermissions 去除重复的能力类别。
func normalizePermissions(permissions []ToolPermission) []ToolPermission {
	if len(permissions) == 0 {
		return nil
	}
	normalized := make([]ToolPermission, 0, len(permissions))
	for _, permission := range permissions {
		if containsPermission(normalized, permission) {
			continue
		}
		normalized = append(normalized, permission)
	}
	return normalized
}

// normalizeToolNames 去除工具名称首尾空白并去重。
func normalizeToolNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(names))
	for _, name := range names {
		tool := strings.TrimSpace(name)
		if containsName(normalized, tool) {
			continue
		}
		normalized = append(normalized, tool)
	}
	return normalized
}

// containsPermission 判断能力类别是否存在于列表中。
func containsPermission(permissions []ToolPermission, target ToolPermission) bool {
	for _, permission := range permissions {
		if permission == target {
			return true
		}
	}
	return false
}
