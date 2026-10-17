package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// mustExecutionHost 创建并写入一个测试执行节点。
func mustExecutionHost(t *testing.T, db *DB, name, host string, isDefault bool) domain.ExecutionHost {
	t.Helper()
	entity, err := domain.NewExecutionHost("", name, host, 0, "deploy", "", time.Now())
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	entity.IsDefault = isDefault
	if err := db.CreateExecutionHost(testContext(t), entity); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	return entity
}

func TestCreateAndGetExecutionHost(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	created := mustExecutionHost(t, db, "构建节点", "10.0.0.11", false)

	loaded, err := db.GetExecutionHost(ctx, created.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	if loaded.Name != "构建节点" || loaded.Host != "10.0.0.11" {
		t.Fatalf("节点字段不符: %+v", loaded)
	}
	if loaded.Port != domain.DefaultSSHPort {
		t.Fatalf("端口不符: %d", loaded.Port)
	}
	if loaded.Username != "deploy" {
		t.Fatalf("用户名不符: %s", loaded.Username)
	}
	if loaded.WorkspaceRoot != domain.DefaultWorkspaceRoot {
		t.Fatalf("工作空间默认值不符: %s", loaded.WorkspaceRoot)
	}
	if !loaded.Enabled {
		t.Fatal("节点应当默认启用")
	}
	if loaded.IsDefault {
		t.Fatal("节点不应默认为默认节点")
	}
	if !loaded.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("创建时间未按毫秒精度保存: %s vs %s", loaded.CreatedAt, created.CreatedAt)
	}
}

func TestGetExecutionHostNotFound(t *testing.T) {
	db := newTestDB(t)
	_, err := db.GetExecutionHost(testContext(t), domain.NewID())
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

func TestUpdateExecutionHost(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	created := mustExecutionHost(t, db, "构建节点", "10.0.0.11", false)

	updated := created
	updated.Name = "集成节点"
	updated.Host = "10.0.0.12"
	updated.Port = 2222
	updated.Username = "ci"
	updated.WorkspaceRoot = "/srv/agentflow"
	updated.Enabled = false
	updated.UpdatedAt = time.Now()
	if err := db.UpdateExecutionHost(ctx, updated); err != nil {
		t.Fatalf("更新执行节点失败: %v", err)
	}
	loaded, err := db.GetExecutionHost(ctx, created.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	if loaded.Name != "集成节点" || loaded.Host != "10.0.0.12" || loaded.Port != 2222 {
		t.Fatalf("更新未生效: %+v", loaded)
	}
	if loaded.WorkspaceRoot != "/srv/agentflow" || loaded.Enabled {
		t.Fatalf("更新未生效: %+v", loaded)
	}
	if !loaded.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("创建时间不应被更新: %s vs %s", loaded.CreatedAt, created.CreatedAt)
	}
}

func TestUpdateMissingExecutionHostReturnsNotFound(t *testing.T) {
	db := newTestDB(t)
	entity, err := domain.NewExecutionHost("", "节点", "10.0.0.11", 0, "deploy", "", time.Now())
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.UpdateExecutionHost(testContext(t), entity); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

func TestListExecutionHostsOrderedByCreation(t *testing.T) {
	db := newTestDB(t)
	mustExecutionHost(t, db, "第一个", "10.0.0.1", false)
	mustExecutionHost(t, db, "第二个", "10.0.0.2", false)
	mustExecutionHost(t, db, "第三个", "10.0.0.3", false)

	hosts, err := db.ListExecutionHosts(testContext(t))
	if err != nil {
		t.Fatalf("查询执行节点失败: %v", err)
	}
	if len(hosts) != 3 {
		t.Fatalf("节点数量不符: %d", len(hosts))
	}
	if hosts[0].Name != "第一个" || hosts[2].Name != "第三个" {
		t.Fatalf("节点顺序不符: %+v", hosts)
	}
}

func TestDeleteExecutionHost(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	created := mustExecutionHost(t, db, "构建节点", "10.0.0.11", false)
	if err := db.DeleteExecutionHost(ctx, created.ID); err != nil {
		t.Fatalf("删除执行节点失败: %v", err)
	}
	if _, err := db.GetExecutionHost(ctx, created.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("删除后应当不存在，实际: %v", err)
	}
	if err := db.DeleteExecutionHost(ctx, created.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("重复删除应当返回记录不存在，实际: %v", err)
	}
}

func TestSetDefaultExecutionHostSwitches(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	first := mustExecutionHost(t, db, "第一个", "10.0.0.1", false)
	second := mustExecutionHost(t, db, "第二个", "10.0.0.2", false)

	if err := db.SetDefaultExecutionHost(ctx, first.ID); err != nil {
		t.Fatalf("设置默认节点失败: %v", err)
	}
	current, err := db.GetDefaultExecutionHost(ctx)
	if err != nil {
		t.Fatalf("读取默认节点失败: %v", err)
	}
	if current.ID != first.ID {
		t.Fatalf("默认节点不符: %s", current.ID)
	}
	if err := db.SetDefaultExecutionHost(ctx, second.ID); err != nil {
		t.Fatalf("切换默认节点失败: %v", err)
	}
	current, err = db.GetDefaultExecutionHost(ctx)
	if err != nil {
		t.Fatalf("读取默认节点失败: %v", err)
	}
	if current.ID != second.ID {
		t.Fatalf("默认节点未切换: %s", current.ID)
	}
	hosts, err := db.ListExecutionHosts(ctx)
	if err != nil {
		t.Fatalf("查询执行节点失败: %v", err)
	}
	defaults := 0
	for _, host := range hosts {
		if host.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("默认节点必须唯一，实际 %d 个", defaults)
	}
}

func TestSetDefaultExecutionHostIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	created := mustExecutionHost(t, db, "第一个", "10.0.0.1", false)
	if err := db.SetDefaultExecutionHost(ctx, created.ID); err != nil {
		t.Fatalf("设置默认节点失败: %v", err)
	}
	if err := db.SetDefaultExecutionHost(ctx, created.ID); err != nil {
		t.Fatalf("重复设置默认节点应当成功: %v", err)
	}
	current, err := db.GetDefaultExecutionHost(ctx)
	if err != nil {
		t.Fatalf("读取默认节点失败: %v", err)
	}
	if current.ID != created.ID {
		t.Fatalf("默认节点不符: %s", current.ID)
	}
}

func TestSetDefaultExecutionHostNotFound(t *testing.T) {
	db := newTestDB(t)
	err := db.SetDefaultExecutionHost(testContext(t), domain.NewID())
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

func TestGetDefaultExecutionHostNotFound(t *testing.T) {
	db := newTestDB(t)
	_, err := db.GetDefaultExecutionHost(testContext(t))
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("没有默认节点时应当返回记录不存在，实际: %v", err)
	}
}

func TestCreateExecutionHostRejectsSecondDefault(t *testing.T) {
	db := newTestDB(t)
	mustExecutionHost(t, db, "第一个", "10.0.0.1", true)
	second, err := domain.NewExecutionHost("", "第二个", "10.0.0.2", 0, "deploy", "", time.Now())
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	second.IsDefault = true
	err = db.CreateExecutionHost(testContext(t), second)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("第二个默认节点应当被唯一索引拒绝，实际: %v", err)
	}
}

func TestDeleteDefaultExecutionHostLeavesNoDefault(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	created := mustExecutionHost(t, db, "唯一节点", "10.0.0.1", true)
	if err := db.DeleteExecutionHost(ctx, created.ID); err != nil {
		t.Fatalf("删除执行节点失败: %v", err)
	}
	if _, err := db.GetDefaultExecutionHost(ctx); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("删除默认节点后不应存在默认节点，实际: %v", err)
	}
	// 删除后应当可以重新设置默认节点。
	another := mustExecutionHost(t, db, "新节点", "10.0.0.2", false)
	if err := db.SetDefaultExecutionHost(ctx, another.ID); err != nil {
		t.Fatalf("重新设置默认节点失败: %v", err)
	}
}

func TestCreateExecutionHostRejectsInvalidPort(t *testing.T) {
	db := newTestDB(t)
	entity, err := domain.NewExecutionHost("", "节点", "10.0.0.1", 0, "deploy", "", time.Now())
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	entity.Port = 70000
	if err := db.CreateExecutionHost(testContext(t), entity); err == nil {
		t.Fatal("非法端口应当被拒绝")
	}
}

func TestCreateExecutionHostRejectsDuplicateID(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	first := mustExecutionHost(t, db, "第一个", "10.0.0.1", false)
	duplicated, err := domain.NewExecutionHost(first.ID, "重复", "10.0.0.2", 0, "deploy", "", time.Now())
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, duplicated); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("重复标识应当返回唯一约束冲突，实际: %v", err)
	}
}

func TestExecutionHostTransactionRollback(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	entity, err := domain.NewExecutionHost("", "节点", "10.0.0.1", 0, "deploy", "", time.Now())
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	rollbackErr := errors.New("主动回滚")
	err = db.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		if createErr := tx.CreateExecutionHost(ctx, entity); createErr != nil {
			return createErr
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("事务应当返回回调错误，实际: %v", err)
	}
	if _, err := db.GetExecutionHost(ctx, entity.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("回滚后节点不应存在，实际: %v", err)
	}
}

func TestExecutionHostStoresNoCredentialColumns(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	rows, err := db.sql.QueryContext(ctx, `SELECT name FROM pragma_table_info('execution_hosts')`)
	if err != nil {
		t.Fatalf("读取表结构失败: %v", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	columns := make([]string, 0, 16)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("读取列名失败: %v", err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("读取列名失败: %v", err)
	}
	for _, forbidden := range []string{"password", "private_key", "auth_type", "credential"} {
		if containsString(columns, forbidden) {
			t.Fatalf("执行节点表不应包含凭据列 %s: %v", forbidden, columns)
		}
	}
	for _, expected := range []string{
		"id",
		"name",
		"host",
		"port",
		"username",
		"workspace_root",
		"is_default",
		"enabled",
		"created_at",
		"updated_at",
	} {
		if !containsString(columns, expected) {
			t.Fatalf("执行节点表缺少列 %s: %v", expected, columns)
		}
	}
}

// TestExecutionHostAuthFieldsRoundTrip 验证认证与连接结果字段可以完整往返。
func TestExecutionHostAuthFieldsRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	entity, err := domain.NewExecutionHost(
		"",
		"私钥节点",
		"10.0.0.12",
		2222,
		"deploy",
		"/srv/agentflow",
		time.Now(),
		domain.WithAuth(domain.AuthMethodPrivateKey, "/home/deploy/.ssh/id_ed25519"),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, entity); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	loaded, err := db.GetExecutionHost(ctx, entity.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	if loaded.AuthMethod != domain.AuthMethodPrivateKey {
		t.Fatalf("认证方式不符: %s", loaded.AuthMethod)
	}
	if loaded.PrivateKeyPath != "/home/deploy/.ssh/id_ed25519" {
		t.Fatalf("私钥路径不符: %s", loaded.PrivateKeyPath)
	}
	if loaded.HostKeyTrustedAt != nil || loaded.LastConnectedAt != nil {
		t.Fatalf("未信任与未连接时时间应为空: %+v", loaded)
	}
	if loaded.LastConnectStatus != domain.ConnectStatusUnknown {
		t.Fatalf("未连接时状态应为空: %s", loaded.LastConnectStatus)
	}
	if err := loaded.TrustHostKey("ssh-ed25519", "SHA256:abc", time.Now()); err != nil {
		t.Fatalf("记录主机密钥信任失败: %v", err)
	}
	if err := loaded.RecordConnectResult(domain.ConnectStatusFailed, "auth_failed", time.Now()); err != nil {
		t.Fatalf("记录连接结果失败: %v", err)
	}
	if err := db.UpdateExecutionHost(ctx, *loaded); err != nil {
		t.Fatalf("更新执行节点失败: %v", err)
	}
	reloaded, err := db.GetExecutionHost(ctx, entity.ID)
	if err != nil {
		t.Fatalf("重新读取执行节点失败: %v", err)
	}
	if reloaded.HostKeyAlgorithm != "ssh-ed25519" || reloaded.HostKeyFingerprint != "SHA256:abc" {
		t.Fatalf("主机密钥信任信息不符: %+v", reloaded)
	}
	if reloaded.HostKeyTrustedAt == nil {
		t.Fatal("应当保存主机密钥信任时间")
	}
	if reloaded.LastConnectStatus != domain.ConnectStatusFailed ||
		reloaded.LastConnectError != "auth_failed" {
		t.Fatalf("连接结果不符: %+v", reloaded)
	}
	if reloaded.LastConnectedAt == nil {
		t.Fatal("应当保存最近连接时间")
	}
}

// TestExecutionHostPrivateKeyRequiresPath 验证数据库层面拒绝缺少私钥路径的记录。
func TestExecutionHostPrivateKeyRequiresPath(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	entity, err := domain.NewExecutionHost("", "节点", "10.0.0.13", 0, "deploy", "", time.Now())
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, entity); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	// 绕过领域校验，直接验证数据库触发器约束。
	_, err = db.sql.ExecContext(
		ctx,
		`UPDATE execution_hosts SET auth_method = 'private_key', private_key_path = NULL WHERE id = ?`,
		entity.ID.String(),
	)
	if err == nil {
		t.Fatal("私钥认证缺少私钥路径时应当被数据库拒绝")
	}
}

// TestExecutionHostRejectsPlaintextSecretColumns 验证表中不存在任何凭据列。
func TestExecutionHostRejectsPlaintextSecretColumns(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	rows, err := db.sql.QueryContext(ctx, `SELECT name FROM pragma_table_info('execution_hosts')`)
	if err != nil {
		t.Fatalf("读取表结构失败: %v", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	forbidden := map[string]struct{}{
		"password":    {},
		"passphrase":  {},
		"private_key": {},
		"secret":      {},
		"credential":  {},
	}
	columns := make([]string, 0, 24)
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr != nil {
			t.Fatalf("读取列名失败: %v", scanErr)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("读取列名失败: %v", err)
	}
	for _, name := range columns {
		if _, bad := forbidden[name]; bad {
			t.Fatalf("执行节点表不应包含凭据列: %s", name)
		}
	}
}
