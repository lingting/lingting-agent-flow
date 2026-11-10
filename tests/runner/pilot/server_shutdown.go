package pilot

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 试点的时间预算：启动（含首次数据库迁移）与优雅关闭都留足余量，超时一律判失败。
const (
	// serverReadyTimeout 是等待 /api/v1/health 就绪的上限。
	serverReadyTimeout = 60 * time.Second
	// serverShutdownWindow 是等待服务进程自行退出的上限，必须大于示例配置的关闭超时（默认 10s）。
	serverShutdownWindow = 30 * time.Second
	// serverCaseTimeout 是试点用例的执行上限。
	serverCaseTimeout = 180 * time.Second
)

// ServerShutdownCase 是 V3 R4 的试点用例：真实服务进程的启动、就绪、优雅停止与关闭后核验。
//
// 服务进程由 service-process 提供者启动并托管（工作目录取用例工作空间），用例只消费句柄：
// 投递中断、取退出码、核验端口与数据库，最后交给提供者释放与自证。
var ServerShutdownCase = testrunner.Register(testrunner.Case{
	ID:       "pilot/server-shutdown",
	Timeout:  serverCaseTimeout,
	Parallel: false,
	Resources: []testrunner.ResourceDecl{{
		Name:  "service",
		Type:  testrunner.ResourceServiceProcess,
		Scope: testrunner.ScopeCaseExclusive,
		Spec: map[string]string{
			"binary":           serverBinaryPath(),
			"args":             serverArguments(),
			"ready_path":       healthPath,
			"ready_timeout_ms": strconv.Itoa(int(serverReadyTimeout.Milliseconds())),
		},
	}},
	Run: runServerShutdown,
})

// runServerShutdown 执行试点主体：就绪核验 → 请求优雅停止 → 关闭后核验。
//
// 顺序是有意的：先打开进程句柄再投递中断（进程退出后取不到退出码），先看 WAL/SHM 再重开数据库
// （重开会收起附属文件）。
func runServerShutdown(c *testrunner.Context) {
	handle, ok := c.Resource("service")
	if !ok {
		c.Fatalf("缺少资源 service 的句柄")
	}
	pid, err := strconv.Atoi(handle.PayloadValue("pid"))
	if err != nil || pid <= 0 {
		c.Fatalf("句柄的 pid %q 不是合法进程号", handle.PayloadValue("pid"))
	}
	workspace := c.WorkspaceDir()
	if workspace == "" {
		c.Fatalf("编排进程没有下发用例工作空间目录")
	}
	baseURL := handle.PayloadValue("base_url")
	database := databasePath(workspace)
	c.Logf("服务进程就绪：pid=%d base_url=%s 工作空间=%s", pid, baseURL, workspace)

	assertIsServerBinary(c, handle)
	assertHealthy(c, baseURL)
	assertDatabaseCreated(c, database)

	process, err := os.FindProcess(pid)
	if err != nil {
		c.Fatalf("打开服务进程 %d 的句柄失败: %v", pid, err)
	}
	stopAt := time.Now()
	if err := testrunner.InterruptProcess(pid); err != nil {
		c.Fatalf("无法向服务进程投递中断信号，优雅停止路径未验证: %v", err)
	}
	exitCode, exitErr := waitExitCode(process, serverShutdownWindow)
	shutdownMS := time.Since(stopAt).Milliseconds()
	switch {
	case errors.Is(exitErr, errExitCodeUnsupported):
		c.Warnf("当前平台（%s）无法读取非子进程的退出码，优雅关闭以服务日志标记为准", runtime.GOOS)
	case exitErr != nil:
		c.Fatalf("等待服务进程退出失败: %v", exitErr)
	case exitCode != 0:
		c.Fatalf("服务进程退出码 %d，不是优雅关闭", exitCode)
	default:
		c.Logf("服务进程退出码 0，关闭耗时 %dms", shutdownMS)
	}

	assertProcessGone(c, pid)
	reportJournalSidecars(c, database)
	assertPortRebindable(c, serviceAddress(c, baseURL))
	reopenDatabase(c, database)
	assertMigrationsApplied(c, database)
	assertGracefulShutdownLogged(c, workspace)
}
