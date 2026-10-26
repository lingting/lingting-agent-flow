package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/server"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// codeSaveWiring 是装配验证需要的任务标识与记录数。
type codeSaveWiring struct {
	taskID  string
	records int
}

// seedCodeSaveWiring 用独立连接写入两条代码保存记录。
//
// 写入发生在服务启动之前，与真实部署一致：代码保存记录只由业务写入，HTTP 查询只读。
func seedCodeSaveWiring(t *testing.T, cfg *config.Config) codeSaveWiring {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store, err := sqlite.Open(ctx, sqlite.Options{Path: cfg.Storage.DatabasePath})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	}()
	now := time.Now().UTC()
	requirement, err := domain.NewRequirement("装配验证需求", "装配验证正文", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := store.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	record, err := domain.NewCodeSaveRecord(
		domain.NewID(),
		task.ID.String(),
		"container-1",
		"docker-container-1",
		"origin",
		"https://git.example.com/group/repo.git",
		"agent/"+task.ID.String(),
		"main",
		false,
		now,
	)
	if err != nil {
		t.Fatalf("构造代码保存记录失败: %v", err)
	}
	if err := record.MarkCommit(
		domain.CommitStatusSucceeded,
		"1111111111111111111111111111111111111111",
		"装配验证提交",
		now,
	); err != nil {
		t.Fatalf("记录提交结果失败: %v", err)
	}
	if err := record.MarkPush(domain.PushStatusUnknown, "", now); err != nil {
		t.Fatalf("记录推送结果失败: %v", err)
	}
	if err := store.CreateCodeSaveRecord(ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	second, err := domain.NewCodeSaveRecord(
		domain.NewID(),
		task.ID.String(),
		"container-1",
		"docker-container-1",
		"origin",
		"https://git.example.com/group/repo.git",
		"agent/"+task.ID.String(),
		"main",
		false,
		now.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("构造代码保存记录失败: %v", err)
	}
	if err := store.CreateCodeSaveRecord(ctx, second); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	return codeSaveWiring{taskID: task.ID.String(), records: 2}
}

// TestServerWiresCodeSaveQuery 校验统一服务装配后代码保存记录列表接口真实可用。
//
// 该用例走真实装配路径：server.New + Run 打开真实数据库与迁移，Server 内部按既有顺序
// 构造代码保存记录查询服务并注入 HTTP 服务。测试不注入任何代码保存相关替身，
// 也没有提供 SSH 能力——正是为了证明列表查询不需要远程依赖。
func TestServerWiresCodeSaveQuery(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.Roles = defaultRoles()
	wiring := seedCodeSaveWiring(t, cfg)
	instance := newServer(t, server.Options{
		Address:      "127.0.0.1:0",
		OpenStore:    realStoreOpener(t),
		SystemInfo:   func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		RequireWebUI: false,
	})
	cancel, done := runUntilHealthy(t, instance, cfg)
	defer stopServer(t, cancel, done)

	baseURL := "http://" + instance.Addr()
	target := baseURL + httpapi.PathTasks + "/" + wiring.taskID + "/code-saves"
	status, body := getJSON(t, target)
	if status != http.StatusOK {
		t.Fatalf("代码保存记录列表应返回 200，实际 %d，响应: %s", status, body)
	}
	var response httpapi.CodeSaveRecordListResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("解析列表响应失败: %v，响应: %s", err, body)
	}
	if response.Total != wiring.records || len(response.Records) != wiring.records {
		t.Fatalf("代码保存记录数量不符: %+v", response)
	}
	for _, item := range response.Records {
		if item.TaskID != wiring.taskID {
			t.Fatalf("记录归属不符: %s", item.TaskID)
		}
		if item.ID == "" {
			t.Fatal("记录标识不能为空")
		}
		if item.RecoveryStatus != string(domain.RecoveryStatusUnverified) {
			t.Fatalf("空恢复结论应归一化为 unverified，实际 %q", item.RecoveryStatus)
		}
	}

	// 不存在的任务在真实装配下返回 404，而不是空列表。
	missing := baseURL + httpapi.PathTasks + "/" + domain.NewID().String() + "/code-saves"
	status, body = getJSON(t, missing)
	if status != http.StatusNotFound {
		t.Fatalf("不存在的任务应返回 404，实际 %d，响应: %s", status, body)
	}
	var errorBody httpapi.ErrorResponse
	if err := json.Unmarshal([]byte(body), &errorBody); err != nil {
		t.Fatalf("解析错误响应失败: %v，响应: %s", err, body)
	}
	if errorBody.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误代码不符: %s", errorBody.Error.Code)
	}
}
