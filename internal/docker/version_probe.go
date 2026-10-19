package docker

import (
	"context"
	"fmt"
	"strings"
)

// VersionProbe 是一次 Docker 版本与 API 兼容性探测的结果。
//
// CLI 版本、Engine 版本与 API 版本区间分别记录：三者可能来自不同版本，
// 不得把 CLI 版本当成 Engine 版本，也不得把服务端最低接受版本与 API 基准直接比较。
type VersionProbe struct {
	// CLIVersion 是实际执行命令的 Docker CLI 版本。
	CLIVersion string
	// EngineVersion 是 CLI 所连接 Engine 的版本。
	EngineVersion string
	// APIVersion 是 Engine 声明的最高 API 版本。
	APIVersion string
	// MinAPIVersion 是 Engine 接受的最低 API 版本。
	MinAPIVersion string
	// Status 是失败分类，成功时为空。
	Status string
	// Message 是可读说明。
	Message string
	// TimedOut 表示探测因超时未完成。
	TimedOut bool
}

// Succeeded 判断探测是否通过。
func (p VersionProbe) Succeeded() bool {
	return p.Status == ""
}

// ProbeVersion 检查 Docker CLI、Engine 与固定 API 基准的兼容性。
//
// 探测阶段显式隔离 DOCKER_API_VERSION，避免远端遗留变量关闭 CLI 的自动版本协商而使结果失真；
// API 基准通过固定基准路径的只读 GET /v1.52/version 验证。该方法不创建容器、不拉取镜像，
// 也不改变远端任何状态。
func (r *Runner) ProbeVersion(ctx context.Context) VersionProbe {
	probe := VersionProbe{}
	cliCommand, err := dockerProbeCommand("version", "--format", "{{.Client.Version}}")
	if err != nil {
		probe.Status = CodeInvalidArgument.String()
		probe.Message = err.Error()
		return probe
	}
	cliRun, cliErr := r.RunQuery(ctx, "docker version", cliCommand)
	if cliErr != nil {
		probe.Status = classifyPreflightCode(cliRun, cliErr).String()
		probe.Message = summarizeOutput(cliRun.Stderr + "\n" + cliRun.Stdout)
		if probe.Message == "" {
			probe.Message = cliErr.Error()
		}
		probe.TimedOut = cliRun.TimedOut()
		return probe
	}
	probe.CLIVersion = strings.TrimSpace(cliRun.Stdout)
	if check := CheckMinimumVersion("Docker CLI", probe.CLIVersion, MinimumCLIVersion); !check.Succeeded() {
		probe.Status = CodeVersionUnsupported.String()
		probe.Message = check.Err.Error()
		return probe
	}

	engineCommand, err := dockerProbeCommand("info", "--format", "{{.ServerVersion}}")
	if err != nil {
		probe.Status = CodeInvalidArgument.String()
		probe.Message = err.Error()
		return probe
	}
	engineRun, engineErr := r.RunQuery(ctx, "docker info", engineCommand)
	if engineErr != nil {
		probe.Status = classifyPreflightCode(engineRun, engineErr).String()
		probe.Message = summarizeOutput(engineRun.Stderr + "\n" + engineRun.Stdout)
		if probe.Message == "" {
			probe.Message = engineErr.Error()
		}
		probe.TimedOut = engineRun.TimedOut()
		return probe
	}
	probe.EngineVersion = strings.TrimSpace(engineRun.Stdout)
	if check := CheckMinimumVersion("Docker Engine", probe.EngineVersion, MinimumEngineVersion); !check.Succeeded() {
		probe.Status = CodeVersionUnsupported.String()
		probe.Message = check.Err.Error()
		return probe
	}

	apiResult := r.ProbeAPIVersion(ctx, DefaultDistributionTimeout)
	probe.APIVersion = apiResult.APIVersion
	probe.MinAPIVersion = apiResult.MinAPIVersion
	if !apiResult.Succeeded() {
		probe.Status = CodeVersionUnsupported.String()
		probe.Message = apiResult.Message
		probe.TimedOut = apiResult.Status == APICheckCodeTimeout
		return probe
	}
	probe.Message = fmt.Sprintf(
		"Docker CLI %s、Engine %s 满足最低版本 %s；固定 API 基准 %s 在 Engine 上可用（最高 %s，最低接受 %s）",
		probe.CLIVersion,
		probe.EngineVersion,
		MinimumCLIVersion,
		EngineAPIBaseVersion,
		probe.APIVersion,
		probe.MinAPIVersion,
	)
	return probe
}
