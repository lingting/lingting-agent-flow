package codesaver

import (
	"context"
	"fmt"
	"time"
)

// InspectorOptions 约束只读工作区核验器的构造。
//
// 与 Options 的区别是没有 Recorder：只读核验不产生任何代码保存记录，
// 因此也不需要能写记录的仓储。
type InspectorOptions struct {
	// Executor 是容器内命令执行器，必填。
	Executor ContainerExecutor
	// Guard 是容器归属核验器，必填。
	Guard ContainerGuard
	// ProbeTimeout 覆盖轻量查询命令的默认超时。
	ProbeTimeout time.Duration
}

// Inspector 只读地核验并读取容器内 Git 工作区状态。
//
// 它与 Saver 共用同一套容器归属核验、工作区路径校验与状态解析：记录到的分支、
// HEAD 与变更摘要必须与真正的代码保存看到的是同一个工作区。Inspector 不提交、
// 不推送、不切换分支，也不写入任何 code_saves 记录。
type Inspector struct {
	executor     ContainerExecutor
	guard        ContainerGuard
	probeTimeout time.Duration
}

// NewInspector 构造只读工作区核验器。
func NewInspector(options InspectorOptions) (*Inspector, error) {
	if options.Executor == nil {
		return nil, fmt.Errorf("工作区核验初始化失败: 容器执行器不能为空")
	}
	if options.Guard == nil {
		return nil, fmt.Errorf("工作区核验初始化失败: 容器核验器不能为空")
	}
	timeout := options.ProbeTimeout
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	return &Inspector{
		executor:     options.Executor,
		guard:        options.Guard,
		probeTimeout: timeout,
	}, nil
}

// Inspect 读取目标工作区当前的分支、HEAD 与变更摘要。
//
// 前置校验与代码保存完全一致：容器必须受管理且正在运行、登记信息必须一致、
// 目标目录必须是容器内安全路径，且容器内必须有可用的 Git。任一项不满足时返回
// 带类别的错误，绝不用零值冒充"工作区干净"。
func (i *Inspector) Inspect(ctx context.Context, request Request) (WorktreeState, error) {
	saver := &Saver{
		executor:     i.executor,
		guard:        i.guard,
		probeTimeout: i.probeTimeout,
	}
	prepared, err := saver.validate(request)
	if err != nil {
		return WorktreeState{}, err
	}
	container, err := i.guard.EnsureExecutable(ctx, prepared.ContainerID)
	if err != nil {
		return WorktreeState{}, classifyGuardError(err)
	}
	if err := checkContainer(prepared, container); err != nil {
		return WorktreeState{}, err
	}
	runner := &gitRunner{
		executor:     i.executor,
		dockerID:     container.DockerID,
		workDir:      prepared.WorkDir,
		probeTimeout: i.probeTimeout,
	}
	if err := ensureGitAvailable(ctx, runner); err != nil {
		return WorktreeState{}, err
	}
	session := &Session{
		request:   prepared,
		runner:    runner,
		container: container,
		started:   time.Now().UTC(),
	}
	return session.inspect(ctx)
}
