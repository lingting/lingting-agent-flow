package testrunner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// fatalSignal 让 Fatalf/Skipf 立即结束用例主体，同时保留已记录的结论。
type fatalSignal struct {
	skip bool
}

// Error 实现 error，便于 recover 后统一识别。
func (f fatalSignal) Error() string {
	return "用例被提前终止"
}

// testrunnerPackagePrefix 是本包全部函数的名称前缀，失败定位会跳过这些栈帧。
const testrunnerPackagePrefix = "github.com/lingting/agentflow/internal/testrunner."

// Context 是 Case 的执行上下文，是 testing.T 能力的最小替代集合。
//
// 用例只能通过它写日志、记录失败、跳过与读取注入的资源句柄；日志写入 worker 独占的
// stdout，因此不同用例的输出天然分流到各自产物目录。
type Context struct {
	caseID          string
	artifactDir     string
	workspaceDir    string
	realTests       bool
	realTestsReason string
	ctx             context.Context
	handles         map[string]Handle

	mu         sync.Mutex
	failures   []string
	skipReason string
	warnings   []string
	tempSeq    int
	// cleanups 是用例结束时要执行的清理函数，按注册顺序保存、按相反顺序执行。
	cleanups []func()
	// helpers 是通过 Helper 标记的辅助函数名，失败定位会跳过这些函数。
	helpers map[string]struct{}
}

// newContext 构造 worker 内的执行上下文；worker 是唯一调用方。
func newContext(ctx context.Context, c Case, req WorkerRequest, handles []Handle) *Context {
	byName := make(map[string]Handle, len(handles))
	for _, handle := range handles {
		byName[handle.Name] = handle
	}
	return &Context{
		caseID:          c.ID,
		artifactDir:     req.ArtifactDir,
		workspaceDir:    req.WorkspaceDir,
		realTests:       req.RealTests,
		realTestsReason: req.RealTestsReason,
		ctx:             ctx,
		handles:         byName,
	}
}

// Name 返回当前用例 ID。
func (c *Context) Name() string {
	return c.caseID
}

// ArtifactDir 返回该用例的独占产物目录，目录已存在。
func (c *Context) ArtifactDir() string {
	return c.artifactDir
}

// WorkspaceDir 返回该用例的工作空间目录，目录已由编排进程创建。
//
// 工作空间是被测进程与用例共享的可写目录：真实服务进程以它作为工作目录，用例在这里读写
// 配置、数据库与日志。它位于系统临时目录下的运行工作空间根目录内，不在产物目录里。
func (c *Context) WorkspaceDir() string {
	return c.workspaceDir
}

// TempDir 在产物目录下创建一个临时目录，随产物保留以便排查失败现场。
func (c *Context) TempDir() (string, error) {
	c.mu.Lock()
	c.tempSeq++
	seq := c.tempSeq
	c.mu.Unlock()
	path := filepath.Join(c.artifactDir, "tmp", fmt.Sprintf("%03d", seq))
	if err := os.MkdirAll(path, 0o750); err != nil {
		return "", fmt.Errorf("创建用例临时目录失败: %w", err)
	}
	return path, nil
}

// Logf 把一行日志写入 worker 的 stdout。
func (c *Context) Logf(format string, args ...any) {
	fmt.Fprintf(os.Stdout, format+"\n", args...)
}

// Warnf 记录一条不影响结论的警告，并立即写日志。
func (c *Context) Warnf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	c.mu.Lock()
	c.warnings = append(c.warnings, message)
	c.mu.Unlock()
	c.Logf("WARN %s", message)
}

// Errorf 记录一次失败，用例可以继续执行后续断言。
func (c *Context) Errorf(format string, args ...any) {
	c.recordFailure(fmt.Sprintf(format, args...))
}

// recordFailure 追加一条失败，并在 worker 的标准错误里给出调用位置。
//
// 调用位置跳过 Helper 标记的辅助函数与 testrunner 自身，因此定位指向真正的断言行。
func (c *Context) recordFailure(message string) {
	if site := c.callSite(); site != "" {
		message = site + ": " + message
	}
	c.mu.Lock()
	c.failures = append(c.failures, message)
	c.mu.Unlock()
	fmt.Fprintf(os.Stderr, "FAIL %s: %s\n", c.caseID, message)
}

// Fatalf 记录失败并立即结束用例主体，等价于 testing.T.Fatalf。
func (c *Context) Fatalf(format string, args ...any) {
	c.Errorf(format, args...)
	panic(fatalSignal{})
}

// Fatal 记录失败并立即结束用例主体，等价于 testing.T.Fatal。
func (c *Context) Fatal(args ...any) {
	c.Errorf("%s", fmt.Sprint(args...))
	panic(fatalSignal{})
}

// Skipf 记录跳过原因并立即结束用例主体，等价于 testing.T.Skipf。
func (c *Context) Skipf(format string, args ...any) {
	c.mu.Lock()
	c.skipReason = fmt.Sprintf(format, args...)
	c.mu.Unlock()
	panic(fatalSignal{skip: true})
}

// Skip 记录跳过原因并立即结束用例主体，等价于 testing.T.Skip。
func (c *Context) Skip(args ...any) {
	c.Skipf("%s", fmt.Sprint(args...))
}

// Failed 报告用例是否已经记录过失败。
func (c *Context) Failed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.failures) > 0
}

// Failures 返回已记录的失败信息，按记录顺序。
func (c *Context) Failures() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.failures...)
}

// Skipped 返回用例是否已记录跳过及其原因。
func (c *Context) Skipped() (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.skipReason != "", c.skipReason
}

// Warnings 返回已记录的警告，按记录顺序。
func (c *Context) Warnings() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.warnings...)
}

// Resource 按声明名读取注入的资源句柄。
func (c *Context) Resource(name string) (Handle, bool) {
	handle, ok := c.handles[name]
	return handle, ok
}

// Resources 返回全部注入句柄，按名称排序。
func (c *Context) Resources() []Handle {
	handles := make([]Handle, 0, len(c.handles))
	for _, handle := range c.handles {
		handles = append(handles, handle)
	}
	sort.Slice(handles, func(i, j int) bool { return handles[i].Name < handles[j].Name })
	return handles
}

// RealTestsAllowed 返回编排进程给出的真实依赖授权结论与原因。
//
// 授权只在编排进程判定一次并下发给 worker，避免测试侧再出现第二套门禁。
func (c *Context) RealTestsAllowed() (bool, string) {
	return c.realTests, c.realTestsReason
}

// Context 返回随用例超时结束的上下文，供用例发起可取消的外部调用。
func (c *Context) Context() context.Context {
	return c.ctx
}

// Deadline 返回用例的执行截止时间。
func (c *Context) Deadline() time.Time {
	deadline, _ := c.ctx.Deadline()
	return deadline
}

// Run 在用例内执行一个顺序子测试，语义对齐 testing.T.Run 的单层顺序形态。
//
// 统一执行器没有独立子测试结论（V3 R5 D6：表驱动子测试按 Case 登记），子测试名只
// 写入日志，结论仍属于整条 Case。子测试内部的 Fatalf 只结束该子测试，后面的子测试
// 继续执行；Skipf 只跳过该子测试，不会让整条 Case 变成跳过；其余 panic 原样外抛，
// 由 worker 判为 panic。
func (c *Context) Run(name string, run func(*Context)) {
	if run == nil {
		return
	}
	c.Logf("SUBTEST %s", name)
	c.mu.Lock()
	previousSkip := c.skipReason
	c.mu.Unlock()
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		signal, ok := recovered.(fatalSignal)
		if !ok {
			panic(recovered)
		}
		if signal.skip {
			c.mu.Lock()
			reason := c.skipReason
			c.skipReason = previousSkip
			c.mu.Unlock()
			c.Logf("SUBTEST SKIP %s: %s", name, reason)
		}
	}()
	run(c)
}

// Helper 把调用它的函数标记为辅助函数，等价于 testing.T.Helper。
//
// 失败定位会跳过这些函数，指向真正做断言的位置；辅助函数自己调用 Helper 是唯一的
// 声明方式，用例侧不需要额外的登记。
func (c *Context) Helper() {
	pc, _, _, ok := runtime.Caller(1)
	if !ok {
		return
	}
	function := runtime.FuncForPC(pc)
	if function == nil {
		return
	}
	c.mu.Lock()
	if c.helpers == nil {
		c.helpers = make(map[string]struct{})
	}
	c.helpers[function.Name()] = struct{}{}
	c.mu.Unlock()
}

// Cleanup 注册用例结束时执行的清理函数，等价于 testing.T.Cleanup。
//
// 清理按注册顺序的相反顺序执行；用例以失败、跳过或 panic 结束时同样执行。清理函数
// 记录失败会把用例判为失败，清理函数 panic 只记为该清理失败，不影响其余清理。
func (c *Context) Cleanup(fn func()) {
	if fn == nil {
		return
	}
	c.mu.Lock()
	c.cleanups = append(c.cleanups, fn)
	c.mu.Unlock()
}

// runCleanups 按后进先出执行已注册的清理函数；worker 在用例主体结束后调用。
func (c *Context) runCleanups() {
	for {
		c.mu.Lock()
		remaining := len(c.cleanups)
		if remaining == 0 {
			c.mu.Unlock()
			return
		}
		fn := c.cleanups[remaining-1]
		c.cleanups = c.cleanups[:remaining-1]
		c.mu.Unlock()
		c.runCleanup(fn)
	}
}

// runCleanup 执行单个清理函数，把 panic 转成一次失败而不外抛。
func (c *Context) runCleanup(fn func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			c.Errorf("清理函数 panic: %v", recovered)
		}
	}()
	fn()
}

// callSite 返回第一条不属于辅助函数、也不属于 testrunner 自身的调用位置。
func (c *Context) callSite() string {
	c.mu.Lock()
	helpers := make(map[string]struct{}, len(c.helpers))
	for name := range c.helpers {
		helpers[name] = struct{}{}
	}
	c.mu.Unlock()
	pcs := make([]uintptr, 24)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	for {
		frame, more := frames.Next()
		if frame.Function != "" && !helperFrame(frame.Function, helpers) {
			return fmt.Sprintf("%s:%d", frame.File, frame.Line)
		}
		if !more {
			return ""
		}
	}
}

// helperFrame 判断一个栈帧是否应当从失败定位里跳过。
func helperFrame(name string, helpers map[string]struct{}) bool {
	if _, ok := helpers[name]; ok {
		return true
	}
	return strings.HasPrefix(name, testrunnerPackagePrefix)
}
