// Package httpapi_test 提供任务显式重试接口的协议测试。
//
// 用例只验证路径分发、请求体校验、状态码映射与只读资格查询：重试服务的启动端口由
// 受控替身提供，因此默认执行路径不访问真实 Pi、模型、SSH 或 Docker。
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// retryAPITaskStore 是重试接口路径上的存储替身。
type retryAPITaskStore struct {
	task  domain.Task
	steps []domain.Step
	// activeRun 表示存在未结束的执行记录。
	activeRun bool
	// codeSaves 是代码保存记录。
	codeSaves []domain.CodeSaveRecord
	// updateErr 使任务写入失败。
	updateErr error
	// writes 统计任务写入次数。
	writes int
}

// GetTask 返回受控的任务记录。
func (s *retryAPITaskStore) GetTask(
	ctx context.Context,
	id domain.ID,
) (*domain.Task, error) {
	if s.task.ID != id {
		return nil, storage.ErrNotFound
	}
	task := s.task
	return &task, nil
}

// ListStepsByTask 返回受控的计划步骤。
func (s *retryAPITaskStore) ListStepsByTask(
	ctx context.Context,
	taskID domain.ID,
) ([]domain.Step, error) {
	return append([]domain.Step(nil), s.steps...), nil
}

// GetActiveWorkflowRun 返回受控的执行记录。
func (s *retryAPITaskStore) GetActiveWorkflowRun(
	ctx context.Context,
	taskID domain.ID,
) (*domain.WorkflowRun, error) {
	if !s.activeRun {
		return nil, storage.ErrNotFound
	}
	return &domain.WorkflowRun{
		ID:     domain.NewID(),
		TaskID: taskID,
		Status: domain.WorkflowRunRunning,
	}, nil
}

// ListCodeSaveRecordsByTask 返回受控的代码保存记录。
func (s *retryAPITaskStore) ListCodeSaveRecordsByTask(
	ctx context.Context,
	taskID string,
) ([]domain.CodeSaveRecord, error) {
	return append([]domain.CodeSaveRecord(nil), s.codeSaves...), nil
}

// UpdateTaskLeaseGuard 记录一次任务写入。
func (s *retryAPITaskStore) UpdateTaskLeaseGuard(
	ctx context.Context,
	task domain.Task,
	expected []domain.TaskStatus,
	handle domain.TaskLeaseHandle,
) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.writes++
	s.task = task
	return nil
}

// AcquireTaskLease 记录一次领取并返回代次 1。
func (s *retryAPITaskStore) AcquireTaskLease(
	ctx context.Context,
	request storage.TaskLeaseRequest,
) (domain.TaskLeaseHandle, error) {
	s.task.LeaseOwner = request.Owner
	s.task.LeaseEpoch = 1
	expires := request.At.Add(request.TTL)
	s.task.LeaseExpiresAt = &expires
	return domain.TaskLeaseHandle{TaskID: request.TaskID, Owner: request.Owner, Epoch: 1}, nil
}

// RenewTaskLease 忽略续期。
func (s *retryAPITaskStore) RenewTaskLease(
	ctx context.Context,
	request storage.TaskLeaseRenewRequest,
) error {
	return nil
}

// ReleaseTaskLease 清除受控记录上的租约。
func (s *retryAPITaskStore) ReleaseTaskLease(
	ctx context.Context,
	handle domain.TaskLeaseHandle,
	at time.Time,
) (bool, error) {
	if s.task.LeaseOwner != handle.Owner || s.task.LeaseEpoch != handle.Epoch {
		return false, storage.ErrConflict
	}
	s.task.LeaseOwner = ""
	s.task.LeaseExpiresAt = nil
	return true, nil
}

// AppendTaskLeaseEvent 忽略租约事件写入。
func (s *retryAPITaskStore) AppendTaskLeaseEvent(
	ctx context.Context,
	event domain.Event,
) (domain.Event, error) {
	return event, nil
}

// AppendEvent 忽略事件写入。
func (s *retryAPITaskStore) AppendEvent(
	ctx context.Context,
	event domain.Event,
) (domain.Event, error) {
	return event, nil
}

// retryAPILaunch 是统一启动端口的受控替身。
type retryAPILaunch struct {
	started bool
	calls   int
}

// Launch 记录一次启动。
func (l *retryAPILaunch) Launch(
	ctx context.Context,
	request app.TaskLaunchRequest,
) (app.TaskLaunchResult, error) {
	l.calls++
	return app.TaskLaunchResult{
		TaskID:          domain.ID(request.TaskID),
		Stage:           domain.TaskLaunchStageWorkflow,
		TaskStatus:      domain.TaskStatusPlanned,
		WorkflowStarted: l.started,
	}, nil
}

// retryAPIHandler 构造挂载重试接口的处理器。
func retryAPIHandler(
	t *testing.T,
	store *retryAPITaskStore,
	launch *retryAPILaunch,
) http.Handler {
	t.Helper()
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	leases, err := app.NewTaskLeaseService(app.TaskLeaseOptions{Store: store})
	if err != nil {
		t.Fatalf("构造任务租约服务失败: %v", err)
	}
	retry, err := app.NewTaskRetryService(app.TaskRetryOptions{
		Store:  store,
		Leases: leases,
		Launch: launch,
		Logger: logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造任务重试服务失败: %v", err)
	}
	return httpapi.NewHandler(httpapi.Deps{
		TaskRetry: retry,
		Logger:    logger.Slog(),
	})
}

// failedAPITask 构造一个已收敛到 failed 的任务与步骤。
func failedAPITask(t *testing.T) (*retryAPITaskStore, domain.Task) {
	t.Helper()
	now := time.Now().UTC()
	hostID := domain.NewID()
	task, err := domain.NewTask(
		domain.NewID(),
		"ssh://git.example.com/team/repo",
		"main",
		"",
		now,
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &hostID
	task.Config = domain.TaskConfig{
		Goal:            "完成需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		PlannerRoleID:   "planner",
		DeveloperRoleID: "developer",
	}
	if err := task.Transition(domain.TaskStatusFailed, now); err != nil {
		t.Fatalf("任务迁移到 failed 失败: %v", err)
	}
	if err := task.Finish(domain.TaskResultFailed, "上一周期失败", now); err != nil {
		t.Fatalf("写入失败结论失败: %v", err)
	}
	role, err := domain.NewRole(
		"开发工程师",
		domain.RoleTypeDeveloper,
		"group/free",
		"开发提示词",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	step, err := domain.NewAgentStep(
		task.ID,
		1,
		domain.StepTypeDevelop,
		"实现接口",
		"按需求实现",
		"",
		role,
		domain.VerificationSpec{
			Commands: []domain.VerificationCommand{
				{Name: "build", Command: "go", Args: []string{"build", "./..."}, Required: true},
			},
		},
		domain.RetryConfig{MaxAttempts: 1},
		now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := step.Transition(domain.StepStatusRunning, now); err != nil {
		t.Fatalf("步骤迁移失败: %v", err)
	}
	if err := step.Transition(domain.StepStatusFailed, now); err != nil {
		t.Fatalf("步骤迁移失败: %v", err)
	}
	return &retryAPITaskStore{task: task, steps: []domain.Step{step}}, task
}

// TestRetryAPIAcceptsFailedTask 验证重试接口接受失败的干净任务。
func TestRetryAPIAcceptsFailedTask(t *testing.T) {
	t.Parallel()
	store, task := failedAPITask(t)
	launch := &retryAPILaunch{started: true}
	handler := retryAPIHandler(t, store, launch)
	recorder := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"prompt":"补充说明"}`)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/"+task.ID.String()+"/retry",
		body,
	)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("重试应返回 202，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.RetryTaskResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if response.PreviousStatus != string(domain.TaskStatusFailed) {
		t.Fatalf("响应必须回显重试前状态，实际 %q", response.PreviousStatus)
	}
	if !response.WorkflowStarted {
		t.Fatal("替身声明已启动时响应必须为 true")
	}
	if launch.calls != 1 {
		t.Fatalf("重试必须调用一次启动流程，实际 %d 次", launch.calls)
	}
	if store.writes != 1 {
		t.Fatalf("重试必须写入一次任务状态，实际 %d 次", store.writes)
	}
}

// TestRetryAPIRejectsWrongMethod 验证重试接口只接受 POST。
func TestRetryAPIRejectsWrongMethod(t *testing.T) {
	t.Parallel()
	store, task := failedAPITask(t)
	handler := retryAPIHandler(t, store, &retryAPILaunch{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/"+task.ID.String()+"/retry",
		nil,
	)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("应返回 405，实际 %d", recorder.Code)
	}
	if allow := recorder.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow 头应为 POST，实际 %q", allow)
	}
}

// TestRetryAPIRejectsNonFailedTask 验证非失败状态返回 409。
func TestRetryAPIRejectsNonFailedTask(t *testing.T) {
	t.Parallel()
	store, task := failedAPITask(t)
	// 任务回到 developing：重试必须被拒绝。
	task.Status = domain.TaskStatusDeveloping
	store.task = task
	handler := retryAPIHandler(t, store, &retryAPILaunch{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/"+task.ID.String()+"/retry",
		nil,
	)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("应返回 409，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
}

// TestRetryAPINotFound 验证不存在的任务返回 404。
func TestRetryAPINotFound(t *testing.T) {
	t.Parallel()
	store, _ := failedAPITask(t)
	handler := retryAPIHandler(t, store, &retryAPILaunch{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/"+domain.NewID().String()+"/retry",
		nil,
	)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("应返回 404，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
}

// TestRetryAPIUnavailableWithoutService 验证未装配重试服务时返回 503。
func TestRetryAPIUnavailableWithoutService(t *testing.T) {
	t.Parallel()
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	handler := httpapi.NewHandler(httpapi.Deps{Logger: logger.Slog()})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/"+domain.NewID().String()+"/retry",
		nil,
	)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("应返回 503，实际 %d", recorder.Code)
	}
}

// TestRetryEligibilityAPIIsReadOnly 验证资格查询是只读的。
func TestRetryEligibilityAPIIsReadOnly(t *testing.T) {
	t.Parallel()
	store, task := failedAPITask(t)
	launch := &retryAPILaunch{started: true}
	handler := retryAPIHandler(t, store, launch)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/"+task.ID.String()+"/retry-eligibility",
		nil,
	)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("应返回 200，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.RetryEligibilityResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !response.CanRetry {
		t.Fatalf("干净的失败任务应可重试，实际阻塞项: %v", response.Blocking)
	}
	if response.TaskStatus != string(domain.TaskStatusFailed) {
		t.Fatalf("应回显任务状态，实际 %q", response.TaskStatus)
	}
	if store.writes != 0 {
		t.Fatalf("资格查询不得写任务，实际写入 %d 次", store.writes)
	}
	if launch.calls != 0 {
		t.Fatal("资格查询不得调用启动流程")
	}
}
