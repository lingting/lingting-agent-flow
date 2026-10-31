// Command agentflow-release 生成 AgentFlow 的正式发布制品。
//
// 它只做"构建与打包"：按发布矩阵交叉编译单文件程序，把可执行文件、快速启动说明、
// 版本元数据与第三方声明装进压缩包，最后生成 SHA256SUMS 与 release-manifest.json
// 并自检产物。它不创建 GitHub Release、不推送镜像、不部署任何环境。
//
// 用法（在仓库根目录执行）：
//
//	go run ./cmd/agentflow-release -version v1.2.3 -output dist/release
//
// 版本号不传时输出 dev 制品，用于本地核对打包流程；正式版本必须是 vMAJOR.MINOR.PATCH。
package main

import (
	"fmt"
	"io"
	"os"
)

// main 执行发布构建并以退出码结束进程。
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 解析参数、执行构建，返回进程退出码。
//
// 参数错误返回 2，与构建失败（1）区分：CI 里"命令写错"和"构建真的失败"需要
// 不同的排查方向。
func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseOptions(args, stderr)
	if err != nil {
		return 2
	}
	if err := buildRelease(opts, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "错误: %v\n", err)
		return 1
	}
	return 0
}
