package planning_remote

import (
	"os"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// TestRemotePlanningMultiRoundSessions 验收真实 Pi 的多轮 Session 上下文恢复与角色隔离。
//
// 覆盖：两个规划角色各自的真实 Pi 调用、角色 Session 标识与容器内 / 本机路径隔离、
// 同一角色的多轮上下文连续、Archive 内容随轮次增长，以及"取消的调用不得覆盖最后一份
// 完整 Archive"。
func TestRemotePlanningMultiRoundSessions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	session := f.newSession(t, "多轮上下文验收：读取仓库结构并等待后续问题。")
	detail := f.prepare(t, session)
	defer f.release(t, session)
	f.injectPiConfig(t, detail.Container.ID)

	analystMarker := "ZEBRA-" + f.suffix
	analystFirst := f.runAnalystRound(t, session, detail,
		"请记住这个唯一标记："+analystMarker+"。只需回复 OK。")
	analystSessionID, plannerSessionID := f.sessionRoleIDs(t, session)
	if analystSessionID == "" {
		t.Fatalf("需求分析师角色会话标识未持久化")
	}
	if plannerSessionID == "" {
		t.Fatalf("两个规划角色的会话标识必须成对写入，方案规划师标识为空")
	}
	if plannerSessionID == analystSessionID {
		t.Fatalf("两个规划角色一开始就得到同一个会话标识: %s", analystSessionID)
	}
	assertArchiveComplete(t, analystFirst.ArchivePath, analystMarker)
	analystArchive := analystFirst.ArchivePath
	analystContainerPath := analystFirst.ContainerSessionPath
	firstArchiveSize := fileSize(t, analystArchive)

	plannerMarker := "ORCHID-" + f.suffix
	plannerFirst := f.runRoleOK(t, session, detail, domain.RoleTypePlanner,
		"请记住这个唯一标记："+plannerMarker+"。只需回复 OK。")
	analystSessionIDAfterPlanner, plannerSessionID := f.sessionRoleIDs(t, session)
	if plannerSessionID == "" {
		t.Fatalf("方案规划师角色会话标识未持久化")
	}
	if analystSessionIDAfterPlanner != analystSessionID {
		t.Fatalf("调用方案规划师后台改变了需求分析师的会话标识: %s → %s",
			analystSessionID, analystSessionIDAfterPlanner)
	}
	if plannerSessionID == analystSessionID {
		t.Fatalf("两个规划角色共用了同一个 Pi 会话标识: %s", plannerSessionID)
	}
	if plannerFirst.ArchivePath == analystArchive {
		t.Fatalf("两个规划角色共用了同一个本机归档文件: %s", plannerFirst.ArchivePath)
	}
	if plannerFirst.ContainerSessionPath == analystContainerPath {
		t.Fatalf("两个规划角色共用了同一个容器内 Session 文件: %s", plannerFirst.ContainerSessionPath)
	}
	assertArchiveComplete(t, plannerFirst.ArchivePath, plannerMarker)

	// 同一角色的第二轮必须恢复第一轮上下文：会话标识、容器内路径与本机归档都不变。
	analystSecond := f.runAnalystRound(t, session, detail,
		"上一轮我给你的唯一标记是什么？只回复该标记本身，不要解释。")
	if !strings.Contains(analystSecond.Result.FinalText, analystMarker) {
		t.Fatalf("第二轮没有引用第一轮的标记，上下文未连续：期望包含 %q，实际 %q",
			analystMarker, truncate(analystSecond.Result.FinalText, 400))
	}
	if !analystSecond.Result.StableSession.Restored {
		t.Fatalf("第二轮没有从本机归档恢复历史 Session")
	}
	if analystSecond.RoleSessionID != analystSessionID {
		t.Fatalf("第二轮改变了角色会话标识: %s → %s",
			analystSessionID, analystSecond.RoleSessionID)
	}
	if analystSecond.ArchivePath != analystArchive {
		t.Fatalf("第二轮改变了本机归档路径: %s → %s", analystArchive, analystSecond.ArchivePath)
	}
	if analystSecond.ContainerSessionPath != analystContainerPath {
		t.Fatalf("第二轮改变了容器内 Session 路径: %s → %s",
			analystContainerPath, analystSecond.ContainerSessionPath)
	}
	if size := fileSize(t, analystArchive); size <= firstArchiveSize {
		t.Fatalf("第二轮归档没有增长，历史可能被覆盖: %d → %d", firstArchiveSize, size)
	}
	assertArchiveContains(t, analystArchive, analystMarker)

	// 取消的调用不得覆盖最后一份完整 Archive。
	archiveBefore := mustReadFile(t, analystArchive)
	cancelled := f.runCancelledRole(t, session, detail, domain.RoleTypeRequirementAnalyst,
		"请用一句话总结仓库结构，然后等待。")
	if cancelled == nil {
		t.Fatalf("取消的调用没有返回结果，无法核验归档保护")
	}
	archiveAfter := mustReadFile(t, analystArchive)
	if string(archiveAfter) != string(archiveBefore) {
		t.Fatalf("取消的调用覆盖了最后一份完整归档: %d → %d 字节",
			len(archiveBefore), len(archiveAfter))
	}
	assertArchiveContains(t, analystArchive, analystMarker)
	t.Logf(
		"MULTI_ROUND_OK analyst_session=%s planner_session=%s analyst_archive_bytes=%d",
		analystSessionID,
		plannerSessionID,
		fileSize(t, analystArchive),
	)

	// 取消的规划调用按设计终止了独占容器：用生产准备流程重新启动同一个容器，
	// 再核验项目代码目录没有出现任何 Session 或探针文件。
	restarted := f.restartPlanningContainer(t, session, detail)
	f.injectPiConfig(t, restarted.Container.ID)
	assertSnapshotClean(t, f, restarted.Container.ID, detail.SourceCommit)
}

// runAnalystRound 执行一次需求分析师角色调用并断言成功。
func (f *fixture) runAnalystRound(
	t *testing.T,
	session domain.PlanningSession,
	detail app.PlanningRuntimeDetail,
	prompt string,
) app.PlanningAgentResult {
	t.Helper()
	return f.runRoleOK(t, session, detail, domain.RoleTypeRequirementAnalyst, prompt)
}

// runRoleOK 执行一次规划角色调用并断言真实 Pi 正常完成且归档成功。
func (f *fixture) runRoleOK(
	t *testing.T,
	session domain.PlanningSession,
	detail app.PlanningRuntimeDetail,
	roleType domain.RoleType,
	prompt string,
) app.PlanningAgentResult {
	t.Helper()
	result, err := f.runRole(t, session, roleType, prompt)
	if err != nil {
		t.Fatalf("%s 角色调用失败: %v（输出: %s）",
			roleType, err, truncate(result.Result.FinalText, 400))
	}
	if result.Result.Status != agent.RunCompleted {
		t.Fatalf("%s 角色调用未正常完成: 状态 %s，失败原因 %s",
			roleType, result.Result.Status, result.Result.Failure)
	}
	if result.Container.ID != detail.Container.ID {
		t.Fatalf("%s 角色调用使用了另一个容器: 期望 %s，实际 %s",
			roleType, detail.Container.ID, result.Container.ID)
	}
	if !result.Result.StableSession.Archived {
		t.Fatalf("%s 角色调用的 Session 未归档: %s",
			roleType, result.Result.StableSession.Error)
	}
	if result.Result.FinalText == "" {
		t.Fatalf("%s 角色调用没有返回任何文本", roleType)
	}
	assertArchiveComplete(t, result.ArchivePath, "")
	t.Logf(
		"ROLE_CALL role=%s session=%s container_path=%s archive=%s bytes=%d restored=%t",
		roleType,
		result.RoleSessionID,
		result.ContainerSessionPath,
		result.ArchivePath,
		result.Result.StableSession.Bytes,
		result.Result.StableSession.Restored,
	)
	return result
}

// assertArchiveComplete 核验归档文件存在、非空且结构可解析。
func assertArchiveComplete(t *testing.T, archivePath, expectedMarker string) {
	t.Helper()
	if strings.TrimSpace(archivePath) == "" {
		t.Fatalf("归档路径为空")
	}
	content := mustReadFile(t, archivePath)
	if len(content) == 0 {
		t.Fatalf("归档文件为空: %s", archivePath)
	}
	lines := 0
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
			t.Fatalf("归档文件不是 NDJSON 结构: %s", truncate(trimmed, 200))
		}
		lines++
	}
	if lines == 0 {
		t.Fatalf("归档文件没有任何记录: %s", archivePath)
	}
	if expectedMarker != "" && !strings.Contains(string(content), expectedMarker) {
		t.Fatalf("归档文件缺少本轮标记 %q: %s", expectedMarker, archivePath)
	}
}

// assertArchiveContains 断言归档文件包含指定内容。
func assertArchiveContains(t *testing.T, archivePath, expected string) {
	t.Helper()
	content := mustReadFile(t, archivePath)
	if !strings.Contains(string(content), expected) {
		t.Fatalf("归档文件 %s 缺少内容 %q", archivePath, expected)
	}
}

// mustReadFile 读取文件内容，失败即让测试失败。
func mustReadFile(t *testing.T, target string) []byte {
	t.Helper()
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取文件失败: %s: %v", target, err)
	}
	return content
}

// fileSize 返回文件字节数。
func fileSize(t *testing.T, target string) int64 {
	t.Helper()
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("读取文件信息失败: %s: %v", target, err)
	}
	return info.Size()
}
