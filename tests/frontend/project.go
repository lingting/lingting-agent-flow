package frontend

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/lingting/agentflow/internal/testrunner"
)

// repositoryRoot 返回仓库根目录。
//
// 从当前工作目录向上查找同时含 go.mod 与 lingting-agent-flow-ui 的目录；找不到时返回当前
// 目录，让缺失的前端项目在用例里以"无法执行"如实失败，而不是静默换用其它目录。
func repositoryRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if isRepositoryRoot(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// isRepositoryRoot 判断目录是否同时含 go.mod 与前端项目目录。
func isRepositoryRoot(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, uiProjectDir))
	return err == nil && info.IsDir()
}

// uiRoot 返回前端项目的绝对路径。
func uiRoot() (string, error) {
	root := filepath.Join(repositoryRoot(), uiProjectDir)
	if info, err := os.Stat(filepath.Join(root, "package.json")); err != nil || info.IsDir() {
		return "", fmt.Errorf("前端项目目录 %s 不可用", root)
	}
	return root, nil
}

// requireFrontendTooling 确认 pnpm 与已安装的依赖可用。
//
// 缺依赖时不静默安装：R6 §6 只允许在依赖缺失时执行 pnpm install，是否安装由人工决定，
// 因此这里以"依赖未安装"跳过并写清原因，不把未执行当作通过。
func requireFrontendTooling(c *testrunner.Context, uiDir string, binary string) {
	if _, err := exec.LookPath("pnpm"); err != nil {
		c.Skipf("找不到 pnpm，跳过前端用例: %v", err)
	}
	if _, err := os.Stat(filepath.Join(uiDir, "node_modules", ".bin", binary)); err != nil {
		c.Skipf(
			"前端依赖未安装（%s/node_modules/.bin/%s 不存在），先执行 pnpm install",
			uiDir,
			binary,
		)
	}
}
