package testrunner

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 本文件是 V4 R2 的并发额度内核：包级上限与命名并发池的声明、解析与冲突校验。
//
// 三个决定都是纯函数或纯数据，调度器只读取解析结果：额度不改变"并发由编排进程唯一决定"，
// 用例无法通过声明自行扩大并发。

// 额度条目的类别：报告里包级上限与命名池分成两节，便于分别核对。
const (
	quotaKindPackage = "package"
	quotaKindPool    = "pool"
)

// ConcurrencyDecl 是用例声明的并发额度（V4 R2 §3）。
type ConcurrencyDecl struct {
	// PackageLimit 是同一 Case.Package 同时在跑的上限；0 表示不声明。
	//
	// 同一个包只能有一个非零声明：不同用例给出不同值时在注册期硬失败，不静默取最小值。
	PackageLimit int
	// Pools 声明用例参与的命名并发池：非零值是容量声明，0 表示只参与该池、容量由别处声明。
	//
	// 它表达的是"跨包共享的同一份真实约束"（例如同一个模型供应商），与包级上限是两个概念：
	// 一条用例可以同时受包级上限与多个命名池约束。
	Pools map[string]int
}

// validate 校验额度声明本身是否合法；冲突判定需要看到同一包或同一池的全部声明，不在这里做。
func (d ConcurrencyDecl) validate() error {
	if d.PackageLimit < 0 {
		return fmt.Errorf("包级上限不能为负，当前为 %d", d.PackageLimit)
	}
	for _, name := range sortedKeys(d.Pools) {
		if !poolNamePattern.MatchString(name) {
			return fmt.Errorf("命名池名称 %q 只允许字母、数字与 . _ : - ", name)
		}
		if d.Pools[name] < 0 {
			return fmt.Errorf("命名池 %q 的容量不能为负，当前为 %d", name, d.Pools[name])
		}
	}
	return nil
}

// poolNamePattern 约束命名池的名称：它同时出现在命令行、报告与错误信息里。
var poolNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// ConcurrencyPolicy 是运行级并发额度（D-04：运行级值优先，可临时放大）。
type ConcurrencyPolicy struct {
	// Parallel 是同时在飞的上限；由编排进程按 --parallel 填充，额度解析本身不使用它。
	Parallel int
	// PackageLimit 是运行级包级上限；0 表示按用例声明，仍无声明即不限。
	PackageLimit int
	// Pools 是运行级池容量，键为池名。
	Pools map[string]int
}

// ConcurrencyLimits 是解析后的生效额度，只在编排进程内使用。
type ConcurrencyLimits struct {
	// packageRun 是运行级包级上限；0 表示按用例声明。
	packageRun int
	// packages 与 pools 是生效值：0 表示不限（包）或未声明（池）。
	packages map[string]int
	pools    map[string]int
	// packageDeclared 与 poolDeclared 是用例声明值，只用于报告与 WARN 对照。
	packageDeclared map[string]int
	poolDeclared    map[string]int
	// warnings 记录声明值与生效值不一致等需要人工核对的情况（D-04）。
	warnings []string
}

// ResolveConcurrencyLimits 解析生效额度：运行级值优先，其次是用例声明，都没有则不限或失败。
//
// 预检错误都在派出第一条 Case 之前返回：声明冲突、池没有容量、单条所需额度超过生效上限
// 都不允许降级为"先跑起来再说"。
func ResolveConcurrencyLimits(cases []Case, policy ConcurrencyPolicy) (ConcurrencyLimits, error) {
	limits := ConcurrencyLimits{
		packageRun:      policy.PackageLimit,
		packages:        make(map[string]int),
		pools:           make(map[string]int),
		packageDeclared: make(map[string]int),
		poolDeclared:    make(map[string]int),
	}
	if policy.PackageLimit < 0 {
		return limits, fmt.Errorf("运行级包级上限不能为负，当前为 %d", policy.PackageLimit)
	}
	participating := make(map[string]struct{})
	for _, c := range cases {
		if err := c.Concurrency.validate(); err != nil {
			return limits, fmt.Errorf("用例 %q 的并发声明无效: %w", c.ID, err)
		}
		pkg := strings.TrimSpace(c.Package)
		if pkg == "" {
			if c.Concurrency.PackageLimit > 0 {
				return limits, fmt.Errorf("用例 %q 声明了包级上限但缺少 Package", c.ID)
			}
		} else {
			if _, exists := limits.packages[pkg]; !exists {
				limits.packages[pkg] = 0
			}
			if limit := c.Concurrency.PackageLimit; limit > 0 {
				if err := claimQuota(limits.packageDeclared, pkg, limit, "包"); err != nil {
					return limits, err
				}
			}
		}
		for _, name := range sortedKeys(c.Concurrency.Pools) {
			participating[name] = struct{}{}
			if limit := c.Concurrency.Pools[name]; limit > 0 {
				if err := claimQuota(limits.poolDeclared, name, limit, "命名池"); err != nil {
					return limits, err
				}
			}
		}
	}

	for _, pkg := range sortedKeys(limits.packages) {
		declared := limits.packageDeclared[pkg]
		effective := declared
		if limits.packageRun > 0 {
			effective = limits.packageRun
			if declared > 0 && declared != limits.packageRun {
				limits.warnf(
					"包 %q 的声明上限 %d 被运行级 --package-limit %d 取代（生效 %d）",
					pkg,
					declared,
					limits.packageRun,
					effective,
				)
			}
		}
		limits.packages[pkg] = effective
	}

	for _, name := range sortedNames(participating) {
		declared := limits.poolDeclared[name]
		effective := declared
		if limit, ok := policy.Pools[name]; ok {
			if limit < 0 {
				return limits, fmt.Errorf("运行级命名池 %q 的容量不能为负，当前为 %d", name, limit)
			}
			if limit > 0 {
				effective = limit
				if declared > 0 && declared != limit {
					limits.warnf(
						"命名池 %q 的声明容量 %d 被运行级 --pool 取代（生效 %d）",
						name,
						declared,
						effective,
					)
				}
			}
		}
		if effective <= 0 {
			return limits, fmt.Errorf(
				"命名池 %q 没有容量声明：用 --pool %s=<容量> 指定，或让其中一条用例声明容量",
				name,
				name,
			)
		}
		limits.pools[name] = effective
	}
	for _, name := range sortedKeys(policy.Pools) {
		if _, used := participating[name]; !used {
			limits.warnf("运行级命名池 %q 没有被本次选择的用例使用", name)
		}
	}
	return limits, nil
}

// ValidateConcurrencyPolicy 校验命令行给出的运行级额度，供入口在派发前判定参数错误。
func ValidateConcurrencyPolicy(policy ConcurrencyPolicy, cases []Case) error {
	if policy.Parallel < 0 {
		return fmt.Errorf("--parallel 不能为负，当前为 %d", policy.Parallel)
	}
	if policy.PackageLimit < 0 {
		return fmt.Errorf("--package-limit 不能为负，当前为 %d", policy.PackageLimit)
	}
	declared := make(map[string]struct{})
	for _, c := range cases {
		for name := range c.Concurrency.Pools {
			declared[name] = struct{}{}
		}
	}
	for _, name := range sortedKeys(policy.Pools) {
		limit := policy.Pools[name]
		if limit < 1 {
			return fmt.Errorf("命名池 %q 的容量必须大于 0，当前为 %d", name, limit)
		}
		if _, ok := declared[name]; !ok {
			return fmt.Errorf("命名池 %q 没有被任何用例声明（--pool 名称拼写错误？）", name)
		}
	}
	return nil
}

// ParsePoolSpec 解析 --pool 的取值 name=limit。
func ParsePoolSpec(value string) (string, int, error) {
	name, raw, found := strings.Cut(strings.TrimSpace(value), "=")
	if !found {
		return "", 0, fmt.Errorf("命名池声明 %q 必须形如 name=limit", value)
	}
	name = strings.TrimSpace(name)
	if !poolNamePattern.MatchString(name) {
		return "", 0, fmt.Errorf("命名池名称 %q 只允许字母、数字与 . _ : - ", name)
	}
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return "", 0, fmt.Errorf("命名池 %q 的容量 %q 不是整数", name, strings.TrimSpace(raw))
	}
	if limit < 1 {
		return "", 0, fmt.Errorf("命名池 %q 的容量必须大于 0，当前为 %d", name, limit)
	}
	return name, limit, nil
}

// QuotaReport 是一个额度条目（包级上限或命名池）的观测结论（D-08）。
type QuotaReport struct {
	// Name 是包路径或池名。
	Name string `json:"name"`
	// Limit 是生效上限；包级 0 表示不限。
	Limit int `json:"limit"`
	// DeclaredLimit 是用例声明值；没有声明时为 0。
	DeclaredLimit int `json:"declared_limit"`
	// Peak 是该额度下观测到的最大在跑用例数。
	Peak int `json:"peak"`
	// Completed 是在该额度下结束的用例数（含失败与取消）。
	Completed int `json:"completed"`
	// QueuedTotalMS 是被该额度阻塞过的用例排队时长之和。
	QueuedTotalMS int64 `json:"queued_ms_total"`
	// QueuedMaxMS 是其中最长的一次排队时长。
	QueuedMaxMS int64 `json:"queued_ms_max"`
	// Undispatched 是因调度约束从未执行的用例数。
	Undispatched int `json:"undispatched"`
	// UndispatchedReason 是未派发原因；没有未派发用例时为空。
	UndispatchedReason string `json:"undispatched_reason,omitempty"`
}

// quotaSeed 是一个需要在报告里出现的额度条目：本次选择的用例出现过就必须有条目（I9）。
type quotaSeed struct {
	kind     string
	name     string
	limit    int
	declared int
}

// seeds 返回本次运行出现过的全部额度条目，按类别与名称排序，保证报告顺序确定。
func (l ConcurrencyLimits) seeds() []quotaSeed {
	seeds := make([]quotaSeed, 0, len(l.packages)+len(l.pools))
	for _, pkg := range sortedKeys(l.packages) {
		seeds = append(seeds, quotaSeed{
			kind:     quotaKindPackage,
			name:     pkg,
			limit:    l.packages[pkg],
			declared: l.packageDeclared[pkg],
		})
	}
	for _, name := range sortedKeys(l.pools) {
		seeds = append(seeds, quotaSeed{
			kind:     quotaKindPool,
			name:     name,
			limit:    l.pools[name],
			declared: l.poolDeclared[name],
		})
	}
	return seeds
}

// packageLimitOf 返回该包的生效上限；0 表示不限，负数或未知包按 0 处理。
func (l ConcurrencyLimits) packageLimitOf(pkg string) int {
	return l.packages[pkg]
}

// poolLimitOf 返回该命名池的生效容量；0 表示不限（未声明容量由预检拦下）。
func (l ConcurrencyLimits) poolLimitOf(name string) int {
	return l.pools[name]
}

// Warnings 返回声明值与生效值不一致等需要人工核对的情况（D-04）。
func (l ConcurrencyLimits) Warnings() []string {
	return append([]string(nil), l.warnings...)
}

// warnf 记录一条 WARN：只在编排进程的日志与报告里留痕，不改变调度结果。
func (l *ConcurrencyLimits) warnf(format string, args ...any) {
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}

// claimQuota 登记一个非零额度声明：同名的不同非零声明直接在注册期 / 预检期失败。
func claimQuota(declared map[string]int, name string, limit int, kind string) error {
	if existing, ok := declared[name]; ok && existing != limit {
		return fmt.Errorf("%s %q 的并发额度声明冲突：%d 与 %d 必须一致", kind, name, existing, limit)
	}
	declared[name] = limit
	return nil
}

// 注册期额度登记：同一个包或同一个池的非零声明必须一致（R2 §3 的注册期硬失败）。
var (
	registryPackageLimits = make(map[string]int)
	registryPoolLimits    = make(map[string]int)
)

// recordConcurrencyDecl 在注册期登记额度声明；冲突时返回错误，由注册表一次性报告。
func recordConcurrencyDecl(c Case) error {
	pkg := strings.TrimSpace(c.Package)
	packageLimit := c.Concurrency.PackageLimit
	if packageLimit > 0 {
		if existing, ok := registryPackageLimits[pkg]; ok && existing != packageLimit {
			return fmt.Errorf(
				"包 %q 的并发额度声明冲突：用例 %q 声明 %d，先前已声明 %d",
				pkg,
				c.ID,
				packageLimit,
				existing,
			)
		}
	}
	poolLimits := make(map[string]int, len(c.Concurrency.Pools))
	for _, name := range sortedKeys(c.Concurrency.Pools) {
		limit := c.Concurrency.Pools[name]
		if limit <= 0 {
			continue
		}
		if existing, ok := registryPoolLimits[name]; ok && existing != limit {
			return fmt.Errorf(
				"命名池 %q 的容量声明冲突：用例 %q 声明 %d，先前已声明 %d",
				name,
				c.ID,
				limit,
				existing,
			)
		}
		poolLimits[name] = limit
	}
	if packageLimit > 0 {
		registryPackageLimits[pkg] = packageLimit
	}
	for name, limit := range poolLimits {
		registryPoolLimits[name] = limit
	}
	return nil
}

// sortedNames 把集合里的名称排序，避免 map 迭代顺序影响错误信息与报告。
func sortedNames(values map[string]struct{}) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
