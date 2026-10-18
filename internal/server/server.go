// Package server 是 AgentFlow 完整应用服务的统一生命周期编排。
//
// 它按依赖顺序加载配置、初始化日志与数据库、启动后台服务，再启动 HTTP 服务；退出
// 信号或任一组件故障时按逆序清理：HTTP 服务 → 后台服务 → 数据库。
//
// 依赖方向：server → app / httpserver / lifecycle / config / storage，不反向依赖 CLI。
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/httpserver"
	"github.com/lingting/agentflow/internal/lifecycle"
	"github.com/lingting/agentflow/internal/storage"
)

// ErrStartup 表示启动阶段失败，已启动的组件已按逆序清理。
var ErrStartup = errors.New("server: 启动失败")

// Service 是后台服务契约，等价于 lifecycle.Service。
//
// 上层只需让后台服务实现 Name、Start、Stop 三个方法，即可通过 Options.Services
// 注册进统一生命周期。
type Service = lifecycle.Service

// ConfigLoader 加载并校验配置，失败时服务不启动任何组件。
type ConfigLoader func(ctx context.Context) (*config.Config, error)

// StoreOpener 打开数据库并执行迁移，失败时服务不启动任何组件。
//
// 它以函数注入而不是直接调用 app.App：命令层持有 App 实例，服务层只需要"打开
// 数据库"这一项能力，测试也可以注入可控的失败与替身。
type StoreOpener func(ctx context.Context, cfg *config.Config) (storage.Store, error)

// Options 是构造统一服务实例的输入。
type Options struct {
	// Address 覆盖配置文件中的监听地址，为空时使用配置。
	Address string
	// OpenStore 打开数据库，必填。
	OpenStore StoreOpener
	// SystemInfo 返回系统信息，必填。
	SystemInfo func() app.SystemInfo
	// Tasks 是任务管理服务；为空时由数据库自动构造一个只读配置的管理服务。
	//
	// 管理接口不涉及执行器，因此这里构造的服务没有工作流引擎：任务只会被创建、
	// 查询、取消与删除，不会被推进执行。
	Tasks *app.TaskService
	// Services 是按依赖顺序排列的后台服务，可留空。
	Services []Service
	// RequireWebUI 为 true 时缺少前端构建产物将导致启动失败。
	//
	// 正式发布构建应当置为 true；开发时先启动后端再构建前端属于正常流程，
	// 此时页面由 Vite+ 提供，后端缺少产物只给出诊断页。
	RequireWebUI bool
	// Logger 是日志器，必填。
	Logger *slog.Logger
	// Report 输出面向用户的启动与关闭信息，为空时只写日志。
	Report func(message string)
}

// Server 是一次完整的应用服务运行实例。
type Server struct {
	address    string
	openStore  StoreOpener
	systemInfo func() app.SystemInfo
	tasks      *app.TaskService
	services   []Service
	requireWeb bool
	logger     *slog.Logger
	report     func(message string)
	manager    *lifecycle.Manager

	boundAddr       string
	cfg             *config.Config
	store           storage.Store
	web             *httpserver.Server
	serveErr        chan error
	cancel          context.CancelFunc
	shutdownTimeout time.Duration

	stopOnce sync.Once
	stopErr  error
}

// OpenedStore 返回已打开的数据库，未启动或已关闭时返回 nil。
//
// 只用于测试断言关闭顺序：生产代码不通过它访问数据库。
func (s *Server) OpenedStore() storage.Store {
	return s.store
}

// New 校验启动参数，不接触任何外部资源。
func New(options Options) (*Server, error) {
	if options.Logger == nil {
		return nil, fmt.Errorf("服务初始化失败: 日志器不能为空")
	}
	if options.OpenStore == nil {
		return nil, fmt.Errorf("服务初始化失败: 数据库打开器不能为空")
	}
	if options.SystemInfo == nil {
		return nil, fmt.Errorf("服务初始化失败: 系统信息提供者不能为空")
	}
	report := options.Report
	if report == nil {
		report = func(message string) {
			options.Logger.Info(message)
		}
	}
	return &Server{
		address:    options.Address,
		openStore:  options.OpenStore,
		systemInfo: options.SystemInfo,
		tasks:      options.Tasks,
		services:   options.Services,
		requireWeb: options.RequireWebUI,
		logger:     options.Logger,
		report:     report,
		manager:    lifecycle.New(lifecycle.Options{Logger: options.Logger}),
	}, nil
}

// Run 执行完整的启动、运行与关闭流程。
//
// 返回值语义：ctx 取消导致的正常关闭返回 nil；配置错误、启动失败或运行期组件故障
// 返回错误。任何返回路径上已启动的组件都会按逆序停止，不留监听端口与后台 goroutine。
func (s *Server) Run(ctx context.Context, load ConfigLoader) error {
	if load == nil {
		return fmt.Errorf("%w: 缺少配置加载器", ErrStartup)
	}
	cfg, err := load(ctx)
	if err != nil {
		return errors.Join(ErrStartup, err)
	}
	s.cfg = cfg
	s.shutdownTimeout = cfg.Server.ShutdownTimeout()
	s.address = resolveAddress(cfg, s.address)

	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	defer cancel()

	if err := s.startup(runCtx); err != nil {
		return errors.Join(ErrStartup, err, s.Stop(context.WithoutCancel(ctx)))
	}
	return s.wait(ctx, runCtx)
}

// startup 按依赖顺序启动组件，失败时由 Run 负责逆序清理。
func (s *Server) startup(ctx context.Context) error {
	if err := s.openDatabase(ctx); err != nil {
		return err
	}
	if err := s.startServices(ctx); err != nil {
		return err
	}
	return s.startHTTP(ctx)
}

// openDatabase 打开数据库；数据库是必需组件，失败时不再启动后续组件。
func (s *Server) openDatabase(ctx context.Context) error {
	store, err := s.openStore(ctx, s.cfg)
	if err != nil {
		return fmt.Errorf("初始化数据库失败: %w", err)
	}
	s.store = store
	s.logger.DebugContext(ctx, "数据库已就绪", slog.String("path", s.cfg.Storage.DatabasePath))
	return nil
}

// startServices 注册并启动后台服务。
func (s *Server) startServices(ctx context.Context) error {
	if len(s.services) == 0 {
		return nil
	}
	if err := s.manager.Register(s.services...); err != nil {
		return err
	}
	return s.manager.Start(ctx)
}

// taskService 返回任务管理服务。
//
// 未显式注入时用已打开的数据库构造一个不含工作流引擎的服务：管理接口只负责
// 创建、查询、取消与删除任务，不推进执行，也不接触 Docker、SSH 或 Pi。
func (s *Server) taskService() (*app.TaskService, error) {
	if s.tasks != nil {
		return s.tasks, nil
	}
	tasks, err := app.NewTaskService(app.TaskServiceOptions{
		Store:  s.store,
		Logger: s.logger,
	})
	if err != nil {
		return nil, fmt.Errorf("初始化任务服务失败: %w", err)
	}
	return tasks, nil
}

// startHTTP 组装并启动 HTTP 服务；监听失败时不进入运行阶段。
func (s *Server) startHTTP(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tasks, err := s.taskService()
	if err != nil {
		return err
	}
	web, err := httpserver.New(httpserver.Options{
		Address:         s.address,
		Health:          app.NewHealthService(s.store),
		SystemInfo:      s.systemInfo,
		Tasks:           tasks,
		RequireWebUI:    s.requireWeb,
		ShutdownTimeout: s.shutdownTimeout,
		Report:          s.report,
		Logger:          s.logger,
	})
	if err != nil {
		return err
	}
	// 先建立监听再进入运行阶段：端口占用等错误必须在启动阶段就暴露。
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return fmt.Errorf("HTTP 服务监听失败: %s: %w", s.address, err)
	}
	s.web = web
	s.boundAddr = listener.Addr().String()
	s.serveErr = make(chan error, 1)
	go func() {
		s.serveErr <- web.Serve(ctx, listener)
	}()
	return nil
}

// wait 等待退出信号、HTTP 服务退出或后台服务故障。
func (s *Server) wait(ctx, runCtx context.Context) error {
	select {
	case <-ctx.Done():
		return s.Stop(context.WithoutCancel(ctx))
	case <-runCtx.Done():
		// 运行上下文被 Stop 主动取消：清理由 Stop 完成，这里返回同一结果。
		return s.Stop(context.WithoutCancel(ctx))
	case err := <-s.serveErr:
		// HTTP 服务已经自行退出：清理时不再重复等待它，只清理后台服务与数据库。
		s.serveErr = nil
		s.web = nil
		if err == nil {
			return s.Stop(context.WithoutCancel(ctx))
		}
		return errors.Join(err, s.Stop(context.WithoutCancel(ctx)))
	case fault := <-s.manager.Faults():
		s.logger.ErrorContext(
			runCtx,
			"后台服务异常，开始关闭",
			slog.String("service", fault.Name),
			slog.String("error", fault.Err.Error()),
		)
		return errors.Join(fault, s.Stop(context.WithoutCancel(ctx)))
	}
}

// Stop 按逆序停止 HTTP 服务、后台服务与数据库。
//
// 重复调用返回第一次的结果，不会重复停止组件，也不会 panic。
func (s *Server) Stop(ctx context.Context) error {
	s.stopOnce.Do(func() {
		s.stopErr = s.shutdown(ctx)
	})
	return s.stopErr
}

// shutdown 执行实际的逆序清理。
func (s *Server) shutdown(ctx context.Context) error {
	var failures []error
	if s.cancel != nil {
		s.cancel()
	}
	if s.serveErr != nil {
		if err := s.waitServe(); err != nil {
			failures = append(failures, err)
		}
		s.serveErr = nil
		s.web = nil
	}
	if err := s.manager.Stop(ctx); err != nil {
		failures = append(failures, err)
	}
	if s.store != nil {
		if err := s.store.Close(); err != nil {
			failures = append(failures, fmt.Errorf("关闭数据库失败: %w", err))
		}
		s.store = nil
	}
	return errors.Join(failures...)
}

// waitServe 等待 HTTP 服务退出，超时视为关闭失败。
func (s *Server) waitServe() error {
	select {
	case err := <-s.serveErr:
		if err != nil {
			return fmt.Errorf("HTTP 服务退出失败: %w", err)
		}
		return nil
	case <-time.After(s.shutdownTimeout):
		return fmt.Errorf("等待 HTTP 服务退出超时")
	}
}

// Addr 返回实际监听地址，未启动时返回配置值。
//
// 配置端口为 0 时实际端口由系统分配，这里返回真实地址，便于测试与日志定位。
func (s *Server) Addr() string {
	if s.boundAddr != "" {
		return s.boundAddr
	}
	return s.address
}

// resolveAddress 计算最终监听地址：命令行参数优先于配置文件。
func resolveAddress(cfg *config.Config, override string) string {
	if override != "" {
		return override
	}
	return cfg.Server.Address()
}
