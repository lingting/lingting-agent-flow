package testrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// 组件格式版本：结构变化必须提升版本号，避免旧产物被静默按新结构读取。
const (
	// HandleFileSchemaVersion 是编排进程传给 worker 的资源句柄文件格式。
	HandleFileSchemaVersion = "agentflow-case-handles/1"
)

// 资源类型取值来自 V3 R0 冻结的词汇表（case-resource-matrix.json 的 resource_types）。
const (
	ResourceDockerDaemon      = "docker-daemon"
	ResourceRemoteContainer   = "remote-container"
	ResourceRemoteWorkspace   = "remote-workspace"
	ResourceSSHHost           = "ssh-host"
	ResourceSSHSession        = "ssh-session"
	ResourceLocalSSHServer    = "local-ssh-server"
	ResourcePiCLI             = "pi-cli"
	ResourceSQLiteDB          = "sqlite-db"
	ResourceLocalPort         = "local-port"
	ResourceLocalHTTPServer   = "local-http-server"
	ResourceChildProcess      = "child-process"
	ResourceServiceProcess    = "service-process"
	ResourceGitWorktree       = "git-worktree"
	ResourceNodeWorker        = "node-worker"
	ResourceFrontendDevServer = "frontend-dev-server"
	ResourceBrowser           = "browser"
)

// 资源作用域：决定资源由整次运行共享，还是为单个用例独享。
const (
	// ScopeRunShared 表示一次运行内同一声明只分配一次，多个用例复用同一份资源。
	ScopeRunShared = "run-shared"
	// ScopeCaseExclusive 表示每个用例各分配一份，用完立即释放。
	ScopeCaseExclusive = "case-exclusive"
)

// resourceTypes 是冻结的词汇表，声明未列出的类型直接判为无效。
var resourceTypes = map[string]struct{}{
	ResourceDockerDaemon:      {},
	ResourceRemoteContainer:   {},
	ResourceRemoteWorkspace:   {},
	ResourceSSHHost:           {},
	ResourceSSHSession:        {},
	ResourceLocalSSHServer:    {},
	ResourcePiCLI:             {},
	ResourceSQLiteDB:          {},
	ResourceLocalPort:         {},
	ResourceLocalHTTPServer:   {},
	ResourceChildProcess:      {},
	ResourceServiceProcess:    {},
	ResourceGitWorktree:       {},
	ResourceNodeWorker:        {},
	ResourceFrontendDevServer: {},
	ResourceBrowser:           {},
}

// ResourceTypes 返回冻结词汇表的全部资源类型，按名称排序。
func ResourceTypes() []string {
	types := make([]string, 0, len(resourceTypes))
	for name := range resourceTypes {
		types = append(types, name)
	}
	sort.Strings(types)
	return types
}

// ResourceDecl 是一条资源声明：它只描述需求，实际句柄由编排进程的 Provider 分配。
type ResourceDecl struct {
	// Name 是用例内唯一的声明名，句柄按它回填。
	Name string
	// Type 是冻结词汇表中的资源类型。
	Type string
	// Scope 是 run-shared 或 case-exclusive。
	Scope string
}

// Validate 校验资源声明是否落在冻结词汇表内。
func (d ResourceDecl) Validate() error {
	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf("资源声明缺少名称")
	}
	if _, ok := resourceTypes[d.Type]; !ok {
		return fmt.Errorf("资源 %q 的类型 %q 不在冻结词汇表内", d.Name, d.Type)
	}
	switch d.Scope {
	case ScopeRunShared, ScopeCaseExclusive:
	default:
		return fmt.Errorf("资源 %q 的作用域 %q 不合法", d.Name, d.Scope)
	}
	return nil
}

// Handle 是一个已分配资源的句柄，也是跨进程传递的唯一形态。
//
// 句柄只携带可序列化的描述信息：Payload 的键由资源类型约定（例如 local-port 的 address、
// remote-container 的 container_id/host、sqlite-db 的 path），R2 逐类型补齐键名。
type Handle struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Scope       string            `json:"scope"`
	Payload     map[string]string `json:"payload,omitempty"`
	Fingerprint string            `json:"fingerprint,omitempty"`
}

// PayloadValue 读取句柄的自描述键；缺失时返回空字符串。
func (h Handle) PayloadValue(key string) string {
	return h.Payload[key]
}

// HandleFile 是编排进程写给 worker 的句柄文件内容。
type HandleFile struct {
	SchemaVersion string   `json:"schema_version"`
	RunID         string   `json:"run_id"`
	CaseID        string   `json:"case_id"`
	Handles       []Handle `json:"handles"`
}

// WriteHandleFile 以原子方式写出句柄文件。
func WriteHandleFile(path string, file HandleFile) error {
	file.SchemaVersion = HandleFileSchemaVersion
	if err := writeJSONFile(path, file); err != nil {
		return fmt.Errorf("写出资源句柄文件失败: %w", err)
	}
	return nil
}

// ReadHandleFile 读取并校验句柄文件外壳。
func ReadHandleFile(path string) (HandleFile, error) {
	var file HandleFile
	data, err := os.ReadFile(path)
	if err != nil {
		return file, fmt.Errorf("读取资源句柄文件失败: %w", err)
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return file, fmt.Errorf("解析资源句柄文件失败: %w", err)
	}
	if file.SchemaVersion != HandleFileSchemaVersion {
		return file, fmt.Errorf(
			"资源句柄文件版本 %q 与当前版本 %q 不一致",
			file.SchemaVersion,
			HandleFileSchemaVersion,
		)
	}
	return file, nil
}

// VerifyHandles 校验句柄是否覆盖用例的全部声明：缺失或类型/作用域不符一律失败，
// 多余句柄不影响执行但会作为警告返回，避免资源被静默错配。
func VerifyHandles(c Case, handles []Handle) ([]string, error) {
	byName := make(map[string]Handle, len(handles))
	for _, handle := range handles {
		if _, exists := byName[handle.Name]; exists {
			return nil, fmt.Errorf("资源句柄 %q 重复出现", handle.Name)
		}
		byName[handle.Name] = handle
	}
	for _, decl := range c.Resources {
		handle, ok := byName[decl.Name]
		if !ok {
			return nil, fmt.Errorf("用例 %q 缺少资源 %q 的句柄", c.ID, decl.Name)
		}
		if handle.Type != decl.Type {
			return nil, fmt.Errorf(
				"用例 %q 的资源 %q 类型为 %q，声明要求 %q",
				c.ID,
				decl.Name,
				handle.Type,
				decl.Type,
			)
		}
		if handle.Scope != decl.Scope {
			return nil, fmt.Errorf(
				"用例 %q 的资源 %q 作用域为 %q，声明要求 %q",
				c.ID,
				decl.Name,
				handle.Scope,
				decl.Scope,
			)
		}
		delete(byName, decl.Name)
	}
	warnings := make([]string, 0, len(byName))
	for name := range byName {
		warnings = append(warnings, fmt.Sprintf("忽略未声明的资源句柄 %q", name))
	}
	sort.Strings(warnings)
	return warnings, nil
}

// Provider 为一种资源类型实现分配与释放。
//
// R1 只内置 local-port；其余类型在 R2 起按冻结词汇表逐个实现，未注册的类型在分配阶段
// 直接失败，不会退化成空句柄。
type Provider interface {
	// Acquire 分配一份资源并返回可序列化的句柄。
	Acquire(ctx context.Context, decl ResourceDecl) (Handle, error)
	// Release 释放句柄对应的资源，必须可重复调用。
	Release(ctx context.Context, handle Handle) error
}

// Providers 是资源类型到提供者的映射，由编排进程持有。
type Providers struct {
	byType map[string]Provider
}

// NewProviders 创建一个空提供者集合。
func NewProviders() *Providers {
	return &Providers{byType: make(map[string]Provider)}
}

// Register 为资源类型登记提供者，重复登记直接失败。
func (p *Providers) Register(resourceType string, provider Provider) error {
	if _, ok := resourceTypes[resourceType]; !ok {
		return fmt.Errorf("资源类型 %q 不在冻结词汇表内", resourceType)
	}
	if provider == nil {
		return fmt.Errorf("资源类型 %q 的提供者为空", resourceType)
	}
	if _, exists := p.byType[resourceType]; exists {
		return fmt.Errorf("资源类型 %q 已登记提供者", resourceType)
	}
	p.byType[resourceType] = provider
	return nil
}

// ProviderFor 返回资源类型的提供者，未登记时给出明确错误。
func (p *Providers) ProviderFor(resourceType string) (Provider, error) {
	provider, ok := p.byType[resourceType]
	if !ok {
		return nil, fmt.Errorf("资源类型 %q 尚未提供 R1 分配实现（R2 补齐）", resourceType)
	}
	return provider, nil
}

// DefaultProviders 返回 R1 内置的提供者集合：目前只有 local-port。
func DefaultProviders() (*Providers, error) {
	providers := NewProviders()
	if err := providers.Register(ResourceLocalPort, NewLocalPortProvider()); err != nil {
		return nil, err
	}
	return providers, nil
}
