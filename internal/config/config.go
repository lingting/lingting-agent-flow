// Package config 定义 AgentFlow 的配置结构与校验规则。
//
// 本包只负责把 YAML 配置转换为结构化数据并校验其合法性，
// 不涉及 Pi 安装检查、模型调用或权限实施。
package config

import (
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
)

// 第一版支持的 Agent 运行时与工具策略模式取值。
const (
	// RunnerPi 是当前唯一支持的 Agent 运行时。
	RunnerPi = "pi"

	// ToolsModeReadOnly 表示只读模式，对应规划角色。
	ToolsModeReadOnly = "read_only"
	// ToolsModeDevelopment 表示开发模式，对应开发角色。
	ToolsModeDevelopment = "development"
	// ToolsModeVerification 表示验证模式，对应验证角色。
	ToolsModeVerification = "verification"
)

// Config 是 AgentFlow 的完整配置。
type Config struct {
	// SourcePath 是本次加载使用的配置文件路径，由加载器写入，不参与 YAML 解析。
	SourcePath string `yaml:"-"`

	App       AppConfig       `yaml:"app"`
	Storage   StorageConfig   `yaml:"storage"`
	Workspace WorkspaceConfig `yaml:"workspace"`
	Scheduler SchedulerConfig `yaml:"scheduler"`
	Roles     []RoleConfig    `yaml:"roles"`
}

// AppConfig 描述应用自身的标识与日志设置。
type AppConfig struct {
	Name     string `yaml:"name"`
	LogLevel string `yaml:"log_level"`
}

// StorageConfig 描述持久化位置。
type StorageConfig struct {
	DatabasePath string `yaml:"database_path"`
}

// WorkspaceConfig 描述任务工作区的存放位置。
type WorkspaceConfig struct {
	WorktreeRoot string `yaml:"worktree_root"`
}

// SchedulerConfig 描述调度与重试上限。
type SchedulerConfig struct {
	MaxConcurrentTasks int `yaml:"max_concurrent_tasks"`
	MaxRetriesPerStep  int `yaml:"max_retries_per_step"`
}

// RoleConfig 描述一个角色。
//
// 名称由用户自由命名，工作流只依据 Type 识别职责。
type RoleConfig struct {
	ID           string            `yaml:"id"`
	Name         string            `yaml:"name"`
	Type         domain.RoleType   `yaml:"type"`
	Runner       string            `yaml:"runner"`
	Model        string            `yaml:"model"`
	SystemPrompt string            `yaml:"system_prompt"`
	ToolsPolicy  ToolsPolicyConfig `yaml:"tools_policy"`
}

// ToolsPolicyConfig 描述角色的工具策略声明。
//
// 第一版只定义结构，不据此实施实际权限控制。
type ToolsPolicyConfig struct {
	Mode string `yaml:"mode"`
}

// ValidationError 汇总一份配置中的全部校验问题。
type ValidationError struct {
	// Path 是配置来源文件路径。
	Path string
	// Issues 是全部校验问题，每项以配置字段路径开头。
	Issues []string
}

// Error 实现 error 接口。
func (e *ValidationError) Error() string {
	var builder strings.Builder
	builder.WriteString("配置校验失败: ")
	builder.WriteString(e.Path)
	for _, issue := range e.Issues {
		builder.WriteString("\n  - ")
		builder.WriteString(issue)
	}
	return builder.String()
}

// issues 收集校验问题，使一次校验可以报告全部问题。
type issues struct {
	list []string
}

// add 记录一条校验问题。
func (i *issues) add(field, reason string) {
	i.list = append(i.list, field+": "+reason)
}

// empty 判断是否没有收集到问题。
func (i *issues) empty() bool {
	return len(i.list) == 0
}

// Validate 校验整份配置，返回汇总后的 ValidationError。
func (c *Config) Validate() error {
	var problems issues
	c.App.validate(&problems)
	c.Storage.validate(&problems)
	c.Workspace.validate(&problems)
	c.Scheduler.validate(&problems)
	c.validateRoles(&problems)
	if problems.empty() {
		return nil
	}
	return &ValidationError{Path: c.SourcePath, Issues: problems.list}
}

// validate 校验应用基础配置。
func (a AppConfig) validate(problems *issues) {
	if strings.TrimSpace(a.Name) == "" {
		problems.add("app.name", "不能为空")
	}
	if strings.TrimSpace(a.LogLevel) == "" {
		problems.add("app.log_level", "不能为空")
		return
	}
	if _, err := logging.ParseLevel(a.LogLevel); err != nil {
		problems.add("app.log_level", err.Error())
	}
}

// validate 校验持久化路径。
func (s StorageConfig) validate(problems *issues) {
	if strings.TrimSpace(s.DatabasePath) == "" {
		problems.add("storage.database_path", "不能为空")
	}
}

// validate 校验工作区路径。
func (w WorkspaceConfig) validate(problems *issues) {
	if strings.TrimSpace(w.WorktreeRoot) == "" {
		problems.add("workspace.worktree_root", "不能为空")
	}
}

// validate 校验调度上限。
func (s SchedulerConfig) validate(problems *issues) {
	if s.MaxConcurrentTasks <= 0 {
		problems.add("scheduler.max_concurrent_tasks", "必须大于 0")
	}
	if s.MaxRetriesPerStep < 0 {
		problems.add("scheduler.max_retries_per_step", "不得为负数")
	}
}

// validateRoles 校验角色列表与角色 ID 唯一性。
func (c *Config) validateRoles(problems *issues) {
	if len(c.Roles) == 0 {
		problems.add("roles", "角色列表不能为空")
		return
	}
	seen := make(map[string]int, len(c.Roles))
	for index, role := range c.Roles {
		role.validate(index, problems)
		id := strings.TrimSpace(role.ID)
		if id == "" {
			continue
		}
		if first, duplicated := seen[id]; duplicated {
			problems.add(
				fmt.Sprintf("roles[%d].id", index),
				fmt.Sprintf("角色 ID %q 与 roles[%d] 重复", id, first),
			)
			continue
		}
		seen[id] = index
	}
}

// validate 校验单个角色的字段约束。
func (r RoleConfig) validate(index int, problems *issues) {
	prefix := fmt.Sprintf("roles[%d]", index)
	if strings.TrimSpace(r.ID) == "" {
		problems.add(prefix+".id", "不能为空")
	}
	if strings.TrimSpace(r.Name) == "" {
		problems.add(prefix+".name", "不能为空")
	}
	r.validateType(prefix, problems)
	r.validateRunner(prefix, problems)
	if strings.TrimSpace(r.Model) == "" {
		problems.add(prefix+".model", "不能为空")
	}
	if strings.TrimSpace(r.SystemPrompt) == "" {
		problems.add(prefix+".system_prompt", "不能为空")
	}
	r.ToolsPolicy.validate(prefix, r.Type, problems)
}

// validateType 校验角色类型。
func (r RoleConfig) validateType(prefix string, problems *issues) {
	if strings.TrimSpace(r.Type.String()) == "" {
		problems.add(prefix+".type", "不能为空")
		return
	}
	if err := r.Type.Validate(); err != nil {
		problems.add(prefix+".type", err.Error())
	}
}

// validateRunner 校验 Agent 运行时取值。
func (r RoleConfig) validateRunner(prefix string, problems *issues) {
	runner := strings.TrimSpace(r.Runner)
	if runner == "" {
		problems.add(prefix+".runner", "不能为空")
		return
	}
	if runner != RunnerPi {
		problems.add(prefix+".runner", "当前仅支持 "+RunnerPi+"，收到 "+runner)
	}
}

// validate 校验工具策略声明。
func (p ToolsPolicyConfig) validate(prefix string, roleType domain.RoleType, problems *issues) {
	mode := strings.TrimSpace(p.Mode)
	if mode == "" {
		problems.add(prefix+".tools_policy.mode", "不能为空")
		return
	}
	expected, ok := expectedToolsMode(roleType)
	if !ok {
		return
	}
	if mode != expected {
		problems.add(
			prefix+".tools_policy.mode",
			fmt.Sprintf("角色类型 %s 只允许 %s，收到 %s", roleType, expected, mode),
		)
	}
}

// expectedToolsMode 返回角色类型对应的工具策略模式。
func expectedToolsMode(roleType domain.RoleType) (string, bool) {
	switch roleType {
	case domain.RoleTypePlanner:
		return ToolsModeReadOnly, true
	case domain.RoleTypeDeveloper:
		return ToolsModeDevelopment, true
	case domain.RoleTypeValidator:
		return ToolsModeVerification, true
	default:
		return "", false
	}
}
