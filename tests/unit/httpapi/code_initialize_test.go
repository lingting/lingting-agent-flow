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
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/source"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 代码初始化接口测试使用的固定 commit SHA。
const (
	codeInitAPITaskBranch = "agent/"
	codeInitAPIBaseSHA    = "1111111111111111111111111111111111111111"
	codeInitAPIHeadSHA    = "2222222222222222222222222222222222222222"
)

// codeInitAPIExecutor 是容器能力的受控替身，只用于初始化接口测试。
type codeInitAPIExecutor struct {
	running bool
	// fetchErr 是代码获取返回的错误。
	fetchErr error
	// fetched 是代码获取返回的结果。
	fetched source.FetchResult
	// calls 统计代码获取次数。
	calls int
	// attemptID 是容器记录登记的尝试归属。
	attemptID domain.ID
}

// InspectContainer 返回受控的容器状态。
func (e *codeInitAPIExecutor) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	attemptID := domain.ID("attempt-placeholder")
	if e.attemptID != "" {
		attemptID = e.attemptID
	}
	return app.ContainerDetail{
		Container: domain.Container{
			ID:        domain.ID(request.ContainerID),
			HostID:    "host-1",
			Name:      "agentflow-abcdef123456",
			DockerID:  "docker-" + request.ContainerID,
			TaskID:    &attemptID,
			AttemptID: &attemptID,
			State:     domain.ContainerStateRunning,
		},
		Inspect: &docker.ContainerInspect{Running: e.running},
	}, nil
}

// FetchSourceWith 返回受控的代码获取结果。
func (e *codeInitAPIExecutor) FetchSourceWith(
	ctx context.Context,
	containerID string,
	request source.FetchRequest,
) (source.FetchResult, error) {
	e.calls++
	if e.fetchErr != nil {
		return e.fetched, e.fetchErr
	}
	fetched := e.fetched
	if fetched.Commit == "" {
		fetched = source.FetchResult{
			TaskID:           request.TaskID,
			AttemptID:        request.AttemptID,
			SourceType:       domain.CodeSourceGit,
			Repository:       "https://git.example.com/group/repo.git",
			ContainerID:      containerID,
			DockerID:         "docker-" + containerID,
			WorkDir:          request.WorkDir,
			BaseBranch:       "main",
			BaseSelection:    domain.GitBranchSelectionOriginHead,
			BaseCommit:       codeInitAPIBaseSHA,
			TaskBranch:       codeInitAPITaskBranch + request.TaskID,
			TaskBranchSource: domain.TaskBranchSourceNew,
			Commit:           codeInitAPIHeadSHA,
			Stage:            domain.CodeFetchStageVerify,
		}
	}
	now := time.Now().UTC()
	fetched.StartedAt = now
	fetched.FinishedAt = now
	return fetched, nil
}

// seedCodeInitAPIWorkspace 写入一条任务级工作区已就绪的记录。
func seedCodeInitAPIWorkspace(
	t *testing.T,
	db *sqlite.DB,
	task domain.Task,
	attempt domain.Attempt,
	now time.Time,
) {
	t.Helper()
	ctx := context.Background()
	containerID := ""
	if task.ContainerID != "" {
		containerID = task.ContainerID
	}
	record, err := domain.NewTaskWorkspaceInitRecord(
		domain.NewID(),
		task.ID.String(),
		containerID,
		"docker-"+containerID,
		domain.CodeSourceGit,
		domain.DefaultContainerWorkDir,
		now,
	)
	if err != nil {
		t.Fatalf("构造任务级工作区记录失败: %v", err)
	}
	if err := db.CreateTaskWorkspaceInit(ctx, record); err != nil {
		t.Fatalf("写入任务级工作区记录失败: %v", err)
	}
	if err := record.MarkRunning(domain.CodeFetchStageClone, now); err != nil {
		t.Fatalf("标记任务级工作区运行失败: %v", err)
	}
	if err := record.MarkSucceeded(
		"main",
		domain.GitBranchSelectionExplicit,
		codeInitAPIBaseSHA,
		domain.TaskBranchSourceNew.String(),
		codeInitAPIHeadSHA,
		now,
	); err != nil {
		t.Fatalf("标记任务级工作区成功失败: %v", err)
	}
	if err := db.UpdateTaskWorkspaceInit(ctx, record); err != nil {
		t.Fatalf("更新任务级工作区记录失败: %v", err)
	}
	_ = attempt
}

// codeInitAPIFixture 是代码初始化接口测试的固定装置。
type codeInitAPIFixture struct {
	handler http.Handler
	db      *sqlite.DB
	ctx     context.Context
	exec    *codeInitAPIExecutor
	task    domain.Task
	step    domain.Step
	attempt domain.Attempt
}

// newCodeInitAPIFixture 构造代码初始化接口测试数据。
func newCodeInitAPIFixture(t *testing.T) *codeInitAPIFixture {
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
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	now := time.Now().UTC()
	developer := apiRole(t, db, ctx, domain.RoleTypeDeveloper, "开发工程师", now)
	planner := apiRole(t, db, ctx, domain.RoleTypePlanner, "规划师", now)
	host := markAPIHostNormal(t, db, ctx, apiHost(t, db, ctx, now))
	requirement, err := domain.NewRequirement("需求标题", "需求正文", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "ssh://git.example.com/team/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &host.ID
	// 容器归属必须来自真实的容器登记记录：任务记录里的 container_id 只是普通
	// 文本列，执行链路会核验归属。
	seedOwnedContainer(t, db, ctx, task, "container-1", "docker-container-1")
	task.ContainerID = "container-1"
	task.Config = domain.TaskConfig{
		Goal:            "完成本次需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		ProjectID:       "demo",
		PlannerRoleID:   planner.ID,
		DeveloperRoleID: developer.ID,
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusPreparing, now); err != nil {
		t.Fatalf("任务迁移失败: %v", err)
	}
	if err := db.UpdateTaskIfStatus(ctx, task, []domain.TaskStatus{domain.TaskStatusQueued}); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	step, err := domain.NewAgentStep(
		task.ID,
		1,
		domain.StepTypeDevelop,
		"实现接口",
		"按需求实现接口",
		"",
		developer,
		domain.VerificationSpec{},
		domain.RetryConfig{MaxAttempts: 1},
		now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := db.CreateStep(ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	attempt, err := db.StartAttempt(ctx, storageStartAttempt(task.ID, step.ID))
	if err != nil {
		t.Fatalf("创建执行尝试失败: %v", err)
	}
	// 尝试级初始化必须排在任务级工作区初始化之后：两者共用同一个容器内工作目录。
	seedCodeInitAPIWorkspace(t, db, task, attempt, now)
	exec := &codeInitAPIExecutor{running: true, attemptID: attempt.ID}
	service, err := app.NewCodeInitializeService(app.CodeInitializeOptions{
		Store:      db,
		Containers: exec,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造代码初始化服务失败: %v", err)
	}
	tasks, err := app.NewTaskService(app.TaskServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	records, err := app.NewExecutionRecordService(db)
	if err != nil {
		t.Fatalf("构造执行记录服务失败: %v", err)
	}
	return &codeInitAPIFixture{
		handler: httpapi.NewHandler(httpapi.Deps{
			Health:           app.NewHealthService(db),
			SystemInfo:       func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
			Tasks:            tasks,
			ExecutionRecords: records,
			CodeInitialize:   service,
		}),
		db:      db,
		ctx:     ctx,
		exec:    exec,
		task:    task,
		step:    step,
		attempt: attempt,
	}
}

// initializePath 返回代码初始化接口路径。
func (f *codeInitAPIFixture) initializePath() string {
	return httpapi.PathTasks + "/" + f.task.ID.String() +
		"/steps/" + f.step.ID.String() +
		"/attempts/" + f.attempt.ID.String() + "/initialize-code"
}

// post 发送一次初始化请求。
func (f *codeInitAPIFixture) post(t *testing.T, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
		payload = encoded
	}
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder
}

// TestInitializeCodeReturnsRecord 验证成功初始化返回完整结果。
func TestInitializeCodeReturnsRecord(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitAPIFixture(t)
	recorder := fixture.post(t, fixture.initializePath(), httpapi.InitializeCodeRequest{})
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var body httpapi.InitializeCodeResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !body.Succeeded || body.Status != string(domain.CodeFetchStatusSucceeded) {
		t.Fatalf("响应状态不符: %+v", body)
	}
	if body.Commit != codeInitAPIHeadSHA {
		t.Fatalf("commit 不符: %s", body.Commit)
	}
	if body.SourceType != string(domain.CodeSourceGit) {
		t.Fatalf("获取方式不符: %s", body.SourceType)
	}
	if !body.Persisted {
		t.Fatalf("记录必须写入成功: %v", body.PersistenceErrors)
	}
	if body.WorkDir != domain.DefaultContainerWorkDir {
		t.Fatalf("目标目录不符: %s", body.WorkDir)
	}
}

// TestInitializeCodeRejectsGet 验证非 POST 请求返回 405。
func TestInitializeCodeRejectsGet(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitAPIFixture(t)
	request := httptest.NewRequest(http.MethodGet, fixture.initializePath(), nil)
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("期望 405，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestInitializeCodeReturnsNotFoundForUnknownTask 验证不存在的任务返回 404。
func TestInitializeCodeReturnsNotFoundForUnknownTask(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitAPIFixture(t)
	target := httpapi.PathTasks + "/" + domain.NewID().String() +
		"/steps/" + fixture.step.ID.String() +
		"/attempts/" + fixture.attempt.ID.String() + "/initialize-code"
	recorder := fixture.post(t, target, nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestInitializeCodeReturnsConflictWhenStepMismatched 验证步骤归属不符返回 404。
func TestInitializeCodeReturnsConflictWhenStepMismatched(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitAPIFixture(t)
	target := httpapi.PathTasks + "/" + fixture.task.ID.String() +
		"/steps/" + domain.NewID().String() +
		"/attempts/" + fixture.attempt.ID.String() + "/initialize-code"
	recorder := fixture.post(t, target, nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestInitializeCodeReturnsPreconditionWhenContainerMissing 验证缺少容器返回 412。
func TestInitializeCodeReturnsPreconditionWhenContainerMissing(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitAPIFixture(t)
	task, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	task.ContainerID = ""
	if err := fixture.db.UpdateTask(fixture.ctx, *task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	recorder := fixture.post(t, fixture.initializePath(), nil)
	if recorder.Code != http.StatusPreconditionFailed {
		t.Fatalf("期望 412，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if fixture.exec.calls != 0 {
		t.Fatal("前置条件未满足时不得执行代码获取")
	}
}

// TestInitializeCodeReturnsExecutionFailed 验证获取失败返回执行失败。
func TestInitializeCodeReturnsExecutionFailed(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitAPIFixture(t)
	fixture.exec.fetchErr = &source.Error{
		Code:   source.CodeRepositoryAccessFailed,
		Op:     "git clone",
		Detail: "无法访问仓库",
	}
	fixture.exec.fetched = source.FetchResult{
		Stage:        domain.CodeFetchStageClone,
		ErrorCode:    source.CodeRepositoryAccessFailed.String(),
		ErrorSummary: "无法访问仓库",
	}
	recorder := fixture.post(t, fixture.initializePath(), nil)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("期望 500，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var body httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if body.Error.Code != httpapi.CodeExecutionFailed {
		t.Fatalf("错误代码不符: %s", body.Error.Code)
	}
	// 失败也必须留下记录，且不能把执行事实伪装成成功。
	records, err := fixture.db.ListCodeFetchRecordsByAttempt(fixture.ctx, fixture.attempt.ID.String())
	if err != nil {
		t.Fatalf("读取代码初始化记录失败: %v", err)
	}
	if len(records) != 1 || records[0].Status != domain.CodeFetchStatusFailed {
		t.Fatalf("失败记录不符: %+v", records)
	}
}

// TestInitializeCodeRejectsInvalidTimeout 验证超时参数越界返回 400。
func TestInitializeCodeRejectsInvalidTimeout(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitAPIFixture(t)
	recorder := fixture.post(t, fixture.initializePath(), httpapi.InitializeCodeRequest{
		TimeoutSeconds: 7200,
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if fixture.exec.calls != 0 {
		t.Fatal("参数非法时不得执行代码获取")
	}
}

// TestInitializeCodeRejectsInvalidWorkDir 验证非法工作目录返回 400。
func TestInitializeCodeRejectsInvalidWorkDir(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitAPIFixture(t)
	recorder := fixture.post(t, fixture.initializePath(), httpapi.InitializeCodeRequest{
		WorkDir: "/root/project",
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestInitializeCodeRequiresService 验证未初始化服务时返回 503。
func TestInitializeCodeRequiresService(t *testing.T) {
	t.Parallel()
	handler := httpapi.NewHandler(httpapi.Deps{})
	request := httptest.NewRequest(
		http.MethodPost,
		httpapi.PathTasks+"/t1/steps/s1/attempts/a1/initialize-code",
		bytes.NewReader(nil),
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望 503，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestInitializeCodeUnknownPathIsNotFound 验证其他 attempts 形状仍按只读语义处理。
func TestInitializeCodeUnknownPathIsNotFound(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitAPIFixture(t)
	target := httpapi.PathTasks + "/" + fixture.task.ID.String() +
		"/steps/" + fixture.step.ID.String() +
		"/attempts/" + fixture.attempt.ID.String() + "/unknown"
	recorder := fixture.post(t, target, nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestInitializeCodeDoesNotLeakSecrets 验证响应不包含凭据与命令输出。
func TestInitializeCodeDoesNotLeakSecrets(t *testing.T) {
	t.Parallel()
	fixture := newCodeInitAPIFixture(t)
	recorder := fixture.post(t, fixture.initializePath(), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"password", "token", "PRIVATE KEY", "git clone"} {
		if bytes.Contains([]byte(body), []byte(forbidden)) {
			t.Fatalf("响应不得包含 %q: %s", forbidden, body)
		}
	}
}

// storageStartAttempt 构造一次创建执行尝试的请求。
func storageStartAttempt(taskID, stepID domain.ID) storage.StartAttemptRequest {
	return storage.StartAttemptRequest{TaskID: taskID, StepID: stepID, At: time.Now().UTC()}
}
