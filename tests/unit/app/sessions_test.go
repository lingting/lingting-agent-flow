package app_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// newSessionService 在临时目录中打开数据库并构造会话服务。
func newSessionService(t *testing.T) (*app.SessionService, domain.Role, domain.SessionScope) {
	t.Helper()
	ctx := testContext(t)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: filepath.Join(testkit.Dir(t), "agentflow.db")})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	logger, err := logging.New(nopWriter{}, logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	service, err := app.NewSessionService(db, filepath.Join(testkit.Dir(t), "sessions"), logger.Slog())
	if err != nil {
		t.Fatalf("构造会话服务失败: %v", err)
	}

	role, err := domain.NewRole(
		"开发工程师",
		domain.RoleTypeDeveloper,
		"provider/model-1",
		"实现步骤",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := db.CreateRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	requirement, err := domain.NewRequirement(
		"需求标题",
		"需求正文",
		domain.PriorityNormal,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "/repo", "main", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	return service, role, domain.SessionScope{TaskID: task.ID}
}

// TestRecordSessionSuccess 验证成功执行的会话元数据被完整写入。
func TestRecordSessionSuccess(t *testing.T) {
	t.Parallel()
	service, role, scope := newSessionService(t)
	startedAt := time.Now().UTC()
	finishedAt := startedAt.Add(2 * time.Second)
	result := agent.RunResult{
		SessionID:         "session-1",
		ProcessID:         4242,
		Status:            agent.RunCompleted,
		ExitCode:          0,
		StartedAt:         startedAt,
		FinishedAt:        finishedAt,
		Failure:           "",
		ExternalSessionID: "",
		Session: agent.SessionFiles{
			JSONLPath:    "/data/sessions/session.jsonl",
			HTMLPath:     "/data/sessions/session.html",
			Completeness: agent.SessionComplete,
			ExportStatus: agent.ExportSucceeded,
		},
	}
	session, err := service.Record(testContext(t), app.RecordRequest{
		Scope:     scope,
		Role:      role,
		SessionID: "session-1",
		Result:    result,
	})
	if err != nil {
		t.Fatalf("记录会话失败: %v", err)
	}
	if session.ID != domain.ID("session-1") {
		t.Fatalf("会话标识不符: %s", session.ID)
	}
	if session.Status != domain.SessionStatusCompleted {
		t.Fatalf("期望状态 %s，实际 %s", domain.SessionStatusCompleted, session.Status)
	}
	if session.JSONLPath != result.Session.JSONLPath {
		t.Fatalf("原始 Session 路径未记录: %q", session.JSONLPath)
	}
	if session.HTMLPath != result.Session.HTMLPath {
		t.Fatalf("HTML 路径未记录: %q", session.HTMLPath)
	}
	if session.RunStatus != domain.SessionRunCompleted {
		t.Fatalf("运行结果未记录: %s", session.RunStatus)
	}
	if session.Completeness != domain.SessionCompletenessComplete {
		t.Fatalf("完整性未记录: %s", session.Completeness)
	}
	if session.ExportStatus != domain.SessionExportSucceeded {
		t.Fatalf("导出状态未记录: %s", session.ExportStatus)
	}
	if session.ProcessID == nil || *session.ProcessID != 4242 {
		t.Fatalf("进程 ID 未记录: %+v", session.ProcessID)
	}
	if session.Provider != "provider" {
		t.Fatalf("Provider 应从模型标识拆分: %q", session.Provider)
	}
	if session.Model != role.Model {
		t.Fatalf("模型不符: %q", session.Model)
	}
}

// TestRecordSessionExportFailure 验证导出失败仍然保留原始 Session 路径。
func TestRecordSessionExportFailure(t *testing.T) {
	t.Parallel()
	service, role, scope := newSessionService(t)
	startedAt := time.Now().UTC()
	result := agent.RunResult{
		SessionID:  "session-2",
		Status:     agent.RunCompleted,
		ExitCode:   0,
		StartedAt:  startedAt,
		FinishedAt: startedAt.Add(time.Second),
		Session: agent.SessionFiles{
			JSONLPath:    "/data/sessions/session.jsonl",
			Completeness: agent.SessionExportFailed,
			ExportStatus: agent.ExportFailed,
			Error:        "导出 HTML 失败",
		},
	}
	session, err := service.Record(testContext(t), app.RecordRequest{
		Scope:  scope,
		Role:   role,
		Result: result,
	})
	if err != nil {
		t.Fatalf("记录会话失败: %v", err)
	}
	if session.JSONLPath == "" {
		t.Fatal("导出失败时仍应保留原始 Session 路径")
	}
	if session.Completeness != domain.SessionCompletenessExportFailed {
		t.Fatalf("完整性不符: %s", session.Completeness)
	}
	if session.Status != domain.SessionStatusCompleted {
		t.Fatalf("导出失败不应改变执行状态: %s", session.Status)
	}
}

// TestRecordSessionTimeout 验证超时会记录为失败并保留退出码。
func TestRecordSessionTimeout(t *testing.T) {
	t.Parallel()
	service, role, scope := newSessionService(t)
	startedAt := time.Now().UTC()
	result := agent.RunResult{
		SessionID:  "session-3",
		Status:     agent.RunTimedOut,
		ExitCode:   -1,
		Failure:    "执行超时（30m0s）",
		StartedAt:  startedAt,
		FinishedAt: startedAt.Add(30 * time.Minute),
		Session: agent.SessionFiles{
			Completeness: agent.SessionMissing,
			ExportStatus: agent.ExportNotAttempted,
		},
	}
	session, err := service.Record(testContext(t), app.RecordRequest{
		Scope:  scope,
		Role:   role,
		Result: result,
	})
	if err != nil {
		t.Fatalf("记录会话失败: %v", err)
	}
	if session.Status != domain.SessionStatusFailed {
		t.Fatalf("超时应记录为失败状态: %s", session.Status)
	}
	if session.RunStatus != domain.SessionRunTimedOut {
		t.Fatalf("运行结果不符: %s", session.RunStatus)
	}
	if session.FailureSummary != result.Failure {
		t.Fatalf("错误摘要未记录: %q", session.FailureSummary)
	}
	if session.ExitCode == nil || *session.ExitCode != -1 {
		t.Fatalf("退出码未记录: %+v", session.ExitCode)
	}
}

// TestSessionRootFollowsDatabase 验证 Session 根目录位于数据目录下。
func TestSessionRootFollowsDatabase(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Storage: config.StorageConfig{DatabasePath: filepath.Join("data", "agentflow.db")}}
	root := app.SessionRoot(cfg)
	expected := filepath.Join("data", "sessions")
	if root != expected {
		t.Fatalf("期望 %s，实际 %s", expected, root)
	}
	if app.SessionRoot(nil) != "" {
		t.Fatal("配置为空时应返回空路径")
	}
}
