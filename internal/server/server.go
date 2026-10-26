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
	"github.com/lingting/agentflow/internal/logging"
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
	// Hosts 是执行节点服务，为空时由已打开的数据库自动构造。
	//
	// 执行节点接口与后台环境预检都依赖它；构造后由 Server 接入统一生命周期，
	// 因此后台校验服务不需要单独启动。
	Hosts *app.ExecutionHostService
	// SSH 是 SSH 服务，用于构造执行节点环境预检能力；为空时跳过自动构造。
	SSH *app.SSHService
	// SSHFactory 在数据库打开后构造 SSH 服务。
	//
	// SSH 服务需要数据库与配置（known_hosts 路径、超时），因此不能在构造 Server
	// 时创建。工厂返回错误时启动失败，已启动组件按逆序清理。
	SSHFactory func(store storage.Store, cfg *config.Config) (*app.SSHService, error)
	// ValidationFactory 在数据库打开后构造确定性验证引擎。
	//
	// 验证引擎需要容器能力与检查输出目录，因此不能在构造 Server 时创建。工厂返回
	// 错误时启动失败；返回 nil 表示本次运行不提供验证接口，此时接口返回 503。
	ValidationFactory func(store storage.Store, cfg *config.Config) (*app.ValidationService, error)
	// CodeInitializeFactory 在数据库打开后构造代码初始化协调器。
	//
	// 代码初始化需要容器能力（SSH、Docker）与容器内命令执行，因此不能在构造 Server
	// 时创建。工厂返回错误时启动失败；返回 nil 表示本次运行不提供初始化接口，
	// 此时接口返回 503 而不是伪造"代码已就绪"。
	CodeInitializeFactory func(
		store storage.Store,
		cfg *config.Config,
	) (*app.CodeInitializeService, error)
	// TaskWorkflowFactory 在数据库打开后构造任务级工作流编排器。
	//
	// 编排器需要单步骤闭环服务（依赖容器能力）与代码保存能力，因此不能在构造
	// Server 时创建。工厂返回错误时启动失败；返回 nil 表示本次运行不提供工作流
	// 接口，此时接口返回 503 而不是伪造编排能力。
	TaskWorkflowFactory func(
		store storage.Store,
		cfg *config.Config,
		steps *app.StepWorkflowService,
	) (*app.TaskWorkflowService, error)
	// TaskSchedulerFactory 在数据库打开后构造任务调度器。
	//
	// 调度器依赖数据库、任务租约与统一启动服务，因此必须在它们之后构造；返回 nil
	// 表示本次运行不提供自动调度，此时 queued 任务只能由用户显式启动。
	TaskSchedulerFactory func(
		store storage.Store,
		cfg *config.Config,
		leases *app.TaskLeaseService,
		launch *app.TaskLaunchService,
	) (*app.TaskScheduler, error)
	// TaskLaunchFactory 在数据库打开后构造统一任务启动服务。
	//
	// 启动服务复用已有的环境准备、计划生成与工作流编排能力，因此必须在它们之后
	// 构造。工厂返回错误时启动失败；返回 nil 表示本次运行不提供统一启动接口，
	// 此时接口返回 503 而不是伪造启动能力。
	TaskLaunchFactory func(
		store storage.Store,
		cfg *config.Config,
		workflow *app.TaskWorkflowService,
		steps *app.StepWorkflowService,
		leases *app.TaskLeaseService,
	) (*app.TaskLaunchService, error)
	// StepExecutorFactory 在数据库打开后构造单步骤执行协调器。
	//
	// 协调器需要容器能力与容器内 Agent 执行能力（SSH、Docker），因此不能在构造
	// Server 时创建。工厂返回错误时启动失败；返回 nil 表示本次运行不提供执行接口，
	// 此时接口返回 503 而不是伪造执行。
	StepExecutorFactory func(store storage.Store, cfg *config.Config) (*app.StepExecutor, error)
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
	sshFactory func(store storage.Store, cfg *config.Config) (*app.SSHService, error)
	// stepExecutorFactory 在数据库打开后构造单步骤执行协调器。
	stepExecutorFactory func(store storage.Store, cfg *config.Config) (*app.StepExecutor, error)
	// taskWorkflowFactory 在数据库打开后构造任务级工作流编排器。
	taskWorkflowFactory func(
		store storage.Store,
		cfg *config.Config,
		steps *app.StepWorkflowService,
	) (*app.TaskWorkflowService, error)
	// taskLaunchFactory 在数据库打开后构造统一任务启动服务。
	taskLaunchFactory func(
		store storage.Store,
		cfg *config.Config,
		workflow *app.TaskWorkflowService,
		steps *app.StepWorkflowService,
		leases *app.TaskLeaseService,
	) (*app.TaskLaunchService, error)
	// taskSchedulerFactory 在数据库打开后构造任务调度器。
	taskSchedulerFactory func(
		store storage.Store,
		cfg *config.Config,
		leases *app.TaskLeaseService,
		launch *app.TaskLaunchService,
	) (*app.TaskScheduler, error)
	// validationFactory 在数据库打开后构造确定性验证引擎。
	validationFactory func(store storage.Store, cfg *config.Config) (*app.ValidationService, error)
	// codeInitializeFactory 在数据库打开后构造代码初始化协调器。
	codeInitializeFactory func(
		store storage.Store,
		cfg *config.Config,
	) (*app.CodeInitializeService, error)
	systemInfo func() app.SystemInfo
	tasks      *app.TaskService
	services   []Service
	hosts      *app.ExecutionHostService
	ssh        *app.SSHService
	validation *app.HostValidationService
	// sessions 是所有 Agent 调用共享的会话服务。
	sessions *app.SessionService
	// roleQuery 是角色只读查询服务，由已打开的数据库构造。
	roleQuery *app.RoleService
	// executionRecords 是执行记录只读查询服务，由已打开的数据库构造。
	executionRecords *app.ExecutionRecordService
	// environments 是环境只读查询服务，由已打开的数据库构造。
	//
	// 它只读持久化元数据，因此不需要 SSH 或 Docker：环境查询接口不会产生远程副作用。
	environments *app.EnvironmentQueryService
	// stepExecutor 是单步骤执行协调器，为空时不提供执行接口。
	stepExecutor *app.StepExecutor
	// validationEngine 是确定性验证引擎，为空时不提供验证接口。
	validationEngine *app.ValidationService
	// stepWorkflow 是单步骤开发—验证闭环协调器，为空时不提供闭环接口。
	stepWorkflow *app.StepWorkflowService
	// taskWorkflow 是任务级工作流编排器，为空时不提供工作流接口。
	taskWorkflow *app.TaskWorkflowService
	// taskLaunch 是统一任务启动服务，为空时不提供启动接口。
	taskLaunch *app.TaskLaunchService
	// taskLeases 是任务租约服务，为启动、工作流与调度器提供统一执行权。
	//
	// 它在数据库打开后构造，是"任务租约是唯一所有权令牌"的唯一来源。
	taskLeases *app.TaskLeaseService
	// scheduler 是有界并发任务调度器，为空时不自动领取 queued 任务。
	scheduler *app.TaskScheduler
	// containerRecovery 收敛状态不明的容器记录并报告孤儿容器。
	//
	// 它只做只读观测与条件写入，不创建、启动、删除或接管容器。
	containerRecovery *app.ContainerRecoveryService
	// codeFetchRecovery 收敛进程退出后仍处于活动状态的尝试级代码获取记录。
	//
	// 它在残留收敛之后、编排器之前启动：先把没有执行者的代码获取现场收敛为
	// uncertain，编排器与启动流程才不会在未知现场上重放 Clone。
	codeFetchRecovery *app.CodeFetchRecoveryService
	// residue 收敛进程退出后残留在活动状态的 Attempt / Session / ValidationRun。
	//
	// 它必须在下层服务（步骤执行、验证引擎）之后启动、在上层编排器之前启动：
	// 先把"没有执行者"的残留收敛掉，编排器与启动恢复才能读到可信的现场。
	residue *app.ExecutionResidueService
	// codeSaveRecovery 在启动时扫描仍有未决 Git 副作用的代码保存记录。
	//
	// 它只读记录并追加事件，不重放 commit / push / merge，也不推进任务状态。
	codeSaveRecovery *app.CodeSaveRecoveryService
	// codeInitialize 是代码初始化协调器，为空时不提供初始化接口。
	codeInitialize *app.CodeInitializeService
	// containerService 提供代码保存的只读核验能力，为空时核验接口返回 503。
	containerService *app.ContainerService
	// resumeEligibility 计算任务续跑资格，为空时该接口返回 503。
	resumeEligibility *app.ResumeEligibilityService
	// taskRetry 是任务显式重试服务，为空时不提供重试接口。
	taskRetry *app.TaskRetryService
	// taskLeaseRecovery 在启动时核验残留的任务租约现场。
	//
	// 它不释放租约、不改写任务状态：租约过期不等于远程操作已停止。
	taskLeaseRecovery *app.TaskLeaseRecoveryService
	requireWeb        bool
	logger            *slog.Logger
	report            func(message string)
	manager           *lifecycle.Manager

	boundAddr       string
	cfg             *config.Config
	store           storage.Store
	web             *httpserver.Server
	serveErr        chan error
	cancel          context.CancelFunc
	shutdownTimeout time.Duration

	// addrMu 保护 address 与 boundAddr：HTTP 服务在 Run 的 goroutine 里写入实际
	// 监听地址，而 Addr 会被外部（测试、日志、健康检查）并发读取。没有它时
	// `go test -race` 会报 Run 与 Addr 之间的数据竞争。
	addrMu sync.RWMutex

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
		address:               options.Address,
		openStore:             options.OpenStore,
		sshFactory:            options.SSHFactory,
		stepExecutorFactory:   options.StepExecutorFactory,
		taskWorkflowFactory:   options.TaskWorkflowFactory,
		taskLaunchFactory:     options.TaskLaunchFactory,
		taskSchedulerFactory:  options.TaskSchedulerFactory,
		validationFactory:     options.ValidationFactory,
		codeInitializeFactory: options.CodeInitializeFactory,
		systemInfo:            options.SystemInfo,
		tasks:                 options.Tasks,
		services:              options.Services,
		hosts:                 options.Hosts,
		ssh:                   options.SSH,
		requireWeb:            options.RequireWebUI,
		logger:                options.Logger,
		report:                report,
		manager:               lifecycle.New(lifecycle.Options{Logger: options.Logger}),
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
	s.addrMu.Lock()
	s.address = resolveAddress(cfg, s.address)
	s.addrMu.Unlock()

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
	// 角色初始化必须早于任何业务服务：全新数据库的 roles 为空时，创建任务会因
	// 找不到角色直接失败。这里复用存储层的原子初始化，只在空表时导入。
	if err := s.seedRoles(ctx, store); err != nil {
		return err
	}
	root := app.SessionRoot(s.cfg)
	if root == "" {
		return fmt.Errorf("初始化 Session 目录失败: 无法从配置确定 Session 根目录")
	}
	s.sessions, err = app.NewSessionService(store, root, s.logger)
	if err != nil {
		return fmt.Errorf("初始化会话服务失败: %w", err)
	}
	if s.ssh == nil && s.sshFactory != nil {
		sshService, err := s.sshFactory(store, s.cfg)
		if err != nil {
			return fmt.Errorf("初始化 SSH 服务失败: %w", err)
		}
		s.ssh = sshService
	}
	if s.stepExecutorFactory != nil && s.sessions != nil {
		executor, err := s.stepExecutorFactory(store, s.cfg)
		if err != nil {
			return fmt.Errorf("初始化步骤执行协调器失败: %w", err)
		}
		s.stepExecutor = executor
	}
	if s.validationFactory != nil {
		engine, engineErr := s.validationFactory(store, s.cfg)
		if engineErr != nil {
			return fmt.Errorf("初始化验证引擎失败: %w", engineErr)
		}
		s.validationEngine = engine
	}
	if s.stepExecutor != nil && s.validationEngine != nil {
		// 闭环协调器只是把两个已有服务串起来：开发使用 StepExecutor，验证使用
		// ValidationService，两者共享同一任务容器工作区。
		workflow, workflowErr := app.NewStepWorkflowService(app.StepWorkflowOptions{
			Steps:      s.stepExecutor,
			Validation: s.validationEngine,
			Store:      s.store,
			// 执行周期与预算事实来自同一份数据库：修复上限因此可以跨进程保持一致。
			Cycles: s.store,
			Logger: s.logger,
		})
		if workflowErr != nil {
			return fmt.Errorf("初始化步骤闭环协调器失败: %w", workflowErr)
		}
		s.stepWorkflow = workflow
	}
	if s.taskWorkflowFactory != nil && s.stepWorkflow != nil {
		// 任务级编排器复用同一份单步骤闭环服务：它不重复实现 Developer 与 Validator，
		// 也不自行创建 Attempt 与 Session。
		orchestrator, workflowErr := s.taskWorkflowFactory(store, s.cfg, s.stepWorkflow)
		if workflowErr != nil {
			return fmt.Errorf("初始化任务级工作流编排器失败: %w", workflowErr)
		}
		s.taskWorkflow = orchestrator
	}
	if s.stepExecutor != nil {
		// 残留收敛依赖已打开的数据库：它在步骤执行与验证引擎之后构造，
		// 因此可以在两者启动之后、编排器启动之前收敛它们的残留。
		residue, residueErr := app.NewExecutionResidueService(app.ExecutionResidueOptions{
			Store:  store,
			Logger: s.logger,
		})
		if residueErr != nil {
			return fmt.Errorf("初始化残留收敛服务失败: %w", residueErr)
		}
		s.residue = residue
	}
	// 容器残留收敛只依赖已打开的数据库与容器观测能力：它找出状态不明与孤儿
	// 容器，只读观测后条件写入，不做任何破坏性处置。
	observation, observationErr := s.containerObservation()
	s.containerService = observation
	if observation != nil {
		// 续跑资格查询复用同一份容器核验能力：它只读判定，不推进任何状态。
		eligibility, eligibilityErr := app.NewResumeEligibilityService(
			app.ResumeEligibilityOptions{Store: store, Containers: observation},
		)
		if eligibilityErr != nil {
			return fmt.Errorf("初始化续跑资格服务失败: %w", eligibilityErr)
		}
		s.resumeEligibility = eligibility
	}
	if observationErr != nil {
		return fmt.Errorf("初始化容器残留收敛服务失败: %w", observationErr)
	}
	containerRecovery, containerErr := app.NewContainerRecoveryService(app.ContainerRecoveryOptions{
		Store:      store,
		Containers: observation,
		Logger:     s.logger,
	})
	if containerErr != nil {
		return fmt.Errorf("初始化容器残留收敛服务失败: %w", containerErr)
	}
	s.containerRecovery = containerRecovery
	// 尝试级代码获取残留收敛只依赖已打开的数据库：启动时把没有执行者的
	// pending / running 记录收敛为 uncertain，不重放任何容器内 Git 操作。
	codeFetchRecovery, fetchErr := app.NewCodeFetchRecoveryService(app.CodeFetchRecoveryOptions{
		Store:  store,
		Logger: s.logger,
	})
	if fetchErr != nil {
		return fmt.Errorf("初始化代码获取残留收敛服务失败: %w", fetchErr)
	}
	s.codeFetchRecovery = codeFetchRecovery
	// 代码保存恢复扫描只依赖已打开的数据库：它在编排器构造之后建立，启动时
	// 找出仍有未决 Git 副作用的记录并写入事件，不重放任何外部操作。
	codeSaveRecovery, recoveryErr := app.NewCodeSaveRecoveryService(app.CodeSaveRecoveryOptions{
		Store:  store,
		Logger: s.logger,
	})
	if recoveryErr != nil {
		return fmt.Errorf("初始化代码保存恢复扫描服务失败: %w", recoveryErr)
	}
	s.codeSaveRecovery = codeSaveRecovery
	// 任务租约服务是启动、工作流与调度器的共同依赖：它必须在这些服务之前构造，
	// 因为"任务租约是唯一所有权令牌"要求所有执行入口使用同一份租约能力。
	leases, leaseErr := app.NewTaskLeaseService(app.TaskLeaseOptions{
		Store:  store,
		Logger: s.logger,
	})
	if leaseErr != nil {
		return fmt.Errorf("初始化任务租约服务失败: %w", leaseErr)
	}
	s.taskLeases = leases
	if s.taskLaunchFactory != nil && s.taskWorkflow != nil {
		// 统一启动服务只串联既有能力：环境准备、计划生成与工作流编排都在它们
		// 各自的服务里实现，启动服务不重复实现任何一步。
		launcher, launchErr := s.taskLaunchFactory(
			store,
			s.cfg,
			s.taskWorkflow,
			s.stepWorkflow,
			leases,
		)
		if launchErr != nil {
			return fmt.Errorf("初始化任务启动服务失败: %w", launchErr)
		}
		s.taskLaunch = launcher
	}
	if s.taskSchedulerFactory != nil && s.taskLaunch != nil {
		// 调度器在启动服务之后构造：它只调用既有启动流程，不重复实现任何阶段。
		scheduler, schedulerErr := s.taskSchedulerFactory(store, s.cfg, leases, s.taskLaunch)
		if schedulerErr != nil {
			return fmt.Errorf("初始化任务调度器失败: %w", schedulerErr)
		}
		s.scheduler = scheduler
	}
	// 显式重试复用统一启动流程：它只负责重开任务与取得执行权，不实现第二套启动逻辑。
	if s.taskLaunch != nil {
		retry, retryErr := app.NewTaskRetryService(app.TaskRetryOptions{
			Store:  store,
			Leases: leases,
			Launch: s.taskLaunch,
			Logger: s.logger,
		})
		if retryErr != nil {
			return fmt.Errorf("初始化任务重试服务失败: %w", retryErr)
		}
		s.taskRetry = retry
	}
	// 租约残留核验只记录现场：它不释放租约、不改写任务状态、不重放外部操作。
	leaseRecovery, leaseRecoveryErr := app.NewTaskLeaseRecoveryService(
		app.TaskLeaseRecoveryOptions{Store: store, Logger: s.logger},
	)
	if leaseRecoveryErr != nil {
		return fmt.Errorf("初始化租约残留核验服务失败: %w", leaseRecoveryErr)
	}
	s.taskLeaseRecovery = leaseRecovery
	if s.codeInitializeFactory != nil {
		initializer, initErr := s.codeInitializeFactory(store, s.cfg)
		if initErr != nil {
			return fmt.Errorf("初始化代码初始化服务失败: %w", initErr)
		}
		s.codeInitialize = initializer
	}
	s.logger.DebugContext(ctx, "数据库已就绪", slog.String("path", s.cfg.Storage.DatabasePath))
	return nil
}

// seedRoles 在角色表为空时导入配置中的初始角色。
//
// 语义由存储层的 RoleSeeder 保证：检查与写入在同一事务内完成，已有角色（含禁用
// 角色）不会被覆盖，重复启动不会重复导入。普通启动不做任何角色同步，配置变化
// 也不会隐式改写数据库中的角色。
//
// 配置为空或存储不支持角色初始化时直接失败：让服务带着空角色表进入可用状态，
// 只会在用户创建任务时才暴露问题。
func (s *Server) seedRoles(ctx context.Context, store storage.Store) error {
	seeder, ok := store.(storage.RoleSeeder)
	if !ok {
		return fmt.Errorf("初始化角色失败: 存储不支持角色初始化")
	}
	seeds, err := app.RoleSeeds(s.cfg)
	if err != nil {
		return err
	}
	created, err := seeder.SeedRoles(ctx, seeds)
	if err != nil {
		return fmt.Errorf("初始化角色失败: %w", err)
	}
	if created > 0 {
		s.report(fmt.Sprintf("已从配置导入 %d 个角色", created))
	}
	return nil
}

// startServices 构造执行节点服务与后台校验服务，并按注册顺序启动。
//
// 环境预检作为后台服务注册进统一生命周期：由这里启动与停止，不在 HTTP Handler
// 内创建不受管理的长期 goroutine。
func (s *Server) startServices(ctx context.Context) error {
	// 角色查询服务只读已持久化的角色记录：角色导入发生在 openDatabase 阶段，
	// 因此这里读到的一定是本次启动之后数据库中的真实状态。
	roles, err := s.roleService()
	if err != nil {
		return err
	}
	s.roleQuery = roles
	records, err := s.executionRecordService()
	if err != nil {
		return err
	}
	s.executionRecords = records
	// 环境只读查询只依赖已打开的数据库：环境与版本元数据都在 SQLite 中，
	// 读取它们不需要 SSH 或 Docker，也不会触发构建或初始化。
	environments, err := s.environmentQueryService()
	if err != nil {
		return err
	}
	s.environments = environments
	hosts, err := s.hostService()
	if err != nil {
		return err
	}
	services := append([]Service(nil), s.services...)
	if hosts != nil {
		validation, validationErr := s.hostValidationService(hosts)
		if validationErr != nil {
			return validationErr
		}
		if validation != nil {
			services = append(services, validation)
		}
	}
	if s.stepExecutor != nil {
		// 步骤执行协调器同样注册进统一生命周期：Server 停止时取消在途执行并等待
		// 收敛，使中断状态能被写入数据库，而不是留下 running 残留。
		services = append(services, s.stepExecutor)
	}
	if s.validationEngine != nil {
		// 验证引擎也注册进统一生命周期：停止时取消在途验证并等待远程命令中断，
		// 使验证记录收敛为失败终态，而不是留下 running 残留。
		services = append(services, s.validationEngine)
	}
	if s.stepWorkflow != nil {
		// 闭环协调器依赖步骤执行与验证引擎，注册顺序因此在下层服务之后。
		services = append(services, s.stepWorkflow)
	}
	if s.containerRecovery != nil {
		// 容器残留收敛放在步骤执行之前：容器状态不明时，任何依赖容器的执行都不应
		// 在"可能已经不存在的容器"上开始。
		services = append(services, s.containerRecovery)
	}
	if s.residue != nil {
		// 残留收敛必须在下层服务启动之后、编排器启动之前执行：先把没有执行者的
		// Attempt / Session / ValidationRun 收敛掉，编排器才能读到可信现场。
		services = append(services, s.residue)
	}
	if s.codeFetchRecovery != nil {
		// 代码获取残留同样必须在编排器之前收敛：残留记录会阻止同一 Attempt 再次
		// 初始化，若编排器先启动，用户会在无法解释的冲突里反复重试。
		services = append(services, s.codeFetchRecovery)
	}
	if s.taskWorkflow != nil {
		// 任务级编排器注册在最后：它依赖单步骤闭环，停止顺序因此自动反过来，
		// 先停止新工作流请求，再让下层服务收敛在途工作。
		services = append(services, s.taskWorkflow)
	}
	if s.taskLaunch != nil {
		// 统一启动服务注册在编排器之后：它依赖编排器，停止顺序因此反过来，
		// 先停止新的启动请求，再让下层服务收敛在途工作。
		services = append(services, s.taskLaunch)
	}
	if s.taskLeaseRecovery != nil {
		// 租约残留核验必须排在调度器之前：调度器的第一次扫描不得在"租约过期但现场
		// 未核验"的任务上做任何判断。它只记录现场，不释放租约、不改写状态。
		services = append(services, s.taskLeaseRecovery)
	}
	if s.scheduler != nil {
		// 调度器注册在最后：它依赖数据库、任务租约与统一启动服务，因此启动顺序
		// 自然排在恢复与前置收敛之后，停止顺序反转为"先停止新分派"。
		services = append(services, s.scheduler)
	}
	if s.codeSaveRecovery != nil {
		// 代码保存恢复扫描放在最后注册：它只读记录并追加事件，因此不需要在下层
		// 服务收敛之前运行，也不会阻止任何在途工作完成。
		services = append(services, s.codeSaveRecovery)
	}
	if len(services) == 0 {
		return nil
	}
	if err := s.manager.Register(services...); err != nil {
		return err
	}
	return s.manager.Start(ctx)
}

// loggerWrapper 把 slog 日志器适配为应用层日志器。
//
// server 层只持有 slog.Logger；应用服务需要 logging.Logger 才能记录结构化日志。
// 包装后两者共用同一个 handler，输出目标与级别一致，不会产生第二套日志格式。
func (s *Server) loggerWrapper() *logging.Logger {
	return logging.FromSlog(s.logger)
}

// roleService 构造角色只读查询服务。
//
// 未打开数据库时返回 nil：角色接口随后返回 503，而不是伪造空列表。
func (s *Server) roleService() (*app.RoleService, error) {
	if s.store == nil {
		return nil, nil
	}
	roles, err := app.NewRoleService(s.store)
	if err != nil {
		return nil, fmt.Errorf("初始化角色查询服务失败: %w", err)
	}
	return roles, nil
}

// executionRecordService 构造执行记录只读查询服务。
//
// 未打开数据库时返回 nil：执行记录接口随后返回 503，而不是伪造空结果。
func (s *Server) executionRecordService() (*app.ExecutionRecordService, error) {
	if s.store == nil {
		return nil, nil
	}
	records, err := app.NewExecutionRecordService(s.store)
	if err != nil {
		return nil, fmt.Errorf("初始化执行记录服务失败: %w", err)
	}
	return records, nil
}

// environmentQueryService 构造环境只读查询服务。
//
// 未打开数据库时返回 nil：环境查询接口随后返回 503，而不是伪造空列表。
// 该服务只需要存储能力，因此不构造 SSH 或 Docker 依赖。
func (s *Server) environmentQueryService() (*app.EnvironmentQueryService, error) {
	if s.store == nil {
		return nil, nil
	}
	environments, err := app.NewEnvironmentQueryService(s.store)
	if err != nil {
		return nil, fmt.Errorf("初始化环境查询服务失败: %w", err)
	}
	return environments, nil
}

// hostService 返回执行节点服务。
//
// 未显式注入时用已打开的数据库构造，并接入后台校验服务与同步校验器，
// 使新增与修改节点后能自动触发预检。
func (s *Server) hostService() (*app.ExecutionHostService, error) {
	if s.hosts != nil {
		return s.hosts, nil
	}
	if s.store == nil {
		return nil, nil
	}
	hosts, err := app.NewExecutionHostService(s.store, s.loggerWrapper())
	if err != nil {
		return nil, fmt.Errorf("初始化执行节点服务失败: %w", err)
	}
	s.hosts = hosts
	return hosts, nil
}

// containerObservation 返回容器残留收敛使用的只读观测能力。
//
// 缺少 SSH 能力或数据库时返回 nil：收敛服务仍会报告残留，但不改变任何记录，
// 而不是退化成"用本地登记记录推断远程状态"。
func (s *Server) containerObservation() (*app.ContainerService, error) {
	if s.store == nil || s.ssh == nil {
		return nil, nil
	}
	return app.NewContainerService(app.DockerServiceOptions{
		SSH:    s.ssh,
		Store:  s.store,
		Logger: s.loggerWrapper(),
	})
}

// hostValidationService 构造后台环境预检服务并接入执行节点服务。
//
// SSH 服务缺失时无法执行预检，此时返回 nil：执行节点接口仍然可用，
// 新增节点会停留在 checking，直到通过 CLI 或配置修正后重新校验。
func (s *Server) hostValidationService(
	hosts *app.ExecutionHostService,
) (*app.HostValidationService, error) {
	if s.ssh == nil || s.store == nil {
		return nil, nil
	}
	validator, err := app.NewHostValidator(app.HostValidationOptions{
		SSH:    s.ssh,
		Store:  s.store,
		Logger: s.loggerWrapper(),
	})
	if err != nil {
		return nil, fmt.Errorf("初始化执行节点校验器失败: %w", err)
	}
	validation, err := app.NewHostValidationService(app.HostValidationServiceOptions{
		Validator: validator,
		Store:     s.store,
		Logger:    s.loggerWrapper(),
	})
	if err != nil {
		return nil, fmt.Errorf("初始化校验服务失败: %w", err)
	}
	hosts.WithValidation(validation, validator)
	s.validation = validation
	return validation, nil
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
		Address:           s.address,
		Roles:             s.roleQuery,
		Health:            app.NewHealthService(s.store),
		SystemInfo:        s.systemInfo,
		Tasks:             tasks,
		Hosts:             s.hosts,
		Environments:      s.environments,
		ExecutionRecords:  s.executionRecords,
		StepExecutor:      s.stepExecutor,
		Validation:        s.validationEngine,
		StepWorkflow:      s.stepWorkflow,
		Workflow:          s.taskWorkflow,
		TaskLaunch:        s.taskLaunch,
		TaskLeases:        s.taskLeases,
		Containers:        s.containerService,
		ResumeEligibility: s.resumeEligibility,
		TaskRetry:         s.taskRetry,
		CodeInitialize:    s.codeInitialize,
		RequireWebUI:      s.requireWeb,
		ShutdownTimeout:   s.shutdownTimeout,
		Report:            s.report,
		Logger:            s.logger,
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
	s.addrMu.Lock()
	s.boundAddr = listener.Addr().String()
	s.addrMu.Unlock()
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
	s.addrMu.RLock()
	defer s.addrMu.RUnlock()
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
