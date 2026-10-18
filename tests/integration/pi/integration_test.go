//go:build pi_integration

package pi_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/agent/pi"
)

// 真实 Pi 集成测试使用的环境变量。
//
// 默认 go test ./... 不会编译本文件（需要 -tags pi_integration），
// 因此默认测试不依赖外部模型服务。
const (
	// integrationModelEnv 指定集成测试使用的模型标识。
	integrationModelEnv = "AGENTFLOW_PI_MODEL"
	// integrationExecutableEnv 指定 Pi 可执行文件路径，为空时使用 PATH 中的 pi。
	integrationExecutableEnv = "AGENTFLOW_PI_EXECUTABLE"
)

// TestRealPiRun 使用真实 Pi 执行一次最小提示词。
//
// 启用方式：
//
//	AGENTFLOW_PI_MODEL=provider/model-id go test -tags pi_integration ./internal/agent/pi/ -run TestRealPiRun -v
//
// 未设置模型时跳过，避免在缺少模型配置的环境中失败。
func TestRealPiRun(t *testing.T) {
	model := strings.TrimSpace(os.Getenv(integrationModelEnv))
	if model == "" {
		t.Skip("未设置 " + integrationModelEnv + "，跳过真实 Pi 集成测试")
	}
	executable := strings.TrimSpace(os.Getenv(integrationExecutableEnv))
	options := pi.Options{Executable: executable, Timeout: 5 * time.Minute}

	report, err := pi.Check(context.Background(), options, model)
	if err != nil {
		t.Fatalf("Pi 检查失败: %v", err)
	}
	if !report.PrintSupported || !report.SessionSupported {
		t.Fatalf("当前 Pi 版本缺少必需参数: %+v", report)
	}
	if !report.ModelRecognized {
		t.Fatalf("Pi 未识别模型 %s", model)
	}

	// 集成测试使用独立目录，不把真实项目作为工作区。
	workDir := t.TempDir()
	sessionRoot := filepath.Join(t.TempDir(), "sessions")
	runner := pi.NewRunner(options)
	result, err := runner.Run(context.Background(), agent.RunRequest{
		SessionID:   "integration-session",
		Model:       model,
		Prompt:      "只回答 READY 这一个词，不要使用任何工具。",
		WorkingDir:  workDir,
		SessionRoot: sessionRoot,
		Timeout:     5 * time.Minute,
	}, nil)
	if err != nil {
		t.Fatalf("真实执行失败: %v（Session: %s）", err, result.Session.JSONLPath)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
	if result.ExitCode != 0 {
		t.Fatalf("期望退出码 0，实际 %d", result.ExitCode)
	}
	if !strings.Contains(result.FinalText, "READY") {
		t.Fatalf("最终文本不含预期内容: %q", result.FinalText)
	}
	if result.Session.Completeness != agent.SessionComplete {
		t.Fatalf("期望 Session 完整，实际 %s（%s）", result.Session.Completeness, result.Session.Error)
	}
	if _, statErr := os.Stat(result.Session.JSONLPath); statErr != nil {
		t.Fatalf("Session 文件不存在: %v", statErr)
	}
	if result.Session.ExportStatus == agent.ExportSucceeded {
		if _, statErr := os.Stat(result.Session.HTMLPath); statErr != nil {
			t.Fatalf("HTML 文件不存在: %v", statErr)
		}
	}
}
