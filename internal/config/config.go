// Package config 定义 AgentFlow 的配置结构与校验规则。
//
// 本包只负责把 YAML 配置转换为结构化数据并校验其合法性，
// 不涉及 Pi 安装检查、模型调用或权限实施。
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/logging"
)

// HTTP 服务默认监听地址与端口。
//
// 默认端口按构建类型区分：本地开发构建与前端开发服务器（26381）分开，正式发布构建再用另一个
// 端口，避免同一台机器上的开发服务与已发布服务争用同一端口。
const (
	// DefaultServerHost 是未配置监听地址时使用的回环地址。
	DefaultServerHost = "127.0.0.1"
	// DefaultServerPort 是本地开发构建（未注入正式版本号）的默认监听端口。
	DefaultServerPort = 26380
	// ReleaseServerPort 是正式发布构建的默认监听端口。
	ReleaseServerPort = 26382
	// DefaultShutdownTimeout 是未配置优雅关闭超时时使用的等待时间。
	DefaultShutdownTimeout = 10 * time.Second

	// DefaultAppName 是未配置应用名时使用的名称。
	DefaultAppName = "agentflow"
	// HomeEnv 是指定数据主目录的环境变量名：测试环境用它替换默认位置。
	HomeEnv = "AGENTFLOW_HOME"
	// DefaultHomeDirName 是未设置 AGENTFLOW_HOME 时相对用户主目录的数据主目录。
	DefaultHomeDirName = ".lingting/agentflow"
	// DefaultDatabaseFileName 是数据主目录下的数据库文件名。
	DefaultDatabaseFileName = "agentflow.db"
	// DefaultWorktreeDirName 是数据主目录下的工作区目录名。
	DefaultWorktreeDirName = "worktrees"
	// DefaultMaxConcurrentTasks 是未配置并发上限时使用的并发数。
	DefaultMaxConcurrentTasks = 3
	// DefaultSourcePath 表示配置不是来自文件，而是内置默认值。
	DefaultSourcePath = "（内置默认值）"
)

// ServerPortFor 返回给定构建类型下未配置监听端口时使用的默认端口。
func ServerPortFor(isRelease bool) int {
	if isRelease {
		return ReleaseServerPort
	}
	return DefaultServerPort
}

// HomeDir 返回数据主目录：数据库、工作区、Session 与后台日志都落在它下面。
//
// AGENTFLOW_HOME 设置时优先使用它（测试环境据此替换默认位置），相对值按当前工作目录解析；
// 未设置时使用 <用户主目录>/.lingting/agentflow。
func HomeDir() (string, error) {
	if value := strings.TrimSpace(os.Getenv(HomeEnv)); value != "" {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return "", fmt.Errorf("config: 解析 %s 失败: %q: %w", HomeEnv, value, err)
		}
		return absolute, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: 解析用户主目录失败（可用 %s 指定数据主目录）: %w", HomeEnv, err)
	}
	return filepath.Join(home, filepath.FromSlash(DefaultHomeDirName)), nil
}

// Default 返回内置默认配置，使程序可以在没有配置文件的情况下启动。
//
// 只填无法为空的必需字段与数据路径，其余字段保持零值：监听地址、端口、优雅关闭超时与 SSH
// 超时的零值都有各自的默认语义，不在这里重复展开，避免第二份默认值来源。
func Default() (*Config, error) {
	home, err := HomeDir()
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		SourcePath: DefaultSourcePath,
		App: AppConfig{
			Name:     DefaultAppName,
			LogLevel: logging.DefaultLevel,
		},
		Scheduler: SchedulerConfig{MaxConcurrentTasks: DefaultMaxConcurrentTasks},
	}
	cfg.applyDataDefaults(home)
	return cfg, nil
}

// applyDataDefaults 把未配置的数据路径补成数据主目录下的默认位置。
//
// 与 server.host、server.port 一样，空值表示使用默认位置：配置文件不必重复写默认路径。
func (c *Config) applyDataDefaults(home string) {
	if strings.TrimSpace(c.Storage.DatabasePath) == "" {
		c.Storage.DatabasePath = filepath.Join(home, DefaultDatabaseFileName)
	}
	if strings.TrimSpace(c.Workspace.WorktreeRoot) == "" {
		c.Workspace.WorktreeRoot = filepath.Join(home, DefaultWorktreeDirName)
	}
}

// resolvePaths 把配置中的相对路径按数据主目录解析为绝对路径。
//
// 相对路径不再按进程工作目录解析：这样无论从哪个目录启动服务，数据都落在同一个数据主目录下。
// 绝对路径保持原样，空值保持为空（各字段的零值语义由使用方决定）。
func (c *Config) resolvePaths(home string) {
	c.Storage.DatabasePath = resolvePath(home, c.Storage.DatabasePath)
	c.Workspace.WorktreeRoot = resolvePath(home, c.Workspace.WorktreeRoot)
	c.SSH.KnownHostsPath = resolvePath(home, c.SSH.KnownHostsPath)
}

// resolvePath 按数据主目录解析单个配置路径。
func resolvePath(home, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(home, value)
}

// Config 是 AgentFlow 的完整配置。
type Config struct {
	// SourcePath 是本次加载使用的配置文件路径，由加载器写入，不参与 YAML 解析。
	SourcePath string `yaml:"-"`

	App       AppConfig       `yaml:"app"`
	Server    ServerConfig    `yaml:"server"`
	Storage   StorageConfig   `yaml:"storage"`
	Workspace WorkspaceConfig `yaml:"workspace"`
	Scheduler SchedulerConfig `yaml:"scheduler"`
	SSH       SSHConfig       `yaml:"ssh"`
}

// AppConfig 描述应用自身的标识与日志设置。
type AppConfig struct {
	Name     string `yaml:"name"`
	LogLevel string `yaml:"log_level"`
}

// ServerConfig 描述本地 HTTP 服务的监听设置。
//
// 默认只监听回环地址：Web 页面与 API 同源，未实现远程访问认证前不对外开放网卡。
type ServerConfig struct {
	// Host 是监听地址，为空时使用 DefaultServerHost。
	Host string `yaml:"host"`
	// Port 是监听端口，必须大于 0；端口是否被占用由启动阶段判断。
	Port int `yaml:"port"`
	// ShutdownTimeoutSeconds 是优雅关闭的最长等待秒数，零值使用默认值。
	ShutdownTimeoutSeconds int `yaml:"shutdown_timeout_seconds"`
}

// StorageConfig 描述持久化位置。
type StorageConfig struct {
	// DatabasePath 是数据库文件路径。
	//
	// 未配置时使用数据主目录下的 agentflow.db；相对路径按数据主目录解析。
	DatabasePath string `yaml:"database_path"`
}

// WorkspaceConfig 描述任务工作区的存放位置。
type WorkspaceConfig struct {
	// WorktreeRoot 是任务工作区根目录。
	//
	// 未配置时使用数据主目录下的 worktrees；相对路径按数据主目录解析。
	WorktreeRoot string `yaml:"worktree_root"`
}

// SchedulerConfig 描述调度与重试上限。
type SchedulerConfig struct {
	MaxConcurrentTasks int `yaml:"max_concurrent_tasks"`
	MaxRetriesPerStep  int `yaml:"max_retries_per_step"`
}

// SSHConfig 描述 SSH 连接层的非机密配置。
//
// 这里只保存文件路径与超时，不保存任何凭据：密码与私钥口令只能在连接时
// 通过内存中的凭据提供器传入。
type SSHConfig struct {
	// KnownHostsPath 是只读 known_hosts 文件路径，相对路径按数据主目录解析。
	//
	// 为空时复用本机用户 OpenSSH 的 known_hosts；该文件只读取，不会被修改。
	KnownHostsPath string `yaml:"known_hosts_path"`
	// ConnectTimeoutSeconds 是 TCP 连接与 SSH 认证的超时秒数，零值使用默认值。
	ConnectTimeoutSeconds int `yaml:"connect_timeout_seconds"`
}

// ValidationError 汇总一份配置中的全部校验问题。
type ValidationError struct {
	// Path 是配置来源文件路径。
	Path string
	// Issues 是全部校验问题，每项以配置字段路径开头。
	Issues []string
}

// Error 实现 error 接口。
func (e *ValidationError) Error() string {
	var builder strings.Builder
	builder.WriteString("配置校验失败: ")
	builder.WriteString(e.Path)
	for _, issue := range e.Issues {
		builder.WriteString("\n  - ")
		builder.WriteString(issue)
	}
	return builder.String()
}

// issues 收集校验问题，使一次校验可以报告全部问题。
type issues struct {
	list []string
}

// add 记录一条校验问题。
func (i *issues) add(field, reason string) {
	i.list = append(i.list, field+": "+reason)
}

// empty 判断是否没有收集到问题。
func (i *issues) empty() bool {
	return len(i.list) == 0
}

// Validate 校验整份配置，返回汇总后的 ValidationError。
func (c *Config) Validate() error {
	var problems issues
	c.App.validate(&problems)
	c.Server.validate(&problems)
	c.Storage.validate(&problems)
	c.Workspace.validate(&problems)
	c.Scheduler.validate(&problems)
	c.SSH.validate(&problems)
	if problems.empty() {
		return nil
	}
	return &ValidationError{Path: c.SourcePath, Issues: problems.list}
}

// validate 校验应用基础配置。
func (a AppConfig) validate(problems *issues) {
	if strings.TrimSpace(a.Name) == "" {
		problems.add("app.name", "不能为空")
	}
	if strings.TrimSpace(a.LogLevel) == "" {
		problems.add("app.log_level", "不能为空")
		return
	}
	if _, err := logging.ParseLevel(a.LogLevel); err != nil {
		problems.add("app.log_level", err.Error())
	}
}

// validate 校验 HTTP 服务监听设置。
// validate 校验 HTTP 服务监听设置。
//
// 端口 0 表示使用默认端口，因此只在负值时报告问题；监听地址只在启动阶段解析。
func (s ServerConfig) validate(problems *issues) {
	if s.Port < 0 || s.Port > 65535 {
		problems.add("server.port", "必须在 0 到 65535 之间")
	}
	if strings.ContainsAny(s.Host, " \t\r\n") {
		problems.add("server.host", "不能包含空白字符")
	}
	if s.ShutdownTimeoutSeconds < 0 {
		problems.add("server.shutdown_timeout_seconds", "不得为负数")
	}
}

// Address 返回用于监听的 host:port，未配置的字段使用默认值。
func (s ServerConfig) Address() string {
	host := strings.TrimSpace(s.Host)
	if host == "" {
		host = DefaultServerHost
	}
	port := s.Port
	if port <= 0 {
		port = DefaultServerPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// ShutdownTimeout 返回优雅关闭的最长等待时间，未配置时使用默认值。
func (s ServerConfig) ShutdownTimeout() time.Duration {
	if s.ShutdownTimeoutSeconds <= 0 {
		return DefaultShutdownTimeout
	}
	return time.Duration(s.ShutdownTimeoutSeconds) * time.Second
}

// validate 校验持久化路径。
func (s StorageConfig) validate(problems *issues) {
	if strings.TrimSpace(s.DatabasePath) == "" {
		problems.add("storage.database_path", "不能为空")
	}
}

// validate 校验工作区路径。
func (w WorkspaceConfig) validate(problems *issues) {
	if strings.TrimSpace(w.WorktreeRoot) == "" {
		problems.add("workspace.worktree_root", "不能为空")
	}
}

// validate 校验调度上限。
func (s SchedulerConfig) validate(problems *issues) {
	if s.MaxConcurrentTasks <= 0 {
		problems.add("scheduler.max_concurrent_tasks", "必须大于 0")
	}
	if s.MaxRetriesPerStep < 0 {
		problems.add("scheduler.max_retries_per_step", "不得为负数")
	}
}

// validate 校验 SSH 连接层配置。
//
// 这里只检查路径与超时的书写形式；known_hosts 文件是否存在由连接阶段判断，
// 因此允许首次使用时尚未创建该文件。
func (s SSHConfig) validate(problems *issues) {
	if value := s.KnownHostsPath; value != "" {
		for _, char := range value {
			if char == 0 || char == '\n' || char == '\r' {
				problems.add("ssh.known_hosts_path", "不能包含控制字符")
				break
			}
		}
	}
	if s.ConnectTimeoutSeconds < 0 {
		problems.add("ssh.connect_timeout_seconds", "不得为负数")
	}
}
