package testrunner

import (
	"fmt"
	"sort"
	"strings"
)

// ResourceProfile 是一种资源类型的调度属性（V3 R2 §4.6 冻结，R3 直接读取）。
type ResourceProfile struct {
	// Type 是资源类型。
	Type string
	// DefaultScope 是该类型的默认作用域；声明可以显式覆盖为另一种合法作用域。
	DefaultScope string
	// Capacity 是同一时刻允许占用的份数；0 表示不限。
	Capacity int
	// ExclusiveKeyKind 是独占键的语义名，空表示不参与互斥。
	ExclusiveKeyKind string
	// Note 是容量与独占键的判定依据。
	Note string
}

// resourceProfiles 是 16 种资源类型的调度属性。
//
// 前端三类（node-worker / frontend-dev-server / browser）的容量与独占键只是 R2 的初值，
// R6 接入适配器后按实测回写。
var resourceProfiles = []ResourceProfile{
	{
		Type:         ResourceLocalPort,
		DefaultScope: ScopeCaseExclusive,
		Note:         "端口由操作系统随机分配，按用例独立占用",
	},
	{
		Type:         ResourceSQLiteDB,
		DefaultScope: ScopeCaseExclusive,
		Note:         "每个用例一个数据库文件，文件之间没有共享状态",
	},
	{
		Type:         ResourceLocalHTTPServer,
		DefaultScope: ScopeCaseExclusive,
		Note:         "进程内 HTTP 服务随用例生命周期启停",
	},
	{
		Type:         ResourceChildProcess,
		DefaultScope: ScopeCaseExclusive,
		Note:         "子进程与用例一一对应，不跨用例复用",
	},
	{
		Type:         ResourceServiceProcess,
		DefaultScope: ScopeCaseExclusive,
		Note:         "被测服务进程按用例启动，端口与数据库按用例分配",
	},
	{
		Type:             ResourceGitWorktree,
		DefaultScope:     ScopeCaseExclusive,
		ExclusiveKeyKind: "repo",
		Note:             "同一仓库的 worktree 元数据互斥，不同仓库可以并行",
	},
	{
		Type:         ResourceLocalSSHServer,
		DefaultScope: ScopeCaseExclusive,
		Note:         "测试 SSH 服务端端口、主机密钥与客户端密钥都属于单次分配",
	},
	{
		Type:         ResourceSSHHost,
		DefaultScope: ScopeRunShared,
		Note:         "同一 host_id 的连接在一次运行内复用",
	},
	{
		Type:         ResourceSSHSession,
		DefaultScope: ScopeCaseExclusive,
		Capacity:     4,
		Note:         "单台真实主机可接受的并发会话上限",
	},
	{
		Type:             ResourceDockerDaemon,
		DefaultScope:     ScopeRunShared,
		ExclusiveKeyKind: "host_id",
		Note:             "同一主机的 Docker 入口共享，不同主机互不影响",
	},
	{
		Type:             ResourceRemoteContainer,
		DefaultScope:     ScopeCaseExclusive,
		Capacity:         4,
		ExclusiveKeyKind: "image",
		Note:             "同一镜像标签的容器操作互斥；并发容器总量受容量限制",
	},
	{
		Type:             ResourceRemoteWorkspace,
		DefaultScope:     ScopeCaseExclusive,
		ExclusiveKeyKind: "host_path",
		Note:             "同一主机同一路径不能被两个用例同时占用",
	},
	{
		Type:         ResourceNodeWorker,
		DefaultScope: ScopeCaseExclusive,
		Note:         "R6 冻结：一轮一个 Vitest worker 进程",
	},
	{
		Type:             ResourceFrontendDevServer,
		DefaultScope:     ScopeRunShared,
		Capacity:         1,
		ExclusiveKeyKind: "base_url",
		Note:             "R6 冻结：单个 dev server 按 base_url 共享",
	},
	{
		Type:         ResourceBrowser,
		DefaultScope: ScopeCaseExclusive,
		Capacity:     2,
		Note:         "R6 冻结：本机可并发启动的无头浏览器实例上限",
	},
}

// profileByType 是调度属性的索引，键为资源类型。
var profileByType = func() map[string]ResourceProfile {
	index := make(map[string]ResourceProfile, len(resourceProfiles))
	for _, profile := range resourceProfiles {
		index[profile.Type] = profile
	}
	return index
}()

// serialPackages 是必须整包串行的包（R0 §6）。
//
// 键是 Go 测试包目录，与迁移后用例的 group 前缀一致；R5 迁移时按同一张表声明包级串行。
var serialPackages = map[string]string{
	"tests/integration/remote/workflow": "同一台真实远程 Docker 与镜像标签互斥，整包串行",
}

// ResourceProfileOf 返回资源类型的调度属性。
func ResourceProfileOf(resourceType string) (ResourceProfile, error) {
	profile, ok := profileByType[resourceType]
	if !ok {
		return ResourceProfile{}, fmt.Errorf("资源类型 %q 没有调度属性", resourceType)
	}
	return profile, nil
}

// ResourceProfiles 返回全部调度属性，按类型名排序。
func ResourceProfiles() []ResourceProfile {
	profiles := make([]ResourceProfile, 0, len(resourceProfiles))
	profiles = append(profiles, resourceProfiles...)
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Type < profiles[j].Type })
	return profiles
}

// CapacityOf 返回资源类型的容量上限，0 表示不限。
func CapacityOf(resourceType string) int {
	return profileByType[resourceType].Capacity
}

// ExclusiveKeyOf 返回资源声明的独占键；空字符串表示该资源不参与互斥。
//
// 独占键是调度器的输入：同键资源不得并行，否则会争用同一份不可命名的远端状态
// （同一镜像标签、同一远程目录、同一仓库的 worktree 元数据）。
func ExclusiveKeyOf(decl ResourceDecl) string {
	profile, ok := profileByType[decl.Type]
	if !ok || profile.ExclusiveKeyKind == "" {
		return ""
	}
	switch profile.ExclusiveKeyKind {
	case "repo":
		return decl.Type + "|repo=" + defaultIfBlank(specValue(decl, "repo"), "(cwd)")
	case "host_id":
		return decl.Type + "|host_id=" + specValue(decl, "host_id")
	case "image":
		return decl.Type + "|image=" + specValue(decl, "image")
	case "host_path":
		return decl.Type + "|" + specValue(decl, "host_id") + "|" + specValue(decl, "path")
	case "base_url":
		return decl.Type + "|base_url=" + specValue(decl, "base_url")
	default:
		return decl.Type + "|" + profile.ExclusiveKeyKind
	}
}

// SerialPackageReason 返回包级串行的理由；空字符串表示该包可以并行。
func SerialPackageReason(pkg string) string {
	return serialPackages[strings.TrimSpace(pkg)]
}

// SerialPackages 返回全部需要整包串行的包，按路径排序。
func SerialPackages() []string {
	packages := make([]string, 0, len(serialPackages))
	for pkg := range serialPackages {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)
	return packages
}

// defaultIfBlank 在取值是空白时返回兜底值。
func defaultIfBlank(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
