package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage"
)

// 执行节点校验相关的稳定错误。
var (
	// ErrHostValidationUnavailable 表示节点当前不满足用于真实远程任务的条件。
	ErrHostValidationUnavailable = errors.New("app: 执行节点未通过环境预检")
	// ErrHostInvalidInput 表示执行节点的请求参数不合法。
	ErrHostInvalidInput = errors.New("app: 执行节点参数不合法")
)

// HostValidationOptions 是构造执行节点校验器的参数。
type HostValidationOptions struct {
	// SSH 是已配置的 SSH 服务，负责建立经过验证的连接。
	SSH *SSHService
	// Store 是持久化入口。
	Store storage.Store
	// Logger 是应用日志器。
	Logger *logging.Logger
	// Timeouts 覆盖默认校验超时，零值使用 docker.DefaultValidationTimeouts。
	Timeouts docker.ValidationTimeouts
}

// HostValidator 执行执行节点的环境预检。
//
// 校验按顺序执行三项必需检查：SSH 连接、Docker 版本与 API 兼容性、Docker CLI
// 帮助信息。前一项失败时后续检查记为 not_run，不会伪装成已实际验证。
// 校验器不创建或启动容器、不拉取镜像层，也不改变远端镜像缓存、容器与卷状态。
type HostValidator struct {
	ssh      *SSHService
	store    storage.Store
	logger   *logging.Logger
	timeouts docker.ValidationTimeouts
}

// NewHostValidator 构造执行节点校验器。
func NewHostValidator(options HostValidationOptions) (*HostValidator, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("执行节点校验器初始化失败: 存储不能为空")
	}
	if options.SSH == nil {
		return nil, fmt.Errorf("执行节点校验器初始化失败: SSH 服务不能为空")
	}
	timeouts := options.Timeouts
	if timeouts.TotalSeconds <= 0 {
		timeouts = docker.DefaultValidationTimeouts()
	}
	return &HostValidator{
		ssh:      options.SSH,
		store:    options.Store,
		logger:   options.Logger,
		timeouts: timeouts,
	}, nil
}

// ValidationRequest 是一次校验请求。
type ValidationRequest struct {
	// HostID 是要校验的执行节点。
	HostID string
	// Credentials 提供本次连接所需的凭据。
	//
}

// ValidationOutcome 是一次校验的最终结果。
type ValidationOutcome struct {
	// Host 是校验后的节点配置。
	Host domain.ExecutionHost
	// Status 是总体状态。
	Status domain.ValidationStatus
	// Details 是结构化校验结果。
	Details domain.ValidationDetails
	// Stale 表示本次结果因配置代次变化被丢弃。
	Stale bool
}

// Validate 执行一次完整的节点校验并写回结果。
//
// 返回的 Outcome 始终反映真实执行情况：即使结果因代次过期被丢弃，也会通过
// Stale 标记说明，而不是静默忽略。
func (v *HostValidator) Validate(
	ctx context.Context,
	request ValidationRequest,
) (ValidationOutcome, error) {
	host, err := v.loadHost(ctx, request.HostID)
	if err != nil {
		return ValidationOutcome{}, err
	}
	generation := host.ValidationGeneration
	startedAt := time.Now().UTC()

	totalCtx, cancel := context.WithTimeout(ctx, v.totalTimeout())
	defer cancel()

	details := v.runChecks(totalCtx, *host, startedAt)
	// 结果写回必须使用不受取消影响的上下文：校验被取消或超时同样要把状态收敛为
	// abnormal，否则配置会永久停留在 checking。
	storeCtx := context.WithoutCancel(ctx)
	completedAt := time.Now().UTC()
	details.ValidatorVersion = domain.ValidatorVersion
	details.Generation = generation
	details.StartedAt = startedAt
	details.DurationMillis = completedAt.Sub(startedAt).Milliseconds()

	// 重新读取配置：校验期间用户可能已修改配置，此时代次已增加。
	current, reloadErr := v.loadHost(storeCtx, request.HostID)
	if reloadErr != nil {
		return ValidationOutcome{Host: *host, Details: details}, reloadErr
	}
	if current.ValidationGeneration != generation {
		v.debug(
			ctx,
			"校验结果因配置代次变化被丢弃",
			slog.String("id", host.ID.String()),
			slog.Int64("stale_generation", generation),
			slog.Int64("current_generation", current.ValidationGeneration),
		)
		return ValidationOutcome{Host: *current, Details: details, Stale: true}, nil
	}
	if recordErr := current.RecordValidationResult(generation, details, completedAt); recordErr != nil {
		return ValidationOutcome{Host: *current, Details: details}, recordErr
	}
	applied, applyErr := v.store.UpdateExecutionHostValidation(storeCtx, *current, generation)
	if applyErr != nil {
		return ValidationOutcome{Host: *current, Details: details}, applyErr
	}
	if !applied {
		// 数据库代次已变化：本次结果过期，不能覆盖新配置。
		reloaded, reloadFailure := v.loadHost(storeCtx, request.HostID)
		if reloadFailure != nil {
			return ValidationOutcome{Host: *current, Details: details, Stale: true}, nil
		}
		return ValidationOutcome{Host: *reloaded, Details: details, Stale: true}, nil
	}
	v.debug(
		ctx,
		"执行节点校验完成",
		slog.String("id", host.ID.String()),
		slog.String("status", current.ValidationStatus.String()),
		slog.Int64("generation", generation),
	)
	return ValidationOutcome{
		Host:    *current,
		Status:  current.ValidationStatus,
		Details: details,
	}, nil
}

// FailWithoutChecks 在不执行任何检查的情况下把校验收敛为 abnormal。
//
// 该路径用于调度失败、凭据缺失等"检查根本没有开始"的情况：配置必须收敛为
// abnormal 并给出可读原因，不能永久停留在无法恢复的 checking。
func (v *HostValidator) FailWithoutChecks(
	ctx context.Context,
	hostID string,
	generation int64,
	reason string,
) error {
	host, err := v.loadHost(ctx, hostID)
	if err != nil {
		return err
	}
	if host.ValidationGeneration != generation {
		return nil
	}
	if failErr := host.FailValidation(generation, reason, time.Now().UTC()); failErr != nil {
		return failErr
	}
	_, err = v.store.UpdateExecutionHostValidation(ctx, *host, generation)
	return err
}

// runChecks 依次执行三项必需检查。
//
// 顺序为 SSH 连接 → Docker 版本与 API 兼容性 → Docker CLI 帮助信息。前一项失败时
// 后续检查记为 not_run：SSH 不可用时无法执行任何远程检查，版本不兼容时命令帮助
// 检查也没有意义，不能因为它们可能通过就判定节点可用。
func (v *HostValidator) runChecks(
	ctx context.Context,
	host domain.ExecutionHost,
	startedAt time.Time,
) domain.ValidationDetails {
	details := domain.ValidationDetails{
		Capabilities: []string{
			domain.CapabilityDockerVersionChecked,
			domain.CapabilityDockerCLIHelpOnly,
		},
	}
	sshCheck, client := v.checkSSH(ctx, host, startedAt)
	details.Checks = append(details.Checks, sshCheck)
	if !sshCheck.Status.Passed() {
		details.Checks = append(
			details.Checks,
			notRunCheck(
				domain.ValidationCheckDockerVersion,
				"Docker 版本与 API 兼容性",
				"SSH 检查未通过，未执行 Docker 版本检查",
			),
			notRunCheck(
				domain.ValidationCheckDockerCLIHelp,
				"Docker CLI 帮助信息",
				"SSH 检查未通过，未执行 Docker CLI 帮助检查",
			),
		)
		return details
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			v.debug(ctx, "关闭校验连接失败", slog.String("error", closeErr.Error()))
		}
	}()

	runner, runnerErr := docker.NewRunner(client, docker.RunnerOptions{})
	if runnerErr != nil {
		details.Checks = append(
			details.Checks,
			notRunCheck(
				domain.ValidationCheckDockerVersion,
				"Docker 版本与 API 兼容性",
				"构造远程 Docker 执行器失败，未执行 Docker 版本检查",
			),
			notRunCheck(
				domain.ValidationCheckDockerCLIHelp,
				"Docker CLI 帮助信息",
				"构造远程 Docker 执行器失败，未执行 Docker CLI 帮助检查",
			),
		)
		return details
	}

	versionCheck := v.checkDockerVersion(ctx, runner, startedAt)
	details.Checks = append(details.Checks, versionCheck)
	if !versionCheck.Status.Passed() {
		details.Checks = append(
			details.Checks,
			notRunCheck(
				domain.ValidationCheckDockerCLIHelp,
				"Docker CLI 帮助信息",
				"Docker 版本与 API 兼容性检查未通过，未执行 Docker CLI 帮助检查",
			),
		)
		return details
	}

	details.Checks = append(
		details.Checks,
		v.checkDockerCLIHelp(ctx, runner, startedAt),
	)
	return details
}

// checkDockerVersion 执行 Docker CLI、Engine 与固定 API 基准的兼容性检查。
//
// 检查通过 docker.Preflight 的版本部分完成：CLI 版本与 Engine 版本分别检查，
// API 基准通过只读的 GET /v1.52/version 验证。该检查不创建容器、不拉取镜像。
func (v *HostValidator) checkDockerVersion(
	ctx context.Context,
	runner *docker.Runner,
	startedAt time.Time,
) domain.ValidationCheck {
	check := domain.ValidationCheck{
		Name:      domain.ValidationCheckDockerVersion,
		Title:     "Docker 版本与 API 兼容性",
		Status:    domain.CheckStatusFailed,
		StartedAt: startedAt,
	}
	started := time.Now()
	defer func() {
		completed := time.Now().UTC()
		check.CompletedAt = &completed
		check.DurationMillis = time.Since(started).Milliseconds()
	}()
	versionCtx, cancel := context.WithTimeout(ctx, v.versionTimeout())
	defer cancel()
	probe := runner.ProbeVersion(versionCtx)
	check.Details = append(check.Details, fmt.Sprintf(
		"最低 Docker CLI 版本: %s（实际 %s）",
		docker.MinimumCLIVersion,
		unknownIfEmpty(probe.CLIVersion),
	))
	check.Details = append(check.Details, fmt.Sprintf(
		"最低 Docker Engine 版本: %s（实际 %s）",
		docker.MinimumEngineVersion,
		unknownIfEmpty(probe.EngineVersion),
	))
	check.Details = append(check.Details, fmt.Sprintf(
		"AgentFlow API 基准: %s（Engine 最高 %s，最低接受 %s）",
		docker.EngineAPIBaseVersion,
		unknownIfEmpty(probe.APIVersion),
		unknownIfEmpty(probe.MinAPIVersion),
	))
	if probe.Succeeded() {
		check.Status = domain.CheckStatusPassed
		check.Message = probe.Message
		return check
	}
	check.ErrorCode = probe.Status
	check.Message = probe.Message
	if probe.TimedOut {
		check.Status = domain.CheckStatusTimedOut
	}
	return check
}

// checkDockerCLIHelp 执行 Docker CLI 帮助检查。
func (v *HostValidator) checkDockerCLIHelp(
	ctx context.Context,
	runner *docker.Runner,
	startedAt time.Time,
) domain.ValidationCheck {
	check := domain.ValidationCheck{
		Name:      domain.ValidationCheckDockerCLIHelp,
		Title:     "Docker CLI 帮助信息",
		Status:    domain.CheckStatusFailed,
		StartedAt: startedAt,
	}
	started := time.Now()
	defer func() {
		completed := time.Now().UTC()
		check.CompletedAt = &completed
		check.DurationMillis = time.Since(started).Milliseconds()
	}()
	result := runner.CheckCLIHelp(ctx, docker.RequiredCLICommands(), v.cliHelpTimeout())
	check.ErrorCode = result.Status
	if result.Succeeded() {
		check.Status = domain.CheckStatusPassed
		check.Message = result.Message
		return check
	}
	if result.Status == docker.CLIHelpCodeTimeout {
		check.Status = domain.CheckStatusTimedOut
	}
	check.Message = result.Message
	for _, item := range result.Checks {
		if item.Succeeded() {
			continue
		}
		detail := item.Command.Label() + ": " + item.Message
		if len(item.MissingOptions) > 0 {
			detail += "（缺少 " + strings.Join(item.MissingOptions, ", ") + "）"
		}
		check.Details = append(check.Details, detail)
	}
	return check
}

// checkSSH 执行 SSH 连接检查并返回可复用的连接。
//
// 检查覆盖：连接 SSH 服务、握手、认证、执行一条无副作用的非交互远程命令并取得
// 退出码。任一阶段失败时都给出可定位的分类与说明，不包含凭据内容。
func (v *HostValidator) checkSSH(
	ctx context.Context,
	host domain.ExecutionHost,
	startedAt time.Time,
) (domain.ValidationCheck, sshclient.Client) {
	check := domain.ValidationCheck{
		Name:      domain.ValidationCheckSSHConnection,
		Title:     "SSH 连接",
		Status:    domain.CheckStatusFailed,
		StartedAt: startedAt,
	}
	started := time.Now()
	defer func() {
		completed := time.Now().UTC()
		check.CompletedAt = &completed
		check.DurationMillis = time.Since(started).Milliseconds()
	}()
	if !host.Enabled {
		check.Message = "节点已禁用，请先启用该节点"
		check.ErrorCode = sshclient.CodeInvalidConfig.String()
		return check, nil
	}
	sshCtx, cancel := context.WithTimeout(ctx, v.sshTimeout())
	defer cancel()
	endpoint, authConfig, err := endpointFor(host)
	if err != nil {
		check.Message = err.Error()
		check.ErrorCode = sshclient.CodeInvalidConfig.String()
		return check, nil
	}
	dialResult, dialErr := v.ssh.dialer.Dial(sshCtx, endpoint, authConfig)
	if dialErr != nil {
		check.Message = sshFailureMessage(dialErr)
		check.ErrorCode = sshclient.ErrorCodeOf(dialErr).String()
		return check, nil
	}
	client := dialResult.Client
	command, cmdErr := sshclient.ShellCommand("true")
	if cmdErr != nil {
		_ = client.Close()
		check.Message = "构造远程检查命令失败"
		check.ErrorCode = sshclient.CodeInvalidConfig.String()
		return check, nil
	}
	run, runErr := client.Run(sshCtx, command, sshclient.RunOptions{Timeout: v.sshTimeout()})
	if runErr != nil || !run.Succeeded() {
		_ = client.Close()
		switch {
		case run.Cancelled():
			check.Status = domain.CheckStatusFailed
			check.Message = "远程命令被取消"
			check.ErrorCode = sshclient.CodeCancelled.String()
		case run.TimedOut() || errors.Is(runErr, context.DeadlineExceeded):
			check.Message = "远程命令超时"
			check.ErrorCode = sshclient.CodeTimeout.String()
		case run.Status == sshclient.CommandStatusConnectionError:
			check.Message = "远程命令无法执行: " + summarizeSSHError(runErr)
			check.ErrorCode = sshclient.CodeHandshakeFailed.String()
		default:
			check.Message = fmt.Sprintf(
				"远程命令非零退出（退出码 %d）: %s",
				run.ExitCode,
				summarizeSSHError(runErr),
			)
			check.ErrorCode = "command_exit_error"
		}
		return check, nil
	}
	check.Status = domain.CheckStatusPassed
	check.Message = "SSH 握手、身份认证与远程命令执行均正常"
	return check, client
}

// notRunCheck 构造未执行检查项。
func notRunCheck(name, title, reason string) domain.ValidationCheck {
	return domain.ValidationCheck{
		Name:    name,
		Title:   title,
		Status:  domain.CheckStatusNotRun,
		Message: reason,
	}
}

// sshFailureMessage 生成不含敏感内容的 SSH 失败说明。
func sshFailureMessage(err error) string {
	if err == nil {
		return "SSH 连接失败"
	}
	var connectErr *sshclient.ConnectError
	if errors.As(err, &connectErr) {
		if connectErr.Detail != "" {
			return connectErr.Detail
		}
	}
	return summarizeSSHError(err)
}

// summarizeSSHError 截断错误说明，避免把过长输出写入数据库。
func summarizeSSHError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	const limit = 300
	if len(message) > limit {
		return message[:limit] + "..."
	}
	return message
}

// loadHost 读取执行节点配置。
func (v *HostValidator) loadHost(ctx context.Context, hostID string) (*domain.ExecutionHost, error) {
	id := domain.ID(strings.TrimSpace(hostID))
	if err := id.Validate(); err != nil {
		return nil, fmt.Errorf("读取执行节点失败: %w", err)
	}
	host, err := v.store.GetExecutionHost(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, fmt.Errorf("读取执行节点失败: %w", err)
		}
		return nil, fmt.Errorf("读取执行节点失败: %w", err)
	}
	return host, nil
}

// sshTimeout 返回 SSH 检查超时。
func (v *HostValidator) sshTimeout() time.Duration {
	seconds := v.timeouts.SSHSeconds
	if seconds <= 0 {
		seconds = docker.ValidationSSHTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

// cliHelpTimeout 返回 Docker CLI 帮助检查的超时。
func (v *HostValidator) cliHelpTimeout() time.Duration {
	seconds := v.timeouts.CLIHelpSeconds
	if seconds <= 0 {
		seconds = docker.ValidationCLIHelpTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

// versionTimeout 返回 Docker 版本与 API 兼容性检查的超时。
func (v *HostValidator) versionTimeout() time.Duration {
	seconds := v.timeouts.VersionSeconds
	if seconds <= 0 {
		seconds = docker.ValidationVersionTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

// unknownIfEmpty 把空值转换为可读占位符，避免界面出现空白版本字段。
func unknownIfEmpty(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "未取得"
	}
	return trimmed
}

// totalTimeout 返回整体校验超时。
func (v *HostValidator) totalTimeout() time.Duration {
	seconds := v.timeouts.TotalSeconds
	if seconds <= 0 {
		seconds = docker.ValidationTotalTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

// debug 输出调试日志；未配置日志器时忽略。
func (v *HostValidator) debug(ctx context.Context, message string, attrs ...slog.Attr) {
	if v == nil || v.logger == nil {
		return
	}
	v.logger.Slog().LogAttrs(ctx, slog.LevelDebug, message, attrs...)
}
