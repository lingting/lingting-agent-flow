package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// EnvironmentBuildRunner 是执行真实远程构建的最小能力。
//
// 只由 app.EnvironmentService 实现：管理器负责调度与收敛，不拼接任何 Docker 命令，
// 也不重新实现构建上下文、镜像核实或元数据写入。
type EnvironmentBuildRunner interface {
	// Build 构建指定环境版本，并把构建状态写入数据库。
	Build(ctx context.Context, request BuildRequest) (BuildResult, error)
}

// EnvironmentBuildStore 是后台构建管理依赖的存储能力。
type EnvironmentBuildStore interface {
	// GetExecutionHost 读取执行节点。
	GetExecutionHost(ctx context.Context, id domain.ID) (*domain.ExecutionHost, error)
	// GetEnvironmentVersion 读取环境版本元数据。
	GetEnvironmentVersion(
		ctx context.Context,
		hostID domain.ID,
		ref domain.EnvironmentRef,
		number int,
	) (*domain.EnvironmentVersion, error)
	// ListEnvironmentVersions 按条件列出环境版本。
	ListEnvironmentVersions(
		ctx context.Context,
		filter storage.EnvironmentVersionFilter,
	) ([]domain.EnvironmentVersion, error)
	// UpdateEnvironmentVersionIfStatus 在状态仍属于 expected 时更新构建结果。
	UpdateEnvironmentVersionIfStatus(
		ctx context.Context,
		version domain.EnvironmentVersion,
		expected []domain.BuildStatus,
	) error
}

// EnvironmentBuildTask 是一次被受理的后台构建。
type EnvironmentBuildTask struct {
	// HostID 是目标执行节点。
	HostID domain.ID
	// Ref 是环境引用。
	Ref domain.EnvironmentRef
	// VersionNumber 是构建的版本号。
	VersionNumber int
	// VersionLabel 是版本标签，例如 v0001。
	VersionLabel string
	// AcceptedAt 是服务端受理该构建的时间。
	AcceptedAt time.Time
	// Version 是受理时读到的版本元数据。
	//
	// 它是受理那一刻的真实持久化记录（构建状态可能仍是 pending），调用方不得把它
	// 当成构建结论：结论只能来自后续的构建状态查询。
	Version domain.EnvironmentVersion
}

// environmentBuildRun 是一次在途构建的运行时状态。
type environmentBuildRun struct {
	task EnvironmentBuildTask
	done chan struct{}
}

// EnvironmentBuildManagerOptions 是构造后台构建管理器的输入。
type EnvironmentBuildManagerOptions struct {
	// Store 是持久化入口，必填。
	Store EnvironmentBuildStore
	// Runner 是真实构建执行者，必填。
	Runner EnvironmentBuildRunner
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
}

// EnvironmentBuildManager 在后台执行环境构建，并收敛没有执行者的残留构建记录。
//
// 职责边界：
//   - 构建本身完全复用 EnvironmentService.Build，管理器只负责受理、去重、取消与收敛；
//   - 同一节点、同一环境同时只允许一个构建：重复请求返回 ErrEnvironmentBuildInProgress，
//     而不是排队或并行覆盖同一份远程构建目录；
//   - 停止时取消在途构建并等待其收敛，被中断的构建记为 uncertain，不记为成功或失败；
//   - 启动时把遗留的 building 记录收敛为 uncertain：进程退出后没有执行者，该状态无法
//     由数据库自身证明构建仍在进行。
//
// 管理器不引入新的队列表或任务平台：构建状态的事实来源仍是 environment_versions 表。
type EnvironmentBuildManager struct {
	store  EnvironmentBuildStore
	runner EnvironmentBuildRunner
	logger *slog.Logger

	mu      sync.Mutex
	running map[string]*environmentBuildRun
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	started bool
}

// NewEnvironmentBuildManager 构造后台构建管理器。
func NewEnvironmentBuildManager(
	options EnvironmentBuildManagerOptions,
) (*EnvironmentBuildManager, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("环境构建管理器初始化失败: 存储不能为空")
	}
	if options.Runner == nil {
		return nil, fmt.Errorf("环境构建管理器初始化失败: 构建执行者不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &EnvironmentBuildManager{
		store:   options.Store,
		runner:  options.Runner,
		logger:  logger,
		running: make(map[string]*environmentBuildRun),
	}, nil
}

// Name 返回后台服务名称。
func (m *EnvironmentBuildManager) Name() string {
	return "environment-builds"
}

// Start 收敛遗留构建记录并允许受理新的构建。
//
// 收敛失败直接返回错误：带着"可能仍在构建"的未知现场继续对外提供构建入口，会让
// 同一版本出现两个执行者。
func (m *EnvironmentBuildManager) Start(ctx context.Context) error {
	if m == nil {
		return fmt.Errorf("环境构建管理器未初始化")
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.started = true
	m.mu.Unlock()
	return m.convergeUnmanagedBuilds(ctx)
}

// Stop 取消在途构建并等待其收敛。
//
// 等待受 ctx 约束：停止超时由上层生命周期管理器表达，超时后返回错误而不是无限等待。
func (m *EnvironmentBuildManager) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.started = false
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	finished := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("环境构建管理器停止超时: %w", ctx.Err())
	}
}

// Submit 受理一次后台构建。
//
// 它在返回前完成全部只依赖数据库的校验：节点存在、版本存在、没有同环境在途构建。
// 这些校验不需要 SSH 或 Docker，因此校验失败时请求会得到明确的 4xx，而不是一个
// 稍后才在后台失败的任务。
func (m *EnvironmentBuildManager) Submit(
	ctx context.Context,
	request BuildRequest,
) (EnvironmentBuildTask, error) {
	var task EnvironmentBuildTask
	if m == nil {
		return task, fmt.Errorf("%w: 环境构建管理器未装配", ErrEnvironmentBuildStopped)
	}
	if err := request.Ref.Validate(); err != nil {
		return task, fmt.Errorf("%w: %w", ErrEnvironmentInvalidInput, err)
	}
	if request.VersionNumber < domain.MinEnvironmentVersionNumber ||
		request.VersionNumber > domain.MaxEnvironmentVersionNumber {
		return task, fmt.Errorf(
			"%w: 版本号必须在 %d-%d 之间",
			ErrEnvironmentInvalidInput,
			domain.MinEnvironmentVersionNumber,
			domain.MaxEnvironmentVersionNumber,
		)
	}
	host, err := loadEnvironmentHost(ctx, m.store, request.MachineID)
	if err != nil {
		return task, err
	}
	// 受理前必须核对版本确实存在：否则用户会拿到一个"已受理"的构建，
	// 而它注定在后台因为没有该版本而失败。读到的元数据随任务一并返回，调用方
	// 因此不需要再查一次，也不会因为第二次查询失败而误报受理失败。
	version, err := m.store.GetEnvironmentVersion(
		ctx,
		host.ID,
		request.Ref,
		request.VersionNumber,
	)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return task, fmt.Errorf(
				"%w: %s %s",
				ErrEnvironmentVersionNotFound,
				request.Ref.Label(),
				domain.FormatVersionLabel(request.VersionNumber),
			)
		}
		return task, fmt.Errorf("读取环境版本失败: %w", err)
	}

	key := lockKey(host.ID, request.Ref)
	run := &environmentBuildRun{done: make(chan struct{})}

	m.mu.Lock()
	if !m.started || m.ctx == nil {
		m.mu.Unlock()
		return task, fmt.Errorf("%w: 环境构建服务未启动", ErrEnvironmentBuildStopped)
	}
	if _, busy := m.running[key]; busy {
		m.mu.Unlock()
		return task, fmt.Errorf(
			"%w: 环境 %s 在节点 %s 上已有构建进行中",
			ErrEnvironmentBuildInProgress,
			request.Ref.Label(),
			host.ID,
		)
	}
	buildCtx := m.ctx
	task = EnvironmentBuildTask{
		HostID:        host.ID,
		Ref:           request.Ref,
		VersionNumber: request.VersionNumber,
		VersionLabel:  domain.FormatVersionLabel(request.VersionNumber),
		AcceptedAt:    time.Now().UTC().Truncate(time.Millisecond),
		Version:       *version,
	}
	run.task = task
	m.running[key] = run
	m.wg.Add(1)
	m.mu.Unlock()

	go m.execute(buildCtx, key, run, request)
	return task, nil
}

// Status 报告指定环境是否有在途构建。
func (m *EnvironmentBuildManager) Status(
	hostID domain.ID,
	ref domain.EnvironmentRef,
) (EnvironmentBuildTask, bool) {
	if m == nil {
		return EnvironmentBuildTask{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.running[lockKey(hostID, ref)]
	if !ok {
		return EnvironmentBuildTask{}, false
	}
	return run.task, true
}

// Wait 等待一次构建结束，供测试与诊断使用。
func (m *EnvironmentBuildManager) Wait(hostID domain.ID, ref domain.EnvironmentRef) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	run, ok := m.running[lockKey(hostID, ref)]
	m.mu.Unlock()
	if !ok {
		return false
	}
	<-run.done
	return true
}

// execute 在后台执行一次构建并释放运行位。
func (m *EnvironmentBuildManager) execute(
	ctx context.Context,
	key string,
	run *environmentBuildRun,
	request BuildRequest,
) {
	defer m.wg.Done()
	defer close(run.done)
	defer func() {
		m.mu.Lock()
		delete(m.running, key)
		m.mu.Unlock()
	}()
	result, err := m.runner.Build(ctx, request)
	if err != nil {
		m.logger.WarnContext(
			ctx,
			"环境构建未成功",
			slog.String("environment", request.Ref.Label()),
			slog.String("version", domain.FormatVersionLabel(request.VersionNumber)),
			slog.String("status", result.Version.Status.String()),
			slog.String("error", err.Error()),
		)
		return
	}
	m.logger.InfoContext(
		ctx,
		"环境构建完成",
		slog.String("environment", request.Ref.Label()),
		slog.String("version", result.Version.Label()),
		slog.Bool("cached", result.Cached),
	)
}

// convergeUnmanagedBuilds 把遗留的 building 记录收敛为 uncertain。
//
// 判定依据只有一条：进程启动时不可能存在本进程发起的在途构建。因此数据库中仍为
// building 的记录一定没有执行者，其构建结果无法由数据本身证明，只能记为 uncertain。
func (m *EnvironmentBuildManager) convergeUnmanagedBuilds(ctx context.Context) error {
	versions, err := m.store.ListEnvironmentVersions(ctx, storage.EnvironmentVersionFilter{
		Statuses: []domain.BuildStatus{domain.BuildStatusBuilding},
	})
	if err != nil {
		return fmt.Errorf("读取遗留构建记录失败: %w", err)
	}
	for _, version := range versions {
		stale := version
		if markErr := stale.MarkUncertain(
			"服务重启时该构建仍在进行，进程已退出，构建结果无法确认；请显式重新构建",
		); markErr != nil {
			m.logger.WarnContext(
				ctx,
				"收敛遗留构建记录失败",
				slog.String("environment", version.Ref.Label()),
				slog.String("version", version.Label()),
				slog.String("error", markErr.Error()),
			)
			continue
		}
		if updateErr := m.store.UpdateEnvironmentVersionIfStatus(
			ctx,
			stale,
			[]domain.BuildStatus{domain.BuildStatusBuilding},
		); updateErr != nil {
			if errors.Is(updateErr, storage.ErrConflict) {
				// 记录已被其他操作推进，不覆盖已确认的结论。
				continue
			}
			return fmt.Errorf(
				"收敛遗留构建记录失败: %s: %w",
				version.Ref.Label()+"/"+version.Label(),
				updateErr,
			)
		}
		m.logger.WarnContext(
			ctx,
			"遗留构建记录已收敛为结果不确定",
			slog.String("environment", version.Ref.Label()),
			slog.String("version", version.Label()),
		)
	}
	return nil
}
