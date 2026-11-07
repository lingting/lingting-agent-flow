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
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 规划接口的协议测试只覆盖协议转换与前置检查：成功路径上的远端核验由真实环境验收
// 用例覆盖，这里不伪造任何远程事实。

// planningAPIRemote 是规划运行时远程端口的受控替身。
//
// 所有方法都记录调用次数：未绑定执行节点的规划会话不得触发任何远程动作，用例据此
// 断言"没有现场就不去碰远程"。
type planningAPIRemote struct {
	calls int
}

// InspectManagedContainer 记录一次本不该发生的远程核验。
func (r *planningAPIRemote) InspectManagedContainer(
	ctx context.Context,
	containerID string,
) (app.ContainerDetail, error) {
	r.calls++
	return app.ContainerDetail{}, errPlanningAPIRemoteUnexpected
}

// DeleteManagedContainer 记录一次本不该发生的远程删除。
func (r *planningAPIRemote) DeleteManagedContainer(
	ctx context.Context,
	containerID string,
) (app.ContainerDetail, error) {
	r.calls++
	return app.ContainerDetail{}, errPlanningAPIRemoteUnexpected
}

// InspectPrepContainer 记录一次本不该发生的一次性准备容器核验。
func (r *planningAPIRemote) InspectPrepContainer(
	ctx context.Context,
	hostID domain.ID,
	name string,
) (docker.ContainerInspect, bool, error) {
	r.calls++
	return docker.ContainerInspect{}, false, errPlanningAPIRemoteUnexpected
}

// RemovePrepContainer 记录一次本不该发生的一次性准备容器删除。
func (r *planningAPIRemote) RemovePrepContainer(
	ctx context.Context,
	hostID domain.ID,
	dockerID string,
) (bool, error) {
	r.calls++
	return false, errPlanningAPIRemoteUnexpected
}

// InspectSnapshotVolume 记录一次本不该发生的快照卷核验。
func (r *planningAPIRemote) InspectSnapshotVolume(
	ctx context.Context,
	hostID domain.ID,
	volumeName string,
) (docker.VolumeInspect, bool, error) {
	r.calls++
	return docker.VolumeInspect{}, false, errPlanningAPIRemoteUnexpected
}

// RemoveSnapshotVolume 记录一次本不该发生的快照卷删除。
func (r *planningAPIRemote) RemoveSnapshotVolume(
	ctx context.Context,
	hostID domain.ID,
	volumeName string,
) (bool, error) {
	r.calls++
	return false, errPlanningAPIRemoteUnexpected
}

// VerifySnapshotCommit 记录一次本不该发生的快照 commit 核验。
func (r *planningAPIRemote) VerifySnapshotCommit(
	ctx context.Context,
	target app.PlanningRuntimeSnapshotTarget,
) (string, error) {
	r.calls++
	return "", errPlanningAPIRemoteUnexpected
}

// errPlanningAPIRemoteUnexpected 表示未绑定执行节点的会话触发了远程动作。
var errPlanningAPIRemoteUnexpected = errPlanningAPI("规划接口在未绑定执行节点时触发了远程动作")

// errPlanningAPI 是规划接口测试使用的固定错误。
type errPlanningAPI string

// Error 实现 error 接口。
func (e errPlanningAPI) Error() string {
	return string(e)
}

// planningAPIFixture 是一套已装配规划接口的测试现场。
type planningAPIFixture struct {
	db       *sqlite.DB
	ctx      context.Context
	project  domain.Project
	sessions *app.PlanningSessionService
	remote   *planningAPIRemote
	// handler 已装配规划服务。
	handler http.Handler
	// bare 是未装配任何规划服务的处理器，用于核验 503。
	bare http.Handler
}

// newPlanningAPIFixture 打开测试数据库并装配规划接口处理器。
func newPlanningAPIFixture(t *testing.T) *planningAPIFixture {
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
	sessions, err := app.NewPlanningSessionService(app.PlanningSessionServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造规划会话服务失败: %v", err)
	}
	remote := &planningAPIRemote{}
	runtime, err := app.NewPlanningRuntimeService(app.PlanningRuntimeServiceOptions{
		Store:        db,
		Coordination: app.NewPlanningSessionCoordinator(),
		RemotePort:   remote,
	})
	if err != nil {
		t.Fatalf("构造规划运行时服务失败: %v", err)
	}
	return &planningAPIFixture{
		db:       db,
		ctx:      ctx,
		project:  testkit.CreateProject(t, db, testkit.DefaultTestProjectID, time.Now().UTC()),
		sessions: sessions,
		remote:   remote,
		handler: httpapi.NewHandler(httpapi.Deps{
			PlanningSessions: sessions,
			PlanningRuntime:  runtime,
			Logger:           logger.Slog(),
		}),
		bare: httpapi.NewHandler(httpapi.Deps{Logger: logger.Slog()}),
	}
}

// newSession 创建一个未绑定执行节点的规划会话。
func (f *planningAPIFixture) newSession(t *testing.T) domain.PlanningSession {
	t.Helper()
	session, err := f.sessions.Create(f.ctx, app.CreatePlanningSessionRequest{ProjectID: f.project.ID})
	if err != nil {
		t.Fatalf("创建规划会话失败: %v", err)
	}
	return session
}

// send 发送一次请求并返回响应记录器。
func (f *planningAPIFixture) send(
	t *testing.T,
	handler http.Handler,
	method, target string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else if raw, ok := body.(string); ok {
		reader = bytes.NewReader([]byte(raw))
	} else {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(payload)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// errorCode 解析统一错误响应中的 code。
func planningErrorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var payload httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析错误响应失败: %v，响应体 %s", err, recorder.Body.String())
	}
	return payload.Error.Code
}

// TestPlanningAPISessionDetail 验收规划会话只读查询的字段映射。
func TestPlanningAPISessionDetail(t *testing.T) {
	f := newPlanningAPIFixture(t)
	session := f.newSession(t)
	recorder := f.send(t, f.handler, http.MethodGet,
		httpapi.PathPlanningSessions+"/"+session.ID.String(), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("读取规划会话应返回 200，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	var payload httpapi.PlanningSessionDetailResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析规划会话响应失败: %v", err)
	}
	if payload.Session.ID != session.ID.String() {
		t.Fatalf("会话标识不一致: 期望 %s，实际 %s", session.ID, payload.Session.ID)
	}
	if payload.Session.ProjectID != f.project.ID {
		t.Fatalf("项目标识不一致: 期望 %s，实际 %s", f.project.ID, payload.Session.ProjectID)
	}
	if payload.Session.Status != string(domain.PlanningSessionActive) {
		t.Fatalf("会话状态不一致: 期望 active，实际 %s", payload.Session.Status)
	}
	// 尚未准备运行环境：绑定字段必须缺省，而不是被填成空字符串占位值。
	if payload.Session.HostID != "" || payload.Session.ContainerID != "" ||
		payload.Session.SourceCommit != "" {
		t.Fatalf("未准备运行环境的会话不应带绑定字段: %+v", payload.Session)
	}
}

// TestPlanningAPISessionDetailNotFound 验收未知会话返回 404 而不是空会话。
func TestPlanningAPISessionDetailNotFound(t *testing.T) {
	f := newPlanningAPIFixture(t)
	recorder := f.send(t, f.handler, http.MethodGet,
		httpapi.PathPlanningSessions+"/"+domain.NewID().String(), nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("未知会话应返回 404，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	if code := planningErrorCode(t, recorder); code != httpapi.CodeNotFound {
		t.Fatalf("未知会话的错误代码应为 %s，实际 %s", httpapi.CodeNotFound, code)
	}
}

// TestPlanningAPIRuntimeInspectionWithoutBinding 验收未绑定节点的会话不会被当成现场冲突。
//
// 准备流程在创建任何远程资源之前先落盘执行节点，因此未绑定节点的会话不可能拥有远程
// 资源；这种情况必须按数据库事实报告 absent，且不建立任何远程连接。
func TestPlanningAPIRuntimeInspectionWithoutBinding(t *testing.T) {
	f := newPlanningAPIFixture(t)
	session := f.newSession(t)
	recorder := f.send(t, f.handler, http.MethodGet,
		httpapi.PathPlanningSessions+"/"+session.ID.String()+"/runtime", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("未绑定节点的会话核验应返回 200，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	var payload httpapi.PlanningRuntimeInspectionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析现场核验响应失败: %v", err)
	}
	for label, presence := range map[string]string{
		"规划容器": payload.Container.Presence,
		"准备容器": payload.PrepContainer.Presence,
		"快照卷":  payload.Snapshot.Presence,
	} {
		if presence != string(app.PlanningRuntimePresenceAbsent) {
			t.Fatalf("%s 的存在性应为 absent，实际 %s", label, presence)
		}
	}
	if payload.Container.Running || payload.Snapshot.CommitVerified {
		t.Fatalf("未绑定节点时不应出现运行中或已核验结论: %+v", payload)
	}
	if f.remote.calls != 0 {
		t.Fatalf("未绑定节点的核验不得触发远程动作，实际调用 %d 次", f.remote.calls)
	}
}

// TestPlanningAPIPrepareRequiresBoundHost 验收未绑定节点且未显式指定节点时的参数错误。
func TestPlanningAPIPrepareRequiresBoundHost(t *testing.T) {
	f := newPlanningAPIFixture(t)
	session := f.newSession(t)
	recorder := f.send(t, f.handler, http.MethodPost,
		httpapi.PathPlanningSessions+"/"+session.ID.String()+"/runtime/prepare", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("未绑定节点且未指定节点应返回 400，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	if code := planningErrorCode(t, recorder); code != httpapi.CodeInvalidRequest {
		t.Fatalf("错误代码应为 %s，实际 %s", httpapi.CodeInvalidRequest, code)
	}
	if f.remote.calls != 0 {
		t.Fatalf("参数错误不得触发远程动作，实际调用 %d 次", f.remote.calls)
	}
}

// TestPlanningAPIPrepareWithoutContainerService 验收缺少容器能力时准备返回 412。
//
// 规划运行时只有远程端口、没有容器服务时无法创建或复用容器：这是前置条件缺失，
// 不是参数错误，也不能退化为一次不落地的"准备成功"。
func TestPlanningAPIPrepareWithoutContainerService(t *testing.T) {
	f := newPlanningAPIFixture(t)
	session := f.newSession(t)
	recorder := f.send(t, f.handler, http.MethodPost,
		httpapi.PathPlanningSessions+"/"+session.ID.String()+"/runtime/prepare",
		map[string]string{"host_id": "missing-host"})
	if recorder.Code != http.StatusPreconditionFailed {
		t.Fatalf("缺少容器能力应返回 412，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	if code := planningErrorCode(t, recorder); code != httpapi.CodePreconditionFailed {
		t.Fatalf("错误代码应为 %s，实际 %s", httpapi.CodePreconditionFailed, code)
	}
	if f.remote.calls != 0 {
		t.Fatalf("前置条件缺失不得触发远程动作，实际调用 %d 次", f.remote.calls)
	}
}

// TestPlanningAPIReleaseOnUnboundSession 验收释放未绑定会话按幂等完成处理。
func TestPlanningAPIReleaseOnUnboundSession(t *testing.T) {
	f := newPlanningAPIFixture(t)
	session := f.newSession(t)
	recorder := f.send(t, f.handler, http.MethodPost,
		httpapi.PathPlanningSessions+"/"+session.ID.String()+"/runtime/release", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("释放未绑定会话应返回 200，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	var payload httpapi.PlanningRuntimeReleaseResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析释放响应失败: %v", err)
	}
	if payload.ContainerReleased || payload.SnapshotReleased {
		t.Fatalf("未绑定会话不应报告已释放资源: %+v", payload)
	}
	if f.remote.calls != 0 {
		t.Fatalf("释放未绑定会话不得触发远程动作，实际调用 %d 次", f.remote.calls)
	}
}

// TestPlanningAPIWithoutServices 验收规划服务未装配时四个接口都返回 503。
func TestPlanningAPIWithoutServices(t *testing.T) {
	f := newPlanningAPIFixture(t)
	sessionID := domain.NewID().String()
	paths := []struct {
		method string
		target string
	}{
		{http.MethodGet, httpapi.PathPlanningSessions + "/" + sessionID},
		{http.MethodGet, httpapi.PathPlanningSessions + "/" + sessionID + "/runtime"},
		{http.MethodPost, httpapi.PathPlanningSessions + "/" + sessionID + "/runtime/prepare"},
		{http.MethodPost, httpapi.PathPlanningSessions + "/" + sessionID + "/runtime/release"},
	}
	for _, entry := range paths {
		recorder := f.send(t, f.bare, entry.method, entry.target, nil)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s 应返回 503，实际 %d，响应体 %s",
				entry.method, entry.target, recorder.Code, recorder.Body.String())
		}
	}
}

// TestPlanningAPIMethodNotAllowed 验收规划会话路径只接受 GET。
func TestPlanningAPIMethodNotAllowed(t *testing.T) {
	f := newPlanningAPIFixture(t)
	session := f.newSession(t)
	recorder := f.send(t, f.handler, http.MethodPut,
		httpapi.PathPlanningSessions+"/"+session.ID.String(), nil)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT 规划会话应返回 405，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	if code := planningErrorCode(t, recorder); code != httpapi.CodeMethodNotAllowed {
		t.Fatalf("错误代码应为 %s，实际 %s", httpapi.CodeMethodNotAllowed, code)
	}
}
