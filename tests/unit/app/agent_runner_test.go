// Package app_test 提供统一 Pi Runner 服务的生命周期单元测试。
//
// 这些用例只验证"服务如何登记、拒绝、取消与收敛"，不发起真实 Pi 调用：
// 真实进程行为由 tests/unit/agent/pi 的模拟进程用例覆盖。
package app_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// newRunnerService 构造统一 Runner 服务与配套的会话服务。
func newRunnerService(t *testing.T) (*app.AgentRunnerService, *app.SessionService, domain.Role, domain.SessionScope) {
	t.Helper()
	sessions, role, scope := newSessionService(t)
	service, err := app.NewAgentRunnerService(app.AgentRunnerOptions{
		Sessions: sessions,
		Logger:   slog.New(slog.NewTextHandler(nopWriter{}, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	if err != nil {
		t.Fatalf("构造统一 Runner 服务失败: %v", err)
	}
	return service, sessions, role, scope
}

// TestRunnerName 验证后台服务名称固定，便于在生命周期日志中定位。
func TestRunnerName(t *testing.T) {
	t.Parallel()
	service, _, _, _ := newRunnerService(t)
	if service.Name() != "agent_runner" {
		t.Fatalf("后台服务名称不符: %s", service.Name())
	}
}

// TestRunnerRejectsRunBeforeStart 验证未启动时拒绝调用。
func TestRunnerRejectsRunBeforeStart(t *testing.T) {
	t.Parallel()
	service, _, role, scope := newRunnerService(t)
	_, err := service.Run(testContext(t), app.AgentRunRequest{
		Scope:  scope,
		Role:   role,
		Prompt: "只回答 READY",
	})
	if err == nil {
		t.Fatal("未启动的服务不应接受调用")
	}
	if service.Active() != 0 {
		t.Fatalf("拒绝的调用不应登记为在途: %d", service.Active())
	}
}

// TestRunnerRejectsRunAfterStop 验证停止后拒绝新调用。
func TestRunnerRejectsRunAfterStop(t *testing.T) {
	t.Parallel()
	service, _, role, scope := newRunnerService(t)
	if err := service.Start(testContext(t)); err != nil {
		t.Fatalf("启动服务失败: %v", err)
	}
	if err := service.Stop(testContext(t)); err != nil {
		t.Fatalf("停止服务失败: %v", err)
	}
	_, err := service.Run(testContext(t), app.AgentRunRequest{
		Scope:  scope,
		Role:   role,
		Prompt: "只回答 READY",
	})
	if err == nil {
		t.Fatal("已停止的服务不应接受调用")
	}
}

// TestRunnerRejectsEmptyPrompt 验证提示词为空时不会创建会话记录。
func TestRunnerRejectsEmptyPrompt(t *testing.T) {
	t.Parallel()
	service, sessions, role, scope := newRunnerService(t)
	if err := service.Start(testContext(t)); err != nil {
		t.Fatalf("启动服务失败: %v", err)
	}
	t.Cleanup(func() {
		if err := service.Stop(context.Background()); err != nil {
			t.Errorf("停止服务失败: %v", err)
		}
	})
	if _, err := service.Run(testContext(t), app.AgentRunRequest{
		Scope:  scope,
		Role:   role,
		Prompt: "   ",
	}); err == nil {
		t.Fatal("空提示词应当被拒绝")
	}
	page, err := sessions.List(testContext(t), scope.TaskID, 0, 0)
	if err != nil {
		t.Fatalf("查询会话列表失败: %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("被拒绝的调用不应创建会话记录: %d", page.Total)
	}
}

// TestRunnerRejectsMissingModel 验证角色缺少模型时不会创建会话记录。
func TestRunnerRejectsMissingModel(t *testing.T) {
	t.Parallel()
	service, sessions, role, scope := newRunnerService(t)
	role.Model = ""
	if err := service.Start(testContext(t)); err != nil {
		t.Fatalf("启动服务失败: %v", err)
	}
	t.Cleanup(func() {
		if err := service.Stop(context.Background()); err != nil {
			t.Errorf("停止服务失败: %v", err)
		}
	})
	if _, err := service.Run(testContext(t), app.AgentRunRequest{
		Scope:  scope,
		Role:   role,
		Prompt: "只回答 READY",
	}); err == nil {
		t.Fatal("缺少模型时应当被拒绝")
	}
	page, err := sessions.List(testContext(t), scope.TaskID, 0, 0)
	if err != nil {
		t.Fatalf("查询会话列表失败: %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("被拒绝的调用不应创建会话记录: %d", page.Total)
	}
}

// TestRunnerConvergesSessionWhenExecutableMissing 验证可执行程序缺失时不留 running 状态。
//
// 这是"启动进程失败也必须收敛会话记录"的核心回归：既不能残留 running，
// 也不能把记录写成成功。
func TestRunnerConvergesSessionWhenExecutableMissing(t *testing.T) {
	t.Parallel()
	service, sessions, role, scope := newRunnerService(t)
	absent := filepath.Join(testkit.Dir(t), "absent-pi")
	if err := service.Start(testContext(t)); err != nil {
		t.Fatalf("启动服务失败: %v", err)
	}
	t.Cleanup(func() {
		if err := service.Stop(context.Background()); err != nil {
			t.Errorf("停止服务失败: %v", err)
		}
	})
	outcome, err := service.Run(testContext(t), app.AgentRunRequest{
		Scope:     scope,
		Role:      role,
		Prompt:    "只回答 READY",
		PiOptions: pi.Options{Executable: absent},
	})
	if err == nil {
		t.Fatal("可执行程序缺失时应返回错误")
	}
	if outcome.Result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, outcome.Result.Status)
	}
	if outcome.Session.ID.IsEmpty() {
		t.Fatal("失败调用也必须留下会话记录")
	}
	loaded, loadErr := sessions.Load(testContext(t), outcome.Session.ID)
	if loadErr != nil {
		t.Fatalf("读取会话失败: %v", loadErr)
	}
	if loaded.Status.IsTerminal() == false {
		t.Fatalf("启动失败后不应残留 %s 状态", loaded.Status)
	}
	if loaded.Status == domain.SessionStatusCompleted {
		t.Fatalf("启动失败不应记录为成功: %s", loaded.Status)
	}
	if loaded.RunStatus != domain.SessionRunFailed {
		t.Fatalf("运行结果不符: %s", loaded.RunStatus)
	}
	// 没有真实文件时必须记录归档失败，不能标记为已归档。
	if loaded.ArchiveStatus != domain.SessionArchiveFailed {
		t.Fatalf("没有可用文件时归档状态应为失败: %s", loaded.ArchiveStatus)
	}
	// 会话计数必须与真实执行次数一致。
	page, listErr := sessions.List(testContext(t), scope.TaskID, 0, 0)
	if listErr != nil {
		t.Fatalf("查询会话列表失败: %v", listErr)
	}
	if page.Total != 1 {
		t.Fatalf("失败调用应留下且只留下一条记录: %d", page.Total)
	}
}

// TestRunnerStopCancelsInFlight 验证停止服务会取消在途调用并等待其收敛。
func TestRunnerStopCancelsInFlight(t *testing.T) {
	t.Parallel()
	service, sessions, role, scope := newRunnerService(t)
	if err := service.Start(testContext(t)); err != nil {
		t.Fatalf("启动服务失败: %v", err)
	}
	executable := fakePiExecutable(t)
	done := make(chan error, 1)
	go func() {
		_, err := service.Run(context.Background(), app.AgentRunRequest{
			Scope:  scope,
			Role:   role,
			Prompt: "只回答 READY",
			// hang 场景只有在被终止时才退出，因此必须依赖停止信号真正传播到进程。
			PiOptions: pi.Options{
				Executable:   executable,
				Env:          fakePiEnv(t, "hang"),
				GraceTimeout: 200 * time.Millisecond,
			},
		})
		done <- err
	}()
	waitForActive(t, service, 1)
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := service.Stop(stopCtx); err != nil {
		t.Fatalf("停止服务失败: %v", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("被停止取消的调用应当返回错误")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("停止后调用未收敛")
	}
	// 会话记录必须已经收敛，不能停留在 running。
	page, loadErr := sessions.List(testContext(t), scope.TaskID, 0, 0)
	if loadErr != nil {
		t.Fatalf("查询会话列表失败: %v", loadErr)
	}
	if page.Total != 1 {
		t.Fatalf("期望一条会话记录，实际 %d", page.Total)
	}
	if !page.Sessions[0].Status.IsTerminal() {
		t.Fatalf("停止后会话仍停留在 %s", page.Sessions[0].Status)
	}
}

// waitForActive 在有上限的时间内等待在途调用数量达到期望值。
func waitForActive(t *testing.T, service *app.AgentRunnerService, want int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if service.Active() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待在途调用登记超时，当前 %d", service.Active())
}

// TestNewRunnerServiceRequiresSessions 验证会话服务是必需依赖。
func TestNewRunnerServiceRequiresSessions(t *testing.T) {
	t.Parallel()
	if _, err := app.NewAgentRunnerService(app.AgentRunnerOptions{}); err == nil {
		t.Fatal("缺少会话服务时应当返回错误")
	}
}

// TestSessionServiceRequiresLocalRoot 验证会话服务要求明确的本地持久化根目录。
func TestSessionServiceRequiresLocalRoot(t *testing.T) {
	t.Parallel()
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
	if _, err := app.NewSessionService(db, "", logger.Slog()); err == nil {
		t.Fatal("Session 根目录为空时应当返回错误")
	}
	if _, err := app.NewSessionService(nil, testkit.Dir(t), logger.Slog()); err == nil {
		t.Fatal("存储为空时应当返回错误")
	}
}
