// Package server_test 中的本文件验证服务端启动时的规划分析占用恢复。
//
// 用例跑的是真实的 Server.Run：取得进程锁 → 打开数据库 → 启动后台服务（含占用恢复）
// → 建立 HTTP 监听。断言因此能证明"恢复发生在 HTTP 开始接收请求之前"，而不是只证明
// 恢复函数本身可用。
package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/server"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// ephemeralListenAddress 是测试使用的监听地址：端口 0 由系统分配。
const ephemeralListenAddress = "127.0.0.1:0"

// interruptedPlanningSession 在数据库里留下"进程中断"的现场：analyzing + 分析占用。
func interruptedPlanningSession(
	t *testing.T,
	ctx context.Context,
	databasePath string,
) domain.PlanningSession {
	t.Helper()
	db, err := sqlite.Open(ctx, sqlite.Options{Path: databasePath})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	project := testkit.CreateProject(t, db, testkit.DefaultTestProjectID, time.Now().UTC())
	sessions, err := app.NewPlanningSessionService(app.PlanningSessionServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造规划会话服务失败: %v", err)
	}
	session, err := sessions.Create(ctx, app.CreatePlanningSessionRequest{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("创建规划会话失败: %v", err)
	}
	if _, err := sessions.SubmitMessage(ctx, session.ID.String(), app.SubmitPlanningMessageRequest{
		Content: "为规划会话补齐两阶段结构化分析",
	}); err != nil {
		t.Fatalf("提交规划消息失败: %v", err)
	}
	if err := db.ClaimPlanningAnalysis(
		ctx,
		session.ID,
		domain.NewID().String(),
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("写入分析占用失败: %v", err)
	}
	// 关闭数据库即模拟"上一次进程退出"：占用没有任何执行者。
	if err := db.Close(); err != nil {
		t.Fatalf("关闭测试数据库失败: %v", err)
	}
	closed = true
	return session
}

// startServerWithDatabase 启动一个只绑定测试数据库的服务实例。
func startServerWithDatabase(
	t *testing.T,
	ctx context.Context,
	databasePath string,
) (*server.Server, context.CancelFunc, <-chan error) {
	t.Helper()
	instance, err := server.New(server.Options{
		Address: ephemeralListenAddress,
		OpenStore: func(ctx context.Context, cfg *config.Config) (storage.Store, error) {
			return sqlite.Open(ctx, sqlite.Options{Path: cfg.Storage.DatabasePath})
		},
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{} },
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("构造服务实例失败: %v", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- instance.Run(runCtx, func(context.Context) (*config.Config, error) {
			return &config.Config{
				Storage: config.StorageConfig{DatabasePath: databasePath},
			}, nil
		})
	}()
	// 端口 0 由系统分配：等待 Addr 从配置值变成真实监听地址即为就绪。
	deadline := time.Now().Add(60 * time.Second)
	for instance.Addr() == ephemeralListenAddress && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if instance.Addr() == ephemeralListenAddress {
		cancel()
		select {
		case runErr := <-done:
			t.Fatalf("服务未能启动: %v", runErr)
		case <-time.After(30 * time.Second):
			t.Fatal("服务未能启动，且停止过程没有返回")
		}
	}
	return instance, cancel, done
}

// TestServerRunRecoversInterruptedPlanningAnalysis 验证启动恢复先于 HTTP 对外服务。
func TestServerRunRecoversInterruptedPlanningAnalysis(t *testing.T) {
	t.Parallel()
	workspace := testkit.Dir(t)
	databasePath := filepath.Join(workspace, "data", "agentflow.db")
	seedCtx, cancelSeed := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelSeed()
	session := interruptedPlanningSession(t, seedCtx, databasePath)

	instance, stop, done := startServerWithDatabase(t, seedCtx, databasePath)
	addr := instance.Addr()

	// HTTP 已经可用：此时读到的会话状态必须是恢复之后的结论。
	url := fmt.Sprintf("http://%s/api/v1/planning/sessions/%s", addr, session.ID)
	response, err := http.Get(url)
	if err != nil {
		stop()
		<-done
		t.Fatalf("请求规划会话失败: %v", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		stop()
		<-done
		t.Fatalf("读取响应失败: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		stop()
		<-done
		t.Fatalf("读取规划会话应返回 200，实际 %d，响应体 %s", response.StatusCode, body)
	}
	var payload struct {
		Session struct {
			Status       string `json:"status"`
			ErrorSummary string `json:"error_summary"`
		} `json:"session"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		stop()
		<-done
		t.Fatalf("解析响应失败: %v，响应体 %s", err, body)
	}
	if payload.Session.Status != string(domain.PlanningSessionFailed) {
		stop()
		<-done
		t.Fatalf("HTTP 对外可见的状态必须是恢复后的 failed，实际 %s", payload.Session.Status)
	}
	if !strings.Contains(payload.Session.ErrorSummary, "进程中断") {
		stop()
		<-done
		t.Fatalf("失败摘要没有记录进程中断: %q", payload.Session.ErrorSummary)
	}

	stop()
	if err := <-done; err != nil {
		t.Fatalf("服务停止失败: %v", err)
	}

	// 占用已被释放：再次启动不会把这条会话当作遗留执行。
	reopened, err := sqlite.Open(seedCtx, sqlite.Options{Path: databasePath})
	if err != nil {
		t.Fatalf("重新打开测试数据库失败: %v", err)
	}
	defer func() {
		if closeErr := reopened.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	}()
	claims, err := reopened.ListPlanningAnalysisClaims(seedCtx)
	if err != nil {
		t.Fatalf("读取规划分析占用失败: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("恢复后不应留下占用: %+v", claims)
	}
}
