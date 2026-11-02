package app

import (
	"strings"

	"github.com/lingting/agentflow/internal/config"
)

// RuntimeConfig 是服务启动时实际生效的基础运行配置只读快照。
//
// 快照由启动阶段已加载并完成命令行覆盖的 config.Config 生成，HTTP 层只做协议转换：
// 读取快照不会重新读取 YAML、环境变量或本机文件，也不触发磁盘扫描、远程连接、
// Docker 操作或配置重载。
//
// 只包含四项非机密运行参数：SSH known_hosts 路径、凭据、私钥路径、模型凭据与仓库
// 凭据一律不对外暴露，也不返回完整配置对象。
type RuntimeConfig struct {
	// DatabasePath 是数据库实际使用的文件路径表达式，与打开数据库时传入的值一致。
	DatabasePath string `json:"database_path"`
	// ListenAddress 是最终生效的监听地址 host:port，已包含启动参数覆盖。
	ListenAddress string `json:"listen_address"`
	// MaxConcurrentTasks 是任务调度的并发任务上限。
	MaxConcurrentTasks int `json:"max_concurrent_tasks"`
	// SessionRoot 是 Session 文件实际写入的根目录。
	SessionRoot string `json:"session_root"`
}

// NewRuntimeConfig 由启动阶段生效的配置与最终监听地址生成只读快照。
//
// listenAddress 必须是解析后的最终地址（命令行覆盖优先于配置文件），不能传入原始
// 配置值。Session 根目录取自 SessionService 实际使用的 app.SessionRoot：它由数据库
// 路径派生，与 workspace.worktree_root 无关；取值与 SessionService 实际写入的目录一致。
//
// 配置为空时返回 nil，表示本次运行没有可用快照；调用方必须把它当作"服务不可用"
// 处理，不得用默认值补造一份配置。
func NewRuntimeConfig(cfg *config.Config, listenAddress string) *RuntimeConfig {
	if cfg == nil {
		return nil
	}
	sessionRoot, err := resolveSessionRoot(SessionRoot(cfg))
	if err != nil {
		// 解析不出真实 Session 目录时不给快照：宁可按服务不可用处理，也不展示猜测路径。
		return nil
	}
	return &RuntimeConfig{
		DatabasePath:       strings.TrimSpace(cfg.Storage.DatabasePath),
		ListenAddress:      listenAddress,
		MaxConcurrentTasks: cfg.Scheduler.MaxConcurrentTasks,
		SessionRoot:        sessionRoot,
	}
}
