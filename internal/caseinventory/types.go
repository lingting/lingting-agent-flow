// Package caseinventory 盘点现有测试用例与它们依赖的资源。
//
// 产出是测试执行器重构（V3 R0）的冻结资产：legacy-cases.manifest.json 是"哪些用例
// 必须被新体系覆盖"的对账基准，case-resource-matrix.json 是调度器读取的资源声明。
// 它只用 git ls-files 枚举已跟踪文件，不遍历工作区噪声目录，也不执行任何测试。
package caseinventory

// 资产格式版本：结构变化必须同步提升版本号，避免旧产物被静默按新结构读取。
const (
	ManifestSchemaVersion       = "agentflow-legacy-cases/1"
	ResourceMatrixSchemaVersion = "agentflow-case-resources/1"
)

// 仓库约定：模块路径、Go 测试目录与前端工程目录。
const (
	ModulePath   = "github.com/lingting/agentflow"
	TestsPrefix  = "tests/"
	FrontendRoot = "lingting-agent-flow-ui/"
)

// 用例运行时取值。
const (
	RuntimeGo         = "go"
	RuntimeVitest     = "vitest"
	RuntimePlaywright = "playwright"
)

// 迁移阶段取值。路径 C：tests/unit 保留 go test，只迁移集成、E2E 与前端适配器。
const (
	StageKeepGoTest  = "keep-go-test"
	StageIntegration = "R5"
	StageFrontend    = "R6"
)

// 资源共享语义与作用域取值。
const (
	SharingSharedCandidate   = "shared-candidate"
	SharingExclusiveRequired = "exclusive-required"
	SharingUnclassified      = "unclassified"

	ScopeRunShared     = "run-shared"
	ScopeCaseExclusive = "case-exclusive"
)

// Result 是一次盘点的完整结果；Notices 只用于终端输出，不写入资产。
type Result struct {
	Manifest Manifest
	Matrix   ResourceMatrix
	Notices  []string
}

// Manifest 是冻结的旧用例清单。
//
// 迁移期间条目只允许显式标记 dropped，不允许消失；覆盖核对以 Cases 与 Counts 为准。
type Manifest struct {
	SchemaVersion  string `json:"schema_version"`
	BaselineCommit string `json:"baseline_commit"`
	Counts         Counts `json:"counts"`
	Cases          []Case `json:"cases"`
}

// Counts 是迁移对账口径的规模统计。
type Counts struct {
	GoTestFiles     int     `json:"go_test_files"`
	GoTestPackages  int     `json:"go_test_packages"`
	GoTestFunctions int     `json:"go_test_functions"`
	GoSubtests      int     `json:"go_subtests"`
	GoSubtestCalls  int     `json:"go_subtest_calls"`
	GoTestMain      int     `json:"go_test_main"`
	VitestFiles     int     `json:"vitest_files"`
	PlaywrightFiles int     `json:"playwright_files"`
	Cases           int     `json:"cases"`
	MigrationCases  int     `json:"migration_cases"`
	Signals         Signals `json:"signals"`
}

// Signals 是全仓库调用点计数，用于核对"迁移没有静默丢失"的分母。
type Signals struct {
	FatalCalls    int `json:"fatal_calls"`
	ErrorCalls    int `json:"error_calls"`
	HelperCalls   int `json:"helper_calls"`
	CleanupCalls  int `json:"cleanup_calls"`
	ParallelCalls int `json:"parallel_calls"`
	SkipCalls     int `json:"skip_calls"`
	TempDirCalls  int `json:"temp_dir_calls"`
}

// Case 是一条必须被新体系覆盖的用例。
//
// Go 用例是单个测试函数，Subtests 记录其中的 t.Run 字面量子测试名；前端用例以
// "一个文件 = 一个用例"为粒度，与前端测试的并行模型一致。
type Case struct {
	ID          string   `json:"id"`
	Runtime     string   `json:"runtime"`
	Stage       string   `json:"stage"`
	File        string   `json:"file"`
	PackageDir  string   `json:"package_dir,omitempty"`
	PackagePath string   `json:"package_path,omitempty"`
	Name        string   `json:"name"`
	Subtests    []string `json:"subtests,omitempty"`
	Dropped     string   `json:"dropped,omitempty"`
}

// ResourceMatrix 是资源需求矩阵：调度器（R3）按它选择共享或独享资源。
//
// 自动推断只做初筛，NeedsReview 为真的条目必须由人工逐项确认后才能作为调度输入。
type ResourceMatrix struct {
	SchemaVersion    string              `json:"schema_version"`
	BaselineCommit   string              `json:"baseline_commit"`
	ResourceTypes    []ResourceType      `json:"resource_types"`
	Packages         []PackageResources  `json:"packages"`
	Frontend         []FrontendResources `json:"frontend"`
	ParallelSuspects []ParallelSuspect   `json:"parallel_suspects"`
}

// ResourceType 是资源类型的冻结词汇表条目。
type ResourceType struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	DefaultScope string `json:"default_scope"`
}

// ResourceNeed 是一条资源声明；Evidence 必须能直接指向源码事实。
type ResourceNeed struct {
	Type     string   `json:"type"`
	Scope    string   `json:"scope"`
	Evidence []string `json:"evidence"`
}

// PackageResources 是一个 Go 测试包的资源声明。
type PackageResources struct {
	Dir              string         `json:"dir"`
	PackagePath      string         `json:"package_path"`
	Stage            string         `json:"stage"`
	CaseCount        int            `json:"case_count"`
	RealTestGate     bool           `json:"real_test_gate"`
	RealDependencies []string       `json:"real_dependencies,omitempty"`
	Resources        []ResourceNeed `json:"resources"`
	Sharing          string         `json:"sharing"`
	DestructiveHints []string       `json:"destructive_hints,omitempty"`
	SerialDeclared   bool           `json:"serial_declared"`
	SerialReason     string         `json:"serial_reason,omitempty"`
	NeedsReview      bool           `json:"needs_review"`
	ReviewReasons    []string       `json:"review_reasons,omitempty"`
}

// FrontendResources 是一个前端用例文件的资源声明。
type FrontendResources struct {
	File          string         `json:"file"`
	Runtime       string         `json:"runtime"`
	CaseID        string         `json:"case_id"`
	Resources     []ResourceNeed `json:"resources"`
	Sharing       string         `json:"sharing"`
	NeedsReview   bool           `json:"needs_review"`
	ReviewReasons []string       `json:"review_reasons,omitempty"`
}

// ParallelSuspect 标记疑似依赖并行时序的文件（P1-4 的迁移风险清单）。
type ParallelSuspect struct {
	File     string   `json:"file"`
	Evidence []string `json:"evidence"`
	Risk     string   `json:"risk"`
}
