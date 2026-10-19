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
// ExecutionHost 是一台通过 SSH 管理的远程 Linux 执行节点。
//
// 本模型只保存连接目标、认证方式种类与工作空间配置，不保存密码、私钥原文或
// 私钥口令：认证材料只能通过内存中的凭据提供器在连接时提供。私钥认证只保存
// 本机私钥文件路径，AgentFlow 不会复制或修改该文件。
type ExecutionHost struct {
	ID            ID     `json:"id"`
	Name          string `json:"name"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	WorkspaceRoot string `json:"workspace_root"`
	IsDefault     bool   `json:"is_default"`
	Enabled       bool   `json:"enabled"`
	// AuthMethod 是认证方式，取值见 AuthMethod 常量。
	AuthMethod AuthMethod `json:"auth_method"`
	// PrivateKeyPath 是本机私钥文件路径，仅私钥认证使用。
	PrivateKeyPath string `json:"private_key_path,omitempty"`
	// HostKeyAlgorithm 是已确认信任的主机密钥算法。
	HostKeyAlgorithm string `json:"host_key_algorithm,omitempty"`
	// HostKeyFingerprint 是已确认信任的主机密钥 SHA-256 指纹。
	HostKeyFingerprint string `json:"host_key_fingerprint,omitempty"`
	// HostKeyTrustedAt 是主机密钥被确认信任的时间，未信任时为空。
	HostKeyTrustedAt *time.Time `json:"host_key_trusted_at,omitempty"`
	// LastConnectStatus 是最近一次连接测试的结果类别。
	LastConnectStatus ConnectStatus `json:"last_connect_status"`
	// LastConnectError 是最近一次连接失败的分类代码，不包含敏感内容。
	LastConnectError string `json:"last_connect_error,omitempty"`
	// LastConnectedAt 是最近一次连接测试的时间，未测试时为空。
	// LastConnectedAt 是最近一次连接测试的时间，未测试时为空。
	LastConnectedAt *time.Time `json:"last_connected_at,omitempty"`
	// ValidationStatus 是环境预检的总体状态。
	//
	// 取值见 ValidationStatus 常量；只有 normal 允许用于真实远程任务执行。
	// 该字段不参与 Validate 的取值校验：数据库不声明枚举白名单，读取层必须原样
	// 保留未知取值，不能把它映射成某个已知状态。
	ValidationStatus ValidationStatus `json:"validation_status"`
	// ValidationMessage 是最新校验结果的摘要或失败原因，不包含凭据内容。
	ValidationMessage string `json:"validation_message,omitempty"`
	// ValidationDetails 是各项校验的结构化结果，未完成校验时为空。
	ValidationDetails *ValidationDetails `json:"validation_details,omitempty"`
	// ValidationStartedAt 是最近一次校验的开始时间。
	ValidationStartedAt *time.Time `json:"validation_started_at,omitempty"`
	// ValidationCompletedAt 是最近一次校验的结束时间，未完成时为空。
	ValidationCompletedAt *time.Time `json:"validation_completed_at,omitempty"`
	// ValidationGeneration 是校验代次，用于阻止旧结果覆盖新配置。
	ValidationGeneration int64     `json:"validation_generation"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
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
	options ...HostOption,
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
		// 认证方式固定为私钥：本版本不再支持 SSH Agent 与密码认证。
		AuthMethod: AuthMethodPrivateKey,
		// 新建节点尚未完成预检，由服务端统一置为 checking；客户端不能指定为 normal。
		// 代次从 1 开始，使首次校验拥有明确的代次基线。
		ValidationStatus:     ValidationStatusChecking,
		ValidationGeneration: 1,
		CreatedAt:            createdAt,
		UpdatedAt:            createdAt,
	}
	for _, option := range options {
		if option != nil {
			option(&entity)
		}
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
	if err := validateWorkspaceRoot(h.WorkspaceRoot); err != nil {
		return err
	}
	if err := validateAuthConfig(h); err != nil {
		return err
	}
	if err := validateHostKeyFingerprint(h.HostKeyFingerprint); err != nil {
		return err
	}
	if err := h.LastConnectStatus.Validate(); err != nil {
		return err
	}
	if err := validateConnectErrorCode(h.LastConnectError); err != nil {
		return err
	}
	// validation_status 不校验取值：数据库不声明枚举白名单，读取层必须保留未知值。
	return nil
}

// RecordConnectResult 记录最近一次连接测试的结果。
//
// errorCode 应当是稳定的分类代码（例如 host_key_unknown、auth_failed），
// 而不是包含主机名、用户名或凭据的完整错误消息。
func (h *ExecutionHost) RecordConnectResult(status ConnectStatus, errorCode string, at time.Time) error {
	if err := status.Validate(); err != nil {
		return err
	}
	if err := validateConnectErrorCode(errorCode); err != nil {
		return err
	}
	if status == ConnectStatusSuccess {
		errorCode = ""
	}
	recordedAt := normalizeTime(at)
	h.LastConnectStatus = status
	h.LastConnectError = errorCode
	h.LastConnectedAt = &recordedAt
	h.UpdatedAt = recordedAt
	return nil
}

// TrustHostKey 记录用户确认信任的主机密钥信息。
func (h *ExecutionHost) TrustHostKey(algorithm, fingerprint string, at time.Time) error {
	if err := validateHostKeyFingerprint(fingerprint); err != nil {
		return err
	}
	trustedAt := normalizeTime(at)
	h.HostKeyAlgorithm = strings.TrimSpace(algorithm)
	h.HostKeyFingerprint = strings.TrimSpace(fingerprint)
	h.HostKeyTrustedAt = &trustedAt
	h.UpdatedAt = trustedAt
	return nil
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
