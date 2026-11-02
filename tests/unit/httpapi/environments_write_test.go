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
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// fakeEnvironmentWriter 是环境同步写路径的受控替身。
//
// 它不建立 SSH 连接、不读写任何远程目录：测试关注的是 HTTP 层的协议转换与错误映射，
// 真正的远程副作用由 app.EnvironmentService 承担，不属于本文件的验证范围。
type fakeEnvironmentWriter struct {
	initFunc       func(ctx context.Context, options app.InitOptions) (app.InitResult, error)
	createFunc     func(ctx context.Context, request app.CreateVersionRequest) (domain.EnvironmentVersion, error)
	dockerfileFunc func(ctx context.Context, request app.ReadDockerfileRequest) (app.DockerfileContent, error)
	logFunc        func(ctx context.Context, request app.ReadBuildLogRequest) (app.BuildLog, error)
	// seen 记录最近一次收到的应用层请求，用于断言 HTTP 层没有擅自改写参数。
	seenBuildLog   app.ReadBuildLogRequest
	seenDockerfile app.ReadDockerfileRequest
}

// Init 实现 httpapi.EnvironmentWriter。
func (f *fakeEnvironmentWriter) Init(
	ctx context.Context,
	options app.InitOptions,
) (app.InitResult, error) {
	if f.initFunc == nil {
		return app.InitResult{}, errors.New("测试替身未实现 Init")
	}
	return f.initFunc(ctx, options)
}

// CreateVersion 实现 httpapi.EnvironmentWriter。
func (f *fakeEnvironmentWriter) CreateVersion(
	ctx context.Context,
	request app.CreateVersionRequest,
) (domain.EnvironmentVersion, error) {
	if f.createFunc == nil {
		return domain.EnvironmentVersion{}, errors.New("测试替身未实现 CreateVersion")
	}
	return f.createFunc(ctx, request)
}

// ReadDockerfile 实现 httpapi.EnvironmentWriter。
func (f *fakeEnvironmentWriter) ReadDockerfile(
	ctx context.Context,
	request app.ReadDockerfileRequest,
) (app.DockerfileContent, error) {
	f.seenDockerfile = request
	if f.dockerfileFunc == nil {
		return app.DockerfileContent{}, errors.New("测试替身未实现 ReadDockerfile")
	}
	return f.dockerfileFunc(ctx, request)
}

// ReadBuildLog 实现 httpapi.EnvironmentWriter。
func (f *fakeEnvironmentWriter) ReadBuildLog(
	ctx context.Context,
	request app.ReadBuildLogRequest,
) (app.BuildLog, error) {
	f.seenBuildLog = request
	if f.logFunc == nil {
		return app.BuildLog{}, errors.New("测试替身未实现 ReadBuildLog")
	}
	return f.logFunc(ctx, request)
}

// fakeEnvironmentBuilder 是后台构建入口的受控替身。
type fakeEnvironmentBuilder struct {
	submitFunc func(ctx context.Context, request app.BuildRequest) (app.EnvironmentBuildTask, error)
	statusFunc func(
		hostID domain.ID,
		ref domain.EnvironmentRef,
	) (app.EnvironmentBuildTask, bool)
	seen app.BuildRequest
}

// Submit 实现 httpapi.EnvironmentBuilder。
func (f *fakeEnvironmentBuilder) Submit(
	ctx context.Context,
	request app.BuildRequest,
) (app.EnvironmentBuildTask, error) {
	f.seen = request
	if f.submitFunc == nil {
		return app.EnvironmentBuildTask{}, errors.New("测试替身未实现 Submit")
	}
	return f.submitFunc(ctx, request)
}

// Status 实现 httpapi.EnvironmentBuilder。
func (f *fakeEnvironmentBuilder) Status(
	hostID domain.ID,
	ref domain.EnvironmentRef,
) (app.EnvironmentBuildTask, bool) {
	if f.statusFunc == nil {
		return app.EnvironmentBuildTask{}, false
	}
	return f.statusFunc(hostID, ref)
}

// environmentWriteFixture 是环境写接口测试的运行环境。
type environmentWriteFixture struct {
	handler     http.Handler
	db          *sqlite.DB
	writer      *fakeEnvironmentWriter
	builder     *fakeEnvironmentBuilder
	host        domain.ExecutionHost
	baseRef     domain.EnvironmentRef
	languageRef domain.EnvironmentRef
}

// newEnvironmentWriteFixture 打开真实数据库、写入执行节点并构造处理器。
func newEnvironmentWriteFixture(t *testing.T) *environmentWriteFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: testkit.Dir(t) + "/agentflow.db"})
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
	queries, err := app.NewEnvironmentQueryService(db)
	if err != nil {
		t.Fatalf("构造环境查询服务失败: %v", err)
	}
	writer := &fakeEnvironmentWriter{}
	builder := &fakeEnvironmentBuilder{}
	baseRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	languageRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	return &environmentWriteFixture{
		baseRef:     baseRef,
		builder:     builder,
		db:          db,
		handler:     httpapi.NewHandler(httpapi.Deps{Environments: queries, EnvironmentWrites: writer, EnvironmentBuilds: builder, Logger: logger.Slog()}),
		host:        writeEnvironmentHost(t, ctx, db),
		languageRef: languageRef,
		writer:      writer,
	}
}

// doJSON 发起一次请求并返回状态码与响应体。
func (f *environmentWriteFixture) doJSON(
	t *testing.T,
	method string,
	target string,
	body string,
) (int, string) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

// errorCode 解析统一错误响应中的错误码。
func errorCode(t *testing.T, body string) string {
	t.Helper()
	var payload httpapi.ErrorResponse
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("解析错误响应失败: %v，响应: %s", err, body)
	}
	return payload.Error.Code
}

// TestCreateVersionRejectsInvalidRequests 覆盖创建版本接口的参数校验。
//
// 全部失败都必须在协议层完成：非法 Dockerfile、缺失节点标识与未知字段都不应进入
// 应用层，更不应该返回 2xx。
func TestCreateVersionRejectsInvalidRequests(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentWriteFixture(t)
	target := httpapi.PathEnvironments + "/language/go/versions"
	cases := []struct {
		name   string
		target string
		body   string
		status int
		method string
	}{
		{
			name:   "缺少 host_id",
			target: target,
			body:   `{"dockerfile":"FROM debian:12.7-slim\n"}`,
			status: http.StatusBadRequest,
			method: http.MethodPost,
		},
		{
			name:   "Dockerfile 缺少 FROM",
			target: target,
			body:   `{"host_id":"` + fixture.host.ID.String() + `","dockerfile":"RUN echo hi\n"}`,
			status: http.StatusBadRequest,
			method: http.MethodPost,
		},
		{
			name:   "Dockerfile 使用 latest 标签",
			target: target,
			body:   `{"host_id":"` + fixture.host.ID.String() + `","dockerfile":"FROM debian:latest\n"}`,
			status: http.StatusBadRequest,
			method: http.MethodPost,
		},
		{
			name:   "未知字段",
			target: target,
			body:   `{"host_id":"` + fixture.host.ID.String() + `","dockerfile":"FROM debian:12.7-slim\n","checkout":"/tmp"}`,
			status: http.StatusBadRequest,
			method: http.MethodPost,
		},
		{
			name:   "环境类型非法",
			target: httpapi.PathEnvironments + "/python/go/versions",
			body:   `{"host_id":"` + fixture.host.ID.String() + `","dockerfile":"FROM debian:12.7-slim\n"}`,
			status: http.StatusBadRequest,
			method: http.MethodPost,
		},
		{
			name: "环境名称非法",
			// 环境名称只允许小写字母、数字与连字符；下划线在规范化后仍然是非法字符。
			target: httpapi.PathEnvironments + "/language/Go_Lang/versions",
			body:   `{"host_id":"` + fixture.host.ID.String() + `","dockerfile":"FROM debian:12.7-slim\n"}`,
			status: http.StatusBadRequest,
			method: http.MethodPost,
		},
		{
			name:   "方法不允许",
			target: target,
			body:   "",
			status: http.StatusMethodNotAllowed,
			method: http.MethodGet,
		},
		{
			name:   "路径形状不存在",
			target: httpapi.PathEnvironments + "/language/go/unknown",
			body:   `{}`,
			status: http.StatusNotFound,
			method: http.MethodPost,
		},
	}
	for _, testCase := range cases {
		status, body := fixture.doJSON(t, testCase.method, testCase.target, testCase.body)
		if status != testCase.status {
			t.Fatalf("%s: 期望 %d，实际 %d，响应: %s", testCase.name, testCase.status, status, body)
		}
	}
}

// TestCreateVersionMapsServiceErrors 覆盖创建版本接口的错误类别映射。
//
// 映射必须依据错误类别：参数错误 400、目标不存在 404、冲突 409、节点未通过预检 412。
func TestCreateVersionMapsServiceErrors(t *testing.T) {
	t.Parallel()
	target := httpapi.PathEnvironments + "/language/go/versions"
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{
			name:   "节点不存在",
			err:    app.ErrEnvironmentHostNotFound,
			status: http.StatusNotFound,
			code:   httpapi.CodeNotFound,
		},
		{
			name:   "版本写入冲突",
			err:    storage.ErrConflict,
			status: http.StatusConflict,
			code:   httpapi.CodeConflict,
		},
		{
			name:   "节点未通过预检",
			err:    app.ErrHostValidationUnavailable,
			status: http.StatusPreconditionFailed,
			code:   httpapi.CodePreconditionFailed,
		},
		{
			name:   "父基础版本不可用",
			err:    errors.New("父基础环境版本尚未构建成功"),
			status: http.StatusInternalServerError,
			code:   httpapi.CodeInternal,
		},
	}
	for _, testCase := range cases {
		fixture := newEnvironmentWriteFixture(t)
		fixture.writer.createFunc = func(
			context.Context,
			app.CreateVersionRequest,
		) (domain.EnvironmentVersion, error) {
			return domain.EnvironmentVersion{}, testCase.err
		}
		body := `{"host_id":"` + fixture.host.ID.String() + `","dockerfile":"FROM debian:12.7-slim\n"}`
		status, response := fixture.doJSON(t, http.MethodPost, target, body)
		if status != testCase.status {
			t.Fatalf("%s: 期望 %d，实际 %d，响应: %s", testCase.name, testCase.status, status, response)
		}
		if code := errorCode(t, response); code != testCase.code {
			t.Fatalf("%s: 期望错误码 %s，实际 %s", testCase.name, testCase.code, code)
		}
	}
}

// TestCreateVersionConvertsRequest 验证 HTTP 层只做协议转换。
func TestCreateVersionConvertsRequest(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentWriteFixture(t)
	created := domain.EnvironmentVersion{
		HostID:        fixture.host.ID,
		Ref:           fixture.languageRef,
		Number:        3,
		ParentVersion: "v0001",
		Status:        domain.BuildStatusPending,
		ImageRef:      "agentflow-language-go:v0003-aaaaaaaaaaaa",
		CreatedAt:     time.Now().UTC().Truncate(time.Millisecond),
	}
	fixture.writer.createFunc = func(
		_ context.Context,
		request app.CreateVersionRequest,
	) (domain.EnvironmentVersion, error) {
		if request.MachineID != fixture.host.ID.String() {
			t.Fatalf("host 未透传: %s", request.MachineID)
		}
		if string(request.Dockerfile) != "FROM debian:12.7-slim\n" {
			t.Fatalf("Dockerfile 未透传: %q", request.Dockerfile)
		}
		if request.ParentVersion != "v0001" {
			t.Fatalf("父版本未透传: %s", request.ParentVersion)
		}
		if len(request.ExtraFiles) != 1 || request.ExtraFiles[0].RelativePath != "scripts/setup.sh" {
			t.Fatalf("额外文件未透传: %+v", request.ExtraFiles)
		}
		if request.ExtraFiles[0].Mode.Perm() != 0o755 {
			t.Fatalf("额外文件权限未透传: %v", request.ExtraFiles[0].Mode)
		}
		return created, nil
	}
	body := `{"host_id":"` + fixture.host.ID.String() + `","dockerfile":"FROM debian:12.7-slim\n",` +
		`"parent_version":"v0001","extra_files":[{"path":"scripts/setup.sh","content":"echo ok","mode":493}]}`
	status, response := fixture.doJSON(
		t,
		http.MethodPost,
		httpapi.PathEnvironments+"/language/go/versions",
		body,
	)
	if status != http.StatusCreated {
		t.Fatalf("创建版本应返回 201，实际 %d，响应: %s", status, response)
	}
	var payload httpapi.EnvironmentVersionResponse
	if err := json.Unmarshal([]byte(response), &payload); err != nil {
		t.Fatalf("解析响应失败: %v，响应: %s", err, response)
	}
	if payload.Version != "v0003" || payload.BuildStatus != domain.BuildStatusPending.String() {
		t.Fatalf("响应字段不符: %+v", payload)
	}
	if payload.Usable {
		t.Fatalf("未构建的版本不得标记为可用: %+v", payload)
	}
}

// TestBuildSubmitReportsAcceptedOnly 验证构建接口只报告"已受理"。
//
// 202 + in_progress 不代表构建成功；重复提交必须返回 409，而不是静默返回同一个成功。
func TestBuildSubmitReportsAcceptedOnly(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentWriteFixture(t)
	version := writeEnvironmentVersion(t, fixture, environmentWriteSeed{
		number: 2,
		ref:    fixture.languageRef,
		status: domain.BuildStatusPending,
		// 语言环境必须记录父基础环境版本，未构建的父版本不影响"受理构建"。
		parent: "v0001",
	})
	target := httpapi.PathEnvironments + "/language/go/build"
	acceptedAt := time.Now().UTC().Truncate(time.Millisecond)
	fixture.builder.submitFunc = func(
		_ context.Context,
		request app.BuildRequest,
	) (app.EnvironmentBuildTask, error) {
		return app.EnvironmentBuildTask{
			AcceptedAt:    acceptedAt,
			HostID:        fixture.host.ID,
			Ref:           request.Ref,
			Version:       version,
			VersionLabel:  version.Label(),
			VersionNumber: version.Number,
		}, nil
	}
	body := `{"host_id":"` + fixture.host.ID.String() + `","version":2,"no_cache":true}`
	status, response := fixture.doJSON(t, http.MethodPost, target, body)
	if status != http.StatusAccepted {
		t.Fatalf("构建受理应返回 202，实际 %d，响应: %s", status, response)
	}
	if !fixture.builder.seen.NoCache {
		t.Fatalf("no_cache 未透传: %+v", fixture.builder.seen)
	}
	var payload httpapi.EnvironmentBuildResponse
	if err := json.Unmarshal([]byte(response), &payload); err != nil {
		t.Fatalf("解析响应失败: %v，响应: %s", err, response)
	}
	if !payload.InProgress {
		t.Fatalf("受理响应必须标记 in_progress: %+v", payload)
	}
	if payload.Version.BuildStatus == domain.BuildStatusSucceeded.String() {
		t.Fatalf("受理响应不得声称构建成功: %+v", payload)
	}

	// 重复提交：应用层返回"构建进行中"时必须是 409 + 稳定错误码。
	fixture.builder.submitFunc = func(
		context.Context,
		app.BuildRequest,
	) (app.EnvironmentBuildTask, error) {
		return app.EnvironmentBuildTask{}, app.ErrEnvironmentBuildInProgress
	}
	status, response = fixture.doJSON(t, http.MethodPost, target, body)
	if status != http.StatusConflict {
		t.Fatalf("重复构建应返回 409，实际 %d，响应: %s", status, response)
	}
	if code := errorCode(t, response); code != httpapi.CodeEnvironmentBuildInProgress {
		t.Fatalf("重复构建错误码不符: %s", code)
	}
}

// TestBuildSubmitValidation 覆盖构建接口的参数校验与装配缺失语义。
func TestBuildSubmitValidation(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentWriteFixture(t)
	target := httpapi.PathEnvironments + "/language/go/build"
	cases := []struct {
		name   string
		body   string
		err    error
		code   string
		status int
	}{
		{
			name:   "版本号为 0",
			body:   `{"host_id":"` + fixture.host.ID.String() + `","version":0}`,
			status: http.StatusBadRequest,
			code:   httpapi.CodeInvalidRequest,
		},
		{
			name:   "节点标识为空",
			body:   `{"host_id":"","version":1}`,
			err:    app.ErrEnvironmentInvalidInput,
			status: http.StatusBadRequest,
			code:   httpapi.CodeInvalidRequest,
		},
		{
			name:   "版本不存在",
			body:   `{"host_id":"` + fixture.host.ID.String() + `","version":9}`,
			err:    app.ErrEnvironmentVersionNotFound,
			status: http.StatusNotFound,
			code:   httpapi.CodeNotFound,
		},
		{
			name:   "节点未通过预检",
			body:   `{"host_id":"` + fixture.host.ID.String() + `","version":1}`,
			err:    app.ErrHostValidationUnavailable,
			status: http.StatusPreconditionFailed,
			code:   httpapi.CodePreconditionFailed,
		},
		{
			name:   "构建服务已停止",
			body:   `{"host_id":"` + fixture.host.ID.String() + `","version":1}`,
			err:    app.ErrEnvironmentBuildStopped,
			status: http.StatusServiceUnavailable,
			code:   httpapi.CodeEnvironmentUnavailable,
		},
	}
	for _, testCase := range cases {
		fixture.builder.submitFunc = func(
			context.Context,
			app.BuildRequest,
		) (app.EnvironmentBuildTask, error) {
			if testCase.err == nil {
				t.Fatalf("%s: 该用例不应进入应用层", testCase.name)
			}
			return app.EnvironmentBuildTask{}, testCase.err
		}
		status, response := fixture.doJSON(t, http.MethodPost, target, testCase.body)
		if status != testCase.status {
			t.Fatalf("%s: 期望 %d，实际 %d，响应: %s", testCase.name, testCase.status, status, response)
		}
		if code := errorCode(t, response); code != testCase.code {
			t.Fatalf("%s: 期望错误码 %s，实际 %s", testCase.name, testCase.code, code)
		}
	}
}

// TestBuildStatusReportsRealOutcome 验证构建状态接口如实区分四种结论。
func TestBuildStatusReportsRealOutcome(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentWriteFixture(t)
	// 基础环境版本按状态构造，构建状态查询的结论完全来自数据库记录。
	writeEnvironmentVersion(t, fixture, environmentWriteSeed{
		number: 1,
		ref:    fixture.baseRef,
		status: domain.BuildStatusSucceeded,
		usable: true,
	})
	writeEnvironmentVersion(t, fixture, environmentWriteSeed{
		number:  2,
		ref:     fixture.baseRef,
		status:  domain.BuildStatusBuilding,
		summary: "",
	})
	writeEnvironmentVersion(t, fixture, environmentWriteSeed{
		number:  3,
		ref:     fixture.baseRef,
		status:  domain.BuildStatusFailed,
		summary: "构建失败: 退出码 1",
	})
	writeEnvironmentVersion(t, fixture, environmentWriteSeed{
		number:  4,
		ref:     fixture.baseRef,
		status:  domain.BuildStatusUncertain,
		summary: "构建被中断，结果无法确认",
	})
	fixture.builder.statusFunc = func(
		hostID domain.ID,
		ref domain.EnvironmentRef,
	) (app.EnvironmentBuildTask, bool) {
		if ref.Label() != fixture.baseRef.Label() {
			return app.EnvironmentBuildTask{}, false
		}
		return app.EnvironmentBuildTask{
			AcceptedAt: time.Now().UTC().Truncate(time.Millisecond),
			HostID:     hostID,
			Ref:        ref,
		}, true
	}
	base := httpapi.PathEnvironments + "/base/base/versions/"
	hostQuery := "?host_id=" + fixture.host.ID.String()

	status, body := fixture.doJSON(t, http.MethodGet, base+"1/build-status"+hostQuery, "")
	if status != http.StatusOK {
		t.Fatalf("构建成功应返回 200，实际 %d，响应: %s", status, body)
	}

	status, body = fixture.doJSON(t, http.MethodGet, base+"2/build-status"+hostQuery, "")
	if status != http.StatusAccepted {
		t.Fatalf("构建中应返回 202，实际 %d，响应: %s", status, body)
	}
	var running httpapi.EnvironmentBuildStatusResponse
	if err := json.Unmarshal([]byte(body), &running); err != nil {
		t.Fatalf("解析响应失败: %v，响应: %s", err, body)
	}
	if !running.InProgress {
		t.Fatalf("在途构建必须标记 in_progress: %+v", running)
	}

	status, body = fixture.doJSON(t, http.MethodGet, base+"3/build-status"+hostQuery, "")
	if status != http.StatusInternalServerError {
		t.Fatalf("构建失败应返回 500，实际 %d，响应: %s", status, body)
	}
	if code := errorCode(t, body); code != httpapi.CodeEnvironmentBuildFailed {
		t.Fatalf("构建失败错误码不符: %s", code)
	}
	if !strings.Contains(body, "退出码 1") {
		t.Fatalf("构建失败响应必须包含服务端原因: %s", body)
	}

	status, body = fixture.doJSON(t, http.MethodGet, base+"4/build-status"+hostQuery, "")
	if status != http.StatusInternalServerError {
		t.Fatalf("构建结果不确定应返回 500，实际 %d，响应: %s", status, body)
	}
	if code := errorCode(t, body); code != httpapi.CodeEnvironmentBuildUncertain {
		t.Fatalf("构建结果不确定错误码不符: %s", code)
	}
}

// TestBuildLogNeverAcceptsPaths 验证构建日志接口不接受任何路径参数。
//
// 客户端只能指定节点、环境与版本号；日志路径由服务端从版本记录推导。
func TestBuildLogNeverAcceptsPaths(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentWriteFixture(t)
	version := writeEnvironmentVersion(t, fixture, environmentWriteSeed{
		number: 1,
		ref:    fixture.baseRef,
		status: domain.BuildStatusFailed,
	})
	fixture.writer.logFunc = func(
		_ context.Context,
		request app.ReadBuildLogRequest,
	) (app.BuildLog, error) {
		return app.BuildLog{
			Content:    []byte("step 1/3: FROM debian\n"),
			RemotePath: "/home/deploy/.lingting/agentflow/builds/base/v0001-aaaaaaaaaaaa/build.log",
			Version:    version,
		}, nil
	}
	target := httpapi.PathEnvironments + "/base/base/versions/1/build-log?host_id=" +
		fixture.host.ID.String() + "&path=/etc/passwd&local_path=/tmp/x"
	status, body := fixture.doJSON(t, http.MethodGet, target, "")
	if status != http.StatusOK {
		t.Fatalf("读取构建日志应返回 200，实际 %d，响应: %s", status, body)
	}
	if fixture.writer.seenBuildLog.MachineID != fixture.host.ID.String() ||
		fixture.writer.seenBuildLog.VersionNumber != 1 {
		t.Fatalf("应用层收到的请求不符: %+v", fixture.writer.seenBuildLog)
	}
	if fixture.writer.seenBuildLog.Ref.Label() != fixture.baseRef.Label() {
		t.Fatalf("应用层收到的环境不符: %+v", fixture.writer.seenBuildLog)
	}
	var payload httpapi.EnvironmentBuildLogResponse
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("解析响应失败: %v，响应: %s", err, body)
	}
	if payload.Content != "step 1/3: FROM debian\n" {
		t.Fatalf("日志正文不符: %+v", payload)
	}

	// 日志不存在时必须是 404：读取失败不能被渲染成空日志。
	fixture.writer.logFunc = func(
		context.Context,
		app.ReadBuildLogRequest,
	) (app.BuildLog, error) {
		return app.BuildLog{}, app.ErrEnvironmentBuildLogMissing
	}
	status, body = fixture.doJSON(t, http.MethodGet, target, "")
	if status != http.StatusNotFound {
		t.Fatalf("日志缺失应返回 404，实际 %d，响应: %s", status, body)
	}
	if code := errorCode(t, body); code != httpapi.CodeNotFound {
		t.Fatalf("日志缺失错误码不符: %s", code)
	}
}

// TestDockerfileEndpointReadsVersion 验证 Dockerfile 读取接口。
func TestDockerfileEndpointReadsVersion(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentWriteFixture(t)
	version := writeEnvironmentVersion(t, fixture, environmentWriteSeed{
		number: 2,
		ref:    fixture.languageRef,
		status: domain.BuildStatusSucceeded,
		usable: true,
		parent: "v0001",
	})
	fixture.writer.dockerfileFunc = func(
		_ context.Context,
		request app.ReadDockerfileRequest,
	) (app.DockerfileContent, error) {
		return app.DockerfileContent{
			Content:    []byte("FROM base:v0001\n"),
			RemotePath: "/remote/dockerfile",
			Version:    version,
		}, nil
	}
	target := httpapi.PathEnvironments + "/language/go/versions/2/dockerfile?host_id=" +
		fixture.host.ID.String()
	status, body := fixture.doJSON(t, http.MethodGet, target, "")
	if status != http.StatusOK {
		t.Fatalf("读取 Dockerfile 应返回 200，实际 %d，响应: %s", status, body)
	}
	var payload httpapi.EnvironmentDockerfileResponse
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("解析响应失败: %v，响应: %s", err, body)
	}
	if payload.Content != "FROM base:v0001\n" || payload.Version != "v0002" {
		t.Fatalf("响应字段不符: %+v", payload)
	}
	if fixture.writer.seenDockerfile.VersionNumber != 2 {
		t.Fatalf("应用层收到的版本号不符: %+v", fixture.writer.seenDockerfile)
	}
}

// TestEnvironmentReadEndpointsRegression 验证只读查询接口在扩展后行为不变。
func TestEnvironmentReadEndpointsRegression(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentWriteFixture(t)
	writeEnvironmentVersion(t, fixture, environmentWriteSeed{
		number: 1,
		ref:    fixture.languageRef,
		status: domain.BuildStatusSucceeded,
		usable: true,
		parent: "v0001",
	})
	writeEnvironmentVersion(t, fixture, environmentWriteSeed{
		number: 2,
		ref:    fixture.languageRef,
		status: domain.BuildStatusPending,
		parent: "v0001",
	})
	hostQuery := "?host_id=" + fixture.host.ID.String()

	status, body := fixture.doJSON(t, http.MethodGet, httpapi.PathEnvironments+hostQuery, "")
	if status != http.StatusOK {
		t.Fatalf("环境列表应返回 200，实际 %d，响应: %s", status, body)
	}
	var list httpapi.EnvironmentListResponse
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("解析列表失败: %v，响应: %s", err, body)
	}
	if list.Total != 1 || list.Records[0].CurrentVersion != "v0001" || list.Records[0].LatestVersion != "v0002" {
		t.Fatalf("列表语义不符: %+v", list)
	}

	status, body = fixture.doJSON(
		t,
		http.MethodGet,
		httpapi.PathEnvironments+"/language/go"+hostQuery,
		"",
	)
	if status != http.StatusOK {
		t.Fatalf("环境详情应返回 200，实际 %d，响应: %s", status, body)
	}
	var detail httpapi.EnvironmentResponse
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		t.Fatalf("解析详情失败: %v，响应: %s", err, body)
	}
	if detail.Name != "go" || len(detail.Versions) != 2 {
		t.Fatalf("详情字段不符: %+v", detail)
	}

	// 不存在的环境：404，不是空列表。
	status, body = fixture.doJSON(
		t,
		http.MethodGet,
		httpapi.PathEnvironments+"/language/node"+hostQuery,
		"",
	)
	if status != http.StatusNotFound {
		t.Fatalf("不存在的环境应返回 404，实际 %d，响应: %s", status, body)
	}

	// 不存在的版本：404。
	status, body = fixture.doJSON(
		t,
		http.MethodGet,
		httpapi.PathEnvironments+"/language/go/versions/9"+hostQuery,
		"",
	)
	if status != http.StatusNotFound {
		t.Fatalf("不存在的版本应返回 404，实际 %d，响应: %s", status, body)
	}

	// 缺少 host_id：400，不隐式回退到默认节点。
	status, body = fixture.doJSON(t, http.MethodGet, httpapi.PathEnvironments, "")
	if status != http.StatusBadRequest {
		t.Fatalf("缺少 host_id 应返回 400，实际 %d，响应: %s", status, body)
	}
}

// TestEnvironmentWriteEndpointsRequireAssembly 验证服务未装配时返回 503。
func TestEnvironmentWriteEndpointsRequireAssembly(t *testing.T) {
	t.Parallel()
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	handler := httpapi.NewHandler(httpapi.Deps{Logger: logger.Slog()})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		httpapi.PathEnvironments+"/init",
		strings.NewReader(`{"host_id":"host-1"}`),
	)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("未装配环境管理服务时应返回 503，实际 %d", recorder.Code)
	}
	if code := errorCode(t, recorder.Body.String()); code != httpapi.CodeEnvironmentUnavailable {
		t.Fatalf("错误码不符: %s", code)
	}
}

// TestInitEndpointReportsPreflight 验证初始化接口返回真实预检结果。
func TestInitEndpointReportsPreflight(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentWriteFixture(t)
	fixture.writer.initFunc = func(
		_ context.Context,
		options app.InitOptions,
	) (app.InitResult, error) {
		if len(options.WithLanguages) != 2 {
			t.Fatalf("语言环境列表未透传: %+v", options)
		}
		return app.InitResult{
			Created:       []string{"/home/deploy/.lingting/agentflow/environments"},
			Environments:  []domain.EnvironmentRef{fixture.baseRef},
			Host:          fixture.host,
			WorkspaceRoot: "/home/deploy/.lingting/agentflow",
		}, nil
	}
	body := `{"host_id":"` + fixture.host.ID.String() + `","with_languages":["go","node"]}`
	status, response := fixture.doJSON(t, http.MethodPost, httpapi.PathEnvironments+"/init", body)
	if status != http.StatusOK {
		t.Fatalf("初始化应返回 200，实际 %d，响应: %s", status, response)
	}
	var payload httpapi.EnvironmentInitResponse
	if err := json.Unmarshal([]byte(response), &payload); err != nil {
		t.Fatalf("解析响应失败: %v，响应: %s", err, response)
	}
	if payload.WorkspaceRoot == "" || len(payload.Environments) != 1 {
		t.Fatalf("初始化响应字段不符: %+v", payload)
	}
	// 预检结果来自服务端：默认零值必须如实表达"未通过"，不能显示成通过。
	if payload.Preflight.Ready {
		t.Fatalf("零值预检不得显示为通过: %+v", payload.Preflight)
	}
}

// environmentWriteSeed 描述一个待写入的环境版本。
type environmentWriteSeed struct {
	ref     domain.EnvironmentRef
	number  int
	status  domain.BuildStatus
	usable  bool
	parent  string
	summary string
}

// writeEnvironmentVersion 写入一个合法的环境版本记录。
func writeEnvironmentVersion(
	t *testing.T,
	fixture *environmentWriteFixture,
	seed environmentWriteSeed,
) domain.EnvironmentVersion {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	label := seed.ref.Label() + "-" + domain.FormatVersionLabel(seed.number)
	version := domain.EnvironmentVersion{
		HostID:            fixture.host.ID,
		Ref:               seed.ref,
		Number:            seed.number,
		DockerfileSHA256:  apiHash("dockerfile-" + label),
		BuildConfigSHA256: apiHash("config-" + label),
		ContextSHA256:     apiHash("context-" + seed.ref.Label()),
		ParentVersion:     seed.parent,
		ImageRef:          "agentflow-" + string(seed.ref.Type) + "-" + seed.ref.Name + ":" + domain.FormatVersionLabel(seed.number) + "-aaaaaaaaaaaa",
		Status:            seed.status,
		ErrorSummary:      seed.summary,
		CreatedAt:         time.Now().UTC().Truncate(time.Millisecond),
	}
	if seed.usable {
		version.ImageID = "sha256:" + strings.Repeat("d", 64)
		builtAt := time.Now().UTC().Truncate(time.Millisecond)
		version.BuiltAt = &builtAt
	}
	if err := version.Validate(); err != nil {
		t.Fatalf("构造的版本应当合法: %v", err)
	}
	if err := fixture.db.CreateEnvironmentVersion(ctx, version); err != nil {
		t.Fatalf("写入环境版本失败: %v", err)
	}
	return version
}
