package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/execenv"
)

// 容器可用性核验使用的固定脚本与超时。
const (
	// verifyScriptMakeWritable 创建并核验工作目录可写。
	//
	// 目录以位置参数传入而不是拼接进脚本，因此路径内容不会改变脚本语义。
	verifyScriptMakeWritable = `mkdir -p "$1" && test -w "$1"`
	// verifyScriptName 是 sh -c 的 $0 占位，使后续参数成为 $1 起的位置参数。
	verifyScriptName = "agentflow-verify"
	// verifyProbeTimeout 是单项核验命令的超时时间。
	verifyProbeTimeout = 60 * time.Second
)

// Prepare 准备执行环境：校验配置 → 准备镜像 → 创建容器 → 核验可用性。
//
// 每个阶段都会记录到结果的 Steps 中，因此失败时能定位停在哪一步。
// 准备阶段不执行耗时的镜像拉取之外的任何副作用检查，也不复用上一次失败任务的
// 旧容器：每次准备都创建新的容器。
func (s *ExecutionEnvironmentService) Prepare(
	ctx context.Context,
	spec execenv.PrepareSpec,
) (execenv.Prepared, error) {
	prepared := execenv.Prepared{StartedAt: time.Now().UTC()}
	finish := func(err error) (execenv.Prepared, error) {
		prepared.FinishedAt = time.Now().UTC()
		if err != nil {
			prepared.Err = err
		}
		return prepared, err
	}

	// 阶段 1：校验机器与项目配置，并确认 Docker 兼容性基线可用。
	validated, err := s.runPrepareStep(ctx, execenv.StepValidate, func(
		ctx context.Context,
	) (string, error) {
		return s.validatePrepare(ctx, spec, &prepared)
	})
	if err != nil {
		prepared.Steps = append(prepared.Steps, validated)
		return finish(err)
	}
	prepared.Steps = append(prepared.Steps, validated)

	// 阶段 2：准备镜像（构建或复用已成功的版本）。
	imageStep, err := s.runPrepareStep(ctx, execenv.StepImage, func(
		ctx context.Context,
	) (string, error) {
		return s.prepareImage(ctx, spec, &prepared)
	})
	prepared.Steps = append(prepared.Steps, imageStep)
	if err != nil {
		return finish(err)
	}

	// 阶段 3：创建并启动本次 Attempt 专属的新容器。
	containerStep, err := s.runPrepareStep(ctx, execenv.StepCreate, func(
		ctx context.Context,
	) (string, error) {
		return s.prepareContainer(ctx, spec, &prepared)
	})
	prepared.Steps = append(prepared.Steps, containerStep)
	if err != nil {
		return finish(err)
	}

	// 阶段 4：核验容器运行状态与执行入口可用。
	startedStep, err := s.runPrepareStep(ctx, execenv.StepStart, func(
		ctx context.Context,
	) (string, error) {
		return s.verifyContainerRunning(ctx, prepared.Environment)
	})
	prepared.Steps = append(prepared.Steps, startedStep)
	if err != nil {
		return finish(err)
	}

	verifyStep, err := s.runPrepareStep(ctx, execenv.StepVerify, func(
		ctx context.Context,
	) (string, error) {
		return s.verifyContainerUsable(ctx, prepared.Environment)
	})
	prepared.Steps = append(prepared.Steps, verifyStep)
	if err != nil {
		return finish(err)
	}
	s.debug(
		ctx,
		"执行环境已就绪",
		slog.String("container", prepared.Environment.Container().ContainerID),
		slog.String("image_id", prepared.ImageID),
		slog.String("workdir", prepared.Environment.WorkDir()),
	)
	return finish(nil)
}

// runPrepareStep 执行一个准备阶段并记录耗时与结果。
//
// 阶段使用调用方传入的上下文：准备过程中的取消与超时必须传播到远程命令，
// 不能因为"阶段封装"而丢掉取消信号。
func (s *ExecutionEnvironmentService) runPrepareStep(
	ctx context.Context,
	step execenv.Step,
	run func(ctx context.Context) (string, error),
) (execenv.StepResult, error) {
	result := execenv.StepResult{Step: step, Started: time.Now().UTC()}
	detail, err := run(ctx)
	result.FinishedAt = time.Now().UTC()
	result.Detail = detail
	result.Err = err
	if err != nil {
		return result, fmt.Errorf("%s: %w", step, err)
	}
	return result, nil
}

// validatePrepare 校验执行节点、工作区与项目配置。
//
// 该阶段只做只读检查：不做镜像拉取、不创建容器，也不启动任何工作负载。
func (s *ExecutionEnvironmentService) validatePrepare(
	ctx context.Context,
	spec execenv.PrepareSpec,
	prepared *execenv.Prepared,
) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if _, err := s.requireHost(ctx, spec.HostID); err != nil {
		return "", fmt.Errorf("执行节点不可用: %w", err)
	}
	// 只读预检同时确认 Docker CLI / Engine 版本与 API 基准兼容性，并解析远程
	// 管理工作区路径；它不会创建目录或任何 Docker 资源。
	preflight, err := s.images.Preflight(ctx, InitOptions{MachineID: spec.HostID})
	if err != nil {
		return "", fmt.Errorf("远程 Docker 环境预检未通过: %w", err)
	}
	layout, err := docker.NewLayout(preflight.WorkspaceRoot)
	if err != nil {
		return "", err
	}
	management, err := execenv.NewManagementWorkspace(
		preflight.WorkspaceRoot,
		resolveProjectManagementDir(layout, spec.ProjectID),
	)
	if err != nil {
		return "", err
	}
	prepared.Management = management
	workspace, err := execenv.NewWorkspace(spec.WorkDir, spec.SessionDir, nil)
	if err != nil {
		return "", err
	}
	if _, err := s.images.ResolveVersion(
		ctx,
		preflight.Host.ID,
		spec.Environment,
		spec.VersionNumber,
	); err != nil {
		return "", fmt.Errorf("解析环境版本失败: %w", err)
	}
	if err := s.validateCaches(spec, workspace); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"节点 %s，管理工作区 %s，项目目录 %s，Docker CLI %s / Engine %s / API %s",
		preflight.Host.ID,
		management.Root,
		workspace.Project,
		preflight.Preflight.CLIVersion,
		preflight.Preflight.EngineVersion,
		preflight.Preflight.APIVersion,
	), nil
}

// validateCaches 校验项目声明的缓存类型在语言环境中存在。
func (s *ExecutionEnvironmentService) validateCaches(
	spec execenv.PrepareSpec,
	workspace execenv.Workspace,
) error {
	if len(spec.Caches) == 0 {
		return nil
	}
	template, err := docker.LanguageTemplateFor(spec.Environment.Name)
	if err != nil {
		return err
	}
	selected, err := domain.SelectCaches(template.Caches, spec.Caches)
	if err != nil {
		return err
	}
	for _, mount := range selected {
		if err := mount.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// prepareImage 构建或复用环境镜像。
//
// 复用前会核验镜像仍然存在且构建配置一致；构建失败时如实返回构建错误，
// 不会把镜像准备标记为成功。
func (s *ExecutionEnvironmentService) prepareImage(
	ctx context.Context,
	spec execenv.PrepareSpec,
	prepared *execenv.Prepared,
) (string, error) {
	result, err := s.images.Build(ctx, BuildRequest{
		MachineID:     spec.HostID,
		Ref:           spec.Environment,
		VersionNumber: spec.VersionNumber,
	})
	if err != nil {
		return "", fmt.Errorf("准备环境镜像失败: %w", err)
	}
	if !result.Version.Usable() {
		return "", fmt.Errorf(
			"环境版本 %s 尚未构建成功，不能用于创建容器",
			result.Version.Label(),
		)
	}
	prepared.ImageRef = result.Version.ImageRef
	prepared.ImageID = result.Version.ImageID
	prepared.ImageCached = result.Cached
	prepared.VersionLabel = result.Version.Label()
	prepared.BuildLogPath = result.LogPath
	detail := fmt.Sprintf(
		"环境 %s %s 镜像 %s（%s）",
		result.Version.Ref.Label(),
		result.Version.Label(),
		result.Version.ImageRef,
		imageSourceLabel(result.Cached),
	)
	if warning := strings.TrimSpace(result.MetadataWarning); warning != "" {
		detail += "；" + warning
	}
	return detail, nil
}

// imageSourceLabel 返回镜像来源说明。
func imageSourceLabel(cached bool) string {
	if cached {
		return "复用已有构建"
	}
	return "本次构建"
}

// prepareContainer 创建并启动本次执行专属的新容器。
func (s *ExecutionEnvironmentService) prepareContainer(
	ctx context.Context,
	spec execenv.PrepareSpec,
	prepared *execenv.Prepared,
) (string, error) {
	versionNumber, err := domain.ParseVersionLabel(
		imageVersionLabel(prepared),
	)
	if err != nil {
		return "", err
	}
	workspace, err := execenv.NewWorkspace(spec.WorkDir, spec.SessionDir, nil)
	if err != nil {
		return "", err
	}
	detail, err := s.containers.CreateContainer(ctx, CreateContainerRequest{
		MachineID:            spec.HostID,
		Ref:                  spec.Environment,
		VersionNumber:        versionNumber,
		ProjectID:            spec.ProjectID,
		Caches:               spec.Caches,
		TaskID:               spec.TaskID,
		AttemptID:            spec.AttemptID,
		WorkDir:              workspace.Project,
		SkipRunContractCheck: spec.SkipRunContractCheck,
	})
	if err != nil {
		return "", fmt.Errorf("创建任务容器失败: %w", err)
	}
	if detail.Container.State != domain.ContainerStateRunning {
		return "", fmt.Errorf(
			"容器 %s 创建后未进入运行状态（当前 %s）",
			detail.Container.Name,
			detail.Container.State,
		)
	}
	ref, err := execenv.ContainerRefFrom(detail.Container)
	if err != nil {
		return "", err
	}
	environment, err := execenv.NewEnvironment(
		execenv.KindRemoteContainer,
		workspace,
		ref,
		time.Now().UTC(),
	)
	if err != nil {
		return "", err
	}
	prepared.Environment = environment
	return fmt.Sprintf(
		"容器 %s（记录 %s，Docker %s）镜像 %s",
		detail.Container.Name,
		detail.Container.ID,
		detail.Container.DockerID,
		detail.Container.ImageID,
	), nil
}

// imageVersionLabel 返回已准备镜像对应的环境版本标签。
//
// 镜像引用形如 repository:v0001-<hash>，版本标签由环境版本记录决定，
// 这里从 Prepared 中记录的信息无法反推，因此由 prepareImage 保存原始标签。
func imageVersionLabel(prepared *execenv.Prepared) string {
	return prepared.VersionLabel
}

// verifyContainerRunning 核验容器实际处于运行状态。
func (s *ExecutionEnvironmentService) verifyContainerRunning(
	ctx context.Context,
	environment execenv.Environment,
) (string, error) {
	status, err := s.Status(ctx, environment)
	if err != nil {
		return "", fmt.Errorf("核验容器状态失败: %w", err)
	}
	if !status.Usable() {
		return "", fmt.Errorf(
			"容器 %s 未处于可执行状态（%s）",
			environment.Container().Name,
			status,
		)
	}
	return fmt.Sprintf("容器运行状态 %s", status), nil
}

// verifyContainerUsable 核验工作目录与必需执行入口。
//
// 这些检查只验证执行环境基本可用，不替代验证引擎对项目代码的验证。
func (s *ExecutionEnvironmentService) verifyContainerUsable(
	ctx context.Context,
	environment execenv.Environment,
) (string, error) {
	workDir := environment.WorkDir()
	if _, err := s.Exec(ctx, environment, execenv.ExecSpec{
		Command:        "sh",
		Args:           []string{"-c", verifyScriptMakeWritable, verifyScriptName, workDir},
		WorkDir:        workDir,
		Timeout:        verifyProbeTimeout,
		MaxOutputBytes: 4 << 10,
	}); err != nil {
		return "", fmt.Errorf("核验工作目录 %s 失败: %w", workDir, err)
	}
	for _, probe := range []struct {
		name    string
		command string
	}{
		{name: "git", command: "git"},
		{name: "pi", command: "pi"},
	} {
		result, err := s.Exec(ctx, environment, execenv.ExecSpec{
			Command:        probe.command,
			Args:           []string{"--version"},
			WorkDir:        workDir,
			Timeout:        verifyProbeTimeout,
			MaxOutputBytes: 4 << 10,
		})
		if err != nil || !result.Succeeded() {
			return "", fmt.Errorf(
				"容器内缺少可用的 %s 执行入口: %s",
				probe.name,
				result.Summary(),
			)
		}
	}
	return fmt.Sprintf("工作目录 %s 可写，git 与 pi 可用", workDir), nil
}
