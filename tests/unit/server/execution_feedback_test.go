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
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// feedbackWiring 是装配验证需要的路径与标识。
type feedbackWiring struct {
	taskID string
	stepID string
	runID  string
}

// seedFeedbackWiring 用独立连接写入一条完整的验证失败反馈现场。
//
// 写入发生在服务启动之前，与真实部署一致：执行记录只由业务写入，HTTP 查询只读。
func seedFeedbackWiring(t *testing.T, cfg *config.Config) feedbackWiring {
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
	role, err := domain.NewRole(
		"装配验证开发角色",
		domain.RoleTypeDeveloper,
		"provider/model",
		"角色提示词",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := store.CreateRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
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
	step, err := domain.NewStep(
		task.ID,
		1,
		"装配验证步骤",
		"装配验证目标",
		"装配验证要求",
		role,
		domain.VerificationSpec{},
		now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := store.CreateStep(ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	attempt, err := store.StartAttempt(ctx, storage.StartAttemptRequest{
		TaskID: task.ID,
		StepID: step.ID,
		At:     now,
	})
	if err != nil {
		t.Fatalf("开始执行尝试失败: %v", err)
	}
	run, err := store.BeginValidationRun(ctx, storage.ValidationRunStartRequest{
		TaskID:             task.ID,
		StepID:             step.ID,
		AttemptID:          attempt.ID,
		ExpectedTaskStatus: task.Status,
		Spec: domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{
			{
				ID:       "build",
				Name:     "构建",
				Type:     domain.ValidationCheckTypeCommand,
				Enabled:  true,
				Required: true,
				Sequence: 1,
				Command:  "build",
			},
		}},
		SpecSnapshot: `{"checks":["build"]}`,
		ExecutionEnv: "container",
		Workspace:    "/workspace/project",
		ContainerID:  "container-1",
		At:           now,
	})
	if err != nil {
		t.Fatalf("创建验证记录失败: %v", err)
	}
	if err := store.FinishValidationRun(ctx, storage.ValidationRunFinishRequest{
		RunID:          run.ID,
		Status:         domain.ValidationRunStatusCompleted,
		Verdict:        domain.VerificationVerdictFailed,
		Summary:        "验证未通过",
		FailureSummary: "必需检查未通过: build",
		FailedChecks:   1,
		At:             now,
	}); err != nil {
		t.Fatalf("写入验证结论失败: %v", err)
	}
	if err := store.CreateValidationFailureFeedback(ctx, domain.ValidationFailureFeedback{
		TaskID:             task.ID,
		StepID:             step.ID,
		DeveloperAttemptID: attempt.ID,
		ValidationRunID:    run.ID,
		Verdict:            domain.VerificationVerdictFailed,
		Summary:            "装配验证失败反馈",
		Checks: []domain.ValidationCheckFeedback{
			{
				CheckID:       "build",
				CheckName:     "构建",
				Required:      true,
				Status:        domain.ValidationCheckStatusFailed,
				ExitCode:      1,
				FailureReason: "退出码 1 不在允许范围内",
				StderrSummary: "编译错误",
				StartedAt:     now,
				FinishedAt:    now,
			},
		},
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("写入验证失败反馈失败: %v", err)
	}
	return feedbackWiring{
		taskID: task.ID.String(),
		stepID: step.ID.String(),
		runID:  run.ID.String(),
	}
}

// TestServerWiresValidationFeedbackQuery 校验统一服务装配后反馈查询接口真实可用。
//
// 该用例走真实装配路径：server.New + Run 打开真实数据库与迁移，Server 内部按既有顺序
// 构造执行记录服务并注入 HTTP 服务。测试不注入任何执行记录相关替身。
func TestServerWiresValidationFeedbackQuery(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.Roles = defaultRoles()
	wiring := seedFeedbackWiring(t, cfg)
	instance := newServer(t, server.Options{
		Address:      "127.0.0.1:0",
		OpenStore:    realStoreOpener(t),
		SystemInfo:   func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		RequireWebUI: false,
	})
	cancel, done := runUntilHealthy(t, instance, cfg)
	defer stopServer(t, cancel, done)

	baseURL := "http://" + instance.Addr()
	target := baseURL + httpapi.PathTasks + "/" + wiring.taskID + "/steps/" + wiring.stepID +
		"/validations/" + wiring.runID + "/feedback"
	status, body := getJSON(t, target)
	if status != http.StatusOK {
		t.Fatalf("反馈查询应返回 200，实际 %d，响应: %s", status, body)
	}
	var feedback httpapi.ValidationFailureFeedbackResponse
	if err := json.Unmarshal([]byte(body), &feedback); err != nil {
		t.Fatalf("解析反馈响应失败: %v，响应: %s", err, body)
	}
	if feedback.TaskID != wiring.taskID || feedback.StepID != wiring.stepID {
		t.Fatalf("反馈归属不符: %+v", feedback)
	}
	if feedback.ValidationRunID != wiring.runID {
		t.Fatalf("反馈验证记录不符: %s", feedback.ValidationRunID)
	}
	if feedback.Summary != "装配验证失败反馈" {
		t.Fatalf("反馈摘要不符: %s", feedback.Summary)
	}
	if len(feedback.Checks) != 1 || feedback.Checks[0].ExitCode != 1 {
		t.Fatalf("反馈检查条目不符: %+v", feedback.Checks)
	}

	// 不存在的验证记录在真实装配下同样返回 404，而不是空反馈。
	missing := baseURL + httpapi.PathTasks + "/" + wiring.taskID + "/steps/" + wiring.stepID +
		"/validations/" + domain.NewID().String() + "/feedback"
	status, body = getJSON(t, missing)
	if status != http.StatusNotFound {
		t.Fatalf("不存在的验证记录应返回 404，实际 %d，响应: %s", status, body)
	}
	var errorBody httpapi.ErrorResponse
	if err := json.Unmarshal([]byte(body), &errorBody); err != nil {
		t.Fatalf("解析错误响应失败: %v，响应: %s", err, body)
	}
	if errorBody.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误代码不符: %s", errorBody.Error.Code)
	}
}
