package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
)

// TestWorkflowAPIStatusOmitsMissingStepTimestamps 校验从未开始的步骤不带时间字段。
//
// 计划刚持久化时步骤仍是 pending：响应必须省略 started_at / finished_at，
// 不能用工整的零值或任务级时间填充，否则界面会把"尚未开始"显示成"已开始执行"。
func TestWorkflowAPIStatusOmitsMissingStepTimestamps(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowAPIFixture(t, 1)
	status := fixture.workflowStatus(t)
	if len(status.Steps) != 1 {
		t.Fatalf("步骤快照应包含计划步骤，实际 %d", len(status.Steps))
	}
	step := status.Steps[0]
	if step.Status != string(domain.StepStatusPending) {
		t.Fatalf("步骤应为待执行，实际 %s", step.Status)
	}
	if step.StartedAt != "" {
		t.Fatalf("未开始的步骤不应返回开始时间: %q", step.StartedAt)
	}
	if step.FinishedAt != "" {
		t.Fatalf("未开始的步骤不应返回结束时间: %q", step.FinishedAt)
	}
}

// TestWorkflowAPIStatusReturnsPersistedStepTimestamps 校验步骤时间来自持久化字段。
//
// 断言依据是数据库里的真实记录：先把步骤按真实状态机推进到 running 并落库，
// 再确认响应中出现了同一时刻的 RFC3339 文本。
func TestWorkflowAPIStatusReturnsPersistedStepTimestamps(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowAPIFixture(t, 1)
	ctx := fixture.ctx
	step, err := fixture.db.GetStep(ctx, fixture.steps[0].ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	start := time.Now().UTC().Truncate(time.Millisecond)
	if err := step.Transition(domain.StepStatusRunning, start); err != nil {
		t.Fatalf("启动步骤失败: %v", err)
	}
	if err := fixture.db.UpdateStepIfStatus(
		ctx,
		*step,
		[]domain.StepStatus{domain.StepStatusPending},
	); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	stored, err := fixture.db.GetStep(ctx, fixture.steps[0].ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if stored.StartedAt == nil {
		t.Fatal("数据库中的步骤应已记录开始时间")
	}

	status := fixture.workflowStatus(t)
	if len(status.Steps) != 1 {
		t.Fatalf("步骤快照数量不符: %d", len(status.Steps))
	}
	served := status.Steps[0]
	if served.Status != string(domain.StepStatusRunning) {
		t.Fatalf("步骤状态应如实返回，实际 %s", served.Status)
	}
	if served.StartedAt == "" {
		t.Fatal("执行中的步骤必须返回开始时间")
	}
	parsed, err := time.Parse(time.RFC3339Nano, served.StartedAt)
	if err != nil {
		t.Fatalf("开始时间不是 RFC3339 文本: %q（%v）", served.StartedAt, err)
	}
	if !parsed.Equal(*stored.StartedAt) {
		t.Fatalf("开始时间与持久化值不一致: %s != %s", parsed, stored.StartedAt)
	}
	// 仍在执行：不能凭空给出结束时间。
	if served.FinishedAt != "" {
		t.Fatalf("执行中的步骤不应返回结束时间: %q", served.FinishedAt)
	}
}

// TestWorkflowAPIStatusReturnsFinishedAtOnTerminalSteps 校验结束状态的时间被返回。
func TestWorkflowAPIStatusReturnsFinishedAtOnTerminalSteps(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowAPIFixture(t, 1)
	ctx := fixture.ctx
	step, err := fixture.db.GetStep(ctx, fixture.steps[0].ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	start := time.Now().UTC().Truncate(time.Millisecond)
	finish := start.Add(45 * time.Second)
	if err := step.Transition(domain.StepStatusRunning, start); err != nil {
		t.Fatalf("启动步骤失败: %v", err)
	}
	if err := step.Transition(domain.StepStatusCompleted, finish); err != nil {
		t.Fatalf("完成步骤失败: %v", err)
	}
	if err := fixture.db.UpdateStepIfStatus(
		ctx,
		*step,
		[]domain.StepStatus{domain.StepStatusPending},
	); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}

	served := fixture.workflowStatus(t).Steps[0]
	if served.Status != string(domain.StepStatusCompleted) {
		t.Fatalf("步骤状态应为 completed，实际 %s", served.Status)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, served.StartedAt)
	if err != nil {
		t.Fatalf("开始时间无法解析: %q（%v）", served.StartedAt, err)
	}
	if !startedAt.Equal(start) {
		t.Fatalf("开始时间不符: %s != %s", startedAt, start)
	}
	finishedAt, err := time.Parse(time.RFC3339Nano, served.FinishedAt)
	if err != nil {
		t.Fatalf("结束时间无法解析: %q（%v）", served.FinishedAt, err)
	}
	if !finishedAt.Equal(finish) {
		t.Fatalf("结束时间不符: %s != %s", finishedAt, finish)
	}
}

// TestWorkflowAPIStatusStepTimesAreNotTaskTimes 校验步骤时间不会被任务级时间顶替。
//
// 任务与工作流执行记录都有自己的 started_at。响应中的步骤时间只能来自步骤本身：
// 把任务开始时间映射成步骤时间会让"哪一步真的跑过"失去可追溯性。
func TestWorkflowAPIStatusStepTimesAreNotTaskTimes(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowAPIFixture(t, 2)
	ctx := fixture.ctx
	// 只有第 2 个步骤真正执行过，第 1 个仍然待执行。
	second, err := fixture.db.GetStep(ctx, fixture.steps[1].ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	start := time.Now().UTC().Truncate(time.Millisecond)
	if err := second.Transition(domain.StepStatusRunning, start); err != nil {
		t.Fatalf("启动步骤失败: %v", err)
	}
	if err := fixture.db.UpdateStepIfStatus(
		ctx,
		*second,
		[]domain.StepStatus{domain.StepStatusPending},
	); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	// 任务级开始时间必须明显不同：它来自任务记录，不是任何一个步骤的时间。
	task, err := fixture.db.GetTask(ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}

	status := fixture.workflowStatus(t)
	if len(status.Steps) != 2 {
		t.Fatalf("步骤快照数量不符: %d", len(status.Steps))
	}
	if status.Steps[0].StartedAt != "" || status.Steps[0].FinishedAt != "" {
		t.Fatalf(
			"从未执行的步骤不应获得任何时间: %+v",
			status.Steps[0],
		)
	}
	if status.Steps[1].StartedAt == "" {
		t.Fatal("真实执行过的步骤必须有开始时间")
	}
	if task.StartedAt != nil {
		parsed, err := time.Parse(time.RFC3339Nano, status.Steps[1].StartedAt)
		if err != nil {
			t.Fatalf("开始时间无法解析: %q（%v）", status.Steps[1].StartedAt, err)
		}
		if parsed.Equal(*task.StartedAt) {
			t.Fatal("步骤时间不应取任务开始时间")
		}
	}
}

// TestWorkflowAPIStatusKeepsFailureConclusion 校验失败与待核验步骤不被序列化成成功。
func TestWorkflowAPIStatusKeepsFailureConclusion(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowAPIFixture(t, 2)
	ctx := fixture.ctx
	transitions := []struct {
		stepID domain.ID
		path   []domain.StepStatus
	}{
		{
			stepID: fixture.steps[0].ID,
			path:   []domain.StepStatus{domain.StepStatusRunning, domain.StepStatusFailed},
		},
		{
			stepID: fixture.steps[1].ID,
			path: []domain.StepStatus{
				domain.StepStatusRunning,
				domain.StepStatusRecoveryRequired,
			},
		},
	}
	for _, item := range transitions {
		step, err := fixture.db.GetStep(ctx, item.stepID)
		if err != nil {
			t.Fatalf("读取步骤失败: %v", err)
		}
		previous := step.Status
		for index, next := range item.path {
			if err := step.Transition(next, time.Now().UTC().Add(time.Duration(index)*time.Second)); err != nil {
				t.Fatalf("迁移到 %s 失败: %v", next, err)
			}
		}
		step.FailureSummary = "go vet 未通过"
		if err := fixture.db.UpdateStepIfStatus(ctx, *step, []domain.StepStatus{previous}); err != nil {
			t.Fatalf("写入步骤失败: %v", err)
		}
	}

	status := fixture.workflowStatus(t)
	if status.Steps[0].Status != string(domain.StepStatusFailed) {
		t.Fatalf("失败步骤的状态应如实返回，实际 %s", status.Steps[0].Status)
	}
	if status.Steps[0].Error == "" {
		t.Fatal("失败步骤必须保留失败摘要")
	}
	if status.Steps[1].Status != string(domain.StepStatusRecoveryRequired) {
		t.Fatalf("待核验步骤的状态应如实返回，实际 %s", status.Steps[1].Status)
	}
	for _, step := range status.Steps {
		if step.Status == string(domain.StepStatusCompleted) {
			t.Fatalf("失败或待核验的步骤不得被序列化成 completed: %+v", step)
		}
		if step.FinishedAt == "" {
			t.Fatalf("执行周期已结束的步骤必须有结束时间: %+v", step)
		}
		// 与步骤列表接口保持一致的时间来源：两者都来自同一条持久化记录。
		stored, err := fixture.db.GetStep(ctx, domain.ID(step.StepID))
		if err != nil {
			t.Fatalf("读取步骤失败: %v", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, step.StartedAt)
		if err != nil {
			t.Fatalf("开始时间无法解析: %q（%v）", step.StartedAt, err)
		}
		if stored.StartedAt == nil || !parsed.Equal(*stored.StartedAt) {
			t.Fatalf("工作流响应与数据库中的开始时间不一致: %s", step.StepID)
		}
	}
}

// TestStepsAPIReturnsSameStepTimestamps 校验步骤列表接口返回同一事实源的时间。
//
// 两个端点读取同一条步骤记录：任何一方的时间都必须能被另一方的数据库值复核，
// 不允许各自推算。
func TestStepsAPIReturnsSameStepTimestamps(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowAPIFixture(t, 1)
	ctx := fixture.ctx
	step, err := fixture.db.GetStep(ctx, fixture.steps[0].ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	start := time.Now().UTC().Truncate(time.Millisecond)
	finish := start.Add(30 * time.Second)
	if err := step.Transition(domain.StepStatusRunning, start); err != nil {
		t.Fatalf("启动步骤失败: %v", err)
	}
	if err := step.Transition(domain.StepStatusCompleted, finish); err != nil {
		t.Fatalf("完成步骤失败: %v", err)
	}
	if err := fixture.db.UpdateStepIfStatus(
		ctx,
		*step,
		[]domain.StepStatus{domain.StepStatusPending},
	); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}

	// 步骤列表接口由执行记录服务提供：这里单独装配一个处理器，避免改动共享
	// 工作流测试现场的依赖。
	records, err := app.NewExecutionRecordService(fixture.db)
	if err != nil {
		t.Fatalf("构造执行记录服务失败: %v", err)
	}
	tasks, err := app.NewTaskService(app.TaskServiceOptions{Store: fixture.db})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	listHandler := httpapi.NewHandler(httpapi.Deps{
		Tasks:            tasks,
		ExecutionRecords: records,
	})
	request := httptest.NewRequest(
		http.MethodGet,
		httpapi.PathTasks+"/"+fixture.task.ID.String()+"/steps",
		nil,
	)
	recorder := httptest.NewRecorder()
	listHandler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("查询步骤失败: %d %s", recorder.Code, recorder.Body.String())
	}
	var page httpapi.StepListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析步骤列表失败: %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("步骤数量不符: %d", len(page.Records))
	}
	listed := page.Records[0]
	if listed.StartedAt == nil || !listed.StartedAt.Equal(start) {
		t.Fatalf("步骤列表的开始时间与写入值不一致: %v", listed.StartedAt)
	}
	if listed.FinishedAt == nil || !listed.FinishedAt.Equal(finish) {
		t.Fatalf("步骤列表的结束时间与写入值不一致: %v", listed.FinishedAt)
	}

	served := fixture.workflowStatus(t).Steps[0]
	workflowStarted, err := time.Parse(time.RFC3339Nano, served.StartedAt)
	if err != nil {
		t.Fatalf("工作流响应的开始时间无法解析: %q（%v）", served.StartedAt, err)
	}
	workflowFinished, err := time.Parse(time.RFC3339Nano, served.FinishedAt)
	if err != nil {
		t.Fatalf("工作流响应的结束时间无法解析: %q（%v）", served.FinishedAt, err)
	}
	if !workflowStarted.Equal(*listed.StartedAt) {
		t.Fatalf("两个端点的开始时间不一致: %s != %s", workflowStarted, listed.StartedAt)
	}
	if !workflowFinished.Equal(*listed.FinishedAt) {
		t.Fatalf("两个端点的结束时间不一致: %s != %s", workflowFinished, listed.FinishedAt)
	}
}
