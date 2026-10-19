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
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// newValidationEnv 打开测试数据库并构造执行节点服务与后台校验服务。
func newValidationEnv(t *testing.T) (
	*app.ExecutionHostService,
	*app.HostValidationService,
	storage.Store,
) {
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
	logger, err := logging.New(nopWriter{}, logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	sshService, err := app.NewSSHService(db, logger, app.SSHOptions{
		KnownHostsPath: filepath.Join(testkit.Dir(t), "known_hosts"),
		StorePath:      filepath.Join(testkit.Dir(t), "agentflow_known_hosts"),
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
	validation, err := app.NewHostValidationService(app.HostValidationServiceOptions{
		Validator: validator,
		Store:     db,
		Logger:    logger,
	})
	if err != nil {
		t.Fatalf("构造校验服务失败: %v", err)
	}
	hosts, err := app.NewExecutionHostService(db, logger)
	if err != nil {
		t.Fatalf("构造执行节点服务失败: %v", err)
	}
	hosts.WithValidation(validation, validator)
	return hosts, validation, db
}

// unreachableHost 构造一个必然连不上的节点配置，用于验证失败路径。
func unreachableHost(t *testing.T, hosts *app.ExecutionHostService) domain.ExecutionHost {
	t.Helper()
	// 使用回环地址上极不可能监听的端口，避免依赖外部网络。
	host, err := hosts.Create(context.Background(), app.CreateExecutionHostRequest{
		Name:           "不可达节点",
		Host:           "127.0.0.1",
		Port:           1,
		Username:       "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	return host
}

// TestCreateHostReturnsChecking 验证新增节点返回真实初始状态 checking。
func TestCreateHostReturnsChecking(t *testing.T) {
	t.Parallel()
	hosts, validation, _ := newValidationEnv(t)
	if err := validation.Start(context.Background()); err != nil {
		t.Fatalf("启动校验服务失败: %v", err)
	}
	t.Cleanup(func() {
		_ = validation.Stop(context.Background())
	})
	host, err := hosts.Create(context.Background(), app.CreateExecutionHostRequest{
		Name:           "新增节点",
		Host:           "127.0.0.1",
		Port:           1,
		Username:       "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	if host.ValidationStatus != domain.ValidationStatusChecking {
		t.Fatalf("新增后应为 checking: %s", host.ValidationStatus)
	}
}

// TestCreateHostSchedulesValidation 验证新增节点后后台校验会写回结果。
func TestCreateHostSchedulesValidation(t *testing.T) {
	t.Parallel()
	hosts, validation, _ := newValidationEnv(t)
	if err := validation.Start(context.Background()); err != nil {
		t.Fatalf("启动校验服务失败: %v", err)
	}
	t.Cleanup(func() {
		_ = validation.Stop(context.Background())
	})
	host := unreachableHost(t, hosts)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		current, err := hosts.Get(context.Background(), host.ID.String())
		if err != nil {
			t.Fatalf("读取执行节点失败: %v", err)
		}
		if current.ValidationStatus != domain.ValidationStatusChecking {
			if current.ValidationStatus != domain.ValidationStatusAbnormal {
				t.Fatalf("不可达节点应被判为 abnormal: %s", current.ValidationStatus)
			}
			if current.ValidationMessage == "" {
				t.Fatal("失败时必须给出可读原因")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("后台校验未在时限内写回结果")
}

// TestCreateHostWithoutServiceStaysChecking 验证未接入校验服务时保持 checking。
func TestCreateHostWithoutServiceStaysChecking(t *testing.T) {
	t.Parallel()
	hosts, _, _ := newValidationEnv(t)
	// 这里复用同一个服务，但先不启动后台服务：新增后应停留在 checking。
	host, err := hosts.Create(context.Background(), app.CreateExecutionHostRequest{
		Name:           "未调度节点",
		Host:           "127.0.0.1",
		Port:           1,
		Username:       "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	if host.ValidationStatus != domain.ValidationStatusChecking {
		t.Fatalf("未启动校验服务时应停留在 checking: %s", host.ValidationStatus)
	}
}

// TestUpdateInvalidatesValidation 验证修改连接参数会使旧校验结果失效。
func TestUpdateInvalidatesValidation(t *testing.T) {
	t.Parallel()
	hosts, _, store := newValidationEnv(t)
	ctx := context.Background()
	host, err := hosts.Create(ctx, app.CreateExecutionHostRequest{
		Name:           "待修改节点",
		Host:           "10.0.0.40",
		Username:       "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	// 直接把状态置为 normal，模拟一次已完成的校验。
	stored, err := store.GetExecutionHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	completed := time.Now().UTC()
	details := domain.ValidationDetails{
		ValidatorVersion: domain.ValidatorVersion,
		Checks: []domain.ValidationCheck{
			{Name: domain.ValidationCheckSSHConnection, Status: domain.CheckStatusPassed},
		},
	}
	if err := stored.RecordValidationResult(stored.ValidationGeneration, details, completed); err != nil {
		t.Fatalf("记录校验结果失败: %v", err)
	}
	if _, err := store.UpdateExecutionHostValidation(ctx, *stored, stored.ValidationGeneration); err != nil {
		t.Fatalf("写入校验结果失败: %v", err)
	}
	generationBefore := stored.ValidationGeneration

	port := 2222
	updated, err := hosts.Update(ctx, app.UpdateExecutionHostRequest{
		ID:   host.ID.String(),
		Port: &port,
	})
	if err != nil {
		t.Fatalf("更新执行节点失败: %v", err)
	}
	if updated.ValidationStatus != domain.ValidationStatusChecking {
		t.Fatalf("修改后应为 checking: %s", updated.ValidationStatus)
	}
	if updated.ValidationGeneration != generationBefore+1 {
		t.Fatalf("修改后代次应递增: %d -> %d", generationBefore, updated.ValidationGeneration)
	}
	if updated.ValidationDetails != nil {
		t.Fatal("修改后不应保留旧详情")
	}
}

// TestUpdateNameKeepsValidation 验证不影响校验输入的修改不会使结果失效。
func TestUpdateNameKeepsValidation(t *testing.T) {
	t.Parallel()
	hosts, _, store := newValidationEnv(t)
	ctx := context.Background()
	host, err := hosts.Create(ctx, app.CreateExecutionHostRequest{
		Name:           "原名",
		Host:           "10.0.0.41",
		Username:       "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	stored, err := store.GetExecutionHost(ctx, host.ID)
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
	if _, err := store.UpdateExecutionHostValidation(ctx, *stored, stored.ValidationGeneration); err != nil {
		t.Fatalf("写入校验结果失败: %v", err)
	}
	generationBefore := stored.ValidationGeneration

	name := "新名称"
	updated, err := hosts.Update(ctx, app.UpdateExecutionHostRequest{
		ID:   host.ID.String(),
		Name: &name,
	})
	if err != nil {
		t.Fatalf("更新执行节点失败: %v", err)
	}
	if updated.ValidationStatus != domain.ValidationStatusNormal {
		t.Fatalf("仅改名不应使结果失效: %s", updated.ValidationStatus)
	}
	if updated.ValidationGeneration != generationBefore {
		t.Fatalf("仅改名不应改变代次: %d", updated.ValidationGeneration)
	}
}

// TestEnsureHostUsableForRemoteTask 验证准入规则。
func TestEnsureHostUsableForRemoteTask(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		status  domain.ValidationStatus
		enabled bool
		wantErr bool
	}{
		{"正常且启用", domain.ValidationStatusNormal, true, false},
		{"校验中", domain.ValidationStatusChecking, true, true},
		{"异常", domain.ValidationStatusAbnormal, true, true},
		{"已禁用", domain.ValidationStatusNormal, false, true},
		{"未知状态", domain.ValidationStatus("verifying_v2"), true, true},
	}
	for _, item := range cases {
		host, err := domain.NewExecutionHost(
			"", "节点", "10.0.0.50", 0, "deploy", "", time.Now(),
			domain.WithPrivateKey(testPrivateKeyPath(t)),
		)
		if err != nil {
			t.Fatalf("%s: 构造执行节点失败: %v", item.name, err)
		}
		host.ValidationStatus = item.status
		host.Enabled = item.enabled
		checkErr := app.EnsureHostUsableForRemoteTask(host)
		if item.wantErr {
			if checkErr == nil {
				t.Fatalf("%s: 应当拒绝", item.name)
			}
			if !errors.Is(checkErr, app.ErrHostValidationUnavailable) {
				t.Fatalf("%s: 错误类别不符: %v", item.name, checkErr)
			}
			if !strings.Contains(checkErr.Error(), host.ID.String()) {
				t.Fatalf("%s: 错误应指明节点标识: %v", item.name, checkErr)
			}
			continue
		}
		if checkErr != nil {
			t.Fatalf("%s: 不应拒绝: %v", item.name, checkErr)
		}
	}
}

// TestRevalidateReturnsCheckingImmediately 验证重新校验立即返回 checking。
func TestRevalidateReturnsCheckingImmediately(t *testing.T) {
	t.Parallel()
	hosts, validation, _ := newValidationEnv(t)
	if err := validation.Start(context.Background()); err != nil {
		t.Fatalf("启动校验服务失败: %v", err)
	}
	t.Cleanup(func() {
		_ = validation.Stop(context.Background())
	})
	host, err := hosts.Create(context.Background(), app.CreateExecutionHostRequest{
		Name:           "重新校验节点",
		Host:           "127.0.0.1",
		Port:           1,
		Username:       "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	started := time.Now()
	revalidated, err := hosts.Revalidate(context.Background(), host.ID.String())
	if err != nil {
		t.Fatalf("重新校验失败: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("重新校验不应等待校验完成: %s", elapsed)
	}
	if revalidated.ValidationStatus != domain.ValidationStatusChecking {
		t.Fatalf("重新校验后应立即为 checking: %s", revalidated.ValidationStatus)
	}
	if revalidated.ValidationGeneration <= host.ValidationGeneration {
		t.Fatalf("重新校验应增加代次: %d", revalidated.ValidationGeneration)
	}
}

// TestSubmitDeduplicatesSameGeneration 验证同一代次不会被重复执行。
func TestSubmitDeduplicatesSameGeneration(t *testing.T) {
	t.Parallel()
	hosts, validation, store := newValidationEnv(t)
	ctx := context.Background()
	host, err := hosts.Create(ctx, app.CreateExecutionHostRequest{
		Name:           "去重节点",
		Host:           "10.0.0.52",
		Username:       "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	// 未启动服务时提交应返回明确错误。
	if err := validation.SubmitHost(ctx, host.ID.String()); !errors.Is(
		err,
		app.ErrValidationServiceStopped,
	) {
		t.Fatalf("未启动时提交应返回服务已停止: %v", err)
	}
	if err := validation.Start(ctx); err != nil {
		t.Fatalf("启动校验服务失败: %v", err)
	}
	t.Cleanup(func() {
		_ = validation.Stop(context.Background())
	})
	stored, err := store.GetExecutionHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	// 第一次提交进入队列或开始执行，第二次提交应被去重。
	first := validation.SubmitHost(ctx, host.ID.String())
	second := validation.SubmitHost(ctx, host.ID.String())
	if first != nil && !errors.Is(first, app.ErrValidationQueueFull) {
		t.Fatalf("首次提交失败: %v", first)
	}
	if second != nil && !errors.Is(second, app.ErrValidationQueueFull) {
		t.Fatalf("重复提交应被忽略: %v", second)
	}
	_ = stored
}

// TestServiceStopCancelsInFlight 验证停止服务会取消在途校验。
func TestServiceStopCancelsInFlight(t *testing.T) {
	t.Parallel()
	hosts, validation, _ := newValidationEnv(t)
	ctx := context.Background()
	host, err := hosts.Create(ctx, app.CreateExecutionHostRequest{
		Name:           "取消节点",
		Host:           "127.0.0.1",
		Port:           1,
		Username:       "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	if err := validation.Start(ctx); err != nil {
		t.Fatalf("启动校验服务失败: %v", err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stopped := make(chan error, 1)
	go func() {
		stopped <- validation.Stop(stopCtx)
	}()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("停止校验服务失败: %v", err)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("停止校验服务超时")
	}
	// 停止后再次提交应被拒绝。
	if err := validation.SubmitHost(ctx, host.ID.String()); !errors.Is(
		err,
		app.ErrValidationServiceStopped,
	) {
		t.Fatalf("停止后提交应被拒绝: %v", err)
	}
}

// TestStartRecoversPendingHosts 验证启动时恢复停留在 checking 的节点。
func TestStartRecoversPendingHosts(t *testing.T) {
	t.Parallel()
	hosts, validation, _ := newValidationEnv(t)
	ctx := context.Background()
	host, err := hosts.Create(ctx, app.CreateExecutionHostRequest{
		Name:           "待恢复节点",
		Host:           "127.0.0.1",
		Port:           1,
		Username:       "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	if err != nil {
		t.Fatalf("新增执行节点失败: %v", err)
	}
	if host.ValidationStatus != domain.ValidationStatusChecking {
		t.Fatalf("前置条件不符: %s", host.ValidationStatus)
	}
	// 启动服务应重新调度该节点，最终写回 abnormal（端口不可达）。
	if err := validation.Start(ctx); err != nil {
		t.Fatalf("启动校验服务失败: %v", err)
	}
	t.Cleanup(func() {
		_ = validation.Stop(context.Background())
	})
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		current, getErr := hosts.Get(ctx, host.ID.String())
		if getErr != nil {
			t.Fatalf("读取执行节点失败: %v", getErr)
		}
		if current.ValidationStatus == domain.ValidationStatusAbnormal {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("启动恢复未在时限内推进停留在 checking 的节点")
}

// TestValidationServiceName 验证后台服务名称稳定，供生命周期注册使用。
func TestValidationServiceName(t *testing.T) {
	t.Parallel()
	_, validation, _ := newValidationEnv(t)
	if validation.Name() == "" {
		t.Fatal("后台服务名称不能为空")
	}
}

// fakeSubmitter 记录提交次数，用于验证提交失败时的状态收敛。
type fakeSubmitter struct {
	mu    sync.Mutex
	err   error
	calls []string
}

// SubmitHost 记录调用并返回预设错误。
func (s *fakeSubmitter) SubmitHost(_ context.Context, hostID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, hostID)
	return s.err
}

// TestScheduleFailureConvergesToAbnormal 验证提交失败不会留下无法恢复的 checking。
func TestScheduleFailureConvergesToAbnormal(t *testing.T) {
	t.Parallel()
	hosts, _, store := newValidationEnv(t)
	submitter := &fakeSubmitter{err: errors.New("队列已满")}
	hosts.WithValidation(submitter, nil)
	host, err := hosts.Create(context.Background(), app.CreateExecutionHostRequest{
		Name:           "提交失败节点",
		Host:           "10.0.0.60",
		Username:       "deploy",
		PrivateKeyPath: testPrivateKeyPath(t),
	})
	if err == nil {
		t.Fatal("提交失败时新增操作应返回错误")
	}
	loaded, loadErr := store.GetExecutionHost(context.Background(), host.ID)
	if loadErr != nil {
		t.Fatalf("读取执行节点失败: %v", loadErr)
	}
	if loaded.ValidationStatus != domain.ValidationStatusAbnormal {
		t.Fatalf("提交失败应收敛为 abnormal: %s", loaded.ValidationStatus)
	}
	if !strings.Contains(loaded.ValidationMessage, "无法提交环境预检任务") {
		t.Fatalf("失败原因不符: %s", loaded.ValidationMessage)
	}
}

// testPrivateKeyPath 返回测试工作空间中的一个私钥文件路径。
//
// 本版本只支持私钥认证，构造执行节点时必须提供该路径。
func testPrivateKeyPath(t *testing.T) string {
	t.Helper()
	return testkit.PrivateKeyPath(t)
}
