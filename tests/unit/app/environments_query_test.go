package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// envQueryFixture 是环境只读查询测试的运行环境。
type envQueryFixture struct {
	service *app.EnvironmentQueryService
	store   storage.Store
	db      *sqlite.DB
	host    domain.ExecutionHost
}

// newEnvQueryFixture 打开真实数据库、写入一个执行节点并构造查询服务。
func newEnvQueryFixture(t *testing.T) *envQueryFixture {
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
	host := createQueryHost(t, ctx, db, "host-1")
	service, err := app.NewEnvironmentQueryService(db)
	if err != nil {
		t.Fatalf("构造环境查询服务失败: %v", err)
	}
	return &envQueryFixture{service: service, store: db, db: db, host: host}
}

// createQueryHost 写入一个真实的执行节点记录。
func createQueryHost(
	t *testing.T,
	ctx context.Context,
	store storage.Store,
	id string,
) domain.ExecutionHost {
	t.Helper()
	host, err := domain.NewExecutionHost(
		domain.ID(id),
		"构建机",
		"192.0.2.10",
		22,
		"deploy",
		"/home/deploy/.lingting/agentflow",
		time.Now().UTC(),
		// 私钥认证是唯一受支持的认证方式：节点记录必须给出私钥路径。
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

// envVersionSeed 描述一个待写入的环境版本。
type envVersionSeed struct {
	// ref 是环境引用。
	ref domain.EnvironmentRef
	// number 是版本号。
	number int
	// status 是构建状态。
	status domain.BuildStatus
	// usable 为 true 时补齐镜像 ID 与构建时间，使版本真实可用。
	usable bool
	// parent 是父基础环境版本标签。
	parent string
	// summary 是失败摘要。
	summary string
}

// seedVersion 写入一个合法且自洽的环境版本记录。
func (f *envQueryFixture) seedVersion(t *testing.T, ctx context.Context, seed envVersionSeed) domain.EnvironmentVersion {
	t.Helper()
	version := domain.EnvironmentVersion{
		HostID:           f.host.ID,
		Ref:              seed.ref,
		Number:           seed.number,
		DockerfileSHA256: queryHash("dockerfile-" + seed.ref.Label() + "-" + domain.FormatVersionLabel(seed.number)),
		BuildConfigSHA256: queryHash(
			"config-" + seed.ref.Label() + "-" + domain.FormatVersionLabel(seed.number),
		),
		ContextSHA256: queryHash("context-" + seed.ref.Label()),
		ParentVersion: seed.parent,
		ImageRef:      "agentflow-" + string(seed.ref.Type) + "-" + seed.ref.Name + ":v0001-abcdefabcdef",
		Status:        seed.status,
		ErrorSummary:  seed.summary,
		CreatedAt:     time.Now().UTC().Truncate(time.Millisecond),
	}
	if seed.usable {
		version.ImageID = "sha256:" + queryRepeat("a", 64)
		builtAt := time.Now().UTC().Truncate(time.Millisecond)
		version.BuiltAt = &builtAt
	}
	if err := version.Validate(); err != nil {
		t.Fatalf("构造的版本应当合法: %v", err)
	}
	if err := f.store.CreateEnvironmentVersion(ctx, version); err != nil {
		t.Fatalf("写入环境版本失败: %v", err)
	}
	return version
}

// queryHash 返回测试用 64 位十六进制摘要。
func queryHash(seed string) string {
	sum := queryRepeat("0", 64)
	digits := []byte(sum)
	for index, char := range []byte(seed) {
		digits[index%64] = "0123456789abcdef"[int(char)%16]
	}
	return string(digits)
}

// queryRepeat 返回重复 n 次的字符。
func queryRepeat(char string, count int) string {
	buffer := make([]byte, count)
	for index := range buffer {
		buffer[index] = char[0]
	}
	return string(buffer)
}

// TestEnvironmentListSummarizesVersions 校验列表按环境归并版本并给出当前可用与最新版本。
func TestEnvironmentListSummarizesVersions(t *testing.T) {
	t.Parallel()
	fixture := newEnvQueryFixture(t)
	ctx := testContext(t)
	base, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	goRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	fixture.seedVersion(t, ctx, envVersionSeed{ref: base, number: 1, status: domain.BuildStatusSucceeded, usable: true})
	fixture.seedVersion(t, ctx, envVersionSeed{
		ref:    goRef,
		number: 1,
		status: domain.BuildStatusSucceeded,
		usable: true,
		parent: domain.FormatVersionLabel(1),
	})

	summaries, err := fixture.service.List(ctx, app.ListRequest{MachineID: fixture.host.ID.String()})
	if err != nil {
		t.Fatalf("查询环境列表失败: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("应返回 2 个环境，实际 %d", len(summaries))
	}
	byLabel := make(map[string]app.EnvironmentSummary, len(summaries))
	for _, summary := range summaries {
		byLabel[summary.Ref.Label()] = summary
	}
	for _, want := range []domain.EnvironmentRef{base, goRef} {
		summary, ok := byLabel[want.Label()]
		if !ok {
			t.Fatalf("环境列表缺少 %s", want.Label())
		}
		if summary.CurrentVersion != "v0001" || summary.LatestVersion != "v0001" {
			t.Fatalf("%s 的版本汇总不符: %+v", want.Label(), summary)
		}
		if len(summary.Versions) != 1 {
			t.Fatalf("%s 的版本数应为 1，实际 %d", want.Label(), len(summary.Versions))
		}
	}
}

// TestEnvironmentListDistinguishesCurrentFromLatest 校验当前可用版本与最新创建版本分别表达。
func TestEnvironmentListDistinguishesCurrentFromLatest(t *testing.T) {
	t.Parallel()
	fixture := newEnvQueryFixture(t)
	ctx := testContext(t)
	base, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	fixture.seedVersion(t, ctx, envVersionSeed{ref: base, number: 1, status: domain.BuildStatusSucceeded, usable: true})
	fixture.seedVersion(t, ctx, envVersionSeed{
		ref:     base,
		number:  2,
		status:  domain.BuildStatusFailed,
		summary: "build failed: exit 1",
	})
	fixture.seedVersion(t, ctx, envVersionSeed{ref: base, number: 3, status: domain.BuildStatusPending})

	summary, err := fixture.service.Get(ctx, app.GetRequest{
		MachineID: fixture.host.ID.String(),
		Ref:       base,
	})
	if err != nil {
		t.Fatalf("查询环境失败: %v", err)
	}
	if summary.LatestVersion != "v0003" {
		t.Fatalf("最新版本应为 v0003，实际 %s", summary.LatestVersion)
	}
	if summary.CurrentVersion != "v0001" {
		t.Fatalf("当前可用版本应为 v0001（失败与未构建版本不可用），实际 %q", summary.CurrentVersion)
	}
	if len(summary.Versions) != 3 {
		t.Fatalf("版本数应为 3，实际 %d", len(summary.Versions))
	}
}

// TestEnvironmentListWithoutUsableVersionKeepsCurrentEmpty 校验没有可用版本时当前版本为空。
func TestEnvironmentListWithoutUsableVersionKeepsCurrentEmpty(t *testing.T) {
	t.Parallel()
	fixture := newEnvQueryFixture(t)
	ctx := testContext(t)
	base, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	fixture.seedVersion(t, ctx, envVersionSeed{ref: base, number: 1, status: domain.BuildStatusBuilding})

	summaries, err := fixture.service.List(ctx, app.ListRequest{MachineID: fixture.host.ID.String()})
	if err != nil {
		t.Fatalf("查询环境列表失败: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("应返回 1 个环境，实际 %d", len(summaries))
	}
	if summaries[0].CurrentVersion != "" {
		t.Fatalf("没有可用版本时当前版本应为空，实际 %q", summaries[0].CurrentVersion)
	}
	if summaries[0].LatestVersion != "v0001" {
		t.Fatalf("最新版本应为 v0001，实际 %s", summaries[0].LatestVersion)
	}
}

// TestEnvironmentListIsSortedByVersion 校验版本列表按版本号升序且顺序稳定。
func TestEnvironmentListIsSortedByVersion(t *testing.T) {
	t.Parallel()
	fixture := newEnvQueryFixture(t)
	ctx := testContext(t)
	base, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	// 故意按非升序写入：顺序必须由存储层查询决定，而不是写入顺序。
	for _, number := range []int{3, 1, 2} {
		fixture.seedVersion(t, ctx, envVersionSeed{
			ref:    base,
			number: number,
			status: domain.BuildStatusPending,
		})
	}
	for round := 0; round < 3; round++ {
		summaries, err := fixture.service.List(ctx, app.ListRequest{MachineID: fixture.host.ID.String()})
		if err != nil {
			t.Fatalf("查询环境列表失败: %v", err)
		}
		if len(summaries) != 1 {
			t.Fatalf("应返回 1 个环境，实际 %d", len(summaries))
		}
		numbers := make([]int, 0, len(summaries[0].Versions))
		for _, version := range summaries[0].Versions {
			numbers = append(numbers, version.Number)
		}
		want := []int{1, 2, 3}
		if len(numbers) != len(want) {
			t.Fatalf("版本数不符: %v", numbers)
		}
		for index, number := range want {
			if numbers[index] != number {
				t.Fatalf("第 %d 次查询的版本顺序应为 %v，实际 %v", round+1, want, numbers)
			}
		}
	}
}

// TestEnvironmentListEmptyHostReturnsEmptyList 校验没有任何版本时返回空列表而不是错误。
func TestEnvironmentListEmptyHostReturnsEmptyList(t *testing.T) {
	t.Parallel()
	fixture := newEnvQueryFixture(t)
	summaries, err := fixture.service.List(testContext(t), app.ListRequest{
		MachineID: fixture.host.ID.String(),
	})
	if err != nil {
		t.Fatalf("没有版本时列表应成功返回: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("没有版本时应返回空列表，实际 %d 项", len(summaries))
	}
}

// TestEnvironmentListRejectsUnknownHost 校验不存在的执行节点返回明确的 404 类别。
func TestEnvironmentListRejectsUnknownHost(t *testing.T) {
	t.Parallel()
	fixture := newEnvQueryFixture(t)
	_, err := fixture.service.List(testContext(t), app.ListRequest{MachineID: "missing-host"})
	if !errors.Is(err, app.ErrEnvironmentHostNotFound) {
		t.Fatalf("不存在的节点应返回 ErrEnvironmentHostNotFound，实际 %v", err)
	}
}

// TestEnvironmentListRejectsEmptyHost 校验缺少节点标识返回参数错误类别。
func TestEnvironmentListRejectsEmptyHost(t *testing.T) {
	t.Parallel()
	fixture := newEnvQueryFixture(t)
	_, err := fixture.service.List(testContext(t), app.ListRequest{})
	if !errors.Is(err, app.ErrEnvironmentInvalidInput) {
		t.Fatalf("空节点标识应返回 ErrEnvironmentInvalidInput，实际 %v", err)
	}
}

// TestEnvironmentGetVersionReturnsMetadata 校验单版本查询返回真实构建状态与元数据。
func TestEnvironmentGetVersionReturnsMetadata(t *testing.T) {
	t.Parallel()
	fixture := newEnvQueryFixture(t)
	ctx := testContext(t)
	base, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	fixture.seedVersion(t, ctx, envVersionSeed{ref: base, number: 1, status: domain.BuildStatusSucceeded, usable: true})
	fixture.seedVersion(t, ctx, envVersionSeed{
		ref:     base,
		number:  2,
		status:  domain.BuildStatusFailed,
		summary: "镜像核实失败",
	})

	usable, err := fixture.service.GetVersion(ctx, app.GetVersionRequest{
		MachineID: fixture.host.ID.String(),
		Ref:       base,
		Number:    1,
	})
	if err != nil {
		t.Fatalf("查询版本失败: %v", err)
	}
	if usable.Status != domain.BuildStatusSucceeded || !usable.Usable() {
		t.Fatalf("v0001 应为可用版本: %+v", usable)
	}
	if usable.ImageID == "" || usable.BuiltAt == nil {
		t.Fatalf("可用版本必须带镜像 ID 与构建时间: %+v", usable)
	}

	failed, err := fixture.service.GetVersion(ctx, app.GetVersionRequest{
		MachineID: fixture.host.ID.String(),
		Ref:       base,
		Number:    2,
	})
	if err != nil {
		t.Fatalf("查询失败版本失败: %v", err)
	}
	if failed.Status != domain.BuildStatusFailed {
		t.Fatalf("v0002 的构建状态应为 failed，实际 %s", failed.Status)
	}
	if failed.Usable() {
		t.Fatal("构建失败的版本不得可用")
	}
	if failed.ErrorSummary != "镜像核实失败" {
		t.Fatalf("失败摘要应如实返回，实际 %q", failed.ErrorSummary)
	}
}

// TestEnvironmentGetVersionRejectsMissingVersion 校验不存在的版本返回明确的 404 类别。
func TestEnvironmentGetVersionRejectsMissingVersion(t *testing.T) {
	t.Parallel()
	fixture := newEnvQueryFixture(t)
	ctx := testContext(t)
	base, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	fixture.seedVersion(t, ctx, envVersionSeed{ref: base, number: 1, status: domain.BuildStatusPending})

	_, err = fixture.service.GetVersion(ctx, app.GetVersionRequest{
		MachineID: fixture.host.ID.String(),
		Ref:       base,
		Number:    9,
	})
	if !errors.Is(err, app.ErrEnvironmentVersionNotFound) {
		t.Fatalf("不存在的版本应返回 ErrEnvironmentVersionNotFound，实际 %v", err)
	}
}

// TestEnvironmentGetVersionRejectsInvalidInput 校验非法环境引用与版本号被拒绝。
func TestEnvironmentGetVersionRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	fixture := newEnvQueryFixture(t)
	ctx := testContext(t)
	validRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	cases := []struct {
		name   string
		ref    domain.EnvironmentRef
		number int
	}{
		{
			name:   "未知环境类型",
			ref:    domain.EnvironmentRef{Type: domain.EnvironmentType("unknown"), Name: "base"},
			number: 1,
		},
		{
			name:   "非法环境名称",
			ref:    domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "Bad Name"},
			number: 1,
		},
		{
			name:   "基础环境名称不符",
			ref:    domain.EnvironmentRef{Type: domain.EnvironmentTypeBase, Name: "other"},
			number: 1,
		},
		{
			name:   "版本号为零",
			ref:    validRef,
			number: 0,
		},
		{
			name:   "版本号超出上限",
			ref:    validRef,
			number: domain.MaxEnvironmentVersionNumber + 1,
		},
	}
	for _, testCase := range cases {
		_, err := fixture.service.GetVersion(ctx, app.GetVersionRequest{
			MachineID: fixture.host.ID.String(),
			Ref:       testCase.ref,
			Number:    testCase.number,
		})
		if !errors.Is(err, app.ErrEnvironmentInvalidInput) {
			t.Fatalf("%s 应返回 ErrEnvironmentInvalidInput，实际 %v", testCase.name, err)
		}
	}
}

// TestEnvironmentGetWithoutVersionsReturnsNotFound 校验没有版本的环境按不存在处理。
func TestEnvironmentGetWithoutVersionsReturnsNotFound(t *testing.T) {
	t.Parallel()
	fixture := newEnvQueryFixture(t)
	goRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	_, err = fixture.service.Get(testContext(t), app.GetRequest{
		MachineID: fixture.host.ID.String(),
		Ref:       goRef,
	})
	if !errors.Is(err, app.ErrEnvironmentNotFound) {
		t.Fatalf("没有版本的环境应返回 ErrEnvironmentNotFound，实际 %v", err)
	}
}

// TestEnvironmentQueryStorageFailureIsNotAnEmptyList 校验存储错误不会被伪装成空列表。
func TestEnvironmentQueryStorageFailureIsNotAnEmptyList(t *testing.T) {
	t.Parallel()
	fixture := newEnvQueryFixture(t)
	ctx := testContext(t)
	// 关闭数据库连接，制造真实的存储层错误。
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
	_, err := fixture.service.List(ctx, app.ListRequest{MachineID: fixture.host.ID.String()})
	if err == nil {
		t.Fatal("存储错误不得返回成功的空列表")
	}
	if errors.Is(err, app.ErrEnvironmentHostNotFound) || errors.Is(err, app.ErrEnvironmentNotFound) {
		t.Fatalf("存储错误不应被归类为资源不存在: %v", err)
	}
}

// TestEnvironmentQueryServiceRequiresStore 校验缺少存储时构造失败。
func TestEnvironmentQueryServiceRequiresStore(t *testing.T) {
	t.Parallel()
	if _, err := app.NewEnvironmentQueryService(nil); err == nil {
		t.Fatal("存储为空时应当构造失败")
	}
}
