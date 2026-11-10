package testrunner

import (
	"context"
	"fmt"
	"strings"
)

// ProviderOptions 是编排进程构造默认提供者集合所需的运行期输入。
type ProviderOptions struct {
	// Root 是本次运行的资源工作根目录，位于 <run-dir>/resources 下。
	Root string
	// RunID 是本次运行标识，用于远程容器命名与远程测试目录。
	RunID string
	// SelfExecutable 是当前编排进程的可执行文件，供 child-process 的默认常驻子进程使用。
	SelfExecutable string
	// Hosts 是远程提供者使用的真实主机配置，键为 host_id；R4.5 试点负责装载。
	Hosts map[string]RemoteHost
}

// DefaultProviders 按冻结契约构造 R2 的提供者集合。
//
// 前端三类资源（node-worker / frontend-dev-server / browser）在 R6 补齐，本阶段没有
// 提供者：声明它们的用例在分配阶段失败，不会退化成空句柄。
func DefaultProviders(opts ProviderOptions) (*Providers, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, fmt.Errorf("资源提供者缺少工作根目录")
	}
	hosts := newRemoteHosts(opts.Hosts)
	providers := NewProviders()
	entries := []struct {
		resourceType string
		provider     Provider
	}{
		{ResourceLocalPort, NewLocalPortProvider()},
		{ResourceSQLiteDB, NewSQLiteProvider(root)},
		{ResourceLocalHTTPServer, NewLocalHTTPServerProvider()},
		{ResourceChildProcess, NewChildProcessProvider(opts.SelfExecutable)},
		{ResourceServiceProcess, NewServiceProcessProvider(opts.SelfExecutable)},
		{ResourceGitWorktree, NewGitWorktreeProvider(root)},
		{ResourceLocalSSHServer, NewLocalSSHServerProvider(root)},
		{ResourceSSHHost, NewSSHHostProvider(hosts)},
		{ResourceSSHSession, NewSSHSessionProvider(hosts)},
		{ResourceDockerDaemon, NewDockerDaemonProvider(hosts)},
		{ResourceRemoteContainer, NewRemoteContainerProvider(hosts, opts.RunID)},
		{ResourceRemoteWorkspace, NewRemoteWorkspaceProvider(hosts, opts.RunID)},
	}
	for _, entry := range entries {
		if err := providers.Register(entry.resourceType, entry.provider); err != nil {
			return nil, err
		}
	}
	return providers, nil
}

// missingProviderError 返回未登记提供者的错误说明。
func missingProviderError(resourceType string) error {
	switch resourceType {
	case ResourceNodeWorker, ResourceFrontendDevServer, ResourceBrowser:
		return fmt.Errorf("资源类型 %q 的提供者由 R6（前端适配器）补齐", resourceType)
	default:
		return fmt.Errorf("资源类型 %q 没有已登记的提供者", resourceType)
	}
}

// FaultInjection 是只用于自检的故障注入，正常运行全部保持零值。
//
// 注入只能由 --selfcheck-* 参数打开；注入生效时资源结论、用例警告、取消原因与
// residual_check 的原因都会写明注入来源，避免被当成真实缺陷。
type FaultInjection struct {
	// FailReleaseType 是要注入释放失败的类型，空值表示不注入。
	FailReleaseType string
	// CancelOnCase 让调度器在派发该用例后立即取消整次运行，空值表示不注入。
	CancelOnCase string
	// LateResult 让编排进程为已定型的 timeout / error 用例写出伪造的迟到结果文件。
	LateResult bool
}

// applyFaultInjection 包装指定类型的提供者，使它的释放与自证必定失败。
func (p *Providers) applyFaultInjection(injection FaultInjection) error {
	resourceType := strings.TrimSpace(injection.FailReleaseType)
	if resourceType == "" {
		return nil
	}
	inner, err := p.ProviderFor(resourceType)
	if err != nil {
		return fmt.Errorf("故障注入目标无效: %w", err)
	}
	p.byType[resourceType] = &failingReleaseProvider{
		inner:  inner,
		reason: "自检故障注入 --selfcheck-fail-release=" + resourceType,
	}
	return nil
}

// failingReleaseProvider 让释放与自证必定失败，用于验证零残留终检本身。
type failingReleaseProvider struct {
	inner  Provider
	reason string
}

// Acquire 交给内层提供者，故障只体现在释放阶段。
func (f *failingReleaseProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	return f.inner.Acquire(ctx, decl)
}

// Release 必定失败，并让内层资源保持存活，从而在终检中表现为残留。
func (f *failingReleaseProvider) Release(ctx context.Context, handle Handle) error {
	return fmt.Errorf("%s：拒绝释放资源 %q", f.reason, handle.Name)
}

// Verify 必定失败：注入下不得出现"已释放"的结论。
func (f *failingReleaseProvider) Verify(ctx context.Context, handle Handle) error {
	return fmt.Errorf("%s：无法自证资源 %q 已释放", f.reason, handle.Name)
}

// Snapshot 交给内层提供者，残留因此可见。
func (f *failingReleaseProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	return f.inner.Snapshot(ctx)
}
