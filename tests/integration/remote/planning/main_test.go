// Package planning_remote 提供需要真实远程 Docker、真实 SSH、真实容器内 Pi CLI 与
// 真实项目代码快照的规划运行时验收测试。
//
// 该包与 tests/unit/app 的规划用例分工：
//   - tests/unit/app 用受控替身覆盖规划运行时的分支与错误路径，可在任意开发机重复执行；
//   - 本目录只用真实依赖（真实文件型 SQLite、真实 SSH 目标、真实远程 Docker Engine、
//     真实只读规划容器、真实固定 commit 快照卷、真实容器内 Pi CLI 与真实模型），用于
//     验证真实环境下只读边界、Session 恢复、容器生命周期、SSH 中断终止与资源释放确实成立。
//
// 运行方式（Git Bash / Linux / macOS）：
//
//	cp tests/config/example.ssh.yml tests/config/ssh.yml       # 开启 ssh.enable 并填写远程目标
//	cp tests/config/example.docker.yml tests/config/docker.yml # 设置 remote_enabled: true 并填写 docker.pi
//	cp tests/config/example.git.yml tests/config/git.yml       # 填写远程可访问的仓库
//	go test -count=1 -v ./tests/integration/remote/planning/...
//
// 安全约束（与仓库测试规范一致）：
//   - 只操作本次测试创建、带随机标识与所有权标签的容器、卷与远程目录；
//   - 不执行全局 prune，不删除共享测试镜像、共享缓存卷与其他测试的资源；
//   - 主机密钥校验不可关闭，也不自动接受未知主机密钥；
//   - 容器内 Pi 的模型凭据只写入规划容器自身的临时可写目录，不写入日志、数据库、
//     项目仓库或测试产物；
//   - 测试产物（数据库、Session 归档、日志）全部落在本机测试工作空间内并保留。
//
// 在 GitHub Actions 中未获真实测试授权时先跳过，不读取配置、不建立任何远程连接。
package planning_remote

import (
	"os"
	"testing"

	"github.com/lingting/agentflow/tests/internal/testkit"
)

// planningHostContainerRun 是本次测试进程通过生产服务创建的规划容器登记表。
//
// 规划容器由 app.ContainerService 创建，名称在创建之前无法预知，因此清理责任在准备
// 运行环境之前登记，并在容器创建成功后补写精确身份。
var planningHostContainerRun = testkit.NewContainerRun("remote/planning")

// planningVolumeRun 是本次测试进程创建的快照卷登记表。
//
// 快照卷与容器同等重要：只清理容器而留下卷会破坏"零残留核验"，因此两者都登记，
// 并在全部测试结束后按真实远端清单核验。
var planningVolumeRun = newVolumeRun("remote/planning")

// TestMain 把本次运行的完整测试输出写入本次运行根目录下的 test.log。
//
// 统一入口保证逐用例结论、WARN [SKIP] 跳过原因与 slog 日志都能事后复核；最终核验在
// 全部测试结束后、日志刷新之前执行，容器或卷的残留会让进程以非零码退出。
func TestMain(m *testing.M) {
	testkit.RegisterContainerFinalizer(planningHostContainerRun)
	planningVolumeRun.RegisterFinalizer()
	os.Exit(testkit.Main(m))
}
