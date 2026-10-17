package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
)

// validYAML 是一份可以通过校验的配置，用于构造各类失败用例的基础。
const validYAML = `
app:
  name: agentflow
  log_level: info
storage:
  database_path: .agentflow/agentflow.db
workspace:
  worktree_root: .agentflow/worktrees
scheduler:
  max_concurrent_tasks: 3
  max_retries_per_step: 2
roles:
  - id: planner
    name: 架构规划师
    type: planner
    runner: pi
    model: "provider/model-id"
    system_prompt: 生成开发计划
    tools_policy:
      mode: read_only
  - id: developer
    name: 开发工程师
    type: developer
    runner: pi
    model: "provider/model-id"
    system_prompt: 实现代码
    tools_policy:
      mode: development
`

// writeConfig 在临时目录写入配置文件并返回其路径。
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentflow.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入临时配置失败: %v", err)
	}
	return path
}

// TestLoadValidConfig 校验有效配置可以成功加载。
func TestLoadValidConfig(t *testing.T) {
	path := writeConfig(t, validYAML)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("加载有效配置失败: %v", err)
	}
	if cfg.SourcePath != path {
		t.Fatalf("配置来源路径不符: 期望 %s，实际 %s", path, cfg.SourcePath)
	}
	if cfg.Scheduler.MaxConcurrentTasks != 3 {
		t.Fatalf("并发数解析错误: %d", cfg.Scheduler.MaxConcurrentTasks)
	}
	if len(cfg.Roles) != 2 {
		t.Fatalf("角色数量不符: %d", len(cfg.Roles))
	}
	if cfg.Roles[0].Type != domain.RoleTypePlanner {
		t.Fatalf("角色类型解析错误: %s", cfg.Roles[0].Type)
	}
}

// TestLoadSyntaxError 校验 YAML 语法错误返回可定位的错误。
func TestLoadSyntaxError(t *testing.T) {
	path := writeConfig(t, "app:\n  name: agentflow\n bad-indent: 1\n")
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("语法错误应返回错误")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("错误信息应包含文件路径，实际: %v", err)
	}
}

// TestLoadUnknownField 校验未知字段被拒绝。
func TestLoadUnknownField(t *testing.T) {
	path := writeConfig(t, validYAML+"\nunknown_section:\n  value: 1\n")
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("未知字段应返回错误")
	}
	if !strings.Contains(err.Error(), "unknown_section") {
		t.Fatalf("错误信息应指出未知字段，实际: %v", err)
	}
}

// TestLoadMissingFile 校验配置文件不存在时返回明确错误。
func TestLoadMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	_, err := config.Load(missing)
	if err == nil {
		t.Fatal("文件不存在应返回错误")
	}
	if !strings.Contains(err.Error(), "配置文件不存在") {
		t.Fatalf("错误信息应说明文件不存在，实际: %v", err)
	}
}

// TestValidateMissingFields 校验缺失必填字段时失败。
func TestValidateMissingFields(t *testing.T) {
	content := `
app:
  name: ""
scheduler:
  max_concurrent_tasks: 3
roles:
  - id: planner
    type: planner
    runner: pi
    tools_policy:
      mode: read_only
`
	path := writeConfig(t, content)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("缺失必填字段应返回错误")
	}
	var validation *config.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("应返回 ValidationError，实际: %T", err)
	}
	issues := strings.Join(validation.Issues, "\n")
	for _, field := range []string{
		"app.name",
		"app.log_level",
		"storage.database_path",
		"workspace.worktree_root",
		"roles[0].name",
		"roles[0].model",
		"roles[0].system_prompt",
	} {
		if !strings.Contains(issues, field) {
			t.Fatalf("校验问题应包含 %s，实际:\n%s", field, issues)
		}
	}
}

// TestValidateDuplicateRoleID 校验重复角色 ID 失败。
func TestValidateDuplicateRoleID(t *testing.T) {
	content := strings.Replace(
		validYAML,
		"  - id: developer",
		"  - id: planner",
		1,
	)
	path := writeConfig(t, content)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("重复角色 ID 应返回错误")
	}
	if !strings.Contains(err.Error(), "重复") {
		t.Fatalf("错误信息应说明重复，实际: %v", err)
	}
}

// TestValidateInvalidRoleType 校验无效角色类型失败。
func TestValidateInvalidRoleType(t *testing.T) {
	content := strings.Replace(validYAML, "type: planner", "type: reviewer", 1)
	path := writeConfig(t, content)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("无效角色类型应返回错误")
	}
	if !strings.Contains(err.Error(), "roles[0].type") {
		t.Fatalf("错误信息应指出角色类型字段，实际: %v", err)
	}
}

// TestValidateInvalidScheduler 校验非法并发数与重试次数失败。
func TestValidateInvalidScheduler(t *testing.T) {
	cases := map[string]string{
		"并发数为零": strings.Replace(
			validYAML,
			"max_concurrent_tasks: 3",
			"max_concurrent_tasks: 0",
			1,
		),
		"重试次数为负": strings.Replace(
			validYAML,
			"max_retries_per_step: 2",
			"max_retries_per_step: -1",
			1,
		),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, content)
			_, err := config.Load(path)
			if err == nil {
				t.Fatal("非法调度配置应返回错误")
			}
			if !strings.Contains(err.Error(), "scheduler.") {
				t.Fatalf("错误信息应指出调度字段，实际: %v", err)
			}
		})
	}
}

// TestValidateToolsPolicyMode 校验工具策略模式与角色类型必须匹配。
func TestValidateToolsPolicyMode(t *testing.T) {
	content := strings.Replace(validYAML, "mode: read_only", "mode: development", 1)
	path := writeConfig(t, content)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("模式与类型不匹配应返回错误")
	}
	if !strings.Contains(err.Error(), "tools_policy.mode") {
		t.Fatalf("错误信息应指出工具策略字段，实际: %v", err)
	}
}
