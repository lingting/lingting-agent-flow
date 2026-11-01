package cigate

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// 命令与退出码约定。
const (
	// CommandRealTestsGate 是判断真实测试是否获授权并写出 CI 输出的子命令。
	CommandRealTestsGate = "real-tests-gate"
	// exitOK 表示判定正常完成：授权与拒绝都属于正常结论。
	exitOK = 0
	// exitFailed 表示写出输出或摘要失败。
	exitFailed = 1
	// exitUsage 表示命令行参数错误。
	exitUsage = 2
)

// 判定过程使用的事件上下文环境变量名。
const (
	// EventNameEnv 是 GitHub Actions 提供的事件名。
	EventNameEnv = "GITHUB_EVENT_NAME"
	// ActorEnv 是触发本次运行的 GitHub 账号。
	ActorEnv = "GITHUB_ACTOR"
	// RepositoryOwnerEnv 是仓库所有者账号。
	RepositoryOwnerEnv = "GITHUB_REPOSITORY_OWNER"
	// RepositoryEnv 是 owner/repo 形式的仓库全名。
	RepositoryEnv = "GITHUB_REPOSITORY"
	// APIURLEnv 是 GitHub API 根地址。
	APIURLEnv = "GITHUB_API_URL"
	// GitHubOutputEnv 是 GitHub Actions 的输出文件路径。
	GitHubOutputEnv = "GITHUB_OUTPUT"
	// StepSummaryEnv 是 GitHub Actions 的摘要文件路径。
	StepSummaryEnv = "GITHUB_STEP_SUMMARY"
	// GitHubTokenEnv 是仓库只读令牌，仅用于查询推送者的仓库权限。
	GitHubTokenEnv = "GITHUB_TOKEN"
	// GitHubFallbackTokenEnv 是备用令牌环境变量名。
	GitHubFallbackTokenEnv = "GH_TOKEN"
	// defaultAPIURL 是 GitHub.com 的 API 根地址。
	defaultAPIURL = "https://api.github.com"
)

// permissionQueryTimeout 是单次仓库权限查询的超时上限。
const permissionQueryTimeout = 60 * time.Second

// Run 执行 agentflow-ci 命令并返回进程退出码。
//
// getenv 由调用方注入，便于在测试中替换环境；判定结论为拒绝时退出码仍然是 0：
// "没有授权"是正常结论，不是命令执行失败。
func Run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if len(args) == 0 {
		printUsage(stderr)
		return exitUsage
	}
	command, rest := args[0], args[1:]
	switch command {
	case CommandRealTestsGate:
		code, err := runRealTestsGate(rest, getenv, stdout, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "错误: %v\n", err)
		}
		return code
	default:
		fmt.Fprintf(stderr, "错误: 未知子命令 %q\n", command)
		printUsage(stderr)
		return exitUsage
	}
}

// printUsage 输出命令用法。
func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "用法: agentflow-ci real-tests-gate [参数]")
	fmt.Fprintln(writer, "  判定本次 GitHub Actions 运行是否获准执行真实外部依赖测试。")
	fmt.Fprintln(writer, "  参数默认取值来自 GitHub Actions 注入的环境变量。")
}

// gateOptions 是 real-tests-gate 的命令行参数。
type gateOptions struct {
	mode              string
	event             string
	actor             string
	repositoryOwner   string
	pullRequestAuthor string
	authorAssociation string
	repository        string
	apiURL            string
	githubOutput      string
	stepSummary       string
}

// runRealTestsGate 执行授权判定并写出 CI 输出。
func runRealTestsGate(
	args []string,
	getenv func(string) string,
	stdout io.Writer,
	stderr io.Writer,
) (int, error) {
	options, err := parseGateOptions(args, getenv, stderr)
	if err != nil {
		return exitUsage, err
	}
	input := options.input()
	decision := Decide(context.Background(), input, options.checker(getenv))
	fmt.Fprintf(
		stdout,
		"真实测试授权: %s (mode=%s, event=%s)\n",
		boolText(decision.Enabled),
		decision.Mode.Display(),
		tableText(input.Event),
	)
	fmt.Fprintf(stdout, "判定原因: %s\n", decision.Reason)
	if err := writeOutputs(options.githubOutput, decision); err != nil {
		return exitFailed, err
	}
	if err := writeSummary(options.stepSummary, decision.Summary(input)); err != nil {
		return exitFailed, err
	}
	return exitOK, nil
}

// parseGateOptions 解析命令行参数，未显式给出的参数回落到环境变量。
func parseGateOptions(
	args []string,
	getenv func(string) string,
	stderr io.Writer,
) (gateOptions, error) {
	options := gateOptions{}
	flags := flag.NewFlagSet(CommandRealTestsGate, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&options.mode, "mode", getenv(ModeVariable),
		"AGENTFLOW_REAL_TESTS 的取值（self / collaborator / all）")
	flags.StringVar(&options.event, "event", getenv(EventNameEnv), "触发事件名")
	flags.StringVar(&options.actor, "actor", getenv(ActorEnv), "触发本次运行的账号")
	flags.StringVar(&options.repositoryOwner, "repository-owner", getenv(RepositoryOwnerEnv),
		"仓库所有者账号")
	flags.StringVar(&options.pullRequestAuthor, "pr-author", "", "PR 作者账号（仅报告用）")
	flags.StringVar(&options.authorAssociation, "author-association", "",
		"PR 作者的 author_association")
	flags.StringVar(&options.repository, "repository", getenv(RepositoryEnv),
		"owner/repo 形式的仓库全名")
	apiURL := getenv(APIURLEnv)
	if strings.TrimSpace(apiURL) == "" {
		apiURL = defaultAPIURL
	}
	flags.StringVar(&options.apiURL, "api-url", apiURL, "GitHub API 根地址")
	flags.StringVar(&options.githubOutput, "github-output", getenv(GitHubOutputEnv),
		"GitHub Actions 输出文件路径，为空时不写文件")
	flags.StringVar(&options.stepSummary, "step-summary", getenv(StepSummaryEnv),
		"GitHub Actions 摘要文件路径，为空时不写文件")
	if err := flags.Parse(args); err != nil {
		return gateOptions{}, err
	}
	if flags.NArg() > 0 {
		return gateOptions{}, fmt.Errorf("存在多余参数: %s", strings.Join(flags.Args(), " "))
	}
	return options, nil
}

// input 把命令行参数转换为判定输入。
func (o gateOptions) input() Input {
	return Input{
		Mode:              o.mode,
		Event:             o.event,
		Actor:             o.actor,
		RepositoryOwner:   o.repositoryOwner,
		PullRequestAuthor: o.pullRequestAuthor,
		AuthorAssociation: o.authorAssociation,
	}
}

// checker 返回本次判定需要的权限检查器。
//
// 只有 push 事件的 collaborator 模式需要查询仓库权限，其他情况返回 nil：
// 不产生任何网络访问。
func (o gateOptions) checker(getenv func(string) string) PermissionChecker {
	if ParseMode(o.mode) != ModeCollaborator || ParseEvent(o.event) != EventPush {
		return nil
	}
	token := strings.TrimSpace(getenv(GitHubTokenEnv))
	if token == "" {
		token = strings.TrimSpace(getenv(GitHubFallbackTokenEnv))
	}
	return &timedPermissionChecker{
		inner: GitHubPermissionChecker{
			APIURL:     o.apiURL,
			Repository: o.repository,
			Token:      token,
		},
		timeout: permissionQueryTimeout,
	}
}

// timedPermissionChecker 给权限查询加上固定的超时上限。
type timedPermissionChecker struct {
	inner   GitHubPermissionChecker
	timeout time.Duration
}

// HasWritePermission 在超时时间内查询仓库写入权限。
func (c *timedPermissionChecker) HasWritePermission(
	ctx context.Context,
	login string,
) (bool, error) {
	timeout := c.timeout
	if timeout <= 0 {
		timeout = permissionQueryTimeout
	}
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.inner.HasWritePermission(queryCtx, login)
}

// writeOutputs 以 key=value 形式追加写入 GitHub Actions 输出文件。
func writeOutputs(path string, decision Decision) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	var builder strings.Builder
	for _, entry := range decision.Outputs() {
		fmt.Fprintf(&builder, "%s=%s\n", entry[0], entry[1])
	}
	return appendFile(path, builder.String())
}

// writeSummary 追加写入 GitHub Actions 摘要文件。
func writeSummary(path, summary string) error {
	if strings.TrimSpace(path) == "" || summary == "" {
		return nil
	}
	return appendFile(path, summary)
}

// appendFile 以追加方式写入文件，不改变原有内容。
func appendFile(path, content string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开输出文件失败: %w", err)
	}
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("写入输出文件失败: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭输出文件失败: %w", err)
	}
	return nil
}
