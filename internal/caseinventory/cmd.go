package caseinventory

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// 资产文件名：R0 冻结产物的固定命名，供调度器与后续覆盖核对直接读取。
const (
	ManifestFileName = "legacy-cases.manifest.json"
	MatrixFileName   = "case-resource-matrix.json"
)

// Run 是命令入口：解析参数、执行盘点并写出资产。
func Run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("agentflow-test-inventory", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "仓库根目录")
	outDir := flags.String("out", filepath.Join(".steps", "V3", "R0"), "资产输出目录")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if err := ensureRootExists(*root); err != nil {
		fmt.Fprintf(stderr, "盘点失败: %v\n", err)
		return 1
	}
	result, err := Build(*root)
	if err != nil {
		fmt.Fprintf(stderr, "盘点失败: %v\n", err)
		return 1
	}
	if err := writeArtifacts(*outDir, result); err != nil {
		fmt.Fprintf(stderr, "写出资产失败: %v\n", err)
		return 1
	}
	report(stdout, *outDir, result)
	return 0
}

// writeArtifacts 把两份资产写入输出目录。
func writeArtifacts(outDir string, result Result) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}
	if err := writeJSON(filepath.Join(outDir, ManifestFileName), result.Manifest); err != nil {
		return err
	}
	return writeJSON(filepath.Join(outDir, MatrixFileName), result.Matrix)
}

// writeJSON 以固定缩进和 LF 行尾写出 JSON，保证重复执行得到相同文件内容。
func writeJSON(targetPath string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 %s 失败: %w", filepath.Base(targetPath), err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(targetPath, encoded, 0o644); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", filepath.Base(targetPath), err)
	}
	return nil
}

// report 输出盘点摘要；详细清单以 JSON 资产为准。
func report(stdout io.Writer, outDir string, result Result) {
	counts := result.Manifest.Counts
	signals := counts.Signals
	fmt.Fprintf(stdout, "基线提交: %s\n", result.Manifest.BaselineCommit)
	fmt.Fprintf(stdout, "Go 测试文件: %d，测试包: %d，测试函数: %d，子测试: %d（字面量 %d），TestMain: %d\n",
		counts.GoTestFiles, counts.GoTestPackages, counts.GoTestFunctions,
		counts.GoSubtestCalls, counts.GoSubtests, counts.GoTestMain)
	fmt.Fprintf(stdout, "前端用例文件: Vitest %d，Playwright %d\n",
		counts.VitestFiles, counts.PlaywrightFiles)
	fmt.Fprintf(stdout, "用例总数: %d，其中需要迁移: %d（路径 C：unit 保留 go test）\n",
		counts.Cases, counts.MigrationCases)
	fmt.Fprintf(stdout,
		"调用点: Fatal %d，Error %d，Helper %d，Cleanup %d，Parallel %d，Skip %d，TempDir %d\n",
		signals.FatalCalls, signals.ErrorCalls, signals.HelperCalls, signals.CleanupCalls,
		signals.ParallelCalls, signals.SkipCalls, signals.TempDirCalls)

	needsReview := 0
	for _, pkg := range result.Matrix.Packages {
		if pkg.NeedsReview {
			needsReview++
		}
	}
	fmt.Fprintf(stdout, "资源矩阵: 测试包 %d（待人工确认 %d），前端文件 %d，并行时序嫌疑 %d\n",
		len(result.Matrix.Packages), needsReview, len(result.Matrix.Frontend),
		len(result.Matrix.ParallelSuspects))
	for _, notice := range result.Notices {
		fmt.Fprintf(stdout, "提示: %s\n", notice)
	}
	fmt.Fprintf(stdout, "资产目录: %s\n", outDir)
}
