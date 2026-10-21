package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/execenv"
	"github.com/spf13/cobra"
)

// envExecFlags 是执行环境命令共用的参数。
type envExecFlags struct {
	// Machine 是目标执行节点。
	Machine string
	// Type 是环境类型。
	Type string
	// Name 是语言环境名称。
	Name string
	// Version 是环境版本号。
	Version int
	// Project 是项目标识。
	Project string
	// Caches 是允许挂载的缓存类型。
	Caches []string
	// WorkDir 是容器内项目目录。
	WorkDir string
	// SessionDir 是容器内 Session 目录。
	SessionDir string
}

// resolve 把命令行参数解析为准备请求。
func (f envExecFlags) resolve() (execenv.PrepareSpec, error) {
	environmentType, err := domain.ParseEnvironmentType(f.Type)
	if err != nil {
		return execenv.PrepareSpec{}, err
	}
	ref, err := domain.NewEnvironmentRef(environmentType, f.Name)
	if err != nil {
		return execenv.PrepareSpec{}, err
	}
	return execenv.PrepareSpec{
		HostID:        strings.TrimSpace(f.Machine),
		Environment:   ref,
		VersionNumber: f.Version,
		ProjectID:     strings.TrimSpace(f.Project),
		Caches:        f.Caches,
		WorkDir:       strings.TrimSpace(f.WorkDir),
		SessionDir:    strings.TrimSpace(f.SessionDir),
	}, nil
}

// addEnvExecFlags 注册执行环境命令的公共参数。
func addEnvExecFlags(command *cobra.Command, flags *envExecFlags) {
	command.Flags().StringVar(&flags.Machine, "machine", "", "目标执行节点标识（必填）")
	command.Flags().StringVar(
		&flags.Type,
		"type",
		string(domain.EnvironmentTypeLanguage),
		"环境类型，执行环境必须使用 language",
	)
	command.Flags().StringVar(&flags.Name, "name", "", "语言环境名称（必填）")
	command.Flags().IntVar(&flags.Version, "version", 0, "环境版本号，省略时使用当前可用版本")
	command.Flags().StringVar(&flags.Project, "project", "", "项目标识，用于项目级缓存共享范围")
	command.Flags().StringSliceVar(&flags.Caches, "cache", nil, "允许挂载的缓存类型，可重复指定")
	command.Flags().StringVar(
		&flags.WorkDir,
		"project-dir",
		"",
		"容器内项目目录，省略时使用 "+execenv.DefaultProjectDir,
	)
	command.Flags().StringVar(
		&flags.SessionDir,
		"session-dir",
		"",
		"容器内 Session 目录，省略时使用 "+execenv.DefaultSessionDir,
	)
	for _, name := range []string{"machine", "name"} {
		if err := command.MarkFlagRequired(name); err != nil {
			panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
		}
	}
}

// newExecEnvCommand 构建执行环境命令组。
//
// 该命令组是执行环境契约的可操作入口：它只做协议转换，不实现工作流与重试逻辑。
func newExecEnvCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "execenv",
		Short: "管理统一执行环境（远程 Linux + Docker 容器）",
		Long: "执行环境把「准备工作区、执行命令、释放资源」统一到一套契约下：\n" +
			"容器内项目代码目录固定为 " + execenv.DefaultProjectDir + "，\n" +
			"远程管理工作区只存放项目配置、Dockerfile 与镜像构建资源。\n" +
			"当前只实现远程 Linux + Docker 容器执行环境；其他执行环境会明确报错，\n" +
			"不会静默回退到本机执行。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newExecEnvPrepareCommand(a))
	command.AddCommand(newExecEnvPrepareTaskCommand(a))
	command.AddCommand(newExecEnvExecCommand(a))
	command.AddCommand(newExecEnvReleaseCommand(a))
	command.PersistentFlags().String(
		"config",
		"",
		"配置文件路径（默认 "+config.DefaultPath+"）",
	)
	return command
}

// newExecEnvPrepareCommand 构建 execenv prepare 子命令。
func newExecEnvPrepareCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var envFlags envExecFlags
	command := &cobra.Command{
		Use:   "prepare",
		Short: "准备执行环境：校验配置、准备镜像、创建并核验容器",
		Long: "按固定阶段执行：校验节点与工作区 → 准备镜像 → 创建容器 → 核验可用性。\n" +
			"每个阶段的结果都会输出；任一步失败都会如实报告失败阶段，不会把镜像准备\n" +
			"或容器创建标记为成功。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := envFlags.resolve()
			if err != nil {
				return err
			}
			return withExecutionEnvironmentService(
				cmd.Context(),
				a,
				hostConfigPath(cmd),
				func(ctx context.Context, service *app.ExecutionEnvironmentService) error {
					prepared, prepareErr := service.Prepare(ctx, spec)
					if printErr := printPreparedResult(a, prepared); printErr != nil {
						return printErr
					}
					return prepareErr
				},
			)
		},
	}
	addCredentialFlags(&flags, command)
	addEnvExecFlags(command, &envFlags)
	return command
}

// newExecEnvExecCommand 构建 execenv exec 子命令。
func newExecEnvExecCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var envFlags envExecFlags
	var containerID string
	var workDir string
	var timeout time.Duration
	var envVars []string
	command := &cobra.Command{
		Use:   "exec --container <id> -- <命令> [参数...]",
		Short: "在已有执行环境的容器内执行一条命令",
		Long: "命令以参数数组传递，逐个转义，不经过 shell；工作目录必须位于容器内\n" +
			"/workspace 之下，省略时使用项目目录。容器内命令的退出码会透传。",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := envFlags.resolve()
			if err != nil {
				return err
			}
			return withExecutionEnvironmentService(
				cmd.Context(),
				a,
				hostConfigPath(cmd),
				func(ctx context.Context, service *app.ExecutionEnvironmentService) error {
					environment, err := service.EnvironmentFor(ctx, containerID, spec)
					if err != nil {
						return err
					}
					result, execErr := service.Exec(ctx, environment, execenv.ExecSpec{
						Command: args[0],
						Args:    args[1:],
						WorkDir: workDir,
						Env:     envVars,
						Timeout: timeout,
					})
					if printErr := printExecEnvResult(a, result); printErr != nil {
						return printErr
					}
					if execErr != nil {
						return execErr
					}
					if result.ExitCode != 0 {
						return fmt.Errorf("容器内命令退出码 %d", result.ExitCode)
					}
					return nil
				},
			)
		},
	}
	addCredentialFlags(&flags, command)
	addEnvExecFlags(command, &envFlags)
	command.Flags().StringVar(&containerID, "container", "", "容器登记记录 ID（必填）")
	command.Flags().StringVar(&workDir, "workdir", "", "本次执行的容器内目录，省略时使用项目目录")
	command.Flags().DurationVar(&timeout, "timeout", 0, "本次执行超时时间，省略时使用默认值")
	command.Flags().StringArrayVar(&envVars, "env", nil, "显式传入容器内进程的环境变量，形如 KEY=VALUE")
	if err := command.MarkFlagRequired("container"); err != nil {
		panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
	}
	return command
}

// newExecEnvReleaseCommand 构建 execenv release 子命令。
func newExecEnvReleaseCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var envFlags envExecFlags
	var containerID string
	var retention string
	var outcome string
	var assumeYes bool
	command := &cobra.Command{
		Use:   "release --container <id>",
		Short: "按处置方式释放执行环境拥有的资源",
		Long: "delete 删除容器与任务级临时资源，detach 保留现场并脱离自动管理，keep 保留现场。\n" +
			"删除是不可逆操作，非交互执行必须显式给出 --yes。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := envFlags.resolve()
			if err != nil {
				return err
			}
			value := execenv.Retention(strings.ToLower(strings.TrimSpace(retention)))
			if err := value.Validate(); err != nil {
				return err
			}
			if value == execenv.RetentionDelete && !assumeYes {
				return fmt.Errorf(
					"删除执行环境是不可逆操作；确认后请追加 --yes 重新执行（容器 %s）",
					containerID,
				)
			}
			containerOutcome := domain.ContainerOutcome(strings.ToLower(strings.TrimSpace(outcome)))
			if err := containerOutcome.Validate(); err != nil {
				return err
			}
			return withExecutionEnvironmentService(
				cmd.Context(),
				a,
				hostConfigPath(cmd),
				func(ctx context.Context, service *app.ExecutionEnvironmentService) error {
					environment, err := service.EnvironmentFor(ctx, containerID, spec)
					if err != nil {
						return err
					}
					result, releaseErr := service.Release(ctx, environment, execenv.ReleaseRequest{
						Retention: value,
						Outcome:   containerOutcome,
					})
					if printErr := printReleaseResult(a, result); printErr != nil {
						return printErr
					}
					return releaseErr
				},
			)
		},
	}
	addCredentialFlags(&flags, command)
	addEnvExecFlags(command, &envFlags)
	command.Flags().StringVar(&containerID, "container", "", "容器登记记录 ID（必填）")
	command.Flags().StringVar(
		&retention,
		"retention",
		string(execenv.RetentionKeep),
		"资源处置方式：keep、delete 或 detach",
	)
	command.Flags().StringVar(
		&outcome,
		"outcome",
		string(domain.ContainerOutcomeFailed),
		"任务结果分类：succeeded、failed 或 interrupted",
	)
	command.Flags().BoolVar(&assumeYes, "yes", false, "确认执行删除（delete 时必填）")
	if err := command.MarkFlagRequired("container"); err != nil {
		panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
	}
	return command
}

// printPreparedResult 输出执行环境准备结果。
func printPreparedResult(a *app.App, prepared execenv.Prepared) error {
	lines := make([]string, 0, len(prepared.Steps)+8)
	for _, step := range prepared.Steps {
		status := "成功"
		if !step.Succeeded() {
			status = "失败"
		}
		line := fmt.Sprintf("[%s] %s（%s）", status, step.Step, step.Duration().Round(time.Millisecond))
		if step.Detail != "" {
			line += ": " + step.Detail
		}
		if step.Err != nil {
			line += "；原因: " + step.Err.Error()
		}
		lines = append(lines, line)
	}
	if prepared.Err == nil {
		container := prepared.Environment.Container()
		lines = append(lines,
			fmt.Sprintf("容器记录: %s", container.ContainerID),
			fmt.Sprintf("Docker 容器: %s", container.DockerID),
			fmt.Sprintf("镜像: %s（%s）", prepared.ImageRef, prepared.ImageID),
			fmt.Sprintf("项目目录: %s", prepared.Environment.WorkDir()),
			fmt.Sprintf("管理工作区: %s", prepared.Management.Project),
		)
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// printExecEnvResult 输出执行环境命令结果。
func printExecEnvResult(a *app.App, result execenv.ExecResult) error {
	if _, err := fmt.Fprint(a.Stdout(), result.Stdout); err != nil {
		return err
	}
	if strings.TrimSpace(result.Stderr) != "" {
		if _, err := fmt.Fprint(a.Stderr(), result.Stderr); err != nil {
			return err
		}
	}
	return nil
}

// printReleaseResult 输出资源释放结果。
func printReleaseResult(a *app.App, result execenv.ReleaseResult) error {
	lines := []string{
		fmt.Sprintf("处置方式: %s", result.Retention),
		fmt.Sprintf("容器已删除: %s", flagMark(result.ContainerRemoved)),
		fmt.Sprintf("容器已保留: %s", flagMark(result.ContainerRetained)),
		fmt.Sprintf("任务级临时资源已清理: %s", flagMark(result.TaskResourcesRemoved)),
	}
	if result.Detail != "" {
		lines = append(lines, "说明: "+result.Detail)
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// newExecEnvPrepareTaskCommand 构建 execenv prepare-task 子命令。
//
// 该命令把任务记录与环境准备关联起来：执行节点、语言环境、版本、缓存与工作目录
// 全部来自任务配置，调用方不能逐项覆盖。
func newExecEnvPrepareTaskCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var attemptID string
	var skipRunContract bool
	command := &cobra.Command{
		Use:   "prepare-task <task-id>",
		Short: "依据任务记录准备执行环境并写回容器关联",
		Long: "执行节点、语言环境、环境版本、缓存与工作目录全部取自任务配置。\n" +
			"准备成功后把新的容器登记记录写回任务；准备失败时不会留下指向不存在容器的引用。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withExecutionEnvironmentService(
				cmd.Context(),
				a,
				hostConfigPath(cmd),
				func(ctx context.Context, service *app.ExecutionEnvironmentService) error {
					prepared, prepareErr := service.PrepareForTask(ctx, app.TaskEnvironmentRequest{
						TaskID:               args[0],
						AttemptID:            attemptID,
						SkipRunContractCheck: skipRunContract,
					})
					if printErr := printPreparedResult(a, prepared); printErr != nil {
						return printErr
					}
					return prepareErr
				},
			)
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().StringVar(&attemptID, "attempt", "", "本次执行所属尝试标识，可省略")
	command.Flags().BoolVar(
		&skipRunContract,
		"skip-run-contract-check",
		false,
		"跳过镜像常驻运行契约预检（仅在已确认镜像可用时使用）",
	)
	return command
}
