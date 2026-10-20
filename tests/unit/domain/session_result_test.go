package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// mustSessionWithRole 构造一个用于产出元数据测试的会话。
func mustSessionWithRole(t *testing.T) domain.Session {
	t.Helper()
	role, err := domain.NewRole(
		"开发工程师",
		domain.RoleTypeDeveloper,
		"provider/model",
		"实现步骤",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	session, err := domain.NewSession(
		domain.SessionScope{TaskID: domain.NewID()},
		role,
		"provider",
		"provider/model",
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造会话失败: %v", err)
	}
	return session
}

// TestApplyArtifactsCompletesSession 验证正常产出会结束会话并记录文件路径。
func TestApplyArtifactsCompletesSession(t *testing.T) {
	t.Parallel()
	session := mustSessionWithRole(t)
	if err := session.MarkRunning("pi-session"); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	finishedAt := time.Now().UTC()
	artifacts := domain.SessionArtifacts{
		JSONLPath:    "/data/sessions/session.jsonl",
		HTMLPath:     "/data/sessions/session.html",
		RunStatus:    domain.SessionRunCompleted,
		Completeness: domain.SessionCompletenessComplete,
		ExportStatus: domain.SessionExportSucceeded,
		ExitCode:     0,
	}
	if err := session.ApplyArtifacts(artifacts, finishedAt); err != nil {
		t.Fatalf("记录产出元数据失败: %v", err)
	}
	if session.Status != domain.SessionStatusCompleted {
		t.Fatalf("期望状态 %s，实际 %s", domain.SessionStatusCompleted, session.Status)
	}
	if session.FinishedAt == nil {
		t.Fatal("结束时间应当被记录")
	}
	if session.JSONLPath != artifacts.JSONLPath || session.HTMLPath != artifacts.HTMLPath {
		t.Fatalf("文件路径未记录: %+v", session)
	}
	if session.ExitCode == nil || *session.ExitCode != 0 {
		t.Fatalf("退出码未记录: %+v", session.ExitCode)
	}
	if err := session.Validate(); err != nil {
		t.Fatalf("记录后应通过校验: %v", err)
	}
}

// TestApplyArtifactsMapsRunStatus 验证运行结果映射到会话状态。
func TestApplyArtifactsMapsRunStatus(t *testing.T) {
	t.Parallel()
	cases := map[domain.SessionRunStatus]domain.SessionStatus{
		domain.SessionRunCompleted: domain.SessionStatusCompleted,
		domain.SessionRunFailed:    domain.SessionStatusFailed,
		domain.SessionRunCancelled: domain.SessionStatusCancelled,
		domain.SessionRunTimedOut:  domain.SessionStatusFailed,
	}
	for runStatus, expected := range cases {
		session := mustSessionWithRole(t)
		if err := session.MarkRunning(""); err != nil {
			t.Fatalf("标记运行失败: %v", err)
		}
		artifacts := domain.SessionArtifacts{
			RunStatus:      runStatus,
			Completeness:   domain.SessionCompletenessMissing,
			ExportStatus:   domain.SessionExportNotAttempted,
			ExitCode:       -1,
			FailureSummary: "执行未正常完成",
		}
		if err := session.ApplyArtifacts(artifacts, time.Now().UTC()); err != nil {
			t.Fatalf("%s: 记录产出元数据失败: %v", runStatus, err)
		}
		if session.Status != expected {
			t.Fatalf("%s: 期望状态 %s，实际 %s", runStatus, expected, session.Status)
		}
	}
}

// TestApplyArtifactsRejectsIncompleteMetadata 验证产出元数据不自洽时被拒绝。
func TestApplyArtifactsRejectsIncompleteMetadata(t *testing.T) {
	t.Parallel()
	cases := map[string]domain.SessionArtifacts{
		"保留 Session 但缺少路径": {
			RunStatus:    domain.SessionRunCompleted,
			Completeness: domain.SessionCompletenessComplete,
			ExportStatus: domain.SessionExportNotAttempted,
		},
		"导出成功但缺少路径": {
			JSONLPath:    "/data/session.jsonl",
			RunStatus:    domain.SessionRunCompleted,
			Completeness: domain.SessionCompletenessComplete,
			ExportStatus: domain.SessionExportSucceeded,
		},
		"未知运行结果": {
			RunStatus:    domain.SessionRunStatus("unknown"),
			Completeness: domain.SessionCompletenessMissing,
			ExportStatus: domain.SessionExportNotAttempted,
		},
		"未知完整性": {
			RunStatus:    domain.SessionRunCompleted,
			Completeness: domain.SessionCompleteness("unknown"),
			ExportStatus: domain.SessionExportNotAttempted,
		},
		"未知导出状态": {
			RunStatus:    domain.SessionRunCompleted,
			Completeness: domain.SessionCompletenessMissing,
			ExportStatus: domain.SessionExportStatus("unknown"),
		},
	}
	for name, artifacts := range cases {
		session := mustSessionWithRole(t)
		if err := session.ApplyArtifacts(artifacts, time.Now().UTC()); err == nil {
			t.Fatalf("%s: 期望校验失败", name)
		} else if !errors.Is(err, domain.ErrInvalidValue) {
			t.Fatalf("%s: 期望字段校验错误，实际 %v", name, err)
		}
		if session.Status != domain.SessionStatusStarting {
			t.Fatalf("%s: 校验失败时不应改变状态: %s", name, session.Status)
		}
	}
}

// TestSessionCompletenessHasJSONL 验证只有保留文件的完整性状态才要求路径。
func TestSessionCompletenessHasJSONL(t *testing.T) {
	t.Parallel()
	cases := map[domain.SessionCompleteness]bool{
		domain.SessionCompletenessComplete:     true,
		domain.SessionCompletenessPartial:      true,
		domain.SessionCompletenessExportFailed: true,
		domain.SessionCompletenessMissing:      false,
	}
	for completeness, expected := range cases {
		if got := completeness.HasJSONL(); got != expected {
			t.Fatalf("%s: 期望 %v，实际 %v", completeness, expected, got)
		}
	}
}

// TestApplyArtifactsRecordsOutputPaths 验证两个输出流的位置被分别记录。
//
// stdout 与 stderr 必须保留各自的路径：合成一个字段会让"哪段输出来自哪条流"
// 永久丢失，出错时无法区分真实回答与诊断信息。
func TestApplyArtifactsRecordsOutputPaths(t *testing.T) {
	t.Parallel()
	session := mustSessionWithRole(t)
	if err := session.MarkRunning(""); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	artifacts := domain.SessionArtifacts{
		JSONLPath:    "/data/sessions/session.jsonl",
		RunStatus:    domain.SessionRunCompleted,
		Completeness: domain.SessionCompletenessComplete,
		ExportStatus: domain.SessionExportNotAttempted,
		ExitCode:     0,
		StdoutPath:   "/data/sessions/stdout.log",
		StderrPath:   "/data/sessions/stderr.log",
	}
	if err := session.ApplyArtifacts(artifacts, time.Now().UTC()); err != nil {
		t.Fatalf("记录产出元数据失败: %v", err)
	}
	if session.StdoutPath != artifacts.StdoutPath {
		t.Fatalf("标准输出路径未记录: %q", session.StdoutPath)
	}
	if session.StderrPath != artifacts.StderrPath {
		t.Fatalf("标准错误路径未记录: %q", session.StderrPath)
	}
	// 两个路径不能指向同一个文件。
	if session.StdoutPath == session.StderrPath {
		t.Fatalf("两个输出流不应共用路径: %s", session.StdoutPath)
	}
}
