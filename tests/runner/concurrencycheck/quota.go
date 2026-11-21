package concurrencycheck

import (
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 本文件是 V4 R2 的并发额度自检：包级上限、命名并发池、8/3/5 场景与声明冲突。
//
// 断言落在编排进程的报告字段上（concurrency.package / concurrency.quotas 的 limit、
// declared_limit、peak、completed、queued_ms_*、undispatched）：用例本身只占用窗口，
// 不用 sleep 猜时序。唯一在用例内断言的是声明冲突，它属于纯函数的预检结论。

const (
	// quotaPackage 是包级上限场景使用的包路径：同一个 Package 共享一份额度。
	quotaPackage = "tests/runner/concurrencycheck/quota"
	// quotaPackageLimit 是包级上限的声明值。
	quotaPackageLimit = 2
	// poolLimited 与 poolSlotting 是两处命名池：前者验证"池内排队不挡池外"，
	// 后者验证 8/3/5 场景。池名与容量都来自用例声明，运行时不需要额外的命令行参数。
	poolLimited  = "model-provider:vendor-A"
	poolSlotting = "model-provider:vendor-B"
	// poolLimitedCapacity 与 poolSlottingCapacity 是两处池容量的声明值。
	poolLimitedCapacity  = 3
	poolSlottingCapacity = 3
)

// 场景 10：包级上限。
//
// 4 条用例声明同一个包与同一个包级上限 2：--parallel 8 时该包的 peak 必须不超过 2，
// 其余用例排队而不失败，报告里的 queued_ms_total 大于 0。
var (
	PackageLimitOne   = packageLimitCase("concurrencycheck/quota-package-1")
	PackageLimitTwo   = packageLimitCase("concurrencycheck/quota-package-2")
	PackageLimitThree = packageLimitCase("concurrencycheck/quota-package-3")
	PackageLimitFour  = packageLimitCase("concurrencycheck/quota-package-4")
)

// packageLimitCase 注册一条声明包级上限的占用用例。
func packageLimitCase(id string) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       id,
		Package:  quotaPackage,
		Parallel: true,
		Concurrency: testrunner.ConcurrencyDecl{
			PackageLimit: quotaPackageLimit,
		},
		Run: func(c *testrunner.Context) {
			c.Logf("用例 %s 占用包 %s 的额度", c.Name(), quotaPackage)
			time.Sleep(holdWindow)
		},
	})
}

// 场景 11：命名并发池与队头阻塞。
//
// 4 条池内用例 + 2 条池外用例：池容量 3（由其中一条池内用例声明，其余用例只声明参与），
// 因此池内任何时刻最多 3 条在飞；池内第 4 条排队时，池外用例必须照常被派发（I6）。
var (
	PoolLimitOne        = poolCase("concurrencycheck/quota-pool-1", poolLimited, poolLimitedCapacity)
	PoolLimitTwo        = poolCase("concurrencycheck/quota-pool-2", poolLimited, 0)
	PoolLimitThree      = poolCase("concurrencycheck/quota-pool-3", poolLimited, 0)
	PoolLimitFour       = poolCase("concurrencycheck/quota-pool-4", poolLimited, 0)
	PoolLimitOutsideOne = outsidePoolCase("concurrencycheck/quota-pool-outside-1")
	PoolLimitOutsideTwo = outsidePoolCase("concurrencycheck/quota-pool-outside-2")
)

// 场景 12：8/3/5 场景。
//
// 12 条池内用例（池容量 3）+ 8 条池外用例，--parallel 8 时稳态是 3 条池内 + 5 条池外同时在飞，
// 池内完成一条立刻补一条；池外先跑完时只剩 3 条池内在飞、其余槽位空闲——这是正确结果（I5）。
var slottingCases = registerSlottingCases()

// poolCase 注册一条参与命名池的占用用例；capacity 为 0 表示只参与、容量由别处声明。
func poolCase(id, pool string, capacity int) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       id,
		Parallel: true,
		Concurrency: testrunner.ConcurrencyDecl{
			Pools: map[string]int{pool: capacity},
		},
		Run: func(c *testrunner.Context) {
			c.Logf("用例 %s 占用命名池 %s 的额度", c.Name(), pool)
			time.Sleep(holdWindow)
		},
	})
}

// outsidePoolCase 注册一条不参与任何池的占用用例：用于证明池内排队不阻塞池外。
func outsidePoolCase(id string) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       id,
		Parallel: true,
		Run: func(c *testrunner.Context) {
			c.Logf("用例 %s 不参与命名池", c.Name())
			time.Sleep(holdWindow)
		},
	})
}

// registerSlottingCases 注册 8/3/5 场景的 12 条池内用例与 8 条池外用例。
func registerSlottingCases() []testrunner.Case {
	registered := make([]testrunner.Case, 0, 20)
	for index := 1; index <= 12; index++ {
		capacity := 0
		if index == 1 {
			capacity = poolSlottingCapacity
		}
		registered = append(registered, poolCase(slottingID("quota-slot", index), poolSlotting, capacity))
	}
	for index := 1; index <= 8; index++ {
		registered = append(registered, outsidePoolCase(slottingID("quota-slot-outside", index)))
	}
	return registered
}

// slottingID 拼出两位序号，保证 ID 升序与真实派发顺序一致。
func slottingID(prefix string, index int) string {
	return "concurrencycheck/" + prefix + "-" + pad2(index)
}

// pad2 把序号补成两位，保证 ID 升序与字符串序一致。
func pad2(value int) string {
	return fmt.Sprintf("%02d", value)
}

// 场景 13：额度声明的预检结论。
//
// 声明冲突、池没有容量都是纯函数结论，不需要真的派发：它在派出第一条 Case 之前就该失败。
// 运行级放大按 D-04 生效，并留下"声明值 vs 生效值"的 WARN。
var QuotaDeclarationCheck = testrunner.Register(testrunner.Case{
	ID:       "concurrencycheck/quota-declaration-check",
	Parallel: true,
	Run: func(c *testrunner.Context) {
		checkPackageConflict(c)
		checkPoolConflict(c)
		checkPoolWithoutCapacity(c)
		checkRunLevelOverride(c)
	},
})

// checkPackageConflict 断言同包两个不同的非零包级上限在预检期失败，并给出两个声明值。
func checkPackageConflict(c *testrunner.Context) {
	_, err := testrunner.ResolveConcurrencyLimits(
		[]testrunner.Case{
			{
				ID:          "quota-decl/package-a",
				Package:     "tests/runner/concurrencycheck/quota-conflict",
				Concurrency: testrunner.ConcurrencyDecl{PackageLimit: 2},
			},
			{
				ID:          "quota-decl/package-b",
				Package:     "tests/runner/concurrencycheck/quota-conflict",
				Concurrency: testrunner.ConcurrencyDecl{PackageLimit: 3},
			},
		},
		testrunner.ConcurrencyPolicy{},
	)
	if err == nil {
		c.Fatalf("同包两个不同的包级上限没有被判为声明冲突")
	}
	if !strings.Contains(err.Error(), "2") || !strings.Contains(err.Error(), "3") {
		c.Fatalf("包级上限冲突必须给出两个声明值，实际: %v", err)
	}
	c.Logf("包级上限冲突按预期失败: %v", err)
}

// checkPoolConflict 断言同池两个不同的非零容量在预检期失败。
func checkPoolConflict(c *testrunner.Context) {
	_, err := testrunner.ResolveConcurrencyLimits(
		[]testrunner.Case{
			{
				ID:          "quota-decl/pool-a",
				Concurrency: testrunner.ConcurrencyDecl{Pools: map[string]int{"quota-decl-conflict": 2}},
			},
			{
				ID:          "quota-decl/pool-b",
				Concurrency: testrunner.ConcurrencyDecl{Pools: map[string]int{"quota-decl-conflict": 4}},
			},
		},
		testrunner.ConcurrencyPolicy{},
	)
	if err == nil {
		c.Fatalf("同池两个不同的容量没有被判为声明冲突")
	}
	if !strings.Contains(err.Error(), "2") || !strings.Contains(err.Error(), "4") {
		c.Fatalf("池容量冲突必须给出两个声明值，实际: %v", err)
	}
	c.Logf("池容量冲突按预期失败: %v", err)
}

// checkPoolWithoutCapacity 断言引用了池却没有容量声明时预检失败，而不是永久排队。
func checkPoolWithoutCapacity(c *testrunner.Context) {
	_, err := testrunner.ResolveConcurrencyLimits(
		[]testrunner.Case{{
			ID:          "quota-decl/pool-missing",
			Concurrency: testrunner.ConcurrencyDecl{Pools: map[string]int{"quota-decl-missing": 0}},
		}},
		testrunner.ConcurrencyPolicy{},
	)
	if err == nil {
		c.Fatalf("引用没有容量声明的池没有被预检拦下")
	}
	if !strings.Contains(err.Error(), "quota-decl-missing") {
		c.Fatalf("缺容量错误必须给出池名，实际: %v", err)
	}
	c.Logf("缺容量按预期失败: %v", err)
}

// checkRunLevelOverride 断言 D-04：运行级值生效并留下与声明值不一致的 WARN。
func checkRunLevelOverride(c *testrunner.Context) {
	limits, err := testrunner.ResolveConcurrencyLimits(
		[]testrunner.Case{{
			ID:          "quota-decl/override",
			Package:     "tests/runner/concurrencycheck/quota-override",
			Concurrency: testrunner.ConcurrencyDecl{PackageLimit: 2},
		}},
		testrunner.ConcurrencyPolicy{PackageLimit: 5},
	)
	if err != nil {
		c.Fatalf("运行级放大不应被判为冲突: %v", err)
	}
	warnings := limits.Warnings()
	if len(warnings) == 0 {
		c.Fatalf("运行级覆盖用例声明时必须留下 WARN")
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "2") || !strings.Contains(joined, "5") {
		c.Fatalf("WARN 必须同时给出声明值与生效值，实际: %s", joined)
	}
	c.Logf("运行级覆盖按预期生效: %s", joined)
}
