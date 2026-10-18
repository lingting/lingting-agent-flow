package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// 构建上下文的约束。
const (
	// MaxContextFiles 是单个构建上下文允许的最大文件数。
	MaxContextFiles = 128
	// MaxContextFileBytes 是单个上下文文件的大小上限。
	MaxContextFileBytes = 4 << 20
	// MaxContextBytes 是整个构建上下文的大小上限。
	MaxContextBytes = 16 << 20
)

// ErrInvalidContext 表示构建上下文内容不满足约束。
var ErrInvalidContext = fmt.Errorf("docker: 构建上下文不合法")

// ContextFile 是构建上下文中的一个文件。
type ContextFile struct {
	// RelativePath 是规范化后的相对路径，使用 POSIX 分隔符。
	RelativePath string
	// Content 是文件内容。
	Content []byte
	// Mode 是文件权限位。
	Mode os.FileMode
}

// Context 是一次镜像构建使用的上下文。
//
// 上下文只包含环境配置与构建所需文件，不包含项目源代码、凭据或本机绝对路径。
type Context struct {
	// Files 是按相对路径升序排列的文件列表。
	Files []ContextFile
}

// Hash 计算构建上下文的确定性哈希。
//
// 哈希输入由规范化的相对路径、文件权限与文件内容依次拼接而成，文件按相对路径
// 升序排列，因此同一组文件无论遍历顺序如何都得到相同的哈希。
func (c Context) Hash() string {
	digest := sha256.New()
	files := append([]ContextFile(nil), c.Files...)
	sort.Slice(files, func(i, j int) bool {
		return files[i].RelativePath < files[j].RelativePath
	})
	for _, file := range files {
		writeHashField(digest, file.RelativePath)
		writeHashField(digest, fmt.Sprintf("%04o", file.Mode.Perm()))
		writeHashField(digest, string(file.Content))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// writeHashField 写入带长度前缀的哈希字段。
//
// 长度前缀使字段边界无歧义，避免 "ab"+"c" 与 "a"+"bc" 产生相同哈希输入。
func writeHashField(writer io.Writer, value string) {
	_, _ = fmt.Fprintf(writer, "%d:", len(value))
	_, _ = io.WriteString(writer, value)
	_, _ = io.WriteString(writer, "\n")
}

// NewContext 构造并校验构建上下文。
//
// 校验项：
//   - 相对路径必须已规范化，不能是绝对路径，不能包含 .. 段；
//   - 不允许重复路径；
//   - 单个文件与整体大小不得超过上限；
//   - 内容不得包含本机绝对路径引用或明显的凭据材料。
func NewContext(files []ContextFile) (Context, error) {
	if len(files) == 0 {
		return Context{}, fmt.Errorf("%w: 上下文不能为空", ErrInvalidContext)
	}
	if len(files) > MaxContextFiles {
		return Context{}, fmt.Errorf(
			"%w: 文件数 %d 超过上限 %d",
			ErrInvalidContext,
			len(files),
			MaxContextFiles,
		)
	}
	seen := make(map[string]struct{}, len(files))
	normalized := make([]ContextFile, 0, len(files))
	total := 0
	for _, file := range files {
		cleaned, err := normalizeContextPath(file.RelativePath)
		if err != nil {
			return Context{}, err
		}
		if _, ok := seen[cleaned]; ok {
			return Context{}, fmt.Errorf("%w: 重复文件 %s", ErrInvalidContext, cleaned)
		}
		seen[cleaned] = struct{}{}
		if len(file.Content) > MaxContextFileBytes {
			return Context{}, fmt.Errorf(
				"%w: 文件 %s 大小 %d 超过上限 %d",
				ErrInvalidContext,
				cleaned,
				len(file.Content),
				MaxContextFileBytes,
			)
		}
		total += len(file.Content)
		mode := file.Mode
		if mode == 0 {
			mode = 0o644
		}
		normalized = append(normalized, ContextFile{
			RelativePath: cleaned,
			Content:      file.Content,
			Mode:         mode.Perm(),
		})
	}
	if total > MaxContextBytes {
		return Context{}, fmt.Errorf(
			"%w: 上下文总大小 %d 超过上限 %d",
			ErrInvalidContext,
			total,
			MaxContextBytes,
		)
	}
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].RelativePath < normalized[j].RelativePath
	})
	return Context{Files: normalized}, nil
}

// normalizeContextPath 校验并规范化上下文内的相对路径。
func normalizeContextPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: 相对路径不能为空", ErrInvalidContext)
	}
	if strings.ContainsRune(trimmed, '\\') {
		return "", fmt.Errorf("%w: %q 必须使用 POSIX 分隔符 /", ErrInvalidContext, raw)
	}
	if strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("%w: %q 必须是相对路径", ErrInvalidContext, raw)
	}
	for _, char := range trimmed {
		if char == 0 || char == '\n' || char == '\r' || char == '\t' {
			return "", fmt.Errorf("%w: %q 不能包含控制字符", ErrInvalidContext, raw)
		}
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == ".." {
			return "", fmt.Errorf("%w: %q 不能包含上级目录引用 ..", ErrInvalidContext, raw)
		}
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("%w: %q 不是有效文件路径", ErrInvalidContext, raw)
	}
	if cleaned != trimmed {
		return "", fmt.Errorf("%w: %q 未规范化", ErrInvalidContext, raw)
	}
	return cleaned, nil
}

// sensitivePatterns 是构建上下文中禁止出现的凭据特征。
var sensitivePatterns = []string{
	"BEGIN OPENSSH PRIVATE KEY",
	"BEGIN RSA PRIVATE KEY",
	"BEGIN EC PRIVATE KEY",
	"BEGIN PGP PRIVATE KEY",
	"-----BEGIN PRIVATE KEY-----",
}

// localPathPatterns 是构建上下文中禁止出现的本机绝对路径特征。
var localPathPatterns = []string{
	"file:///",
}

// ValidateContent 检查文件内容是否包含禁止出现的内容。
//
// 该方法只做静态特征检查，不能替代人工审查；它的目标是拦截明显误传的
// 私钥与本机绝对路径，而不是充当完整的机密扫描器。
func ValidateContent(relativePath string, content []byte) error {
	text := string(content)
	for _, pattern := range sensitivePatterns {
		if strings.Contains(text, pattern) {
			return fmt.Errorf(
				"%w: 文件 %s 疑似包含私钥材料",
				ErrInvalidContext,
				relativePath,
			)
		}
	}
	for _, pattern := range localPathPatterns {
		if strings.Contains(text, pattern) {
			return fmt.Errorf(
				"%w: 文件 %s 包含本机路径引用",
				ErrInvalidContext,
				relativePath,
			)
		}
	}
	if strings.Contains(text, dockerSocketPath) {
		return fmt.Errorf(
			"%w: 文件 %s 引用了宿主机 Docker Socket",
			ErrInvalidContext,
			relativePath,
		)
	}
	return nil
}

// dockerSocketPath 是宿主机 Docker Socket 路径。
const dockerSocketPath = "/var/run/docker.sock"

// Validate 对上下文的全部文件执行内容校验。
func (c Context) Validate() error {
	for _, file := range c.Files {
		if err := ValidateContent(file.RelativePath, file.Content); err != nil {
			return err
		}
	}
	return nil
}

// ReadContextDirectory 从本机目录读取构建上下文。
//
// 只读取普通文件，符号链接与目录会被拒绝；隐藏文件与临时文件被忽略，
// 避免把编辑器备份文件带进构建上下文。
func ReadContextDirectory(root string) (Context, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return Context{}, fmt.Errorf("%w: 上下文目录不能为空", ErrInvalidContext)
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return Context{}, fmt.Errorf("%w: 解析上下文目录失败: %v", ErrInvalidContext, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return Context{}, fmt.Errorf("%w: 读取上下文目录失败: %v", ErrInvalidContext, err)
	}
	if !info.IsDir() {
		return Context{}, fmt.Errorf("%w: %s 不是目录", ErrInvalidContext, absolute)
	}
	files := make([]ContextFile, 0, 8)
	walkErr := filepath.WalkDir(absolute, func(current string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if skipContextFile(entry.Name()) {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: 上下文不允许符号链接: %s", ErrInvalidContext, current)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%w: 上下文只允许普通文件: %s", ErrInvalidContext, current)
		}
		relative, relErr := filepath.Rel(absolute, current)
		if relErr != nil {
			return relErr
		}
		content, readErr := os.ReadFile(current)
		if readErr != nil {
			return readErr
		}
		files = append(files, ContextFile{
			RelativePath: filepath.ToSlash(relative),
			Content:      content,
			Mode:         0o644,
		})
		return nil
	})
	if walkErr != nil {
		return Context{}, fmt.Errorf("%w: %v", ErrInvalidContext, walkErr)
	}
	context, err := NewContext(files)
	if err != nil {
		return Context{}, err
	}
	if err := context.Validate(); err != nil {
		return Context{}, err
	}
	return context, nil
}

// skipContextFile 判断文件是否应当被排除在构建上下文之外。
func skipContextFile(name string) bool {
	if name == ".DS_Store" || name == "Thumbs.db" {
		return true
	}
	if strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".swp") {
		return true
	}
	if strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".swp") {
		return true
	}
	return false
}
