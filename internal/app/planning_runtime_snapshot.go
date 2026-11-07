package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// 规划快照的命令超时。
const (
	// planningSnapshotCloneTimeout 是容器内 Clone 与 checkout 的超时时间。
	//
	// 不做浅克隆，因此与代码获取使用同样的宽松上限。
	planningSnapshotCloneTimeout = 20 * time.Minute
	// planningSnapshotProbeTimeout 是容器内轻量 Git 查询的超时时间。
	planningSnapshotProbeTimeout = 60 * time.Second
	// planningSnapshotRunTimeout 是一次性只读核验容器的运行超时时间。
	planningSnapshotRunTimeout = 2 * time.Minute
)

// planningSnapshotRequest 是准备固定 commit 快照的输入。
type planningSnapshotRequest struct {
	session domain.PlanningSession
	project domain.Project
	host    domain.ExecutionHost
	version domain.EnvironmentVersion
}

// planningSnapshotResult 是快照卷的准备结果。
type planningSnapshotResult struct {
	// VolumeName 是快照卷名称。
	VolumeName string
	// Commit 是快照实际检出的完整 commit SHA。
	Commit string
	// Created 表示本次新建了快照卷。
	Created bool
	// Reused 表示复用并核验了已有快照卷。
	Reused bool
}

// planningGitOutput 在快照工作区内执行一条只读 Git 查询并返回标准输出。
type planningGitOutput func(ctx context.Context, args ...string) (string, error)

// planningSnapshotVolumeName 返回规划会话在某执行节点上的稳定快照卷名。
//
// 卷名只由服务端依据会话与节点标识生成，因此同一会话在同一节点上始终得到同一个卷；
// 不同会话、不同节点的卷名互不相同。
func planningSnapshotVolumeName(sessionID, hostID domain.ID) string {
	return "agentflow-plan-" + sessionID.String() + "-" + hostID.String()
}

// planningPrepContainerName 返回一次性快照准备容器的稳定名称。
func planningPrepContainerName(sessionID, hostID domain.ID) (string, error) {
	name := "agentflow-planprep-" + sessionID.String() + "-" + hostID.String()
	if err := domain.ValidateContainerName(name); err != nil {
		return "", err
	}
	return name, nil
}

// planningRuntimeLabels 返回规划资源的可追溯标签。
//
// 所有权标签由 docker 包统一追加；这里只补充执行节点与规划会话标识。
func planningRuntimeLabels(sessionID, hostID domain.ID) map[string]string {
	return map[string]string{
		docker.HostLabelKey:            hostID.String(),
		docker.PlanningSessionLabelKey: sessionID.String(),
	}
}

// ensurePlanningSnapshot 准备或复用本会话的固定 commit 快照卷。
func (s *PlanningRuntimeService) ensurePlanningSnapshot(
	ctx context.Context,
	request planningSnapshotRequest,
) (planningSnapshotResult, error) {
	volumeName := planningSnapshotVolumeName(request.session.ID, request.host.ID)
	result := planningSnapshotResult{VolumeName: volumeName}
	err := s.containers.withRemoteDocker(
		ctx,
		request.host.ID.String(),
		false,
		func(ctx context.Context, remote *remoteDocker) error {
			ensured, ensureErr := s.ensureSnapshot(ctx, remote, request, volumeName)
			result = ensured
			return ensureErr
		},
	)
	if err != nil {
		return result, planningRuntimeStagedOrFailed(err, PlanningRuntimeStageSnapshot, "准备快照卷失败")
	}
	return result, nil
}

// ensureSnapshot 在单个远程会话内准备并核验快照卷。
func (s *PlanningRuntimeService) ensureSnapshot(
	ctx context.Context,
	remote *remoteDocker,
	request planningSnapshotRequest,
	volumeName string,
) (planningSnapshotResult, error) {
	result := planningSnapshotResult{VolumeName: volumeName}
	if err := remote.runner.CheckRunContract(ctx, request.version.ImageRef); err != nil {
		if docker.ErrorCodeOf(err) == docker.CodeStateUncertain {
			return result, planningRuntimeFailure(
				ErrPlanningRuntimeUncertain,
				PlanningRuntimeStageSnapshot,
				"语言环境镜像的运行契约预检结果不确定",
				err,
			)
		}
		return result, planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			PlanningRuntimeStageSnapshot,
			"语言环境镜像不满足常驻运行契约",
			err,
		)
	}
	volume, found, inspectErr := inspectPlanningVolume(ctx, remote, volumeName)
	if inspectErr != nil {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			PlanningRuntimeStageSnapshot,
			"读取快照卷失败",
			inspectErr,
		)
	}
	if found {
		if err := verifyPlanningSnapshotVolume(volume, volumeName, request.session.ID, request.host.ID); err != nil {
			return result, planningRuntimeFailure(
				ErrPlanningRuntimeConflict,
				PlanningRuntimeStageSnapshot,
				"快照卷归属与当前规划会话不一致",
				err,
			)
		}
		commit, verifyErr := verifySnapshotOnRemote(ctx, remote, planningSnapshotTargetOf(request))
		if verifyErr != nil {
			return result, verifyErr
		}
		if expected := strings.TrimSpace(request.session.SourceCommit); expected != "" && expected != commit {
			return result, planningRuntimeFailure(
				ErrPlanningRuntimeConflict,
				PlanningRuntimeStageSnapshotVerify,
				"快照实际 commit "+commit+" 与已记录的 "+expected+" 不一致，拒绝覆盖现场",
				nil,
			)
		}
		result.Commit = commit
		result.Reused = true
		return result, nil
	}
	if expected := strings.TrimSpace(request.session.SourceCommit); expected != "" {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageSnapshot,
			"规划会话已记录固定 commit "+expected+"，但快照卷不存在，不能重新克隆覆盖现场",
			nil,
		)
	}
	labels := planningRuntimeLabels(request.session.ID, request.host.ID)
	if err := remote.runner.EnsureVolume(ctx, volumeName, labels); err != nil {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			PlanningRuntimeStageSnapshot,
			"创建快照卷失败",
			err,
		)
	}
	created, stillFound, createdErr := inspectPlanningVolume(ctx, remote, volumeName)
	if createdErr != nil || !stillFound {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			PlanningRuntimeStageSnapshot,
			"快照卷创建后未能核验",
			createdErr,
		)
	}
	if err := verifyPlanningSnapshotVolume(created, volumeName, request.session.ID, request.host.ID); err != nil {
		return result, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageSnapshot,
			"新建快照卷的归属与当前规划会话不一致",
			err,
		)
	}
	commit, cloneErr := s.cloneSnapshot(ctx, remote, request, volumeName)
	if cloneErr != nil {
		return result, cloneErr
	}
	result.Commit = commit
	result.Created = true
	return result, nil
}

// inspectPlanningVolume 读取命名卷；卷不存在时返回 found=false。
func inspectPlanningVolume(
	ctx context.Context,
	remote *remoteDocker,
	volumeName string,
) (docker.VolumeInspect, bool, error) {
	volume, err := remote.runner.InspectVolume(ctx, volumeName)
	if err != nil {
		if docker.ErrorCodeOf(err) == docker.CodeVolumeNotFound {
			return docker.VolumeInspect{}, false, nil
		}
		return docker.VolumeInspect{}, false, err
	}
	return volume, true, nil
}

// verifyPlanningSnapshotVolume 核验快照卷确实属于当前会话与执行节点。
//
// 名称、管理标签、节点标签与会话标签必须同时匹配：只凭卷名相同不足以证明所有权，
// 也不能因为名称相似就删除别人的卷。
func verifyPlanningSnapshotVolume(
	volume docker.VolumeInspect,
	name string,
	sessionID, hostID domain.ID,
) error {
	if strings.TrimSpace(volume.Name) != name {
		return fmt.Errorf("快照卷名称不一致: 期望 %s，实际 %s", name, volume.Name)
	}
	if volume.Labels[docker.OwnershipLabelKey] != "true" {
		return fmt.Errorf("快照卷缺少 AgentFlow 管理标签: %s", name)
	}
	if actual := volume.Labels[docker.HostLabelKey]; actual != hostID.String() {
		return fmt.Errorf("快照卷所属节点标签不一致: 期望 %s，实际 %s", hostID, actual)
	}
	if actual := volume.Labels[docker.PlanningSessionLabelKey]; actual != sessionID.String() {
		return fmt.Errorf("快照卷所属规划会话标签不一致: 期望 %s，实际 %s", sessionID, actual)
	}
	return nil
}

// cloneSnapshot 在一次性准备容器内克隆仓库、检出固定 commit 并核验工作区。
//
// 准备容器以可写方式挂载本次快照卷，完成后立即删除；它不会被登记为受管理的执行
// 容器，也不会写入 PlanningSession.ContainerID。
func (s *PlanningRuntimeService) cloneSnapshot(
	ctx context.Context,
	remote *remoteDocker,
	request planningSnapshotRequest,
	volumeName string,
) (string, error) {
	name, err := planningPrepContainerName(request.session.ID, request.host.ID)
	if err != nil {
		return "", planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			PlanningRuntimeStageSnapshot,
			"生成临时准备容器名称失败",
			err,
		)
	}
	workDir := request.project.EffectiveWorkDir()
	dockerID, startErr := s.startSnapshotPrepContainer(ctx, remote, request, name, volumeName, workDir)
	if startErr != nil {
		return "", startErr
	}
	commit, cloneErr := s.runPlanningClone(ctx, remote, request, dockerID, workDir)
	if cloneErr != nil && planningRuntimeUncertain(cloneErr) {
		// 结果不确定时保留准备容器与快照卷：远程可能仍在 Clone，盲目删除会破坏现场。
		return "", cloneErr
	}
	removeErr := s.removeTemporaryContainer(ctx, remote, dockerID)
	if cloneErr != nil {
		return "", s.discardSnapshot(ctx, remote, volumeName, cloneErr, removeErr)
	}
	if removeErr != nil {
		return "", planningRuntimeStagedOrFailed(
			removeErr,
			PlanningRuntimeStageSnapshot,
			"快照准备容器未能释放",
		)
	}
	return commit, nil
}

// startSnapshotPrepContainer 启动并核验一次性准备容器。
func (s *PlanningRuntimeService) startSnapshotPrepContainer(
	ctx context.Context,
	remote *remoteDocker,
	request planningSnapshotRequest,
	name, volumeName, workDir string,
) (string, error) {
	dockerID, err := remote.runner.StartTemporaryContainer(ctx, docker.TemporaryContainerSpec{
		Name:     name,
		ImageRef: request.version.ImageRef,
		WorkDir:  workDir,
		Mounts: []docker.VolumeMountSpec{
			{VolumeName: volumeName, ContainerPath: workDir, ReadOnly: false},
		},
		Labels: planningRuntimeLabels(request.session.ID, request.host.ID),
	})
	if err != nil {
		if docker.ErrorCodeOf(err) == docker.CodeContainerConflict {
			return "", planningRuntimeFailure(
				ErrPlanningRuntimeConflict,
				PlanningRuntimeStageSnapshot,
				"已存在同名临时准备容器，现场需要先核验，不能自动删除",
				err,
			)
		}
		return "", planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			PlanningRuntimeStageSnapshot,
			"启动临时准备容器失败",
			err,
		)
	}
	if err := verifyTemporaryPlanningContainer(ctx, remote, dockerID, name, request); err != nil {
		return "", err
	}
	return dockerID, nil
}

// inspectRemotePrepContainer 按固定名称读取一次性准备容器；容器不存在时 found=false。
//
// 名称由服务端依据规划会话与执行节点生成，因此"按名称查不到"就是"本会话没有准备容器"，
// 与"读取失败"必须区分。
func inspectRemotePrepContainer(
	ctx context.Context,
	remote *remoteDocker,
	name string,
) (docker.ContainerInspect, bool, error) {
	inspect, err := remote.runner.InspectContainer(ctx, name)
	if err != nil {
		if docker.ErrorCodeOf(err) == docker.CodeContainerNotFound {
			return docker.ContainerInspect{}, false, nil
		}
		return docker.ContainerInspect{}, false, err
	}
	return inspect, true, nil
}

// verifyPlanningPrepContainer 核验一次性准备容器的身份与归属。
//
// 只有名称、AgentFlow 管理标签、节点标签与规划会话标签全部匹配，且不带正式容器的登记
// 标签时才算本会话的一次性准备容器：只凭名称相同就删除会误删其他会话、其他节点甚至正式
// 规划容器。
func verifyPlanningPrepContainer(
	inspect docker.ContainerInspect,
	name string,
	sessionID, hostID domain.ID,
) error {
	if strings.TrimSpace(inspect.ID) == "" {
		return fmt.Errorf("一次性准备容器的 Docker 容器 ID 为空: %s", name)
	}
	if strings.TrimSpace(inspect.Name) != name {
		return fmt.Errorf("一次性准备容器名称不一致: 期望 %s，实际 %s", name, inspect.Name)
	}
	if inspect.Labels[docker.OwnershipLabelKey] != "true" {
		return fmt.Errorf("一次性准备容器缺少 AgentFlow 管理标签: %s", name)
	}
	if actual := inspect.Labels[docker.ContainerLabelKey]; actual != "" {
		return fmt.Errorf("一次性准备容器带容器登记标签（%s），属于受管理容器: %s", actual, name)
	}
	if actual := inspect.Labels[docker.HostLabelKey]; actual != hostID.String() {
		return fmt.Errorf("一次性准备容器所属节点标签不一致: 期望 %s，实际 %s", hostID, actual)
	}
	if actual := inspect.Labels[docker.PlanningSessionLabelKey]; actual != sessionID.String() {
		return fmt.Errorf("一次性准备容器所属规划会话标签不一致: 期望 %s，实际 %s", sessionID, actual)
	}
	return nil
}

// verifyTemporaryPlanningContainer 核验一次性容器确实由本会话创建且已经运行。
func verifyTemporaryPlanningContainer(
	ctx context.Context,
	remote *remoteDocker,
	dockerID, name string,
	request planningSnapshotRequest,
) error {
	inspect, err := remote.runner.InspectContainer(ctx, dockerID)
	if err != nil {
		return planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			PlanningRuntimeStageSnapshot,
			"核验临时准备容器失败",
			err,
		)
	}
	if err := verifyPlanningPrepContainer(inspect, name, request.session.ID, request.host.ID); err != nil {
		return planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageSnapshot,
			"临时准备容器的归属与本次准备不一致",
			err,
		)
	}
	if !inspect.IsActivelyRunning() {
		return planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			PlanningRuntimeStageSnapshot,
			"临时准备容器启动后未进入运行状态",
			fmt.Errorf("Docker 状态 %s，退出码 %d", inspect.State, inspect.ExitCode),
		)
	}
	return nil
}

// runPlanningClone 在准备容器内执行 Clone、分支解析、detached checkout 与核验。
func (s *PlanningRuntimeService) runPlanningClone(
	ctx context.Context,
	remote *remoteDocker,
	request planningSnapshotRequest,
	dockerID, workDir string,
) (string, error) {
	repository, err := domain.ParseGitRepositoryURL(request.project.RepositoryURL)
	if err != nil {
		return "", planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			PlanningRuntimeStageSnapshot,
			"项目仓库地址不合法",
			err,
		)
	}
	branch, err := domain.NormalizeGitBranch(request.project.NormalizedBaseBranch())
	if err != nil {
		return "", planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			PlanningRuntimeStageSnapshot,
			"项目基础分支不合法",
			err,
		)
	}
	if _, err := s.execPlanningCommand(
		ctx,
		remote,
		dockerID,
		workDir,
		[]string{"git", "--version"},
		planningSnapshotProbeTimeout,
	); err != nil {
		return "", planningRuntimeFailure(
			ErrPlanningRuntimePrerequisite,
			PlanningRuntimeStageSnapshot,
			"临时准备容器内没有可用的 Git",
			err,
		)
	}
	if err := s.ensureSnapshotTargetEmpty(ctx, remote, dockerID, workDir); err != nil {
		return "", err
	}
	cloneCommand := []string{"git", "clone", "--no-hardlinks", "--", repository.Raw(), "."}
	if _, err := s.execPlanningCommand(
		ctx,
		remote,
		dockerID,
		workDir,
		cloneCommand,
		planningSnapshotCloneTimeout,
	); err != nil {
		return "", planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			PlanningRuntimeStageSnapshot,
			"在临时准备容器内 Clone 项目代码失败",
			err,
		)
	}
	commit, err := s.resolveBaseBranchCommit(ctx, remote, dockerID, workDir, branch)
	if err != nil {
		return "", err
	}
	if _, err := s.execPlanningCommand(
		ctx,
		remote,
		dockerID,
		workDir,
		[]string{"git", "checkout", "--detach", commit},
		planningSnapshotCloneTimeout,
	); err != nil {
		return "", planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			PlanningRuntimeStageSnapshot,
			"检出固定 commit 失败",
			err,
		)
	}
	git := func(ctx context.Context, args ...string) (string, error) {
		result, execErr := s.execPlanningCommand(
			ctx,
			remote,
			dockerID,
			workDir,
			append([]string{"git"}, args...),
			planningSnapshotProbeTimeout,
		)
		if execErr != nil {
			return "", execErr
		}
		return result.Stdout, nil
	}
	verified, err := verifyPlanningSnapshotWorktree(ctx, git, commit, true)
	if err != nil {
		return "", planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			PlanningRuntimeStageSnapshotVerify,
			"快照工作区核验失败",
			err,
		)
	}
	return verified, nil
}

// ensureSnapshotTargetEmpty 确认快照卷目标目录为空。
func (s *PlanningRuntimeService) ensureSnapshotTargetEmpty(
	ctx context.Context,
	remote *remoteDocker,
	dockerID, workDir string,
) error {
	result, err := s.execPlanningCommand(
		ctx,
		remote,
		dockerID,
		workDir,
		[]string{"ls", "-A", "."},
		planningSnapshotProbeTimeout,
	)
	if err != nil {
		return planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			PlanningRuntimeStageSnapshot,
			"读取快照卷目标目录失败",
			err,
		)
	}
	if strings.TrimSpace(result.Stdout) != "" {
		return planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageSnapshot,
			"快照卷目标目录非空，拒绝覆盖已有内容",
			nil,
		)
	}
	return nil
}

// resolveBaseBranchCommit 把项目基础分支解析为完整 commit SHA。
func (s *PlanningRuntimeService) resolveBaseBranchCommit(
	ctx context.Context,
	remote *remoteDocker,
	dockerID, workDir, branch string,
) (string, error) {
	candidates := []string{
		"refs/remotes/origin/" + branch,
		"refs/heads/" + branch,
	}
	for _, ref := range candidates {
		result, err := remote.runner.ExecCommandIn(
			ctx,
			dockerID,
			[]string{"git", "rev-parse", "--verify", "--quiet", ref + "^{commit}"},
			docker.ExecOptions{WorkDir: workDir, Timeout: planningSnapshotProbeTimeout},
		)
		if err != nil {
			if docker.ErrorCodeOf(err) == docker.CodeStateUncertain || !result.Certain {
				return "", planningRuntimeFailure(
					ErrPlanningRuntimeUncertain,
					PlanningRuntimeStageSnapshot,
					"读取基础分支 commit 时结果不确定",
					err,
				)
			}
			if result.ExitCode == 1 {
				continue
			}
			return "", planningRuntimeFailure(
				ErrPlanningRuntimeFailed,
				PlanningRuntimeStageSnapshot,
				"读取基础分支 "+branch+" 的 commit 失败",
				err,
			)
		}
		commit := strings.TrimSpace(result.Stdout)
		if commit == "" {
			continue
		}
		if err := domain.ValidateGitCommitSHA("source_commit", commit); err != nil {
			return "", planningRuntimeFailure(
				ErrPlanningRuntimeFailed,
				PlanningRuntimeStageSnapshot,
				"基础分支返回的 commit SHA 非法",
				err,
			)
		}
		return commit, nil
	}
	return "", planningRuntimeFailure(
		ErrPlanningRuntimePrerequisite,
		PlanningRuntimeStageSnapshot,
		"项目基础分支 "+branch+" 在克隆结果中不存在",
		nil,
	)
}

// verifySnapshotOnRemote 用一次性只读容器核验已有快照卷的实际 commit 与工作区状态。
//
// 该路径不 Clone、不 checkout、不 reset：只读取 HEAD、当前引用与工作区状态。核验容器以前
// 台 `--rm` 方式运行且只读挂载快照卷，结束后自动删除，因此不产生需要后续清理的远程资源。
func verifySnapshotOnRemote(
	ctx context.Context,
	remote *remoteDocker,
	target PlanningRuntimeSnapshotTarget,
) (string, error) {
	volumeName := strings.TrimSpace(target.VolumeName)
	workDir := strings.TrimSpace(target.WorkDir)
	if workDir == "" {
		workDir = domain.DefaultContainerWorkDir
	}
	git := func(ctx context.Context, args ...string) (string, error) {
		result, err := remote.runner.RunOneShotContainer(
			ctx,
			docker.OneShotContainerSpec{
				ImageRef: target.ImageRef,
				WorkDir:  workDir,
				Mounts: []docker.VolumeMountSpec{
					{VolumeName: volumeName, ContainerPath: workDir, ReadOnly: true},
				},
				Labels:  planningRuntimeLabels(target.SessionID, target.HostID),
				Command: append([]string{"git"}, args...),
			},
			planningSnapshotRunTimeout,
		)
		if err != nil {
			if docker.ErrorCodeOf(err) == docker.CodeStateUncertain || (result.Started && !result.Certain) {
				return "", fmt.Errorf("%w: 快照核验命令未能确认结束", ErrPlanningRuntimeUncertain)
			}
			return "", fmt.Errorf(
				"容器内 Git 查询失败（退出码 %d）: %s",
				result.ExitCode,
				strings.TrimSpace(result.Stderr),
			)
		}
		return result.Stdout, nil
	}
	// 只读挂载下只核验 HEAD 与 detached 状态：git status 可能刷新索引，在没有
	// 写权限的卷上会给出与快照完整性无关的失败。工作区是否干净由准备阶段在可写
	// 卷上核验一次。
	commit, err := verifyPlanningSnapshotWorktree(ctx, git, "", false)
	if err != nil {
		if planningRuntimeUncertain(err) {
			return "", planningRuntimeFailure(
				ErrPlanningRuntimeUncertain,
				PlanningRuntimeStageSnapshotVerify,
				"快照核验结果不确定",
				err,
			)
		}
		return "", planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageSnapshotVerify,
			"快照卷无法核验为可复用的完整快照",
			err,
		)
	}
	return commit, nil
}

// planningSnapshotTargetOf 从准备请求构造只读核验的定位信息。
func planningSnapshotTargetOf(request planningSnapshotRequest) PlanningRuntimeSnapshotTarget {
	return PlanningRuntimeSnapshotTarget{
		SessionID:  request.session.ID,
		HostID:     request.host.ID,
		VolumeName: planningSnapshotVolumeName(request.session.ID, request.host.ID),
		ImageRef:   request.version.ImageRef,
		WorkDir:    request.project.EffectiveWorkDir(),
	}
}

// verifyPlanningSnapshotWorktree 校验提交 SHA 与 detached HEAD，并按需校验工作区干净。
//
// 只有准备阶段在可写快照卷上要求工作区干净；只读复核不依赖 git status，避免把
// 索引刷新失败误判为快照不完整。
func verifyPlanningSnapshotWorktree(
	ctx context.Context,
	git planningGitOutput,
	expected string,
	requireClean bool,
) (string, error) {
	commitOutput, err := git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("读取 HEAD 失败: %w", err)
	}
	commit := strings.TrimSpace(commitOutput)
	if err := domain.ValidateGitCommitSHA("source_commit", commit); err != nil {
		return "", fmt.Errorf("HEAD 不是完整 commit SHA: %w", err)
	}
	if expected != "" && commit != expected {
		return "", fmt.Errorf("HEAD %s 与预期 commit %s 不一致", commit, expected)
	}
	branchOutput, err := git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("读取当前引用失败: %w", err)
	}
	if actual := strings.TrimSpace(branchOutput); actual != "HEAD" {
		return "", fmt.Errorf("快照必须处于 detached HEAD，当前引用为 %s", actual)
	}
	if requireClean {
		statusOutput, err := git(ctx, "status", "--porcelain")
		if err != nil {
			return "", fmt.Errorf("读取工作区状态失败: %w", err)
		}
		if strings.TrimSpace(statusOutput) != "" {
			return "", fmt.Errorf("快照工作区存在未提交变更")
		}
	}
	return commit, nil
}

// execPlanningCommand 在容器内执行一条命令，并把不确定结果统一归类。
func (s *PlanningRuntimeService) execPlanningCommand(
	ctx context.Context,
	remote *remoteDocker,
	dockerID, workDir string,
	command []string,
	timeout time.Duration,
) (docker.CommandResult, error) {
	result, err := remote.runner.ExecCommandIn(ctx, dockerID, command, docker.ExecOptions{
		WorkDir: workDir,
		Timeout: timeout,
	})
	if err != nil {
		if docker.ErrorCodeOf(err) == docker.CodeStateUncertain {
			return result, fmt.Errorf("%w: 容器内命令未能确认结束", ErrPlanningRuntimeUncertain)
		}
		return result, err
	}
	return result, nil
}

// removeTemporaryContainer 删除一次性容器。
func (s *PlanningRuntimeService) removeTemporaryContainer(
	ctx context.Context,
	remote *remoteDocker,
	dockerID string,
) error {
	if strings.TrimSpace(dockerID) == "" {
		return nil
	}
	if _, err := remote.runner.RemoveContainer(ctx, dockerID); err != nil {
		return fmt.Errorf("删除一次性容器失败: %w", err)
	}
	return nil
}

// discardSnapshot 清理本次新建但已确定失败的专属快照资源。
//
// 只有"命令已确认结束的明确失败"才走到这里：结果不确定的现场必须保留。
func (s *PlanningRuntimeService) discardSnapshot(
	ctx context.Context,
	remote *remoteDocker,
	volumeName string,
	cause, containerErr error,
) error {
	cleanup := containerErr
	if cleanup != nil {
		cleanup = fmt.Errorf("准备容器清理失败: %w", cleanup)
	}
	if _, err := remote.runner.RemoveVolume(ctx, volumeName); err != nil {
		cleanup = errors.Join(cleanup, fmt.Errorf("快照卷清理失败: %w", err))
	}
	if cleanup == nil {
		return cause
	}
	kind := ErrPlanningRuntimeFailed
	if planningRuntimeUncertain(cleanup) {
		kind = ErrPlanningRuntimeUncertain
	}
	return errors.Join(cause, planningRuntimeFailure(
		kind,
		PlanningRuntimeStageSnapshot,
		"快照准备失败后专属资源未能完全清理",
		cleanup,
	))
}

// planningRuntimeUncertain 判断错误是否表示远程状态无法确认。
func planningRuntimeUncertain(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrPlanningRuntimeUncertain) ||
		docker.ErrorCodeOf(err) == docker.CodeStateUncertain
}

// planningRuntimeStaged 判断错误是否已经带规划运行时阶段信息。
func planningRuntimeStaged(err error) bool {
	var staged *PlanningRuntimeError
	return errors.As(err, &staged)
}

// planningRuntimeStagedOrFailed 把未分类错误转换为带阶段信息的失败。
func planningRuntimeStagedOrFailed(err error, stage PlanningRuntimeStage, detail string) error {
	if planningRuntimeStaged(err) {
		return err
	}
	if planningRuntimeUncertain(err) {
		return planningRuntimeFailure(ErrPlanningRuntimeUncertain, stage, detail, err)
	}
	return planningRuntimeFailure(ErrPlanningRuntimeFailed, stage, detail, err)
}
