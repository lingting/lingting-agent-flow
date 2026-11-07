package planning_remote

import (
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// TestRemotePlanningContainerRestartRecovery 验收真实容器停止、应用服务重建与 Session 恢复。
//
// 覆盖：真实停止规划容器并核验 Docker 退出、关闭应用服务与 SQLite 连接后用同一数据库文件
// 与同一 Session 根目录重建服务、用原规划会话继续调用同一容器、容器 / 快照 / commit /
// 角色会话标识 / 本机归档全部保持不变，以及第二轮真实 Pi 调用恢复了历史上下文。
//
// 说明：本用例重建的是应用服务对象与数据库连接，不是 Server 进程崩溃重启；报告中必须
// 如实区分这两种恢复范围。
func TestRemotePlanningContainerRestartRecovery(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	session := f.newSession(t, "容器停止与恢复验收：先记录一个标记，再在重建后核对。")
	detail := f.prepare(t, session)
	defer f.release(t, session)
	f.injectPiConfig(t, detail.Container.ID)

	marker := "LANTERN-" + f.suffix
	first := f.runRoleOK(t, session, detail, domain.RoleTypeRequirementAnalyst,
		"请记住这个唯一标记："+marker+"。只需回复 OK。")
	analystSessionID, _ := f.sessionRoleIDs(t, session)
	archiveBefore := mustReadFile(t, first.ArchivePath)

	// 用生产停止入口停止本次测试容器，并核验真实 Docker 状态。
	stopped, err := f.containers.StopContainer(f.ctx, app.StopContainerRequest{
		ContainerID: detail.Container.ID.String(),
	})
	if err != nil {
		t.Fatalf("停止规划容器失败: %v", err)
	}
	if stopped.Container.State != domain.ContainerStateStopped {
		t.Fatalf("停止后本地容器状态不是 stopped: %s", stopped.Container.State)
	}
	f.requireContainerStopped(t, detail.Container.DockerID)

	// 关闭应用服务与 SQLite 连接，再用同一数据库文件与 Session 根目录重建。
	f.rebuild(t)

	// 重建后的持久化事实必须原样存在：绑定没有被清空或替换。
	afterRebuild := f.reloadSession(t, session.ID)
	if afterRebuild.ContainerID != detail.Container.ID.String() {
		t.Fatalf("服务重建后容器绑定被改变: 期望 %s，实际 %s",
			detail.Container.ID, afterRebuild.ContainerID)
	}
	if afterRebuild.HostID == nil || *afterRebuild.HostID != f.host.ID {
		t.Fatalf("服务重建后执行节点绑定被改变: %v", afterRebuild.HostID)
	}
	if afterRebuild.SourceCommit != detail.SourceCommit {
		t.Fatalf("服务重建后固定 commit 被改变: 期望 %s，实际 %s",
			detail.SourceCommit, afterRebuild.SourceCommit)
	}
	if afterRebuild.AnalystSessionID != analystSessionID {
		t.Fatalf("服务重建后角色会话标识被改变: %s → %s",
			analystSessionID, afterRebuild.AnalystSessionID)
	}

	// 快照卷与固定 commit 在容器停止期间保持不变。
	if _, err := f.runner.InspectVolume(f.ctx, detail.CodeVolumeName); err != nil {
		t.Fatalf("容器停止后快照卷不应被删除: %v", err)
	}

	// 用生产准备流程重新启动同一个容器，再注入临时可写目录中的 Pi 配置（tmpfs 已随
	// 容器停止而清空）。
	restarted := f.restartPlanningContainer(t, session, detail)
	f.injectPiConfig(t, restarted.Container.ID)
	f.requireContainerRunning(t, detail.Container.DockerID)

	second := f.runRoleOK(t, session, restarted, domain.RoleTypeRequirementAnalyst,
		"上一轮我给你的唯一标记是什么？只回复该标记本身，不要解释。")
	if !strings.Contains(second.Result.FinalText, marker) {
		t.Fatalf("重启后第二轮没有恢复历史上下文：期望包含 %q，实际 %q",
			marker, truncate(second.Result.FinalText, 400))
	}
	if !second.Result.StableSession.Restored {
		t.Fatalf("重启后第二轮没有从本机归档恢复历史 Session")
	}
	if second.RoleSessionID != analystSessionID {
		t.Fatalf("重启后角色会话标识被改变: %s → %s", analystSessionID, second.RoleSessionID)
	}
	if second.ArchivePath != first.ArchivePath {
		t.Fatalf("重启后本机归档路径被改变: %s → %s", first.ArchivePath, second.ArchivePath)
	}
	if second.ContainerSessionPath != first.ContainerSessionPath {
		t.Fatalf("重启后容器内 Session 路径被改变: %s → %s",
			first.ContainerSessionPath, second.ContainerSessionPath)
	}
	if size := fileSize(t, second.ArchivePath); int64(len(archiveBefore)) >= size {
		t.Fatalf("重启后归档没有比停止前更大，历史可能被覆盖: %d → %d",
			len(archiveBefore), size)
	}
	assertArchiveContains(t, second.ArchivePath, marker)

	f.assertBindingIntact(t, session, detail)
	f.assertLocalContainerState(t, detail.Container.ID, domain.ContainerStateRunning)
	t.Logf(
		"RESTART_RECOVERY_OK container=%s docker_id=%s volume=%s commit=%s archive_bytes=%d",
		detail.Container.ID,
		detail.Container.DockerID,
		detail.CodeVolumeName,
		detail.SourceCommit,
		fileSize(t, second.ArchivePath),
	)
}

// requireContainerRunning 通过独立控制连接核验容器已确认运行。
func (f *fixture) requireContainerRunning(t *testing.T, dockerID string) {
	t.Helper()
	inspect := f.controlInspectCondition(t, dockerID)
	if !inspect.IsActivelyRunning() {
		t.Fatalf("容器 %s 未被确认运行: Docker 状态 %s，Running=%t，判定 %s",
			dockerID, inspect.State, inspect.Running, inspect.RuntimeCondition())
	}
	t.Logf("REMOTE_CONTAINER_RUNNING docker_id=%s state=%s", dockerID, inspect.State)
}
