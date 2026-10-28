package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/spf13/cobra"
)

// newProjectCommand 构建 project 命令组。
func newProjectCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "project",
		Short: "管理项目登记：仓库、基础分支与执行环境配置",
		Long: "项目记录同一份代码加同一套执行环境的非机密元数据：仓库地址、基础分支、\n" +
			"语言环境与版本、缓存类型、容器内工作目录。\n" +
			"任务必须关联项目，未在创建任务时给出的字段从项目继承；项目标识同时是\n" +
			"项目级缓存的共享范围键与远程管理工作目录名，因此必须指向真实记录。\n" +
			"删除是逻辑删除：历史任务与代码保存记录仍指向该标识，不会被级联删除。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.PersistentFlags().String(
		"config",
		"",
		"配置文件路径（默认 "+config.DefaultPath+"）",
	)
	command.AddCommand(newProjectAddCommand(a))
	command.AddCommand(newProjectListCommand(a))
	command.AddCommand(newProjectShowCommand(a))
	command.AddCommand(newProjectUpdateCommand(a))
	command.AddCommand(newProjectRemoveCommand(a))
	return command
}

// newProjectAddCommand 构建 project add 子命令。
func newProjectAddCommand(a *app.App) *cobra.Command {
	var request app.CreateProjectRequest
	command := &cobra.Command{
		Use:   "add",
		Short: "新增项目",
		Long: "项目标识只能包含小写字母、数字与连字符，且不超过 32 个字符。\n" +
			"仓库地址只允许 ssh://、https:// 与 host:path 三种形式，内嵌凭据的地址会被拒绝。\n" +
			"未指定 --workdir 时使用 " + domain.DefaultContainerWorkDir + "。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := withProjectService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ProjectService,
			) (domain.Project, error) {
				return service.Create(ctx, request)
			})
			if err != nil {
				return err
			}
			return printProjectSummary(a, project)
		},
	}
	command.Flags().StringVar(&request.ID, "id", "", "项目标识（必填）")
	command.Flags().StringVar(&request.Name, "name", "", "项目名称（必填）")
	command.Flags().StringVar(&request.RepositoryURL, "repo", "", "仓库地址（必填）")
	command.Flags().StringVar(&request.BaseBranch, "base", "", "基础分支（必填）")
	command.Flags().StringVar(&request.Environment, "environment", "", "语言环境名称（必填）")
	command.Flags().StringVar(&request.EnvironmentVersion, "environment-version", "", "锁定的环境版本标签")
	command.Flags().StringSliceVar(&request.Caches, "cache", nil, "允许挂载的缓存类型，可重复")
	command.Flags().StringVar(&request.WorkDir, "workdir", "", "容器内项目目录，省略时使用默认目录")
	for _, required := range []string{"id", "name", "repo", "base", "environment"} {
		if err := command.MarkFlagRequired(required); err != nil {
			panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
		}
	}
	return command
}

// newProjectListCommand 构建 project list 子命令。
func newProjectListCommand(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出未删除的项目",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			projects, err := withProjectService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ProjectService,
			) ([]domain.Project, error) {
				return service.List(ctx)
			})
			if err != nil {
				return err
			}
			if len(projects) == 0 {
				_, err := fmt.Fprintln(a.Stdout(), "尚无项目")
				return err
			}
			for _, project := range projects {
				line := fmt.Sprintf(
					"%s\t%s\t%s\t%s\t%s",
					project.ID,
					project.Name,
					project.RepositoryURL,
					project.NormalizedBaseBranch(),
					describeProjectEnvironment(project),
				)
				if _, err := fmt.Fprintln(a.Stdout(), line); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// newProjectShowCommand 构建 project show 子命令。
func newProjectShowCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "show <project-id>",
		Short: "查看项目详情",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := withProjectService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ProjectService,
			) (domain.Project, error) {
				return service.Get(ctx, args[0])
			})
			if err != nil {
				return err
			}
			return printProjectSummary(a, project)
		},
	}
	return command
}

// newProjectUpdateCommand 构建 project update 子命令。
//
// 更新是整体替换：所有字段都必须给出，避免部分更新让调用方难以判断某次修改后
// 缓存与目录到底是什么。
func newProjectUpdateCommand(a *app.App) *cobra.Command {
	var request app.UpdateProjectRequest
	var projectID string
	command := &cobra.Command{
		Use:   "update",
		Short: "整体更新项目配置",
		Long: "所有字段按整体替换处理，未给出的字段不会继承原值。\n" +
			"项目标识不可修改；已创建的任务保留创建时的配置快照，不受本次更新影响。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := withProjectService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ProjectService,
			) (domain.Project, error) {
				return service.Update(ctx, projectID, request)
			})
			if err != nil {
				return err
			}
			return printProjectSummary(a, project)
		},
	}
	command.Flags().StringVar(&projectID, "id", "", "项目标识（必填）")
	command.Flags().StringVar(&request.Name, "name", "", "项目名称（必填）")
	command.Flags().StringVar(&request.RepositoryURL, "repo", "", "仓库地址（必填）")
	command.Flags().StringVar(&request.BaseBranch, "base", "", "基础分支（必填）")
	command.Flags().StringVar(&request.Environment, "environment", "", "语言环境名称（必填）")
	command.Flags().StringVar(&request.EnvironmentVersion, "environment-version", "", "锁定的环境版本标签")
	command.Flags().StringSliceVar(&request.Caches, "cache", nil, "允许挂载的缓存类型，可重复")
	command.Flags().StringVar(&request.WorkDir, "workdir", "", "容器内项目目录，省略时使用默认目录")
	for _, required := range []string{"id", "name", "repo", "base", "environment"} {
		if err := command.MarkFlagRequired(required); err != nil {
			panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
		}
	}
	return command
}

// newProjectRemoveCommand 构建 project remove 子命令。
func newProjectRemoveCommand(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <project-id>",
		Short: "逻辑删除项目",
		Long: "只标记删除时间：历史任务、代码获取与代码保存记录仍保留该标识，\n" +
			"任务自身的配置快照也不会改变。删除后不能再创建关联该项目的任务。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := withProjectService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ProjectService,
			) (struct{}, error) {
				return struct{}{}, service.Delete(ctx, args[0])
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(a.Stdout(), "项目已删除: %s\n", strings.TrimSpace(args[0]))
			return err
		},
	}
}

// withProjectService 加载配置并构造项目管理服务。
func withProjectService[T any](
	ctx context.Context,
	a *app.App,
	configPath string,
	run func(ctx context.Context, service *app.ProjectService) (T, error),
) (T, error) {
	var zero T
	cfg, err := a.ValidateConfig(ctx, configPath)
	if err != nil {
		return zero, err
	}
	store, err := a.OpenStore(ctx, cfg)
	if err != nil {
		return zero, err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			a.Logger().Slog().WarnContext(ctx, "关闭数据库失败", "error", closeErr)
		}
	}()
	service, err := app.NewProjectService(app.ProjectServiceOptions{
		Store:  store,
		Logger: a.Logger().Slog(),
	})
	if err != nil {
		return zero, err
	}
	return run(ctx, service)
}

// describeProjectEnvironment 返回项目环境的可读描述。
func describeProjectEnvironment(project domain.Project) string {
	version := project.EnvironmentVersion
	if strings.TrimSpace(version) == "" {
		version = "当前可用版本"
	}
	return project.Environment.Name + " " + version
}

// printProjectSummary 输出项目摘要。
func printProjectSummary(a *app.App, project domain.Project) error {
	caches := "未声明"
	if len(project.Caches) > 0 {
		caches = strings.Join(project.Caches, ", ")
	}
	_, err := fmt.Fprintf(
		a.Stdout(),
		"项目: %s\n名称: %s\n仓库: %s\n基础分支: %s\n环境: %s\n缓存: %s\n"+
			"工作目录: %s\n创建时间: %s\n更新时间: %s\n",
		project.ID,
		project.Name,
		project.RepositoryURL,
		project.NormalizedBaseBranch(),
		describeProjectEnvironment(project),
		caches,
		project.EffectiveWorkDir(),
		formatProjectTime(project.CreatedAt),
		formatProjectTime(project.UpdatedAt),
	)
	return err
}

// formatProjectTime 以 UTC RFC3339 输出时间。
func formatProjectTime(at time.Time) string {
	if at.IsZero() {
		return "—"
	}
	return at.UTC().Format(time.RFC3339)
}
