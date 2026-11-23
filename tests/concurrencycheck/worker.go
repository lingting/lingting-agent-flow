package concurrencycheck

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 本文件是 V4 R3 的 worker 池自检：复用、独占与状态隔离。
//
// 断言主要落在编排进程的报告字段上（workers 分节的 total / reuse / restarts /
// isolated_cases 与每条 Case 的 worker_id），用例只负责制造场景；探针方式与期望写在各场景的
// 注释里。
//
// 状态隔离用一对用例验证：前一条改动环境变量与工作目录，后一条断言它们已经被重置（W3）。
// --parallel 1 时两条必然落在同一个 worker 上，报告里相同的 worker_id 是"确实复用"的凭证；
// 包级变量不在 W3 的重置范围内，由迁移时逐包核对（R5–R8 每包登记）。

// reuseCaseTotal 是复用场景的用例条数：远多于探针使用的 --parallel 2，复用才有观测空间。
const reuseCaseTotal = 6

// 场景 14：多条无副作用用例复用一个 worker。
//
// 探针用 --parallel 2 执行这 6 条：workers.total 应为 2、workers.reuse 应为 4，
// 每条用例的 worker_id 只会取这两个值（旧模型下 total 会是 6、reuse 为 0）。
var reuseCases = registerReuseCases()

// registerReuseCases 注册复用场景的用例：都允许并行、不声明资源，只占用一个窗口。
func registerReuseCases() []testrunner.Case {
	registered := make([]testrunner.Case, 0, reuseCaseTotal)
	for index := 1; index <= reuseCaseTotal; index++ {
		registered = append(registered, testrunner.Register(testrunner.Case{
			ID:       slottingID("worker-reuse", index),
			Parallel: true,
			Run: func(c *testrunner.Context) {
				c.Logf("用例 %s 在 worker %d 上执行", c.Name(), c.WorkerID())
				time.Sleep(holdWindow)
			},
		}))
	}
	return registered
}

// 场景 15：独占用例。
//
// 两条用例都声明 DedicatedWorker：每条都由全新的 worker 执行、执行完即退出。探针核对
// workers.isolated_cases 等于 2，且两条用例的 worker_id 互不相同、各自只出现一次。
var (
	DedicatedOne = dedicatedWorkerCase("concurrencycheck/worker-dedicated-a")
	DedicatedTwo = dedicatedWorkerCase("concurrencycheck/worker-dedicated-b")
)

// dedicatedWorkerCase 注册一条必须独占 worker 的用例。
func dedicatedWorkerCase(id string) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:              id,
		Parallel:        true,
		DedicatedWorker: true,
		Run: func(c *testrunner.Context) {
			c.Logf("用例 %s 独占 worker %d（pid %d）", c.Name(), c.WorkerID(), os.Getpid())
			time.Sleep(holdWindow)
		},
	})
}

// workerBleedEnv 是串味场景使用的环境变量名：用例自己设置，下一条用例必须看不到它。
const workerBleedEnv = "AGENTFLOW_WORKER_BLEED"

// 场景 16：复用 worker 的进程级状态隔离（W3）。
var (
	// BleedDirty 改动环境变量与工作目录：它留下的状态不得带到同一条 worker 的下一条用例。
	BleedDirty = testrunner.Register(testrunner.Case{
		ID:       "concurrencycheck/worker-bleed-a",
		Parallel: true,
		Run: func(c *testrunner.Context) {
			if err := os.Setenv(workerBleedEnv, "1"); err != nil {
				c.Fatalf("设置环境变量失败: %v", err)
			}
			if err := os.Chdir(os.TempDir()); err != nil {
				c.Fatalf("切换工作目录失败: %v", err)
			}
			c.Logf("用例 %s 故意改动环境变量与工作目录（worker %d）", c.Name(), c.WorkerID())
		},
	})
	// BleedClean 断言上一条用例留下的进程级状态已经被重置：它只有在同一个 worker 里才构成证据。
	BleedClean = testrunner.Register(testrunner.Case{
		ID:       "concurrencycheck/worker-bleed-b",
		Parallel: true,
		Run: func(c *testrunner.Context) {
			if _, exists := os.LookupEnv(workerBleedEnv); exists {
				c.Fatalf("上一条用例设置的环境变量 %s 没有被重置", workerBleedEnv)
			}
			dir, err := os.Getwd()
			if err != nil {
				c.Fatalf("读取工作目录失败: %v", err)
			}
			if sameDirectory(dir, os.TempDir()) {
				c.Fatalf("上一条用例切换的工作目录没有被恢复，当前仍在 %s", dir)
			}
			c.Logf("用例 %s 在 worker %d 上确认进程级状态已重置", c.Name(), c.WorkerID())
		},
	})
)

// sameDirectory 比较两个目录是否相同：Windows 的路径大小写不敏感，直接比较字符串会误判。
func sameDirectory(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}
