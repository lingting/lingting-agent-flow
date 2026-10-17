// Package testkit 提供 AgentFlow 测试统一使用的工作空间与环境辅助组件。
//
// 设计约束（由代码结构保证，不依赖使用者记忆）：
//
//   - 每次测试运行在系统临时目录下的 lingting-agent-flow-tests 中创建随机根目录；
//   - 每个测试用例在其中创建独立子目录，互不共享数据库、Session 与文件；
//   - 目录创建后永不删除，测试结束只关闭运行时资源，便于检查失败现场。
package testkit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// baseDirName 是统一存放测试根目录的目录名。
const baseDirName = "lingting-agent-flow-tests"

// 运行根目录在单个测试进程内只创建一次。
var (
	rootOnce sync.Once
	rootPath string
	rootErr  error
)

// RunRoot 返回本次测试运行（当前测试包）的根目录绝对路径。
//
// 根目录位于 os.TempDir()/lingting-agent-flow-tests 之下，名称随机且唯一，
// 首次创建时把绝对路径输出到标准输出，便于事后定位测试产物。
func RunRoot(t *testing.T) string {
	t.Helper()
	rootOnce.Do(func() {
		rootPath, rootErr = createRunRoot()
	})
	if rootErr != nil {
		t.Fatalf("创建测试根目录失败: %v", rootErr)
	}
	return rootPath
}

// Dir 在运行根目录下创建一个唯一的子目录并返回其绝对路径。
//
// 它替代 t.TempDir()：返回的目录不会被自动删除。
func Dir(t *testing.T) string {
	t.Helper()
	root := RunRoot(t)
	path, err := os.MkdirTemp(root, "case-")
	if err != nil {
		t.Fatalf("创建测试子目录失败: %v", err)
	}
	return path
}

// createRunRoot 在系统临时目录下创建本次运行的根目录。
func createRunRoot() (string, error) {
	base := filepath.Join(os.TempDir(), baseDirName)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("创建测试根目录父目录失败: %s: %w", base, err)
	}
	root, err := os.MkdirTemp(base, packageLabel()+"-")
	if err != nil {
		return "", fmt.Errorf("创建测试根目录失败: %s: %w", base, err)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("解析测试根目录绝对路径失败: %s: %w", root, err)
	}
	fmt.Fprintf(os.Stdout, "TEST_ARTIFACT_DIR=%s\n", absolute)
	return absolute, nil
}

// packageLabel 从测试二进制名称推导当前测试包的标签。
//
// go test 生成的二进制名与包目录名一致（例如 store.test），因此不需要额外传参。
func packageLabel() string {
	name := filepath.Base(os.Args[0])
	name = strings.TrimSuffix(name, ".exe")
	name = strings.TrimSuffix(name, ".test")
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "tests"
	}
	return sanitize(name)
}

// sanitize 把标签转换为可用于文件名的形式。
func sanitize(value string) string {
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			char == '-', char == '_':
			builder.WriteRune(char)
		default:
			builder.WriteRune('-')
		}
	}
	return builder.String()
}
