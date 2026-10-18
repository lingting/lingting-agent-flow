package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// 任务分支与 Git 引用相关的固定约定。
const (
	// TaskBranchPrefix 是任务分支的固定前缀。
	//
	// 任务分支统一为 agent/<task-id>，由 TaskBranchName 集中生成，
	// SourceProvider、Runner 与 CLI 不得各自拼接分支名。
	TaskBranchPrefix = "agent/"
	// DefaultBaseBranch 是识别基础分支时的首选候选。
	DefaultBaseBranch = "main"
	// FallbackBaseBranch 是识别基础分支时的次选候选。
	FallbackBaseBranch = "master"
	// OriginHeadRef 是远程默认分支的引用名。
	OriginHeadRef = "refs/remotes/origin/HEAD"
	// maxGitRefLength 是单个 Git 引用名的长度上限。
	maxGitRefLength = 255
)

// gitRefSegmentPattern 约束单个引用片段允许的字符。
//
// 该规则比 Git 自身宽松一些，但排除了空白、shell 元字符、~ ^ : ? * [ \ 与 ..，
// 因此引用名既不能被 shell 解释，也不会与 Git 的引用规则冲突。
var gitRefSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// taskIDPattern 约束任务标识可以出现在分支名中的形式。
//
// 任务标识由 AgentFlow 生成，只包含小写十六进制字符；这里额外允许连字符，
// 使人工指定的标识也能使用，同时排除任何可能被 shell 或 Git 解释的字符。
var taskIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidateTaskIDForBranch 校验任务标识可以安全用于分支名。
func ValidateTaskIDForBranch(taskID string) error {
	trimmed := strings.TrimSpace(taskID)
	if trimmed == "" {
		return invalidField("TaskBranch", "task_id", "任务标识不能为空")
	}
	if len(trimmed) > 64 {
		return invalidField("TaskBranch", "task_id", "任务标识长度不能超过 64 个字符")
	}
	if !taskIDPattern.MatchString(trimmed) {
		return invalidField(
			"TaskBranch",
			"task_id",
			"任务标识只能包含小写字母、数字与连字符，且不能以连字符开头",
		)
	}
	return nil
}

// TaskBranchName 返回任务的约定分支名 agent/<task-id>。
//
// 该函数是任务分支名的唯一生成入口：任何需要分支名的组件都必须调用它，
// 避免不同模块生成不一致的分支名。
func TaskBranchName(taskID string) (string, error) {
	if err := ValidateTaskIDForBranch(taskID); err != nil {
		return "", err
	}
	return TaskBranchPrefix + strings.TrimSpace(taskID), nil
}

// ValidateGitBranchName 校验分支名是合法的 Git 引用名。
//
// 只接受可以安全写入命令行的形式：不允许以 - 或 / 开头、不允许连续斜杠、
// 不允许以 .lock 结尾，也不允许包含 Git 明确禁止的字符序列。
func ValidateGitBranchName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return invalidField("GitBranch", "name", "分支名不能为空")
	}
	if len(trimmed) > maxGitRefLength {
		return invalidField("GitBranch", "name", "分支名长度不能超过 255 个字符")
	}
	if trimmed != name {
		return invalidField("GitBranch", "name", "分支名首尾不能包含空白")
	}
	if strings.HasPrefix(trimmed, "-") {
		return invalidField("GitBranch", "name", "分支名不能以 - 开头")
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasSuffix(trimmed, "/") {
		return invalidField("GitBranch", "name", "分支名不能以 / 开头或结尾")
	}
	if strings.Contains(trimmed, "//") {
		return invalidField("GitBranch", "name", "分支名不能包含连续的 /")
	}
	if strings.Contains(trimmed, "..") {
		return invalidField("GitBranch", "name", "分支名不能包含 ..")
	}
	if strings.Contains(trimmed, "@{") {
		return invalidField("GitBranch", "name", "分支名不能包含 @{")
	}
	if strings.HasSuffix(trimmed, ".lock") {
		return invalidField("GitBranch", "name", "分支名不能以 .lock 结尾")
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" {
			return invalidField("GitBranch", "name", "分支名不能包含空的路径片段")
		}
		if segment == "." || segment == ".." {
			return invalidField("GitBranch", "name", "分支名不能包含 . 或 .. 片段")
		}
		if strings.HasSuffix(segment, ".lock") {
			return invalidField("GitBranch", "name", "分支名片段不能以 .lock 结尾")
		}
		if !gitRefSegmentPattern.MatchString(segment) {
			return invalidField(
				"GitBranch",
				"name",
				"分支名片段 "+segment+" 含有非法字符",
			)
		}
	}
	return nil
}

// GitRepositoryURL 是经过校验与脱敏的仓库地址。
type GitRepositoryURL struct {
	// raw 是原始仓库地址，只在执行 Clone 时使用。
	raw string
	// display 是脱敏后的地址，可以写入日志与诊断信息。
	display string
	// anonymous 表示该地址不需要额外凭据即可访问。
	anonymous bool
}

// Raw 返回原始仓库地址。
func (u GitRepositoryURL) Raw() string {
	return u.raw
}

// Display 返回脱敏后的仓库地址。
//
// 脱敏后的地址不包含用户名与口令片段，可以安全写入日志、数据库与错误信息。
func (u GitRepositoryURL) Display() string {
	return u.display
}

// Anonymous 表示该地址按公开仓库处理，不需要额外凭据。
func (u GitRepositoryURL) Anonymous() bool {
	return u.anonymous
}

// IsZero 判断地址是否为空。
func (u GitRepositoryURL) IsZero() bool {
	return u.raw == ""
}

// ParseGitRepositoryURL 校验并解析仓库地址。
//
// 第一版只接受两种无需内嵌凭据的地址形式：
//   - ssh://[user@]host[:port]/path
//   - https://host/path
//
// 明确拒绝把用户名口令写进 URL 的形式（user:token@host），因为该内容会进入
// 命令行、进程参数与错误信息，无法安全脱敏。
func ParseGitRepositoryURL(raw string) (GitRepositoryURL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return GitRepositoryURL{}, invalidField("GitRepository", "url", "仓库地址不能为空")
	}
	if len(trimmed) > 2048 {
		return GitRepositoryURL{}, invalidField("GitRepository", "url", "仓库地址过长")
	}
	for _, char := range trimmed {
		if char == 0 || char == '\n' || char == '\r' || char == '\t' || char == ' ' {
			return GitRepositoryURL{}, invalidField(
				"GitRepository",
				"url",
				"仓库地址不能包含空白或控制字符",
			)
		}
	}
	scheme, rest, ok := strings.Cut(trimmed, "://")
	if !ok {
		return GitRepositoryURL{}, invalidField(
			"GitRepository",
			"url",
			"仓库地址必须以 ssh:// 或 https:// 开头",
		)
	}
	switch strings.ToLower(scheme) {
	case "ssh":
		return parseSSHRepositoryURL(trimmed, rest)
	case "https":
		return parseHTTPSRepositoryURL(trimmed, rest)
	default:
		return GitRepositoryURL{}, invalidField(
			"GitRepository",
			"url",
			"不支持的仓库地址协议 "+scheme+"，只允许 ssh 与 https",
		)
	}
}

// parseSSHRepositoryURL 解析 ssh:// 形式的仓库地址。
func parseSSHRepositoryURL(raw, rest string) (GitRepositoryURL, error) {
	authority, pathPart, ok := strings.Cut(rest, "/")
	if !ok || strings.TrimSpace(pathPart) == "" {
		return GitRepositoryURL{}, invalidField("GitRepository", "url", "ssh 地址缺少仓库路径")
	}
	if authority == "" {
		return GitRepositoryURL{}, invalidField("GitRepository", "url", "ssh 地址缺少主机")
	}
	userPart, hostPart := splitAuthorityUser(authority)
	if strings.Contains(hostPart, ":") {
		// 端口形式保留在原始地址中，但脱敏展示时只保留主机。
		host, port, portOK := strings.Cut(hostPart, ":")
		if !portOK || !isNumeric(port) || strings.TrimSpace(host) == "" {
			return GitRepositoryURL{}, invalidField("GitRepository", "url", "ssh 地址端口非法")
		}
		hostPart = host
	}
	if err := validateRepositoryHost(hostPart); err != nil {
		return GitRepositoryURL{}, err
	}
	if strings.Contains(userPart, ":") {
		return GitRepositoryURL{}, invalidField(
			"GitRepository",
			"url",
			"仓库地址不能包含内嵌口令，请使用独立的凭据机制",
		)
	}
	return GitRepositoryURL{
		raw:       raw,
		display:   "ssh://" + hostPart + "/" + strings.TrimPrefix(pathPart, "/"),
		anonymous: false,
	}, nil
}

// parseHTTPSRepositoryURL 解析 https:// 形式的仓库地址。
func parseHTTPSRepositoryURL(raw, rest string) (GitRepositoryURL, error) {
	authority, pathPart, ok := strings.Cut(rest, "/")
	if !ok || strings.TrimSpace(pathPart) == "" {
		return GitRepositoryURL{}, invalidField("GitRepository", "url", "https 地址缺少仓库路径")
	}
	if authority == "" {
		return GitRepositoryURL{}, invalidField("GitRepository", "url", "https 地址缺少主机")
	}
	userPart, hostPart := splitAuthorityUser(authority)
	if strings.Contains(userPart, ":") || strings.TrimSpace(userPart) != "" {
		return GitRepositoryURL{}, invalidField(
			"GitRepository",
			"url",
			"仓库地址不能包含内嵌凭据，请使用独立的凭据机制",
		)
	}
	if strings.Contains(hostPart, ":") {
		host, port, portOK := strings.Cut(hostPart, ":")
		if !portOK || !isNumeric(port) || strings.TrimSpace(host) == "" {
			return GitRepositoryURL{}, invalidField("GitRepository", "url", "https 地址端口非法")
		}
		hostPart = host
	}
	if err := validateRepositoryHost(hostPart); err != nil {
		return GitRepositoryURL{}, err
	}
	return GitRepositoryURL{
		raw:       raw,
		display:   "https://" + hostPart + "/" + strings.TrimPrefix(pathPart, "/"),
		anonymous: true,
	}, nil
}

// splitAuthorityUser 拆分 authority 中的用户与主机部分。
func splitAuthorityUser(authority string) (string, string) {
	if index := strings.LastIndex(authority, "@"); index >= 0 {
		return authority[:index], authority[index+1:]
	}
	return "", authority
}

// validateRepositoryHost 校验仓库主机名。
func validateRepositoryHost(host string) error {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return invalidField("GitRepository", "url", "仓库主机不能为空")
	}
	if strings.HasPrefix(trimmed, "-") {
		return invalidField("GitRepository", "url", "仓库主机不能以 - 开头")
	}
	for _, char := range trimmed {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '.' || char == '-' || char == '_' || char == '[' || char == ']':
		default:
			return invalidField("GitRepository", "url", "仓库主机含有非法字符")
		}
	}
	return nil
}

// isNumeric 判断字符串是否只包含数字。
func isNumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// NormalizeGitBranch 返回去掉常见前缀后的分支名。
//
// 调用方可能给出 main、refs/heads/main 或 origin/main，这里统一规范化为本地分支名。
func NormalizeGitBranch(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	for _, prefix := range []string{"refs/heads/", "refs/remotes/origin/", "origin/"} {
		trimmed = strings.TrimPrefix(trimmed, prefix)
	}
	if err := ValidateGitBranchName(trimmed); err != nil {
		return "", err
	}
	return trimmed, nil
}

// GitBranchSelection 描述基础分支的确定方式。
type GitBranchSelection string

// 基础分支确定方式取值。
const (
	// GitBranchSelectionExplicit 表示调用方显式指定了基础分支。
	GitBranchSelectionExplicit GitBranchSelection = "explicit"
	// GitBranchSelectionOriginHead 表示依据 refs/remotes/origin/HEAD 确定。
	GitBranchSelectionOriginHead GitBranchSelection = "origin_head"
	// GitBranchSelectionMain 表示依据候选 main 确定。
	GitBranchSelectionMain GitBranchSelection = "candidate_main"
	// GitBranchSelectionMaster 表示依据候选 master 确定。
	GitBranchSelectionMaster GitBranchSelection = "candidate_master"
)

// Validate 校验基础分支确定方式是否受支持。
func (s GitBranchSelection) Validate() error {
	switch s {
	case GitBranchSelectionExplicit,
		GitBranchSelectionOriginHead,
		GitBranchSelectionMain,
		GitBranchSelectionMaster:
		return nil
	default:
		return invalidField("GitBranch", "selection", "未知的基础分支确定方式 "+string(s))
	}
}

// SelectBaseBranch 按约定优先级从候选集合中确定基础分支。
//
// 顺序为 origin/HEAD 指向的分支、main、master。无法确定时返回错误，由调用方
// 要求用户显式指定基础分支，绝不把任意分支静默当作主分支。
func SelectBaseBranch(originHead string, candidates []string) (string, GitBranchSelection, error) {
	if normalized, err := NormalizeGitBranch(originHead); err == nil && normalized != "" {
		return normalized, GitBranchSelectionOriginHead, nil
	}
	available := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if normalized, err := NormalizeGitBranch(candidate); err == nil {
			available[normalized] = struct{}{}
		}
	}
	if _, ok := available[DefaultBaseBranch]; ok {
		return DefaultBaseBranch, GitBranchSelectionMain, nil
	}
	if _, ok := available[FallbackBaseBranch]; ok {
		return FallbackBaseBranch, GitBranchSelectionMaster, nil
	}
	return "", "", fmt.Errorf(
		"%w: 无法确定仓库基础分支（已尝试 origin/HEAD、%s、%s），请显式指定基础分支",
		ErrInvalidValue,
		DefaultBaseBranch,
		FallbackBaseBranch,
	)
}
