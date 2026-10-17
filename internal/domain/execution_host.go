package domain

import (
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// SSH 执行节点的默认配置。
const (
	// DefaultSSHPort 是未显式指定端口时使用的 SSH 端口。
	DefaultSSHPort = 22

	// DefaultWorkspaceRoot 是未显式指定时使用的远程工作空间路径。
	//
	// 路径中的 ~ 指远程 SSH 登录用户的 Home，不是本机用户的 Home；
	// 后续远程操作必须由远程用户环境解析该路径并转换为绝对路径。
	DefaultWorkspaceRoot = "~/.lingting/agentflow"
)

// ExecutionHost 是一台通过 SSH 管理的远程 Linux 执行节点。
//
// 本模型只保存连接目标与工作空间配置，不保存密码、私钥或任何认证材料：
// 认证方式在后续阶段单独设计，避免数据模型过早绑定某一种认证方式。
type ExecutionHost struct {
	ID            ID        `json:"id"`
	Name          string    `json:"name"`
	Host          string    `json:"host"`
	Port          int       `json:"port"`
	Username      string    `json:"username"`
	WorkspaceRoot string    `json:"workspace_root"`
	IsDefault     bool      `json:"is_default"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// NewExecutionHost 创建并校验执行节点。
//
// id 为空时生成随机标识；port 为 0 时使用 DefaultSSHPort；
// workspaceRoot 为空时使用 DefaultWorkspaceRoot。新建节点默认启用。
func NewExecutionHost(
	id ID,
	name, host string,
	port int,
	username, workspaceRoot string,
	at time.Time,
) (ExecutionHost, error) {
	if id.IsEmpty() {
		id = NewID()
	}
	if port == 0 {
		port = DefaultSSHPort
	}
	createdAt := normalizeTime(at)
	entity := ExecutionHost{
		ID:            id,
		Name:          strings.TrimSpace(name),
		Host:          NormalizeHostAddress(host),
		Port:          port,
		Username:      strings.TrimSpace(username),
		WorkspaceRoot: normalizeWorkspaceRoot(workspaceRoot),
		Enabled:       true,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
	if err := entity.Validate(); err != nil {
		return ExecutionHost{}, err
	}
	return entity, nil
}

// Validate 校验执行节点的全部字段。
func (h ExecutionHost) Validate() error {
	if err := h.ID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(h.Name) == "" {
		return invalidField("ExecutionHost", "name", "节点名称不能为空")
	}
	if err := validateHostAddress(h.Host); err != nil {
		return err
	}
	if h.Port < 1 || h.Port > 65535 {
		return invalidField("ExecutionHost", "port", "端口必须在 1-65535 之间")
	}
	if err := validateSSHUsername(h.Username); err != nil {
		return err
	}
	return validateWorkspaceRoot(h.WorkspaceRoot)
}

// Address 返回 SSH 连接使用的 host:port 形式地址，IPv6 会自动加方括号。
func (h ExecutionHost) Address() string {
	return net.JoinHostPort(h.Host, strconv.Itoa(h.Port))
}

// NormalizeHostAddress 去掉主机地址两侧空白与 IPv6 的方括号。
//
// 节点只保存主机地址本身，不含端口，端口由 Port 字段单独保存。
func NormalizeHostAddress(host string) string {
	trimmed := strings.TrimSpace(host)
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		inner := strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")
		if net.ParseIP(inner) != nil {
			return inner
		}
	}
	return trimmed
}

// normalizeWorkspaceRoot 统一工作空间路径的书写形式。
func normalizeWorkspaceRoot(root string) string {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return DefaultWorkspaceRoot
	}
	for len(trimmed) > 1 && strings.HasSuffix(trimmed, "/") {
		trimmed = strings.TrimSuffix(trimmed, "/")
	}
	return trimmed
}

// validateHostAddress 校验主机地址是合法的 IP 或主机名。
func validateHostAddress(host string) error {
	if strings.TrimSpace(host) == "" {
		return invalidField("ExecutionHost", "host", "主机地址不能为空")
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	if len(host) > 253 {
		return invalidField("ExecutionHost", "host", "主机名长度不能超过 253 个字符")
	}
	for _, label := range strings.Split(host, ".") {
		if err := validateHostLabel(label); err != nil {
			return err
		}
	}
	return nil
}

// validateHostLabel 校验主机名的单个标签。
func validateHostLabel(label string) error {
	if label == "" {
		return invalidField("ExecutionHost", "host", "主机名不能包含空标签")
	}
	if len(label) > 63 {
		return invalidField("ExecutionHost", "host", "主机名的单个标签不能超过 63 个字符")
	}
	if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return invalidField("ExecutionHost", "host", "主机名标签不能以连字符开头或结尾")
	}
	for _, char := range label {
		if unicode.IsLetter(char) && char < unicode.MaxASCII {
			continue
		}
		if unicode.IsDigit(char) && char < unicode.MaxASCII {
			continue
		}
		if char == '-' {
			continue
		}
		return invalidField("ExecutionHost", "host", "主机名只能包含字母、数字、连字符与点")
	}
	return nil
}

// validateSSHUsername 校验 SSH 登录用户名。
func validateSSHUsername(username string) error {
	if strings.TrimSpace(username) == "" {
		return invalidField("ExecutionHost", "username", "SSH 登录用户名不能为空")
	}
	if len(username) > 64 {
		return invalidField("ExecutionHost", "username", "SSH 登录用户名不能超过 64 个字符")
	}
	for _, char := range username {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return invalidField("ExecutionHost", "username", "SSH 登录用户名不能包含空白或控制字符")
		}
		if strings.ContainsRune(`:@/\~`, char) {
			return invalidField("ExecutionHost", "username", "SSH 登录用户名不能包含 : @ / \\ ~")
		}
	}
	return nil
}

// validateWorkspaceRoot 校验远程工作空间路径。
//
// 远程节点是 Linux，因此只接受 POSIX 绝对路径或以 ~/ 开头的路径；
// 这里只做书写层面的检查，目录创建与删除时必须再做路径安全检查。
func validateWorkspaceRoot(root string) error {
	if strings.TrimSpace(root) == "" {
		return invalidField("ExecutionHost", "workspace_root", "远程工作空间路径不能为空")
	}
	if strings.ContainsRune(root, '\\') {
		return invalidField("ExecutionHost", "workspace_root", "远程工作空间必须使用 POSIX 分隔符 /")
	}
	for _, char := range root {
		if unicode.IsControl(char) {
			return invalidField("ExecutionHost", "workspace_root", "远程工作空间不能包含控制字符")
		}
	}
	switch {
	case root == "/" || root == "~":
		return invalidField("ExecutionHost", "workspace_root", "远程工作空间不能是根目录或 Home 目录本身")
	case strings.HasPrefix(root, "~/"):
	case strings.HasPrefix(root, "/"):
	default:
		return invalidField("ExecutionHost", "workspace_root", "远程工作空间必须以 / 或 ~/ 开头")
	}
	if strings.Contains(root, "//") {
		return invalidField("ExecutionHost", "workspace_root", "远程工作空间不能包含连续分隔符 //")
	}
	for _, segment := range strings.Split(root, "/") {
		if segment == ".." {
			return invalidField("ExecutionHost", "workspace_root", "远程工作空间不能包含上级目录引用 ..")
		}
	}
	return nil
}
