package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 需求删除接口的协议测试。
//
// 这些用例只验证协议转换、错误分类与真实响应字段：删除的原子性、执行权与执行现场
// 核验由应用层用例证明，这里不重复实现判定。

// requirementDeleteFixture 在确认接口现场之上装配带任务执行协调器的删除接口。
type requirementDeleteFixture struct {
	*requirementAPIFixture
	coordination *app.TaskExecutionCoordinator
	handler      http.Handler
	service      *app.RequirementService
}

// newRequirementDeleteFixture 打开数据库并装配可删除需求的服务与处理器。
func newRequirementDeleteFixture(t *testing.T) *requirementDeleteFixture {
	t.Helper()
	base := newRequirementAPIFixture(t)
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	coordination := app.NewTaskExecutionCoordinator()
	service, err := app.NewRequirementService(app.RequirementServiceOptions{
		Coordination: coordination,
		Store:        base.db,
	})
	if err != nil {
		t.Fatalf("构造需求服务失败: %v", err)
	}
	return &requirementDeleteFixture{
		requirementAPIFixture: base,
		coordination:          coordination,
		handler:               httpapi.NewHandler(httpapi.Deps{Logger: logger.Slog(), Requirement: service}),
		service:               service,
	}
}

// requirementPath 返回单个需求的接口路径。
func requirementPath(requirementID string) string {
	return httpapi.PathRequirements + "/" + requirementID
}

// confirm 生成 1 个需求与两个任务并返回确认结果。
func (f *requirementDeleteFixture) confirm(t *testing.T) app.ConfirmRequirementResult {
	t.Helper()
	session, revision := f.previewSession(t)
	result, err := f.baseConfirm().Confirm(f.ctx, app.ConfirmRequirementRequest{
		PlanningSessionID: session.ID.String(),
		RevisionID:        revision.ID.String(),
	})
	if err != nil {
		t.Fatalf("确认方案失败: %v", err)
	}
	return result
}

// baseConfirm 返回确认用的服务实例（不需要执行协调器）。
func (f *requirementDeleteFixture) baseConfirm() *app.RequirementService {
	return f.requirementAPIFixture.service
}

// setTaskStatus 把任务推进到指定状态并落库。
func (f *requirementDeleteFixture) setTaskStatus(
	t *testing.T,
	taskID domain.ID,
	status domain.TaskStatus,
) {
	t.Helper()
	task, err := f.db.GetTask(f.ctx, taskID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	path := httpStatusPath(status)
	for _, next := range path {
		if err := task.Transition(next, time.Now().UTC()); err != nil {
			t.Fatalf("任务迁移到 %s 失败: %v", next, err)
		}
	}
	if err := f.db.UpdateTask(f.ctx, *task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
}

// httpStatusPath 返回从 queued 到达目标状态的合法迁移序列。
func httpStatusPath(status domain.TaskStatus) []domain.TaskStatus {
	switch status {
	case domain.TaskStatusPreparing:
		return []domain.TaskStatus{domain.TaskStatusPreparing}
	case domain.TaskStatusDeveloping:
		return []domain.TaskStatus{domain.TaskStatusPreparing, domain.TaskStatusDeveloping}
	case domain.TaskStatusCancelling:
		return []domain.TaskStatus{domain.TaskStatusCancelling}
	case domain.TaskStatusFailed:
		return []domain.TaskStatus{domain.TaskStatusFailed}
	case domain.TaskStatusRecoveryRequired:
		return []domain.TaskStatus{domain.TaskStatusFailed}
	default:
		panic("未定义的状态迁移路径: " + string(status))
	}
}

// TestRequirementAPIDeleteReturnsRealResult 验收删除返回真实需求标识与任务数量。
func TestRequirementAPIDeleteReturnsRealResult(t *testing.T) {
	t.Parallel()
	f := newRequirementDeleteFixture(t)
	result := f.confirm(t)
	f.setTaskStatus(t, result.Tasks[0].ID, domain.TaskStatusFailed)

	recorder := f.send(t, f.handler, http.MethodDelete, requirementPath(result.Requirement.ID.String()), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("删除应返回 200，实际 %d，响应体 %s", recorder.Code, recorder.Body.String())
	}
	var payload httpapi.DeleteRequirementResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析删除响应失败: %v", err)
	}
	if payload.RequirementID != result.Requirement.ID.String() {
		t.Fatalf("响应需求标识不符: %s", payload.RequirementID)
	}
	if payload.DeletedTaskCount != len(result.Tasks) {
		t.Fatalf("响应任务数量不符: %d", payload.DeletedTaskCount)
	}
	if payload.DeletedAt.IsZero() {
		t.Fatal("响应必须包含真实删除时间")
	}

	// 真实落库：需求与任务都写入删除时间，且默认列表不再返回。
	persisted, err := f.db.GetRequirement(f.ctx, result.Requirement.ID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	// 数据库以毫秒保存时间，响应保留完整精度，因此按毫秒比较真实写入值。
	if persisted.DeletedAt == nil ||
		!persisted.DeletedAt.Equal(payload.DeletedAt.Truncate(time.Millisecond)) {
		t.Fatalf("数据库删除时间与响应不一致: %+v", persisted.DeletedAt)
	}
	for _, task := range result.Tasks {
		persistedTask, taskErr := f.db.GetTask(f.ctx, task.ID)
		if taskErr != nil {
			t.Fatalf("读取任务失败: %v", taskErr)
		}
		if persistedTask.DeletedAt == nil {
			t.Fatalf("任务 %s 应写入删除时间", task.ID)
		}
	}
	if len(f.requirements(t)) != 0 {
		t.Fatal("删除后默认需求列表应为空")
	}

	// 详情按不存在处理（进度接口依赖项目执行服务，由项目执行用例覆盖）。
	probe := f.send(t, f.handler, http.MethodGet, requirementPath(result.Requirement.ID.String()), nil)
	if probe.Code != http.StatusNotFound {
		t.Fatalf("删除后需求详情应返回 404，实际 %d", probe.Code)
	}
}

// TestRequirementAPIDeleteMissingAndRepeated 验收不存在与重复删除的错误语义。
func TestRequirementAPIDeleteMissingAndRepeated(t *testing.T) {
	t.Parallel()
	f := newRequirementDeleteFixture(t)
	missing := f.send(t, f.handler, http.MethodDelete, requirementPath(domain.NewID().String()), nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("不存在的需求应返回 404，实际 %d", missing.Code)
	}
	if !hasErrorCode(missing.Body.Bytes(), "not_found") {
		t.Fatalf("错误码应为 not_found: %s", missing.Body.String())
	}

	result := f.confirm(t)
	first := f.send(t, f.handler, http.MethodDelete, requirementPath(result.Requirement.ID.String()), nil)
	if first.Code != http.StatusOK {
		t.Fatalf("首次删除应返回 200，实际 %d", first.Code)
	}
	second := f.send(t, f.handler, http.MethodDelete, requirementPath(result.Requirement.ID.String()), nil)
	if second.Code != http.StatusNotFound {
		t.Fatalf("重复删除应返回 404，实际 %d", second.Code)
	}
	if !hasErrorCode(second.Body.Bytes(), "not_found") {
		t.Fatalf("重复删除的错误码应为 not_found: %s", second.Body.String())
	}
}

// TestRequirementAPIDeleteRejectsInvalidID 验收非法路径标识按"接口不存在"处理。
//
// 路径标识的合法性由共享的 pathIDGuard 判定，与任务、项目等所有路径标识接口一致：
// 非法取值不是协议参数错误，而是无法定位接口，因此返回 404 而不是 400。
func TestRequirementAPIDeleteRejectsInvalidID(t *testing.T) {
	t.Parallel()
	f := newRequirementDeleteFixture(t)
	recorder := f.send(t, f.handler, http.MethodDelete, requirementPath("not-a-valid-id"), nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("非法路径标识应返回 404，实际 %d，响应体 %s", recorder.Code, recorder.Body.String())
	}
}

// TestRequirementAPIDeleteRejectsUnsettledTasks 验收未收敛任务的删除冲突。
func TestRequirementAPIDeleteRejectsUnsettledTasks(t *testing.T) {
	t.Parallel()
	statuses := []domain.TaskStatus{
		domain.TaskStatusPreparing,
		domain.TaskStatusDeveloping,
		domain.TaskStatusCancelling,
		domain.TaskStatusRecoveryRequired,
	}
	for _, status := range statuses {
		status := status
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			f := newRequirementDeleteFixture(t)
			result := f.confirm(t)
			if status == domain.TaskStatusRecoveryRequired {
				raw := testkit.OpenSQLite(t, f.db.Path())
				if _, err := raw.ExecContext(
					f.ctx,
					`UPDATE tasks SET status = ? WHERE id = ?`,
					string(status),
					result.Tasks[0].ID.String(),
				); err != nil {
					t.Fatalf("写入未知/待核验状态失败: %v", err)
				}
			} else {
				f.setTaskStatus(t, result.Tasks[0].ID, status)
			}

			recorder := f.send(
				t,
				f.handler,
				http.MethodDelete,
				requirementPath(result.Requirement.ID.String()),
				nil,
			)
			if recorder.Code != http.StatusConflict {
				t.Fatalf("状态 %s 应返回 409，实际 %d，响应体 %s",
					status, recorder.Code, recorder.Body.String())
			}
			if !hasErrorCode(recorder.Body.Bytes(), "state_conflict") {
				t.Fatalf("错误码应为 state_conflict: %s", recorder.Body.String())
			}
			if len(f.requirements(t)) != 1 {
				t.Fatal("被拒绝时需求不得被删除")
			}
			visible, err := f.db.ListTasks(f.ctx, storage.TaskFilter{
				RequirementID: result.Requirement.ID,
			})
			if err != nil {
				t.Fatalf("查询任务失败: %v", err)
			}
			if len(visible) != len(result.Tasks) {
				t.Fatalf("被拒绝时全部任务都应可见: %d", len(visible))
			}
		})
	}
}

// TestRequirementAPIDeleteUnknownStatusConflicts 验收未知任务状态同样拒绝删除。
func TestRequirementAPIDeleteUnknownStatusConflicts(t *testing.T) {
	t.Parallel()
	f := newRequirementDeleteFixture(t)
	result := f.confirm(t)
	raw := testkit.OpenSQLite(t, f.db.Path())
	if _, err := raw.ExecContext(
		f.ctx,
		`UPDATE tasks SET status = ? WHERE id = ?`,
		"mystery",
		result.Tasks[0].ID.String(),
	); err != nil {
		t.Fatalf("写入未知状态失败: %v", err)
	}
	recorder := f.send(t, f.handler, http.MethodDelete, requirementPath(result.Requirement.ID.String()), nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("未知状态应返回 409，实际 %d，响应体 %s", recorder.Code, recorder.Body.String())
	}
	persisted, err := f.db.GetRequirement(f.ctx, result.Requirement.ID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if persisted.DeletedAt != nil {
		t.Fatal("被拒绝时不得写入删除时间")
	}
}

// TestRequirementAPIDeleteUnavailableWithoutCoordination 验收删除能力未装配返回 503。
func TestRequirementAPIDeleteUnavailableWithoutCoordination(t *testing.T) {
	t.Parallel()
	f := newRequirementDeleteFixture(t)
	result := f.confirm(t)
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	service, err := app.NewRequirementService(app.RequirementServiceOptions{Store: f.db})
	if err != nil {
		t.Fatalf("构造需求服务失败: %v", err)
	}
	handler := httpapi.NewHandler(httpapi.Deps{Logger: logger.Slog(), Requirement: service})
	recorder := f.send(t, handler, http.MethodDelete, requirementPath(result.Requirement.ID.String()), nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("缺少协调器应返回 503，实际 %d，响应体 %s", recorder.Code, recorder.Body.String())
	}
}

// TestRequirementAPIDeleteBareHandler 验收服务未装配时任何方法都返回 503。
func TestRequirementAPIDeleteBareHandler(t *testing.T) {
	t.Parallel()
	f := newRequirementDeleteFixture(t)
	recorder := f.send(t, f.bare, http.MethodDelete, requirementPath(domain.NewID().String()), nil)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("服务未装配应返回 503，实际 %d", recorder.Code)
	}
}

// TestRequirementAPIDeleteMethodNotAllowed 验收不支持的方法返回 405 与 Allow。
func TestRequirementAPIDeleteMethodNotAllowed(t *testing.T) {
	t.Parallel()
	f := newRequirementDeleteFixture(t)
	result := f.confirm(t)
	recorder := f.send(t, f.handler, http.MethodPost, requirementPath(result.Requirement.ID.String()), nil)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("不支持的方法应返回 405，实际 %d", recorder.Code)
	}
	if allow := recorder.Header().Get("Allow"); allow != "GET, HEAD, DELETE" {
		t.Fatalf("Allow 头不符: %s", allow)
	}
}

// TestRequirementAPIDeleteReportsPersistenceFailure 验收事务提交失败返回 500。
func TestRequirementAPIDeleteReportsPersistenceFailure(t *testing.T) {
	t.Parallel()
	f := newRequirementDeleteFixture(t)
	result := f.confirm(t)
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	broken, err := app.NewRequirementService(app.RequirementServiceOptions{
		Coordination: app.NewTaskExecutionCoordinator(),
		Store:        &failingRequirementStore{Store: f.db},
	})
	if err != nil {
		t.Fatalf("构造失败存储的需求服务失败: %v", err)
	}
	handler := httpapi.NewHandler(httpapi.Deps{Logger: logger.Slog(), Requirement: broken})
	recorder := f.send(t, handler, http.MethodDelete, requirementPath(result.Requirement.ID.String()), nil)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("持久化失败应返回 500，实际 %d，响应体 %s", recorder.Code, recorder.Body.String())
	}
	if !hasErrorCode(recorder.Body.Bytes(), "internal_error") {
		t.Fatalf("错误码应为 internal_error: %s", recorder.Body.String())
	}
	persisted, err := f.db.GetRequirement(f.ctx, result.Requirement.ID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if persisted.DeletedAt != nil {
		t.Fatal("持久化失败后需求不得被删除")
	}
}

// TestRequirementAPIDeleteReportsEventFailure 验收事件写入失败返回 500 且整体回滚。
func TestRequirementAPIDeleteReportsEventFailure(t *testing.T) {
	t.Parallel()
	f := newRequirementDeleteFixture(t)
	result := f.confirm(t)
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	broken, err := app.NewRequirementService(app.RequirementServiceOptions{
		Coordination: app.NewTaskExecutionCoordinator(),
		Store:        &eventFailRequirementStore{Store: f.db},
	})
	if err != nil {
		t.Fatalf("构造失败存储的需求服务失败: %v", err)
	}
	handler := httpapi.NewHandler(httpapi.Deps{Logger: logger.Slog(), Requirement: broken})
	recorder := f.send(t, handler, http.MethodDelete, requirementPath(result.Requirement.ID.String()), nil)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("事件写入失败应返回 500，实际 %d，响应体 %s", recorder.Code, recorder.Body.String())
	}
	persisted, err := f.db.GetRequirement(f.ctx, result.Requirement.ID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if persisted.DeletedAt != nil {
		t.Fatal("事件写入失败后需求删除必须回滚")
	}
	for _, task := range result.Tasks {
		persistedTask, taskErr := f.db.GetTask(f.ctx, task.ID)
		if taskErr != nil {
			t.Fatalf("读取任务失败: %v", taskErr)
		}
		if persistedTask.DeletedAt != nil {
			t.Fatalf("事件写入失败后任务 %s 的删除必须回滚", task.ID)
		}
	}
}

// eventFailRequirementStore 让事件写入失败，用于验收事件失败时整体回滚。
type eventFailRequirementStore struct {
	storage.Store
}

// WithTx 在事务内注入事件写入失败。
func (s *eventFailRequirementStore) WithTx(
	ctx context.Context,
	fn func(ctx context.Context, tx storage.Tx) error,
) error {
	return s.Store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		return fn(ctx, &eventFailTx{Tx: tx})
	})
}

// eventFailTx 只拦截事件写入。
type eventFailTx struct {
	storage.Tx
}

// AppendEvent 始终返回存储失败。
func (t *eventFailTx) AppendEvent(
	ctx context.Context,
	event domain.Event,
) (domain.Event, error) {
	return domain.Event{}, errors.New("事件写入失败")
}

// hasErrorCode 判断响应体的错误码。
func hasErrorCode(body []byte, code string) bool {
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return payload.Error.Code == code
}
