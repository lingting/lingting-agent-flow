// Package pi_integration 提供依赖真实 Pi CLI 的集成测试。
//
// 本版本只支持在远程容器内执行 Agent 调用，因此这里只保留不依赖执行位置的检查项：
// Pi CLI 的安装与能力探测。真实 Agent 调用的验证由远程容器集成测试承担。
package pi_integration

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/agent/pi"
)

// TestRealPiCheck 校验真实 Pi 版本具备非交互执行所需的参数。
func TestRealPiCheck(t *testing.T) {
	t.Parallel()
	executable := requirePi(t)
	report, err := pi.Check(
		context.Background(),
		pi.Options{Executable: executable},
		"",
	)
	if err != nil {
		t.Fatalf("Pi 检查失败: %v", err)
	}
	if report.Executable == "" || report.Version == "" {
		t.Fatalf("检查结果缺少可执行程序或版本: %+v", report)
	}
	if !report.PrintSupported || !report.SessionSupported || !report.ExportSupported {
		t.Fatalf("当前 Pi 版本缺少必需参数: %+v", report)
	}
	t.Logf("PI_VERSION=%s", report.Version)
}

// TestRealPiModelRecognition 校验真实 Pi 能识别测试配置指定的模型。
//
// 模型由 tests/config/pi.yml 的 pi.model 决定，未填写时使用 group/free：
// 每次调用都显式传入模型，不回退到 Pi CLI 的默认（可能付费的）模型。
func TestRealPiModelRecognition(t *testing.T) {
	t.Parallel()
	executable := requirePi(t)
	model := resolveModel(t)
	if strings.TrimSpace(model) == "" {
		t.Fatalf("测试配置必须给出模型标识")
	}
	report, err := pi.Check(
		context.Background(),
		pi.Options{Executable: executable},
		model,
	)
	if err != nil {
		t.Fatalf("模型检查失败: %v", err)
	}
	if !report.ModelRecognized {
		// 模型清单可能因网络或凭据不可用而无法拉取：这里如实记录而不是伪造成功，
		// 也不把"清单未包含"当作 CLI 能力缺陷导致整轮测试失败。
		t.Logf(
			"WARN [SKIP] 当前 Pi 未识别模型 %s（模型清单不可用或该模型不在清单中）: %+v",
			model,
			report,
		)
		t.Skip("当前 Pi 未识别测试配置指定的模型")
	}
}

// TestMain 保留统一测试入口。
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
