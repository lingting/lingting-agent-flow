package sqlite_test

import (
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// mustValidationHost 在给定数据库中写入一个执行节点并返回其当前配置。
func mustValidationHost(
	t *testing.T,
	db *sqlite.DB,
	name, host string,
) domain.ExecutionHost {
	t.Helper()
	entity, err := domain.NewExecutionHost(
		"", name, host, 0, "deploy", "", time.Now(),
		domain.WithPrivateKey(testPrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(testContext(t), entity); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	loaded, err := db.GetExecutionHost(testContext(t), entity.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	return *loaded
}

// passedValidation 构造一份全部通过的校验详情。
func passedValidation() domain.ValidationDetails {
	completed := time.Now().UTC()
	return domain.ValidationDetails{
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
}

// TestExecutionHostValidationDefaultsToChecking 验证新建记录落库为 checking。
func TestExecutionHostValidationDefaultsToChecking(t *testing.T) {
	t.Parallel()
	host := mustValidationHost(t, newTestDB(t), "校验节点", "10.0.0.30")
	if host.ValidationStatus != domain.ValidationStatusChecking {
		t.Fatalf("新建节点应为 checking: %s", host.ValidationStatus)
	}
	if host.ValidationGeneration != 1 {
		t.Fatalf("新建节点代次应为 1: %d", host.ValidationGeneration)
	}
}

// TestExecutionHostValidationRoundTrip 验证校验状态与详情可以正确读写。
func TestExecutionHostValidationRoundTrip(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	entity, err := domain.NewExecutionHost(
		"", "校验节点", "10.0.0.31", 0, "deploy", "", time.Now(),
		domain.WithPrivateKey(testPrivateKeyPath(t)),
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
	details := passedValidation()
	details.Checks[0].Details = []string{"补充说明"}
	details.Checks[0].Images = []domain.ImageMetadataResult{
		{
			Reference:      "node:22.19.0-bookworm-slim",
			Resolved:       "node:22.19.0-bookworm-slim",
			Status:         domain.CheckStatusPassed,
			Digest:         "sha256:abc",
			Platforms:      []string{"linux/amd64"},
			DurationMillis: 42,
			Message:        "镜像元数据查询成功",
		},
	}
	if err := loaded.RecordValidationResult(loaded.ValidationGeneration, details, time.Now()); err != nil {
		t.Fatalf("记录校验结果失败: %v", err)
	}
	applied, err := db.UpdateExecutionHostValidation(ctx, *loaded, loaded.ValidationGeneration)
	if err != nil {
		t.Fatalf("写入校验结果失败: %v", err)
	}
	if !applied {
		t.Fatal("代次一致时应写入成功")
	}

	reloaded, err := db.GetExecutionHost(ctx, entity.ID)
	if err != nil {
		t.Fatalf("重新读取执行节点失败: %v", err)
	}
	if reloaded.ValidationStatus != domain.ValidationStatusNormal {
		t.Fatalf("状态应为 normal: %s", reloaded.ValidationStatus)
	}
	if reloaded.ValidationMessage == "" {
		t.Fatal("摘要应被保存")
	}
	if reloaded.ValidationStartedAt == nil || reloaded.ValidationCompletedAt == nil {
		t.Fatal("开始与完成时间应被保存")
	}
	if reloaded.ValidationDetails == nil {
		t.Fatal("校验详情应被保存")
	}
	if reloaded.ValidationDetails.ValidatorVersion != domain.ValidatorVersion {
		t.Fatalf("校验器版本不符: %s", reloaded.ValidationDetails.ValidatorVersion)
	}
	check := reloaded.ValidationDetails.Checks[0]
	if len(check.Details) != 1 || check.Details[0] != "补充说明" {
		t.Fatalf("检查项补充说明丢失: %+v", check.Details)
	}
	if len(check.Images) != 1 {
		t.Fatalf("镜像结果丢失: %+v", check.Images)
	}
	image := check.Images[0]
	if image.Digest != "sha256:abc" || image.DurationMillis != 42 {
		t.Fatalf("镜像结果字段不符: %+v", image)
	}
	if len(image.Platforms) != 1 || image.Platforms[0] != "linux/amd64" {
		t.Fatalf("镜像平台丢失: %+v", image.Platforms)
	}
}

// TestExecutionHostValidationRejectsStaleGeneration 验证旧代次不能覆盖新配置。
func TestExecutionHostValidationRejectsStaleGeneration(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	entity, err := domain.NewExecutionHost(
		"", "校验节点", "10.0.0.32", 0, "deploy", "", time.Now(),
		domain.WithPrivateKey(testPrivateKeyPath(t)),
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
	staleGeneration := loaded.ValidationGeneration

	// 模拟用户修改配置：代次递增，状态回到 checking。
	updated := *loaded
	updated.InvalidateValidation(time.Now())
	if err := db.UpdateExecutionHost(ctx, updated); err != nil {
		t.Fatalf("更新执行节点失败: %v", err)
	}

	// 旧任务带着旧代次写回结果，必须被拒绝。
	stale := *loaded
	if err := stale.RecordValidationResult(staleGeneration, passedValidation(), time.Now()); err != nil {
		t.Fatalf("记录校验结果失败: %v", err)
	}
	applied, err := db.UpdateExecutionHostValidation(ctx, stale, staleGeneration)
	if err != nil {
		t.Fatalf("写入校验结果出错: %v", err)
	}
	if applied {
		t.Fatal("旧代次结果不得写入")
	}
	current, err := db.GetExecutionHost(ctx, entity.ID)
	if err != nil {
		t.Fatalf("重新读取执行节点失败: %v", err)
	}
	if current.ValidationStatus != domain.ValidationStatusChecking {
		t.Fatalf("旧结果不得覆盖新配置: %s", current.ValidationStatus)
	}
	if current.ValidationDetails != nil {
		t.Fatal("旧结果不得写入详情")
	}
}

// TestExecutionHostValidationKeepsUnknownStatus 验证未知状态可以原样保存与读取。
func TestExecutionHostValidationKeepsUnknownStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	entity, err := domain.NewExecutionHost(
		"", "校验节点", "10.0.0.33", 0, "deploy", "", time.Now(),
		domain.WithPrivateKey(testPrivateKeyPath(t)),
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
	// 直接写入本版本不认识的取值，模拟更高版本写入的数据。
	unknown := *loaded
	unknown.ValidationStatus = domain.ValidationStatus("verifying_v2")
	if err := db.UpdateExecutionHost(ctx, unknown); err != nil {
		t.Fatalf("未知状态应可写入: %v", err)
	}
	reloaded, err := db.GetExecutionHost(ctx, entity.ID)
	if err != nil {
		t.Fatalf("重新读取执行节点失败: %v", err)
	}
	if reloaded.ValidationStatus != domain.ValidationStatus("verifying_v2") {
		t.Fatalf("未知状态必须原样保留: %s", reloaded.ValidationStatus)
	}
	if reloaded.ValidationStatus.Usable() {
		t.Fatal("未知状态不得用于远程任务")
	}
}

// TestListExecutionHostsByValidationStatus 验证按状态查询用于启动恢复。
func TestListExecutionHostsByValidationStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	checking, err := domain.NewExecutionHost(
		"", "待校验节点", "10.0.0.34", 0, "deploy", "", time.Now(),
		domain.WithPrivateKey(testPrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, checking); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	normal, err := domain.NewExecutionHost(
		"", "已校验节点", "10.0.0.35", 0, "deploy", "", time.Now(),
		domain.WithPrivateKey(testPrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, normal); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	loadedNormal, err := db.GetExecutionHost(ctx, normal.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	applied := *loadedNormal
	if err := applied.RecordValidationResult(applied.ValidationGeneration, passedValidation(), time.Now()); err != nil {
		t.Fatalf("记录校验结果失败: %v", err)
	}
	if _, err := db.UpdateExecutionHostValidation(ctx, applied, applied.ValidationGeneration); err != nil {
		t.Fatalf("写入校验结果失败: %v", err)
	}

	pending, err := db.ListExecutionHostsByValidationStatus(ctx, domain.ValidationStatusChecking)
	if err != nil {
		t.Fatalf("按状态查询失败: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != checking.ID {
		t.Fatalf("只应返回停留在 checking 的节点: %+v", pending)
	}
}

// TestUpdateExecutionHostValidationIgnoresDeletedHost 验证已删除节点不会被写入。
func TestUpdateExecutionHostValidationIgnoresDeletedHost(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	host := mustValidationHost(t, db, "已删除节点", "10.0.0.36")
	if err := db.DeleteExecutionHost(ctx, host.ID); err != nil {
		t.Fatalf("删除执行节点失败: %v", err)
	}
	updated := host
	if err := updated.RecordValidationResult(updated.ValidationGeneration, passedValidation(), time.Now()); err != nil {
		t.Fatalf("记录校验结果失败: %v", err)
	}
	applied, err := db.UpdateExecutionHostValidation(ctx, updated, updated.ValidationGeneration)
	if err != nil {
		t.Fatalf("写入校验结果出错: %v", err)
	}
	if applied {
		t.Fatal("已删除节点不应被写入校验结果")
	}
}

// testPrivateKeyPath 返回测试工作空间中的一个私钥文件路径。
//
// 本版本只支持私钥认证，构造执行节点时必须提供该路径。
func testPrivateKeyPath(t *testing.T) string {
	t.Helper()
	return testkit.PrivateKeyPath(t)
}
