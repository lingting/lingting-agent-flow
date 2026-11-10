package testrunner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// GitWorktreeProvider 为用例创建独占的 Git 工作区（git worktree）。
//
// 声明不给 repo 时使用编排进程的工作目录，但该目录必须是 Git 仓库；不给 commit 时使用
// 当前 HEAD。工作区路径固定在本次运行的资源根目录下，释放时按路径删除并 prune。
type GitWorktreeProvider struct {
	root  string
	mu    sync.Mutex
	trees map[string]*gitWorktreeEntry
}

// gitWorktreeEntry 是一条已创建的工作区记录。
type gitWorktreeEntry struct {
	handle Handle
	repo   string
}

// NewGitWorktreeProvider 创建一个以 root 为工作根目录的工作区提供者。
func NewGitWorktreeProvider(root string) *GitWorktreeProvider {
	return &GitWorktreeProvider{root: root, trees: make(map[string]*gitWorktreeEntry)}
}

// Acquire 创建 detached 工作区，句柄给出 path 与解析后的 commit。
func (p *GitWorktreeProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := acquirePrecheck(ctx, decl); err != nil {
		return Handle{}, err
	}
	repo, err := resolveGitRepo(ctx, specValue(decl, "repo"))
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	revision := specValue(decl, "commit")
	if revision == "" {
		revision = "HEAD"
	}
	commit, err := runGit(ctx, repo, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 时解析提交 %q 失败: %w", decl.Name, revision, err)
	}
	token, err := randomToken(4)
	if err != nil {
		return Handle{}, err
	}
	base := resourceShardDir(p.root, ResourceGitWorktree, decl.Name)
	if err := os.MkdirAll(base, 0o750); err != nil {
		return Handle{}, fmt.Errorf("创建资源 %q 的目录失败: %w", decl.Name, err)
	}
	path := filepath.Join(base, safeName(decl.Name)+"-"+token)
	if _, err := runGit(ctx, repo, "worktree", "add", "--detach", path, commit); err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 的工作区失败: %w", decl.Name, err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return Handle{}, fmt.Errorf("工作区 %s 创建后不可用: %w", path, err)
	}
	handle, err := buildHandle(decl, map[string]string{"path": path, "commit": commit}, "")
	if err != nil {
		_, _ = runGit(context.Background(), repo, "worktree", "remove", "--force", path)
		return Handle{}, err
	}
	p.mu.Lock()
	p.trees[path] = &gitWorktreeEntry{handle: handle, repo: repo}
	p.mu.Unlock()
	return handle, nil
}

// Release 删除工作区并清理仓库的 worktree 元数据，可重复调用。
func (p *GitWorktreeProvider) Release(ctx context.Context, handle Handle) error {
	path := handle.PayloadValue("path")
	p.mu.Lock()
	entry := p.trees[path]
	delete(p.trees, path)
	p.mu.Unlock()
	if entry == nil {
		return nil
	}
	removeErr := error(nil)
	if _, err := runGit(ctx, entry.repo, "worktree", "remove", "--force", path); err != nil {
		removeErr = err
	}
	if _, err := runGit(ctx, entry.repo, "worktree", "prune"); err != nil && removeErr == nil {
		removeErr = err
	}
	if removeErr != nil {
		if err := verifyFileAbsent(path); err == nil {
			return nil
		}
		return fmt.Errorf("释放资源 %q 的工作区失败: %w", handle.Name, removeErr)
	}
	return nil
}

// Verify 自证工作区目录在释放后不再存在。
func (p *GitWorktreeProvider) Verify(ctx context.Context, handle Handle) error {
	path := handle.PayloadValue("path")
	p.mu.Lock()
	_, tracked := p.trees[path]
	p.mu.Unlock()
	if tracked {
		return fmt.Errorf("工作区 %s 仍被提供者持有，释放没有生效", path)
	}
	return verifyFileAbsent(path)
}

// Snapshot 返回仍存在的工作区。
func (p *GitWorktreeProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := make([]LiveResource, 0, len(p.trees))
	for path, entry := range p.trees {
		entries = append(entries, LiveResource{
			Handle: entry.handle,
			Detail: "现存的 Git 工作区 " + path,
		})
	}
	return entries, nil
}

// resolveGitRepo 把声明给出的路径解析为 Git 仓库根目录；为空时使用当前工作目录。
func resolveGitRepo(ctx context.Context, configured string) (string, error) {
	candidate := strings.TrimSpace(configured)
	if candidate == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("读取当前工作目录失败: %w", err)
		}
		candidate = cwd
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("解析仓库路径 %q 失败: %w", candidate, err)
	}
	top, err := runGit(ctx, absolute, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("目录 %s 不是 Git 仓库: %w", absolute, err)
	}
	return top, nil
}

// runGit 在指定目录执行一条 git 命令并返回去掉行尾的标准输出。
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf(
			"git %s 失败: %w（输出：%s）",
			strings.Join(args, " "),
			err,
			strings.TrimSpace(string(output)),
		)
	}
	return strings.TrimSpace(string(output)), nil
}
