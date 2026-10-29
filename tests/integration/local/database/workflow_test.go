package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// expectedSchemaVersion 是当前程序内置迁移的最新版本号。
//
// 该常量与 migrations 目录中的脚本数量保持一致，新增迁移时必须同步更新。
const expectedSchemaVersion = 25

// TestPersistentWorkflowLifecycle 覆盖一次完整的持久化链路：
// 迁移 → 角色初始化 → 需求与任务 → 计划 → 尝试 → 会话 → 事件 → 重启恢复。
func TestPersistentWorkflowLifecycle(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	path := filepath.Join(testkit.Dir(t), "nested", "agentflow.db")
	db, err := sqlite.Open(ctx, sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}

	cfg := &config.Config{
		Roles: []config.RoleConfig{
			{
				ID:           "planner",
				Name:         "架构规划师",
				Type:         domain.RoleTypePlanner,
				Runner:       config.RunnerPi,
				Model:        "provider/model",
				SystemPrompt: "规划提示词",
			},
			{
				ID:           "developer",
				Name:         "开发工程师",
				Type:         domain.RoleTypeDeveloper,
				Runner:       config.RunnerPi,
				Model:        "provider/model",
				SystemPrompt: "开发提示词",
			},
			{
				ID:           "validator",
				Name:         "验证工程师",
				Type:         domain.RoleTypeValidator,
				Runner:       config.RunnerPi,
				Model:        "provider/model",
				SystemPrompt: "验证提示词",
			},
		},
	}
	seeds, err := app.RoleSeeds(cfg)
	if err != nil {
		t.Fatalf("转换角色失败: %v", err)
	}
	created, err := db.SeedRoles(ctx, seeds)
	if err != nil {
		t.Fatalf("初始化角色失败: %v", err)
	}
	if created != len(seeds) {
		t.Fatalf("初始化角色数量不符: %d", created)
	}
	// 重复初始化不应覆盖已有角色。
	if again, err := db.SeedRoles(ctx, seeds); err != nil || again != 0 {
		t.Fatalf("重复初始化应当无操作: %d, %v", again, err)
	}

	requirement, err := domain.NewRequirement(
		"实现存储层",
		"保存角色、需求、任务、步骤、尝试、会话与事件",
		domain.PriorityHigh,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "/repo", "main", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	roleIDs := []domain.ID{"planner", "developer", "validator"}

	// 需求、任务与首批事件在同一事务内提交。
	err = db.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		if err := tx.CreateRequirement(ctx, requirement); err != nil {
			return err
		}
		if err := tx.CreateTask(ctx, task); err != nil {
			return err
		}
		for _, eventType := range []domain.EventType{
			domain.EventTypeRequirementSubmitted,
			domain.EventTypeTaskCreated,
		} {
			event, err := domain.NewEvent(
				task.ID, nil, nil, nil, eventType, nil, time.Now().UTC(),
			)
			if err != nil {
				return err
			}
			if _, err := tx.AppendEvent(ctx, event); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("写入需求与任务失败: %v", err)
	}

	roles := make(map[domain.ID]domain.Role, len(roleIDs))
	for _, id := range roleIDs {
		role, err := db.GetRole(ctx, id)
		if err != nil {
			t.Fatalf("读取角色失败: %v", err)
		}
		roles[id] = *role
	}

	// Planner 会话挂在任务上，没有步骤与尝试。
	plannerSession, err := domain.NewSession(
		domain.SessionScope{TaskID: task.ID},
		roles["planner"],
		"pi",
		"provider/model",
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造规划会话失败: %v", err)
	}
	if err := db.CreateSession(ctx, plannerSession); err != nil {
		t.Fatalf("写入规划会话失败: %v", err)
	}

	step, err := domain.NewStep(
		task.ID,
		1,
		"实现数据访问层",
		"提供可靠的持久化能力",
		"按实体划分仓储并通过事务保证一致性",
		roles["developer"],
		domain.VerificationSpec{
			AcceptanceCriteria: []string{"构建通过"},
			Commands: []domain.VerificationCommand{
				{
					Name:           "build",
					Command:        "go",
					Args:           []string{"build", "./..."},
					Required:       true,
					TimeoutSeconds: 120,
				},
			},
		},
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	attempt, err := domain.NewAttempt(task.ID, step.ID, 1, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造执行尝试失败: %v", err)
	}
	if err := db.CreateSteps(ctx, []domain.Step{step}); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	if err := db.CreateAttempt(ctx, attempt); err != nil {
		t.Fatalf("写入执行尝试失败: %v", err)
	}

	stepID := step.ID
	attemptID := attempt.ID
	developerSession, err := domain.NewSession(
		domain.SessionScope{TaskID: task.ID, StepID: &stepID, AttemptID: &attemptID},
		roles["developer"],
		"pi",
		"provider/model",
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造开发会话失败: %v", err)
	}
	validatorSession, err := domain.NewSession(
		domain.SessionScope{TaskID: task.ID, StepID: &stepID, AttemptID: &attemptID},
		roles["validator"],
		"pi",
		"provider/model",
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造验证会话失败: %v", err)
	}
	for _, session := range []domain.Session{developerSession, validatorSession} {
		if err := db.CreateSession(ctx, session); err != nil {
			t.Fatalf("写入会话失败: %v", err)
		}
	}

	// 验证失败后保留历史，并创建新的尝试与新的会话。
	if err := attempt.Fail("构建失败", time.Now().UTC()); err != nil {
		t.Fatalf("标记尝试失败失败: %v", err)
	}
	if err := db.UpdateAttempt(ctx, attempt); err != nil {
		t.Fatalf("更新执行尝试失败: %v", err)
	}

	next, err := db.NextAttemptNumber(ctx, step.ID)
	if err != nil {
		t.Fatalf("查询下一尝试序号失败: %v", err)
	}
	if next != 2 {
		t.Fatalf("下一尝试序号不符: %d", next)
	}
	second, err := domain.NewAttempt(task.ID, step.ID, next, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造执行尝试失败: %v", err)
	}
	if err := db.CreateAttempt(ctx, second); err != nil {
		t.Fatalf("写入执行尝试失败: %v", err)
	}
	secondID := second.ID
	retrySession, err := domain.NewSession(
		domain.SessionScope{TaskID: task.ID, StepID: &stepID, AttemptID: &secondID},
		roles["developer"],
		"pi",
		"provider/model",
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造重试会话失败: %v", err)
	}
	if err := db.CreateSession(ctx, retrySession); err != nil {
		t.Fatalf("写入重试会话失败: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	// 重新打开后所有已提交数据仍然存在，且历史记录完整。
	reopened, err := sqlite.Open(ctx, sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("重新打开数据库失败: %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("关闭数据库失败: %v", err)
		}
	}()

	version, err := reopened.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("读取 schema 版本失败: %v", err)
	}
	if version != expectedSchemaVersion {
		t.Fatalf("schema 版本不符: %d", version)
	}

	loadedRoles, err := reopened.ListRoles(ctx)
	if err != nil {
		t.Fatalf("读取角色失败: %v", err)
	}
	if len(loadedRoles) != len(roleIDs) {
		t.Fatalf("角色数量不符: %d", len(loadedRoles))
	}

	loadedRequirement, err := reopened.GetRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if loadedRequirement.Priority != domain.PriorityHigh {
		t.Fatalf("需求优先级未持久化: %s", loadedRequirement.Priority)
	}

	loadedTask, err := reopened.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loadedTask.RequirementID != requirement.ID {
		t.Fatalf("任务归属未持久化: %s", loadedTask.RequirementID)
	}

	steps, err := reopened.ListStepsByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("步骤数量不符: %d", len(steps))
	}
	if steps[0].RoleSnapshot.Name != "开发工程师" {
		t.Fatalf("角色快照未持久化: %+v", steps[0].RoleSnapshot)
	}
	if steps[0].RoleSnapshot.RoleID != roles["developer"].ID {
		t.Fatalf("角色快照标识不符: %s", steps[0].RoleSnapshot.RoleID)
	}

	attempts, err := reopened.ListAttemptsByStep(ctx, step.ID)
	if err != nil {
		t.Fatalf("读取执行尝试失败: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("尝试历史数量不符: %d", len(attempts))
	}
	if attempts[0].Status != domain.AttemptStatusFailed {
		t.Fatalf("第一次尝试状态未持久化: %s", attempts[0].Status)
	}

	firstSessions, err := reopened.ListSessionsByAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if len(firstSessions) != 2 {
		t.Fatalf("第一次尝试的会话数量不符: %d", len(firstSessions))
	}
	secondSessions, err := reopened.ListSessionsByAttempt(ctx, second.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if len(secondSessions) != 1 || secondSessions[0].ID != retrySession.ID {
		t.Fatalf("重试会话未持久化: %+v", secondSessions)
	}

	events, err := reopened.ListEventsByTask(ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("事件数量不符: %d", len(events))
	}

	// 角色删除改为逻辑删除：历史引用不会阻止删除，但历史记录必须完整保留。
	if err := reopened.DeleteRole(ctx, roles["developer"].ID); err != nil {
		t.Fatalf("逻辑删除角色失败: %v", err)
	}
	if _, err := reopened.GetRole(ctx, roles["developer"].ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("逻辑删除后角色不应出现在正常查询中，实际: %v", err)
	}
	reloadedStep, err := reopened.GetStep(ctx, step.ID)
	if err != nil {
		t.Fatalf("步骤历史不应因角色逻辑删除而丢失: %v", err)
	}
	if reloadedStep.RoleID != roles["developer"].ID {
		t.Fatalf("步骤应保留原角色标识: %s", reloadedStep.RoleID)
	}
}
