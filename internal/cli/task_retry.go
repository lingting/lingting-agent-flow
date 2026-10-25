package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/spf13/cobra"
)

// newTaskRetryCommand 构建 task retry 子命令。
//
// 它是失败任务唯一的重新执行入口：调度器只领取 queued，启动接口明确排除 failed，
// 因此"失败后重新执行"必须由用户显式发起。
func newTaskRetryCommand(a *app.App) *cobra.Command {
	var (
		address       string
		prompt        string
		timeout       string
		verifyTimeout string
		planTimeout   string
		wait          bool
		interval      time.Duration
	)
	command := &cobra.Command{
		Use:   "retry <task-id>",
		Short: "显式重试失败任务，开启一次新的执行周期",
		Long: "向本地 API 发起 POST /api/v1/tasks/{taskID}/retry，由服务端按持久化事实判定：\n" +
			"  1. 只有 failed 任务允许重试，且必须没有取消请求；\n" +
			"  2. 任务租约仍有效、仍有未结束执行记录、仍有被占用步骤或结果不确定的步骤时拒绝；\n" +
			"  3. 仍有未核验的外部副作用（commit / push / merge）时拒绝，必须先执行 code verify；\n" +
			"  4. 通过判定后先领取任务执行权，再把任务重开为 preparing 并交接给统一启动流程。\n" +
			"重试不覆盖历史：旧 Attempt、Session、WorkflowRun 与代码保存记录全部保留，\n" +
			"新执行周期由新的领取代次与新的执行记录表达。\n" +
			"该命令不会自动重试：判定拒绝时如实返回原因，由用户处理现场后再次发起。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.ValidateConfig(cmd.Context(), hostConfigPath(cmd))
			if err != nil {
				return err
			}
			target := strings.TrimSpace(address)
			if target == "" {
				target = cfg.Server.Address()
			}
			devTimeout, err := parseOptionalDuration(timeout)
			if err != nil {
				return err
			}
			validationTimeout, err := parseOptionalDuration(verifyTimeout)
			if err != nil {
				return err
			}
			planCallTimeout, err := parseOptionalDuration(planTimeout)
			if err != nil {
				return err
			}
			if interval <= 0 {
				return fmt.Errorf("轮询间隔必须大于 0: %s", interval)
			}
			client := newWorkflowAPIClient(target, a.Logger().Slog())
			ctx := cmd.Context()
			result, err := client.retry(ctx, args[0], workflowAPIRetryRequest{
				Prompt:                   strings.TrimSpace(prompt),
				TimeoutSeconds:           int(devTimeout.Seconds()),
				ValidationTimeoutSeconds: int(validationTimeout.Seconds()),
				PlanTimeoutSeconds:       int(planCallTimeout.Seconds()),
			})
			if err != nil {
				return err
			}
			if err := printRetryResult(a, result); err != nil {
				return err
			}
			if !wait || result.Run == nil || workflowRunStopped(*result.Run) {
				return nil
			}
			final, waitErr := client.wait(ctx, args[0], interval, a)
			if final.RunID == "" {
				return waitErr
			}
			if printErr := printWorkflowRun(a, final); printErr != nil {
				return printErr
			}
			return waitErr
		},
	}
	command.Flags().StringVar(&address, "address", "", "服务地址，省略时使用配置文件的 server 段")
	command.Flags().StringVar(&prompt, "prompt", "", "追加在每一步骤提示词之后的补充说明")
	command.Flags().StringVar(&timeout, "timeout", "", "单个步骤开发阶段的超时时间，例如 30m")
	command.Flags().StringVar(&verifyTimeout, "verify-timeout", "", "单个步骤验证阶段的超时时间，例如 10m")
	command.Flags().StringVar(&planTimeout, "plan-timeout", "", "本次规划调用的超时时间，例如 10m")
	command.Flags().BoolVar(&wait, "wait", true, "轮询真实进度直到执行收敛")
	command.Flags().DurationVar(&interval, "interval", 2*time.Second, "轮询间隔")
	return command
}

// newTaskRetryStatusCommand 构建 task retry-status 子命令。
//
// 它是只读查询：让用户在发起重试之前看到全部阻塞项，而不是先收到一个 409。
func newTaskRetryStatusCommand(a *app.App) *cobra.Command {
	var address string
	command := &cobra.Command{
		Use:   "retry-status <task-id>",
		Short: "查询任务能否被显式重试，以及不能重试时的阻塞项",
		Long: "向本地 API 发起 GET /api/v1/tasks/{taskID}/retry-eligibility。\n" +
			"该命令只读：不领取执行权、不修改任何状态，因此可以随时查询。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.ValidateConfig(cmd.Context(), hostConfigPath(cmd))
			if err != nil {
				return err
			}
			target := strings.TrimSpace(address)
			if target == "" {
				target = cfg.Server.Address()
			}
			client := newWorkflowAPIClient(target, a.Logger().Slog())
			result, err := client.retryEligibility(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printRetryEligibility(a, result)
		},
	}
	command.Flags().StringVar(&address, "address", "", "服务地址，省略时使用配置文件的 server 段")
	return command
}
