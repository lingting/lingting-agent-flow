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
	command.AddCommand(newCodeVerifyCommand(a))
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

// newCodeVerifyCommand 构建 code verify 子命令。
func newCodeVerifyCommand(a *app.App) *cobra.Command {
	var (
		recordID    string
		containerID string
		workDir     string
	)
	command := &cobra.Command{
		Use:   "verify",
		Short: "只读核验一次代码保存记录对应的真实仓库状态",
		Long: "读取已持久化的代码保存记录，并在容器内执行只读 Git 查询，判断提交、推送与\n" +
			"合并是否真的已经发生。\n" +
			"该命令不提交、不推送、不合并，也不会删除或接管容器。核验确认某个 Git 操作已经\n" +
			"发生时，结论连同该阶段状态一起写回记录；确认未发生或无法确认时只写恢复结论。\n" +
			"记录仍是 pending 时，禁止仅凭记录重新执行有副作用的 Git 操作。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withContainerService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ContainerService,
			) error {
				result, verifyErr := service.VerifyCodeSave(ctx, app.CodeSaveVerifyRequest{
					RecordID:    recordID,
					ContainerID: containerID,
					WorkDir:     workDir,
				})
				if printErr := printCodeSaveVerifyResult(a, result); printErr != nil {
					return printErr
				}
				return verifyErr
			})
		},
	}
	command.Flags().StringVar(&recordID, "record", "", "代码保存记录标识（必填）")
	command.Flags().StringVar(&containerID, "container", "", "覆盖记录中的容器登记记录标识")
	command.Flags().StringVar(
		&workDir,
		"workdir",
		"",
		"容器内 Git 工作区路径，省略时使用 "+domain.DefaultContainerWorkDir,
	)
	if err := command.MarkFlagRequired("record"); err != nil {
		panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
	}
	return command
}

// printCodeSaveVerifyResult 输出一次只读核验的结论。
func printCodeSaveVerifyResult(a *app.App, result app.CodeSaveVerifyResult) error {
	if result.Record.ID.IsEmpty() {
		return nil
	}
	lines := []string{
		fmt.Sprintf("记录: %s", result.Record.ID),
		fmt.Sprintf("任务分支: %s", result.Record.TaskBranch),
		fmt.Sprintf("核验前提交: %s %s", result.Record.CommitStatus, emptyAsUnknown(result.Record.CommitSHA)),
		fmt.Sprintf("核验前推送: %s %s", result.Record.PushStatus, emptyAsUnknown(result.Record.RemoteCommitSHA)),
		fmt.Sprintf("核验前合并: %s %s", result.Record.MergeStatus, emptyAsUnknown(result.Record.MergeSHA)),
	}
	if result.Observation.ReadFailed {
		lines = append(lines, "核验状态: 无法核验")
		if detail := strings.TrimSpace(result.Observation.ReadError); detail != "" {
			// 失败原因必须显式打印：否则用户只能得知"无法核验"，无法区分节点不可达、
			// Docker 不可访问与 Git 查询失败。
			lines = append(lines, "核验失败原因: "+detail)
		}
	}
	if result.Observation.HeadCommit != "" {
		lines = append(lines, fmt.Sprintf("当前 HEAD: %s", result.Observation.HeadCommit))
	}
	if result.Observation.RemoteTaskCommit != "" {
		lines = append(lines, fmt.Sprintf("远程任务分支: %s", result.Observation.RemoteTaskCommit))
	}
	if result.Observation.RemoteBaseCommit != "" {
		lines = append(lines, fmt.Sprintf("远程基础分支: %s", result.Observation.RemoteBaseCommit))
	}
	if result.Observation.ExpectedMergeCommit != "" {
		lines = append(lines, fmt.Sprintf("预期合并提交: %s", result.Observation.ExpectedMergeCommit))
	}
	lines = append(lines,
		fmt.Sprintf("恢复结论: %s", result.Decision.Status),
		"判定说明: "+result.Decision.Summary,
	)
	if result.Decision.SafeToRetry {
		lines = append(lines, "后续操作: 可以安全地重新执行对应操作（执行前仍会重新核验前置条件）")
	} else {
		lines = append(lines, "后续操作: 不要自动重试，先按判定说明人工核验")
	}
	// 四个事实必须分别输出：判定是否可靠、结论是否落盘、记录是否已推进、是否需要
	// 用户介入。把它们合并成一句话会让"核验成功"掩盖"结论没写进数据库"。
	if result.Persisted {
		lines = append(lines, "结论已写回记录")
	} else {
		lines = append(lines, "结论未写回记录（写入失败或并发冲突），请保存本次输出并重新核验")
	}
	if result.Persisted && result.Updated.RecoveryConverged() {
		lines = append(lines, "记录状态: 恢复已收敛，可按后续操作提示处理")
	} else {
		lines = append(lines, "记录状态: 仍需人工处理，任务不会自动继续推进")
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
			fmt.Sprintf("记录写入: %s", record.PersistStatus.Normalized()),
		}
		if record.BaselineCommitSHA != "" {
			lines = append(lines, fmt.Sprintf("提交基线: %s", record.BaselineCommitSHA))
		}
		if record.BaselineBaseCommitSHA != "" {
			lines = append(lines, fmt.Sprintf("合并基线: %s", record.BaselineBaseCommitSHA))
		}
		if record.RecoveryStatus != "" {
			lines = append(lines, fmt.Sprintf("恢复结论: %s", record.RecoveryStatus))
		}
		if record.ErrorSummary != "" {
			lines = append(lines, fmt.Sprintf("失败摘要: %s", record.ErrorSummary))
		}
		if record.PersistError != "" {
			lines = append(lines, fmt.Sprintf("写入失败: %s", record.PersistError))
		}
		// 续跑资格必须显式列出：仅凭状态字段用户无法判断"能不能继续推进"。
		if resume := domain.DecideCodeSaveResume(record); resume.Action != domain.CodeSaveResumeProceed {
			lines = append(lines, "续跑资格: "+string(resume.Action)+"（"+resume.Summary+"）")
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(blocks, "\n\n"))
	return err
}

// codeSaveHint 根据记录状态给出可诊断的下一步建议。
func codeSaveHint(record domain.CodeSaveRecord) string {
	switch {
	case record.PersistStatus.IsUnconfirmed():
		return "记录可能落后于真实 Git 状态；先执行 code verify 核验，不要直接重试"
	case record.RecoveryStatus == domain.RecoveryStatusUnverifiable:
		return "上次核验无法确认仓库状态；请检查容器与远程可达性后重新执行 code verify"
	case record.RecoveryStatus == domain.RecoveryStatusConfirmedNotApplied:
		return "已核验确认对应操作没有发生；可以重新执行，执行前会自动重新核验前置条件"
	case record.PushStatus == domain.PushStatusPending:
		return "推送结果尚未确认；先执行 code verify 核验远程任务分支，不要直接重试"
	case record.PushStatus == domain.PushStatusUnknown:
		return "先核验远程任务分支实际指向的 commit，再决定是否重试推送"
	case record.PushStatus == domain.PushStatusConflict:
		return "远程任务分支与本地历史不一致，需要人工处理；不要强制推送"
	case record.PushStatus == domain.PushStatusFailed:
		return "检查仓库认证与网络后重新执行 code push；容器内提交仍然保留"
	case record.CommitStatus == domain.CommitStatusPending:
		return "提交结果尚未确认；先执行 code verify 核验任务分支 HEAD"
	case record.CommitStatus == domain.CommitStatusUnknown:
		return "提交结果无法确认；先执行 code verify 核验任务分支 HEAD，不要直接重新提交"
	case record.MergeStatus == domain.MergeStatusPending:
		return "合并结果尚未确认；先执行 code verify 核验远程基础分支"
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
