// Command agentflow 是 AgentFlow 的命令行入口。
//
// 本文件只负责注入构建信息、启动应用并返回退出码，不放置业务逻辑。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/cli"
)

// 通过 -ldflags -X 注入的构建变量，默认值表示开发构建。
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

// main 启动 CLI 并以退出码结束进程。
func main() {
	os.Exit(run())
}

// run 组装应用并执行命令，返回进程退出码。
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application, err := app.New(app.Options{
		Build: app.BuildInfo{
			Version:   version,
			Commit:    commit,
			BuildDate: buildDate,
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 初始化应用失败: %v\n", err)
		return 1
	}
	return cli.Execute(ctx, application)
}
