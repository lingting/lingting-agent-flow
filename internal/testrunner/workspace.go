package testrunner

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RunLayout 描述一次运行的产物目录布局：
//
//	<root>/<run-id>/report.json
//	<root>/<run-id>/cases/<case-id>/{stdout.log,stderr.log,result.json,artifacts/}
//
// 运行目录按 run-id 隔离，历史产物不会被后续运行覆盖。
type RunLayout struct {
	Root   string
	RunDir string
}

// NewRunLayout 校验并构造运行布局。
func NewRunLayout(root, runID string) (RunLayout, error) {
	if strings.TrimSpace(root) == "" {
		return RunLayout{}, fmt.Errorf("产物根目录不能为空")
	}
	if !caseIDPattern.MatchString(runID) {
		return RunLayout{}, fmt.Errorf("运行 ID %q 含有不允许的字符", runID)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return RunLayout{}, fmt.Errorf("解析产物根目录失败: %w", err)
	}
	return RunLayout{Root: absolute, RunDir: filepath.Join(absolute, runID)}, nil
}

// NewRunID 生成 "<UTC 时间>-<4 位随机>" 形式的运行 ID。
func NewRunID(now time.Time) (string, error) {
	suffix := make([]byte, 2)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("生成运行 ID 失败: %w", err)
	}
	return now.UTC().Format("20060102t150405z") + "-" + hex.EncodeToString(suffix), nil
}

// CreateRunDir 创建本次运行的目录。
func (l RunLayout) CreateRunDir() error {
	if err := os.MkdirAll(l.CasesDir(), 0o750); err != nil {
		return fmt.Errorf("创建运行目录失败: %w", err)
	}
	return nil
}

// CasesDir 返回用例产物目录。
func (l RunLayout) CasesDir() string {
	return filepath.Join(l.RunDir, "cases")
}

// ReportPath 返回统一报告的默认位置。
func (l RunLayout) ReportPath() string {
	return filepath.Join(l.RunDir, "report.json")
}

// CaseDir 返回单条用例的产物目录；用例 ID 的层级用双下划线展开成扁平目录名。
func (l RunLayout) CaseDir(caseID string) string {
	return filepath.Join(l.CasesDir(), strings.ReplaceAll(caseID, "/", "__"))
}

// CaseArtifactPaths 返回一条用例约定的全部产物路径。
func (l RunLayout) CaseArtifactPaths(caseID string) ArtifactPaths {
	dir := l.CaseDir(caseID)
	return ArtifactPaths{
		CaseDir:     dir,
		Stdout:      filepath.Join(dir, "stdout.log"),
		Stderr:      filepath.Join(dir, "stderr.log"),
		Result:      filepath.Join(dir, "result.json"),
		ArtifactDir: filepath.Join(dir, "artifacts"),
	}
}

// CreateCaseDir 创建用例产物目录与用例可写的产物子目录。
func (l RunLayout) CreateCaseDir(paths ArtifactPaths) error {
	if err := os.MkdirAll(paths.ArtifactDir, 0o750); err != nil {
		return fmt.Errorf("创建用例产物目录失败: %w", err)
	}
	return nil
}

// ResourcesDir 返回本次运行的资源工作根目录，供进程内与本地资源使用。
func (l RunLayout) ResourcesDir() string {
	return filepath.Join(l.RunDir, "resources")
}
