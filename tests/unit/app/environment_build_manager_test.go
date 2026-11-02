package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// fakeBuildRunner 是真实远程构建的受控替身。
//
// 它不建立 SSH 连接、不调用 Docker：测试关注的是管理器的受理、去重、取消与收敛语义，
// 真正的构建流程由 app.EnvironmentService.Build 承担。
type fakeBuildRunner struct {
	mu      sync.Mutex
	calls   []app.BuildRequest
	started chan struct{}
	release chan struct{}
	// waitForCancel 为 true 时构造一直阻塞到 ctx 取消，用于验证停止语义。
	waitForCancel bool
	canceled      bool
}

// Build 实现 app.EnvironmentBuildRunner。
func (r *fakeBuildRunner) Build(
	ctx context.Context,
	request app.BuildRequest,
) (app.BuildResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, request)
	started := r.started
	release := r.release
	waitForCancel := r.waitForCancel
	r.mu.Unlock()
	if started != nil {
		close(started)
	}
	if waitForCancel {
		<-ctx.Done()
		r.mu.Lock()
		r.canceled = true
		r.mu.Unlock()
		return app.BuildResult{}, ctx.Err()
	}
	if release != nil {
		<-release
	}
	return app.BuildResult{Version: domain.EnvironmentVersion{Number: request.VersionNumber}}, nil
}

// requests 返回收到的构建请求副本。
func (r *fakeBuildRunner) requests() []app.BuildRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]app.BuildRequest(nil), r.calls...)
}

// wasCanceled 报告构建是否观察到上下文取消。
func (r *fakeBuildRunner) wasCanceled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.canceled
}

// buildManagerFixture 是后台构建管理器测试的运行环境。
type buildManagerFixture struct {
	db     *sqlite.DB
	host   domain.ExecutionHost
	runner *fakeBuildRunner
	ref    domain.EnvironmentRef
}

// newBuildManagerFixture 打开真实数据库并写入执行节点。
func newBuildManagerFixture(t *testing.T) *buildManagerFixture {
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
	host := createQueryHost(t, ctx, db, "build-host")
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	return &buildManagerFixture{
		db:     db,
		host:   host,
		ref:    ref,
		runner: &fakeBuildRunner{},
	}
}

// manager 构造并启动一个后台构建管理器。
func (f *buildManagerFixture) manager(t *testing.T) *app.EnvironmentBuildManager {
	t.Helper()
	manager, err := app.NewEnvironmentBuildManager(app.EnvironmentBuildManagerOptions{
		Logger: testkit.Slog(),
		Runner: f.runner,
		Store:  f.db,
	})
	if err != nil {
		t.Fatalf("构造构建管理器失败: %v", err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("启动构建管理器失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if stopErr := manager.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止构建管理器失败: %v", stopErr)
		}
	})
	return manager
}

// writeVersion 写入一个环境版本记录。
func (f *buildManagerFixture) writeVersion(
	t *testing.T,
	number int,
	status domain.BuildStatus,
) domain.EnvironmentVersion {
	t.Helper()
	ctx := context.Background()
	version := domain.EnvironmentVersion{
		HostID:            f.host.ID,
		Ref:               f.ref,
		Number:            number,
		DockerfileSHA256:  buildHash("dockerfile-" + domain.FormatVersionLabel(number)),
		BuildConfigSHA256: buildHash("config-" + domain.FormatVersionLabel(number)),
		ContextSHA256:     buildHash("context"),
		ImageRef:          "agentflow-base:" + domain.FormatVersionLabel(number) + "-aaaaaaaaaaaa",
		Status:            status,
		CreatedAt:         time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := version.Validate(); err != nil {
		t.Fatalf("构造的版本应当合法: %v", err)
	}
	if err := f.db.CreateEnvironmentVersion(ctx, version); err != nil {
		t.Fatalf("写入环境版本失败: %v", err)
	}
	return version
}

// buildHash 返回测试用的 64 位十六进制摘要。
func buildHash(seed string) string {
	digits := []byte(strings.Repeat("0", 64))
	for index, char := range []byte(seed) {
		digits[index%64] = "0123456789abcdef"[int(char)%16]
	}
	return string(digits)
}

// TestBuildManagerAcceptanceAndDedupe 验证受理语义与重复提交拦截。
func TestBuildManagerAcceptanceAndDedupe(t *testing.T) {
	t.Parallel()
	fixture := newBuildManagerFixture(t)
	fixture.writeVersion(t, 1, domain.BuildStatusPending)
	release := make(chan struct{})
	fixture.runner.release = release
	manager := fixture.manager(t)

	task, err := manager.Submit(context.Background(), app.BuildRequest{
		MachineID:     fixture.host.ID.String(),
		Ref:           fixture.ref,
		VersionNumber: 1,
	})
	if err != nil {
		t.Fatalf("受理构建失败: %v", err)
	}
	if task.VersionNumber != 1 || task.AcceptedAt.IsZero() {
		t.Fatalf("受理结果不完整: %+v", task)
	}
	// 受理时读到的版本快照必须是真实记录，而不是伪造的成功状态。
	if task.Version.Status != domain.BuildStatusPending {
		t.Fatalf("受理快照不应改写构建状态: %+v", task.Version)
	}

	// 同一环境在途构建期间再次提交：必须拒绝，且不能启动第二个构建。
	if _, err := manager.Submit(context.Background(), app.BuildRequest{
		MachineID:     fixture.host.ID.String(),
		Ref:           fixture.ref,
		VersionNumber: 1,
	}); !errors.Is(err, app.ErrEnvironmentBuildInProgress) {
		t.Fatalf("重复提交应返回构建进行中，实际: %v", err)
	}
	if _, running := manager.Status(fixture.host.ID, fixture.ref); !running {
		t.Fatal("在途构建必须可通过 Status 观察到")
	}

	close(release)
	if !manager.Wait(fixture.host.ID, fixture.ref) {
		t.Fatal("等待在途构建失败")
	}
	if calls := len(fixture.runner.requests()); calls != 1 {
		t.Fatalf("构建执行次数不符: %d", calls)
	}
	if _, running := manager.Status(fixture.host.ID, fixture.ref); running {
		t.Fatal("构建结束后不应再有在途记录")
	}
}

// TestBuildManagerRejectsUnknownTargets 验证受理前的节点与版本校验。
func TestBuildManagerRejectsUnknownTargets(t *testing.T) {
	t.Parallel()
	fixture := newBuildManagerFixture(t)
	manager := fixture.manager(t)
	cases := []struct {
		name    string
		request app.BuildRequest
		want    error
	}{
		{
			name: "节点标识为空",
			request: app.BuildRequest{
				MachineID:     "",
				Ref:           fixture.ref,
				VersionNumber: 1,
			},
			want: app.ErrEnvironmentInvalidInput,
		},
		{
			name: "节点不存在",
			request: app.BuildRequest{
				MachineID:     "missing-host",
				Ref:           fixture.ref,
				VersionNumber: 1,
			},
			want: app.ErrEnvironmentHostNotFound,
		},
		{
			name: "版本号为 0",
			request: app.BuildRequest{
				MachineID:     fixture.host.ID.String(),
				Ref:           fixture.ref,
				VersionNumber: 0,
			},
			want: app.ErrEnvironmentInvalidInput,
		},
		{
			name: "版本不存在",
			request: app.BuildRequest{
				MachineID:     fixture.host.ID.String(),
				Ref:           fixture.ref,
				VersionNumber: 7,
			},
			want: app.ErrEnvironmentVersionNotFound,
		},
	}
	for _, testCase := range cases {
		if _, err := manager.Submit(context.Background(), testCase.request); !errors.Is(err, testCase.want) {
			t.Fatalf("%s: 期望 %v，实际 %v", testCase.name, testCase.want, err)
		}
	}
	if calls := len(fixture.runner.requests()); calls != 0 {
		t.Fatalf("非法请求不得启动构建，实际执行 %d 次", calls)
	}
}

// TestBuildManagerSubmitBeforeStartIsRejected 验证未启动的管理器直接拒绝受理。
func TestBuildManagerSubmitBeforeStartIsRejected(t *testing.T) {
	t.Parallel()
	fixture := newBuildManagerFixture(t)
	fixture.writeVersion(t, 1, domain.BuildStatusPending)
	manager, err := app.NewEnvironmentBuildManager(app.EnvironmentBuildManagerOptions{
		Logger: testkit.Slog(),
		Runner: fixture.runner,
		Store:  fixture.db,
	})
	if err != nil {
		t.Fatalf("构造构建管理器失败: %v", err)
	}
	_, err = manager.Submit(context.Background(), app.BuildRequest{
		MachineID:     fixture.host.ID.String(),
		Ref:           fixture.ref,
		VersionNumber: 1,
	})
	if !errors.Is(err, app.ErrEnvironmentBuildStopped) {
		t.Fatalf("未启动的管理器应拒绝受理，实际: %v", err)
	}
}

// TestBuildManagerConvergesLeftoverBuilding 验证启动时收敛遗留的 building 记录。
//
// 进程退出后没有执行者能证明构建仍在进行，因此收敛为 uncertain：既不能当成成功，
// 也不能伪造失败。
func TestBuildManagerConvergesLeftoverBuilding(t *testing.T) {
	t.Parallel()
	fixture := newBuildManagerFixture(t)
	fixture.writeVersion(t, 1, domain.BuildStatusBuilding)
	fixture.writeVersion(t, 2, domain.BuildStatusPending)
	fixture.manager(t)

	ctx := context.Background()
	stale, err := fixture.db.GetEnvironmentVersion(ctx, fixture.host.ID, fixture.ref, 1)
	if err != nil {
		t.Fatalf("读取遗留版本失败: %v", err)
	}
	if stale.Status != domain.BuildStatusUncertain {
		t.Fatalf("遗留构建应收敛为 uncertain，实际 %s", stale.Status)
	}
	if stale.ErrorSummary == "" {
		t.Fatal("收敛为 uncertain 时必须记录可诊断原因")
	}
	if stale.Usable() {
		t.Fatal("uncertain 版本不得标记为可用")
	}
	pending, err := fixture.db.GetEnvironmentVersion(ctx, fixture.host.ID, fixture.ref, 2)
	if err != nil {
		t.Fatalf("读取未构建版本失败: %v", err)
	}
	if pending.Status != domain.BuildStatusPending {
		t.Fatalf("未开始构建的版本不应被改写，实际 %s", pending.Status)
	}
}

// TestBuildManagerStopCancelsInFlightBuild 验证停止会取消并等待在途构建收敛。
func TestBuildManagerStopCancelsInFlightBuild(t *testing.T) {
	t.Parallel()
	fixture := newBuildManagerFixture(t)
	fixture.writeVersion(t, 1, domain.BuildStatusPending)
	fixture.runner.waitForCancel = true
	started := make(chan struct{})
	fixture.runner.started = started

	manager, err := app.NewEnvironmentBuildManager(app.EnvironmentBuildManagerOptions{
		Logger: testkit.Slog(),
		Runner: fixture.runner,
		Store:  fixture.db,
	})
	if err != nil {
		t.Fatalf("构造构建管理器失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("启动构建管理器失败: %v", err)
	}
	if _, err := manager.Submit(context.Background(), app.BuildRequest{
		MachineID:     fixture.host.ID.String(),
		Ref:           fixture.ref,
		VersionNumber: 1,
	}); err != nil {
		t.Fatalf("受理构建失败: %v", err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("等待构建开始超时")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	if err := manager.Stop(stopCtx); err != nil {
		t.Fatalf("停止构建管理器失败: %v", err)
	}
	if !fixture.runner.wasCanceled() {
		t.Fatal("停止时必须取消在途构建")
	}
	if _, running := manager.Status(fixture.host.ID, fixture.ref); running {
		t.Fatal("停止后不应保留在途记录")
	}
	// 停止后再次受理必须被拒绝：服务已停止不是空操作成功。
	_, submitErr := manager.Submit(context.Background(), app.BuildRequest{
		MachineID:     fixture.host.ID.String(),
		Ref:           fixture.ref,
		VersionNumber: 1,
	})
	if !errors.Is(submitErr, app.ErrEnvironmentBuildStopped) {
		t.Fatalf("停止后受理应被拒绝，实际: %v", submitErr)
	}
}

// TestEnvironmentVersionConditionalUpdateGuardsStaleResult 验证迟到的构建结果不会覆盖收敛结论。
func TestEnvironmentVersionConditionalUpdateGuardsStaleResult(t *testing.T) {
	t.Parallel()
	fixture := newBuildManagerFixture(t)
	created := fixture.writeVersion(t, 1, domain.BuildStatusBuilding)
	ctx := context.Background()

	// 收敛为 uncertain：前置状态仍是 building，写入成功。
	uncertain := created
	if err := uncertain.MarkUncertain("服务重启时构建结果无法确认"); err != nil {
		t.Fatalf("标记结果不确定失败: %v", err)
	}
	if err := fixture.db.UpdateEnvironmentVersionIfStatus(
		ctx,
		uncertain,
		[]domain.BuildStatus{domain.BuildStatusBuilding},
	); err != nil {
		t.Fatalf("收敛遗留构建失败: %v", err)
	}

	// 迟到的成功结果：前置状态已不是 building，必须被拒绝。
	late := created
	builtAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := late.MarkBuilt("sha256:"+strings.Repeat("a", 64), builtAt); err != nil {
		t.Fatalf("构造迟到结果失败: %v", err)
	}
	err := fixture.db.UpdateEnvironmentVersionIfStatus(
		ctx,
		late,
		[]domain.BuildStatus{domain.BuildStatusBuilding},
	)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("迟到结果应返回冲突，实际: %v", err)
	}
	stored, readErr := fixture.db.GetEnvironmentVersion(ctx, fixture.host.ID, fixture.ref, 1)
	if readErr != nil {
		t.Fatalf("读取版本失败: %v", readErr)
	}
	if stored.Status != domain.BuildStatusUncertain {
		t.Fatalf("已确认的收敛结论不得被覆盖，实际 %s", stored.Status)
	}
}
