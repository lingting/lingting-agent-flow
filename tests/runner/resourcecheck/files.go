package resourcecheck

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"

	_ "modernc.org/sqlite" // 用产品同款驱动打开提供者给出的数据库文件
)

// sqliteHeader 是 SQLite 数据库文件的前 16 个字节。
const sqliteHeader = "SQLite format 3\x00"

// SQLiteCase 验证 sqlite-db 提供者：文件是真实可打开的 SQLite 数据库。
var SQLiteCase = testrunner.Register(testrunner.Case{
	ID: "resourcecheck/sqlite-db",
	Resources: []testrunner.ResourceDecl{{
		Name: "db", Type: testrunner.ResourceSQLiteDB, Scope: testrunner.ScopeCaseExclusive,
	}},
	Run: func(c *testrunner.Context) {
		handle, ok := c.Resource("db")
		if !ok {
			c.Fatalf("缺少资源 db 的句柄")
		}
		path := handle.PayloadValue("path")
		header := make([]byte, len(sqliteHeader))
		file, err := os.Open(path)
		if err != nil {
			c.Fatalf("打开数据库文件失败: %v", err)
		}
		_, readErr := file.Read(header)
		_ = file.Close()
		if readErr != nil {
			c.Fatalf("读取数据库文件头失败: %v", readErr)
		}
		if string(header) != sqliteHeader {
			c.Fatalf("文件 %s 不是 SQLite 数据库", path)
		}
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(0)")
		if err != nil {
			c.Fatalf("打开数据库连接失败: %v", err)
		}
		defer func() { _ = db.Close() }()
		if err := db.PingContext(c.Context()); err != nil {
			c.Fatalf("连接数据库失败: %v", err)
		}
		if _, err := db.ExecContext(c.Context(), "CREATE TABLE probe (id INTEGER PRIMARY KEY)"); err != nil {
			c.Fatalf("建表失败: %v", err)
		}
		c.Logf("数据库资源可用：%s", path)
	},
})

// GitWorktreeCase 验证 git-worktree 提供者：工作区存在且 HEAD 与句柄 commit 一致。
var GitWorktreeCase = testrunner.Register(testrunner.Case{
	ID:      "resourcecheck/git-worktree",
	Timeout: 60 * time.Second,
	Resources: []testrunner.ResourceDecl{{
		Name: "tree", Type: testrunner.ResourceGitWorktree, Scope: testrunner.ScopeCaseExclusive,
	}},
	Run: func(c *testrunner.Context) {
		handle, ok := c.Resource("tree")
		if !ok {
			c.Fatalf("缺少资源 tree 的句柄")
		}
		path := handle.PayloadValue("path")
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			c.Fatalf("工作区 %s 不可用: %v", path, err)
		}
		if _, err := os.Stat(path + string(os.PathSeparator) + ".git"); err != nil {
			c.Fatalf("工作区 %s 缺少 .git 元数据: %v", path, err)
		}
		head, err := gitOutput(c.Context(), path, "rev-parse", "HEAD")
		if err != nil {
			c.Fatalf("读取工作区 HEAD 失败: %v", err)
		}
		if head != handle.PayloadValue("commit") {
			c.Fatalf("工作区 HEAD %s 与句柄 commit %s 不一致", head, handle.PayloadValue("commit"))
		}
		c.Logf("Git 工作区可用：%s（%s）", path, head)
	},
})

// gitOutput 在指定目录执行一条 git 命令并返回去掉行尾的输出。
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(runCtx, "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
