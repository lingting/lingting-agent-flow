package caseinventory

import (
	"regexp"
	"sort"
	"strings"
)

// packageScan 是一个 Go 测试包的全部已跟踪源码（含辅助文件）。
type packageScan struct {
	Dir           string
	PackagePath   string
	Files         []goSource
	SourceAll     string
	Symbols       map[string]bool
	Imports       map[string]bool
	TestSignals   signals
	Markers       fileMarkers
	HasTestMain   bool
	CaseCount     int
	TestFileCount int
}

// hasSymbol 判断包内是否引用过任一 testkit 符号。
func (p *packageScan) hasSymbol(names ...string) []string {
	var hits []string
	for _, name := range names {
		if p.Symbols[name] {
			hits = append(hits, "testkit."+name)
		}
	}
	sort.Strings(hits)
	return hits
}

// hasImport 判断包内是否导入过指定模块。
func (p *packageScan) hasImport(paths ...string) []string {
	var hits []string
	for _, path := range paths {
		if p.Imports[path] {
			hits = append(hits, "import "+path)
		}
	}
	sort.Strings(hits)
	return hits
}

// hasImportPrefix 判断包内是否存在以指定前缀开头的导入。
func (p *packageScan) hasImportPrefix(prefix string) []string {
	var hits []string
	for path := range p.Imports {
		if strings.HasPrefix(path, prefix) {
			hits = append(hits, "import "+path)
		}
	}
	sort.Strings(hits)
	return hits
}

// hasText 判断包内源码是否出现指定文本。
func (p *packageScan) hasText(tokens ...string) []string {
	var hits []string
	for _, token := range tokens {
		if strings.Contains(p.SourceAll, token) {
			hits = append(hits, "text:"+token)
		}
	}
	sort.Strings(hits)
	return hits
}

// isIntegrationOrE2E 判断包是否属于集成或 E2E 目录（单元测试里的 Docker / SSH 都是替身）。
func (p *packageScan) isIntegrationOrE2E() bool {
	return strings.HasPrefix(p.Dir, "tests/integration/") || strings.HasPrefix(p.Dir, "tests/e2e/") ||
		p.Dir == "tests/e2e" || p.Dir == "tests/integration"
}

// resourceTypes 是资源类型的冻结词汇表，R3 的调度器直接按它校验声明。
var resourceTypes = []ResourceType{
	{Name: "docker-daemon", DefaultScope: ScopeRunShared, Description: "远程 Docker daemon 命令入口（经 internal/docker）"},
	{Name: "remote-container", DefaultScope: ScopeCaseExclusive, Description: "本次用例独占的真实远程容器（testkit 容器登记与零残留核验）"},
	{Name: "remote-workspace", DefaultScope: ScopeCaseExclusive, Description: "远程机器上的临时测试目录"},
	{Name: "ssh-host", DefaultScope: ScopeRunShared, Description: "tests/config/ssh.yml 指向的真实远程主机"},
	{Name: "ssh-session", DefaultScope: ScopeCaseExclusive, Description: "一条真实 SSH 连接及其会话"},
	{Name: "local-ssh-server", DefaultScope: ScopeCaseExclusive, Description: "测试进程内启动的 SSH/SFTP 服务端"},
	{Name: "pi-cli", DefaultScope: ScopeCaseExclusive, Description: "本机真实 Pi CLI 调用"},
	{Name: "sqlite-db", DefaultScope: ScopeCaseExclusive, Description: "用例工作空间内的 SQLite 数据库文件"},
	{Name: "local-port", DefaultScope: ScopeCaseExclusive, Description: "用例自己监听的本地端口"},
	{Name: "local-http-server", DefaultScope: ScopeCaseExclusive, Description: "httptest 启动的进程内 HTTP 服务"},
	{Name: "child-process", DefaultScope: ScopeCaseExclusive, Description: "用例启动的外部子进程（git、ssh、docker、pi 等）"},
	{Name: "service-process", DefaultScope: ScopeCaseExclusive, Description: "被测服务进程（./cmd/agentflow 构建产物）"},
	{Name: "git-worktree", DefaultScope: ScopeCaseExclusive, Description: "用例独占的 Git 仓库或工作区"},
	{Name: "node-worker", DefaultScope: ScopeCaseExclusive, Description: "Vitest fork 进程（一个文件一个 worker）"},
	{Name: "frontend-dev-server", DefaultScope: ScopeRunShared, Description: "前端 Vite dev server（Playwright webServer）"},
	{Name: "browser", DefaultScope: ScopeCaseExclusive, Description: "无头浏览器实例（Playwright project）"},
}

// needBuilder 收集资源声明并按类型去重，证据保持稳定顺序。
type needBuilder struct {
	needs []ResourceNeed
}

// add 追加一条资源声明；证据为空时不生成声明。
func (b *needBuilder) add(resource, scope string, evidence []string) {
	if len(evidence) == 0 {
		return
	}
	unique := map[string]bool{}
	items := make([]string, 0, len(evidence))
	for _, item := range evidence {
		if unique[item] {
			continue
		}
		unique[item] = true
		items = append(items, item)
	}
	sort.Strings(items)
	b.needs = append(b.needs, ResourceNeed{Type: resource, Scope: scope, Evidence: items})
}

// finish 返回按资源名排序的声明列表；没有声明时返回空列表而不是 nil。
func (b *needBuilder) finish() []ResourceNeed {
	sort.Slice(b.needs, func(i, j int) bool { return b.needs[i].Type < b.needs[j].Type })
	if b.needs == nil {
		return []ResourceNeed{}
	}
	return b.needs
}

// inferResources 按"证据 → 资源"的固定规则推断一个测试包的资源需求。
//
// 规则只使用可直接指认的源码事实：testkit 符号、精确导入路径与文本标记；不确定的组合
// 一律落到 NeedsReview，而不是猜测成共享或独享。
func inferResources(pkg *packageScan) []ResourceNeed {
	var builder needBuilder
	external := pkg.isIntegrationOrE2E() || pkg.Symbols["RequireRealTests"]

	// 外部设备资源只对集成 / E2E 包与获准运行真实依赖的包生效：单元测试里的 Docker、
	// SSH 与 Pi 全部是进程内替身，把它们算成外部资源会让矩阵失真。
	if external {
		builder.add("remote-container", ScopeCaseExclusive, pkg.hasSymbol(
			"ContainerRun", "NewContainerRun", "RegisterContainerFinalizer", "ContainerFinalizer",
			"CleanupContainer", "IsTestContainerName", "ContainerInspectFor",
		))
		builder.add("remote-workspace", ScopeCaseExclusive, pkg.hasSymbol(
			"RemoteTestRoot", "ResolveRemoteTestRoot", "EnsureRemoteDir",
		))
		builder.add("docker-daemon", ScopeRunShared, pkg.hasSymbol("LoadDockerConfig", "DockerConfig"))
		builder.add("ssh-host", ScopeRunShared, pkg.hasSymbol(
			"LoadSSHConfig", "SSHConfig", "KnownHostsSources",
		))
		builder.add("ssh-session", ScopeCaseExclusive, pkg.hasImport(ModulePath+"/internal/sshclient"))
		builder.add("pi-cli", ScopeCaseExclusive, pkg.hasSymbol("LoadPiConfig"))
	}
	if pkg.Markers.NetListen {
		builder.add("local-port", ScopeCaseExclusive, []string{"call:net.Listen"})
		if pkg.Imports["golang.org/x/crypto/ssh"] {
			builder.add("local-ssh-server", ScopeCaseExclusive, []string{
				"call:net.Listen", "import golang.org/x/crypto/ssh",
			})
		}
	}
	if pkg.Markers.HTTPTestServer {
		builder.add("local-http-server", ScopeCaseExclusive, []string{"call:httptest.New"})
	}
	if pkg.Markers.ExecCommand {
		builder.add("child-process", ScopeCaseExclusive, []string{"call:exec.Command"})
	}
	builder.add("sqlite-db", ScopeCaseExclusive, append(
		pkg.hasSymbol("OpenSQLite"),
		pkg.hasImport(ModulePath+"/internal/storage/sqlite")...,
	))
	if external {
		builder.add("git-worktree", ScopeCaseExclusive, append(
			pkg.hasImportPrefix(ModulePath+"/internal/source"),
			pkg.hasImportPrefix(ModulePath+"/internal/codesaver")...,
		))
		builder.add("service-process", ScopeCaseExclusive, pkg.hasText("./cmd/agentflow"))
	}
	return builder.finish()
}

// destructiveTokens 是不可逆操作的文本线索；命中且包内确实有资源时视为"可能独占"。
//
// 只保留会作用于共享资源的操作：容器与镜像删除、远程目录清理、服务关闭。通用的
// Stop()/Kill()/os.Remove 会命中进程内的生命周期替身，不纳入。
var destructiveTokens = []string{
	"ContainerRemove", "ImageRemove", "docker rm ", "docker rmi ",
	"Shutdown(", "RemoveAll(",
}

// destructiveHints 返回包内的不可逆操作线索。
func destructiveHints(pkg *packageScan) []string {
	return pkg.hasText(destructiveTokens...)
}

// realDependencies 返回包内声明的真实外部依赖开关。
func realDependencies(pkg *packageScan) []string {
	var deps []string
	if pkg.Symbols["LoadPiConfig"] {
		deps = append(deps, "pi")
	}
	if pkg.Symbols["LoadSSHConfig"] || pkg.Symbols["KnownHostsSources"] {
		deps = append(deps, "ssh")
	}
	if pkg.Symbols["LoadDockerConfig"] {
		deps = append(deps, "docker")
	}
	return deps
}

// serialReason 返回包注释中显式声明的固定串行理由。
func serialReason(pkg *packageScan) string {
	const marker = "不使用 t.Parallel"
	for _, line := range strings.Split(pkg.SourceAll, "\n") {
		if strings.Contains(line, marker) {
			return strings.TrimSpace(strings.TrimPrefix(line, "//"))
		}
	}
	return ""
}

// fixedPortPattern 匹配用例里写死的本地端口。
var fixedPortPattern = regexp.MustCompile(`(127\.0\.0\.1|localhost):[0-9]{2,5}`)

// parallelSuspects 返回疑似依赖并行时序的文件清单（P1-4 的迁移风险）。
func parallelSuspects(pkg *packageScan) []ParallelSuspect {
	var suspects []ParallelSuspect
	for _, file := range pkg.Files {
		if !file.IsTest {
			continue
		}
		var evidence []string
		if file.Markers.SyncOnce {
			evidence = append(evidence, "sync.Once：包级一次性初始化")
		}
		if ports := fixedPortPattern.FindAllString(file.Source, -1); len(ports) > 0 {
			unique := map[string]bool{}
			for _, port := range ports {
				if unique[port] {
					continue
				}
				unique[port] = true
				evidence = append(evidence, "固定端口 "+port)
			}
		}
		if len(evidence) == 0 {
			continue
		}
		sort.Strings(evidence)
		suspects = append(suspects, ParallelSuspect{
			File:     file.RelPath,
			Evidence: evidence,
			Risk:     "包级状态或固定端口可能让用例依赖并行时序；迁移后语义变化需人工判定",
		})
	}
	return suspects
}
