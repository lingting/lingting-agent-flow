package cli

import (
	"fmt"
	"os"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/server"
	"github.com/spf13/cobra"
)

// runServerInBackground 以后台方式启动服务端。
//
// 命令层只负责三件事：确认本次将使用的配置、把当前可执行文件与参数交给后台启动，
// 再输出后台服务的真实地址与日志位置。转发输出与等待健康检查都由服务层完成。
func runServerInBackground(
	cmd *cobra.Command,
	a *app.App,
	configPath, address string,
	requireWebUI bool,
) error {
	ctx := cmd.Context()
	cfg, err := a.ValidateServerConfig(ctx, configPath)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位当前程序失败: %w", err)
	}
	background, err := server.StartBackground(ctx, server.BackgroundOptions{
		Config:     cfg,
		Address:    address,
		Executable: executable,
		Args:       serverArgs(configPath, address, requireWebUI),
		Stdout:     a.Stdout(),
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		a.Stdout(),
		"后台服务已启动: http://%s（PID %d）\n日志: %s\n",
		background.Address(),
		background.PID(),
		background.LogPath(),
	)
	return err
}

// serverArgs 生成后台子进程的参数。
//
// 参数按当前命令的显式取值重建，而不是过滤原始命令行：这样后台进程不会重复携带
// --async（否则会无限再生后台进程），也不会因为省略参数而回落到与本次不同的默认值。
func serverArgs(configPath, address string, requireWebUI bool) []string {
	args := []string{"server"}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	if address != "" {
		args = append(args, "--address", address)
	}
	if !requireWebUI {
		args = append(args, "--require-web-ui=false")
	}
	return args
}
