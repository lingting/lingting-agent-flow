package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// taskManagementFixture 是一套任务管理测试数据。
type taskManagementFixture struct {
	db        *sqlite.DB
	service   *app.TaskService
	host      domain.ExecutionHost
	planner   domain.Role
	developer domain.Role
}

// newTaskManagementFixture 在测试工作空间中准备任务管理所需的最小数据。
func newTaskManagementFixture(t *testing.T) *taskManagementFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	service, err := app.NewTaskService(app.TaskServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	now := time.Now().UTC()
	fixture := &taskManagementFixture{db: db, service: service}
	fixture.planner = createManagementRole(t, db, ctx, domain.RoleTypePlanner, "规划师", now)
	fixture.developer = createManagementRole(t, db, ctx, domain.RoleTypeDeveloper, "开发工程师", now)
	fixture.host = createManagementHost(t, db, ctx, now)
	return fixture
}

// createManagementRole 写入一个测试角色。
func createManagementRole(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	roleType domain.RoleType,
	name string,
	now time.Time,
) domain.Role {
	t.Helper()
	role, err := domain.NewRole(
		name,
		roleType,
		"provider/model",
		"提示词",
		domain.DefaultToolsPolicy(roleType),
		now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := db.CreateRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	return role
}

// createManagementHost 写入一个测试执行节点。
func createManagementHost(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	now time.Time,
) domain.ExecutionHost {
	t.Helper()
	host, err := domain.NewExecutionHost("", "测试节点", "10.0.0.1", 22, "deploy", "/srv/agentflow", now)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	return host
}

// request 构造一个字段完整的建任务请求。
func (f *taskManagementFixture) request(name, description string) app.CreateTaskFromAPIRequest {
	return app.CreateTaskFromAPIRequest{
		Name:            name,
		Description:     description,
		HostID:          f.host.ID.String(),
		RepositoryURL:   "ssh://git.example.com/team/repo",
		BaseBranch:      "main",
		Environment:     "go",
		PlannerRoleID:   f.planner.ID.String(),
		DeveloperRoleID: f.developer.ID.String(),
	}
}

// create 创建任务并返回结果。
func (f *taskManagementFixture) create(
	t *testing.T,
	ctx context.Context,
	request app.CreateTaskFromAPIRequest,
) domain.Task {
	t.Helper()
	task, err := f.service.CreateTaskFromAPI(ctx, request)
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	return task
}

// finish 把任务推进到指定结束状态并写回数据库。
func (f *taskManagementFixture) finish(
	t *testing.T,
	ctx context.Context,
	task domain.Task,
	path ...domain.TaskStatus,
) domain.Task {
	t.Helper()
	for _, status := range path {
		if err := task.Transition(status, time.Now().UTC()); err != nil {
			t.Fatalf("状态迁移到 %s 失败: %v", status, err)
		}
	}
	if err := f.db.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	return task
}

// TestCreateTaskFromAPIStartsQueued 校验接口创建的任务停在等待执行状态。
func TestCreateTaskFromAPIStartsQueued(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	task := fixture.create(t, ctx, fixture.request("实现登录接口", "为登录页接入真实接口"))
	if task.Status != domain.TaskStatusQueued {
		t.Fatalf("新建任务状态应为 queued，实际 %s", task.Status)
	}
	if task.StartedAt != nil || task.FinishedAt != nil {
		t.Fatal("新建任务不应有开始或结束时间")
	}
	if task.FinalResult != domain.TaskResultNone {
		t.Fatalf("新建任务不应有结论: %q", task.FinalResult)
	}

	requirement, err := fixture.service.Requirement(ctx, task.RequirementID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if requirement.Title != "实现登录接口" || requirement.Description != "为登录页接入真实接口" {
		t.Fatalf("名称与描述未正确写入需求: %+v", requirement)
	}
}

// TestCreateTaskFromAPIValidatesInput 校验创建接口的输入校验。
func TestCreateTaskFromAPIValidatesInput(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	cases := map[string]func(*app.CreateTaskFromAPIRequest){
		"名称为空":   func(r *app.CreateTaskFromAPIRequest) { r.Name = "   " },
		"描述为空":   func(r *app.CreateTaskFromAPIRequest) { r.Description = " " },
		"名称过长":   func(r *app.CreateTaskFromAPIRequest) { r.Name = string(make([]rune, 201)) },
		"描述过长":   func(r *app.CreateTaskFromAPIRequest) { r.Description = string(make([]rune, 4001)) },
		"项目标识非法": func(r *app.CreateTaskFromAPIRequest) { r.ProjectID = "Demo Project" },
	}
	for name, mutate := range cases {
		request := fixture.request("任务", "描述")
		mutate(&request)
		_, err := fixture.service.CreateTaskFromAPI(ctx, request)
		if err == nil {
			t.Fatalf("%s: 应当返回参数错误", name)
		}
		if !errors.Is(err, app.ErrTaskInvalidInput) && !errors.Is(err, domain.ErrInvalidValue) {
			t.Fatalf("%s: 错误类别应为参数错误，实际: %v", name, err)
		}
	}
}

// TestCreateTaskFromAPIRejectsUnknownHost 校验执行节点不存在时创建失败。
func TestCreateTaskFromAPIRejectsUnknownHost(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	request := fixture.request("任务", "描述")
	request.HostID = domain.NewID().String()
	if _, err := fixture.service.CreateTaskFromAPI(ctx, request); err == nil {
		t.Fatal("未知执行节点应当被拒绝")
	}
}

// TestCreateTaskFromAPIIsIdempotent 校验相同幂等键不会创建第二个任务。
func TestCreateTaskFromAPIIsIdempotent(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	request := fixture.request("任务", "描述")
	request.IdempotencyKey = "api-key-1"
	first := fixture.create(t, ctx, request)
	second := fixture.create(t, ctx, request)
	if first.ID != second.ID {
		t.Fatalf("相同幂等键应返回同一任务: %s / %s", first.ID, second.ID)
	}
}

// TestListTasksForAPIPaginatesAndFilters 校验列表分页与筛选。
func TestListTasksForAPIPaginatesAndFilters(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	created := make([]domain.Task, 0, 5)
	for index := 0; index < 5; index++ {
		created = append(created, fixture.create(t, ctx, fixture.request("任务", "描述")))
	}
	finished := fixture.finish(
		t,
		ctx,
		created[0],
		domain.TaskStatusPreparing,
		domain.TaskStatusFailed,
	)

	firstPage, err := fixture.service.ListTasksForAPI(ctx, app.TaskPageRequest{Current: 1, Size: 2})
	if err != nil {
		t.Fatalf("查询首页失败: %v", err)
	}
	if firstPage.Total != len(created) {
		t.Fatalf("总数不符: %d", firstPage.Total)
	}
	if len(firstPage.Tasks) != 2 {
		t.Fatalf("首页记录数不符: %d", len(firstPage.Tasks))
	}

	secondPage, err := fixture.service.ListTasksForAPI(ctx, app.TaskPageRequest{Current: 2, Size: 2})
	if err != nil {
		t.Fatalf("查询第二页失败: %v", err)
	}
	for _, left := range firstPage.Tasks {
		for _, right := range secondPage.Tasks {
			if left.ID == right.ID {
				t.Fatalf("分页出现重复记录: %s", left.ID)
			}
		}
	}

	filtered, err := fixture.service.ListTasksForAPI(ctx, app.TaskPageRequest{
		Current:  1,
		Size:     10,
		Statuses: []domain.TaskStatus{domain.TaskStatusFailed},
	})
	if err != nil {
		t.Fatalf("按状态筛选失败: %v", err)
	}
	if filtered.Total != 1 || len(filtered.Tasks) != 1 || filtered.Tasks[0].ID != finished.ID {
		t.Fatalf("状态筛选结果不符: %+v", filtered)
	}
}

// TestListTasksForAPIRejectsOversizedPage 校验页大小超限时被拒绝而不是读取全表。
func TestListTasksForAPIRejectsOversizedPage(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	_, err := fixture.service.ListTasksForAPI(ctx, app.TaskPageRequest{
		Current: 1,
		Size:    app.MaxTaskPageSize + 1,
	})
	if !errors.Is(err, app.ErrTaskInvalidInput) {
		t.Fatalf("页大小超限应返回参数错误，实际: %v", err)
	}
}

// TestListTasksForAPIRejectsUnknownStatus 校验未知状态筛选被拒绝。
func TestListTasksForAPIRejectsUnknownStatus(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	_, err := fixture.service.ListTasksForAPI(ctx, app.TaskPageRequest{
		Statuses: []domain.TaskStatus{"not-a-status"},
	})
	if !errors.Is(err, app.ErrTaskInvalidInput) {
		t.Fatalf("未知状态应返回参数错误，实际: %v", err)
	}
}

// TestGetTaskForAPIReturnsNotFound 校验不存在与已删除任务都返回不存在。
func TestGetTaskForAPIReturnsNotFound(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	if _, err := fixture.service.GetTaskForAPI(ctx, domain.NewID().String()); !errors.Is(
		err,
		app.ErrTaskNotFound,
	) {
		t.Fatalf("不存在的任务应返回不存在，实际: %v", err)
	}

	task := fixture.create(t, ctx, fixture.request("任务", "描述"))
	if err := fixture.service.DeleteTask(ctx, task.ID.String()); err != nil {
		// queued 任务不允许删除，这里先取消再删除。
		if !errors.Is(err, app.ErrTaskStateConflict) {
			t.Fatalf("等待中的任务删除应返回状态冲突，实际: %v", err)
		}
		if _, cancelErr := fixture.service.CancelTaskForAPI(ctx, task.ID.String()); cancelErr != nil {
			t.Fatalf("取消任务失败: %v", cancelErr)
		}
		if err := fixture.service.DeleteTask(ctx, task.ID.String()); err != nil {
			t.Fatalf("取消后删除失败: %v", err)
		}
	}
	if _, err := fixture.service.GetTaskForAPI(ctx, task.ID.String()); !errors.Is(
		err,
		app.ErrTaskNotFound,
	) {
		t.Fatalf("已删除任务应返回不存在，实际: %v", err)
	}
}

// TestCancelTaskForAPICancelsQueuedImmediately 校验等待中的任务直接收敛为已取消。
func TestCancelTaskForAPICancelsQueuedImmediately(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	task := fixture.create(t, ctx, fixture.request("任务", "描述"))
	cancelled, err := fixture.service.CancelTaskForAPI(ctx, task.ID.String())
	if err != nil {
		t.Fatalf("取消任务失败: %v", err)
	}
	if cancelled.Status != domain.TaskStatusCancelled {
		t.Fatalf("等待中的任务应直接取消，实际 %s", cancelled.Status)
	}
	if !cancelled.CancelRequested {
		t.Fatal("应记录取消请求")
	}
	if cancelled.FinishedAt == nil {
		t.Fatal("应记录结束时间")
	}

	// 落盘结果与返回值一致。
	loaded, err := fixture.service.GetTaskForAPI(ctx, task.ID.String())
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.Status != domain.TaskStatusCancelled {
		t.Fatalf("取消结果未持久化: %s", loaded.Status)
	}
}

// TestCancelTaskForAPIMarksRunningAsCancelling 校验执行中的任务只进入取消中。
func TestCancelTaskForAPIMarksRunningAsCancelling(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	task := fixture.finish(t, ctx, fixture.create(t, ctx, fixture.request("任务", "描述")),
		domain.TaskStatusPreparing)
	cancelling, err := fixture.service.CancelTaskForAPI(ctx, task.ID.String())
	if err != nil {
		t.Fatalf("取消任务失败: %v", err)
	}
	if cancelling.Status != domain.TaskStatusCancelling {
		t.Fatalf("执行中的任务应进入 cancelling，实际 %s", cancelling.Status)
	}
	if cancelling.FinalResult != domain.TaskResultNone {
		t.Fatalf("尚未确认停止时不应写入结论: %q", cancelling.FinalResult)
	}
	if cancelling.FinishedAt != nil {
		t.Fatal("尚未确认停止时不应记录结束时间")
	}
}

// TestCancelTaskForAPIIsIdempotent 校验重复取消保持稳定结果。
func TestCancelTaskForAPIIsIdempotent(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	task := fixture.finish(t, ctx, fixture.create(t, ctx, fixture.request("任务", "描述")),
		domain.TaskStatusPreparing)
	first, err := fixture.service.CancelTaskForAPI(ctx, task.ID.String())
	if err != nil {
		t.Fatalf("首次取消失败: %v", err)
	}
	second, err := fixture.service.CancelTaskForAPI(ctx, task.ID.String())
	if err != nil {
		t.Fatalf("重复取消应当成功: %v", err)
	}
	if first.Status != second.Status {
		t.Fatalf("重复取消不应改变状态: %s / %s", first.Status, second.Status)
	}
	if first.CancelRequestedAt == nil || second.CancelRequestedAt == nil {
		t.Fatal("取消请求时间应被记录")
	}
	if !first.CancelRequestedAt.Equal(*second.CancelRequestedAt) {
		t.Fatalf("重复取消不应改写请求时间: %v / %v",
			*first.CancelRequestedAt, *second.CancelRequestedAt)
	}
}

// TestCancelTaskForAPIRejectsFinishedTask 校验已结束的任务不能再取消。
func TestCancelTaskForAPIRejectsFinishedTask(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	task := fixture.finish(
		t,
		ctx,
		fixture.create(t, ctx, fixture.request("任务", "描述")),
		domain.TaskStatusPreparing,
		domain.TaskStatusFailed,
	)
	_, err := fixture.service.CancelTaskForAPI(ctx, task.ID.String())
	if !errors.Is(err, app.ErrTaskStateConflict) {
		t.Fatalf("已结束任务取消应返回状态冲突，实际: %v", err)
	}
	loaded, err := fixture.service.GetTaskForAPI(ctx, task.ID.String())
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.Status != domain.TaskStatusFailed {
		t.Fatalf("拒绝时不应改变状态: %s", loaded.Status)
	}
}

// TestDeleteTaskRejectsRunningTask 校验执行中的任务不允许删除。
func TestDeleteTaskRejectsRunningTask(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	task := fixture.finish(t, ctx, fixture.create(t, ctx, fixture.request("任务", "描述")),
		domain.TaskStatusPreparing)
	if err := fixture.service.DeleteTask(ctx, task.ID.String()); !errors.Is(
		err,
		app.ErrTaskStateConflict,
	) {
		t.Fatalf("执行中的任务删除应返回状态冲突，实际: %v", err)
	}
	if _, err := fixture.service.GetTaskForAPI(ctx, task.ID.String()); err != nil {
		t.Fatalf("拒绝删除时任务应仍然存在: %v", err)
	}
}

// TestDeleteTaskRejectsCancellingTask 校验取消中的任务不允许删除。
func TestDeleteTaskRejectsCancellingTask(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	task := fixture.finish(t, ctx, fixture.create(t, ctx, fixture.request("任务", "描述")),
		domain.TaskStatusPreparing)
	if _, err := fixture.service.CancelTaskForAPI(ctx, task.ID.String()); err != nil {
		t.Fatalf("取消任务失败: %v", err)
	}
	if err := fixture.service.DeleteTask(ctx, task.ID.String()); !errors.Is(
		err,
		app.ErrTaskStateConflict,
	) {
		t.Fatalf("取消中的任务删除应返回状态冲突，实际: %v", err)
	}
}

// TestDeleteTaskAcceptsFinishedTask 校验已结束任务可以删除且历史保留。
func TestDeleteTaskAcceptsFinishedTask(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	task := fixture.finish(
		t,
		ctx,
		fixture.create(t, ctx, fixture.request("任务", "描述")),
		domain.TaskStatusPreparing,
		domain.TaskStatusFailed,
	)
	if err := fixture.service.DeleteTask(ctx, task.ID.String()); err != nil {
		t.Fatalf("删除已结束任务失败: %v", err)
	}
	// 记录仍在数据库中，只是被标记删除。
	raw, err := fixture.db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("删除后记录不应消失: %v", err)
	}
	if raw.DeletedAt == nil {
		t.Fatal("deleted_at 应当被写入")
	}
	// 需求记录不受影响。
	if _, err := fixture.db.GetRequirement(ctx, task.RequirementID); err != nil {
		t.Fatalf("删除任务不应影响需求: %v", err)
	}
	// 列表不再返回该任务。
	page, err := fixture.service.ListTasksForAPI(ctx, app.TaskPageRequest{Current: 1, Size: 10})
	if err != nil {
		t.Fatalf("查询任务列表失败: %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("已删除任务不应出现在列表中: %d", page.Total)
	}
}

// TestDeleteTaskAfterCancelSucceeds 校验取消后可以删除。
func TestDeleteTaskAfterCancelSucceeds(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	task := fixture.create(t, ctx, fixture.request("任务", "描述"))
	if _, err := fixture.service.CancelTaskForAPI(ctx, task.ID.String()); err != nil {
		t.Fatalf("取消任务失败: %v", err)
	}
	if err := fixture.service.DeleteTask(ctx, task.ID.String()); err != nil {
		t.Fatalf("取消后删除失败: %v", err)
	}
}

// TestDeleteTaskRejectsRepeatedDelete 校验重复删除返回不存在。
func TestDeleteTaskRejectsRepeatedDelete(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	task := fixture.create(t, ctx, fixture.request("任务", "描述"))
	if _, err := fixture.service.CancelTaskForAPI(ctx, task.ID.String()); err != nil {
		t.Fatalf("取消任务失败: %v", err)
	}
	if err := fixture.service.DeleteTask(ctx, task.ID.String()); err != nil {
		t.Fatalf("首次删除失败: %v", err)
	}
	if err := fixture.service.DeleteTask(ctx, task.ID.String()); !errors.Is(
		err,
		app.ErrTaskNotFound,
	) {
		t.Fatalf("重复删除应返回不存在，实际: %v", err)
	}
}

// TestConcurrentCancelOnlyOneTransitionWins 校验并发取消不会互相覆盖状态。
func TestConcurrentCancelOnlyOneTransitionWins(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	task := fixture.finish(t, ctx, fixture.create(t, ctx, fixture.request("任务", "描述")),
		domain.TaskStatusPreparing)

	const workers = 8
	results := make(chan error, workers)
	for index := 0; index < workers; index++ {
		go func() {
			_, err := fixture.service.CancelTaskForAPI(ctx, task.ID.String())
			results <- err
		}()
	}
	succeeded := 0
	for index := 0; index < workers; index++ {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, app.ErrTaskStateConflict),
			errors.Is(err, storage.ErrConflict):
			// 其余请求明确失败，不静默成功。
		default:
			t.Fatalf("并发取消返回了非预期错误: %v", err)
		}
	}
	if succeeded == 0 {
		t.Fatal("并发取消至少应有一个成功")
	}
	loaded, err := fixture.service.GetTaskForAPI(ctx, task.ID.String())
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.Status != domain.TaskStatusCancelling {
		t.Fatalf("并发取消后状态应为 cancelling，实际 %s", loaded.Status)
	}
	if !loaded.CancelRequested {
		t.Fatal("取消请求应被记录")
	}
}

// TestConcurrentDeleteOnlyOneSucceeds 校验并发删除同一任务只有一个成功。
func TestConcurrentDeleteOnlyOneSucceeds(t *testing.T) {
	t.Parallel()
	fixture := newTaskManagementFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	task := fixture.finish(
		t,
		ctx,
		fixture.create(t, ctx, fixture.request("任务", "描述")),
		domain.TaskStatusPreparing,
		domain.TaskStatusFailed,
	)
	const workers = 6
	results := make(chan error, workers)
	for index := 0; index < workers; index++ {
		go func() {
			results <- fixture.service.DeleteTask(ctx, task.ID.String())
		}()
	}
	succeeded := 0
	for index := 0; index < workers; index++ {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, app.ErrTaskNotFound), errors.Is(err, app.ErrTaskStateConflict):
		default:
			t.Fatalf("并发删除返回了非预期错误: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("并发删除应当只有一个成功，实际 %d", succeeded)
	}
}
