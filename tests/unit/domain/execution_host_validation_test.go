package domain_test

import (
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// mustHost 构造一个测试执行节点。
func mustHost(t *testing.T) domain.ExecutionHost {
	t.Helper()
	host, err := domain.NewExecutionHost(
		"", "校验节点", "10.0.0.20", 0, "deploy", "", time.Now(),
		domain.WithPrivateKey(testPrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	return host
}

// passedDetails 构造一份全部检查通过的详情。
func passedDetails() domain.ValidationDetails {
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
			{
				Name:        domain.ValidationCheckDockerCLIHelp,
				Title:       "Docker CLI 帮助信息",
				Status:      domain.CheckStatusPassed,
				CompletedAt: &completed,
			},
			{
				Name:        domain.ValidationCheckBaseImageMetadata,
				Title:       "基础镜像元数据",
				Status:      domain.CheckStatusPassed,
				CompletedAt: &completed,
			},
		},
	}
}

// TestNewExecutionHostStartsChecking 验证新建节点默认处于 checking 且代次为 1。
func TestNewExecutionHostStartsChecking(t *testing.T) {
	t.Parallel()
	host := mustHost(t)
	if host.ValidationStatus != domain.ValidationStatusChecking {
		t.Fatalf("新建节点应为 checking: %s", host.ValidationStatus)
	}
	if host.ValidationGeneration != 1 {
		t.Fatalf("新建节点代次应为 1: %d", host.ValidationGeneration)
	}
	if host.ValidationStatus.Usable() {
		t.Fatal("checking 状态不得用于远程任务")
	}
	if host.ValidationDetails != nil {
		t.Fatal("新建节点不应带有校验详情")
	}
}

// TestValidationStatusValues 验证三态定义与未知取值保留。
func TestValidationStatusValues(t *testing.T) {
	t.Parallel()
	known := []domain.ValidationStatus{
		domain.ValidationStatusChecking,
		domain.ValidationStatusNormal,
		domain.ValidationStatusAbnormal,
	}
	for _, status := range known {
		if !status.IsKnown() {
			t.Fatalf("状态应为已知取值: %s", status)
		}
	}
	unknown := domain.ValidationStatus("verifying_v2")
	if unknown.IsKnown() {
		t.Fatal("未知状态不应被当作已知")
	}
	if unknown.Usable() {
		t.Fatal("未知状态不得用于远程任务")
	}
	// 实体校验不限制取值：数据库不声明枚举白名单，读取层必须保留未知值。
	host := mustHost(t)
	host.ValidationStatus = unknown
	if err := host.Validate(); err != nil {
		t.Fatalf("未知状态应可保存: %v", err)
	}
}

// TestInvalidateValidationIncrementsGeneration 验证失效会清空结果并增加代次。
func TestInvalidateValidationIncrementsGeneration(t *testing.T) {
	t.Parallel()
	host := mustHost(t)
	details := passedDetails()
	if err := host.RecordValidationResult(1, details, time.Now()); err != nil {
		t.Fatalf("记录校验结果失败: %v", err)
	}
	if host.ValidationStatus != domain.ValidationStatusNormal {
		t.Fatalf("全部通过时应为 normal: %s", host.ValidationStatus)
	}
	before := host.ValidationGeneration
	host.InvalidateValidation(time.Now())
	if host.ValidationStatus != domain.ValidationStatusChecking {
		t.Fatalf("失效后应为 checking: %s", host.ValidationStatus)
	}
	if host.ValidationGeneration != before+1 {
		t.Fatalf("失效后代次应递增: %d -> %d", before, host.ValidationGeneration)
	}
	if host.ValidationDetails != nil || host.ValidationMessage != "" {
		t.Fatal("失效后不应保留旧结果")
	}
	if host.ValidationCompletedAt != nil {
		t.Fatal("失效后不应保留完成时间")
	}
}

// TestRecordValidationResultRejectsStaleGeneration 验证旧代次结果不能覆盖新配置。
func TestRecordValidationResultRejectsStaleGeneration(t *testing.T) {
	t.Parallel()
	host := mustHost(t)
	host.InvalidateValidation(time.Now())
	current := host.ValidationGeneration
	if err := host.RecordValidationResult(current-1, passedDetails(), time.Now()); err == nil {
		t.Fatal("旧代次结果必须被拒绝")
	}
	if host.ValidationStatus != domain.ValidationStatusChecking {
		t.Fatalf("被拒绝后状态不应改变: %s", host.ValidationStatus)
	}
}

// TestRecordValidationResultRequiresAllChecks 验证未全部通过时一律 abnormal。
func TestRecordValidationResultRequiresAllChecks(t *testing.T) {
	t.Parallel()
	cases := map[string]domain.CheckStatus{
		"失败":  domain.CheckStatusFailed,
		"超时":  domain.CheckStatusTimedOut,
		"未执行": domain.CheckStatusNotRun,
		"已跳过": domain.CheckStatusSkipped,
	}
	for name, status := range cases {
		details := passedDetails()
		details.Checks[2].Status = status
		host := mustHost(t)
		if err := host.RecordValidationResult(host.ValidationGeneration, details, time.Now()); err != nil {
			t.Fatalf("%s: 记录校验结果失败: %v", name, err)
		}
		if host.ValidationStatus != domain.ValidationStatusAbnormal {
			t.Fatalf("%s: 单项状态 %s 不应产生 normal", name, status)
		}
		if host.ValidationMessage == "" {
			t.Fatalf("%s: 失败时必须给出可读摘要", name)
		}
	}
}

// TestRecordValidationResultRequiresChecks 验证没有任何检查项时不能判为 normal。
func TestRecordValidationResultRequiresChecks(t *testing.T) {
	t.Parallel()
	host := mustHost(t)
	details := domain.ValidationDetails{ValidatorVersion: domain.ValidatorVersion}
	if err := host.RecordValidationResult(host.ValidationGeneration, details, time.Now()); err != nil {
		t.Fatalf("记录校验结果失败: %v", err)
	}
	if host.ValidationStatus != domain.ValidationStatusAbnormal {
		t.Fatalf("没有检查项时不应为 normal: %s", host.ValidationStatus)
	}
}

// TestFailValidationSetsAbnormal 验证无法开始校验时收敛为 abnormal。
func TestFailValidationSetsAbnormal(t *testing.T) {
	t.Parallel()
	host := mustHost(t)
	host.InvalidateValidation(time.Now())
	generation := host.ValidationGeneration
	if err := host.FailValidation(generation, "无法提交预检任务", time.Now()); err != nil {
		t.Fatalf("记录失败状态出错: %v", err)
	}
	if host.ValidationStatus != domain.ValidationStatusAbnormal {
		t.Fatalf("状态应为 abnormal: %s", host.ValidationStatus)
	}
	if host.ValidationMessage != "无法提交预检任务" {
		t.Fatalf("失败原因未保存: %q", host.ValidationMessage)
	}
	if host.ValidationCompletedAt == nil {
		t.Fatal("失败也必须记录完成时间，避免停留在 checking")
	}
}

// TestMarkValidationRunningRejectsStaleGeneration 验证开始校验时校验代次。
func TestMarkValidationRunningRejectsStaleGeneration(t *testing.T) {
	t.Parallel()
	host := mustHost(t)
	host.InvalidateValidation(time.Now())
	if err := host.MarkValidationRunning(host.ValidationGeneration-1, time.Now()); err == nil {
		t.Fatal("旧代次不应允许开始校验")
	}
	if err := host.MarkValidationRunning(host.ValidationGeneration, time.Now()); err != nil {
		t.Fatalf("当前代次应允许开始校验: %v", err)
	}
	if host.ValidationStartedAt == nil {
		t.Fatal("开始校验应记录开始时间")
	}
}

// TestValidationDetailsAllRequiredPassed 验证全部通过判定不受顺序影响。
func TestValidationDetailsAllRequiredPassed(t *testing.T) {
	t.Parallel()
	details := passedDetails()
	if !details.AllRequiredPassed() {
		t.Fatal("全部通过时应返回 true")
	}
	details.Checks[0].Status = domain.CheckStatusFailed
	if details.AllRequiredPassed() {
		t.Fatal("存在失败项时应返回 false")
	}
	if _, ok := details.Check(domain.ValidationCheckDockerCLIHelp); !ok {
		t.Fatal("应能按名称查找检查项")
	}
}

// TestFingerprintOfExcludesSecrets 验证校验指纹不包含凭据。
func TestFingerprintOfExcludesSecrets(t *testing.T) {
	t.Parallel()
	host := mustHost(t)
	fingerprint := domain.FingerprintOf(host, "abc123")
	if fingerprint.Host != host.Host || fingerprint.Port != host.Port {
		t.Fatalf("指纹应包含连接目标: %+v", fingerprint)
	}
	if fingerprint.DockerfileSHA256 != "abc123" {
		t.Fatalf("指纹应包含 Dockerfile 摘要: %+v", fingerprint)
	}
	// 指纹结构本身没有任何凭据字段：修改密码或口令不会改变指纹。
	if fingerprint.AuthMethod != host.AuthMethod {
		t.Fatalf("指纹应包含认证方式种类: %+v", fingerprint)
	}
}
