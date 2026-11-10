package testrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 组件格式版本：结构变化必须提升版本号，避免旧产物被静默按新结构读取。
const (
	// HandleFileSchemaVersion 是编排进程传给 worker 的资源句柄文件格式。
	HandleFileSchemaVersion = "agentflow-case-handles/1"
)

// 资源类型取值来自 V3 R0 冻结的词汇表（case-resource-matrix.json 的 resource_types）；
// 随本机 Pi 测试废弃的 pi-cli 已从词汇表移除。
const (
	ResourceDockerDaemon      = "docker-daemon"
	ResourceRemoteContainer   = "remote-container"
	ResourceRemoteWorkspace   = "remote-workspace"
	ResourceSSHHost           = "ssh-host"
	ResourceSSHSession        = "ssh-session"
	ResourceLocalSSHServer    = "local-ssh-server"
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
// ResourceDecl 是一条资源声明：它只描述需求，实际句柄由编排进程的 Provider 分配。
type ResourceDecl struct {
	// Name 是用例内唯一的声明名，句柄按它回填。
	Name string
	// Type 是冻结词汇表中的资源类型。
	Type string
	// Scope 是 run-shared 或 case-exclusive。
	Scope string
	// Spec 是该类型的创建参数，键名由冻结的 payload 契约给出（V3 R2 §5）。
	//
	// 契约外的键在声明阶段失败；必填键缺失在分配阶段失败，不会静默回退成默认实例。
	Spec map[string]string
}

// Validate 校验资源声明是否落在冻结词汇表与 payload 契约内。
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
	contract, err := ResourceContractFor(d.Type)
	if err != nil {
		return fmt.Errorf("资源 %q 的契约无效: %w", d.Name, err)
	}
	if err := contract.validateSpecKeys(d); err != nil {
		return err
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
		contract, err := ResourceContractFor(decl.Type)
		if err != nil {
			return nil, err
		}
		if err := contract.validateHandle(c.ID, handle); err != nil {
			return nil, err
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

// Provider 为一种资源类型实现分配、释放与自证。
//
// R1 只内置 local-port；R2 起按冻结词汇表逐个实现本机与远程提供者，未注册的类型在
// 分配阶段直接失败，不会退化成空句柄。四个方法都必须如实反映观测结果：无法确认时
// 返回错误，不得用"已释放"冒充成功。
type Provider interface {
	// Acquire 分配一份资源并返回可序列化的句柄。
	Acquire(ctx context.Context, decl ResourceDecl) (Handle, error)
	// Release 释放句柄对应的资源，必须可重复调用。
	Release(ctx context.Context, handle Handle) error
	// Verify 在释放后自证资源确实消失；无法确认时返回错误。
	Verify(ctx context.Context, handle Handle) error
	// Snapshot 返回提供者当前仍持有的资源（含外部观测到的残留），供运行级终检比对。
	Snapshot(ctx context.Context) ([]LiveResource, error)
}

// LiveResource 是一条仍然存活、需要被零残留核验收敛的资源记录。
type LiveResource struct {
	// Handle 是资源的句柄形态；外部观测到的残留可以只填 Name 与 Type。
	Handle Handle
	// Detail 是观测方式与关键输出摘要，写入报告便于人工复核。
	Detail string
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
		return nil, missingProviderError(resourceType)
	}
	return provider, nil
}

// registeredTypes 返回已登记提供者的资源类型，按名称排序。
func (p *Providers) registeredTypes() []string {
	types := make([]string, 0, len(p.byType))
	for resourceType := range p.byType {
		types = append(types, resourceType)
	}
	sort.Strings(types)
	return types
}

// ProcessLogReporter 由会把子进程输出写入文件、并能给出文件路径的提供者实现。
//
// 输出落盘是证据的一部分：用例结束后（尤其是失败与取消）必须能读回被管理子进程的输出，
// 不能只留在编排进程的终端上。
type ProcessLogReporter interface {
	// ProcessLogPath 返回该句柄对应子进程的输出文件路径；没有记录时返回空字符串。
	ProcessLogPath(handle Handle) string
}

// caseWorkspaceKey 是编排进程传给提供者的用例工作空间目录的上下文键。
type caseWorkspaceKey struct{}

// withCaseWorkspace 把当前用例的工作空间目录放进资源分配上下文。
//
// 分配上下文本来就随用例走（取消由它传播），把用例工作目录挂在同一处，提供者就能在不修改
// 资源契约的前提下决定子进程的工作目录与输出位置。
func withCaseWorkspace(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, caseWorkspaceKey{}, dir)
}

// caseWorkspaceOf 返回资源分配上下文里的用例工作空间目录，未设置时为空字符串。
//
// 没有工作空间时提供者按"未配置"处理：不回退到仓库目录或用户目录，避免把文件写到产物之外。
func caseWorkspaceOf(ctx context.Context) string {
	dir, _ := ctx.Value(caseWorkspaceKey{}).(string)
	return dir
}

// resourceShardDir 返回某个资源在运行工作空间内的存放目录（V3 R4 §5 D6）。
//
// 需要独立目录的提供者把资源文件放在 <root>/shard_<资源类型>_<声明名> 下：目录名同时表达
// 类型与声明名，多个资源不会互相覆盖，也不必为每种类型再建一层中间目录。
func resourceShardDir(root, resourceType, resourceID string) string {
	return filepath.Join(root, "shard_"+resourceType+"_"+safeName(resourceID))
}
