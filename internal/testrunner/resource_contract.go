package testrunner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// ResourceContract 是一种资源类型的冻结契约（V3 R2 §5）。
//
// 契约同时约束两侧：声明 Spec 允许出现哪些创建参数，句柄 payload 必须给出哪些键。
// 新增键必须同时改契约与 .steps/V3/R2/README.md，不允许提供者自行扩展 payload 或让
// 用例传入契约外的 Spec。
type ResourceContract struct {
	// Type 是契约对应的资源类型。
	Type string
	// PayloadKeys 是句柄 payload 的冻结键名，按契约顺序排列。
	PayloadKeys []string
	// PayloadRequired 是 payload 的必填键；缺失时 worker 判 error，不回退成空字符串。
	PayloadRequired []string
	// SpecKeys 是声明 Spec 允许出现的键名。
	SpecKeys []string
	// SpecRequired 是声明 Spec 必须给出的键名；缺失在分配阶段失败。
	SpecRequired []string
	// Fingerprint 是指纹语义说明，空表示该类型不按内容寻址。
	Fingerprint string
}

// resourceContracts 是 15 种资源类型的冻结契约，顺序与 R0 词汇表一致（pi-cli 已移除）。
var resourceContracts = []ResourceContract{
	{
		Type:        ResourceLocalPort,
		PayloadKeys: []string{"address"},
		PayloadRequired: []string{
			"address",
		},
	},
	{
		Type:        ResourceSQLiteDB,
		PayloadKeys: []string{"path"},
		PayloadRequired: []string{
			"path",
		},
	},
	{
		Type:        ResourceLocalHTTPServer,
		PayloadKeys: []string{"base_url"},
		PayloadRequired: []string{
			"base_url",
		},
	},
	{
		Type:        ResourceChildProcess,
		PayloadKeys: []string{"pid", "executable"},
		PayloadRequired: []string{
			"pid",
			"executable",
		},
		SpecKeys: []string{"command", "args"},
	},
	{
		Type:        ResourceServiceProcess,
		PayloadKeys: []string{"pid", "base_url", "binary"},
		PayloadRequired: []string{
			"pid",
			"base_url",
			"binary",
		},
		SpecKeys: []string{"binary", "args", "ready_path", "ready_timeout_ms"},
	},
	{
		Type:        ResourceGitWorktree,
		PayloadKeys: []string{"path", "commit"},
		PayloadRequired: []string{
			"path",
			"commit",
		},
		SpecKeys: []string{"repo", "commit"},
	},
	{
		Type:        ResourceLocalSSHServer,
		PayloadKeys: []string{"address", "host_key", "user", "private_key_path"},
		PayloadRequired: []string{
			"address",
			"host_key",
			"user",
			"private_key_path",
		},
	},
	{
		Type:            ResourceSSHHost,
		PayloadKeys:     []string{"host_id", "address", "user", "port"},
		PayloadRequired: []string{"host_id", "address", "user", "port"},
		SpecKeys:        []string{"host_id"},
		SpecRequired:    []string{"host_id"},
		Fingerprint:     "known_hosts 校验通过的主机密钥指纹",
	},
	{
		Type:            ResourceSSHSession,
		PayloadKeys:     []string{"session_id", "host_id"},
		PayloadRequired: []string{"session_id", "host_id"},
		SpecKeys:        []string{"host_id"},
		SpecRequired:    []string{"host_id"},
	},
	{
		Type:            ResourceDockerDaemon,
		PayloadKeys:     []string{"host_id", "endpoint"},
		PayloadRequired: []string{"host_id", "endpoint"},
		SpecKeys:        []string{"host_id"},
		SpecRequired:    []string{"host_id"},
	},
	{
		Type:            ResourceRemoteContainer,
		PayloadKeys:     []string{"container_id", "host_id"},
		PayloadRequired: []string{"container_id", "host_id"},
		SpecKeys:        []string{"host_id", "image", "command"},
		SpecRequired:    []string{"host_id", "image"},
		Fingerprint:     "镜像 ID（docker image inspect 的 Id）",
	},
	{
		Type:            ResourceRemoteWorkspace,
		PayloadKeys:     []string{"host_id", "path"},
		PayloadRequired: []string{"host_id", "path"},
		SpecKeys:        []string{"host_id", "path"},
		SpecRequired:    []string{"host_id"},
	},
	{
		Type:            ResourceNodeWorker,
		PayloadKeys:     []string{"pid", "runtime"},
		PayloadRequired: []string{"pid", "runtime"},
		Fingerprint:     "R6 冻结",
	},
	{
		Type:            ResourceFrontendDevServer,
		PayloadKeys:     []string{"base_url", "pid"},
		PayloadRequired: []string{"base_url", "pid"},
		Fingerprint:     "R6 冻结",
	},
	{
		Type:            ResourceBrowser,
		PayloadKeys:     []string{"browser", "version", "session_dir"},
		PayloadRequired: []string{"browser", "version", "session_dir"},
		Fingerprint:     "R6 冻结",
	},
}

// contractByType 是契约表的索引，键为资源类型。
var contractByType = func() map[string]ResourceContract {
	index := make(map[string]ResourceContract, len(resourceContracts))
	for _, contract := range resourceContracts {
		index[contract.Type] = contract
	}
	return index
}()

// ResourceContractFor 返回资源类型的冻结契约，未登记的类型直接失败。
func ResourceContractFor(resourceType string) (ResourceContract, error) {
	contract, ok := contractByType[resourceType]
	if !ok {
		return ResourceContract{}, fmt.Errorf("资源类型 %q 没有冻结的 payload 契约", resourceType)
	}
	return contract, nil
}

// ResourceContracts 返回按类型名排序的全部契约，供文档对账与人工核对使用。
func ResourceContracts() []ResourceContract {
	contracts := make([]ResourceContract, 0, len(resourceContracts))
	contracts = append(contracts, resourceContracts...)
	sort.Slice(contracts, func(i, j int) bool { return contracts[i].Type < contracts[j].Type })
	return contracts
}

// validateSpecKeys 校验声明 Spec 没有契约外的键；声明阶段使用，缺失必填键留到分配阶段。
func (c ResourceContract) validateSpecKeys(decl ResourceDecl) error {
	allowed := make(map[string]struct{}, len(c.SpecKeys))
	for _, key := range c.SpecKeys {
		allowed[key] = struct{}{}
	}
	unknown := make([]string, 0, len(decl.Spec))
	for key := range decl.Spec {
		if _, ok := allowed[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf(
		"资源 %q 的 Spec 含契约外的键 %s（允许：%s）",
		decl.Name,
		strings.Join(unknown, ", "),
		strings.Join(c.SpecKeys, ", "),
	)
}

// validateSpecRequired 校验声明 Spec 给出了必填键；缺失在分配阶段失败。
func (c ResourceContract) validateSpecRequired(decl ResourceDecl) error {
	missing := make([]string, 0, len(c.SpecRequired))
	for _, key := range c.SpecRequired {
		if strings.TrimSpace(decl.Spec[key]) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"资源 %q（%s）缺少必需的 Spec 键 %s",
		decl.Name,
		c.Type,
		strings.Join(missing, ", "),
	)
}

// validateHandle 校验句柄 payload 的必填键，用于 worker 侧的句柄注入校验。
func (c ResourceContract) validateHandle(caseID string, handle Handle) error {
	missing := make([]string, 0, len(c.PayloadRequired))
	for _, key := range c.PayloadRequired {
		if strings.TrimSpace(handle.Payload[key]) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"用例 %q 的资源 %q（%s）句柄缺少必需的 payload 键 %s",
			caseID,
			handle.Name,
			handle.Type,
			strings.Join(missing, ", "),
		)
	}
	return nil
}

// buildHandle 按契约组装句柄并校验必填键，避免提供者产出不完整的句柄。
func buildHandle(decl ResourceDecl, payload map[string]string, fingerprint string) (Handle, error) {
	handle := Handle{
		Name:        decl.Name,
		Type:        decl.Type,
		Scope:       decl.Scope,
		Payload:     payload,
		Fingerprint: fingerprint,
	}
	contract, err := ResourceContractFor(decl.Type)
	if err != nil {
		return Handle{}, err
	}
	if err := contract.validateHandle("", handle); err != nil {
		return Handle{}, err
	}
	return handle, nil
}

// specValue 读取声明 Spec 的键，缺失时返回空字符串。
func specValue(decl ResourceDecl, key string) string {
	return strings.TrimSpace(decl.Spec[key])
}

// specArgs 把 Spec 中的空格分隔参数拆成命令行参数。
//
// 参数不支持引号与转义：需要复杂命令行时改用提供者可执行的脚本，而不是在 Spec 里塞入
// shell 片段。
func specArgs(decl ResourceDecl, key string) []string {
	raw := specValue(decl, key)
	if raw == "" {
		return nil
	}
	return strings.Fields(raw)
}

// acquirePrecheck 在分配前统一校验上下文、声明合法性与契约必填键。
//
// 声明已在注册阶段校验过契约外的键；这里补齐分配阶段的两件事：上下文是否还活着、
// 契约声明的必填 Spec 键是否齐全。
func acquirePrecheck(ctx context.Context, decl ResourceDecl) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("分配资源 %q 前上下文已结束: %w", decl.Name, err)
	}
	if err := decl.Validate(); err != nil {
		return err
	}
	contract, err := ResourceContractFor(decl.Type)
	if err != nil {
		return err
	}
	return contract.validateSpecRequired(decl)
}

// randomToken 生成用于资源命名与标识的随机十六进制串。
func randomToken(size int) (string, error) {
	if size <= 0 {
		size = 4
	}
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("生成资源随机标识失败: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}
