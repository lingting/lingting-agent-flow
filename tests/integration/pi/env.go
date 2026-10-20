// Package pi_integration 提供需要本机真实 Pi CLI 的集成测试入口。
//
// 运行方式：
//
//	cp tests/config/example.pi.yml tests/config/pi.yml   # 按需填写后执行
//	go test -count=1 -v ./tests/integration/pi/...
//
// 配置项来自 tests/config/pi.yml，字段说明见 tests/config/example.pi.yml。
// 本机不存在 pi 时，依赖真实 CLI 的用例输出 WARN 并跳过；Pi Runner 的单元测试
// 位于 tests/unit/agent/pi，不依赖本机是否安装 Pi。
package pi_integration

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/lingting/agentflow/tests/internal/testkit"
)

// requirePi 返回可用于真实调用的 Pi 可执行文件路径。
//
// 本机不存在 Pi 时输出警告并跳过当前用例；Pi 存在但后续调用失败时必须由用例
// 如实报告失败，不得在这里当作缺少可选依赖跳过。
func requirePi(t *testing.T) string {
	t.Helper()
	config, _ := testkit.LoadPiConfig(t)
	executable := strings.TrimSpace(config.Executable)
	if executable == "" {
		found, err := exec.LookPath("pi")
		if err != nil {
			fmt.Fprintf(os.Stdout,
				"WARN [SKIP] Pi integration tests: executable \"pi\" not found in PATH.\n")
			t.Skip("WARN [SKIP] Pi integration tests: executable \"pi\" not found in PATH")
		}
		executable = found
	}
	return executable
}

// resolveModel 返回本次真实调用使用的模型标识。
//
// 取值来自 tests/config/pi.yml 的 pi.model，未填写时使用默认免费模型；
// 每次调用都必须显式传入，不允许回退到 Pi CLI 配置中的默认模型。
func resolveModel(t *testing.T) string {
	t.Helper()
	config, _ := testkit.LoadPiConfig(t)
	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = testkit.DefaultPiModel
	}
	t.Logf("PI_MODEL=%s", model)
	return model
}
