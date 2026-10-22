// Package environment_local 覆盖远程操作的执行节点准入校验。
//
// 本文件验证"未通过环境预检的节点不得用于真实远程操作"这一硬性约束：
// 所有远程操作都经由 remoteDockerService.withRemoteSession，因此拒绝必须发生在
// 建立 SSH 连接之前，且错误信息要能定位到具体节点与后续操作。
package environment_local

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// discardWriter 丢弃日志输出。
type discardWriter struct{}

// Write 丢弃内容。
func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// newHostGateFixture 构造只用于准入校验的环境服务。
func newHostGateFixture(t *testing.T) (*app.EnvironmentService, storage.Store) {
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
	sshService, err := app.NewSSHService(db, logger, app.SSHOptions{
		KnownHostsPath: filepath.Join(testkit.Dir(t), "known_hosts"),
	})
	if err != nil {
		t.Fatalf("构造 SSH 服务失败: %v", err)
	}
	service, err := app.NewEnvironmentService(app.DockerServiceOptions{
		SSH:    sshService,
		Store:  db,
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("构造环境服务失败: %v", err)
	}
	return service, db
}

// insertHost 写入一个带指定校验状态的执行节点。
func insertHost(
	t *testing.T,
	store storage.Store,
	status domain.ValidationStatus,
) domain.ExecutionHost {
	t.Helper()
	ctx := context.Background()
	host, err := domain.NewExecutionHost(
		"", "准入节点", "127.0.0.1", 1, "deploy", "", time.Now(),
		domain.WithPrivateKey(testPrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	host.ValidationStatus = status
	if status == domain.ValidationStatusNormal {
		host.ValidationGeneration = 2
	}
	if err := store.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	return host
}

// TestRemoteOperationsRejectUnvalidatedHost 验证未通过校验的节点被拒绝。
func TestRemoteOperationsRejectUnvalidatedHost(t *testing.T) {
	t.Parallel()
	service, store := newHostGateFixture(t)
	cases := map[string]domain.ValidationStatus{
		"校验中": domain.ValidationStatusChecking,
		"异常":  domain.ValidationStatusAbnormal,
	}
	for name, status := range cases {
		host := insertHost(t, store, status)
		// Init 会建立真实远程会话，因此必须经过统一准入校验。
		_, err := service.Init(context.Background(), app.InitOptions{MachineID: host.ID.String()})
		if err == nil {
			t.Fatalf("%s: 未通过校验的节点不应允许远程操作", name)
		}
		if !errors.Is(err, app.ErrHostValidationUnavailable) {
			t.Fatalf("%s: 错误类别不符: %v", name, err)
		}
		if !strings.Contains(err.Error(), host.ID.String()) {
			t.Fatalf("%s: 错误应指明节点标识: %v", name, err)
		}
		if !strings.Contains(err.Error(), "agentflow host validate") {
			t.Fatalf("%s: 错误应给出后续操作提示: %v", name, err)
		}
	}
}

// TestDisabledHostIsRejected 验证已禁用节点被拒绝。
func TestDisabledHostIsRejected(t *testing.T) {
	t.Parallel()
	service, store := newHostGateFixture(t)
	ctx := context.Background()
	host := insertHost(t, store, domain.ValidationStatusNormal)
	stored, err := store.GetExecutionHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	disabled := *stored
	disabled.Enabled = false
	if err := store.UpdateExecutionHost(ctx, disabled); err != nil {
		t.Fatalf("更新执行节点失败: %v", err)
	}
	_, listErr := service.Init(ctx, app.InitOptions{MachineID: host.ID.String()})
	if listErr == nil {
		t.Fatal("已禁用节点不应允许远程操作")
	}
	if !errors.Is(listErr, app.ErrHostValidationUnavailable) {
		t.Fatalf("错误类别不符: %v", listErr)
	}
}

// TestReadOnlyPreflightBypassesGate 验证只读预检不受准入校验限制。
//
// 预检的职责正是报告节点是否可用，因此它必须在节点尚未通过校验时也能执行；
// 这里断言它不会因为 validation_status = checking 被准入校验拦下，而是因为
// 连接失败（端口 1 不可达）而返回连接错误。
func TestReadOnlyPreflightBypassesGate(t *testing.T) {
	t.Parallel()
	service, store := newHostGateFixture(t)
	host := insertHost(t, store, domain.ValidationStatusChecking)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := service.Preflight(ctx, app.InitOptions{MachineID: host.ID.String()})
	if err == nil {
		t.Fatal("不可达节点应返回连接错误")
	}
	if errors.Is(err, app.ErrHostValidationUnavailable) {
		t.Fatalf("只读预检不应被准入校验拦截: %v", err)
	}
}

// TestEnsureHostUsableForRemoteTaskIsShared 验证准入规则是单一实现。
//
// 该函数是远程操作与未来任务执行器共用的判定入口，因此它必须对未知状态与
// 空状态一律拒绝，避免新增状态时被默认放行。
func TestEnsureHostUsableForRemoteTaskIsShared(t *testing.T) {
	t.Parallel()
	statuses := []domain.ValidationStatus{
		"",
		"verifying_v2",
		domain.ValidationStatusChecking,
		domain.ValidationStatusAbnormal,
	}
	for _, status := range statuses {
		host, err := domain.NewExecutionHost(
			"", "节点", "10.0.0.90", 0, "deploy", "", time.Now(),
			domain.WithPrivateKey(testPrivateKeyPath(t)),
		)
		if err != nil {
			t.Fatalf("构造执行节点失败: %v", err)
		}
		host.ValidationStatus = status
		if err := app.EnsureHostUsableForRemoteTask(host); err == nil {
			t.Fatalf("状态 %q 应被拒绝", status)
		}
	}
}

// testPrivateKeyPath 返回测试工作空间中的一个私钥文件路径。
//
// 本版本只支持私钥认证，构造执行节点时必须提供该路径。
func testPrivateKeyPath(t *testing.T) string {
	t.Helper()
	return testkit.PrivateKeyPath(t)
}
