package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/app"
	"github.com/spf13/cobra"
)

// newCodeInitCommand 构建 code init 子命令。
//
// 它挂在 code 命令组下：代码初始化与代码保存都属于"任务容器内的代码生命周期"，
// 但两者职责不同——初始化只负责把代码放进容器并准备任务分支。
func newCodeInitCommand(a *app.App) *cobra.Command {
	var taskID string
	var stepID string
	var attemptID string
	var workDir string
	var timeout string
	command := &cobra.Command{
		Use:   "init",
		Short: "在任务容器内获取代码并创建 agent/<task-id> 分支",
		Long: "代码通过容器内 Git Clone 直接获取到任务容器的工作目录，不经过本机数据目录，\n" +
			"也不使用宿主机 bind mount。任务分支统一为 agent/<task-id>。\n" +
			"同一尝试已经初始化成功时不会重新 Clone，只记录 skipped，避免覆盖已有开发成果。\n" +
			"目标目录非空时直接失败，不会为了重试自动删除目录内容。\n" +
			"需要认证的私有仓库会返回明确错误：第一版不提供凭据存储。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			parsedTimeout, err := parseOptionalDuration(timeout)
			if err != nil {
				return err
			}
			return withDockerServices(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				services dockerServices,
			) error {
				initializer, initErr := app.NewCodeInitializeService(app.CodeInitializeOptions{
					Store:      services.store,
					Containers: services.containers,
					Logger:     a.Logger().Slog(),
				})
				if initErr != nil {
					return initErr
				}
				result, runErr := initializer.Initialize(ctx, app.CodeInitRequest{
					TaskID:    taskID,
					StepID:    stepID,
					AttemptID: attemptID,
					WorkDir:   workDir,
					Timeout:   parsedTimeout,
				})
				if printErr := printCodeInitResult(a, result); printErr != nil {
					return printErr
				}
				return runErr
			})
		},
	}
	command.Flags().StringVar(&taskID, "task", "", "任务标识（必填）")
	command.Flags().StringVar(&stepID, "step", "", "发起初始化的步骤标识（必填）")
	command.Flags().StringVar(&attemptID, "attempt", "", "本次初始化所属的执行尝试标识（必填）")
	command.Flags().StringVar(
		&workDir,
		"workdir",
		"",
		"容器内目标目录，省略时使用任务配置的目录",
	)
	command.Flags().StringVar(&timeout, "timeout", "", "本次代码初始化的超时时间，例如 10m")
	for _, required := range []string{"task", "step", "attempt"} {
		if err := command.MarkFlagRequired(required); err != nil {
			panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
		}
	}
	return command
}

// printCodeInitResult 输出代码初始化结果。
func printCodeInitResult(a *app.App, result app.CodeInitResult) error {
	if result.RecordID.IsEmpty() {
		return nil
	}
	lines := []string{
		fmt.Sprintf("记录: %s", result.RecordID),
		fmt.Sprintf("状态: %s（阶段 %s）", result.Status, emptyAsUnknown(result.Stage.String())),
		fmt.Sprintf("代码就绪: %s", flagMark(result.Succeeded)),
		fmt.Sprintf("仓库: %s", emptyAsUnknown(result.Record.Repository)),
		fmt.Sprintf("容器: %s", emptyAsUnknown(result.ContainerID)),
		fmt.Sprintf("容器内目录: %s", emptyAsUnknown(result.WorkDir)),
		fmt.Sprintf("基础分支: %s", emptyAsUnknown(result.BaseBranch)),
		fmt.Sprintf("任务分支: %s", emptyAsUnknown(result.TaskBranch)),
		fmt.Sprintf("当前 commit: %s", emptyAsUnknown(result.Commit)),
		fmt.Sprintf("记录已写入: %s", flagMark(result.Persisted)),
	}
	if len(result.PersistenceErrors) > 0 {
		lines = append(lines, "写入失败: "+strings.Join(result.PersistenceErrors, "；"))
	}
	if failure := strings.TrimSpace(result.Failure); failure != "" {
		lines = append(lines, "失败原因: "+failure)
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}
