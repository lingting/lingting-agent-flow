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

// workspaceRootDirName 是运行工作空间在系统临时目录下的根名。
const workspaceRootDirName = "lingting-agent-flow-tests"

// RunLayout 描述一次运行的产物目录与工作空间布局（V3 R4 §5 D6）：
//
//	<artifact-root>/<run-id>/report.json
//	<artifact-root>/<run-id>/cases/<case-id>/{stdout.log,stderr.log,result.json,artifacts/}
//	<os.TempDir()>/lingting-agent-flow-tests/<run-id>/<case-id>/          用例工作空间
//	<os.TempDir()>/lingting-agent-flow-tests/<run-id>/shard_<type>_<id>/  资源存放目录
//
// 运行目录按 run-id 隔离，历史产物不会被后续运行覆盖。用例工作空间与资源存放目录放在系统
// 临时目录下：配置、数据库、Session 与服务进程输出都落在用例工作空间内，与产物目录分离，
// 使「零残留」只需要收敛进程、端口与远端目录，而不涉及保留的测试产物。
type RunLayout struct {
	Root   string
	RunDir string
	// RunID 是本次运行的标识，同时是用例工作空间根目录名。
	RunID string
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
	return RunLayout{Root: absolute, RunDir: filepath.Join(absolute, runID), RunID: runID}, nil
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
	return filepath.Join(l.CasesDir(), flattenCaseID(caseID))
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

// WorkspaceRoot 返回本次运行的工作空间根目录。
//
// 它固定在系统临时目录下，与产物根目录无关：用例工作空间与资源存放目录都挂在它下面，
// 一次运行对应一个目录，多次运行的产物不会互相覆盖。
func (l RunLayout) WorkspaceRoot() string {
	return filepath.Join(os.TempDir(), workspaceRootDirName, l.RunID)
}

// CaseWorkspaceDir 返回用例工作空间目录，用例 ID 的层级同样用双下划线展开。
func (l RunLayout) CaseWorkspaceDir(caseID string) string {
	return filepath.Join(l.WorkspaceRoot(), flattenCaseID(caseID))
}

// CreateCaseWorkspace 创建用例工作空间目录并返回其绝对路径。
//
// 调用发生在分配资源之前：需要工作目录的提供者（例如 service-process 的子进程）据此
// 决定工作目录，用例主体之后在同一个目录里读写配置、数据库与日志。
func (l RunLayout) CreateCaseWorkspace(caseID string) (string, error) {
	dir := l.CaseWorkspaceDir(caseID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("创建用例工作空间失败: %w", err)
	}
	return dir, nil
}

// flattenCaseID 把用例 ID 的层级用双下划线展开成扁平目录名。
func flattenCaseID(caseID string) string {
	return strings.ReplaceAll(caseID, "/", "__")
}
