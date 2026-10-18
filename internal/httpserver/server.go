// Package httpserver 组装 AgentFlow 的本地 HTTP 服务。
//
// 服务同时提供 API 与嵌入的前端静态资源，使生产环境只需要一个可执行文件；
// 监听地址、请求分发与优雅关闭都集中在这里，进程级生命周期由 internal/server 编排。
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/httpapi"
)

// 服务生命周期相关的时间参数。
const (
	// defaultReadHeaderTimeout 防止慢速请求头占用连接。
	defaultReadHeaderTimeout = 10 * time.Second
	// DefaultShutdownTimeout 是未配置时优雅关闭的最长等待时间。
	DefaultShutdownTimeout = 10 * time.Second
)

// Options 是构造 HTTP 服务的输入。
type Options struct {
	// Address 是监听地址，形如 127.0.0.1:26380，必填。
	Address string
	// Health 是健康检查服务，为空时健康检查接口报告服务不可用。
	Health *app.HealthService
	// SystemInfo 返回系统信息，为空时该接口报告服务不可用。
	SystemInfo func() app.SystemInfo
	// Files 是前端静态资源文件系统，为空时使用嵌入资源。
	Files fs.FS
	// RequireWebUI 为 true 时，缺少前端构建产物将导致构造失败。
	RequireWebUI bool
	// ShutdownTimeout 是优雅关闭的最长等待时间，零值使用 DefaultShutdownTimeout。
	ShutdownTimeout time.Duration
	// Report 用于输出启动信息，为空时只写日志。
	Report func(message string)
	// Logger 是日志器，为空时使用 slog.Default。
	Logger *slog.Logger
}

// Server 是一个已组装但尚未启动的 HTTP 服务。
type Server struct {
	address         string
	http            *http.Server
	report          func(message string)
	shutdownTimeout time.Duration
}

// New 组装 API 与静态资源处理器，返回可启动的服务。
//
// 缺少前端构建产物时：RequireWebUI 为 true 直接失败；否则仍启动 API，
// 页面请求返回明确的诊断信息，而不是空白页。
func New(options Options) (*Server, error) {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	address := options.Address
	if address == "" {
		return nil, fmt.Errorf("HTTP 服务初始化失败: 监听地址不能为空")
	}
	timeout := options.ShutdownTimeout
	if timeout <= 0 {
		timeout = DefaultShutdownTimeout
	}
	api := httpapi.NewHandler(httpapi.Deps{
		Health:     options.Health,
		SystemInfo: options.SystemInfo,
		Logger:     logger,
	})
	web, err := buildWebHandler(options.Files, options.RequireWebUI, logger)
	if err != nil {
		return nil, err
	}
	report := options.Report
	if report == nil {
		report = func(message string) {
			logger.Info(message)
		}
	}
	return &Server{
		address:         address,
		report:          report,
		shutdownTimeout: timeout,
		http: &http.Server{
			Addr:              address,
			Handler:           routes(api, web),
			ReadHeaderTimeout: defaultReadHeaderTimeout,
		},
	}, nil
}

// Run 启动服务并阻塞直到 ctx 取消或服务出错。
//
// 返回 nil 表示按 ctx 正常关闭；返回错误表示监听失败或服务异常退出。
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return fmt.Errorf("HTTP 服务监听失败: %s: %w", s.address, err)
	}
	return s.Serve(ctx, listener)
}

// Serve 在已建立的监听器上提供服务，供测试注入随机端口。
//
// ctx 取消后停止接收新连接，并在超时窗口内等待进行中的请求结束。
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	serveErr := make(chan error, 1)
	go func() {
		s.report("AgentFlow 服务已启动: http://" + listener.Addr().String())
		err := s.http.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()
	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("HTTP 服务异常退出: %w", err)
		}
		return nil
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
	defer cancel()
	if err := s.http.Shutdown(shutdownCtx); err != nil {
		// 关闭失败时立即断开连接，避免进程无法退出。
		_ = s.http.Close()
		return fmt.Errorf("HTTP 服务关闭失败: %w", err)
	}
	if err := <-serveErr; err != nil {
		return fmt.Errorf("HTTP 服务异常退出: %w", err)
	}
	s.report("AgentFlow 服务已关闭")
	return nil
}

// Addr 返回服务的监听地址配置。
func (s *Server) Addr() string {
	return s.address
}

// Handler 返回服务使用的根处理器，供测试直接调用而无需监听端口。
func (s *Server) Handler() http.Handler {
	return s.http.Handler
}
