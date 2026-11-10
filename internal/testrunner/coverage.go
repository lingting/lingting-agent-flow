package testrunner

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// LegacyManifestSchemaVersion 是 R0 冻结清单（legacy-cases.manifest.json）的格式版本。
const LegacyManifestSchemaVersion = "agentflow-legacy-cases/1"

// CoverageSchemaVersion 是覆盖对账结果的格式版本。
const CoverageSchemaVersion = "agentflow-test-coverage/1"

// DefaultLegacyManifestPath 是 R0 冻结清单在仓库内的默认位置。
const DefaultLegacyManifestPath = ".steps/V3/R0/legacy-cases.manifest.json"

// DefaultAuditStage 是本阶段覆盖对账的默认范围（V3 R5 负责 R5 的 328 条）。
const DefaultAuditStage = "R5"

// modulePath 是本仓库的模块路径：清单记录完整导入路径，Case.Package 记录去掉它之后的
// 相对包路径，对账前统一到后者。
const modulePath = "github.com/lingting/agentflow"

// LegacyManifest 是 R0 冻结的旧用例清单：迁移期覆盖对账的分母。
type LegacyManifest struct {
	SchemaVersion  string            `json:"schema_version"`
	BaselineCommit string            `json:"baseline_commit"`
	Counts         LegacyCounts      `json:"counts"`
	Cases          []LegacyCaseEntry `json:"cases"`
}

// LegacyCounts 是清单自报的规模基线，用于核对清单自身是否完整。
type LegacyCounts struct {
	GoTestFiles     int `json:"go_test_files"`
	GoTestPackages  int `json:"go_test_packages"`
	GoTestFunctions int `json:"go_test_functions"`
	Cases           int `json:"cases"`
	MigrationCases  int `json:"migration_cases"`
}

// LegacyCaseEntry 是清单里的一条旧用例。
type LegacyCaseEntry struct {
	ID          string `json:"id"`
	Runtime     string `json:"runtime"`
	Stage       string `json:"stage"`
	File        string `json:"file"`
	PackageDir  string `json:"package_dir"`
	PackagePath string `json:"package_path"`
	Name        string `json:"name"`
}

// DroppedCase 是一条显式标记为不再迁移的旧用例。
type DroppedCase struct {
	// ID 是清单里的条目 ID，必须与清单逐字一致。
	ID string
	// Reason 是不再迁移的理由，写入对账报告。
	Reason string
}

// droppedRegistry 保存全部 dropped 声明；宣告在用例包的 init 里完成。
var (
	droppedMu    sync.Mutex
	droppedCases []DroppedCase
	droppedErrs  []error
)

// DeclareDropped 登记一条不再迁移的旧用例。
//
// 只在用例确实不适用时使用：条目消失但没在这里登记，覆盖对账会把它报成缺失并退出非零。
// 声明错误（空 ID、空理由、重复声明）与用例注册错误一样在 DroppedCases 里一次性暴露。
func DeclareDropped(id, reason string) {
	droppedMu.Lock()
	defer droppedMu.Unlock()
	if strings.TrimSpace(id) == "" {
		droppedErrs = append(droppedErrs, fmt.Errorf("dropped 声明缺少旧用例 ID"))
		return
	}
	if strings.TrimSpace(reason) == "" {
		droppedErrs = append(droppedErrs, fmt.Errorf("dropped 声明 %q 缺少理由", id))
		return
	}
	for _, declared := range droppedCases {
		if declared.ID == id {
			droppedErrs = append(droppedErrs, fmt.Errorf("dropped 声明 %q 重复", id))
			return
		}
	}
	droppedCases = append(droppedCases, DroppedCase{ID: id, Reason: reason})
}

// DroppedCases 返回全部 dropped 声明，按清单 ID 排序，并汇总声明期间的错误。
func DroppedCases() ([]DroppedCase, error) {
	droppedMu.Lock()
	defer droppedMu.Unlock()
	if len(droppedErrs) > 0 {
		return nil, fmt.Errorf("dropped 声明存在 %d 个错误: %w", len(droppedErrs), droppedErrs[0])
	}
	cases := append([]DroppedCase(nil), droppedCases...)
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return cases, nil
}

// CoverageOptions 是覆盖对账的输入。
type CoverageOptions struct {
	// ManifestPath 是 R0 冻结清单的路径；为空时取 DefaultLegacyManifestPath。
	ManifestPath string
	// Stages 是本次对账负责的阶段集合；为空时只对账 DefaultAuditStage。
	//
	// 对账只对范围内的条目负责：清单里的其他阶段（unit 由路径 C 负责、前端由 R6 负责）
	// 不计入缺失，也不影响退出码。
	Stages []string
}

// CoverageEntry 是覆盖对账里的一条条目。
type CoverageEntry struct {
	// ID 是清单条目 ID；多余条目没有清单来源，只填 CaseID。
	ID string `json:"id,omitempty"`
	// Stage 是该条目在清单里的阶段。
	Stage string `json:"stage,omitempty"`
	// PackagePath 是来源 go test 包路径（相对模块根）。
	PackagePath string `json:"package_path,omitempty"`
	// Name 是来源 go test 测试函数名。
	Name string `json:"name,omitempty"`
	// CaseID 是注册表里的用例 ID，只对已覆盖条目有意义。
	CaseID string `json:"case_id,omitempty"`
	// Reason 是 dropped 的理由或异常条目的说明。
	Reason string `json:"reason,omitempty"`
}

// CoveragePackage 是单个来源包的覆盖统计，供批次验收逐包核对。
type CoveragePackage struct {
	PackagePath string `json:"package_path"`
	InScope     int    `json:"in_scope"`
	Covered     int    `json:"covered"`
	Dropped     int    `json:"dropped"`
	Missing     int    `json:"missing"`
}

// CoverageTotals 是覆盖对账的汇总计数。
type CoverageTotals struct {
	// ManifestEntries 是清单条目总数，用于核对清单自报的 counts。
	ManifestEntries int `json:"manifest_entries"`
	// InScope 是本次范围（Stages）内的清单条目数。
	InScope int `json:"in_scope"`
	Covered int `json:"covered"`
	Dropped int `json:"dropped"`
	Missing int `json:"missing"`
	// Extra 是注册表里存在、但清单范围内没有对应条目的用例数。
	Extra int `json:"extra"`
}

// CoverageReport 是 --audit-coverage 的输出。
type CoverageReport struct {
	SchemaVersion string            `json:"schema_version"`
	ManifestPath  string            `json:"manifest_path"`
	Stages        []string          `json:"stages"`
	Totals        CoverageTotals    `json:"totals"`
	Packages      []CoveragePackage `json:"packages"`
	Missing       []CoverageEntry   `json:"missing"`
	Dropped       []CoverageEntry   `json:"dropped"`
	Extra         []CoverageEntry   `json:"extra"`
	// Problems 是清单与注册表的结构性问题：清单自报计数不符、dropped 指向清单外条目、
	// 迁移用例缺少 LegacyName 等。任何一条都使对账失败。
	Problems []string `json:"problems"`
}

// Failed 报告对账是否未通过：存在缺失、多余或结构性问题。
func (r CoverageReport) Failed() bool {
	return r.Totals.Missing > 0 || r.Totals.Extra > 0 || len(r.Problems) > 0
}

// ReadLegacyManifest 读取并校验 R0 冻结清单；只读文件，不执行用例。
func ReadLegacyManifest(path string) (LegacyManifest, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return LegacyManifest{}, fmt.Errorf("读取旧用例清单 %s 失败: %w", path, err)
	}
	var manifest LegacyManifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return LegacyManifest{}, fmt.Errorf("解析旧用例清单 %s 失败: %w", path, err)
	}
	if manifest.SchemaVersion != LegacyManifestSchemaVersion {
		return LegacyManifest{}, fmt.Errorf(
			"旧用例清单 %s 的格式版本为 %q，只接受 %s",
			path,
			manifest.SchemaVersion,
			LegacyManifestSchemaVersion,
		)
	}
	return manifest, nil
}

// AuditCoverage 以冻结清单为分母核对注册表，只读文件与注册表，不执行用例。
//
// 判定口径（V3 R5 §4.6）：范围内的条目消失即失败；确需删除必须先用 DeclareDropped 登记
// 理由；注册表里多出范围内清单没有的迁移用例同样失败，不允许悄悄放宽覆盖口径。
func AuditCoverage(cases []Case, opts CoverageOptions) (CoverageReport, error) {
	manifestPath := opts.ManifestPath
	if manifestPath == "" {
		manifestPath = DefaultLegacyManifestPath
	}
	stages := opts.Stages
	if len(stages) == 0 {
		stages = []string{DefaultAuditStage}
	}
	manifest, err := ReadLegacyManifest(manifestPath)
	if err != nil {
		return CoverageReport{}, err
	}
	dropped, err := DroppedCases()
	if err != nil {
		return CoverageReport{}, err
	}
	report := CoverageReport{
		SchemaVersion: CoverageSchemaVersion,
		ManifestPath:  manifestPath,
		Stages:        append([]string(nil), stages...),
		Totals:        CoverageTotals{ManifestEntries: len(manifest.Cases)},
	}
	if declared := manifest.Counts.Cases; declared > 0 && declared != len(manifest.Cases) {
		report.Problems = append(report.Problems, fmt.Sprintf(
			"清单自报 %d 条用例，实际列出 %d 条：以清单条目为准核对",
			declared,
			len(manifest.Cases),
		))
	}

	inScope := make([]LegacyCaseEntry, 0, len(manifest.Cases))
	byID := make(map[string]LegacyCaseEntry, len(manifest.Cases))
	for _, entry := range manifest.Cases {
		byID[entry.ID] = entry
		if containsString(stages, entry.Stage) {
			inScope = append(inScope, entry)
		}
	}
	report.Totals.InScope = len(inScope)

	droppedByID := make(map[string]string, len(dropped))
	for _, declared := range dropped {
		if _, ok := byID[declared.ID]; !ok {
			report.Problems = append(report.Problems, fmt.Sprintf(
				"dropped 声明 %q 不在旧用例清单内",
				declared.ID,
			))
			continue
		}
		droppedByID[declared.ID] = declared.Reason
	}

	covered := indexCoverage(cases, &report)
	packages := make(map[string]*CoveragePackage, len(inScope))
	totals := &report.Totals
	for _, entry := range inScope {
		stats := packageStats(packages, entry.PackagePath)
		stats.InScope++
		if _, ok := covered[auditKey(entry.PackagePath, entry.Name)]; ok {
			stats.Covered++
			totals.Covered++
			delete(covered, auditKey(entry.PackagePath, entry.Name))
			continue
		}
		if reason, ok := droppedByID[entry.ID]; ok {
			stats.Dropped++
			totals.Dropped++
			report.Dropped = append(report.Dropped, CoverageEntry{
				ID:          entry.ID,
				Stage:       entry.Stage,
				PackagePath: relativePackage(entry.PackagePath),
				Name:        entry.Name,
				Reason:      reason,
			})
			continue
		}
		stats.Missing++
		totals.Missing++
		report.Missing = append(report.Missing, CoverageEntry{
			ID:          entry.ID,
			Stage:       entry.Stage,
			PackagePath: relativePackage(entry.PackagePath),
			Name:        entry.Name,
		})
	}
	for _, leftover := range sortedCoverageKeys(covered) {
		totals.Extra++
		report.Extra = append(report.Extra, CoverageEntry{
			CaseID:      covered[leftover],
			PackagePath: strings.SplitN(leftover, "\x00", 2)[0],
			Name:        strings.SplitN(leftover, "\x00", 2)[1],
			Reason:      "注册表里的迁移用例在清单范围内没有对应条目",
		})
	}
	report.Packages = flattenPackageStats(packages)
	sortCoverageEntries(report.Missing)
	sortCoverageEntries(report.Dropped)
	sortCoverageEntries(report.Extra)
	return report, nil
}

// indexCoverage 建立「来源包路径 + 来源函数名」到用例 ID 的索引。
//
// 只统计设了 Package 的用例：Package 为空的是 R1 起新增的自检用例，没有清单来源。
func indexCoverage(cases []Case, report *CoverageReport) map[string]string {
	covered := make(map[string]string, len(cases))
	seen := make(map[string]string, len(cases))
	for _, c := range cases {
		if strings.TrimSpace(c.Package) == "" {
			continue
		}
		if c.LegacyName == "" {
			report.Problems = append(report.Problems, fmt.Sprintf(
				"用例 %q 无法对账：缺少来源测试函数名（用例主体应命名为 func run<原函数名去掉 Test 前缀>）",
				c.ID,
			))
			continue
		}
		key := auditKey(c.Package, c.LegacyName)
		if previous, exists := seen[key]; exists {
			report.Problems = append(report.Problems, fmt.Sprintf(
				"用例 %q 与 %q 都声明来源 %s.%s",
				previous,
				c.ID,
				c.Package,
				c.LegacyName,
			))
			continue
		}
		seen[key] = c.ID
		covered[key] = c.ID
	}
	return covered
}

// auditKey 是覆盖对账的匹配键：来源包路径（相对模块根）+ 来源测试函数名。
func auditKey(packagePath, name string) string {
	return relativePackage(packagePath) + "\x00" + name
}

// relativePackage 把完整导入路径统一成相对模块根的包路径。
func relativePackage(packagePath string) string {
	return strings.TrimPrefix(packagePath, modulePath+"/")
}

// packageStats 取出（必要时新建）单个来源包的统计。
func packageStats(stats map[string]*CoveragePackage, packagePath string) *CoveragePackage {
	key := relativePackage(packagePath)
	if existing, ok := stats[key]; ok {
		return existing
	}
	created := &CoveragePackage{PackagePath: key}
	stats[key] = created
	return created
}

// flattenPackageStats 按包路径排序输出统计，便于逐包核对。
func flattenPackageStats(stats map[string]*CoveragePackage) []CoveragePackage {
	flattened := make([]CoveragePackage, 0, len(stats))
	for _, value := range stats {
		flattened = append(flattened, *value)
	}
	sort.Slice(flattened, func(i, j int) bool {
		return flattened[i].PackagePath < flattened[j].PackagePath
	})
	return flattened
}

// sortCoverageEntries 按包路径与名称排序，使报告可复现。
func sortCoverageEntries(entries []CoverageEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].PackagePath != entries[j].PackagePath {
			return entries[i].PackagePath < entries[j].PackagePath
		}
		return entries[i].Name < entries[j].Name
	})
}

// sortedCoverageKeys 返回有序的索引键，使多余条目按稳定顺序输出。
func sortedCoverageKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// containsString 报告列表里是否包含给定取值。
func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// WriteCoverageReport 以原子方式写出覆盖对账结果。
func WriteCoverageReport(path string, report CoverageReport) error {
	if err := writeJSONFile(path, report); err != nil {
		return fmt.Errorf("写出覆盖对账结果失败: %w", err)
	}
	return nil
}

// Summary 返回面向终端的覆盖对账摘要：逐包统计、缺失、dropped 与多余条目。
func (r CoverageReport) Summary() string {
	var builder strings.Builder
	fmt.Fprintf(
		&builder,
		"覆盖对账：范围 %s；清单 %s\n",
		strings.Join(r.Stages, ","),
		r.ManifestPath,
	)
	fmt.Fprintf(
		&builder,
		"条目：清单 %d / 范围 %d / 覆盖 %d / dropped %d / 缺失 %d / 多余 %d\n",
		r.Totals.ManifestEntries,
		r.Totals.InScope,
		r.Totals.Covered,
		r.Totals.Dropped,
		r.Totals.Missing,
		r.Totals.Extra,
	)
	for _, stats := range r.Packages {
		fmt.Fprintf(
			&builder,
			"  %-46s 覆盖 %3d / %3d（dropped %d，缺失 %d）\n",
			stats.PackagePath,
			stats.Covered,
			stats.InScope,
			stats.Dropped,
			stats.Missing,
		)
	}
	for _, entry := range r.Missing {
		fmt.Fprintf(&builder, "缺失 %s.%s（%s）\n", entry.PackagePath, entry.Name, entry.Stage)
	}
	for _, entry := range r.Dropped {
		fmt.Fprintf(
			&builder,
			"dropped %s.%s：%s\n",
			entry.PackagePath,
			entry.Name,
			entry.Reason,
		)
	}
	for _, entry := range r.Extra {
		fmt.Fprintf(
			&builder,
			"多余 %s（来源 %s.%s）\n",
			entry.CaseID,
			entry.PackagePath,
			entry.Name,
		)
	}
	for _, problem := range r.Problems {
		fmt.Fprintf(&builder, "问题 %s\n", problem)
	}
	return builder.String()
}
