package pilot

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 试点在注册期就能确定的固定约定：被测二进制、示例配置与工作空间内的相对路径。
//
// 用例工作空间要到运行期才由编排进程分配，因此这里只解析仓库内路径；工作空间内的文件位置
// 用相对路径表达，服务进程与用例都以工作空间为工作目录解析同一个相对路径。
const (
	// serverBinaryName 是被测服务可执行文件名。
	serverBinaryName = "agentflow"
	// serverBinaryNameWindows 是 Windows 下带扩展名的构建产物名。
	serverBinaryNameWindows = "agentflow.exe"
	// exampleConfigPath 是仓库内示例配置的相对路径。
	//
	// 示例配置中的数据库、工作区与会话路径都是相对路径，写入位置因此由服务进程的工作目录
	// （用例工作空间）决定：用例按同样的相对路径回读，不需要在注册期拼绝对路径。
	exampleConfigPath = "configs/example.yaml"
	// databaseRelativePath 是示例配置里 storage.database_path 的取值。
	databaseRelativePath = ".agentflow/agentflow.db"
	// healthPath 是被测服务的健康检查路径。
	healthPath = "/api/v1/health"
	// gracefulShutdownMarker 是服务走完优雅关闭时写入标准输出的标记。
	gracefulShutdownMarker = "AgentFlow 服务已关闭"
	// serviceLogDir 是提供者写入服务进程输出文件的目录（相对用例工作空间）。
	serviceLogDir = "process"
)

// repositoryRoot 返回仓库根目录。
//
// 从当前工作目录向上查找同时含 go.mod 与 cmd/agentflow 的目录；找不到时退回当前目录，让缺少的
// 构建产物在分配阶段以"可执行文件不可用"如实失败，而不是静默换成别的程序。
func repositoryRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if isRepositoryRoot(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// isRepositoryRoot 判断目录是否是本仓库根目录。
func isRepositoryRoot(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "cmd", "agentflow"))
	return err == nil && info.IsDir()
}

// serverBinaryPath 返回被测服务的可执行文件路径。
//
// 构建由授权命令在仓库根目录完成：go build -o bin/agentflow ./cmd/agentflow。
func serverBinaryPath() string {
	return filepath.Join(repositoryRoot(), "bin", serverBinaryNameOn(runtime.GOOS))
}

// serverBinaryNameOn 返回指定平台上的被测服务可执行文件名。
//
// Windows 下 go build -o bin/agentflow 得到不带扩展名的 PE 文件，而按文件名启动时会被补上 .exe，
// 因此两种产物名都要接受；存在带扩展名的产物时优先用它。
func serverBinaryNameOn(goos string) string {
	if goos != "windows" {
		return serverBinaryName
	}
	if exists(filepath.Join(repositoryRoot(), "bin", serverBinaryNameWindows)) {
		return serverBinaryNameWindows
	}
	return serverBinaryName
}

// serverArguments 返回启动被测服务的参数。
//
// 端口写成 {port} 占位，由 service-process 提供者替换为它分配的空闲端口；--require-web-ui=false
// 让试点只依赖 API 与进程生命周期，不依赖前端构建产物（R6 范围）。
//
// 参数按空格分隔（契约规定），因此仓库路径不能含空格：试点在仓库路径含空格时会在分配阶段
// 以"服务没有就绪"失败，不会静默换用别的配置。
func serverArguments() string {
	return strings.Join([]string{
		"server",
		"--config", filepath.Join(repositoryRoot(), exampleConfigPath),
		"--address", "127.0.0.1:{port}",
		"--require-web-ui=false",
	}, " ")
}

// exists 报告路径指向的文件是否存在。
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
