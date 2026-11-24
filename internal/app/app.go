// Package app 是 CLI 与后续 HTTP 接口共用的应用服务层。
//
// 本包负责组装配置、日志与命令运行依赖，命令处理函数只做参数解析与结果输出。
package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"

	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/release"
)

// BuildInfo 是通过构建变量注入的版本信息。
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// String 返回多行版本信息，供 --version 与 version 子命令共用。
func (b BuildInfo) String() string {
	return fmt.Sprintf(
		"agentflow %s\n提交: %s\n构建时间: %s\nGo: %s\n平台: %s\n",
		b.Version,
		b.Commit,
		b.BuildDate,
		runtime.Version(),
		runtime.GOOS+"/"+runtime.GOARCH,
	)
}

// IsRelease 报告当前是否为正式发布构建。
//
// 判定沿用发布流程的版本号规则：只有注入 vMAJOR.MINOR.PATCH 才是正式制品，缺省构建变量
// 与本地 `make build` 都属于本地开发构建。
func (b BuildInfo) IsRelease() bool {
	return release.IsReleaseVersion(b.Version)
}

// Options 是构造应用实例所需的运行期选项。
type Options struct {
	// Build 是版本信息。
	Build BuildInfo
	// Stdout 用于普通命令输出，默认 os.Stdout。
	Stdout io.Writer
	// Stderr 用于日志与错误输出，默认 os.Stderr。
	Stderr io.Writer
}

// App 持有一次进程运行所需的共享依赖。
type App struct {
	build  BuildInfo
	stdout io.Writer
	stderr io.Writer
	logger *logging.Logger
}

// New 构造应用实例，并创建输出到标准错误流的日志器。
func New(options Options) (*App, error) {
	stdout := options.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := options.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	logger, err := logging.New(stderr, logging.DefaultLevel)
	if err != nil {
		return nil, err
	}
	return &App{
		build:  options.Build,
		stdout: stdout,
		stderr: stderr,
		logger: logger,
	}, nil
}

// Build 返回构建版本信息。
func (a *App) Build() BuildInfo {
	return a.build
}

// Stdout 返回普通输出写入器。
func (a *App) Stdout() io.Writer {
	return a.stdout
}

// Stderr 返回错误输出写入器。
func (a *App) Stderr() io.Writer {
	return a.stderr
}

// Logger 返回应用日志器。
func (a *App) Logger() *logging.Logger {
	return a.logger
}

// ValidateConfig 加载并校验配置文件，成功后按配置调整日志级别。
//
// 失败时不打印错误，由命令层统一转换为退出码与用户可读信息。
func (a *App) ValidateConfig(ctx context.Context, path string) (*config.Config, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return a.applyConfig(ctx, cfg)
}

// ValidateServerConfig 加载并校验服务端配置，成功后按配置调整日志级别。
//
// 未指定配置文件且默认路径不存在时全部使用内置默认值，服务端因此可以在没有任何
// 配置文件的情况下启动；显式指定的路径不存在时仍然报错，不会静默改用默认值。
func (a *App) ValidateServerConfig(ctx context.Context, path string) (*config.Config, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg, err := config.LoadOrDefault(path)
	if err != nil {
		return nil, err
	}
	return a.applyConfig(ctx, cfg)
}

// applyConfig 补齐与构建类型相关的默认值并调整日志级别。
func (a *App) applyConfig(ctx context.Context, cfg *config.Config) (*config.Config, error) {
	// 未配置监听端口时按构建类型落为默认端口：本地开发构建与正式发布制品使用不同端口，
	// 同一台机器上并发运行两者时不会争用同一端口。
	if cfg.Server.Port <= 0 {
		cfg.Server.Port = config.ServerPortFor(a.build.IsRelease())
	}
	if err := a.logger.SetLevel(cfg.App.LogLevel); err != nil {
		return nil, fmt.Errorf("应用日志级别失败: %w", err)
	}
	a.logger.Slog().DebugContext(
		ctx,
		"配置校验通过",
		slog.String("path", cfg.SourcePath),
		slog.String("log_level", a.logger.Level()),
	)
	return cfg, nil
}
