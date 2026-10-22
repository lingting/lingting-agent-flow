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
	logger, err := testkit.Logger(logging.LevelError)
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

// TestStartRecordsRunningSession 验证启动会先落库再推进到 running。
func TestStartRecordsRunningSession(t *testing.T) {
	t.Parallel()
	service, role, scope := newSessionService(t)
	session, err := service.Start(testContext(t), app.StartRequest{
		Scope:        scope,
		Role:         role,
		InvocationID: "invocation-1",
	})
	if err != nil {
		t.Fatalf("启动会话失败: %v", err)
	}
	if session.Status != domain.SessionStatusRunning {
		t.Fatalf("期望状态 %s，实际 %s", domain.SessionStatusRunning, session.Status)
	}
	if session.ID.IsEmpty() {
		t.Fatal("会话标识不能为空")
	}
	if session.InvocationID != "invocation-1" {
		t.Fatalf("调用标识未记录: %q", session.InvocationID)
	}
	if session.Provider != "provider" {
		t.Fatalf("Provider 应从模型标识拆分: %q", session.Provider)
	}
	if session.Model != role.Model {
		t.Fatalf("模型不符: %q", session.Model)
	}
}

// TestFinishWritesArtifactsAndArchive 验证收尾会写入产出与归档结果。
func TestFinishWritesArtifactsAndArchive(t *testing.T) {
	t.Parallel()
	service, role, scope := newSessionService(t)
	started, err := service.Start(testContext(t), app.StartRequest{Scope: scope, Role: role})
	if err != nil {
		t.Fatalf("启动会话失败: %v", err)
	}
	startedAt := time.Now().UTC()
	result := agent.RunResult{
		SessionID:         started.ID.String(),
		ProcessID:         4242,
		Status:            agent.RunCompleted,
		ExitCode:          0,
		StartedAt:         startedAt,
		FinishedAt:        startedAt.Add(2 * time.Second),
		StdoutPath:        "/data/sessions/stdout.log",
		StderrPath:        "/data/sessions/stderr.log",
		FinalText:         "READY",
		Stdout:            "READY",
		ExternalSessionID: "",
		Session: agent.SessionFiles{
			JSONLPath:    "/data/sessions/session.jsonl",
			HTMLPath:     "/data/sessions/session.html",
			Completeness: agent.SessionComplete,
			ExportStatus: agent.ExportSucceeded,
		},
	}
	session, err := service.Finish(testContext(t), app.FinishRequest{
		SessionID: started.ID,
		Result:    result,
		Archive: app.ArchiveOutcome{
			Status: domain.SessionArchiveSucceeded,
			Path:   "/data/sessions/session.jsonl",
		},
	})
	if err != nil {
		t.Fatalf("结束会话失败: %v", err)
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
	// 归档状态与路径必须独立记录。
	if session.ArchiveStatus != domain.SessionArchiveSucceeded {
		t.Fatalf("归档状态未记录: %s", session.ArchiveStatus)
	}
	if session.ArchivePath != "/data/sessions/session.jsonl" {
		t.Fatalf("归档路径未记录: %q", session.ArchivePath)
	}
	// 两个输出流的位置必须分别记录。
	if session.StdoutPath != result.StdoutPath || session.StderrPath != result.StderrPath {
		t.Fatalf("输出日志路径未记录: %q / %q", session.StdoutPath, session.StderrPath)
	}
}

// TestFinishKeepsArchiveFailureIndependent 验证归档失败不改写成功的执行状态。
func TestFinishKeepsArchiveFailureIndependent(t *testing.T) {
	t.Parallel()
	service, role, scope := newSessionService(t)
	started, err := service.Start(testContext(t), app.StartRequest{Scope: scope, Role: role})
	if err != nil {
		t.Fatalf("启动会话失败: %v", err)
	}
	session, err := service.Finish(testContext(t), app.FinishRequest{
		SessionID: started.ID,
		Result: agent.RunResult{
			Status:     agent.RunCompleted,
			ExitCode:   0,
			StartedAt:  time.Now().UTC(),
			FinishedAt: time.Now().UTC(),
			Session: agent.SessionFiles{
				Completeness: agent.SessionMissing,
				ExportStatus: agent.ExportNotAttempted,
			},
		},
		Archive: app.ArchiveOutcome{
			Status: domain.SessionArchiveFailed,
			Error:  "归档目录不可写",
		},
	})
	if err != nil {
		t.Fatalf("结束会话失败: %v", err)
	}
	if session.Status != domain.SessionStatusCompleted {
		t.Fatalf("归档失败不应改变执行状态: %s", session.Status)
	}
	if session.ArchiveStatus != domain.SessionArchiveFailed {
		t.Fatalf("归档状态未记录: %s", session.ArchiveStatus)
	}
	if session.Completeness != domain.SessionCompletenessMissing {
		t.Fatalf("完整性不应被归档结果覆盖: %s", session.Completeness)
	}
}

// TestFinishRejectsTerminalSession 验证终态会话不会被重复改写。
func TestFinishRejectsTerminalSession(t *testing.T) {
	t.Parallel()
	service, role, scope := newSessionService(t)
	started, err := service.Start(testContext(t), app.StartRequest{Scope: scope, Role: role})
	if err != nil {
		t.Fatalf("启动会话失败: %v", err)
	}
	first := agent.RunResult{
		Status:     agent.RunCompleted,
		ExitCode:   0,
		StartedAt:  time.Now().UTC(),
		FinishedAt: time.Now().UTC(),
		Session: agent.SessionFiles{
			JSONLPath:    "/data/sessions/session.jsonl",
			Completeness: agent.SessionComplete,
			ExportStatus: agent.ExportNotAttempted,
		},
	}
	if _, err := service.Finish(testContext(t), app.FinishRequest{
		SessionID: started.ID,
		Result:    first,
	}); err != nil {
		t.Fatalf("首次结束会话失败: %v", err)
	}
	// 迟到的取消信号不得覆盖已完成的记录。
	_, err = service.Finish(testContext(t), app.FinishRequest{
		SessionID: started.ID,
		Result: agent.RunResult{
			Status:     agent.RunCancelled,
			ExitCode:   -1,
			StartedAt:  time.Now().UTC(),
			FinishedAt: time.Now().UTC(),
			Session: agent.SessionFiles{
				Completeness: agent.SessionMissing,
				ExportStatus: agent.ExportNotAttempted,
			},
		},
	})
	if err == nil {
		t.Fatal("终态会话不应允许重复改写")
	}
	loaded, loadErr := service.Load(testContext(t), started.ID)
	if loadErr != nil {
		t.Fatalf("读取会话失败: %v", loadErr)
	}
	if loaded.Status != domain.SessionStatusCompleted {
		t.Fatalf("迟到的结果覆盖了终态: %s", loaded.Status)
	}
}

// TestFailConvergesRunningSession 验证启动失败不会残留 running 状态。
func TestFailConvergesRunningSession(t *testing.T) {
	t.Parallel()
	service, role, scope := newSessionService(t)
	started, err := service.Start(testContext(t), app.StartRequest{Scope: scope, Role: role})
	if err != nil {
		t.Fatalf("启动会话失败: %v", err)
	}
	session, err := service.Fail(
		testContext(t),
		started.ID,
		time.Now().UTC(),
		"进程启动失败",
	)
	if err != nil {
		t.Fatalf("标记失败状态失败: %v", err)
	}
	if session.Status != domain.SessionStatusFailed {
		t.Fatalf("期望状态 %s，实际 %s", domain.SessionStatusFailed, session.Status)
	}
	if session.RunStatus != domain.SessionRunFailed {
		t.Fatalf("运行结果不符: %s", session.RunStatus)
	}
	if session.FailureSummary != "进程启动失败" {
		t.Fatalf("失败摘要未记录: %q", session.FailureSummary)
	}
	if session.ExitCode == nil || *session.ExitCode != -1 {
		t.Fatalf("退出码未记录为未知: %+v", session.ExitCode)
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
