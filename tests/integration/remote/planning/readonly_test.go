package planning_remote

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestRemotePlanningReadOnlyEnvironment 验收真实只读环境。
//
// 覆盖：真实容器创建参数在远端的实际落地（rootfs 只读、项目快照只读挂载、唯一可写
// tmpfs、无特权 / 宿主机绑定 / Docker Socket）、规划角色的工具策略上限、真实文件工具与
// 真实 Shell 的写入拒绝，以及规划会话释放后的真实资源删除。
func TestRemotePlanningReadOnlyEnvironment(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	session := f.newSession(t, "分析当前仓库结构，只读取，不修改任何文件。")
	detail := f.prepare(t, session)
	defer f.release(t, session)

	container := detail.Container
	raw := f.inspectRaw(t, container.DockerID)
	assertReadOnlyContainer(t, raw, detail)

	t.Logf(
		"READONLY_RUNTIME container=%s docker_id=%s volume=%s commit=%s",
		container.ID,
		container.DockerID,
		detail.CodeVolumeName,
		detail.SourceCommit,
	)

	assertRoleToolCeiling(t, f)

	// 快照必须固定在 Prepare 记录的 commit 上，且工作区干净。
	assertSnapshotClean(t, f, container.ID, detail.SourceCommit)

	// 注入测试侧模型配置，使真实 Pi 诊断调用可用；它不改变生产角色策略。
	f.injectPiConfig(t, container.ID)

	probePath := planningWorkDir + "/write-tool-probe-" + f.suffix + ".txt"
	assertFileToolWriteRejected(t, f, container.ID, probePath)
	assertFileAbsentInContainer(t, f, container.ID, probePath)

	shellProbe := planningWorkDir + "/shell-probe-" + f.suffix + ".tmp"
	assertShellWriteRejected(t, f, container.ID, shellProbe)
	assertFileAbsentInContainer(t, f, container.ID, shellProbe)

	// 两次真实写入尝试之后，项目快照仍然固定在原 commit，且工作区没有变化。
	assertSnapshotClean(t, f, container.ID, detail.SourceCommit)

	// 释放：真实删除容器与快照卷，并保留历史。
	release := f.release(t, session)
	if !release.ContainerReleased {
		t.Fatalf("规划容器未按已核验结果报告为已释放: %+v", release)
	}
	if !release.SnapshotReleased {
		t.Fatalf("快照卷未按已核验结果报告为已释放: %+v", release)
	}
	f.requireNoContainer(t, container.DockerID)
	f.requireVolumeAbsent(t, detail.CodeVolumeName)
	f.requireNoPrepContainer(t, session)

	released := f.reloadSession(t, session.ID)
	if released.ContainerID != "" {
		t.Fatalf("释放后规划会话仍保留容器绑定: %s", released.ContainerID)
	}
	if released.HostID != nil {
		t.Fatalf("释放后规划会话仍保留执行节点绑定: %s", released.HostID)
	}
	if strings.TrimSpace(released.SourceCommit) != "" {
		t.Fatalf("释放后规划会话仍保留固定 commit: %s", released.SourceCommit)
	}
	messages, err := f.sessions.Messages(f.ctx, session.ID.String())
	if err != nil {
		t.Fatalf("读取规划消息失败: %v", err)
	}
	if len(messages) == 0 {
		t.Fatalf("资源释放后规划消息不应被删除")
	}
	t.Logf("RELEASE_OK container=%s volume=%s messages=%d", container.ID, detail.CodeVolumeName, len(messages))

	// 重复 Release 必须按既有幂等语义收敛：绑定已清空时没有资源可释放，返回成功且
	// 不谎称删除过资源；远端资源仍然不存在，绑定也不会被重新写回。
	repeated := f.release(t, session)
	t.Logf(
		"RELEASE_REPEAT container_released=%t snapshot_released=%t",
		repeated.ContainerReleased,
		repeated.SnapshotReleased,
	)
	f.requireNoContainer(t, container.DockerID)
	f.requireVolumeAbsent(t, detail.CodeVolumeName)
	afterRepeat := f.reloadSession(t, session.ID)
	if afterRepeat.ContainerID != "" || afterRepeat.HostID != nil {
		t.Fatalf("重复释放后规划会话被重新写入运行时绑定: %+v", afterRepeat)
	}
}

// assertReadOnlyContainer 用真实 Docker inspect 核验只读容器现场。
func assertReadOnlyContainer(t *testing.T, raw rawContainerInspect, detail app.PlanningRuntimeDetail) {
	t.Helper()
	if !raw.HostConfig.ReadonlyRootfs {
		t.Fatalf("真实容器 rootfs 不是只读: %+v", raw.HostConfig)
	}
	mount, found := raw.mountFor(planningWorkDir)
	if !found {
		t.Fatalf("真实容器没有把项目快照挂载到 %s: %+v", planningWorkDir, raw.Mounts)
	}
	if mount.Type != "volume" {
		t.Fatalf("项目快照挂载类型必须是命名卷，实际 %q", mount.Type)
	}
	if mount.Name != detail.CodeVolumeName {
		t.Fatalf("项目快照挂载的卷名与准备结果的快照卷不一致: 期望 %s，实际 %s",
			detail.CodeVolumeName, mount.Name)
	}
	if mount.RW {
		t.Fatalf("项目快照挂载必须是只读，实际可写: %+v", mount)
	}
	if _, ok := raw.HostConfig.Tmpfs["/tmp"]; !ok {
		t.Fatalf("真实容器缺少显式声明的可写临时目录 /tmp: %+v", raw.HostConfig.Tmpfs)
	}
	if len(raw.HostConfig.Binds) != 0 {
		t.Fatalf("只读规划容器不允许宿主机绑定挂载，实际: %v", raw.HostConfig.Binds)
	}
	if raw.HostConfig.Privileged {
		t.Fatalf("只读规划容器不允许特权模式")
	}
	if raw.HostConfig.PidMode == "host" {
		t.Fatalf("只读规划容器不允许共享宿主机 PID 命名空间")
	}
	if raw.HostConfig.NetworkMode == "host" {
		t.Fatalf("只读规划容器不允许使用宿主机网络")
	}
	for _, mount := range raw.Mounts {
		if mount.Type == "bind" {
			t.Fatalf("只读规划容器不允许 bind 挂载，实际: %+v", mount)
		}
		if strings.Contains(mount.Source, "docker.sock") ||
			strings.Contains(mount.Destination, "docker.sock") {
			t.Fatalf("只读规划容器不允许挂载 Docker Socket: %+v", mount)
		}
		if mount.Destination != planningWorkDir && mount.RW {
			t.Fatalf("只读规划容器出现绕过只读设计的可写挂载: %+v", mount)
		}
	}
	t.Logf(
		"READONLY_INSPECT readonly_rootfs=%t code_volume=%s code_rw=%t tmpfs=%v",
		raw.HostConfig.ReadonlyRootfs,
		mount.Name,
		mount.RW,
		raw.HostConfig.Tmpfs,
	)
}

// assertRoleToolCeiling 核验两个规划角色在持久化配置上只获得只读工具。
//
// 这是 Pi 工具策略层：它与文件系统只读是两个独立层次，任何一层都不能被当作另一层的证据。
func assertRoleToolCeiling(t *testing.T, f *fixture) {
	t.Helper()
	for _, roleType := range []domain.RoleType{
		domain.RoleTypeRequirementAnalyst,
		domain.RoleTypePlanner,
	} {
		role, err := f.roles.Get(f.ctx, planningRoleIDFor(t, roleType))
		if err != nil {
			t.Fatalf("读取规划角色配置失败: %v", err)
		}
		tools, err := domain.EffectivePiTools(role.Type, role.ToolsPolicy)
		if err != nil {
			t.Fatalf("计算角色 %s 的 Pi 工具失败: %v", role.Type, err)
		}
		for _, forbidden := range []string{domain.PiToolEdit, domain.PiToolWrite, domain.PiToolBash} {
			for _, tool := range tools {
				if tool == forbidden {
					t.Fatalf("规划角色 %s 不应获得工具 %q，实际工具: %v",
						role.Type, forbidden, tools)
				}
			}
		}
		if len(tools) == 0 {
			t.Fatalf("规划角色 %s 没有可用的只读工具，实际工具: %v", role.Type, tools)
		}
		t.Logf("ROLE_TOOLS role=%s tools=%v", role.Type, tools)
	}
}

// assertSnapshotClean 断言项目快照仍在固定 commit 上且没有工作区改动。
func assertSnapshotClean(t *testing.T, f *fixture, containerID domain.ID, expectedCommit string) {
	t.Helper()
	commit := strings.TrimSpace(f.execInContainer(t, containerID,
		[]string{"git", "-C", planningWorkDir, "rev-parse", "HEAD"}).Result.Stdout)
	if commit != expectedCommit {
		t.Fatalf("规划容器内快照 commit 与记录不一致: 期望 %s，实际 %s", expectedCommit, commit)
	}
	status := f.execInContainer(t, containerID,
		[]string{"git", "-C", planningWorkDir, "status", "--porcelain"})
	if changes := strings.TrimSpace(status.Result.Stdout); changes != "" {
		t.Fatalf("规划容器内快照工作区出现意外改动:\n%s", changes)
	}
	t.Logf("SNAPSHOT_CLEAN commit=%s", commit)
}

// assertFileToolWriteRejected 用真实文件工具尝试写入，核验文件系统拒绝。
//
// 该诊断调用只启用 Pi 的文件写入工具，属于只读挂载底层能力测试：规划角色本身没有写入
// 权限，因此这里不能用角色会话代替。
func assertFileToolWriteRejected(t *testing.T, f *fixture, containerID domain.ID, probePath string) {
	t.Helper()
	prompt := "请把这个文件写入目标路径：" + probePath + "，内容为一行说明。只使用写入工具。\n"
	sessionPath := planningPiConfigDir + "/diagnostic-" + f.suffix + ".jsonl"
	command := []string{
		"pi", "--print",
		"--session", sessionPath,
		"--model", f.model,
		"--tools", "write",
		"--no-mcp", "--no-extensions",
	}
	result, err := f.containers.Exec(f.ctx, app.ExecRequest{
		ContainerID: containerID.String(),
		Command:     command,
		WorkDir:     planningWorkDir,
		Stdin:       []byte(prompt),
		Timeout:     planningCommitTimeout,
	})
	output := strings.TrimSpace(result.Result.Stdout + "\n" + result.Result.Stderr)
	t.Logf("FILE_TOOL_PROBE exit=%d output=%s", result.Result.ExitCode, truncate(output, 600))
	if err != nil && result.Result.Certain {
		// 真实写入被拒绝时，Pi 可能以非零码退出；这不是缺陷，但必须能从输出看到拒绝。
		t.Logf("FILE_TOOL_PROBE_EXIT_ERROR=%v", err)
	}
	if !mentionsReadOnlyFailure(output) {
		t.Fatalf("文件工具写入没有被文件系统拒绝的明确证据: exit=%d output=%s",
			result.Result.ExitCode, truncate(output, 600))
	}
}

// assertShellWriteRejected 在规划容器内用真实 Shell 尝试写入，核验文件系统拒绝。
func assertShellWriteRejected(t *testing.T, f *fixture, containerID domain.ID, probePath string) {
	t.Helper()
	script := "printf 'probe' > " + probePath
	result, err := f.containers.Exec(f.ctx, app.ExecRequest{
		ContainerID: containerID.String(),
		Command:     []string{"sh", "-c", script},
		WorkDir:     planningWorkDir,
		Timeout:     planningOpTimeout,
	})
	if err == nil && result.Result.ExitCode == 0 {
		t.Fatalf("规划容器内 Shell 写入被接受（退出码 0），只读挂载未生效: %s", probePath)
	}
	if !mentionsReadOnlyFailure(result.Result.Stderr + "\n" + result.Result.Stdout) {
		t.Fatalf("Shell 写入失败但不是只读 / 权限拒绝: exit=%d output=%s",
			result.Result.ExitCode, truncate(strings.TrimSpace(result.Result.Stderr), 400))
	}
	t.Logf("SHELL_WRITE_REJECTED exit=%d stderr=%s",
		result.Result.ExitCode, truncate(strings.TrimSpace(result.Result.Stderr), 300))
}

// assertFileAbsentInContainer 断言容器内目标文件没有被创建。
//
// 判定只接受确定结论：`test -e <path>` 必须在容器内确认执行、确认结束，且退出码精确
// 为 1。退出码 0 表示目标文件存在；退出码 2、127 与其他取值表示探针本身失败；连接
// 中断、超时、取消或状态不确定时同样不得报告"文件不存在"。失败信息打印探针的退出码
// 与命令结果，不包含任何凭据。
func assertFileAbsentInContainer(t *testing.T, f *fixture, containerID domain.ID, target string) {
	t.Helper()
	result, err := f.containers.Exec(f.ctx, app.ExecRequest{
		ContainerID: containerID.String(),
		Command:     []string{"test", "-e", target},
		WorkDir:     planningWorkDir,
		Timeout:     planningOpTimeout,
	})
	run := result.Result
	verdict, verdictErr := testkit.ClassifyFileAbsence(testkit.FileAbsenceProbe{
		ExitCode:   run.ExitCode,
		Started:    run.Started,
		Certain:    run.Certain,
		Determined: absenceProbeDetermined(run, err),
	})
	if verdictErr != nil {
		t.Fatalf("无法确认容器内文件 %s 是否存在: %v\n探针结果: %s",
			target, verdictErr, describeAbsenceProbe(run, err))
	}
	if verdict != testkit.FileConfirmedAbsent {
		t.Fatalf("只读挂载未拒绝写入：文件 %s 被成功创建\n探针结果: %s",
			target, describeAbsenceProbe(run, err))
	}
	t.Logf("FILE_ABSENT target=%s exit=%d", target, run.ExitCode)
}

// absenceProbeDetermined 判断探针结果是否按退出码返回。
//
// 只有命令确实在容器内执行、并且要么正常结束要么以非零退出码结束时，退出码才代表
// `test -e` 的结论；传输中断、超时、取消与服务前置校验失败都不能按退出码解释。
func absenceProbeDetermined(run docker.CommandResult, err error) bool {
	if run.TimedOut() || run.Cancelled() || !run.Certain {
		return false
	}
	if err == nil {
		return true
	}
	return docker.ErrorCodeOf(err) == docker.CodeCommandFailed
}

// describeAbsenceProbe 返回探针的可诊断描述，不包含连接凭据。
func describeAbsenceProbe(run docker.CommandResult, err error) string {
	output := strings.TrimSpace(run.Stderr + "\n" + run.Stdout)
	return fmt.Sprintf(
		"exit=%d started=%t certain=%t status=%s err=%v output=%s",
		run.ExitCode,
		run.Started,
		run.Certain,
		run.Status,
		err,
		truncate(output, 300),
	)
}

// truncate 截断日志文本，避免把模型输出整段写进测试日志。
func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…（已截断）"
}

// assertArchiveAbsent 断言归档文件不存在。
func assertArchiveAbsent(t *testing.T, target string) {
	t.Helper()
	if _, err := os.Stat(target); err == nil {
		t.Fatalf("归档文件不应存在: %s", target)
	} else if !os.IsNotExist(err) {
		t.Fatalf("无法确认归档文件状态: %s: %v", target, err)
	}
}

// mentionsReadOnlyFailure 判断输出中是否出现只读文件系统或权限拒绝的明确证据。
//
// 证据来自真实文件系统错误（EROFS、只读、permission denied），模型用任何语言描述都
// 可以，但必须是文件系统层面的拒绝，而不是"我选择不写"。
func mentionsReadOnlyFailure(output string) bool {
	lowered := strings.ToLower(output)
	for _, marker := range []string{"erofs", "read-only", "readonly", "permission denied", "只读"} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}
