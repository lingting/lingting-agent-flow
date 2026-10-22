package pi_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/tests/internal/fakepi"
)

// TestMain 让测试二进制同时充当假 Pi 子进程。
//
// 检查命令仍然通过本机子进程探测 Pi 的版本与帮助信息，因此需要可控的替身。
func TestMain(m *testing.M) {
	if scenario := os.Getenv(fakepi.EnvScenario); scenario != "" {
		os.Exit(fakepi.Run())
	}
	os.Exit(m.Run())
}

// checkOptions 返回让检查命令使用假 Pi 替身的选项。
func checkOptions(t *testing.T, scenario string) pi.Options {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("获取测试可执行文件失败: %v", err)
	}
	return pi.Options{
		Executable: executable,
		Env: append(
			os.Environ(),
			fakepi.EnvScenario+"="+scenario,
		),
	}
}

// TestCheckReportsCapabilities 验证检查命令识别非交互式执行所需的参数。
func TestCheckReportsCapabilities(t *testing.T) {
	t.Parallel()
	report, err := pi.Check(context.Background(), checkOptions(t, "normal"), "")
	if err != nil {
		t.Fatalf("检查失败: %v", err)
	}
	if report.Executable == "" || report.Version == "" {
		t.Fatalf("检查结果缺少可执行程序或版本: %+v", report)
	}
	if !report.PrintSupported {
		t.Fatalf("应当识别到 --print: %+v", report)
	}
	if !report.SessionSupported {
		t.Fatalf("应当识别到 --session: %+v", report)
	}
	if !report.ExportSupported {
		t.Fatalf("应当识别到 --export: %+v", report)
	}
	if report.Model != "" || report.AuthStatus != "" {
		t.Fatalf("未指定模型时不应检查模型: %+v", report)
	}
}

// TestCheckRejectsOldVersion 验证缺少必需参数时返回明确错误。
func TestCheckRejectsOldVersion(t *testing.T) {
	t.Parallel()
	report, err := pi.Check(context.Background(), checkOptions(t, "old_version"), "")
	if err == nil {
		t.Fatal("缺少必需参数时应当返回错误")
	}
	if !strings.Contains(err.Error(), "--print") {
		t.Fatalf("错误信息应指出缺少的参数: %v", err)
	}
	if report.Version == "" {
		t.Fatal("即使检查失败也应报告版本")
	}
}

// TestCheckModelRecognition 验证模型识别与凭据状态查询。
func TestCheckModelRecognition(t *testing.T) {
	t.Parallel()
	report, err := pi.Check(
		context.Background(),
		checkOptions(t, "normal"),
		"fake/model-1",
	)
	if err != nil {
		t.Fatalf("检查失败: %v", err)
	}
	if !report.ModelRecognized {
		t.Fatalf("应当识别到模型: %+v", report)
	}
	if report.AuthStatus != "ready" {
		t.Fatalf("凭据状态不符: %q", report.AuthStatus)
	}
}

// TestCheckUnknownModel 验证未知模型不会被误判为可用。
func TestCheckUnknownModel(t *testing.T) {
	t.Parallel()
	report, err := pi.Check(
		context.Background(),
		checkOptions(t, "normal"),
		"fake/absent-model",
	)
	if err != nil {
		t.Fatalf("检查失败: %v", err)
	}
	if report.ModelRecognized {
		t.Fatalf("未知模型不应被识别: %+v", report)
	}
}

// TestCheckMissingExecutable 验证 Pi 不可用时检查失败。
func TestCheckMissingExecutable(t *testing.T) {
	t.Parallel()
	_, err := pi.Check(
		context.Background(),
		pi.Options{Executable: "agentflow-not-installed-pi"},
		"",
	)
	if err == nil {
		t.Fatal("期望检查失败")
	}
	if !strings.Contains(err.Error(), "未找到可执行程序") {
		t.Fatalf("错误信息应说明可执行程序缺失: %v", err)
	}
}
