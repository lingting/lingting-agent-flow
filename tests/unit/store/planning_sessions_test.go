package sqlite_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 规划会话存储层用例。
//
// 每个用例都用真实 SQLite 数据库验证持久化事实：字段往返、NULL 语义、消息序号与
// 版本号分配、状态条件更新与两个绑定 CAS。断言尽量重新读取记录或直接用独立连接
// 读取原始列，不以方法返回值代替数据库状态。

// planningProjectID 是规划会话用例关联的项目标识。
//
// 会话表不声明外键，项目是否存在由应用层校验，因此存储层用例只使用书写合法的标识。
const planningProjectID = "demo-service"

// planningBaseTime 是存储层用例的时间基准。
func planningBaseTime() time.Time {
	return time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
}

// newPlanningSession 构造并写入一条 active 规划会话。
func newPlanningSession(t *testing.T, db *sqlite.DB) domain.PlanningSession {
	t.Helper()
	session, err := domain.NewPlanningSession(planningProjectID, planningBaseTime())
	if err != nil {
		t.Fatalf("构造规划会话失败: %v", err)
	}
	if err := db.CreatePlanningSession(testContext(t), session); err != nil {
		t.Fatalf("写入规划会话失败: %v", err)
	}
	return session
}

// planningHostID 返回一个用于绑定执行节点的标识。
func planningHostID() *domain.ID {
	hostID := domain.NewID()
	return &hostID
}

// testCommitSHA 返回一个完整的提交 SHA。
func testCommitSHA(char string) string {
	result := make([]byte, 0, 40)
	for index := 0; index < 40; index++ {
		result = append(result, char[0])
	}
	return string(result)
}

// mustLoadPlanningSession 重新读取规划会话，失败时终止用例。
func mustLoadPlanningSession(t *testing.T, db *sqlite.DB, sessionID domain.ID) domain.PlanningSession {
	t.Helper()
	loaded, err := db.GetPlanningSession(testContext(t), sessionID)
	if err != nil {
		t.Fatalf("读取规划会话失败: %v", err)
	}
	return *loaded
}

// updatePlanningSessionIfStatus 以当前状态为前置条件写回会话。
func updatePlanningSessionIfStatus(
	t *testing.T,
	db *sqlite.DB,
	session domain.PlanningSession,
	expected ...domain.PlanningSessionStatus,
) error {
	t.Helper()
	return db.UpdatePlanningSessionIfStatus(testContext(t), session, expected)
}

// rawPlanningSessionRow 是规划会话表中的原始列，用于验证 NULL 语义。
type rawPlanningSessionRow struct {
	hostID            sql.NullString
	containerID       sql.NullString
	sourceCommit      sql.NullString
	analystSessionID  sql.NullString
	plannerSessionID  sql.NullString
	adoptedRevisionID sql.NullString
	errorSummary      sql.NullString
	finishedAt        sql.NullInt64
	status            string
}

// readRawPlanningSessionRow 用独立连接读取规划会话的原始列。
func readRawPlanningSessionRow(
	t *testing.T,
	db *sqlite.DB,
	sessionID domain.ID,
) rawPlanningSessionRow {
	t.Helper()
	const statement = `
SELECT status, host_id, container_id, source_commit, analyst_session_id, planner_session_id,
       adopted_revision_id, error_summary, finished_at
FROM planning_sessions WHERE id = ?`
	var row rawPlanningSessionRow
	err := rawDB(t, db).QueryRowContext(
		testContext(t),
		statement,
		sessionID.String(),
	).Scan(
		&row.status,
		&row.hostID,
		&row.containerID,
		&row.sourceCommit,
		&row.analystSessionID,
		&row.plannerSessionID,
		&row.adoptedRevisionID,
		&row.errorSummary,
		&row.finishedAt,
	)
	if err != nil {
		t.Fatalf("读取规划会话原始列失败: %v", err)
	}
	return row
}

// TestPlanningSessionStoreRoundTrip 校验会话字段的完整往返与可空字段的 NULL 语义。
func TestPlanningSessionStoreRoundTrip(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	session, err := domain.NewPlanningSession(planningProjectID, planningBaseTime())
	if err != nil {
		t.Fatalf("构造规划会话失败: %v", err)
	}
	hostID := planningHostID()
	commit := testCommitSHA("a")
	bindAt := planningBaseTime().Add(time.Minute)
	if err := session.BindRuntime(hostID, "container-1", commit, bindAt); err != nil {
		t.Fatalf("绑定运行时失败: %v", err)
	}
	if err := session.BindRoleSessions("analyst-session-1", "planner-session-1", bindAt); err != nil {
		t.Fatalf("绑定角色会话失败: %v", err)
	}
	if err := db.CreatePlanningSession(testContext(t), session); err != nil {
		t.Fatalf("写入规划会话失败: %v", err)
	}

	loaded := mustLoadPlanningSession(t, db, session.ID)
	if loaded.ProjectID != planningProjectID {
		t.Fatalf("所属项目不符: %s", loaded.ProjectID)
	}
	if loaded.Status != domain.PlanningSessionActive {
		t.Fatalf("状态不符: %s", loaded.Status)
	}
	if loaded.HostID == nil || *loaded.HostID != *hostID {
		t.Fatalf("执行节点不符: %v", loaded.HostID)
	}
	if loaded.ContainerID != "container-1" {
		t.Fatalf("规划容器不符: %s", loaded.ContainerID)
	}
	if loaded.SourceCommit != commit {
		t.Fatalf("固定提交不符: %s", loaded.SourceCommit)
	}
	if loaded.AnalystSessionID != "analyst-session-1" || loaded.PlannerSessionID != "planner-session-1" {
		t.Fatalf("角色会话标识不符: %q / %q",
			loaded.AnalystSessionID, loaded.PlannerSessionID)
	}
	if loaded.AdoptedRevisionID != nil {
		t.Fatalf("未确认会话不应有采纳版本: %v", loaded.AdoptedRevisionID)
	}
	if loaded.ErrorSummary != "" || loaded.FinishedAt != nil {
		t.Fatalf("未失败会话不应有失败摘要与结束时间: %q / %v",
			loaded.ErrorSummary, loaded.FinishedAt)
	}
	if !loaded.CreatedAt.Equal(session.CreatedAt) || !loaded.UpdatedAt.Equal(bindAt) {
		t.Fatalf("时间字段不符: created=%v updated=%v", loaded.CreatedAt, loaded.UpdatedAt)
	}

	// 未绑定的会话必须以 NULL 落库，而不是以空串代替"没有"。
	empty := newPlanningSession(t, db)
	if empty.HostID != nil || empty.ContainerID != "" || empty.SourceCommit != "" {
		t.Fatalf("新建会话不应带有运行时绑定: %+v", empty)
	}
	row := readRawPlanningSessionRow(t, db, empty.ID)
	for name, column := range map[string]sql.NullString{
		"host_id":             row.hostID,
		"container_id":        row.containerID,
		"source_commit":       row.sourceCommit,
		"analyst_session_id":  row.analystSessionID,
		"planner_session_id":  row.plannerSessionID,
		"adopted_revision_id": row.adoptedRevisionID,
		"error_summary":       row.errorSummary,
	} {
		if column.Valid {
			t.Fatalf("未设置的可空列 %s 应以 NULL 保存，实际 %q", name, column.String)
		}
	}
	if row.finishedAt.Valid {
		t.Fatalf("未结束会话的 finished_at 应为 NULL，实际 %d", row.finishedAt.Int64)
	}

	// 失败摘要与结束时间同样要能完整往返。
	failed := mustLoadPlanningSession(t, db, session.ID)
	analyzeAt := bindAt.Add(time.Minute)
	if err := failed.BeginAnalysis(analyzeAt); err != nil {
		t.Fatalf("开始分析失败: %v", err)
	}
	if err := updatePlanningSessionIfStatus(
		t,
		db,
		failed,
		domain.PlanningSessionActive,
	); err != nil {
		t.Fatalf("写入 analyzing 状态失败: %v", err)
	}
	failAt := analyzeAt.Add(time.Minute)
	if err := failed.Fail("模型不可用", failAt); err != nil {
		t.Fatalf("记录分析失败失败: %v", err)
	}
	if err := updatePlanningSessionIfStatus(
		t,
		db,
		failed,
		domain.PlanningSessionAnalyzing,
	); err != nil {
		t.Fatalf("写入失败状态失败: %v", err)
	}
	failedRow := readRawPlanningSessionRow(t, db, session.ID)
	if failedRow.errorSummary.String != "模型不可用" {
		t.Fatalf("失败摘要未落库: %v", failedRow.errorSummary)
	}
	if !failedRow.finishedAt.Valid || failedRow.finishedAt.Int64 <= 0 {
		t.Fatalf("失败会话应记录结束时间，实际 %v", failedRow.finishedAt)
	}
	reloaded := mustLoadPlanningSession(t, db, session.ID)
	if reloaded.Status != domain.PlanningSessionFailed {
		t.Fatalf("失败状态未落库: %s", reloaded.Status)
	}
	if reloaded.FinishedAt == nil || !reloaded.FinishedAt.Equal(failAt) {
		t.Fatalf("结束时间不符: %v", reloaded.FinishedAt)
	}
}

// TestPlanningSessionStoreListByProject 校验按项目列出会话时的过滤与排序。
func TestPlanningSessionStoreListByProject(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	older, err := domain.NewPlanningSession(planningProjectID, planningBaseTime())
	if err != nil {
		t.Fatalf("构造规划会话失败: %v", err)
	}
	if err := db.CreatePlanningSession(testContext(t), older); err != nil {
		t.Fatalf("写入规划会话失败: %v", err)
	}
	newer, err := domain.NewPlanningSession(planningProjectID, planningBaseTime().Add(time.Hour))
	if err != nil {
		t.Fatalf("构造规划会话失败: %v", err)
	}
	if err := db.CreatePlanningSession(testContext(t), newer); err != nil {
		t.Fatalf("写入规划会话失败: %v", err)
	}
	other, err := domain.NewPlanningSession("other-project", planningBaseTime().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("构造规划会话失败: %v", err)
	}
	if err := db.CreatePlanningSession(testContext(t), other); err != nil {
		t.Fatalf("写入规划会话失败: %v", err)
	}

	sessions, err := db.ListPlanningSessionsByProject(testContext(t), planningProjectID)
	if err != nil {
		t.Fatalf("查询规划会话列表失败: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("应按项目过滤出 2 条会话，实际 %d", len(sessions))
	}
	if sessions[0].ID != newer.ID || sessions[1].ID != older.ID {
		t.Fatalf("会话应按创建时间倒序返回: %s / %s", sessions[0].ID, sessions[1].ID)
	}
}

// newAnalyzingSession 构造并写入一条 analyzing 规划会话。
func newAnalyzingSession(t *testing.T, db *sqlite.DB) domain.PlanningSession {
	t.Helper()
	session := newPlanningSession(t, db)
	loaded := mustLoadPlanningSession(t, db, session.ID)
	if err := loaded.BeginAnalysis(planningBaseTime().Add(time.Minute)); err != nil {
		t.Fatalf("开始分析失败: %v", err)
	}
	if err := db.UpdatePlanningSessionIfStatus(
		testContext(t),
		loaded,
		[]domain.PlanningSessionStatus{domain.PlanningSessionActive},
	); err != nil {
		t.Fatalf("写入 analyzing 状态失败: %v", err)
	}
	return loaded
}

// TestPlanningSessionStoreConditionalStatusUpdate 校验状态条件更新的成功与冲突语义。
func TestPlanningSessionStoreConditionalStatusUpdate(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	// 数据库中的状态与前置条件一致时更新成功。
	analyzing := newAnalyzingSession(t, db)
	loaded := mustLoadPlanningSession(t, db, analyzing.ID)
	if loaded.Status != domain.PlanningSessionAnalyzing {
		t.Fatalf("状态未写入: %s", loaded.Status)
	}

	// 前置条件过期时返回冲突，并且数据库保留最新状态。
	previewReady := mustLoadPlanningSession(t, db, analyzing.ID)
	previewAt := planningBaseTime().Add(2 * time.Minute)
	if err := previewReady.MarkPreviewReady(previewAt); err != nil {
		t.Fatalf("记录方案生成失败: %v", err)
	}
	if err := db.UpdatePlanningSessionIfStatus(
		testContext(t),
		previewReady,
		[]domain.PlanningSessionStatus{domain.PlanningSessionActive},
	); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("过期前置条件应返回 storage.ErrConflict，实际 %v", err)
	}
	conflicted := mustLoadPlanningSession(t, db, analyzing.ID)
	if conflicted.Status != domain.PlanningSessionAnalyzing {
		t.Fatalf("冲突失败后不得改写状态，实际 %s", conflicted.Status)
	}
	if !conflicted.UpdatedAt.Equal(analyzing.UpdatedAt) {
		t.Fatalf("冲突失败后不得改写更新时间，实际 %v", conflicted.UpdatedAt)
	}

	// 迟到的分析结果不能覆盖已确认、已放弃或已失败的会话结论。
	//
	// 迟到写入方持有的是"本轮仍在 analyzing"的会话快照，另一条业务路径已经把会话
	// 收敛为终态：条件更新必须以数据库中的状态为准拒绝这次写入。
	cases := []struct {
		name     string
		advance  func(t *testing.T, db *sqlite.DB, session domain.PlanningSession)
		status   domain.PlanningSessionStatus
		wantSafe func(domain.PlanningSession) error
	}{
		{
			name: "已确认",
			advance: func(t *testing.T, db *sqlite.DB, session domain.PlanningSession) {
				t.Helper()
				revision, err := db.AppendPlanningRevision(
					testContext(t),
					storage.AppendPlanningRevisionRequest{
						PlanningSessionID:       session.ID,
						RequirementAnalysisJSON: json.RawMessage(`{"summary":"需求"}`),
						ImplementationPlanJSON:  json.RawMessage(`{"summary":"方案"}`),
						At:                      planningBaseTime().Add(2 * time.Minute),
					},
				)
				if err != nil {
					t.Fatalf("追加规划版本失败: %v", err)
				}
				if err := session.MarkPreviewReady(planningBaseTime().Add(2 * time.Minute)); err != nil {
					t.Fatalf("记录方案生成失败: %v", err)
				}
				if err := db.UpdatePlanningSessionIfStatus(
					testContext(t),
					session,
					[]domain.PlanningSessionStatus{domain.PlanningSessionAnalyzing},
				); err != nil {
					t.Fatalf("写入 preview_ready 状态失败: %v", err)
				}
				if err := session.Confirm(revision.ID, planningBaseTime().Add(3*time.Minute)); err != nil {
					t.Fatalf("确认方案版本失败: %v", err)
				}
				if err := db.UpdatePlanningSessionIfStatus(
					testContext(t),
					session,
					[]domain.PlanningSessionStatus{domain.PlanningSessionPreviewReady},
				); err != nil {
					t.Fatalf("写入 confirmed 状态失败: %v", err)
				}
			},
			status: domain.PlanningSessionConfirmed,
			wantSafe: func(current domain.PlanningSession) error {
				if current.AdoptedRevisionID == nil {
					return errors.New("采纳版本被清空")
				}
				return nil
			},
		},
		{
			name: "已放弃",
			advance: func(t *testing.T, db *sqlite.DB, session domain.PlanningSession) {
				t.Helper()
				if err := session.MarkPreviewReady(planningBaseTime().Add(2 * time.Minute)); err != nil {
					t.Fatalf("记录方案生成失败: %v", err)
				}
				if err := db.UpdatePlanningSessionIfStatus(
					testContext(t),
					session,
					[]domain.PlanningSessionStatus{domain.PlanningSessionAnalyzing},
				); err != nil {
					t.Fatalf("写入 preview_ready 状态失败: %v", err)
				}
				if err := session.Abandon(planningBaseTime().Add(3 * time.Minute)); err != nil {
					t.Fatalf("放弃规划会话失败: %v", err)
				}
				if err := db.UpdatePlanningSessionIfStatus(
					testContext(t),
					session,
					[]domain.PlanningSessionStatus{domain.PlanningSessionPreviewReady},
				); err != nil {
					t.Fatalf("写入 abandoned 状态失败: %v", err)
				}
			},
			status: domain.PlanningSessionAbandoned,
		},
		{
			name: "已失败",
			advance: func(t *testing.T, db *sqlite.DB, session domain.PlanningSession) {
				t.Helper()
				if err := session.Fail("模型不可用", planningBaseTime().Add(2*time.Minute)); err != nil {
					t.Fatalf("记录分析失败失败: %v", err)
				}
				if err := db.UpdatePlanningSessionIfStatus(
					testContext(t),
					session,
					[]domain.PlanningSessionStatus{domain.PlanningSessionAnalyzing},
				); err != nil {
					t.Fatalf("写入 failed 状态失败: %v", err)
				}
			},
			status: domain.PlanningSessionFailed,
			wantSafe: func(current domain.PlanningSession) error {
				if current.ErrorSummary != "模型不可用" {
					return errors.New("失败摘要被改写")
				}
				return nil
			},
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			snapshot := newAnalyzingSession(t, db)
			testCase.advance(t, db, mustLoadPlanningSession(t, db, snapshot.ID))
			if err := snapshot.MarkPreviewReady(planningBaseTime().Add(4 * time.Minute)); err != nil {
				t.Fatalf("迟到结果的领域迁移应被允许构造: %v", err)
			}
			if err := db.UpdatePlanningSessionIfStatus(
				testContext(t),
				snapshot,
				[]domain.PlanningSessionStatus{domain.PlanningSessionAnalyzing},
			); !errors.Is(err, storage.ErrConflict) {
				t.Fatalf("迟到结果应返回 storage.ErrConflict，实际 %v", err)
			}
			current := mustLoadPlanningSession(t, db, snapshot.ID)
			if current.Status != testCase.status {
				t.Fatalf("冲突后状态应保持数据库中的值，实际 %s", current.Status)
			}
			if testCase.wantSafe != nil {
				if err := testCase.wantSafe(current); err != nil {
					t.Fatalf("冲突后业务结论被改写: %v", err)
				}
			}
		})
	}
}

// TestPlanningSessionMessageSequenceAllocation 校验消息序号分配、独立序号空间与失败回滚。
func TestPlanningSessionMessageSequenceAllocation(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	first := newPlanningSession(t, db)
	second := newPlanningSession(t, db)
	at := planningBaseTime().Add(time.Minute)

	for index := 1; index <= 3; index++ {
		message, err := db.AppendPlanningMessage(testContext(t), storage.AppendPlanningMessageRequest{
			PlanningSessionID: first.ID,
			SenderType:        domain.PlanningSenderUser,
			Content:           "第 " + messageSequenceText(index) + " 条",
			At:                at.Add(time.Duration(index) * time.Minute),
		})
		if err != nil {
			t.Fatalf("追加第 %d 条消息失败: %v", index, err)
		}
		if message.Sequence != index {
			t.Fatalf("消息序号应从 1 起递增，期望 %d，实际 %d", index, message.Sequence)
		}
	}
	// 不同会话使用独立的序号空间。
	other, err := db.AppendPlanningMessage(testContext(t), storage.AppendPlanningMessageRequest{
		PlanningSessionID: second.ID,
		SenderType:        domain.PlanningSenderUser,
		Content:           "另一个会话的第一条",
		At:                at,
	})
	if err != nil {
		t.Fatalf("追加另一会话消息失败: %v", err)
	}
	if other.Sequence != 1 {
		t.Fatalf("不同会话的序号空间应相互独立，实际 %d", other.Sequence)
	}

	// 无效消息不产生半成品记录：正文为空、发送方未知都必须在写入前被拒绝。
	if _, err := db.AppendPlanningMessage(testContext(t), storage.AppendPlanningMessageRequest{
		PlanningSessionID: first.ID,
		SenderType:        domain.PlanningSenderUser,
		Content:           "   ",
		At:                at,
	}); err == nil {
		t.Fatal("空正文消息应被拒绝")
	}
	if _, err := db.AppendPlanningMessage(testContext(t), storage.AppendPlanningMessageRequest{
		PlanningSessionID: first.ID,
		SenderType:        domain.PlanningSenderType("unknown"),
		Content:           "未知发送方",
		At:                at,
	}); err == nil {
		t.Fatal("未知发送方应被拒绝")
	}
	if _, err := db.AppendPlanningMessage(testContext(t), storage.AppendPlanningMessageRequest{
		PlanningSessionID: domain.NewID(),
		SenderType:        domain.PlanningSenderUser,
		Content:           "孤立会话",
		At:                at,
	}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("不存在的会话应返回 storage.ErrNotFound，实际 %v", err)
	}

	messages, err := db.ListPlanningMessages(testContext(t), first.ID)
	if err != nil {
		t.Fatalf("查询规划消息失败: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("无效消息不应留下记录，期望 3 条，实际 %d", len(messages))
	}
	for index, message := range messages {
		if message.Sequence != index+1 {
			t.Fatalf("消息序号应为 %d，实际 %d", index+1, message.Sequence)
		}
	}
}

// messageSequenceText 返回消息序号的中文描述，避免正文拼接成相同文本。
func messageSequenceText(index int) string {
	switch index {
	case 1:
		return "一"
	case 2:
		return "二"
	default:
		return "三"
	}
}

// TestPlanningSessionMessageConcurrentAppend 校验两个独立连接并发追加时序号不重复。
func TestPlanningSessionMessageConcurrentAppend(t *testing.T) {
	t.Parallel()
	path := testkit.New(t).SQLitePath()
	firstDB, err := sqlite.Open(testContext(t), sqlite.Options{Path: path, MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("打开第一个数据库连接失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := firstDB.Close(); closeErr != nil {
			t.Errorf("关闭第一个数据库连接失败: %v", closeErr)
		}
	})
	secondDB, err := sqlite.Open(testContext(t), sqlite.Options{Path: path, MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("打开第二个数据库连接失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := secondDB.Close(); closeErr != nil {
			t.Errorf("关闭第二个数据库连接失败: %v", closeErr)
		}
	})
	session := newPlanningSession(t, firstDB)

	const perWriter = 3
	stores := []*sqlite.DB{firstDB, secondDB}
	errs := make(chan error, len(stores)*perWriter)
	var group sync.WaitGroup
	for index, db := range stores {
		writer := index
		for round := 0; round < perWriter; round++ {
			group.Add(1)
			go func() {
				defer group.Done()
				_, appendErr := db.AppendPlanningMessage(
					testContext(t),
					storage.AppendPlanningMessageRequest{
						PlanningSessionID: session.ID,
						SenderType:        domain.PlanningSenderUser,
						Content:           "并发消息" + messageSequenceText(writer+1),
						At:                planningBaseTime().Add(time.Minute),
					},
				)
				if appendErr != nil {
					errs <- appendErr
				}
			}()
		}
	}
	group.Wait()
	close(errs)
	for appendErr := range errs {
		t.Fatalf("并发追加消息失败: %v", appendErr)
	}

	messages, err := firstDB.ListPlanningMessages(testContext(t), session.ID)
	if err != nil {
		t.Fatalf("查询规划消息失败: %v", err)
	}
	if len(messages) != len(stores)*perWriter {
		t.Fatalf("期望 %d 条消息，实际 %d", len(stores)*perWriter, len(messages))
	}
	for index, message := range messages {
		if message.Sequence != index+1 {
			t.Fatalf("并发追加后序号应连续，第 %d 条实际 %d", index+1, message.Sequence)
		}
	}
}

// TestPlanningSessionRevisionRoundTrip 校验版本号分配与 JSON 快照的原样保存。
func TestPlanningSessionRevisionRoundTrip(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	session := newPlanningSession(t, db)
	other := newPlanningSession(t, db)
	at := planningBaseTime().Add(time.Minute)

	// 快照文本带多余空白与交错字段顺序：存储层必须原样保存，不重排、不压缩。
	analysis := json.RawMessage("{\"b\": 2,\n  \"a\": 1}")
	plan := json.RawMessage(`{"steps":["第一步","第二步"]}`)
	first, err := db.AppendPlanningRevision(testContext(t), storage.AppendPlanningRevisionRequest{
		PlanningSessionID:       session.ID,
		RequirementAnalysisJSON: analysis,
		ImplementationPlanJSON:  plan,
		At:                      at,
	})
	if err != nil {
		t.Fatalf("追加规划版本失败: %v", err)
	}
	if first.RevisionNumber != 1 {
		t.Fatalf("版本号应从 1 开始，实际 %d", first.RevisionNumber)
	}
	second, err := db.AppendPlanningRevision(testContext(t), storage.AppendPlanningRevisionRequest{
		PlanningSessionID:       session.ID,
		RequirementAnalysisJSON: json.RawMessage(`{"b": 3}`),
		ImplementationPlanJSON:  json.RawMessage(`{"steps":[]}`),
		ValidationResultJSON:    json.RawMessage(`{"passed":true}`),
		At:                      at.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("追加第二轮规划版本失败: %v", err)
	}
	if second.RevisionNumber != 2 {
		t.Fatalf("同一会话的版本号应递增，实际 %d", second.RevisionNumber)
	}
	otherRevision, err := db.AppendPlanningRevision(
		testContext(t),
		storage.AppendPlanningRevisionRequest{
			PlanningSessionID:       other.ID,
			RequirementAnalysisJSON: json.RawMessage(`{"b": 1}`),
			ImplementationPlanJSON:  json.RawMessage(`{"steps":[]}`),
			At:                      at,
		},
	)
	if err != nil {
		t.Fatalf("追加另一会话版本失败: %v", err)
	}
	if otherRevision.RevisionNumber != 1 {
		t.Fatalf("不同会话的版本号空间应相互独立，实际 %d", otherRevision.RevisionNumber)
	}

	// 无效 JSON 不产生半成品记录。
	if _, err := db.AppendPlanningRevision(testContext(t), storage.AppendPlanningRevisionRequest{
		PlanningSessionID:       session.ID,
		RequirementAnalysisJSON: json.RawMessage(`{`),
		ImplementationPlanJSON:  json.RawMessage(`{"steps":[]}`),
		At:                      at,
	}); err == nil {
		t.Fatal("非法 JSON 快照应被拒绝")
	}
	if _, err := db.AppendPlanningRevision(testContext(t), storage.AppendPlanningRevisionRequest{
		PlanningSessionID:       session.ID,
		RequirementAnalysisJSON: json.RawMessage(`{"b": 4}`),
		ImplementationPlanJSON:  json.RawMessage(`null`),
		At:                      at,
	}); err == nil {
		t.Fatal("null 快照应被拒绝")
	}
	if _, err := db.AppendPlanningRevision(testContext(t), storage.AppendPlanningRevisionRequest{
		PlanningSessionID:       domain.NewID(),
		RequirementAnalysisJSON: json.RawMessage(`{"b": 4}`),
		ImplementationPlanJSON:  json.RawMessage(`{"steps":[]}`),
		At:                      at,
	}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("不存在的会话应返回 storage.ErrNotFound，实际 %v", err)
	}

	revisions, err := db.ListPlanningRevisions(testContext(t), session.ID)
	if err != nil {
		t.Fatalf("查询规划版本失败: %v", err)
	}
	if len(revisions) != 2 {
		t.Fatalf("无效版本不应留下记录，期望 2 条，实际 %d", len(revisions))
	}
	if string(revisions[0].RequirementAnalysisJSON) != string(analysis) {
		t.Fatalf("需求分析快照被存储层改写: %q", string(revisions[0].RequirementAnalysisJSON))
	}
	if string(revisions[0].ImplementationPlanJSON) != string(plan) {
		t.Fatalf("实施方案快照被存储层改写: %q", string(revisions[0].ImplementationPlanJSON))
	}
	if len(revisions[0].ValidationResultJSON) != 0 {
		t.Fatalf("未校验的版本不应有校验结果: %q", string(revisions[0].ValidationResultJSON))
	}
	if string(revisions[1].ValidationResultJSON) != `{"passed":true}` {
		t.Fatalf("校验结果快照不符: %q", string(revisions[1].ValidationResultJSON))
	}
}

// TestPlanningSessionRevisionConcurrentAppend 校验两个独立连接并发追加时版本号不重复。
func TestPlanningSessionRevisionConcurrentAppend(t *testing.T) {
	t.Parallel()
	path := testkit.New(t).SQLitePath()
	firstDB, err := sqlite.Open(testContext(t), sqlite.Options{Path: path, MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("打开第一个数据库连接失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := firstDB.Close(); closeErr != nil {
			t.Errorf("关闭第一个数据库连接失败: %v", closeErr)
		}
	})
	secondDB, err := sqlite.Open(testContext(t), sqlite.Options{Path: path, MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("打开第二个数据库连接失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := secondDB.Close(); closeErr != nil {
			t.Errorf("关闭第二个数据库连接失败: %v", closeErr)
		}
	})
	session := newPlanningSession(t, firstDB)

	const perWriter = 2
	stores := []*sqlite.DB{firstDB, secondDB}
	errs := make(chan error, len(stores)*perWriter)
	var group sync.WaitGroup
	for _, db := range stores {
		for round := 0; round < perWriter; round++ {
			group.Add(1)
			go func() {
				defer group.Done()
				_, appendErr := db.AppendPlanningRevision(
					testContext(t),
					storage.AppendPlanningRevisionRequest{
						PlanningSessionID:       session.ID,
						RequirementAnalysisJSON: json.RawMessage(`{"b": 1}`),
						ImplementationPlanJSON:  json.RawMessage(`{"steps":[]}`),
						At:                      planningBaseTime().Add(time.Minute),
					},
				)
				if appendErr != nil {
					errs <- appendErr
				}
			}()
		}
	}
	group.Wait()
	close(errs)
	for appendErr := range errs {
		t.Fatalf("并发追加规划版本失败: %v", appendErr)
	}

	revisions, err := firstDB.ListPlanningRevisions(testContext(t), session.ID)
	if err != nil {
		t.Fatalf("查询规划版本失败: %v", err)
	}
	if len(revisions) != len(stores)*perWriter {
		t.Fatalf("期望 %d 个版本，实际 %d", len(stores)*perWriter, len(revisions))
	}
	for index, revision := range revisions {
		if revision.RevisionNumber != index+1 {
			t.Fatalf("并发追加后版本号应连续，第 %d 个实际 %d", index+1, revision.RevisionNumber)
		}
	}
}

// TestPlanningSessionRuntimeBindingCAS 校验运行时绑定的比较并交换与列隔离。
func TestPlanningSessionRuntimeBindingCAS(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	session := newPlanningSession(t, db)
	at := planningBaseTime().Add(time.Minute)

	// 首次绑定：预期是 NULL 绑定，必须能正确匹配并写入。
	hostID := planningHostID()
	binding := storage.PlanningSessionRuntimeBinding{
		HostID:       hostID,
		ContainerID:  "container-1",
		SourceCommit: testCommitSHA("b"),
	}
	if err := db.UpdatePlanningSessionRuntimeBinding(
		testContext(t),
		session.ID,
		storage.PlanningSessionRuntimeBindingOf(session),
		binding,
		at,
	); err != nil {
		t.Fatalf("首次绑定运行时失败: %v", err)
	}
	bound := mustLoadPlanningSession(t, db, session.ID)
	if bound.ContainerID != "container-1" || bound.SourceCommit != binding.SourceCommit {
		t.Fatalf("运行时绑定未落库: %+v", bound)
	}
	if !bound.UpdatedAt.Equal(at) {
		t.Fatalf("绑定应刷新更新时间，实际 %v", bound.UpdatedAt)
	}

	// 旧 expected 不能覆盖新绑定。
	stale := storage.PlanningSessionRuntimeBindingOf(session)
	conflictNext := storage.PlanningSessionRuntimeBinding{
		HostID:       planningHostID(),
		ContainerID:  "container-stale",
		SourceCommit: testCommitSHA("c"),
	}
	if err := db.UpdatePlanningSessionRuntimeBinding(
		testContext(t),
		session.ID,
		stale,
		conflictNext,
		at.Add(time.Minute),
	); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("旧绑定应返回 storage.ErrConflict，实际 %v", err)
	}
	unchanged := mustLoadPlanningSession(t, db, session.ID)
	if unchanged.ContainerID != "container-1" ||
		unchanged.SourceCommit != binding.SourceCommit ||
		unchanged.HostID == nil || *unchanged.HostID != *hostID {
		t.Fatalf("冲突失败不得改写绑定: %+v", unchanged)
	}

	// 已确认的会话：运行时绑定写入只改运行时列，业务结论全部保留。
	revision, err := db.AppendPlanningRevision(testContext(t), storage.AppendPlanningRevisionRequest{
		PlanningSessionID:       session.ID,
		RequirementAnalysisJSON: json.RawMessage(`{"b": 1}`),
		ImplementationPlanJSON:  json.RawMessage(`{"steps":[]}`),
		At:                      at,
	})
	if err != nil {
		t.Fatalf("追加规划版本失败: %v", err)
	}
	confirmed := mustLoadPlanningSession(t, db, session.ID)
	if err := confirmed.BeginAnalysis(at.Add(time.Minute)); err != nil {
		t.Fatalf("开始分析失败: %v", err)
	}
	if err := updatePlanningSessionIfStatus(
		t,
		db,
		confirmed,
		domain.PlanningSessionActive,
	); err != nil {
		t.Fatalf("写入 analyzing 状态失败: %v", err)
	}
	if err := confirmed.MarkPreviewReady(at.Add(2 * time.Minute)); err != nil {
		t.Fatalf("记录方案生成失败: %v", err)
	}
	if err := confirmed.Confirm(revision.ID, at.Add(3*time.Minute)); err != nil {
		t.Fatalf("确认方案版本失败: %v", err)
	}
	if err := updatePlanningSessionIfStatus(
		t,
		db,
		confirmed,
		domain.PlanningSessionAnalyzing,
	); err != nil {
		t.Fatalf("写入 confirmed 状态失败: %v", err)
	}
	beforeRelease := mustLoadPlanningSession(t, db, session.ID)
	releaseAt := at.Add(4 * time.Minute)
	if err := db.UpdatePlanningSessionRuntimeBinding(
		testContext(t),
		session.ID,
		storage.PlanningSessionRuntimeBindingOf(beforeRelease),
		storage.PlanningSessionRuntimeBinding{},
		releaseAt,
	); err != nil {
		t.Fatalf("释放运行时绑定失败: %v", err)
	}
	afterRelease := mustLoadPlanningSession(t, db, session.ID)
	if !storage.PlanningSessionRuntimeBindingOf(afterRelease).Empty() {
		t.Fatalf("释放后运行时绑定应为空: %+v", afterRelease)
	}
	if afterRelease.Status != domain.PlanningSessionConfirmed {
		t.Fatalf("释放不得改变业务状态，实际 %s", afterRelease.Status)
	}
	if afterRelease.AdoptedRevisionID == nil || *afterRelease.AdoptedRevisionID != revision.ID {
		t.Fatalf("释放不得改变采纳版本，实际 %v", afterRelease.AdoptedRevisionID)
	}
	if afterRelease.FinishedAt == nil || !afterRelease.FinishedAt.Equal(
		beforeRelease.FinishedAt.UTC(),
	) {
		t.Fatalf("释放不得改变结束时间，实际 %v", afterRelease.FinishedAt)
	}
	if afterRelease.AnalystSessionID != beforeRelease.AnalystSessionID ||
		afterRelease.PlannerSessionID != beforeRelease.PlannerSessionID {
		t.Fatalf("释放不得改变角色会话标识: %q / %q",
			afterRelease.AnalystSessionID, afterRelease.PlannerSessionID)
	}
	if afterRelease.ErrorSummary != beforeRelease.ErrorSummary {
		t.Fatalf("释放不得改变失败摘要，实际 %q", afterRelease.ErrorSummary)
	}
	if !afterRelease.UpdatedAt.Equal(releaseAt) {
		t.Fatalf("释放应刷新更新时间，实际 %v", afterRelease.UpdatedAt)
	}

	// 会话不存在与绑定冲突必须能区分。
	if err := db.UpdatePlanningSessionRuntimeBinding(
		testContext(t),
		domain.NewID(),
		storage.PlanningSessionRuntimeBinding{},
		binding,
		releaseAt,
	); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("不存在的会话应返回 storage.ErrNotFound，实际 %v", err)
	}
}

// TestPlanningSessionRoleBindingCAS 校验角色会话绑定的成对写入、列隔离与并发互斥。
func TestPlanningSessionRoleBindingCAS(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	session := newPlanningSession(t, db)
	at := planningBaseTime().Add(time.Minute)

	first := storage.PlanningSessionRoleBinding{
		AnalystSessionID: "analyst-session-1",
		PlannerSessionID: "planner-session-1",
	}
	if err := db.UpdatePlanningSessionRoleBindings(
		testContext(t),
		session.ID,
		storage.PlanningSessionRoleBindingOf(session),
		first,
		at,
	); err != nil {
		t.Fatalf("首次绑定角色会话失败: %v", err)
	}
	bound := mustLoadPlanningSession(t, db, session.ID)
	if bound.AnalystSessionID != first.AnalystSessionID ||
		bound.PlannerSessionID != first.PlannerSessionID {
		t.Fatalf("角色会话绑定未成对落库: %q / %q",
			bound.AnalystSessionID, bound.PlannerSessionID)
	}

	// 旧 expected 不能覆盖已写入的标识。
	if err := db.UpdatePlanningSessionRoleBindings(
		testContext(t),
		session.ID,
		storage.PlanningSessionRoleBindingOf(session),
		storage.PlanningSessionRoleBinding{
			AnalystSessionID: "analyst-session-stale",
			PlannerSessionID: "planner-session-stale",
		},
		at.Add(time.Minute),
	); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("旧绑定应返回 storage.ErrConflict，实际 %v", err)
	}
	unchanged := mustLoadPlanningSession(t, db, session.ID)
	if unchanged.AnalystSessionID != first.AnalystSessionID ||
		unchanged.PlannerSessionID != first.PlannerSessionID {
		t.Fatalf("冲突失败不得改写角色会话标识: %q / %q",
			unchanged.AnalystSessionID, unchanged.PlannerSessionID)
	}

	// 两个角色不能得到相同标识：写入前拒绝，且不改动既有绑定。
	if err := db.UpdatePlanningSessionRoleBindings(
		testContext(t),
		session.ID,
		storage.PlanningSessionRoleBindingOf(unchanged),
		storage.PlanningSessionRoleBinding{
			AnalystSessionID: "same-session",
			PlannerSessionID: "same-session",
		},
		at.Add(2*time.Minute),
	); !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("相同角色标识应返回 storage.ErrInvalidData，实际 %v", err)
	}
	if err := db.UpdatePlanningSessionRoleBindings(
		testContext(t),
		session.ID,
		storage.PlanningSessionRoleBindingOf(unchanged),
		storage.PlanningSessionRoleBinding{AnalystSessionID: "only-analyst"},
		at.Add(2*time.Minute),
	); !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("单边绑定应返回 storage.ErrInvalidData，实际 %v", err)
	}

	// 角色绑定不改变业务状态与运行时绑定。
	runtime := storage.PlanningSessionRuntimeBinding{
		HostID:       planningHostID(),
		ContainerID:  "container-1",
		SourceCommit: testCommitSHA("d"),
	}
	withRuntime := mustLoadPlanningSession(t, db, session.ID)
	if err := db.UpdatePlanningSessionRuntimeBinding(
		testContext(t),
		session.ID,
		storage.PlanningSessionRuntimeBindingOf(withRuntime),
		runtime,
		at.Add(3*time.Minute),
	); err != nil {
		t.Fatalf("绑定运行时失败: %v", err)
	}
	revision, err := db.AppendPlanningRevision(testContext(t), storage.AppendPlanningRevisionRequest{
		PlanningSessionID:       session.ID,
		RequirementAnalysisJSON: json.RawMessage(`{"b": 1}`),
		ImplementationPlanJSON:  json.RawMessage(`{"steps":[]}`),
		At:                      at,
	})
	if err != nil {
		t.Fatalf("追加规划版本失败: %v", err)
	}
	current := mustLoadPlanningSession(t, db, session.ID)
	if err := current.BeginAnalysis(at.Add(4 * time.Minute)); err != nil {
		t.Fatalf("开始分析失败: %v", err)
	}
	if err := updatePlanningSessionIfStatus(
		t,
		db,
		current,
		domain.PlanningSessionActive,
	); err != nil {
		t.Fatalf("写入 analyzing 状态失败: %v", err)
	}
	if err := current.MarkPreviewReady(at.Add(5 * time.Minute)); err != nil {
		t.Fatalf("记录方案生成失败: %v", err)
	}
	if err := current.Confirm(revision.ID, at.Add(6*time.Minute)); err != nil {
		t.Fatalf("确认方案版本失败: %v", err)
	}
	if err := updatePlanningSessionIfStatus(
		t,
		db,
		current,
		domain.PlanningSessionAnalyzing,
	); err != nil {
		t.Fatalf("写入 confirmed 状态失败: %v", err)
	}
	beforeRebind := mustLoadPlanningSession(t, db, session.ID)
	second := storage.PlanningSessionRoleBinding{
		AnalystSessionID: "analyst-session-2",
		PlannerSessionID: "planner-session-2",
	}
	rebindAt := at.Add(7 * time.Minute)
	if err := db.UpdatePlanningSessionRoleBindings(
		testContext(t),
		session.ID,
		storage.PlanningSessionRoleBindingOf(beforeRebind),
		second,
		rebindAt,
	); err != nil {
		t.Fatalf("重新绑定角色会话失败: %v", err)
	}
	afterRebind := mustLoadPlanningSession(t, db, session.ID)
	if afterRebind.AnalystSessionID != second.AnalystSessionID ||
		afterRebind.PlannerSessionID != second.PlannerSessionID {
		t.Fatalf("角色会话标识未更新: %q / %q",
			afterRebind.AnalystSessionID, afterRebind.PlannerSessionID)
	}
	if afterRebind.Status != domain.PlanningSessionConfirmed {
		t.Fatalf("角色绑定不得改变业务状态，实际 %s", afterRebind.Status)
	}
	if afterRebind.AdoptedRevisionID == nil || *afterRebind.AdoptedRevisionID != revision.ID {
		t.Fatalf("角色绑定不得改变采纳版本，实际 %v", afterRebind.AdoptedRevisionID)
	}
	if afterRebind.ContainerID != runtime.ContainerID ||
		afterRebind.SourceCommit != runtime.SourceCommit ||
		afterRebind.HostID == nil || *afterRebind.HostID != *runtime.HostID {
		t.Fatalf("角色绑定不得改变运行时绑定: %+v", afterRebind)
	}
	if !afterRebind.UpdatedAt.Equal(rebindAt) {
		t.Fatalf("角色绑定应刷新更新时间，实际 %v", afterRebind.UpdatedAt)
	}

	// 并发条件更新：同一 expected 最多只有一个写入成功。
	path := testkit.New(t).SQLitePath()
	firstDB, err := sqlite.Open(testContext(t), sqlite.Options{Path: path, MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("打开第一个数据库连接失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := firstDB.Close(); closeErr != nil {
			t.Errorf("关闭第一个数据库连接失败: %v", closeErr)
		}
	})
	secondDB, err := sqlite.Open(testContext(t), sqlite.Options{Path: path, MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("打开第二个数据库连接失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := secondDB.Close(); closeErr != nil {
			t.Errorf("关闭第二个数据库连接失败: %v", closeErr)
		}
	})
	contended := newPlanningSession(t, firstDB)
	stores := []*sqlite.DB{firstDB, secondDB}
	results := make(chan error, len(stores))
	var group sync.WaitGroup
	for index, db := range stores {
		candidate := storage.PlanningSessionRoleBinding{
			AnalystSessionID: "analyst-contended-" + roleCandidateSuffix(index),
			PlannerSessionID: "planner-contended-" + roleCandidateSuffix(index),
		}
		store := db
		group.Add(1)
		go func() {
			defer group.Done()
			results <- store.UpdatePlanningSessionRoleBindings(
				testContext(t),
				contended.ID,
				storage.PlanningSessionRoleBinding{},
				candidate,
				at,
			)
		}()
	}
	group.Wait()
	close(results)
	succeeded := 0
	conflicted := 0
	for bindErr := range results {
		switch {
		case bindErr == nil:
			succeeded++
		case errors.Is(bindErr, storage.ErrConflict):
			conflicted++
		default:
			t.Fatalf("并发角色绑定出现意外错误: %v", bindErr)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("并发角色绑定应恰好一成功一冲突，实际成功 %d、冲突 %d", succeeded, conflicted)
	}
	final := mustLoadPlanningSession(t, firstDB, contended.ID)
	if final.AnalystSessionID == "" || final.PlannerSessionID == "" {
		t.Fatalf("竞争后应保留获胜者的绑定: %q / %q",
			final.AnalystSessionID, final.PlannerSessionID)
	}
	if final.AnalystSessionID == final.PlannerSessionID {
		t.Fatalf("两个角色不能得到相同标识: %q", final.AnalystSessionID)
	}
}

// roleCandidateSuffix 返回并发角色绑定用例使用的候选后缀。
func roleCandidateSuffix(index int) string {
	if index == 0 {
		return "a"
	}
	return "b"
}
