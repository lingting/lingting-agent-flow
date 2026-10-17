package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 检查命令使用的超时。
const (
	// checkTimeout 是单项检查的超时时间。
	checkTimeout = 30 * time.Second
)

// CheckReport 是一次 Pi 安装与能力检查的结果。
//
// 检查只验证非交互式执行所需的条件：可执行程序、版本、必需参数是否出现在帮助中、
// 以及模型是否被 Pi 识别。检查不会发送提示词，因此不消耗模型调用。
type CheckReport struct {
	// Executable 是解析后的 Pi 可执行程序路径。
	Executable string
	// Version 是 Pi 报告的版本号，无法获取时为空。
	Version string
	// PrintSupported 表示帮助信息中声明了 --print。
	PrintSupported bool
	// SessionSupported 表示帮助信息中声明了 --session。
	SessionSupported bool
	// ExportSupported 表示帮助信息中声明了 --export。
	ExportSupported bool
	// Model 是被检查的模型标识，为空表示未检查。
	Model string
	// ModelRecognized 表示 Pi 在模型列表中返回了该模型。
	ModelRecognized bool
	// AuthStatus 是 Pi 报告的凭据状态，未检查时为空。
	AuthStatus string
}

// Check 检查 Pi 是否可用以及是否支持非交互式 CLI 执行。
//
// model 非空时额外检查 Pi 是否识别该模型，并查询该模型的凭据状态。
func Check(ctx context.Context, options Options, model string) (CheckReport, error) {
	options = optionsWithDefaults(options)
	report := CheckReport{Model: strings.TrimSpace(model)}
	path, err := resolveExecutable(options.Executable)
	if err != nil {
		return report, err
	}
	report.Executable = path
	version, err := checkVersion(ctx, options)
	if err != nil {
		return report, err
	}
	report.Version = version
	help, err := checkHelp(ctx, options)
	if err != nil {
		return report, err
	}
	report.PrintSupported = containsFlag(help, argPrint)
	report.SessionSupported = containsFlag(help, argSession)
	report.ExportSupported = containsFlag(help, argExport)
	if err := checkRequiredFlags(report); err != nil {
		return report, err
	}
	if report.Model == "" {
		return report, nil
	}
	recognized, err := checkModel(ctx, options, report.Model)
	if err != nil {
		return report, err
	}
	report.ModelRecognized = recognized
	status, err := checkAuth(ctx, options, report.Model)
	if err != nil {
		return report, err
	}
	report.AuthStatus = status
	return report, nil
}

// checkRequiredFlags 确认非交互式执行所需的参数都出现在帮助信息中。
func checkRequiredFlags(report CheckReport) error {
	var missing []string
	if !report.PrintSupported {
		missing = append(missing, argPrint)
	}
	if !report.SessionSupported {
		missing = append(missing, argSession)
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"pi: 当前版本未声明必需的参数 %s，请升级 Pi（当前版本 %s）",
		strings.Join(missing, "、"),
		report.Version,
	)
}

// checkVersion 读取 Pi 版本号。
func checkVersion(ctx context.Context, options Options) (string, error) {
	stdout, exit, err := runOnce(ctx, options, []string{argVersion}, checkTimeout)
	if err != nil {
		return "", err
	}
	if exit.Code != 0 {
		return "", fmt.Errorf(
			"pi: 版本查询失败（退出码 %d）: %s",
			exit.Code,
			strings.TrimSpace(stdout),
		)
	}
	version := strings.TrimSpace(stdout)
	if version == "" {
		return "", fmt.Errorf("pi: 版本查询没有输出")
	}
	return strings.Split(version, "\n")[0], nil
}

// checkHelp 读取 Pi 的命令行帮助。
func checkHelp(ctx context.Context, options Options) (string, error) {
	stdout, exit, err := runOnce(ctx, options, []string{argHelp}, checkTimeout)
	if err != nil {
		return "", err
	}
	if exit.Code != 0 {
		return "", fmt.Errorf("pi: 帮助信息查询失败（退出码 %d）", exit.Code)
	}
	if strings.TrimSpace(stdout) == "" {
		return "", fmt.Errorf("pi: 帮助信息为空，无法确认参数支持情况")
	}
	return stdout, nil
}

// containsFlag 判断帮助信息中是否声明了指定参数。
func containsFlag(help, flag string) bool {
	for _, line := range strings.Split(help, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		for _, field := range fields {
			if strings.TrimSuffix(field, ",") == flag {
				return true
			}
		}
	}
	return false
}

// checkModel 判断 Pi 的模型列表中是否包含指定模型。
//
// Pi 的 --list-models 输出为对齐表格，模型标识位于第二列；这里按列匹配而不是
// 子串匹配，避免 provider/model 与 model 相互误判。
func checkModel(ctx context.Context, options Options, model string) (bool, error) {
	stdout, exit, err := runOnce(ctx, options, []string{argListModels}, checkTimeout)
	if err != nil {
		return false, err
	}
	if exit.Code != 0 {
		return false, fmt.Errorf("pi: 模型列表查询失败（退出码 %d）", exit.Code)
	}
	provider, identifier := splitModel(model)
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if provider == "" {
			if fields[1] == identifier {
				return true, nil
			}
			continue
		}
		if fields[0] == provider && fields[1] == identifier {
			return true, nil
		}
	}
	return false, nil
}

// splitModel 拆分 provider/model 形式的模型标识。
//
// 模型标识中可能包含多个斜杠，只有第一段是 provider。
func splitModel(model string) (string, string) {
	index := strings.Index(model, "/")
	if index <= 0 || index == len(model)-1 {
		return "", model
	}
	return model[:index], model[index+1:]
}

// checkAuth 查询指定模型的凭据状态。
//
// 凭据不可用不属于检查失败：它表示当前环境尚未完成登录，需要用户自行处理。
// 只有命令本身无法执行时才返回错误。
func checkAuth(ctx context.Context, options Options, model string) (string, error) {
	args := []string{argAuthCheck, authSubcommandCheck, argModel, model, argJSON}
	stdout, exit, err := runOnce(ctx, options, args, checkTimeout)
	if err != nil {
		return "", nil
	}
	status := parseAuthStatus(stdout)
	if status != "" {
		return status, nil
	}
	if exit.Code != 0 {
		return "unknown", nil
	}
	return "unknown", nil
}

// authResult 是 pi auth check --json 的输出结构。
type authResult struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// parseAuthStatus 从结构化输出中提取凭据状态。
func parseAuthStatus(stdout string) string {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return ""
	}
	var result authResult
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil {
		return ""
	}
	return strings.TrimSpace(result.Status)
}

// optionsWithDefaults 补齐检查所需的默认配置。
func optionsWithDefaults(options Options) Options {
	if options.GraceTimeout <= 0 {
		options.GraceTimeout = DefaultGraceTimeout
	}
	if options.ExportTimeout <= 0 {
		options.ExportTimeout = DefaultExportTimeout
	}
	if options.Timeout <= 0 {
		options.Timeout = DefaultTimeout
	}
	return options
}
