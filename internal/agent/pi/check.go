package pi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/process"
)

// 检查命令使用的参数与超时。
const (
	// argVersion 打印 Pi 版本。
	argVersion = "--version"
	// argHelp 打印 Pi 的命令行帮助。
	argHelp = "--help"
	// checkTimeout 是单项检查的超时时间。
	checkTimeout = 30 * time.Second
)

// CheckReport 是一次 Pi 安装检查的结果。
type CheckReport struct {
	// Executable 是解析后的 Pi 可执行程序路径。
	Executable string
	// Version 是 Pi 报告的版本号，无法获取时为空。
	Version string
	// RPCModeSupported 表示帮助信息中声明了 rpc 输出模式。
	RPCModeSupported bool
	// StateHandshake 表示 get_state 请求已收到成功响应。
	StateHandshake bool
	// ExternalSessionID 是 get_state 返回的 Pi 会话标识。
	ExternalSessionID string
	// ExitCode 是关闭检查进程后的退出码。
	ExitCode int
	// Stderr 是检查过程中收集的诊断输出。
	Stderr string
}

// Check 检查 Pi 是否可用、是否支持 RPC 模式以及能否完成一次协议握手。
//
// 检查使用真实的 Pi 子进程，但不发送提示词，因此不会调用模型。
func Check(ctx context.Context, options Options) (CheckReport, error) {
	report := CheckReport{ExitCode: -1}
	path, err := resolveExecutable(options.Executable)
	if err != nil {
		return report, err
	}
	report.Executable = path
	if err := checkVersion(ctx, options, path, &report); err != nil {
		return report, err
	}
	if err := checkRPCMode(ctx, options, path, &report); err != nil {
		return report, err
	}
	if err := checkHandshake(ctx, options, path, &report); err != nil {
		return report, err
	}
	return report, nil
}

// checkVersion 读取 Pi 版本号。
func checkVersion(ctx context.Context, options Options, path string, report *CheckReport) error {
	stdout, exit, err := runOnce(ctx, options, path, argVersion)
	if err != nil {
		return err
	}
	if exit.Code != 0 {
		return fmt.Errorf("pi: 版本查询失败（退出码 %d）: %s", exit.Code, strings.TrimSpace(stdout))
	}
	version := strings.TrimSpace(stdout)
	if version == "" {
		return fmt.Errorf("pi: 版本查询没有输出")
	}
	report.Version = strings.Split(version, "\n")[0]
	return nil
}

// checkRPCMode 检查帮助信息中是否声明了 rpc 输出模式。
func checkRPCMode(ctx context.Context, options Options, path string, report *CheckReport) error {
	stdout, exit, err := runOnce(ctx, options, path, argHelp)
	if err != nil {
		return err
	}
	if exit.Code != 0 {
		return fmt.Errorf("pi: 帮助信息查询失败（退出码 %d）", exit.Code)
	}
	report.RPCModeSupported = strings.Contains(stdout, modeRPC)
	if !report.RPCModeSupported {
		return fmt.Errorf("pi: 当前版本未声明 rpc 输出模式，请升级 Pi")
	}
	return nil
}

// runOnce 运行一次短生命周期的 Pi 子进程并收集 stdout。
func runOnce(
	ctx context.Context,
	options Options,
	path string,
	arg string,
) (string, process.Exit, error) {
	runCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	if err := runCtx.Err(); err != nil {
		return "", process.Exit{Code: -1}, err
	}
	supervisor, err := process.Start(
		process.Command{Path: path, Args: []string{arg}, Env: options.Env},
		process.Options{GraceTimeout: resolveGrace(options)},
	)
	if err != nil {
		return "", process.Exit{Code: -1}, err
	}
	collected := make(chan collectedOutput, 1)
	go collectOutput(supervisor, collected)
	select {
	case output := <-collected:
		exit, shutdownErr := supervisor.Shutdown(resolveGrace(options))
		if output.err != nil {
			return output.text, exit, output.err
		}
		return output.text, exit, shutdownErr
	case <-runCtx.Done():
		_, _ = supervisor.Shutdown(resolveGrace(options))
		return "", process.Exit{Code: -1}, runCtx.Err()
	}
}

// collectedOutput 是收集到的子进程输出。
type collectedOutput struct {
	text string
	err  error
}

// collectOutput 读取子进程全部 stdout，直到输出流关闭。
func collectOutput(supervisor *process.Supervisor, result chan<- collectedOutput) {
	var builder strings.Builder
	for line := range supervisor.Lines() {
		if line.Err != nil {
			result <- collectedOutput{text: builder.String(), err: line.Err}
			return
		}
		builder.WriteString(line.Text)
		builder.WriteByte('\n')
	}
	result <- collectedOutput{text: builder.String()}
}

// checkHandshake 启动 RPC 子进程并完成一次 get_state 握手。
func checkHandshake(ctx context.Context, options Options, path string, report *CheckReport) error {
	runCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	supervisor, err := process.Start(
		process.Command{
			Path: path,
			Args: arguments(""),
			Env:  options.Env,
		},
		process.Options{GraceTimeout: resolveGrace(options)},
	)
	if err != nil {
		return err
	}
	defer func() {
		exit, _ := supervisor.Shutdown(resolveGrace(options))
		report.ExitCode = exit.Code
		report.Stderr = strings.TrimSpace(supervisor.Stderr())
	}()

	payload, err := encodeCommand(command{ID: "agentflow-check", Type: commandGetState})
	if err != nil {
		return err
	}
	if err := supervisor.Write(payload); err != nil {
		return err
	}
	response, err := awaitProbe(runCtx, supervisor.Lines(), resolveResponseTimeout(options))
	if err != nil {
		return err
	}
	if !response.Success {
		return fmt.Errorf("pi: get_state 请求失败: %s", snippet(response.Error))
	}
	var data stateData
	if err := response.decodeData(&data); err != nil {
		return err
	}
	report.StateHandshake = true
	report.ExternalSessionID = data.SessionID
	// 握手结束后继续排空输出，避免关闭进程时因管道背压阻塞。
	go drain(supervisor.Lines())
	return nil
}

// awaitProbe 等待检查用请求的响应，超时返回错误。
func awaitProbe(
	ctx context.Context,
	lines <-chan process.Line,
	timeout time.Duration,
) (record, error) {
	dec := &decoder{}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return record{}, ctx.Err()
		case <-timer.C:
			return record{}, fmt.Errorf("pi: 等待 get_state 响应超时（%s）", timeout)
		case line, ok := <-lines:
			if !ok {
				return record{}, fmt.Errorf("pi: 输出流在 get_state 响应之前关闭")
			}
			if line.Err != nil {
				return record{}, line.Err
			}
			rec, err := dec.decode(line.Text)
			if err != nil {
				return record{}, err
			}
			if rec.isResponse() {
				return rec, nil
			}
		}
	}
}

// resolveGrace 返回生效的关闭宽限时间。
func resolveGrace(options Options) time.Duration {
	if options.GraceTimeout > 0 {
		return options.GraceTimeout
	}
	return DefaultGraceTimeout
}

// resolveResponseTimeout 返回生效的响应超时时间。
func resolveResponseTimeout(options Options) time.Duration {
	if options.ResponseTimeout > 0 {
		return options.ResponseTimeout
	}
	return DefaultResponseTimeout
}
