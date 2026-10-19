package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/spf13/cobra"
)

// newContainerLifecycleCommand 构建 docker container lifecycle 子命令。
func newContainerLifecycleCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var outcome string
	var keepInterrupted bool
	command := &cobra.Command{
		Use:   "lifecycle <container-id>",
		Short: "按任务结果应用容器生命周期策略",
		Long: "成功：删除容器与任务级临时资源；失败：保留现场；中断：默认保留，\n" +
			"--keep-interrupted=false 时删除。\n" +
			"注意：本阶段没有实现代码保存，因此不能把容器内命令成功直接当作任务成功；\n" +
			"成功清理只能在工作流引擎确认必要业务操作完成后调用。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parsed := domain.ContainerOutcome(strings.TrimSpace(outcome))
			if err := parsed.Validate(); err != nil {
				return err
			}
			if parsed == domain.ContainerOutcomeSucceeded {
				return fmt.Errorf(
					"拒绝把容器内命令结果直接当作任务成功：本阶段未实现代码保存，" +
						"成功清理必须由工作流引擎在业务操作完成后调用",
				)
			}
			return withContainerService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ContainerService,
			) error {
				result, applyErr := service.ApplyLifecycle(ctx, app.ApplyLifecycleRequest{
					ContainerID:     args[0],
					Outcome:         parsed,
					KeepInterrupted: keepInterrupted,
				})
				if printErr := printLifecycleResult(a, result); printErr != nil {
					return printErr
				}
				return applyErr
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().StringVar(&outcome, "outcome", "", "任务结果分类：failed 或 interrupted（必填）")
	command.Flags().BoolVar(
		&keepInterrupted,
		"keep-interrupted",
		true,
		"中断时是否保留容器现场",
	)
	if err := command.MarkFlagRequired("outcome"); err != nil {
		panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
	}
	return command
}

// newContainerRestartCommand 构建 docker container restart 子命令。
func newContainerRestartCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var previous string
	var disposition string
	var assumeYes bool
	var caches []string
	var projectID string
	command := &cobra.Command{
		Use:   "restart",
		Short: "重新开始失败任务：处置旧容器并创建新容器",
		Long: "重新开始必须创建新的容器实例：不会复用旧容器，也不会把旧容器中的未推送代码\n" +
			"带入新容器。旧容器必须显式选择 delete（删除）或 detach（保留现场并脱离管理）。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			parsed, err := domain.ParseRestartDisposition(disposition)
			if err != nil {
				return err
			}
			if !assumeYes {
				return fmt.Errorf(
					"重新开始会处置旧容器（%s）并创建新容器；确认后请追加 --yes",
					previous,
				)
			}
			return withContainerService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ContainerService,
			) error {
				old, err := service.GetContainerRecord(ctx, previous)
				if err != nil {
					return err
				}
				detail, restartErr := service.RestartContainer(ctx, app.RestartContainerRequest{
					PreviousContainerID: previous,
					Disposition:         parsed,
					Create: app.CreateContainerRequest{
						MachineID: old.HostID.String(),
						Ref:       old.Environment,
						ProjectID: projectID,
						Caches:    caches,
					},
				})
				if printErr := printContainerDetail(a, detail); printErr != nil {
					return printErr
				}
				return restartErr
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().StringVar(&previous, "previous", "", "旧容器登记记录 ID（必填）")
	command.Flags().StringVar(
		&disposition,
		"disposition",
		"",
		"旧容器处置方式：delete 或 detach（必填）",
	)
	command.Flags().BoolVar(&assumeYes, "yes", false, "确认重新开始（非交互执行必须显式给出）")
	command.Flags().StringSliceVar(&caches, "cache", nil, "新容器允许挂载的缓存类型")
	command.Flags().StringVar(&projectID, "project", "", "项目标识，用于项目级缓存共享范围")
	for _, name := range []string{"previous", "disposition"} {
		if err := command.MarkFlagRequired(name); err != nil {
			panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
		}
	}
	return command
}

// runContainerAction 读取容器记录并执行一次容器操作。
//
// 容器所属节点由登记记录决定；认证方式固定为私钥，命令层不再收集凭据。
func runContainerAction(
	ctx context.Context,
	a *app.App,
	configPath string,
	containerID string,
	flags credentialFlags,
	run func(
		context.Context,
		*app.ContainerService,
	) (app.ContainerDetail, error),
) error {
	return withContainerService(ctx, a, configPath, func(
		ctx context.Context,
		service *app.ContainerService,
	) error {
		// 先确认容器登记记录存在，使"容器不存在"与"操作失败"可以区分。
		if _, err := service.GetContainerRecord(ctx, containerID); err != nil {
			return err
		}
		detail, runErr := run(ctx, service)
		if printErr := printContainerDetail(a, detail); printErr != nil {
			return printErr
		}
		return runErr
	})
}
