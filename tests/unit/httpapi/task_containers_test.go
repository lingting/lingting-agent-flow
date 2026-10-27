package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 任务容器接口的协议测试。
//
// 用例只验证路径分发、请求方法、错误映射与响应 DTO 形状：容器服务的远程能力由受控
// 替身提供，因此默认执行路径不访问真实 SSH、Docker 或 Pi。删除与脱离管理这两个真实
// 业务规则（所有权核验、状态机、远程副作用）由 tests/unit/domain 与
// tests/integration/local 覆盖，这里不重复。

// fakeDisposalContainers 是容器操作的受控替身。
//
// 它记录被调用的动作与请求，并按配置返回结果或错误；不接触任何远程连接。
type fakeDisposalContainers struct {
	deletes  []app.DeleteContainerRequest
	detaches []app.DetachContainerRequest
	// deleteDetail 是删除成功后返回的结果。
	deleteDetail app.ContainerDetail
	// detachDetail 是脱离管理成功后返回的结果。
	detachDetail app.ContainerDetail
	// deleteErr 使删除失败。
	deleteErr error
	// detachErr 使脱离管理失败。
	detachErr error
	// fallbackStore 用于把处置后的状态写回数据库，使"重新读取"读出真实记录。
	fallbackStore *sqlite.DB
}

// DeleteContainer 记录删除请求并按配置返回结果。
func (f *fakeDisposalContainers) DeleteContainer(
	ctx context.Context,
	request app.DeleteContainerRequest,
) (app.ContainerDetail, error) {
	f.deletes = append(f.deletes, request)
	if f.deleteErr != nil {
		return f.deleteDetail, f.deleteErr
	}
	f.persist(ctx, f.deleteDetail)
	return f.deleteDetail, nil
}

// DetachContainer 记录脱离管理请求并按配置返回结果。
func (f *fakeDisposalContainers) DetachContainer(
	ctx context.Context,
	request app.DetachContainerRequest,
) (app.ContainerDetail, error) {
	f.detaches = append(f.detaches, request)
	if f.detachErr != nil {
		return f.detachDetail, f.detachErr
	}
	f.persist(ctx, f.detachDetail)
	return f.detachDetail, nil
}

// persist 把替身返回的记录写回数据库。
//
// 处置服务在成功后会重新读取真实记录，替身必须让这次读取看到处置后的状态，
// 否则用例验证的就不是真实链路。
func (f *fakeDisposalContainers) persist(ctx context.Context, detail app.ContainerDetail) {
	if f.fallbackStore == nil || detail.Container.ID == "" {
		return
	}
	if err := f.fallbackStore.UpdateContainer(ctx, detail.Container); err != nil {
		panic("替身写回容器记录失败: " + err.Error())
	}
}

// containerDisposalFixture 是任务容器接口的测试环境。
type containerDisposalFixture struct {
	handler http.Handler
	db      *sqlite.DB
	ctx     context.Context
	task    domain.Task
	// otherTask 是另一个任务，用于验证跨任务操作被拒绝。
	otherTask domain.Task
	// container 是登记在 task 名下的运行中容器。
	container domain.Container
	// otherContainer 是登记在 otherTask 名下的容器。
	otherContainer domain.Container
	// historyContainer 是登记在 task 名下、但不是当前引用的历史容器。
	historyContainer domain.Container
	// removedContainer 是已删除的容器记录。
	removedContainer domain.Container
	containers       *fakeDisposalContainers
}

// newContainerDisposalFixture 准备任务与容器记录并构造处理器。
func newContainerDisposalFixture(t *testing.T) *containerDisposalFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.SQLitePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	now := time.Now().UTC()
	fixture := &containerDisposalFixture{db: db, ctx: ctx}
	developer := apiRole(t, db, ctx, domain.RoleTypeDeveloper, "容器处置开发角色", now)
	fixture.task = apiTaskWithStep(t, db, ctx, now, developer, &domain.Step{}, 1)
	fixture.otherTask = apiTaskWithStep(t, db, ctx, now, developer, &domain.Step{}, 1)

	fixture.container = seedOwnedContainer(t, db, ctx, fixture.task, "container-current", "docker-1")
	fixture.historyContainer = seedOwnedContainer(
		t,
		db,
		ctx,
		fixture.task,
		"container-history",
		"docker-2",
	)
	fixture.otherContainer = seedOwnedContainer(
		t,
		db,
		ctx,
		fixture.otherTask,
		"container-other",
		"docker-3",
	)
	fixture.removedContainer = seedRemovedContainer(t, db, ctx, fixture.task, "container-removed")
	// 任务当前引用的只有 container-current：另外两条是同一任务的历史代次。
	fixture.task.ContainerID = fixture.container.ID.String()
	if err := db.UpdateTask(ctx, fixture.task); err != nil {
		t.Fatalf("回填任务容器引用失败: %v", err)
	}

	fixture.containers = &fakeDisposalContainers{fallbackStore: db}
	fixture.handler = httpapi.NewHandler(httpapi.Deps{
		Health:         app.NewHealthService(db),
		SystemInfo:     func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		Tasks:          codeSaveTestTaskService(t, db),
		TaskContainers: containerDisposalService(t, db, fixture.containers),
	})
	return fixture
}

// seedRemovedContainer 写入一条已删除的容器记录。
func seedRemovedContainer(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	task domain.Task,
	containerID string,
) domain.Container {
	t.Helper()
	container := seedOwnedContainer(t, db, ctx, task, containerID, "docker-removed")
	if err := container.MarkRemoved(domain.ContainerEndReasonManual, time.Now().UTC()); err != nil {
		t.Fatalf("标记容器已删除失败: %v", err)
	}
	if err := db.UpdateContainer(ctx, container); err != nil {
		t.Fatalf("更新容器记录失败: %v", err)
	}
	return container
}

// containerDisposalService 构造任务容器处置服务。
func containerDisposalService(
	t *testing.T,
	db *sqlite.DB,
	port app.ContainerDisposalPort,
) *app.TaskContainerDisposalService {
	t.Helper()
	service, err := app.NewTaskContainerDisposalService(app.TaskContainerDisposalOptions{
		Store:      db,
		Containers: port,
	})
	if err != nil {
		t.Fatalf("构造任务容器处置服务失败: %v", err)
	}
	return service
}

// decodeResponse 把响应体解析到目标结构。
func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("解析响应失败: %v（响应体 %s）", err, recorder.Body.String())
	}
}

// TestTaskContainersListReturnsRealRecords 验证列表返回真实登记记录与关联方式。
func TestTaskContainersListReturnsRealRecords(t *testing.T) {
	t.Parallel()
	fixture := newContainerDisposalFixture(t)

	recorder := doRequest(
		t,
		fixture.handler,
		http.MethodGet,
		"/api/v1/tasks/"+fixture.task.ID.String()+"/containers",
		"",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.TaskContainerListResponse
	decodeResponse(t, recorder, &response)
	if response.Total != 3 {
		t.Fatalf("期望 3 条容器记录，实际 %d", response.Total)
	}
	origins := make(map[string]string, len(response.Records))
	for _, record := range response.Records {
		origins[record.ID] = record.Origin
		// 列表不得输出远程连接细节。
		if record.DockerState == "" {
			t.Fatalf("容器 %s 缺少 Docker 观测状态", record.ID)
		}
	}
	if origins[fixture.container.ID.String()] != string(app.ContainerOriginCurrent) {
		t.Fatalf("当前容器必须标记为 current，实际 %q", origins[fixture.container.ID.String()])
	}
	if origins[fixture.historyContainer.ID.String()] != string(app.ContainerOriginHistory) {
		t.Fatalf("历史容器必须标记为 history，实际 %q", origins[fixture.historyContainer.ID.String()])
	}
	if origins[fixture.removedContainer.ID.String()] != string(app.ContainerOriginHistory) {
		t.Fatalf("已删除的历史容器必须标记为 history")
	}
	// 其他任务的容器不得出现在列表里。
	for _, record := range response.Records {
		if record.ID == fixture.otherContainer.ID.String() {
			t.Fatal("列表不得包含其他任务的容器")
		}
	}
}

// TestTaskContainersListRejectsWrongMethod 验证方法不允许时返回 405。
func TestTaskContainersListRejectsWrongMethod(t *testing.T) {
	t.Parallel()
	fixture := newContainerDisposalFixture(t)

	recorder := doRequest(
		t,
		fixture.handler,
		http.MethodDelete,
		"/api/v1/tasks/"+fixture.task.ID.String()+"/containers",
		"",
	)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("期望 405，实际 %d", recorder.Code)
	}
	if allow := recorder.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("Allow 头不正确: %q", allow)
	}
}

// TestTaskContainersListUnknownTask 验证任务不存在时返回 404。
func TestTaskContainersListUnknownTask(t *testing.T) {
	t.Parallel()
	fixture := newContainerDisposalFixture(t)

	recorder := doRequest(t, fixture.handler, http.MethodGet, "/api/v1/tasks/task-missing/containers", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ErrorResponse
	decodeResponse(t, recorder, &response)
	if response.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误代码不正确: %s", response.Error.Code)
	}
}

// TestTaskContainersMissingServiceReportsUnavailable 验证缺少服务时返回 503。
func TestTaskContainersMissingServiceReportsUnavailable(t *testing.T) {
	t.Parallel()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.SQLitePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	handler := httpapi.NewHandler(httpapi.Deps{
		Health:     app.NewHealthService(db),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
	})

	for _, path := range []string{
		"/api/v1/tasks/task-1/containers",
		"/api/v1/tasks/task-1/containers/container-1/delete",
		"/api/v1/tasks/task-1/containers/container-1/detach",
	} {
		recorder := doRequest(t, handler, http.MethodPost, path, "")
		if path == "/api/v1/tasks/task-1/containers" {
			recorder = doRequest(t, handler, http.MethodGet, path, "")
		}
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s 期望 503，实际 %d：%s", path, recorder.Code, recorder.Body.String())
		}
	}
}

// TestTaskContainerDeleteSucceeds 验证删除成功路径与响应语义。
func TestTaskContainerDeleteSucceeds(t *testing.T) {
	t.Parallel()
	fixture := newContainerDisposalFixture(t)
	removed := fixture.container
	if err := removed.MarkRemoved(domain.ContainerEndReasonManual, time.Now().UTC()); err != nil {
		t.Fatalf("构造删除后的容器记录失败: %v", err)
	}
	fixture.containers.deleteDetail = app.ContainerDetail{Container: removed}

	recorder := doRequest(
		t,
		fixture.handler,
		http.MethodPost,
		"/api/v1/tasks/"+fixture.task.ID.String()+"/containers/"+
			fixture.container.ID.String()+"/delete",
		"",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ContainerDisposalResponse
	decodeResponse(t, recorder, &response)
	if response.Action != string(app.ContainerDisposalDelete) {
		t.Fatalf("action 必须是 delete，实际 %q", response.Action)
	}
	if response.Outcome != string(app.ContainerDisposalCompleted) {
		t.Fatalf("outcome 必须是 completed，实际 %q", response.Outcome)
	}
	if response.Container.State != string(domain.ContainerStateRemoved) {
		t.Fatalf("响应必须描述处置后的真实状态，实际 %q", response.Container.State)
	}
	if response.Container.Detached {
		t.Fatal("删除不得被报告为已脱离管理")
	}
	if len(fixture.containers.deletes) != 1 {
		t.Fatalf("应只调用一次删除，实际 %d 次", len(fixture.containers.deletes))
	}
	if fixture.containers.deletes[0].Reason != domain.ContainerEndReasonManual {
		t.Fatalf("删除原因应为 manual，实际 %q", fixture.containers.deletes[0].Reason)
	}
	if len(fixture.containers.detaches) != 0 {
		t.Fatal("删除动作不得调用脱离管理")
	}
}

// TestTaskContainerDetachSucceeds 验证脱离管理成功路径与响应语义。
func TestTaskContainerDetachSucceeds(t *testing.T) {
	t.Parallel()
	fixture := newContainerDisposalFixture(t)
	detached := fixture.container
	if err := detached.Detach(time.Now().UTC()); err != nil {
		t.Fatalf("构造已脱离的容器记录失败: %v", err)
	}
	fixture.containers.detachDetail = app.ContainerDetail{Container: detached}

	recorder := doRequest(
		t,
		fixture.handler,
		http.MethodPost,
		"/api/v1/tasks/"+fixture.task.ID.String()+"/containers/"+
			fixture.container.ID.String()+"/detach",
		"",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ContainerDisposalResponse
	decodeResponse(t, recorder, &response)
	if response.Action != string(app.ContainerDisposalDetach) {
		t.Fatalf("action 必须是 detach，实际 %q", response.Action)
	}
	if response.Outcome != string(app.ContainerDisposalCompleted) {
		t.Fatalf("outcome 必须是 completed，实际 %q", response.Outcome)
	}
	if !response.Container.Detached {
		t.Fatal("脱离管理后必须标记 detached")
	}
	if response.Container.State != string(domain.ContainerStateDetached) {
		t.Fatalf("脱离管理后状态必须是 detached，实际 %q", response.Container.State)
	}
	if response.Container.EndReason != string(domain.ContainerEndReasonDetached) {
		t.Fatalf("结束原因必须是 detached，实际 %q", response.Container.EndReason)
	}
	if len(fixture.containers.deletes) != 0 {
		t.Fatal("脱离管理不得调用删除")
	}
	if len(fixture.containers.detaches) != 1 {
		t.Fatalf("应只调用一次脱离管理，实际 %d 次", len(fixture.containers.detaches))
	}
}

// TestTaskContainerDisposalRejectsCrossTask 验证跨任务操作被拒绝且不产生远程副作用。
func TestTaskContainerDisposalRejectsCrossTask(t *testing.T) {
	t.Parallel()
	fixture := newContainerDisposalFixture(t)

	for _, action := range []string{"delete", "detach"} {
		recorder := doRequest(
			t,
			fixture.handler,
			http.MethodPost,
			"/api/v1/tasks/"+fixture.task.ID.String()+"/containers/"+
				fixture.otherContainer.ID.String()+"/"+action,
			"",
		)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("%s 跨任务操作期望 409，实际 %d：%s", action, recorder.Code, recorder.Body.String())
		}
		var response httpapi.ErrorResponse
		decodeResponse(t, recorder, &response)
		if response.Error.Code != httpapi.CodeConflict {
			t.Fatalf("错误代码不正确: %s", response.Error.Code)
		}
		if !strings.Contains(response.Error.Message, "归属") {
			t.Fatalf("错误信息必须指出归属问题: %s", response.Error.Message)
		}
	}
	if len(fixture.containers.deletes) != 0 || len(fixture.containers.detaches) != 0 {
		t.Fatal("归属校验失败时不得执行任何破坏性操作")
	}
}

// TestTaskContainerDisposalRejectsUnknownRecords 验证记录不存在与非法标识的映射。
func TestTaskContainerDisposalRejectsUnknownRecords(t *testing.T) {
	t.Parallel()
	fixture := newContainerDisposalFixture(t)

	missing := doRequest(
		t,
		fixture.handler,
		http.MethodPost,
		"/api/v1/tasks/"+fixture.task.ID.String()+"/containers/container-missing/delete",
		"",
	)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("记录不存在期望 404，实际 %d：%s", missing.Code, missing.Body.String())
	}

	unknownTask := doRequest(
		t,
		fixture.handler,
		http.MethodPost,
		"/api/v1/tasks/task-missing/containers/"+fixture.container.ID.String()+"/delete",
		"",
	)
	if unknownTask.Code != http.StatusNotFound {
		t.Fatalf("任务不存在期望 404，实际 %d", unknownTask.Code)
	}

	badAction := doRequest(
		t,
		fixture.handler,
		http.MethodPost,
		"/api/v1/tasks/"+fixture.task.ID.String()+"/containers/"+
			fixture.container.ID.String()+"/stop",
		"",
	)
	// stop 不是本接口的动作：路径不成立，按未知接口处理，不得落到删除或脱离管理。
	if badAction.Code != http.StatusNotFound {
		t.Fatalf("未知动作期望 404，实际 %d：%s", badAction.Code, badAction.Body.String())
	}
	if len(fixture.containers.deletes) != 0 || len(fixture.containers.detaches) != 0 {
		t.Fatal("非法请求不得触发任何处置动作")
	}
}

// TestTaskContainerDetachRejectsRemovedContainer 验证已删除的容器不能被报告为成功脱离。
func TestTaskContainerDetachRejectsRemovedContainer(t *testing.T) {
	t.Parallel()
	fixture := newContainerDisposalFixture(t)

	recorder := doRequest(
		t,
		fixture.handler,
		http.MethodPost,
		"/api/v1/tasks/"+fixture.task.ID.String()+"/containers/"+
			fixture.removedContainer.ID.String()+"/detach",
		"",
	)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("期望 409，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if len(fixture.containers.detaches) != 0 {
		t.Fatal("已删除的容器不得执行脱离管理")
	}
}

// TestTaskContainerDeleteIsIdempotentOnRemovedRecord 验证重复删除走容器服务的幂等路径。
func TestTaskContainerDeleteIsIdempotentOnRemovedRecord(t *testing.T) {
	t.Parallel()
	fixture := newContainerDisposalFixture(t)
	fixture.containers.deleteDetail = app.ContainerDetail{Container: fixture.removedContainer}

	recorder := doRequest(
		t,
		fixture.handler,
		http.MethodPost,
		"/api/v1/tasks/"+fixture.task.ID.String()+"/containers/"+
			fixture.removedContainer.ID.String()+"/delete",
		"",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if len(fixture.containers.deletes) != 1 {
		t.Fatalf("重复删除应交给容器服务判定幂等，实际调用 %d 次", len(fixture.containers.deletes))
	}
}

// TestTaskContainerDisposalFailures 验证三类失败的映射与响应形状。
func TestTaskContainerDisposalFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{
			name:   "远程操作失败",
			err:    errors.New("容器服务不可用"),
			status: http.StatusInternalServerError,
			code:   httpapi.CodeExecutionFailed,
		},
		{
			name:   "记录持久化失败",
			err:    errors.New("更新容器登记记录失败"),
			status: http.StatusInternalServerError,
			code:   httpapi.CodeExecutionFailed,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newContainerDisposalFixture(t)
			fixture.containers.detachErr = testCase.err
			fixture.containers.detachDetail = app.ContainerDetail{Container: fixture.container}

			recorder := doRequest(
				t,
				fixture.handler,
				http.MethodPost,
				"/api/v1/tasks/"+fixture.task.ID.String()+"/containers/"+
					fixture.container.ID.String()+"/detach",
				"",
			)
			if recorder.Code != testCase.status {
				t.Fatalf("期望 %d，实际 %d：%s", testCase.status, recorder.Code, recorder.Body.String())
			}
			var response httpapi.ErrorResponse
			decodeResponse(t, recorder, &response)
			if response.Error.Code != testCase.code {
				t.Fatalf("错误代码期望 %s，实际 %s", testCase.code, response.Error.Code)
			}
		})
	}
}

// TestTaskContainerDisposalUncertainOutcome 验证结果不确定时不被报告为成功。
func TestTaskContainerDisposalUncertainOutcome(t *testing.T) {
	t.Parallel()
	fixture := newContainerDisposalFixture(t)
	// 容器服务报告成功但记录既没有删除也没有脱离：不得报告为已完成。
	fixture.containers.detachDetail = app.ContainerDetail{Container: fixture.container}

	recorder := doRequest(
		t,
		fixture.handler,
		http.MethodPost,
		"/api/v1/tasks/"+fixture.task.ID.String()+"/containers/"+
			fixture.container.ID.String()+"/detach",
		"",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ContainerDisposalResponse
	decodeResponse(t, recorder, &response)
	if response.Outcome == string(app.ContainerDisposalCompleted) {
		t.Fatal("记录未真正脱离管理时不得报告已完成")
	}
	if response.Outcome != string(app.ContainerDisposalUncertain) {
		t.Fatalf("期望 uncertain，实际 %q", response.Outcome)
	}
}

// TestTaskContainerDisposalMethodNotAllowed 验证动作只接受 POST。
func TestTaskContainerDisposalMethodNotAllowed(t *testing.T) {
	t.Parallel()
	fixture := newContainerDisposalFixture(t)

	recorder := doRequest(
		t,
		fixture.handler,
		http.MethodGet,
		"/api/v1/tasks/"+fixture.task.ID.String()+"/containers/"+
			fixture.container.ID.String()+"/delete",
		"",
	)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("期望 405，实际 %d", recorder.Code)
	}
	if allow := recorder.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow 头不正确: %q", allow)
	}
}

// TestTaskContainerResponseHidesInternalDetails 验证响应不泄露不必要的远程信息。
func TestTaskContainerResponseHidesInternalDetails(t *testing.T) {
	t.Parallel()
	fixture := newContainerDisposalFixture(t)

	recorder := doRequest(
		t,
		fixture.handler,
		http.MethodGet,
		"/api/v1/tasks/"+fixture.task.ID.String()+"/containers",
		"",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{
		"docker-1",
		"image_id",
		"build_config_sha256",
		"workspace",
		"private_key",
		"host_id",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("响应不得包含 %q：%s", forbidden, body)
		}
	}
	var raw map[string]any
	decodeResponse(t, recorder, &raw)
	if _, ok := raw["records"]; !ok {
		t.Fatal("列表响应必须包含 records")
	}
	if _, ok := raw["total"]; !ok {
		t.Fatal("列表响应必须包含 total")
	}
}
