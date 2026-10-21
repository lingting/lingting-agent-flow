package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/spf13/cobra"
)

// newCodeCommand 构建 code 命令组。
func newCodeCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "code",
		Short: "在任务容器内保存代码：提交、推送与可选的主分支合并",
		Long: "所有 Git 操作都在指定的任务容器内部执行，不把容器代码复制到宿主机或本机。\n" +
			"任务分支统一为 agent/<task-id>；提交、推送与合并的结果分别持久化。\n" +
			"默认不合并基础分支：只有 --merge 或 --merge-after-push 显式授权时才尝试合并。\n" +
			"推送只针对任务分支，且从不使用强制推送。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newCodeInitCommand(a))
	command.AddCommand(newCodeSaveCommand(a))
	command.AddCommand(newCodePushCommand(a))
	command.AddCommand(newCodeMergeCommand(a))
	command.AddCommand(newCodeStatusCommand(a))
	command.PersistentFlags().String(
		"config",
		"",
		"配置文件路径（默认 "+config.DefaultPath+"）",
	)
	return command
}

// codeFlags 是 code 命令组共用的参数。
type codeFlags struct {
	credentialFlags
	// TaskID 是任务标识。
	TaskID string
	// BaseBranch 是显式指定的基础分支。
	BaseBranch string
	// WorkDir 是容器内 Git 工作区路径。
	WorkDir string
	// RemoteName 是 Git 远程名称。
	RemoteName string
	// CommitMessage 是提交信息。
	CommitMessage string
	// AuthorName 是提交作者姓名。
	AuthorName string
	// AuthorEmail 是提交作者邮箱。
	AuthorEmail string
	// Merge 表示在推送成功后尝试合并基础分支。
	Merge bool
	// AssumeYes 表示确认可能影响基础分支的操作。
	AssumeYes bool
}

// addCodeFlags 注册 code 命令组共用参数。
func addCodeFlags(flags *codeFlags, command *cobra.Command) {
	addCredentialFlags(&flags.credentialFlags, command)
	command.Flags().StringVar(&flags.TaskID, "task", "", "任务标识，任务分支为 agent/<task-id>（必填）")
	command.Flags().StringVar(
		&flags.BaseBranch,
		"base",
		"",
		"基础分支，省略时按 origin/HEAD、main、master 顺序识别",
	)
	command.Flags().StringVar(
		&flags.WorkDir,
		"workdir",
		"",
		"容器内 Git 工作区路径，省略时使用 "+domain.DefaultContainerWorkDir,
	)
	command.Flags().StringVar(&flags.RemoteName, "remote", "", "Git 远程名称，省略时使用 origin")
	command.Flags().StringVar(&flags.CommitMessage, "message", "", "提交信息，省略时使用默认信息")
	command.Flags().StringVar(&flags.AuthorName, "author-name", "", "提交作者姓名")
	command.Flags().StringVar(&flags.AuthorEmail, "author-email", "", "提交作者邮箱")
	if err := command.MarkFlagRequired("task"); err != nil {
		panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
	}
}

// author 返回命令行给出的提交身份。
func (f codeFlags) author() (domain.GitAuthor, error) {
	name := strings.TrimSpace(f.AuthorName)
	email := strings.TrimSpace(f.AuthorEmail)
	if name == "" && email == "" {
		return domain.GitAuthor{}, nil
	}
	author := domain.GitAuthor{Name: name, Email: email}
	if err := author.Validate(); err != nil {
		return domain.GitAuthor{}, err
	}
	return author, nil
}

// newCodeSaveCommand 构建 code save 子命令。
func newCodeSaveCommand(a *app.App) *cobra.Command {
	var flags codeFlags
	command := &cobra.Command{
		Use:   "save <container>",
		Short: "提交并推送任务分支，按配置决定是否合并基础分支",
		Long: "执行顺序：核验容器与工作区 → 校验任务分支 → 提交 → 推送任务分支 →（可选）合并。\n" +
			"工作区没有变更时记录 no_changes，不创建空提交。\n" +
			"推送结果不确定时先核验远程任务分支，不会盲目重试可能覆盖远程历史的命令。\n" +
			"合并默认关闭，--merge-after-push 显式开启；合并失败不会影响任务分支已推送成功的状态。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCodeSaveCommand(cmd, a, args[0], flags, codesaver.OperationSave)
		},
	}
	addCodeFlags(&flags, command)
	command.Flags().BoolVar(
		&flags.Merge,
		"merge-after-push",
		false,
		"推送成功后尝试合并基础分支（默认关闭）",
	)
	return command
}

// newCodePushCommand 构建 code push 子命令。
func newCodePushCommand(a *app.App) *cobra.Command {
	var flags codeFlags
	command := &cobra.Command{
		Use:   "push <container>",
		Short: "只推送任务分支，不隐式执行主分支合并",
		Long: "只推送 agent/<task-id> 分支到远程仓库，并核验远程指向的 commit。\n" +
			"该命令不会执行合并，也不会使用强制推送。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCodeSaveCommand(cmd, a, args[0], flags, codesaver.OperationPush)
		},
	}
	addCodeFlags(&flags, command)
	return command
}

// newCodeMergeCommand 构建 code merge 子命令。
func newCodeMergeCommand(a *app.App) *cobra.Command {
	var flags codeFlags
	command := &cobra.Command{
		Use:   "merge <container>",
		Short: "把已成功推送的任务分支合并到基础分支",
		Long: "只允许对已经确认推送成功的任务分支执行合并：本次调用会沿用该任务最近一次\n" +
			"保存记录的提交与推送结果，并重新核验远程状态。\n" +
			"合并使用 --no-ff 创建可审计的合并提交；出现冲突时中止合并并保留现场，\n" +
			"不会自动选择冲突内容，也不会强制推送覆盖主分支历史。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireMergeConfirmation(flags, args[0]); err != nil {
				return err
			}
			return runCodeSaveCommand(cmd, a, args[0], flags, codesaver.OperationMerge)
		},
	}
	addCodeFlags(&flags, command)
	command.Flags().BoolVar(&flags.AssumeYes, "yes", false, "确认合并基础分支（非交互执行必须显式给出）")
	return command
}

// requireMergeConfirmation 校验用户已经确认合并主分支。
//
// 合并会修改基础分支，因此非交互执行必须显式给出 --yes；缺少确认时直接拒绝，
// 不静默授权危险操作。
func requireMergeConfirmation(flags codeFlags, containerID string) error {
	if flags.AssumeYes {
		return nil
	}
	return fmt.Errorf(
		"合并会更新基础分支 %s；确认后请追加 --yes 重新执行（容器 %s）",
		emptyAsUnknown(flags.BaseBranch),
		containerID,
	)
}

// runCodeSaveCommand 加载配置并执行一次代码保存操作。
func runCodeSaveCommand(
	cmd *cobra.Command,
	a *app.App,
	containerID string,
	flags codeFlags,
	operation codesaver.Operation,
) error {
	author, err := flags.author()
	if err != nil {
		return err
	}
	return withContainerService(cmd.Context(), a, hostConfigPath(cmd), func(
		ctx context.Context,
		service *app.ContainerService,
	) error {
		result, saveErr := service.SaveCode(ctx, app.CodeSaveRequest{
			ContainerID:    containerID,
			TaskID:         flags.TaskID,
			WorkDir:        flags.WorkDir,
			BaseBranch:     flags.BaseBranch,
			MergeAfterPush: flags.Merge,
			CommitMessage:  flags.CommitMessage,
			Author:         author,
			RemoteName:     flags.RemoteName,
			Operation:      operation,
		})
		if printErr := printCodeSaveResult(a, result); printErr != nil {
			return printErr
		}
		return saveErr
	})
}

// newCodeStatusCommand 构建 code status 子命令。
func newCodeStatusCommand(a *app.App) *cobra.Command {
	var taskID string
	command := &cobra.Command{
		Use:   "status",
		Short: "展示任务的提交、推送与合并状态",
		Long: "读取持久化记录，分别展示提交、推送与合并的结果。该命令只读，\n" +
			"不会连接远程机器，也不会修改容器或仓库。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withContainerService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ContainerService,
			) error {
				records, statusErr := service.CodeSaveStatus(ctx, taskID)
				if printErr := printCodeSaveStatus(a, records); printErr != nil {
					return printErr
				}
				return statusErr
			})
		},
	}
	command.Flags().StringVar(&taskID, "task", "", "任务标识（必填）")
	if err := command.MarkFlagRequired("task"); err != nil {
		panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
	}
	return command
}

// printCodeSaveResult 输出一次代码保存的结果。
func printCodeSaveResult(a *app.App, result app.CodeSaveResult) error {
	record := result.Result.Record
	if record.ID.IsEmpty() {
		return nil
	}
	lines := []string{
		fmt.Sprintf("任务: %s", record.TaskID),
		fmt.Sprintf("容器: %s", record.ContainerID),
		fmt.Sprintf("仓库: %s（远程 %s）", emptyAsUnknown(record.Repository), record.RemoteName),
		fmt.Sprintf("任务分支: %s", record.TaskBranch),
		fmt.Sprintf("基础分支: %s", emptyAsUnknown(record.BaseBranch)),
		fmt.Sprintf("提交状态: %s", record.CommitStatus),
		fmt.Sprintf("提交 commit: %s", emptyAsUnknown(record.CommitSHA)),
		fmt.Sprintf("推送状态: %s", record.PushStatus),
		fmt.Sprintf("远程 commit: %s", emptyAsUnknown(record.RemoteCommitSHA)),
		fmt.Sprintf("合并状态: %s", record.MergeStatus),
		fmt.Sprintf("合并 commit: %s", emptyAsUnknown(record.MergeSHA)),
	}
	if record.Stage != "" {
		lines = append(lines, fmt.Sprintf("最后阶段: %s", record.Stage))
	}
	if record.ErrorCode != "" {
		lines = append(lines, fmt.Sprintf("失败分类: %s", record.ErrorCode))
	}
	if record.ErrorSummary != "" {
		lines = append(lines, fmt.Sprintf("失败摘要: %s", record.ErrorSummary))
	}
	if hint := codeSaveHint(record); hint != "" {
		lines = append(lines, "建议: "+hint)
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// printCodeSaveStatus 输出任务的代码保存记录列表。
func printCodeSaveStatus(a *app.App, records []domain.CodeSaveRecord) error {
	if len(records) == 0 {
		_, err := fmt.Fprintln(a.Stdout(), "没有该任务的代码保存记录")
		return err
	}
	blocks := make([]string, 0, len(records))
	for _, record := range records {
		lines := []string{
			fmt.Sprintf("记录: %s", record.ID),
			fmt.Sprintf("任务分支: %s", record.TaskBranch),
			fmt.Sprintf("提交: %s %s", record.CommitStatus, emptyAsUnknown(record.CommitSHA)),
			fmt.Sprintf("推送: %s %s", record.PushStatus, emptyAsUnknown(record.RemoteCommitSHA)),
			fmt.Sprintf("合并: %s %s", record.MergeStatus, emptyAsUnknown(record.MergeSHA)),
		}
		if record.ErrorSummary != "" {
			lines = append(lines, fmt.Sprintf("失败摘要: %s", record.ErrorSummary))
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(blocks, "\n\n"))
	return err
}

// codeSaveHint 根据记录状态给出可诊断的下一步建议。
func codeSaveHint(record domain.CodeSaveRecord) string {
	switch {
	case record.PushStatus == domain.PushStatusUnknown:
		return "先核验远程任务分支实际指向的 commit，再决定是否重试推送"
	case record.PushStatus == domain.PushStatusConflict:
		return "远程任务分支与本地历史不一致，需要人工处理；不要强制推送"
	case record.PushStatus == domain.PushStatusFailed:
		return "检查仓库认证与网络后重新执行 code push；容器内提交仍然保留"
	case record.MergeStatus == domain.MergeStatusConflict:
		return "任务分支已成功推送；请在容器内人工解决合并冲突后重新执行 code merge"
	case record.MergeStatus == domain.MergeStatusUnknown:
		return "先核验远程基础分支的 commit，再决定是否需要人工处理"
	case record.MergeStatus == domain.MergeStatusFailed:
		return "任务分支已成功推送；检查基础分支状态后重新执行 code merge"
	case record.CommitStatus == domain.CommitStatusNoChanges:
		return "工作区没有变更，无需提交"
	default:
		return ""
	}
}
