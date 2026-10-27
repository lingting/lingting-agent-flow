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
	// Roles 是角色查询服务，为空时角色接口报告服务不可用。
	//
	// 角色只在首次初始化时从配置导入，接口本身只读。
	Roles *app.RoleService
	// Health 是健康检查服务，为空时健康检查接口报告服务不可用。
	Health *app.HealthService
	// SystemInfo 返回系统信息，为空时该接口报告服务不可用。
	SystemInfo func() app.SystemInfo
	// Tasks 是任务管理服务，为空时任务接口报告服务不可用。
	Tasks *app.TaskService
	// Hosts 是执行节点服务，为空时执行节点接口报告服务不可用。
	Hosts *app.ExecutionHostService
	// Environments 是环境只读查询服务，为空时环境查询接口报告服务不可用。
	//
	// 它只读取持久化的环境与版本元数据：不建立 SSH / Docker 会话，也不会触发构建。
	Environments *app.EnvironmentQueryService
	// ExecutionRecords 是执行记录查询服务，为空时执行记录接口报告服务不可用。
	ExecutionRecords *app.ExecutionRecordService
	// StepExecutor 是单步骤执行协调器，为空时步骤执行接口报告服务不可用。
	StepExecutor *app.StepExecutor
	// Validation 是确定性验证引擎，为空时验证接口报告服务不可用。
	Validation *app.ValidationService
	// StepWorkflow 是开发—验证闭环协调器，为空时闭环接口报告服务不可用。
	StepWorkflow *app.StepWorkflowService
	// Workflow 是任务级工作流编排器，为空时工作流接口报告服务不可用。
	Workflow *app.TaskWorkflowService
	// CodeInitialize 是代码初始化协调器，为空时初始化接口报告服务不可用。
	CodeInitialize *app.CodeInitializeService
	// TaskLaunch 是统一任务启动服务，为空时启动接口报告服务不可用。
	TaskLaunch *app.TaskLaunchService
	// TaskLeases 是任务租约服务，为启动、计划与工作流接口提供统一执行权。
	//
	// 为空时这些接口报告服务不可用：没有任务租约就无法证明调用方持有该任务的执行权。
	TaskLeases *app.TaskLeaseService
	// Containers 是容器服务，提供代码保存的只读核验能力，为空时核验接口报告服务不可用。
	Containers *app.ContainerService
	// CodeSaveQueries 是代码保存记录只读查询服务，为空时列表接口报告服务不可用。
	//
	// 它只依赖存储：代码保存记录列表不需要 SSH 或 Docker，因此与 Containers 分开注入。
	CodeSaveQueries *app.CodeSaveQueryService
	// ResumeEligibility 计算任务的续跑资格，为空时该接口报告服务不可用。
	ResumeEligibility *app.ResumeEligibilityService
	// History 提供事件时间线与修复周期历史的只读查询，为空时两个接口报告服务不可用。
	History *app.HistoryQueryService
	// TaskRetry 是任务显式重试服务，为空时重试接口报告服务不可用。
	TaskRetry *app.TaskRetryService
	// TaskContainers 提供任务容器的只读查询与显式处置，为空时两个接口报告服务不可用。
	TaskContainers *app.TaskContainerDisposalService
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
		Roles:             options.Roles,
		Health:            options.Health,
		SystemInfo:        options.SystemInfo,
		Tasks:             options.Tasks,
		Hosts:             options.Hosts,
		Environments:      options.Environments,
		ExecutionRecords:  options.ExecutionRecords,
		StepExecutor:      options.StepExecutor,
		Validation:        options.Validation,
		StepWorkflow:      options.StepWorkflow,
		Workflow:          options.Workflow,
		CodeInitialize:    options.CodeInitialize,
		TaskLaunch:        options.TaskLaunch,
		TaskLeases:        options.TaskLeases,
		Containers:        options.Containers,
		CodeSaveQueries:   options.CodeSaveQueries,
		ResumeEligibility: options.ResumeEligibility,
		History:           options.History,
		TaskRetry:         options.TaskRetry,
		TaskContainers:    options.TaskContainers,
		Logger:            logger,
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
