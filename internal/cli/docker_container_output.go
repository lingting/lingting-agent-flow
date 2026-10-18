package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
)

// printContainerDetail 输出容器记录与远程观测结果。
func printContainerDetail(a *app.App, detail app.ContainerDetail) error {
	container := detail.Container
	if container.ID.IsEmpty() {
		return nil
	}
	lines := []string{
		fmt.Sprintf("记录 ID: %s", container.ID),
		fmt.Sprintf("节点: %s", container.HostID),
		fmt.Sprintf("容器名称: %s", container.Name),
		fmt.Sprintf("Docker 容器 ID: %s", emptyAsUnknown(container.DockerID)),
		fmt.Sprintf("环境: %s %s", container.Environment.Label(), container.EnvironmentVersion),
		fmt.Sprintf("镜像 ID: %s", container.ImageID),
		fmt.Sprintf("管理状态: %s", container.State),
		fmt.Sprintf("Docker 状态: %s", container.DockerState),
		fmt.Sprintf("脱离管理: %s", flagMark(container.Detached)),
		fmt.Sprintf("创建时间: %s", container.CreatedAt.Format(timeFormat)),
	}
	if container.StartedAt != nil {
		lines = append(lines, fmt.Sprintf("启动时间: %s", container.StartedAt.Format(timeFormat)))
	}
	if container.FinishedAt != nil {
		lines = append(lines, fmt.Sprintf("结束时间: %s", container.FinishedAt.Format(timeFormat)))
	}
	if container.EndReason != "" {
		lines = append(lines, fmt.Sprintf("结束原因: %s", container.EndReason))
	}
	if container.ErrorSummary != "" {
		lines = append(lines, fmt.Sprintf("诊断摘要: %s", container.ErrorSummary))
	}
	if detail.Inspect != nil {
		lines = append(
			lines,
			fmt.Sprintf("观测状态: %s", detail.Inspect.State),
			fmt.Sprintf("观测运行中: %s", flagMark(detail.Inspect.Running)),
			fmt.Sprintf("观测退出码: %d", detail.Inspect.ExitCode),
		)
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// printExecResult 输出容器内命令的执行结果。
func printExecResult(a *app.App, result app.ExecResult) error {
	lines := []string{
		fmt.Sprintf("容器: %s", result.Container.ID),
		fmt.Sprintf("退出码: %d", result.Result.ExitCode),
		fmt.Sprintf("耗时: %s", result.Result.Duration.Round(time.Millisecond)),
	}
	if result.Result.Stdout != "" {
		lines = append(lines, "标准输出:", indentBlock(result.Result.Stdout))
	}
	if result.Result.Stderr != "" {
		lines = append(lines, "标准错误:", indentBlock(result.Result.Stderr))
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// printLifecycleResult 输出生命周期策略的执行结果。
func printLifecycleResult(a *app.App, result app.ApplyLifecycleResult) error {
	lines := []string{
		fmt.Sprintf("策略结论: %s", result.Decision.Retention),
		fmt.Sprintf("结束原因: %s", result.Decision.EndReason),
		fmt.Sprintf("说明: %s", result.Decision.Detail),
	}
	if result.Decision.RemoveTaskResources {
		lines = append(
			lines,
			"任务级临时资源: 一并清理（不包含共享镜像、共享缓存卷与远程项目配置目录）",
		)
	}
	if !result.Container.ID.IsEmpty() {
		lines = append(
			lines,
			fmt.Sprintf("容器记录: %s", result.Container.ID),
			fmt.Sprintf("管理状态: %s", result.Container.State),
		)
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}
