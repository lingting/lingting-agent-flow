package testrunner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	message := fmt.Sprintf(format, args...)
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

// Skipf 记录跳过原因并立即结束用例主体，等价于 testing.T.Skipf。
func (c *Context) Skipf(format string, args ...any) {
	c.mu.Lock()
	c.skipReason = fmt.Sprintf(format, args...)
	c.mu.Unlock()
	panic(fatalSignal{skip: true})
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
