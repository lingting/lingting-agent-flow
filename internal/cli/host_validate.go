package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/spf13/cobra"
)

// newHostValidateCommand 构建 host validate 子命令。
//
// 该命令同步执行一次完整的执行节点环境预检，并输出三项必需检查的结果。它是
// 唯一可以携带一次性内存凭据的校验入口：密码与带口令私钥无法由后台服务自行
// 取得凭据，因此由命令层在内存中提供，校验结束后立即清除，不写入数据库或日志。
func newHostValidateCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	command := &cobra.Command{
		Use:   "validate <id>",
		Short: "执行节点环境预检（SSH、Docker CLI 帮助、基础镜像元数据）",
		Long: "同步执行一次完整的执行节点环境预检，依次检查：\n" +
			"  1. SSH 连接：握手、认证与无副作用的远程命令；\n" +
			"  2. Docker CLI 帮助信息：核对 AgentFlow 实际使用的命令与参数选项；\n" +
			"  3. 基础 Dockerfile 的外部 FROM 镜像元数据（不下载镜像层）。\n" +
			"第一版 Docker 检查只基于 CLI 帮助信息，不创建或启动容器，也不拉取镜像，\n" +
			"因此通过校验不代表该 Docker Engine 一定能成功创建并运行 AgentFlow 所需的容器。\n" +
			"密码与私钥口令只在内存中传递，可用 --password-stdin 或 --passphrase-stdin 读取。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := withHostService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ExecutionHostService,
			) (struct{}, error) {
				return runHostValidate(ctx, a, service, args[0], flags)
			})
			return err
		},
	}
	addCredentialFlags(&flags, command)
	return command
}

// runHostValidate 执行一次同步校验并输出结果。
func runHostValidate(
	ctx context.Context,
	a *app.App,
	service *app.ExecutionHostService,
	hostID string,
	flags credentialFlags,
) (struct{}, error) {
	validated, validateErr := service.ValidateNow(ctx, hostID)
	if printErr := printValidationResult(a, validated); printErr != nil {
		return struct{}{}, printErr
	}
	if validateErr != nil {
		return struct{}{}, validateErr
	}
	if !validated.ValidationStatus.Usable() {
		return struct{}{}, fmt.Errorf(
			"执行节点 %s 环境预检未通过（状态 %s）：%s",
			validated.ID,
			validated.ValidationStatus,
			validated.ValidationMessage,
		)
	}
	return struct{}{}, nil
}

// printValidationResult 输出校验结果的可读摘要。
func printValidationResult(a *app.App, host domain.ExecutionHost) error {
	lines := []string{
		fmt.Sprintf("节点: %s (%s)", host.ID, host.Name),
		fmt.Sprintf("地址: %s", host.Address()),
		fmt.Sprintf("校验状态: %s", validationStatusLabel(host.ValidationStatus)),
		fmt.Sprintf("校验代次: %d", host.ValidationGeneration),
		fmt.Sprintf("摘要: %s", emptyAsUnknown(host.ValidationMessage)),
		fmt.Sprintf("开始时间: %s", formatOptionalTime(host.ValidationStartedAt)),
		fmt.Sprintf("完成时间: %s", formatOptionalTime(host.ValidationCompletedAt)),
	}
	if details := host.ValidationDetails; details != nil {
		lines = append(lines, fmt.Sprintf("校验器版本: %s", details.ValidatorVersion))
		for _, check := range details.Checks {
			lines = append(lines, fmt.Sprintf(
				"[%s] %s（%d ms）%s",
				checkStatusLabel(check.Status),
				check.Title,
				check.DurationMillis,
				optionalDetail(check.Message),
			))
			for _, image := range check.Images {
				lines = append(lines, fmt.Sprintf(
					"    镜像 %s → %s: %s%s",
					image.Reference,
					image.Resolved,
					checkStatusLabel(image.Status),
					optionalDetail(image.Message),
				))
			}
			for _, detail := range check.Details {
				lines = append(lines, "    "+detail)
			}
		}
		for _, capability := range details.Capabilities {
			lines = append(lines, "能力边界: "+capability)
		}
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// validationStatusLabel 返回校验状态的中文标签。
func validationStatusLabel(status domain.ValidationStatus) string {
	switch status {
	case domain.ValidationStatusChecking:
		return "校验中 (checking)"
	case domain.ValidationStatusNormal:
		return "正常 (normal)"
	case domain.ValidationStatusAbnormal:
		return "异常 (abnormal)"
	default:
		// 未知取值原样展示，不猜测语义。
		return status.String()
	}
}

// checkStatusLabel 返回单项检查状态的中文标签。
func checkStatusLabel(status domain.CheckStatus) string {
	switch status {
	case domain.CheckStatusPassed:
		return "通过"
	case domain.CheckStatusFailed:
		return "失败"
	case domain.CheckStatusTimedOut:
		return "超时"
	case domain.CheckStatusSkipped:
		return "已跳过"
	case domain.CheckStatusNotRun:
		return "未执行"
	default:
		return status.String()
	}
}

// optionalDetail 在说明非空时返回带分隔符的说明文本。
func optionalDetail(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return ""
	}
	return " — " + trimmed
}
