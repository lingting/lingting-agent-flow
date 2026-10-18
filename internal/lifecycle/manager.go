// Package lifecycle 管理后台服务的注册、启动、故障传播与逆序停止。
//
// 后台服务只描述自身如何启动与停止，不关心进程信号、HTTP 服务或数据库生命周期，
// 这些由上层统一编排。Manager 保证：
//
//   - 启动顺序与注册顺序一致，依赖方必须在被依赖方之后注册；
//   - 任一服务启动失败时，已启动的服务按逆序停止；
//   - 停止顺序与启动顺序相反；
//   - 同一服务不会被重复启动或重复停止；
//   - 关闭操作有超时保护，超时通过传递给 Stop 的 context 表达。
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// DefaultStopTimeout 是未显式配置时单个服务管理器使用的停止超时。
const DefaultStopTimeout = 10 * time.Second

// 服务管理器的稳定错误，调用方可通过 errors.Is 判断。
var (
	// ErrStarted 表示服务管理器已启动，不能再注册新的后台服务。
	ErrStarted = errors.New("lifecycle: 服务管理器已启动，不能再注册后台服务")
	// ErrAlreadyStarted 表示服务管理器被重复启动。
	ErrAlreadyStarted = errors.New("lifecycle: 服务管理器已经启动")
	// ErrDuplicateName 表示注册了重名服务，无法区分故障来源。
	ErrDuplicateName = errors.New("lifecycle: 后台服务名称重复")
	// ErrInvalidService 表示注册的后台服务不合法。
	ErrInvalidService = errors.New("lifecycle: 后台服务无效")
)

// Service 是一个由统一生命周期管理的后台服务。
//
// Start 必须在服务就绪后返回，长期运行的循环由服务自己持有；ctx 取消表示进程
// 准备退出。Stop 必须响应 ctx，超时由调用方提供的 deadline 表达。
type Service interface {
	// Name 返回服务名称，用于日志与故障归属，必须在管理器内唯一。
	Name() string
	// Start 启动服务，失败时返回错误。
	Start(ctx context.Context) error
	// Stop 停止服务，必须可被调用一次且响应 ctx 取消。
	Stop(ctx context.Context) error
}

// FaultNotifier 由需要上报运行期致命错误的后台服务实现。
//
// Start 返回成功后，服务把致命错误写入返回的通道；关闭通道表示不会再上报。
// 未实现该接口的服务视为不会在运行期失败。
type FaultNotifier interface {
	Faults() <-chan error
}

// Fault 是一次后台服务运行期故障。
type Fault struct {
	// Name 是上报故障的服务名称。
	Name string
	// Err 是服务上报的错误。
	Err error
}

// Error 实现 error 接口。
func (f Fault) Error() string {
	return fmt.Sprintf("后台服务 %s 异常退出: %v", f.Name, f.Err)
}

// Unwrap 返回原始错误。
func (f Fault) Unwrap() error {
	return f.Err
}

// Options 是构造管理器的输入。
type Options struct {
	// Logger 是日志器，为空时使用 slog.Default。
	Logger *slog.Logger
	// StopTimeout 是停止全部服务的总超时，零值使用 DefaultStopTimeout。
	StopTimeout time.Duration
}

// Manager 按注册顺序启动后台服务，并按逆序停止它们。
type Manager struct {
	logger      *slog.Logger
	stopTimeout time.Duration
	faults      chan Fault
	done        chan struct{}

	mu       sync.Mutex
	names    map[string]struct{}
	services []Service
	started  []Service
	booted   bool
	stopped  bool
}

// New 构造一个空的服务管理器。
func New(options Options) *Manager {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	timeout := options.StopTimeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	return &Manager{
		logger:      logger,
		stopTimeout: timeout,
		faults:      make(chan Fault, 1),
		done:        make(chan struct{}),
		names:       make(map[string]struct{}),
	}
}

// Register 按顺序注册后台服务。
//
// 重复名称、空名称、nil 服务以及启动后再注册都会返回错误；注册失败时不改变
// 已有的注册列表。
func (m *Manager) Register(services ...Service) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.booted {
		return ErrStarted
	}
	for _, service := range services {
		if service == nil {
			return fmt.Errorf("%w: 服务不能为空", ErrInvalidService)
		}
		name := strings.TrimSpace(service.Name())
		if name == "" {
			return fmt.Errorf("%w: 服务名称不能为空", ErrInvalidService)
		}
		if _, exists := m.names[name]; exists {
			return fmt.Errorf("%w: %s", ErrDuplicateName, name)
		}
		m.names[name] = struct{}{}
		m.services = append(m.services, service)
	}
	return nil
}

// Start 按注册顺序启动全部后台服务。
//
// 任一服务启动失败时，已启动的服务按逆序停止，返回的错误同时包含启动失败原因
// 与清理过程中的错误；未启动的服务不会再被启动。
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.booted {
		m.mu.Unlock()
		return ErrAlreadyStarted
	}
	m.booted = true
	services := append([]Service(nil), m.services...)
	m.mu.Unlock()

	for _, service := range services {
		if err := ctx.Err(); err != nil {
			return errors.Join(
				fmt.Errorf("启动后台服务被取消: %w", err),
				m.shutdown(ctx),
			)
		}
		if err := service.Start(ctx); err != nil {
			startErr := fmt.Errorf("后台服务 %s 启动失败: %w", service.Name(), err)
			return errors.Join(startErr, m.shutdown(ctx))
		}
		m.mu.Lock()
		m.started = append(m.started, service)
		m.mu.Unlock()
		m.logger.InfoContext(ctx, "后台服务已启动", slog.String("service", service.Name()))
		m.watch(service)
	}
	return nil
}

// Stop 按启动顺序的逆序停止全部后台服务。
//
// 重复调用是安全的：只有第一次调用会真正停止服务，后续调用直接返回 nil。
func (m *Manager) Stop(ctx context.Context) error {
	return m.shutdown(ctx)
}

// Faults 返回后台服务运行期故障通道，没有故障时通道不产生值。
func (m *Manager) Faults() <-chan Fault {
	return m.faults
}

// shutdown 停止已启动的服务并标记管理器已停止，重复调用直接返回 nil。
func (m *Manager) shutdown(ctx context.Context) error {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return nil
	}
	m.stopped = true
	services := m.started
	m.started = nil
	m.mu.Unlock()
	close(m.done)
	return m.stopServices(ctx, services)
}

// stopServices 按逆序停止给定服务，并使用统一的超时预算。
func (m *Manager) stopServices(ctx context.Context, services []Service) error {
	if len(services) == 0 {
		return nil
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.stopTimeout)
	defer cancel()
	var failures []error
	for index := len(services) - 1; index >= 0; index-- {
		service := services[index]
		if err := service.Stop(stopCtx); err != nil {
			failures = append(failures, fmt.Errorf("后台服务 %s 停止失败: %w", service.Name(), err))
			continue
		}
		m.logger.InfoContext(stopCtx, "后台服务已停止", slog.String("service", service.Name()))
	}
	return errors.Join(failures...)
}

// watch 订阅服务的运行期故障，并在管理器停止后退出转发。
func (m *Manager) watch(service Service) {
	notifier, ok := service.(FaultNotifier)
	if !ok {
		return
	}
	source := notifier.Faults()
	if source == nil {
		return
	}
	name := service.Name()
	go func() {
		for {
			select {
			case err, open := <-source:
				if !open {
					return
				}
				if err == nil {
					continue
				}
				select {
				case m.faults <- Fault{Name: name, Err: err}:
				case <-m.done:
					return
				}
			case <-m.done:
				return
			}
		}
	}()
}
