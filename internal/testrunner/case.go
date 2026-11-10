// Package testrunner 是统一测试执行器（V3）的编排与执行核心。
//
// 编排进程负责选择用例、按作用域分配资源、启动 worker 并把结果汇总成统一报告；每个 Case
// 都在独立 worker 子进程中执行（同一二进制以 --exec-case 启动），因此 panic、os.Exit 与
// 包级全局状态都只影响该 Case。并发只由 scheduler.go 的调度器决定（R3），资源分配与
// 零残留核验由 R2 落地；用例无法通过代码自行扩大并发。
package testrunner

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Case 是一条可被执行的测试用例。
//
// 用例声明是调度器与报告的唯一输入：选择按 ID/Group，并发与资源按 Parallel/Resources，
// 用例主体 Run 只在 worker 子进程中执行。
type Case struct {
	// ID 是全局唯一的用例标识，格式为 "<group>/<name>"，字符集受产物目录约束。
	ID string
	// Group 是选择与并发决策的分组，留空时取 ID 的第一段。
	Group string
	// Timeout 是该用例的执行上限，零值使用运行级默认超时。
	Timeout time.Duration
	// Package 是该用例所属的 Go 测试包目录（例如 tests/integration/local/scheduler）。
	//
	// 它是串行作用域与包级串行声明的输入：非空时串行作用域取该包，R2 冻结的
	// SerialPackageReason 按它判定整包串行；为空时串行作用域退化为 Group。
	Package string
	// Parallel 表示调度器允许该用例与其他用例并行。
	//
	// false 表示该用例在串行作用域内独占执行（对齐 go test 里未标记 t.Parallel 的用例）；
	// 并发最终由调度器决定，用例无法自行扩大或缩小。
	Parallel bool
	// Resources 是用例需要的资源声明，由编排进程分配后以句柄形式注入 worker。
	Resources []ResourceDecl
	// Run 是用例主体，运行在 worker 子进程中；失败与跳过通过 Context 表达。
	Run func(c *Context)
}

// caseIDPattern 约束用例 ID 的字符集：ID 同时是产物目录名的一部分。
var caseIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9/_-]*$`)

// Validate 校验用例声明是否完整，不执行用例主体。
func (c Case) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("用例缺少 ID")
	}
	if !caseIDPattern.MatchString(c.ID) {
		return fmt.Errorf("用例 ID %q 只允许小写字母、数字与 / _ -", c.ID)
	}
	if strings.HasPrefix(c.ID, "/") || strings.HasSuffix(c.ID, "/") || strings.Contains(c.ID, "//") {
		return fmt.Errorf("用例 ID %q 的层级写法不合法", c.ID)
	}
	if groupOf(c.ID) == "" {
		return fmt.Errorf("用例 ID %q 必须形如 <group>/<name>", c.ID)
	}
	if c.Group != "" && c.Group != groupOf(c.ID) {
		return fmt.Errorf("用例 %q 的 Group %q 与 ID 前缀不一致", c.ID, c.Group)
	}
	if c.Run == nil {
		return fmt.Errorf("用例 %q 缺少 Run", c.ID)
	}
	if c.Timeout < 0 {
		return fmt.Errorf("用例 %q 的 Timeout 不能为负", c.ID)
	}
	if strings.ContainsAny(c.Package, " \t\r\n") {
		return fmt.Errorf("用例 %q 的 Package %q 不能包含空白字符", c.ID, c.Package)
	}
	seen := make(map[string]struct{}, len(c.Resources))
	for _, decl := range c.Resources {
		if err := decl.Validate(); err != nil {
			return fmt.Errorf("用例 %q 的资源声明无效: %w", c.ID, err)
		}
		if _, exists := seen[decl.Name]; exists {
			return fmt.Errorf("用例 %q 重复声明资源 %q", c.ID, decl.Name)
		}
		seen[decl.Name] = struct{}{}
	}
	return nil
}

// groupOf 取 ID 的第一段作为分组名。
func groupOf(id string) string {
	if index := strings.Index(id, "/"); index > 0 {
		return id[:index]
	}
	return ""
}

// 注册表保存全部已登记用例；注册错误在 Cases 中一次性暴露，避免坏声明被静默忽略。
var (
	registryMu    sync.Mutex
	registryCases = make(map[string]Case)
	registryErrs  []error
)

// Register 在 init 阶段登记一条用例，返回登记后的 Case，便于用例文件直接持有变量。
//
// 校验失败或 ID 重复不会 panic，也不会覆盖已有用例：错误留在注册表里，由 Cases 统一报告。
func Register(c Case) Case {
	if c.Group == "" {
		c.Group = groupOf(c.ID)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if err := c.Validate(); err != nil {
		registryErrs = append(registryErrs, err)
		return c
	}
	if _, exists := registryCases[c.ID]; exists {
		registryErrs = append(registryErrs, fmt.Errorf("用例 %q 重复注册", c.ID))
		return c
	}
	registryCases[c.ID] = c
	return c
}

// Cases 返回按 ID 排序的全部已注册用例，并汇总注册期间的错误。
func Cases() ([]Case, error) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if len(registryErrs) > 0 {
		return nil, fmt.Errorf("用例注册表存在 %d 个错误: %w", len(registryErrs), registryErrs[0])
	}
	cases := make([]Case, 0, len(registryCases))
	for _, c := range registryCases {
		cases = append(cases, c)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return cases, nil
}

// Lookup 按 ID 精确查找用例。
func Lookup(id string) (Case, error) {
	registryMu.Lock()
	defer registryMu.Unlock()
	c, ok := registryCases[id]
	if !ok {
		return Case{}, fmt.Errorf("未注册用例 %q", id)
	}
	return c, nil
}
