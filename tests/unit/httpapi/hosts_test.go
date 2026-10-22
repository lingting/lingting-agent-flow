package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// hostAPIFixture 是执行节点接口测试的运行环境。
type hostAPIFixture struct {
	handler   http.Handler
	db        *sqlite.DB
	service   *app.ExecutionHostService
	validator *app.HostValidator
}

// newHostAPIFixture 准备执行节点接口所需的数据与处理器。
func newHostAPIFixture(t *testing.T) *hostAPIFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: filepath.Join(testkit.Dir(t), "agentflow.db")})
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
	service, err := app.NewExecutionHostService(db, logger)
	if err != nil {
		t.Fatalf("构造执行节点服务失败: %v", err)
	}
	sshService, err := app.NewSSHService(db, logger, app.SSHOptions{
		KnownHostsPath: filepath.Join(testkit.Dir(t), "known_hosts"),
	})
	if err != nil {
		t.Fatalf("构造 SSH 服务失败: %v", err)
	}
	validator, err := app.NewHostValidator(app.HostValidationOptions{
		SSH:    sshService,
		Store:  db,
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("构造校验器失败: %v", err)
	}
	service.WithValidation(nil, validator)
	return &hostAPIFixture{
		db:        db,
		service:   service,
		validator: validator,
		handler: httpapi.NewHandler(httpapi.Deps{
			Hosts:  service,
			Logger: logger.Slog(),
		}),
	}
}

// apiDiscard 丢弃日志输出，避免测试噪声。
type apiDiscard struct{}

// Write 丢弃内容。
func (apiDiscard) Write(p []byte) (int, error) { return len(p), nil }

// doRequest 执行一次请求并返回响应。
func doRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// decodeHostResponse 解析单个节点响应。
func decodeHostResponse(t *testing.T, recorder *httptest.ResponseRecorder) httpapi.HostResponse {
	t.Helper()
	var response httpapi.HostResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v: %s", err, recorder.Body.String())
	}
	return response
}

// TestCreateHostReturnsCheckingStatus 验证创建接口返回真实初始状态。
func TestCreateHostReturnsCheckingStatus(t *testing.T) {
	t.Parallel()
	fixture := newHostAPIFixture(t)
	recorder := doRequest(t, fixture.handler, http.MethodPost, httpapi.PathHosts,
		mustJSON(t, map[string]any{
			"host":             "10.0.0.70",
			"username":         "deploy",
			"name":             "接口节点",
			"private_key_path": testKeyPath(t),
		}))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("状态码不符: %d: %s", recorder.Code, recorder.Body.String())
	}
	response := decodeHostResponse(t, recorder)
	if response.ValidationStatus != string(domain.ValidationStatusChecking) {
		t.Fatalf("创建后应为 checking: %s", response.ValidationStatus)
	}
	if response.UsableForRemoteTask {
		t.Fatal("checking 状态不得标记为可用于远程任务")
	}
	if response.ValidationGeneration != 1 {
		t.Fatalf("初始代次应为 1: %d", response.ValidationGeneration)
	}
	if response.ValidationDetails != nil {
		t.Fatal("初始状态不应带有校验详情")
	}
}

// TestCreateHostIgnoresClientValidationFields 验证客户端不能指定校验字段。
func TestCreateHostIgnoresClientValidationFields(t *testing.T) {
	t.Parallel()
	fixture := newHostAPIFixture(t)
	// 请求体包含未知字段时被拒绝，客户端无法伪造校验状态。
	recorder := doRequest(t, fixture.handler, http.MethodPost, httpapi.PathHosts,
		`{"host":"10.0.0.71","username":"deploy","validation_status":"normal"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("包含未知字段应返回 400: %d", recorder.Code)
	}
	var payload httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if payload.Error.Code != httpapi.CodeInvalidRequest {
		t.Fatalf("错误代码不符: %s", payload.Error.Code)
	}
}

// TestCreateHostRejectsInvalidAuthMethod 验证非法认证方式返回参数错误。
func TestCreateHostRejectsInvalidAuthMethod(t *testing.T) {
	t.Parallel()
	fixture := newHostAPIFixture(t)
	recorder := doRequest(t, fixture.handler, http.MethodPost, httpapi.PathHosts,
		mustJSON(t, map[string]any{
			"host":             "10.0.0.72",
			"username":         "deploy",
			"auth_method":      "hardware_key",
			"private_key_path": testKeyPath(t),
		}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("状态码不符: %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestGetHostReturnsValidationDetails 验证详情接口返回已保存的校验结果。
func TestGetHostReturnsValidationDetails(t *testing.T) {
	t.Parallel()
	fixture := newHostAPIFixture(t)
	ctx := context.Background()
	host, err := fixture.service.Create(ctx, app.CreateExecutionHostRequest{
		Name:           "详情节点",
		Host:           "10.0.0.73",
		Username:       "deploy",
		PrivateKeyPath: testKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	stored, err := fixture.db.GetExecutionHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	completed := time.Now().UTC()
	details := domain.ValidationDetails{
		ValidatorVersion: domain.ValidatorVersion,
		Capabilities:     []string{domain.CapabilityDockerCLIHelpOnly},
		Checks: []domain.ValidationCheck{
			{
				Name:        domain.ValidationCheckSSHConnection,
				Title:       "SSH 连接",
				Status:      domain.CheckStatusPassed,
				CompletedAt: &completed,
			},
		},
	}
	if err := stored.RecordValidationResult(stored.ValidationGeneration, details, completed); err != nil {
		t.Fatalf("记录校验结果失败: %v", err)
	}
	if _, err := fixture.db.UpdateExecutionHostValidation(ctx, *stored, stored.ValidationGeneration); err != nil {
		t.Fatalf("写入校验结果失败: %v", err)
	}

	recorder := doRequest(t, fixture.handler, http.MethodGet,
		httpapi.PathHosts+"/"+host.ID.String(), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码不符: %d: %s", recorder.Code, recorder.Body.String())
	}
	response := decodeHostResponse(t, recorder)
	if response.ValidationStatus != string(domain.ValidationStatusNormal) {
		t.Fatalf("状态应为 normal: %s", response.ValidationStatus)
	}
	if !response.UsableForRemoteTask {
		t.Fatal("normal 状态应标记为可用于远程任务")
	}
	if response.ValidationDetails == nil {
		t.Fatal("详情应被返回")
	}
	if response.ValidationDetails.ValidatorVersion != domain.ValidatorVersion {
		t.Fatalf("校验器版本不符: %s", response.ValidationDetails.ValidatorVersion)
	}
	if len(response.ValidationDetails.Checks) != 1 {
		t.Fatalf("检查项数量不符: %+v", response.ValidationDetails.Checks)
	}
}

// TestValidateEndpointReturnsChecking 验证重新校验接口立即返回 checking。
func TestValidateEndpointReturnsChecking(t *testing.T) {
	t.Parallel()
	fixture := newHostAPIFixture(t)
	host, err := fixture.service.Create(context.Background(), app.CreateExecutionHostRequest{
		Name:           "重新校验节点",
		Host:           "10.0.0.74",
		Username:       "deploy",
		PrivateKeyPath: testKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	started := time.Now()
	recorder := doRequest(t, fixture.handler, http.MethodPost,
		httpapi.PathHosts+"/"+host.ID.String()+"/validate", "")
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("重新校验接口不应等待校验完成: %s", elapsed)
	}
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("状态码不符: %d: %s", recorder.Code, recorder.Body.String())
	}
	response := decodeHostResponse(t, recorder)
	if response.ValidationStatus != string(domain.ValidationStatusChecking) {
		t.Fatalf("重新校验后应为 checking: %s", response.ValidationStatus)
	}
	if response.ValidationGeneration <= host.ValidationGeneration {
		t.Fatalf("重新校验应增加代次: %d", response.ValidationGeneration)
	}
}

// TestValidateEndpointRejectsGet 验证重新校验接口只接受 POST。
func TestValidateEndpointRejectsGet(t *testing.T) {
	t.Parallel()
	fixture := newHostAPIFixture(t)
	host, err := fixture.service.Create(context.Background(), app.CreateExecutionHostRequest{
		Name:           "方法校验节点",
		Host:           "10.0.0.75",
		Username:       "deploy",
		PrivateKeyPath: testKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	recorder := doRequest(t, fixture.handler, http.MethodGet,
		httpapi.PathHosts+"/"+host.ID.String()+"/validate", "")
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("状态码不符: %d", recorder.Code)
	}
}

// TestUpdateHostInvalidatesValidation 验证修改接口使旧结果失效。
func TestUpdateHostInvalidatesValidation(t *testing.T) {
	t.Parallel()
	fixture := newHostAPIFixture(t)
	ctx := context.Background()
	host, err := fixture.service.Create(ctx, app.CreateExecutionHostRequest{
		Name:           "修改节点",
		Host:           "10.0.0.76",
		Username:       "deploy",
		PrivateKeyPath: testKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	stored, err := fixture.db.GetExecutionHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	details := domain.ValidationDetails{
		ValidatorVersion: domain.ValidatorVersion,
		Checks: []domain.ValidationCheck{
			{Name: domain.ValidationCheckSSHConnection, Status: domain.CheckStatusPassed},
		},
	}
	if err := stored.RecordValidationResult(stored.ValidationGeneration, details, time.Now()); err != nil {
		t.Fatalf("记录校验结果失败: %v", err)
	}
	if _, err := fixture.db.UpdateExecutionHostValidation(ctx, *stored, stored.ValidationGeneration); err != nil {
		t.Fatalf("写入校验结果失败: %v", err)
	}

	recorder := doRequest(t, fixture.handler, http.MethodPatch,
		httpapi.PathHosts+"/"+host.ID.String(), `{"port":2222}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码不符: %d: %s", recorder.Code, recorder.Body.String())
	}
	response := decodeHostResponse(t, recorder)
	if response.ValidationStatus != string(domain.ValidationStatusChecking) {
		t.Fatalf("修改后应为 checking: %s", response.ValidationStatus)
	}
	if response.UsableForRemoteTask {
		t.Fatal("修改后不得继续标记为可用")
	}
}

// TestUnknownHostReturnsJSONNotFound 验证不存在的节点返回统一 JSON 404。
func TestUnknownHostReturnsJSONNotFound(t *testing.T) {
	t.Parallel()
	fixture := newHostAPIFixture(t)
	recorder := doRequest(t, fixture.handler, http.MethodGet,
		httpapi.PathHosts+"/missing-host", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("状态码不符: %d", recorder.Code)
	}
	var payload httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if payload.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误代码不符: %s", payload.Error.Code)
	}
}

// TestUnknownHostSubPathReturnsNotFound 验证未知子路径不会落到其他接口。
func TestUnknownHostSubPathReturnsNotFound(t *testing.T) {
	t.Parallel()
	fixture := newHostAPIFixture(t)
	recorder := doRequest(t, fixture.handler, http.MethodPost,
		httpapi.PathHosts+"/some-host/unknown-action", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("状态码不符: %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestHostServiceUnavailableReturns503 验证未初始化服务时返回 503。
func TestHostServiceUnavailableReturns503(t *testing.T) {
	t.Parallel()
	handler := httpapi.NewHandler(httpapi.Deps{})
	recorder := doRequest(t, handler, http.MethodGet, httpapi.PathHosts, "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码不符: %d", recorder.Code)
	}
}

// TestListHostsReturnsAllRecords 验证列表接口返回全部节点与真实状态。
func TestListHostsReturnsAllRecords(t *testing.T) {
	t.Parallel()
	fixture := newHostAPIFixture(t)
	ctx := context.Background()
	entries := []struct{ name, host string }{
		{"节点一", "10.0.0.88"},
		{"节点二", "10.0.0.89"},
	}
	for _, entry := range entries {
		if _, err := fixture.service.Create(ctx, app.CreateExecutionHostRequest{
			Name:           entry.name,
			Host:           entry.host,
			Username:       "deploy",
			PrivateKeyPath: testKeyPath(t),
		}); err != nil {
			t.Fatalf("新增执行节点失败: %v", err)
		}
	}
	recorder := doRequest(t, fixture.handler, http.MethodGet, httpapi.PathHosts, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码不符: %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.HostListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if response.Total != 2 || len(response.Records) != 2 {
		t.Fatalf("记录数量不符: %+v", response)
	}
	for _, record := range response.Records {
		if record.ValidationStatus != string(domain.ValidationStatusChecking) {
			t.Fatalf("未校验节点应为 checking: %s", record.ValidationStatus)
		}
	}
}

// TestDeleteHostReturnsNoContent 验证删除返回 204。
func TestDeleteHostReturnsNoContent(t *testing.T) {
	t.Parallel()
	fixture := newHostAPIFixture(t)
	host, err := fixture.service.Create(context.Background(), app.CreateExecutionHostRequest{
		Name:           "删除节点",
		Host:           "10.0.0.80",
		Username:       "deploy",
		PrivateKeyPath: testKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	recorder := doRequest(t, fixture.handler, http.MethodDelete,
		httpapi.PathHosts+"/"+host.ID.String(), "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("状态码不符: %d: %s", recorder.Code, recorder.Body.String())
	}
	detail := doRequest(t, fixture.handler, http.MethodGet,
		httpapi.PathHosts+"/"+host.ID.String(), "")
	if detail.Code != http.StatusNotFound {
		t.Fatalf("删除后应返回 404: %d", detail.Code)
	}
}

// TestHostResponseDoesNotLeakSecrets 验证响应不包含任何凭据字段。
func TestHostResponseDoesNotLeakSecrets(t *testing.T) {
	t.Parallel()
	fixture := newHostAPIFixture(t)
	recorder := doRequest(t, fixture.handler, http.MethodPost, httpapi.PathHosts,
		mustJSON(t, map[string]any{
			"host":             "10.0.0.81",
			"username":         "deploy",
			"auth_method":      "private_key",
			"private_key_path": "C:/keys/id_ed25519",
		}))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("状态码不符: %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"password", "passphrase", "BEGIN OPENSSH PRIVATE KEY"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Fatalf("响应不应包含敏感字段 %q: %s", forbidden, body)
		}
	}
	var payload map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(recorder.Body.Bytes()), &payload); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if _, ok := payload["private_key_path"]; !ok {
		t.Fatal("私钥路径应作为非机密配置返回")
	}
}

// testKeyPath 返回一个测试用私钥路径。
//
// 本版本只支持私钥认证，接口请求必须给出私钥路径；这里只校验书写形式，
// 文件是否存在由连接阶段判断。
func testKeyPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(testkit.Dir(t), "id_ed25519")
}

// mustJSON 把值编码为请求体字符串。
//
// 使用编码而不是手写字符串，避免 Windows 路径中的反斜杠被当成 JSON 转义。
func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("编码请求体失败: %v", err)
	}
	return string(data)
}
