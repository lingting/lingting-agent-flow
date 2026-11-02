// Package config 定义 AgentFlow 的配置结构与校验规则。
//
// 本包只负责把 YAML 配置转换为结构化数据并校验其合法性，
// 不涉及 Pi 安装检查、模型调用或权限实施。
package config

import (
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/logging"
)

// HTTP 服务默认监听地址与端口。
//
// 默认端口与前端开发服务器（26390）分开，避免开发时两个进程争用同一端口。
const (
	// DefaultServerHost 是未配置监听地址时使用的回环地址。
	DefaultServerHost = "127.0.0.1"
	// DefaultServerPort 是未配置监听端口时使用的端口。
	DefaultServerPort = 26380
	// DefaultShutdownTimeout 是未配置优雅关闭超时时使用的等待时间。
	DefaultShutdownTimeout = 10 * time.Second
)

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
	DatabasePath string `yaml:"database_path"`
}

// WorkspaceConfig 描述任务工作区的存放位置。
type WorkspaceConfig struct {
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
	// KnownHostsPath 是只读 known_hosts 文件路径。
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
