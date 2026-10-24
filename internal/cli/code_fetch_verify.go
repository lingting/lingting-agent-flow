package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/app"
	"github.com/spf13/cobra"
)

// newCodeFetchVerifyCommand 构建 code fetch-verify 子命令。
//
// 它对应"尝试级代码获取结果不确定"这一现场：核验只做只读查询（目录是否存在、
// 是否为 Git 仓库、当前分支与 HEAD），事实充分时才把记录收敛为 succeeded/failed，
// 否则保持 uncertain 并把原因写回记录。
func newCodeFetchVerifyCommand(a *app.App) *cobra.Command {
	var recordID string
	command := &cobra.Command{
		Use:   "fetch-verify",
		Short: "只读核验一条结果不确定的代码获取记录",
		Long: "读取已持久化的代码获取记录，并在任务容器内执行只读查询，判断代码是否已经就位。\n" +
			"该命令不 Clone、不 checkout、不 reset，也不会删除容器或工作区。\n" +
			"目录不存在时收敛为 failed；目录存在且位于任务分支、HEAD 可读时收敛为 succeeded；\n" +
			"其余情况保持 uncertain 并写回原因，等待人工判断。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withDockerServices(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				services dockerServices,
			) error {
				return runCodeFetchVerify(ctx, a, services, recordID)
			})
		},
	}
	command.Flags().StringVar(&recordID, "record", "", "代码获取记录标识（必填）")
	if err := command.MarkFlagRequired("record"); err != nil {
		panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
	}
	return command
}

// runCodeFetchVerify 构造核验服务并输出结论。
func runCodeFetchVerify(
	ctx context.Context,
	a *app.App,
	services dockerServices,
	recordID string,
) error {
	verifier, err := app.NewCodeFetchVerifyService(
		services.store,
		services.containers,
		a.Logger().Slog(),
	)
	if err != nil {
		return err
	}
	result, verifyErr := verifier.Verify(ctx, app.CodeFetchVerifyRequest{RecordID: recordID})
	if printErr := printCodeFetchVerifyResult(a, result); printErr != nil {
		return printErr
	}
	return verifyErr
}

// printCodeFetchVerifyResult 输出一次只读核验的结论。
func printCodeFetchVerifyResult(a *app.App, result app.CodeFetchVerifyResult) error {
	if result.Record.ID.IsEmpty() {
		return nil
	}
	lines := []string{
		"记录: " + result.Record.ID.String(),
		"任务分支: " + result.Record.TaskBranch,
		"核验前状态: " + result.Record.Status.String() + "（阶段 " + result.Record.Stage.String() + "）",
	}
	if result.Observation.ReadFailed {
		lines = append(lines, "核验状态: 无法核验")
		if detail := strings.TrimSpace(result.Observation.ReadError); detail != "" {
			lines = append(lines, "核验失败原因: "+detail)
		}
	}
	if result.Observation.WorkDirExists {
		lines = append(lines, "工作目录: 存在")
		lines = append(lines, fmt.Sprintf("是否 Git 仓库: %t", result.Observation.IsGitRepository))
		if result.Observation.Branch != "" {
			lines = append(lines, "当前分支: "+result.Observation.Branch)
		}
		if result.Observation.Head != "" {
			lines = append(lines, "当前 HEAD: "+result.Observation.Head)
		}
	} else if !result.Observation.ReadFailed {
		lines = append(lines, "工作目录: 不存在")
	}
	lines = append(lines, "核验结论: "+result.Decision.Summary)
	if result.Decision.Converged {
		lines = append(lines, "收敛结果: "+result.Decision.Status.String())
	} else {
		lines = append(lines, "收敛结果: 保持 uncertain，需要人工判断")
	}
	if result.Persisted {
		lines = append(lines, "结论已写回记录")
	} else {
		lines = append(lines, "结论未写回记录（写入失败或并发冲突），请重新核验")
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}
