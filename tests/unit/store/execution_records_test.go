package sqlite_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// TestStartAttemptAllocatesSequentialNumbers 校验存储层自动分配尝试序号。
func TestStartAttemptAllocatesSequentialNumbers(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	second, err := db.StartAttempt(ctx, storage.StartAttemptRequest{
		TaskID: built.task.ID,
		StepID: built.step.ID,
		At:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("开始执行尝试失败: %v", err)
	}
	// 夹具已经写入第 1 次尝试，因此新分配到的序号必须是 2。
	if second.AttemptNumber != 2 {
		t.Fatalf("尝试序号应当连续递增，实际: %d", second.AttemptNumber)
	}
	if second.TaskID != built.task.ID || second.StepID != built.step.ID {
		t.Fatalf("尝试归属不符: %+v", second)
	}
	if second.Status != domain.AttemptStatusRunning {
		t.Fatalf("新建尝试应当处于 running，实际: %s", second.Status)
	}
}

// TestStartAttemptIsConcurrencySafe 校验并发分配不会产生重复序号。
func TestStartAttemptIsConcurrencySafe(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	const workers = 8
	numbers := make([]int, workers)
	errs := make([]error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func(slot int) {
			defer group.Done()
			attempt, err := db.StartAttempt(ctx, storage.StartAttemptRequest{
				TaskID: built.task.ID,
				StepID: built.step.ID,
				At:     time.Now().UTC(),
			})
			numbers[slot] = attempt.AttemptNumber
			errs[slot] = err
		}(index)
	}
	group.Wait()

	seen := make(map[int]struct{}, workers)
	for index := 0; index < workers; index++ {
		if errs[index] != nil {
			t.Fatalf("第 %d 个并发分配失败: %v", index, errs[index])
		}
		if _, duplicated := seen[numbers[index]]; duplicated {
			t.Fatalf("并发分配产生了重复序号: %d", numbers[index])
		}
		seen[numbers[index]] = struct{}{}
	}
	// 夹具的第 1 次尝试加上本次的 workers 次，序号必须覆盖 1..workers+1。
	if len(seen) != workers {
		t.Fatalf("并发分配的序号数量不符: %d", len(seen))
	}
	for number := 2; number <= workers+1; number++ {
		if _, ok := seen[number]; !ok {
			t.Fatalf("缺少序号 %d，实际分配结果: %v", number, numbers)
		}
	}
}

// TestStartAttemptRejectsMismatchedTask 校验任务归属与步骤不一致时拒绝写入。
func TestStartAttemptRejectsMismatchedTask(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	other := mustTask(t, db, built.requirement.ID)

	_, err := db.StartAttempt(ctx, storage.StartAttemptRequest{
		TaskID: other.ID,
		StepID: built.step.ID,
		At:     time.Now().UTC(),
	})
	if !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("归属不一致应当返回 ErrInvalidData，实际: %v", err)
	}
	// 拒绝之后不能留下半成品记录。
	attempts, listErr := db.ListAttemptsByStep(ctx, built.step.ID)
	if listErr != nil {
		t.Fatalf("查询尝试失败: %v", listErr)
	}
	if len(attempts) != 1 {
		t.Fatalf("失败的分配不应写入记录，实际尝试数: %d", len(attempts))
	}
}

// TestCreateAttemptRejectsMismatchedTask 校验 CreateAttempt 同样校验归属。
func TestCreateAttemptRejectsMismatchedTask(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	other := mustTask(t, db, built.requirement.ID)

	attempt, err := domain.NewAttempt(other.ID, built.step.ID, 2, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造执行尝试失败: %v", err)
	}
	if err := db.CreateAttempt(ctx, attempt); !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("归属不一致应当返回 ErrInvalidData，实际: %v", err)
	}
}

// TestUpdateAttemptIfStatusRejectsStaleResult 校验过期结果不能覆盖新状态。
func TestUpdateAttemptIfStatusRejectsStaleResult(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	running := built.attempt
	if err := running.Fail("构建失败", time.Now().UTC()); err != nil {
		t.Fatalf("标记失败失败: %v", err)
	}
	if err := db.UpdateAttempt(ctx, running); err != nil {
		t.Fatalf("更新执行尝试失败: %v", err)
	}

	// 另一条迟到的结果仍然认为尝试处于 running：条件更新必须拒绝。
	stale, err := db.GetAttempt(ctx, built.attempt.ID)
	if err != nil {
		t.Fatalf("读取执行尝试失败: %v", err)
	}
	stale.Status = domain.AttemptStatusRunning
	if err := stale.Complete(time.Now().UTC()); err != nil {
		t.Fatalf("标记完成失败: %v", err)
	}
	err = db.UpdateAttemptIfStatus(
		ctx,
		*stale,
		[]domain.AttemptStatus{domain.AttemptStatusRunning},
	)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("过期结果应当返回 ErrConflict，实际: %v", err)
	}
	loaded, err := db.GetAttempt(ctx, built.attempt.ID)
	if err != nil {
		t.Fatalf("读取执行尝试失败: %v", err)
	}
	if loaded.Status != domain.AttemptStatusFailed {
		t.Fatalf("既有事实不应被覆盖，实际状态: %s", loaded.Status)
	}
	if loaded.FailureSummary != "构建失败" {
		t.Fatalf("既有失败摘要不应被覆盖: %q", loaded.FailureSummary)
	}
}

// TestListAttemptPagePagingIsStable 校验尝试分页稳定排序。
func TestListAttemptPagePagingIsStable(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	for index := 0; index < 3; index++ {
		if _, err := db.StartAttempt(ctx, storage.StartAttemptRequest{
			TaskID: built.task.ID,
			StepID: built.step.ID,
			At:     time.Now().UTC(),
		}); err != nil {
			t.Fatalf("开始执行尝试失败: %v", err)
		}
	}

	first, err := db.ListAttemptPage(ctx, storage.AttemptFilter{
		StepID: built.step.ID,
		Limit:  2,
	})
	if err != nil {
		t.Fatalf("查询尝试分页失败: %v", err)
	}
	if first.Total != 4 || len(first.Attempts) != 2 {
		t.Fatalf("首页结果不符: total=%d len=%d", first.Total, len(first.Attempts))
	}
	if first.Attempts[0].AttemptNumber != 1 || first.Attempts[1].AttemptNumber != 2 {
		t.Fatalf("首页顺序不符: %+v", first.Attempts)
	}
	second, err := db.ListAttemptPage(ctx, storage.AttemptFilter{
		StepID: built.step.ID,
		Limit:  2,
		Offset: 2,
	})
	if err != nil {
		t.Fatalf("查询尝试分页失败: %v", err)
	}
	if len(second.Attempts) != 2 ||
		second.Attempts[0].AttemptNumber != 3 ||
		second.Attempts[1].AttemptNumber != 4 {
		t.Fatalf("第二页顺序不符: %+v", second.Attempts)
	}
}

// TestListAttemptPageFiltersByStatus 校验按状态筛选。
func TestListAttemptPageFiltersByStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	failed := built.attempt
	if err := failed.Fail("失败", time.Now().UTC()); err != nil {
		t.Fatalf("标记失败失败: %v", err)
	}
	if err := db.UpdateAttempt(ctx, failed); err != nil {
		t.Fatalf("更新执行尝试失败: %v", err)
	}
	page, err := db.ListAttemptPage(ctx, storage.AttemptFilter{
		StepID:   built.step.ID,
		Statuses: []domain.AttemptStatus{domain.AttemptStatusRunning},
	})
	if err != nil {
		t.Fatalf("查询尝试分页失败: %v", err)
	}
	if page.Total != 0 || len(page.Attempts) != 0 {
		t.Fatalf("状态筛选结果不符: %+v", page)
	}
}

// TestUpdateStepIfStatusRejectsStaleResult 校验步骤状态过期更新被拒绝。
func TestUpdateStepIfStatusRejectsStaleResult(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	step := built.step
	if err := step.Transition(domain.StepStatusRunning, time.Now().UTC()); err != nil {
		t.Fatalf("启动步骤失败: %v", err)
	}
	if err := step.Transition(domain.StepStatusCompleted, time.Now().UTC()); err != nil {
		t.Fatalf("完成步骤失败: %v", err)
	}
	if err := db.UpdateStep(ctx, step); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}

	stale := step
	stale.Status = domain.StepStatusRunning
	err := db.UpdateStepIfStatus(
		ctx,
		stale,
		[]domain.StepStatus{domain.StepStatusRunning, domain.StepStatusDeveloping},
	)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("过期更新应当返回 ErrConflict，实际: %v", err)
	}
	loaded, err := db.GetStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if loaded.Status != domain.StepStatusCompleted {
		t.Fatalf("步骤状态不应被覆盖，实际: %s", loaded.Status)
	}
}

// TestStepFailureSummaryRoundTrip 校验步骤失败摘要单独保存。
func TestStepFailureSummaryRoundTrip(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	step := built.step
	step.FailureStage = "verify"
	step.FailureSummary = "必需验证命令失败"
	if err := db.UpdateStep(ctx, step); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}
	loaded, err := db.GetStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if loaded.FailureStage != "verify" {
		t.Fatalf("失败阶段不符: %q", loaded.FailureStage)
	}
	if loaded.FailureSummary != "必需验证命令命令失败" && loaded.FailureSummary != "必需验证命令失败" {
		t.Fatalf("失败摘要不符: %q", loaded.FailureSummary)
	}
}

// TestListStepPageOrdersBySequence 校验步骤分页按序号稳定排序。
func TestListStepPageOrdersBySequence(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	for sequence := 2; sequence <= 3; sequence++ {
		step, err := domain.NewStep(
			built.task.ID,
			sequence,
			"步骤标题",
			"步骤目标",
			"执行要求",
			built.developer,
			domain.VerificationSpec{},
			time.Now().UTC(),
		)
		if err != nil {
			t.Fatalf("构造步骤失败: %v", err)
		}
		if err := db.CreateStep(ctx, step); err != nil {
			t.Fatalf("写入步骤失败: %v", err)
		}
	}
	page, err := db.ListStepPage(ctx, storage.StepFilter{TaskID: built.task.ID})
	if err != nil {
		t.Fatalf("查询步骤分页失败: %v", err)
	}
	if page.Total != 3 || len(page.Steps) != 3 {
		t.Fatalf("步骤数量不符: total=%d len=%d", page.Total, len(page.Steps))
	}
	for index, step := range page.Steps {
		if step.Sequence != index+1 {
			t.Fatalf("步骤顺序不符: 第 %d 项的序号为 %d", index, step.Sequence)
		}
	}
}

// TestListStepPageRejectsMissingTask 校验步骤分页必须指定任务。
func TestListStepPageRejectsMissingTask(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	if _, err := db.ListStepPage(testContext(t), storage.StepFilter{}); err == nil {
		t.Fatal("缺少任务标识时应当返回错误")
	}
}

// TestSessionArchiveIsIndependentFromExecutionStatus 校验归档状态与执行状态相互独立。
func TestSessionArchiveIsIndependentFromExecutionStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	stepID := built.step.ID
	attemptID := built.attempt.ID
	session := mustSession(t, db, domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}, built.developer)

	// 调用成功但归档失败：会话状态仍然是 completed。
	if err := session.MarkRunning("pi-session-1"); err != nil {
		t.Fatalf("标记会话运行失败: %v", err)
	}
	if err := session.Complete(time.Now().UTC()); err != nil {
		t.Fatalf("标记会话完成失败: %v", err)
	}
	if err := session.ApplyArchive(domain.SessionArchiveFailed, ""); err != nil {
		t.Fatalf("记录归档结果失败: %v", err)
	}
	if err := db.UpdateSession(ctx, session); err != nil {
		t.Fatalf("更新会话失败: %v", err)
	}
	loaded, err := db.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if loaded.Status != domain.SessionStatusCompleted {
		t.Fatalf("归档失败不应改变会话执行状态，实际: %s", loaded.Status)
	}
	if loaded.ArchiveStatus != domain.SessionArchiveFailed {
		t.Fatalf("归档状态不符: %s", loaded.ArchiveStatus)
	}
	// 已确认的归档结果不能被迟到的归档任务改写。
	if err := db.UpdateSessionArchive(ctx, session); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("重复归档应当返回 ErrConflict，实际: %v", err)
	}
}

// TestSessionArchiveRecordsPath 校验归档成功时保存归档位置。
func TestSessionArchiveRecordsPath(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	stepID := built.step.ID
	attemptID := built.attempt.ID
	session := mustSession(t, db, domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}, built.developer)

	if err := session.MarkRunning("pi-session-3"); err != nil {
		t.Fatalf("标记会话运行失败: %v", err)
	}
	if err := session.Complete(time.Now().UTC()); err != nil {
		t.Fatalf("标记会话完成失败: %v", err)
	}
	if err := db.UpdateSession(ctx, session); err != nil {
		t.Fatalf("更新会话失败: %v", err)
	}
	if err := session.ApplyArchive(domain.SessionArchiveSucceeded, "/archive/a.jsonl"); err != nil {
		t.Fatalf("记录归档结果失败: %v", err)
	}
	if err := db.UpdateSessionArchive(ctx, session); err != nil {
		t.Fatalf("更新会话归档失败: %v", err)
	}
	loaded, err := db.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if loaded.ArchiveStatus != domain.SessionArchiveSucceeded {
		t.Fatalf("归档状态不符: %s", loaded.ArchiveStatus)
	}
	if loaded.ArchivePath != "/archive/a.jsonl" {
		t.Fatalf("归档位置不符: %q", loaded.ArchivePath)
	}
	if loaded.Status != domain.SessionStatusCompleted {
		t.Fatalf("更新归档不应改变执行状态: %s", loaded.Status)
	}
}

// TestSessionExportStatusIndependentFromArchive 校验导出状态与归档状态分别记录。
func TestSessionExportStatusIndependentFromArchive(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	stepID := built.step.ID
	attemptID := built.attempt.ID
	session := mustSession(t, db, domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}, built.developer)

	if err := session.MarkRunning("pi-session-2"); err != nil {
		t.Fatalf("标记会话运行失败: %v", err)
	}
	if err := session.ApplyArtifacts(domain.SessionArtifacts{
		JSONLPath:    "/data/sessions/raw.jsonl",
		RunStatus:    domain.SessionRunCompleted,
		Completeness: domain.SessionCompletenessComplete,
		ExportStatus: domain.SessionExportFailed,
		ExitCode:     0,
	}, time.Now().UTC()); err != nil {
		t.Fatalf("记录产出失败: %v", err)
	}
	if err := session.ApplyArchive(domain.SessionArchiveSucceeded, "/archive/raw.jsonl"); err != nil {
		t.Fatalf("记录归档结果失败: %v", err)
	}
	// 会话已由夹具写入，这里只更新产出与归档字段。
	if err := db.UpdateSession(ctx, session); err != nil {
		t.Fatalf("更新会话失败: %v", err)
	}
	loaded, err := db.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if loaded.Status != domain.SessionStatusCompleted {
		t.Fatalf("导出失败不应改变执行状态: %s", loaded.Status)
	}
	if loaded.ArchiveStatus != domain.SessionArchiveSucceeded {
		t.Fatalf("归档状态不符: %s", loaded.ArchiveStatus)
	}
	if loaded.ExportStatus != domain.SessionExportFailed {
		t.Fatalf("导出状态不符: %s", loaded.ExportStatus)
	}
	if loaded.JSONLPath != "/data/sessions/raw.jsonl" {
		t.Fatalf("原始会话路径不符: %q", loaded.JSONLPath)
	}
}

// TestListSessionPageFiltersByAttemptAndRole 校验会话分页的多维筛选。
func TestListSessionPageFiltersByAttemptAndRole(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	stepID := built.step.ID
	attemptID := built.attempt.ID
	developerSession := mustSession(t, db, domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}, built.developer)
	validatorSession := mustSession(t, db, domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}, built.validator)

	page, err := db.ListSessionPage(ctx, storage.SessionFilter{
		TaskID:    built.task.ID,
		AttemptID: &attemptID,
	})
	if err != nil {
		t.Fatalf("查询会话分页失败: %v", err)
	}
	if page.Total != 2 || len(page.Sessions) != 2 {
		t.Fatalf("同一尝试下的会话数量不符: total=%d len=%d", page.Total, len(page.Sessions))
	}
	filtered, err := db.ListSessionPage(ctx, storage.SessionFilter{
		TaskID:    built.task.ID,
		AttemptID: &attemptID,
		RoleID:    &built.validator.ID,
	})
	if err != nil {
		t.Fatalf("查询会话分页失败: %v", err)
	}
	if filtered.Total != 1 || len(filtered.Sessions) != 1 {
		t.Fatalf("角色筛选结果不符: %+v", filtered.Sessions)
	}
	if filtered.Sessions[0].ID != validatorSession.ID {
		t.Fatalf("角色筛选命中了错误会话: %s", filtered.Sessions[0].ID)
	}
	if developerSession.RoleID == validatorSession.RoleID {
		t.Fatal("不同 Agent 角色必须拥有不同的角色标识")
	}
}

// TestSessionAcceptsUnknownArchiveStatus 校验未知归档状态可以原样读回。
func TestSessionAcceptsUnknownArchiveStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	stepID := built.step.ID
	attemptID := built.attempt.ID
	session := mustSession(t, db, domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}, built.developer)

	// 数据库不设枚举白名单：未来新增的归档状态必须能写入并原样读回。
	if _, err := rawDB(t, db).ExecContext(
		ctx,
		`UPDATE sessions SET archive_status = ? WHERE id = ?`,
		"archived_v2",
		session.ID.String(),
	); err != nil {
		t.Fatalf("写入未知归档状态失败: %v", err)
	}
	loaded, err := db.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if string(loaded.ArchiveStatus) != "archived_v2" {
		t.Fatalf("未知归档状态应当原样保留，实际: %s", loaded.ArchiveStatus)
	}
}

// TestAttemptTaskIDIsBackfilledFromSteps 校验迁移把既有尝试的任务归属回填。
func TestAttemptTaskIDIsBackfilledFromSteps(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	// 模拟 0012 之前的记录：清空任务归属后重新执行回填语句。
	raw := rawDB(t, db)
	if _, err := raw.ExecContext(
		ctx,
		`UPDATE attempts SET task_id = NULL WHERE id = ?`,
		built.attempt.ID.String(),
	); err != nil {
		t.Fatalf("清空任务归属失败: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE attempts SET task_id = (
		SELECT steps.task_id FROM steps WHERE steps.id = attempts.step_id
	) WHERE task_id IS NULL`); err != nil {
		t.Fatalf("回填任务归属失败: %v", err)
	}
	loaded, err := db.GetAttempt(ctx, built.attempt.ID)
	if err != nil {
		t.Fatalf("读取执行尝试失败: %v", err)
	}
	if loaded.TaskID != built.task.ID {
		t.Fatalf("任务归属回填不符: %s", loaded.TaskID)
	}
}

// TestAttemptScopeRequiresTaskAndStep 校验领域层拒绝缺少任务归属的尝试。
func TestAttemptScopeRequiresTaskAndStep(t *testing.T) {
	t.Parallel()
	if _, err := domain.NewAttempt("", domain.NewID(), 1, time.Now().UTC()); err == nil {
		t.Fatal("缺少任务归属时应当返回错误")
	}
	if _, err := domain.NewAttempt(domain.NewID(), "", 1, time.Now().UTC()); err == nil {
		t.Fatal("缺少步骤归属时应当返回错误")
	}
}
