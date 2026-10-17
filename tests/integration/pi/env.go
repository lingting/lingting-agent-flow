// Package pi_integration 提供需要本机真实 Pi CLI 的集成测试入口。
//
// 运行方式：
//
//	go test -count=1 -v ./tests/integration/pi/...
//
// 本机不存在 pi 时，依赖真实 CLI 的用例输出 WARN 并跳过；Pi Runner 的单元测试
// 位于 tests/unit/agent/pi，不依赖本机是否安装 Pi。
package pi_integration

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// 真实 Pi 集成测试使用的环境变量。
const (
	// EnvModel 覆盖真实 Pi 集成测试使用的模型标识。
	EnvModel = "AGENTFLOW_TEST_PI_MODEL"
	// EnvExecutable 指定 Pi 可执行文件路径，为空时使用 PATH 中的 pi。
	EnvExecutable = "AGENTFLOW_TEST_PI_EXECUTABLE"
	// DefaultModel 是真实 Pi 集成测试的默认模型。
	//
	// 该标识特意选用非标准名称，避免测试意外消耗付费模型额度；
	// 不得改为任何常见的付费模型标识。
	DefaultModel = "group/free"
)

// requirePi 返回可用于真实调用的 Pi 可执行文件路径。
//
// 本机不存在 Pi 时输出警告并跳过当前用例；Pi 存在但后续调用失败时必须由用例
// 如实报告失败，不得在这里当作缺少可选依赖跳过。
func requirePi(t *testing.T) string {
	t.Helper()
	executable := strings.TrimSpace(os.Getenv(EnvExecutable))
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
// 环境变量存在且非空时使用其值，否则使用 DefaultModel；每次调用都必须显式传入，
// 不允许回退到 Pi CLI 配置中的默认模型。
func resolveModel(t *testing.T) string {
	t.Helper()
	model := strings.TrimSpace(os.Getenv(EnvModel))
	if model == "" {
		model = DefaultModel
	}
	t.Logf("PI_MODEL=%s", model)
	return model
}
