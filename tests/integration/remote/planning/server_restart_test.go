package planning_remote

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage"
)

// 本文件是第 21 阶段的核心真实验收入口：真实 Server 子进程被强制终止后，由全新的
// 子进程使用同一份数据库、配置与 Session 根目录恢复原规划会话及其运行时绑定。
//
// 与 lifecycle_test.go 的区别是这里不重建应用服务对象：两个阶段分别对应两个真实的
// 操作系统进程，父测试进程只负责启动、监控、终止子进程并核验现场。

// processScene 是独立于 Server 进程的验收 oracle。
//
// 所有取值都来自准备阶段与独立控制连接，不依赖任何 Server 进程：恢复后必须与它逐项
// 一致，因此"恢复成功"不是由被测进程自己声称的。
type processScene struct {
	// SessionID 是规划会话标识。
	SessionID string
	// Status 是准备完成后的会话状态。
	Status string
	// HostID 是绑定的执行节点标识。
	HostID string
	// ContainerRecordID 是规划容器登记记录标识。
	ContainerRecordID string
	// DockerID 是规划容器的真实 Docker ID。
	DockerID string
	// SourceCommit 是快照固定的 commit。
	SourceCommit string
	// AnalystSessionID 是需求分析师角色的 Pi 侧会话标识。
	AnalystSessionID string
	// VolumeName 是快照卷名称。
	VolumeName string
	// ArchivePath 是本机 Session 归档路径。
	ArchivePath string
	// ArchiveDigest 是归档内容的 SHA-256 摘要。
	ArchiveDigest string
	// ContainerSessionPath 是容器内 Session 文件路径。
	ContainerSessionPath string
	// Detail 是准备阶段返回的运行环境事实。
	Detail app.PlanningRuntimeDetail
}

// TestRemotePlanningServerProcessRestartRecovery 验收真实 Server 进程退出与全新进程恢复。
//
// 覆盖：
//   - 阶段 A：真实规划会话、真实快照卷、真实只读容器、真实 Pi 角色调用与本机归档；
//   - 阶段 B：正式入口 `agentflow server` 启动独立子进程，健康检查通过后由该进程
//     读取原会话并核验远程现场；
//   - 阶段 C：强制终止子进程（Unix SIGKILL / Windows TerminateProcess），等待回收；
//   - 阶段 D：全新子进程（新 PID）使用同一数据库启动，原会话、绑定、容器、快照卷与
//     归档全部保持不变，且没有创建替代容器或重新 Clone；
//   - 阶段 E：由新进程按既有策略恢复已停止的容器；容器或快照卷缺失时失败关闭且不
//     创建替代资源；最后由生产 Release 路径释放本次测试资源。
//
// 本用例不与其他真实用例并行：它会独占一份数据库并强制终止服务进程。
func TestRemotePlanningServerProcessRestartRecovery(t *testing.T) {
	f := newFixture(t)
	scene := prepareProcessScene(t, f)
	process := newServerProcess(t, f)
	// 父进程交出数据库：子进程必须自己打开同一份文件并在独占文件锁下运行。
	f.closeDBForServerProcess(t)

	firstRun := process.start(t)
	assertServerUsesTestConfig(t, process, scene)
	assertProcessReadsScene(t, process, scene, "第一次启动的 Server 进程")

	killedRun := process.kill(t)
	if killedRun.PID != firstRun.PID {
		t.Fatalf("终止的进程与启动的进程不一致: %d → %d", firstRun.PID, killedRun.PID)
	}
	if killedRun.ExitStatus == "exit status 0" {
		t.Fatalf("强制终止的子进程不应以 0 退出: %s", killedRun.ExitStatus)
	}
	assertServerUnreachable(t, process)

	secondRun := process.start(t)
	if secondRun.PID == killedRun.PID {
		t.Fatalf("第二次启动复用了同一个 PID %d，无法证明这是全新进程", secondRun.PID)
	}
	// 全新进程必须重新核对同一份数据库与配置，而不是读到另一份数据。
	assertServerUsesTestConfig(t, process, scene)
	assertProcessReadsScene(t, process, scene, "第二次启动的 Server 进程")
	assertNoReplacementResources(t, f, scene)

	verifyStoppedContainerRecovery(t, f, process, scene)
	verifyMissingContainerConflict(t, f, process, scene)
	verifyMissingVolumeConflict(t, f, process, scene)
	releaseThroughFreshProcess(t, f, process, scene)

	finalRun := process.kill(t)
	// 父进程重新接管数据库，核对数据库侧事实与远端现场一致。
	f.reopenDBForParent(t)
	assertDatabaseFactsAfterRelease(t, f, scene)
	t.Logf(
		"SERVER_PROCESS_RESTART_RECOVERY_OK first_pid=%d first_exit=%q second_pid=%d final_exit=%q "+
			"session=%s container=%s docker_id=%s volume=%s commit=%s archive_bytes=%d",
		firstRun.PID,
		killedRun.ExitStatus,
		secondRun.PID,
		finalRun.ExitStatus,
		scene.SessionID,
		scene.ContainerRecordID,
		scene.DockerID,
		scene.VolumeName,
		scene.SourceCommit,
		fileSize(t, scene.ArchivePath),
	)
}

// prepareProcessScene 执行阶段 A：准备真实现场并生成独立 oracle。
func prepareProcessScene(t *testing.T, f *fixture) *processScene {
	t.Helper()
	session := f.newSession(t, "进程重启验收：先记录一个唯一标记，重启后核对它仍然存在。")
	detail := f.prepare(t, session)
	// 资源清理责任已在 prepare 中登记；这里额外兜底一次生产释放，避免残留。
	t.Cleanup(func() {
		if f.db != nil {
			f.release(t, session)
		}
	})
	f.injectPiConfig(t, detail.Container.ID)
	marker := "LANTERN-" + f.suffix
	first := f.runRoleOK(
		t,
		session,
		detail,
		domain.RoleTypeRequirementAnalyst,
		"请记住这个唯一标记："+marker+"。只需回复 OK。",
	)
	analystSessionID, _ := f.sessionRoleIDs(t, session)
	if analystSessionID == "" {
		t.Fatalf("需求分析师角色会话标识未持久化，无法验收角色 Session 恢复")
	}
	current := f.reloadSession(t, session.ID)
	if current.HostID == nil {
		t.Fatalf("规划会话没有绑定执行节点")
	}
	scene := &processScene{
		SessionID:            session.ID.String(),
		Status:               string(current.Status),
		HostID:               current.HostID.String(),
		ContainerRecordID:    detail.Container.ID.String(),
		DockerID:             detail.Container.DockerID,
		SourceCommit:         detail.SourceCommit,
		AnalystSessionID:     analystSessionID,
		VolumeName:           detail.CodeVolumeName,
		ArchivePath:          first.ArchivePath,
		ArchiveDigest:        fileDigest(t, first.ArchivePath),
		ContainerSessionPath: first.ContainerSessionPath,
		Detail:               detail,
	}
	assertArchiveContains(t, scene.ArchivePath, marker)
	assertSceneRemoteFacts(t, f, scene)
	t.Logf(
		"PROCESS_SCENE_READY session=%s status=%s host=%s container=%s docker_id=%s volume=%s commit=%s "+
			"analyst_session=%s archive=%s archive_sha256=%s",
		scene.SessionID,
		scene.Status,
		scene.HostID,
		scene.ContainerRecordID,
		scene.DockerID,
		scene.VolumeName,
		scene.SourceCommit,
		scene.AnalystSessionID,
		scene.ArchivePath,
		scene.ArchiveDigest,
	)
	return scene
}

// assertServerUsesTestConfig 验收子进程使用的是指定测试数据库与测试配置。
func assertServerUsesTestConfig(t *testing.T, p *serverProcess, scene *processScene) {
	t.Helper()
	config := p.runtimeConfig(t)
	if !sameTestPath(config.DatabasePath, p.databasePath) {
		t.Fatalf("Server 进程使用的数据库与测试配置不一致: 期望 %s，实际 %s",
			p.databasePath, config.DatabasePath)
	}
	if config.ListenAddress != p.address {
		t.Fatalf("Server 进程监听地址与测试配置不一致: 期望 %s，实际 %s",
			p.address, config.ListenAddress)
	}
	if !sameTestPath(config.SessionRoot, p.sessionRoot) {
		t.Fatalf("Server 进程 Session 根目录与测试现场不一致: 期望 %s，实际 %s",
			p.sessionRoot, config.SessionRoot)
	}
	if !strings.HasPrefix(
		strings.ToLower(filepath.ToSlash(scene.ArchivePath)),
		strings.ToLower(filepath.ToSlash(filepath.Clean(config.SessionRoot)))+"/",
	) {
		t.Fatalf("Session 归档不在 Server 进程报告的 Session 根目录下: %s / %s",
			scene.ArchivePath, config.SessionRoot)
	}
	t.Logf("SERVER_PROCESS_CONFIG_OK database=%s listen=%s session_root=%s",
		config.DatabasePath, config.ListenAddress, config.SessionRoot)
}

// sameTestPath 忽略分隔符差异比较两个路径。
//
// 配置文件里的数据库路径统一使用 "/"，Windows 下与本机路径只差分隔符，这种差异不代表
// 使用了不同的数据库。
func sameTestPath(left, right string) bool {
	return strings.EqualFold(
		filepath.ToSlash(filepath.Clean(left)),
		filepath.ToSlash(filepath.Clean(right)),
	)
}

// assertProcessReadsScene 验收某个 Server 进程读到的事实与 oracle 完全一致。
func assertProcessReadsScene(
	t *testing.T,
	p *serverProcess,
	scene *processScene,
	label string,
) {
	t.Helper()
	detail := p.sessionDetail(t, scene.SessionID)
	if detail.Session.ID != scene.SessionID {
		t.Fatalf("%s 读到的会话标识不一致: %s → %s", label, scene.SessionID, detail.Session.ID)
	}
	if detail.Session.Status != scene.Status {
		t.Fatalf("%s 读到的会话状态不一致: %s → %s", label, scene.Status, detail.Session.Status)
	}
	if detail.Session.HostID != scene.HostID {
		t.Fatalf("%s 读到的执行节点绑定不一致: %s → %s", label, scene.HostID, detail.Session.HostID)
	}
	if detail.Session.ContainerID != scene.ContainerRecordID {
		t.Fatalf("%s 读到的容器绑定不一致: %s → %s",
			label, scene.ContainerRecordID, detail.Session.ContainerID)
	}
	if detail.Session.SourceCommit != scene.SourceCommit {
		t.Fatalf("%s 读到的固定 commit 不一致: %s → %s",
			label, scene.SourceCommit, detail.Session.SourceCommit)
	}
	if detail.Session.AnalystSessionID != scene.AnalystSessionID {
		t.Fatalf("%s 读到的角色会话标识不一致: %s → %s",
			label, scene.AnalystSessionID, detail.Session.AnalystSessionID)
	}
	inspection := p.inspectRuntime(t, scene.SessionID)
	if inspection.Container.Presence != string(app.PlanningRuntimePresencePresent) {
		t.Fatalf("%s 核验到的规划容器不存在: %+v", label, inspection.Container)
	}
	if inspection.Container.DockerID != scene.DockerID {
		t.Fatalf("%s 核验到的 Docker 容器 ID 不一致: %s → %s",
			label, scene.DockerID, inspection.Container.DockerID)
	}
	if !inspection.Container.Running {
		t.Fatalf("%s 核验到的规划容器没有在运行: %+v", label, inspection.Container)
	}
	if inspection.Snapshot.Presence != string(app.PlanningRuntimePresencePresent) {
		t.Fatalf("%s 核验到的快照卷不存在: %+v", label, inspection.Snapshot)
	}
	if inspection.Snapshot.VolumeName != scene.VolumeName {
		t.Fatalf("%s 核验到的快照卷名称不一致: %s → %s",
			label, scene.VolumeName, inspection.Snapshot.VolumeName)
	}
	if !inspection.Snapshot.CommitVerified || !inspection.Snapshot.CommitMatches {
		t.Fatalf("%s 未能确认快照 commit 与记录一致: %+v", label, inspection.Snapshot)
	}
	if inspection.Snapshot.Commit != scene.SourceCommit {
		t.Fatalf("%s 核验到的快照 commit 不一致: %s → %s",
			label, scene.SourceCommit, inspection.Snapshot.Commit)
	}
	// 归档是权威恢复副本：任何一次启动都不得改写它。
	if digest := fileDigest(t, scene.ArchivePath); digest != scene.ArchiveDigest {
		t.Fatalf("%s 改写了本机 Session 归档: %s → %s", label, scene.ArchiveDigest, digest)
	}
	assertSceneRemoteFacts(t, p.f, scene)
	t.Logf(
		"PROCESS_SCENE_VERIFIED label=%s pid=%d container_presence=%s snapshot_presence=%s commit_matches=%t",
		label,
		p.runs[len(p.runs)-1].PID,
		inspection.Container.Presence,
		inspection.Snapshot.Presence,
		inspection.Snapshot.CommitMatches,
	)
}

// assertSceneRemoteFacts 用独立控制连接核验远程现场。
//
// 它不经过任何 Server 进程：容器存在性、所有权标签、只读挂载、快照卷归属与容器内
// Session 文件都从真实 Docker 现场读取。
func assertSceneRemoteFacts(t *testing.T, f *fixture, scene *processScene) {
	t.Helper()
	inspect, err := f.runner.InspectContainer(f.ctx, scene.DockerID)
	if err != nil {
		t.Fatalf("独立连接读取规划容器失败: %v", err)
	}
	if inspect.Labels[docker.OwnershipLabelKey] != "true" {
		t.Fatalf("规划容器缺少 AgentFlow 管理标签: %+v", inspect.Labels)
	}
	if actual := inspect.Labels[docker.PlanningSessionLabelKey]; actual != scene.SessionID {
		t.Fatalf("规划容器归属的规划会话不匹配: 期望 %s，实际 %s", scene.SessionID, actual)
	}
	if actual := inspect.Labels[docker.HostLabelKey]; actual != scene.HostID {
		t.Fatalf("规划容器归属的执行节点不匹配: 期望 %s，实际 %s", scene.HostID, actual)
	}
	volume, err := f.runner.InspectVolume(f.ctx, scene.VolumeName)
	if err != nil {
		t.Fatalf("独立连接读取快照卷失败: %v", err)
	}
	if volume.Labels[docker.OwnershipLabelKey] != "true" {
		t.Fatalf("快照卷缺少 AgentFlow 管理标签: %+v", volume.Labels)
	}
	if actual := volume.Labels[docker.PlanningSessionLabelKey]; actual != scene.SessionID {
		t.Fatalf("快照卷归属的规划会话不匹配: 期望 %s，实际 %s", scene.SessionID, actual)
	}
	if actual := volume.Labels[docker.HostLabelKey]; actual != scene.HostID {
		t.Fatalf("快照卷归属的执行节点不匹配: 期望 %s，实际 %s", scene.HostID, actual)
	}
	// 项目代码目录仍为只读挂载，rootfs 仍只读，且没有宿主机绑定挂载。
	assertReadOnlyContainer(t, f.inspectRaw(t, scene.DockerID), scene.Detail)
	if strings.TrimSpace(scene.ContainerSessionPath) != "" {
		if !f.containerPathExists(t, scene.DockerID, scene.ContainerSessionPath) {
			t.Fatalf("容器内 Session 文件不存在: %s", scene.ContainerSessionPath)
		}
		t.Logf("CONTAINER_SESSION_FILE_PRESENT path=%s", scene.ContainerSessionPath)
	}
	t.Logf(
		"REMOTE_SCENE_VERIFIED docker_id=%s volume=%s session_label=%s host_label=%s",
		scene.DockerID,
		volume.Name,
		scene.SessionID,
		scene.HostID,
	)
}

// assertNoReplacementResources 验收恢复过程没有创建替代容器、没有重新 Clone。
//
// 判定完全基于真实远端事实：带本会话标签的容器数量必须仍然只有一个，快照工作区的
// HEAD 与干净状态必须保持不变。
func assertNoReplacementResources(t *testing.T, f *fixture, scene *processScene) {
	t.Helper()
	if count := f.countPlanningContainers(t, domain.ID(scene.SessionID)); count != 1 {
		t.Fatalf("本会话的远端规划容器数量应为 1，实际 %d（恢复过程不得创建替代容器）", count)
	}
	assertSnapshotCleanDirectly(t, f, scene.DockerID, scene.SourceCommit)
	t.Logf("NO_REPLACEMENT_RESOURCES session=%s container_count=1 commit=%s",
		scene.SessionID, scene.SourceCommit)
}

// verifyStoppedContainerRecovery 验收新进程按既有策略恢复已确认退出的容器。
func verifyStoppedContainerRecovery(
	t *testing.T,
	f *fixture,
	p *serverProcess,
	scene *processScene,
) {
	t.Helper()
	f.stopContainerDirectly(t, scene.DockerID)
	recovered := p.prepareRuntime(t, scene.SessionID)
	if !recovered.ContainerReused {
		t.Fatalf("恢复已停止的容器时应复用同一个容器，实际返回 container_reused=false")
	}
	if recovered.ContainerID != scene.ContainerRecordID || recovered.DockerID != scene.DockerID {
		t.Fatalf("恢复后容器身份发生变化: %s/%s → %s/%s",
			scene.ContainerRecordID, scene.DockerID, recovered.ContainerID, recovered.DockerID)
	}
	if recovered.SourceCommit != scene.SourceCommit {
		t.Fatalf("恢复后固定 commit 发生变化: %s → %s", scene.SourceCommit, recovered.SourceCommit)
	}
	if !recovered.SnapshotReused {
		t.Fatalf("恢复已准备运行环境时应复用同一份快照卷，实际返回 snapshot_reused=false")
	}
	f.requireContainerRunning(t, scene.DockerID)
	assertReadOnlyContainer(t, f.inspectRaw(t, scene.DockerID), scene.Detail)
	t.Logf("STOPPED_CONTAINER_RECOVERED pid=%d container=%s docker_id=%s snapshot_reused=%t",
		p.runs[len(p.runs)-1].PID, recovered.ContainerID, recovered.DockerID, recovered.SnapshotReused)
}

// verifyMissingContainerConflict 验收容器已不存在时的现场核验与失败关闭。
//
// 现场核验按只读事实返回 absent（不把"不存在"当成"读取失败"），而准备流程必须拒绝
// 继续并给出明确冲突，绝不创建第二个容器覆盖绑定。
func verifyMissingContainerConflict(
	t *testing.T,
	f *fixture,
	p *serverProcess,
	scene *processScene,
) {
	t.Helper()
	f.removeContainerDirectly(t, scene.DockerID)
	inspection := p.inspectRuntime(t, scene.SessionID)
	if inspection.Container.Presence != string(app.PlanningRuntimePresenceAbsent) {
		t.Fatalf("容器已删除时现场核验应报告 absent，实际 %s（%s）",
			inspection.Container.Presence, inspection.Container.Detail)
	}
	status, code, message := p.failure(
		t,
		"POST",
		sessionPath(scene.SessionID)+"/runtime/prepare",
		nil,
		serverRemoteRequestTimeout,
	)
	if status != httpStatusConflict || code != httpapi.CodeConflict {
		t.Fatalf("容器不存在时准备运行环境应返回 409 冲突，实际 %d/%s（%s）",
			status, code, message)
	}
	if !strings.Contains(message, "不存在") {
		t.Fatalf("冲突说明应指明容器已不存在，实际 %q", message)
	}
	if count := f.countPlanningContainers(t, domain.ID(scene.SessionID)); count != 0 {
		t.Fatalf("容器缺失后不得创建替代容器，实际远端存在 %d 个", count)
	}
	// 绑定必须原样保留：恢复失败不得清空或改写数据库中的定位信息。
	detail := p.sessionDetail(t, scene.SessionID)
	if detail.Session.ContainerID != scene.ContainerRecordID ||
		detail.Session.SourceCommit != scene.SourceCommit {
		t.Fatalf("失败关闭后运行时绑定被改写: %+v", detail.Session)
	}
	t.Logf("MISSING_CONTAINER_CONFLICT_OK status=%d code=%s message=%s", status, code, message)
}

// verifyMissingVolumeConflict 验收快照卷已不存在时失败关闭且不重新 Clone。
func verifyMissingVolumeConflict(
	t *testing.T,
	f *fixture,
	p *serverProcess,
	scene *processScene,
) {
	t.Helper()
	f.removeVolumeDirectly(t, scene.VolumeName)
	status, code, message := p.failure(
		t,
		"POST",
		sessionPath(scene.SessionID)+"/runtime/prepare",
		nil,
		serverRemoteRequestTimeout,
	)
	if status != httpStatusConflict || code != httpapi.CodeConflict {
		t.Fatalf("快照卷不存在时准备运行环境应返回 409 冲突，实际 %d/%s（%s）",
			status, code, message)
	}
	if !strings.Contains(message, "快照") {
		t.Fatalf("冲突说明应指明快照不可用，实际 %q", message)
	}
	if err := verifyVolumeAbsent(f.ctx, f.runner, scene.VolumeName); err != nil {
		t.Fatalf("失败关闭后不得重新创建快照卷: %v", err)
	}
	detail := p.sessionDetail(t, scene.SessionID)
	if detail.Session.SourceCommit != scene.SourceCommit {
		t.Fatalf("失败关闭后固定 commit 被改写: %+v", detail.Session)
	}
	t.Logf("MISSING_VOLUME_CONFLICT_OK status=%d code=%s message=%s", status, code, message)
}

// releaseThroughFreshProcess 验收由全新进程按生产 Release 路径释放本次测试资源。
func releaseThroughFreshProcess(
	t *testing.T,
	f *fixture,
	p *serverProcess,
	scene *processScene,
) {
	t.Helper()
	released := p.releaseRuntime(t, scene.SessionID)
	if released.Session.ContainerID != "" || released.Session.HostID != "" ||
		released.Session.SourceCommit != "" {
		t.Fatalf("释放后运行时绑定未清空: %+v", released.Session)
	}
	if f.containerExists(t, scene.DockerID) {
		t.Fatalf("释放后规划容器 %s 仍然存在", scene.DockerID)
	}
	if err := verifyVolumeAbsent(f.ctx, f.runner, scene.VolumeName); err != nil {
		t.Fatalf("释放后快照卷仍然存在: %v", err)
	}
	// 归档是规划资产，不随运行环境释放而删除。
	if digest := fileDigest(t, scene.ArchivePath); digest != scene.ArchiveDigest {
		t.Fatalf("释放运行环境时改写了本机 Session 归档: %s → %s", scene.ArchiveDigest, digest)
	}
	t.Logf("PROCESS_RELEASE_OK container_released=%t snapshot_released=%t archive_sha256=%s",
		released.ContainerReleased, released.SnapshotReleased, scene.ArchiveDigest)
}

// assertDatabaseFactsAfterRelease 验收释放后的数据库事实。
//
// 这一步在父进程重新打开数据库后执行：容器登记记录必须收敛为已删除，规划会话的运行时
// 绑定必须已清空，而会话记录本身仍然可读。
func assertDatabaseFactsAfterRelease(t *testing.T, f *fixture, scene *processScene) {
	t.Helper()
	session := f.reloadSession(t, domain.ID(scene.SessionID))
	if session.ContainerID != "" || session.HostID != nil || session.SourceCommit != "" {
		t.Fatalf("释放后规划会话仍保留运行时绑定: %+v", session)
	}
	// 规划路径不进入提交流程：全程没有创建任务，因此也不存在以任务/尝试为前提的
	// 代码保存记录。
	tasks, err := f.db.ListTasks(f.ctx, storage.TaskFilter{})
	if err != nil {
		t.Fatalf("读取任务记录失败: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("规划路径不应创建任何任务，实际 %d 个", len(tasks))
	}
	record := f.reloadContainer(t, domain.ID(scene.ContainerRecordID))
	if record.State != domain.ContainerStateRemoved {
		t.Fatalf("释放后容器登记记录状态应为 removed，实际 %s", record.State)
	}
	t.Logf("DATABASE_FACTS_OK session=%s status=%s container_state=%s",
		scene.SessionID, session.Status, record.State)
}

// containerPathExists 用独立控制连接判断容器内文件是否存在。
//
// 只有 `test -f` 的精确退出码 0/1 才给出结论：其他退出码说明探针本身失败，必须按测试
// 失败上报，不能当作"文件不存在"。
func (f *fixture) containerPathExists(t *testing.T, dockerID, target string) bool {
	t.Helper()
	commandLine, err := sshclient.ShellCommand("docker", "exec", dockerID, "test", "-f", target)
	if err != nil {
		t.Fatalf("构造容器内探针命令失败: %v", err)
	}
	run, runErr := f.runner.Run(f.ctx, "docker exec", commandLine, planningOpTimeout)
	if runErr != nil {
		t.Fatalf("容器内探针执行失败: %v", runErr)
	}
	switch run.ExitCode {
	case 0:
		return true
	case 1:
		return false
	default:
		t.Fatalf("容器内探针 %s 退出码 %d，结果无法判定: %s",
			target, run.ExitCode, strings.TrimSpace(run.Stderr))
		return false
	}
}

// closeDBForServerProcess 关闭父进程的数据库连接，并在用例结束时自动重开。
//
// 子进程必须自己打开同一份数据库文件：父进程继续持有连接会让"进程重启后恢复"读到的
// 可能是父进程的缓存视图，也会让 SQLite 写锁争用掩盖真实问题。
func (f *fixture) closeDBForServerProcess(t *testing.T) {
	t.Helper()
	f.closeDB(t)
	t.Cleanup(func() {
		if f.db == nil {
			// 用例中途失败时也要恢复一份可用的生产装配：清理回调仍会走生产释放路径。
			f.openDB(t)
			f.assemble(t)
		}
	})
}

// reopenDBForParent 在子进程全部退出后由父进程重新打开数据库并重建生产服务。
//
// 只重开数据库不够：父进程早先构造的容器服务与运行时服务仍指向已经关闭的连接，后续的
// 生产释放路径会因此读到"database is closed"。重建装配让清理与核验继续使用生产服务。
func (f *fixture) reopenDBForParent(t *testing.T) {
	t.Helper()
	if f.db != nil {
		return
	}
	f.openDB(t)
	f.assemble(t)
}

// assertSnapshotCleanDirectly 用独立控制连接核验快照工作区未被改写。
//
// 它不经过生产容器服务：父进程在子进程运行期间已交出数据库，任何依赖数据库的生产服务
// 调用都会读到关闭的连接，因此现场核验一律走独立连接。
func assertSnapshotCleanDirectly(t *testing.T, f *fixture, dockerID, expectedCommit string) {
	t.Helper()
	commit := f.execRaw(t, dockerID,
		[]string{"git", "-C", planningWorkDir, "rev-parse", "HEAD"})
	if commit != expectedCommit {
		t.Fatalf("快照 commit 与记录不一致: 期望 %s，实际 %s", expectedCommit, commit)
	}
	status := f.execRaw(t, dockerID,
		[]string{"git", "-C", planningWorkDir, "status", "--porcelain"})
	if strings.TrimSpace(status) != "" {
		t.Fatalf("快照工作区出现意外改动:\n%s", status)
	}
	t.Logf("SNAPSHOT_CLEAN_DIRECT commit=%s", commit)
}

// httpStatusConflict 是 409 的具名常量，避免在断言里散落数字。
const httpStatusConflict = 409
