package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
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

// environmentAPIFixture 是环境查询接口测试的运行环境。
type environmentAPIFixture struct {
	handler http.Handler
	db      *sqlite.DB
	host    domain.ExecutionHost
	base    domain.EnvironmentRef
	goRef   domain.EnvironmentRef
}

// newEnvironmentAPIFixture 打开真实数据库、写入执行节点与处理器。
func newEnvironmentAPIFixture(t *testing.T) *environmentAPIFixture {
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
	service, err := app.NewEnvironmentQueryService(db)
	if err != nil {
		t.Fatalf("构造环境查询服务失败: %v", err)
	}
	host := writeEnvironmentHost(t, ctx, db)
	base, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	goRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	return &environmentAPIFixture{
		db:      db,
		host:    host,
		base:    base,
		goRef:   goRef,
		handler: httpapi.NewHandler(httpapi.Deps{Environments: service, Logger: logger.Slog()}),
	}
}

// writeEnvironmentHost 写入一个真实的执行节点记录。
func writeEnvironmentHost(
	t *testing.T,
	ctx context.Context,
	store storage.Store,
) domain.ExecutionHost {
	t.Helper()
	host, err := domain.NewExecutionHost(
		"host-1",
		"构建机",
		"192.0.2.10",
		22,
		"deploy",
		"/home/deploy/.lingting/agentflow",
		time.Now().UTC(),
		domain.WithPrivateKey(testkit.PrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := store.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	return host
}

// environmentSeed 描述一个待写入的环境版本。
type environmentSeed struct {
	// ref 是环境引用。
	ref domain.EnvironmentRef
	// number 是版本号。
	number int
	// status 是构建状态。
	status domain.BuildStatus
	// usable 为 true 时补齐镜像 ID 与构建时间。
	usable bool
	// parent 是父基础环境版本标签。
	parent string
	// summary 是失败摘要。
	summary string
}

// writeVersion 写入一个合法的环境版本记录。
func (f *environmentAPIFixture) writeVersion(t *testing.T, seed environmentSeed) domain.EnvironmentVersion {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	label := seed.ref.Label() + "-" + domain.FormatVersionLabel(seed.number)
	version := domain.EnvironmentVersion{
		HostID:            f.host.ID,
		Ref:               seed.ref,
		Number:            seed.number,
		DockerfileSHA256:  apiHash("dockerfile-" + label),
		BuildConfigSHA256: apiHash("config-" + label),
		ContextSHA256:     apiHash("context-" + seed.ref.Label()),
		ParentVersion:     seed.parent,
		ImageRef:          "agentflow-" + string(seed.ref.Type) + "-" + seed.ref.Name + ":v0001-abcdefabcdef",
		Status:            seed.status,
		ErrorSummary:      seed.summary,
		CreatedAt:         time.Now().UTC().Truncate(time.Millisecond),
	}
	if seed.usable {
		version.ImageID = "sha256:" + strings.Repeat("a", 64)
		builtAt := time.Now().UTC().Truncate(time.Millisecond)
		version.BuiltAt = &builtAt
	}
	if err := version.Validate(); err != nil {
		t.Fatalf("构造的版本应当合法: %v", err)
	}
	if err := f.db.CreateEnvironmentVersion(ctx, version); err != nil {
		t.Fatalf("写入环境版本失败: %v", err)
	}
	return version
}

// apiHash 返回测试用的 64 位十六进制摘要。
func apiHash(seed string) string {
	digits := []byte(strings.Repeat("0", 64))
	for index, char := range []byte(seed) {
		digits[index%64] = "0123456789abcdef"[int(char)%16]
	}
	return string(digits)
}

// listPath 构造带 host_id 的环境列表路径。
func listPath(hostID string) string {
	return httpapi.PathEnvironments + "?host_id=" + url.QueryEscape(hostID)
}

// versionPath 构造环境版本查询路径。
func versionPath(environmentType, name, number, hostID string) string {
	return httpapi.PathEnvironments + "/" + environmentType + "/" + name +
		"/versions/" + number + "?host_id=" + url.QueryEscape(hostID)
}

// TestEnvironmentListReturnsRealRecords 校验列表返回真实记录与正确字段。
func TestEnvironmentListReturnsRealRecords(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentAPIFixture(t)
	fixture.writeVersion(t, environmentSeed{
		ref:    fixture.base,
		number: 1,
		status: domain.BuildStatusSucceeded,
		usable: true,
	})
	fixture.writeVersion(t, environmentSeed{
		ref:    fixture.goRef,
		number: 1,
		status: domain.BuildStatusSucceeded,
		usable: true,
		parent: domain.FormatVersionLabel(1),
	})
	fixture.writeVersion(t, environmentSeed{
		ref:     fixture.goRef,
		number:  2,
		status:  domain.BuildStatusFailed,
		summary: "镜像核实失败",
		parent:  domain.FormatVersionLabel(1),
	})

	recorder := doRequest(t, fixture.handler, http.MethodGet, listPath(fixture.host.ID.String()), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("环境列表应返回 200，实际 %d，响应: %s", recorder.Code, recorder.Body.String())
	}
	var body httpapi.EnvironmentListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析环境列表失败: %v", err)
	}
	if body.Total != 2 || len(body.Records) != 2 {
		t.Fatalf("应返回 2 个环境，实际 total=%d records=%d", body.Total, len(body.Records))
	}
	byLabel := make(map[string]httpapi.EnvironmentResponse, len(body.Records))
	for _, record := range body.Records {
		byLabel[record.Type+"/"+record.Name] = record
	}
	base, ok := byLabel[fixture.base.Label()]
	if !ok {
		t.Fatalf("响应缺少基础环境: %+v", body.Records)
	}
	if base.CurrentVersion != "v0001" || base.LatestVersion != "v0001" {
		t.Fatalf("基础环境版本汇总不符: %+v", base)
	}
	if len(base.Versions) != 1 || base.Versions[0].BuildStatus != "succeeded" || !base.Versions[0].Usable {
		t.Fatalf("基础环境版本字段不符: %+v", base.Versions)
	}
	if base.Versions[0].ImageRef == "" {
		t.Fatal("版本必须给出 image_ref，供任务选择与容器创建使用")
	}
	goEnv, ok := byLabel[fixture.goRef.Label()]
	if !ok {
		t.Fatalf("响应缺少语言环境: %+v", body.Records)
	}
	// 最新版本 v0002 构建失败，因此当前可用版本仍是 v0001。
	if goEnv.LatestVersion != "v0002" {
		t.Fatalf("语言环境最新版本应为 v0002，实际 %s", goEnv.LatestVersion)
	}
	if goEnv.CurrentVersion != "v0001" {
		t.Fatalf("语言环境当前可用版本应为 v0001，实际 %q", goEnv.CurrentVersion)
	}
	if len(goEnv.Versions) != 2 {
		t.Fatalf("语言环境版本数应为 2，实际 %d", len(goEnv.Versions))
	}
	if goEnv.Versions[1].Usable {
		t.Fatalf("构建失败的版本不得标记为可用: %+v", goEnv.Versions[1])
	}
	if goEnv.Versions[1].ErrorSummary != "镜像核实失败" {
		t.Fatalf("失败摘要应如实返回: %+v", goEnv.Versions[1])
	}
}

// TestEnvironmentListOmitsInternalFields 校验响应不泄露 Dockerfile 与凭据类字段。
func TestEnvironmentListOmitsInternalFields(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentAPIFixture(t)
	fixture.writeVersion(t, environmentSeed{
		ref:    fixture.base,
		number: 1,
		status: domain.BuildStatusSucceeded,
		usable: true,
	})
	recorder := doRequest(t, fixture.handler, http.MethodGet, listPath(fixture.host.ID.String()), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("环境列表应返回 200，实际 %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{
		"FROM ",
		"dockerfile_sha256",
		"build_config_sha256",
		"context_sha256",
		"private_key",
		"password",
		"remote_path",
		"ssh",
	} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Fatalf("响应不得包含 %q，实际响应: %s", forbidden, body)
		}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if _, ok := raw["records"]; !ok {
		t.Fatalf("响应必须使用 records 包装: %s", recorder.Body.String())
	}
	if _, ok := raw["total"]; !ok {
		t.Fatalf("响应必须给出 total: %s", recorder.Body.String())
	}
}

// TestEnvironmentVersionReturnsRealBuildStatus 校验单版本返回真实构建状态与元数据。
func TestEnvironmentVersionReturnsRealBuildStatus(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentAPIFixture(t)
	fixture.writeVersion(t, environmentSeed{
		ref:    fixture.base,
		number: 1,
		status: domain.BuildStatusSucceeded,
		usable: true,
	})
	fixture.writeVersion(t, environmentSeed{
		ref:     fixture.base,
		number:  2,
		status:  domain.BuildStatusFailed,
		summary: "build failed: exit 1",
	})

	recorder := doRequest(
		t,
		fixture.handler,
		http.MethodGet,
		versionPath("base", "base", "1", fixture.host.ID.String()),
		"",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("版本查询应返回 200，实际 %d，响应: %s", recorder.Code, recorder.Body.String())
	}
	var version httpapi.EnvironmentVersionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &version); err != nil {
		t.Fatalf("解析版本响应失败: %v", err)
	}
	if version.Version != "v0001" || version.Number != 1 {
		t.Fatalf("版本标识不符: %+v", version)
	}
	if version.BuildStatus != "succeeded" || !version.Usable {
		t.Fatalf("v0001 应为成功且可用: %+v", version)
	}
	if version.ImageID == "" || version.BuiltAt == nil {
		t.Fatalf("可用版本必须带镜像 ID 与构建时间: %+v", version)
	}

	recorder = doRequest(
		t,
		fixture.handler,
		http.MethodGet,
		versionPath("base", "base", "2", fixture.host.ID.String()),
		"",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("失败版本查询应返回 200，实际 %d", recorder.Code)
	}
	// 重新解码前先清零：响应省略空字段，复用同一个变量会保留上一次的值。
	version = httpapi.EnvironmentVersionResponse{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &version); err != nil {
		t.Fatalf("解析版本响应失败: %v", err)
	}
	if version.BuildStatus != "failed" || version.Usable {
		t.Fatalf("v0002 应为失败且不可用: %+v", version)
	}
	if version.ImageID != "" {
		t.Fatalf("失败版本不得返回镜像 ID: %+v", version)
	}
	if version.ErrorSummary != "build failed: exit 1" {
		t.Fatalf("失败摘要应如实返回: %+v", version)
	}
}

// TestEnvironmentListRejectsMissingHostID 校验缺少 host_id 返回 400。
func TestEnvironmentListRejectsMissingHostID(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentAPIFixture(t)
	recorder := doRequest(t, fixture.handler, http.MethodGet, httpapi.PathEnvironments, "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺少 host_id 应返回 400，实际 %d", recorder.Code)
	}
	var body httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if body.Error.Code != httpapi.CodeInvalidRequest {
		t.Fatalf("错误代码应为 %s，实际 %q", httpapi.CodeInvalidRequest, body.Error.Code)
	}
}

// TestEnvironmentListRejectsUnknownHost 校验不存在的执行节点返回 404。
func TestEnvironmentListRejectsUnknownHost(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentAPIFixture(t)
	recorder := doRequest(t, fixture.handler, http.MethodGet, listPath("missing-host"), "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在的节点应返回 404，实际 %d，响应: %s", recorder.Code, recorder.Body.String())
	}
	var body httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if body.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误代码应为 %s，实际 %q", httpapi.CodeNotFound, body.Error.Code)
	}
}

// TestEnvironmentListEmptyHostReturnsEmptyList 校验没有版本时返回合法空列表。
func TestEnvironmentListEmptyHostReturnsEmptyList(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentAPIFixture(t)
	recorder := doRequest(t, fixture.handler, http.MethodGet, listPath(fixture.host.ID.String()), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("没有版本时应返回 200，实际 %d", recorder.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if string(raw["records"]) != "[]" {
		t.Fatalf("没有版本时应返回空数组，实际 %s", raw["records"])
	}
	if string(raw["total"]) != "0" {
		t.Fatalf("没有版本时 total 应为 0，实际 %s", raw["total"])
	}
}

// TestEnvironmentVersionRejectsUnknownVersion 校验不存在的版本返回 404。
func TestEnvironmentVersionRejectsUnknownVersion(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentAPIFixture(t)
	fixture.writeVersion(t, environmentSeed{
		ref:    fixture.base,
		number: 1,
		status: domain.BuildStatusPending,
	})
	recorder := doRequest(
		t,
		fixture.handler,
		http.MethodGet,
		versionPath("base", "base", "9", fixture.host.ID.String()),
		"",
	)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在的版本应返回 404，实际 %d，响应: %s", recorder.Code, recorder.Body.String())
	}
}

// TestEnvironmentVersionRejectsInvalidParameters 校验非法环境类型、名称与版本号返回 400。
func TestEnvironmentVersionRejectsInvalidParameters(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentAPIFixture(t)
	hostID := fixture.host.ID.String()
	cases := []struct {
		name string
		path string
	}{
		{name: "未知环境类型", path: versionPath("unknown", "base", "1", hostID)},
		{name: "非法环境名称", path: versionPath("language", "Bad_Name", "1", hostID)},
		{name: "基础环境名称不符", path: versionPath("base", "other", "1", hostID)},
		{name: "版本号非整数", path: versionPath("base", "base", "abc", hostID)},
		{name: "版本号为零", path: versionPath("base", "base", "0", hostID)},
		{name: "版本号超上限", path: versionPath("base", "base", "10000", hostID)},
	}
	for _, testCase := range cases {
		recorder := doRequest(t, fixture.handler, http.MethodGet, testCase.path, "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf(
				"%s 应返回 400，实际 %d，响应: %s",
				testCase.name,
				recorder.Code,
				recorder.Body.String(),
			)
		}
		var body httpapi.ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s 解析错误响应失败: %v", testCase.name, err)
		}
		if body.Error.Code != httpapi.CodeInvalidRequest {
			t.Fatalf("%s 的错误代码应为 %s，实际 %q", testCase.name, httpapi.CodeInvalidRequest, body.Error.Code)
		}
	}
}

// TestEnvironmentVersionRejectsMissingHostID 校验版本查询同样要求 host_id。
func TestEnvironmentVersionRejectsMissingHostID(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentAPIFixture(t)
	path := httpapi.PathEnvironments + "/base/base/versions/1"
	recorder := doRequest(t, fixture.handler, http.MethodGet, path, "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺少 host_id 应返回 400，实际 %d", recorder.Code)
	}
}

// TestEnvironmentUnknownSubPathsAreNotFound 校验本阶段未提供的子路径统一 404。
func TestEnvironmentUnknownSubPathsAreNotFound(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentAPIFixture(t)
	hostID := fixture.host.ID.String()
	for _, path := range []string{
		httpapi.PathEnvironments + "/base?host_id=" + hostID,
		httpapi.PathEnvironments + "/base/base?host_id=" + hostID,
		httpapi.PathEnvironments + "/base/base/versions?host_id=" + hostID,
		httpapi.PathEnvironments + "/base/base/versions/1/build?host_id=" + hostID,
		httpapi.PathEnvironments + "/base/base/dockerfile?host_id=" + hostID,
	} {
		recorder := doRequest(t, fixture.handler, http.MethodGet, path, "")
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d", path, recorder.Code)
		}
	}
}

// TestEnvironmentRejectsUnsupportedMethods 校验不支持的方法返回 405 与 Allow。
func TestEnvironmentRejectsUnsupportedMethods(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentAPIFixture(t)
	hostID := fixture.host.ID.String()
	targets := []string{
		listPath(hostID),
		versionPath("base", "base", "1", hostID),
	}
	for _, target := range targets {
		for _, method := range []string{
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
		} {
			recorder := doRequest(t, fixture.handler, method, target, "{}")
			if recorder.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s 应返回 405，实际 %d", method, target, recorder.Code)
			}
			if allow := recorder.Header().Get("Allow"); allow != "GET, HEAD" {
				t.Fatalf("%s %s 的 Allow 应为 GET, HEAD，实际 %q", method, target, allow)
			}
		}
	}
}

// TestEnvironmentWithoutServiceReturnsUnavailable 校验依赖缺失返回 503 而不是空列表。
func TestEnvironmentWithoutServiceReturnsUnavailable(t *testing.T) {
	t.Parallel()
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	handler := httpapi.NewHandler(httpapi.Deps{Logger: logger.Slog()})
	recorder := doRequest(t, handler, http.MethodGet, listPath("host-1"), "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("环境查询服务未初始化时应返回 503，实际 %d", recorder.Code)
	}
	var body httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if body.Error.Code != httpapi.CodeInternal {
		t.Fatalf("错误代码应为 %s，实际 %q", httpapi.CodeInternal, body.Error.Code)
	}
}

// TestEnvironmentStorageFailureIsNotAnEmptyList 校验存储错误返回 500 且不伪造空列表。
func TestEnvironmentStorageFailureIsNotAnEmptyList(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentAPIFixture(t)
	// 关闭数据库连接，制造真实的存储层错误。
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
	recorder := doRequest(t, fixture.handler, http.MethodGet, listPath(fixture.host.ID.String()), "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("存储错误应返回 500，实际 %d，响应: %s", recorder.Code, recorder.Body.String())
	}
	var body httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if body.Error.Code != httpapi.CodeInternal {
		t.Fatalf("错误代码应为 %s，实际 %q", httpapi.CodeInternal, body.Error.Code)
	}
	if strings.Contains(recorder.Body.String(), "sqlite") {
		t.Fatalf("错误响应不得泄露存储实现细节: %s", recorder.Body.String())
	}
}

// TestEnvironmentQueryServiceIsReadOnly 校验查询路径不写入任何环境版本记录。
func TestEnvironmentQueryServiceIsReadOnly(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentAPIFixture(t)
	fixture.writeVersion(t, environmentSeed{
		ref:    fixture.base,
		number: 1,
		status: domain.BuildStatusSucceeded,
		usable: true,
	})
	before := listStoredVersions(t, fixture)
	if recorder := doRequest(
		t,
		fixture.handler,
		http.MethodGet,
		listPath(fixture.host.ID.String()),
		"",
	); recorder.Code != http.StatusOK {
		t.Fatalf("环境列表应返回 200，实际 %d", recorder.Code)
	}
	if recorder := doRequest(
		t,
		fixture.handler,
		http.MethodGet,
		versionPath("base", "base", "1", fixture.host.ID.String()),
		"",
	); recorder.Code != http.StatusOK {
		t.Fatalf("版本查询应返回 200，实际 %d", recorder.Code)
	}
	after := listStoredVersions(t, fixture)
	if len(before) != len(after) {
		t.Fatalf("只读查询不得写入版本记录：之前 %d 条，之后 %d 条", len(before), len(after))
	}
	for index := range before {
		if before[index].Status != after[index].Status ||
			before[index].ImageID != after[index].ImageID {
			t.Fatalf("只读查询不得修改版本记录: %+v vs %+v", before[index], after[index])
		}
	}
}

// listStoredVersions 用独立查询读取当前持久化的版本记录。
func listStoredVersions(t *testing.T, fixture *environmentAPIFixture) []domain.EnvironmentVersion {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	versions, err := fixture.db.ListEnvironmentVersions(ctx, storage.EnvironmentVersionFilter{
		HostID: fixture.host.ID,
	})
	if err != nil {
		t.Fatalf("读取环境版本失败: %v", err)
	}
	return versions
}

// TestEnvironmentVersionNotFoundIsDistinguishable 校验不存在的版本错误可被类别判断识别。
//
// 该用例直接断言应用层错误类别，确保 HTTP 层的 404 映射不是靠文本匹配。
func TestEnvironmentVersionNotFoundIsDistinguishable(t *testing.T) {
	t.Parallel()
	fixture := newEnvironmentAPIFixture(t)
	service, err := app.NewEnvironmentQueryService(fixture.db)
	if err != nil {
		t.Fatalf("构造环境查询服务失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	_, err = service.GetVersion(ctx, app.GetVersionRequest{
		MachineID: fixture.host.ID.String(),
		Ref:       fixture.base,
		Number:    1,
	})
	if !errors.Is(err, app.ErrEnvironmentVersionNotFound) {
		t.Fatalf("不存在的版本应返回 ErrEnvironmentVersionNotFound，实际 %v", err)
	}
}
