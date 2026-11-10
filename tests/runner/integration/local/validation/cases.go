// 本文件是 tests/integration/local/validation/validation_test.go 中 11 个用例的迁移结果。
//
// 改造点：函数头改成 func run*、删除 t.Parallel()、数据库改为注入的 sqlite-db 句柄；
// 断言与注释保持原样。表驱动子测试按 R5 D6 保留为 Case 内顺序块（子测试名写入日志）。
package validation

import (
	"context"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/testrunner"
)

// 登记本包 11 条用例，顺序与源文件一致，便于与 R0 冻结清单逐条对账。
var (
	_ = register("validate-passes-all-checks", runValidatePassesAllChecks, databaseDecl()...)
	_ = register("validate-fails-when-ssh-rejected", runValidateFailsWhenSSHRejected, databaseDecl()...)
	_ = register("validate-fails-when-help-exit-non-zero", runValidateFailsWhenHelpExitNonZero, databaseDecl()...)
	_ = register("validate-fails-when-option-missing", runValidateFailsWhenOptionMissing, databaseDecl()...)
	_ = register("validate-records-timestamps", runValidateRecordsTimestamps, databaseDecl()...)
	_ = register("validate-cancellation-converges", runValidateCancellationConverges, databaseDecl()...)
	_ = register("validate-fails-when-cli-version-too-low", runValidateFailsWhenCLIVersionTooLow, databaseDecl()...)
	_ = register("validate-fails-when-engine-version-too-low", runValidateFailsWhenEngineVersionTooLow, databaseDecl()...)
	_ = register("validate-fails-when-api-incompatible", runValidateFailsWhenAPIIncompatible, databaseDecl()...)
	_ = register("validate-version-check-records-actual-versions", runValidateVersionCheckRecordsActualVersions, databaseDecl()...)
	_ = register("validate-rejects-task-execution-when-abnormal", runValidateRejectsTaskExecutionWhenAbnormal)
)

// 原 TestValidatePassesAllChecks 验证三项检查全部通过时总体状态为 normal。
func runValidatePassesAllChecks(t *testrunner.Context) {
	engine := &engineBehaviour{}
	fixture := newFixture(t, serverOptions{engine: engine})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	if outcome.Stale {
		t.Fatal("结果不应过期")
	}
	if outcome.Status != domain.ValidationStatusNormal {
		t.Fatalf("状态应为 normal: %s / %s", outcome.Status, outcome.Host.ValidationMessage)
	}
	if !outcome.Details.AllRequiredPassed() {
		t.Fatalf("三项检查应全部通过: %+v", outcome.Details.Checks)
	}
	if len(outcome.Details.Checks) != 3 {
		t.Fatalf("应执行三项检查: %d", len(outcome.Details.Checks))
	}
	versionCheck := outcome.Details.Checks[1]
	if versionCheck.Name != domain.ValidationCheckDockerVersion {
		t.Fatalf("第二项应为 Docker 版本检查: %s", versionCheck.Name)
	}
	if outcome.Details.Checks[2].Name != domain.ValidationCheckDockerCLIHelp {
		t.Fatalf("第三项应为 Docker CLI 帮助检查: %s", outcome.Details.Checks[2].Name)
	}
	// 能力边界说明必须存在，避免把 normal 误读为镜像可用或运行兼容性已验证。
	if len(outcome.Details.Capabilities) != 2 {
		t.Fatalf("能力边界说明缺失: %+v", outcome.Details.Capabilities)
	}
	// 请求必须是只读的：只需一次固定基准的版本探测，不得出现 Distribution Inspect。
	if len(engine.requests) != 1 {
		t.Fatalf("应只发起一次版本探测: %+v", engine.requests)
	}
	if !strings.Contains(engine.requests[0], "/v1.52/version") {
		t.Fatalf("版本探测必须使用固定 API 基准路径: %s", engine.requests[0])
	}
	for _, request := range engine.requests {
		if !strings.HasPrefix(request, "GET /v1.52/") {
			t.Fatalf("Engine API 请求必须使用固定基准路径: %s", request)
		}
	}
	// 校验只允许版本查询与帮助命令，不得出现 create / run / pull 等变更命令。
	for _, command := range fixture.server.recordedCommands() {
		if !strings.Contains(command, "docker ") {
			continue
		}
		if strings.Contains(command, " --help") {
			continue
		}
		if strings.Contains(command, "docker version") || strings.Contains(command, "docker info") {
			continue
		}
		t.Fatalf("校验执行了未预期的 Docker 命令: %s", command)
	}
}

// 原 TestValidateFailsWhenSSHRejected 验证 SSH 认证失败时后续检查记为 not_run。
func runValidateFailsWhenSSHRejected(t *testrunner.Context) {
	// 服务端只接受另一把公钥：客户端私钥认证必然被拒绝。
	otherKey := generateHostKey(t)
	fixture := newFixture(t, serverOptions{
		engine:        &engineBehaviour{},
		authorizedKey: otherKey.PublicKey(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome, err := fixture.validator.Validate(ctx, app.ValidationRequest{
		HostID: fixture.host.ID.String(),
	})
	if err != nil {
		t.Fatalf("校验本身不应返回错误: %v", err)
	}
	if outcome.Status != domain.ValidationStatusAbnormal {
		t.Fatalf("状态应为 abnormal: %s", outcome.Status)
	}
	if len(outcome.Details.Checks) != 3 {
		t.Fatalf("应保留三项检查结果: %d", len(outcome.Details.Checks))
	}
	if outcome.Details.Checks[0].Status.Passed() {
		t.Fatal("SSH 检查不应通过")
	}
	for _, check := range outcome.Details.Checks[1:] {
		if check.Status != domain.CheckStatusNotRun {
			t.Fatalf("SSH 失败时后续检查应为 not_run: %s=%s", check.Name, check.Status)
		}
	}
	if !strings.Contains(outcome.Details.Checks[0].Message, "认证") {
		t.Fatalf("失败原因应说明认证问题: %s", outcome.Details.Checks[0].Message)
	}
}

// 原 TestValidateFailsWhenHelpExitNonZero 验证帮助命令非零退出导致 abnormal。
func runValidateFailsWhenHelpExitNonZero(t *testrunner.Context) {
	fixture := newFixture(t, serverOptions{helpExitCode: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	if outcome.Status != domain.ValidationStatusAbnormal {
		t.Fatalf("状态应为 abnormal: %s", outcome.Status)
	}
	if outcome.Details.Checks[2].Status.Passed() {
		t.Fatal("Docker 帮助检查不应通过")
	}
	if len(outcome.Details.Checks) != 3 {
		t.Fatalf("帮助检查失败后不应产生额外的检查项: %d", len(outcome.Details.Checks))
	}
}

// 原 TestValidateFailsWhenOptionMissing 验证帮助信息缺少参数时给出定位信息。
func runValidateFailsWhenOptionMissing(t *testrunner.Context) {
	fixture := newFixture(t, serverOptions{
		missingOption: "--mount",
		engine:        &engineBehaviour{},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	if outcome.Status != domain.ValidationStatusAbnormal {
		t.Fatalf("状态应为 abnormal: %s", outcome.Status)
	}
	check := outcome.Details.Checks[2]
	if check.Status.Passed() {
		t.Fatal("缺少参数时帮助检查不应通过")
	}
	if !strings.Contains(check.Message, "--mount") {
		t.Fatalf("失败原因应指明缺失参数: %s", check.Message)
	}
}

// 原 TestValidateRecordsTimestamps 验证校验结果记录了开始与完成时间。
func runValidateRecordsTimestamps(t *testrunner.Context) {
	engine := &engineBehaviour{}
	fixture := newFixture(t, serverOptions{engine: engine})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	if outcome.Host.ValidationStartedAt == nil {
		t.Fatal("应记录开始时间")
	}
	if outcome.Host.ValidationCompletedAt == nil {
		t.Fatal("应记录完成时间")
	}
	if outcome.Details.ValidatorVersion != domain.ValidatorVersion {
		t.Fatalf("应记录校验器版本: %s", outcome.Details.ValidatorVersion)
	}
	if outcome.Details.Generation != outcome.Host.ValidationGeneration {
		t.Fatalf("详情代次应与节点一致: %d", outcome.Details.Generation)
	}
}

// 原 TestValidateCancellationConverges 验证取消时校验仍会收敛并释放连接。
func runValidateCancellationConverges(t *testrunner.Context) {
	// 版本探测被刻意放慢，使取消必然发生在检查过程中。
	engine := &engineBehaviour{delay: 300 * time.Millisecond}
	fixture := newFixture(t, serverOptions{engine: engine})
	// 校验已经开始后再取消：模拟 Server 关闭时中断在途校验。
	// 取消发生在检查过程中，因此结果必须收敛为 abnormal 而不是保持 checking。
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	defer cancel()
	outcome, err := fixture.validator.Validate(ctx, app.ValidationRequest{
		HostID: fixture.host.ID.String(),
	})
	if err != nil {
		t.Fatalf("校验本身不应返回错误: %v", err)
	}
	if outcome.Status == domain.ValidationStatusNormal {
		t.Fatal("取消的校验不得判为 normal")
	}
}

// 原 TestValidateFailsWhenCLIVersionTooLow 验证 CLI 版本不足时不进入 normal。
func runValidateFailsWhenCLIVersionTooLow(t *testrunner.Context) {
	engine := &engineBehaviour{}
	fixture := newFixture(t, serverOptions{
		cliVersion: "28.5.2",
		engine:     engine,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	if outcome.Status != domain.ValidationStatusAbnormal {
		t.Fatalf("状态应为 abnormal: %s", outcome.Status)
	}
	check := outcome.Details.Checks[1]
	if check.Name != domain.ValidationCheckDockerVersion {
		t.Fatalf("第二项应为 Docker 版本检查: %s", check.Name)
	}
	if check.Status.Passed() {
		t.Fatal("CLI 版本不足时版本检查不应通过")
	}
	if !strings.Contains(check.Message, docker.MinimumCLIVersion) {
		t.Fatalf("失败原因应给出最低版本要求: %s", check.Message)
	}
	if outcome.Details.Checks[2].Status != domain.CheckStatusNotRun {
		t.Fatalf("版本检查失败时帮助检查应为 not_run: %s", outcome.Details.Checks[2].Status)
	}
	// 版本检查失败时不得发起 Engine API 请求：CLI 版本不足时连 Engine 都不会访问。
	if len(engine.requests) != 0 {
		t.Fatalf("版本检查失败时不应查询 Engine: %+v", engine.requests)
	}
}

// 原 TestValidateFailsWhenEngineVersionTooLow 验证 Engine 版本不足时不进入 normal。
func runValidateFailsWhenEngineVersionTooLow(t *testrunner.Context) {
	fixture := newFixture(t, serverOptions{
		engineVersion: "28.5.2",
		engine:        &engineBehaviour{},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	if outcome.Status != domain.ValidationStatusAbnormal {
		t.Fatalf("状态应为 abnormal: %s", outcome.Status)
	}
	check := outcome.Details.Checks[1]
	if check.Status.Passed() {
		t.Fatal("Engine 版本不足时版本检查不应通过")
	}
	if !strings.Contains(check.Message, docker.MinimumEngineVersion) {
		t.Fatalf("失败原因应给出最低版本要求: %s", check.Message)
	}
}

// 原 TestValidateFailsWhenAPIIncompatible 验证 API 区间不含基准时不进入 normal。
func runValidateFailsWhenAPIIncompatible(t *testrunner.Context) {
	cases := []struct {
		name     string
		apiRange docker.APIRange
	}{
		{name: "最高版本低于基准", apiRange: docker.APIRange{Maximum: "1.51", Minimum: "1.24"}},
		{name: "最低接受版本高于基准", apiRange: docker.APIRange{Maximum: "1.56", Minimum: "1.53"}},
	}
	for _, testCase := range cases {
		// 表驱动子测试按 D6 保留为 Case 内顺序块：子测试名写入日志，断言逐条保持原样。
		t.Logf("子测试：%s", testCase.name)
		fixture := newFixture(t, serverOptions{
			engine: &engineBehaviour{apiRange: testCase.apiRange},
		})
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		outcome := fixture.validate(t, ctx)
		if outcome.Status != domain.ValidationStatusAbnormal {
			t.Fatalf("状态应为 abnormal: %s", outcome.Status)
		}
		check := outcome.Details.Checks[1]
		if check.Status.Passed() {
			t.Fatal("API 基准不可用时版本检查不应通过")
		}
		if !strings.Contains(check.Message, docker.EngineAPIBaseVersion) {
			t.Fatalf("失败原因应给出 API 基准: %s", check.Message)
		}
	}
}

// 原 TestValidateVersionCheckRecordsActualVersions 验证版本检查记录实际版本与期望版本。
func runValidateVersionCheckRecordsActualVersions(t *testrunner.Context) {
	fixture := newFixture(t, serverOptions{
		engine: &engineBehaviour{},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	check := outcome.Details.Checks[1]
	if !check.Status.Passed() {
		t.Fatalf("版本检查应通过: %s", check.Message)
	}
	joined := strings.Join(check.Details, "\n")
	for _, expected := range []string{
		docker.MinimumCLIVersion,
		docker.MinimumEngineVersion,
		docker.EngineAPIBaseVersion,
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("检查详情应包含 %s: %s", expected, joined)
		}
	}
}

// 原 TestValidateRejectsTaskExecutionWhenAbnormal 验证未通过校验的节点被远程任务入口拒绝。
func runValidateRejectsTaskExecutionWhenAbnormal(t *testrunner.Context) {
	host := domain.ExecutionHost{
		ID:               "0123456789abcdef0123456789abcdef",
		Name:             "版本不兼容节点",
		Enabled:          true,
		ValidationStatus: domain.ValidationStatusAbnormal,
		ValidationMessage: "Docker CLI 版本 28.5.2 低于要求的最低版本 " +
			docker.MinimumCLIVersion,
	}
	err := app.EnsureHostUsableForRemoteTask(host)
	if err == nil {
		t.Fatal("abnormal 节点必须被远程任务入口拒绝")
	}
	if !strings.Contains(err.Error(), docker.MinimumCLIVersion) {
		t.Fatalf("拒绝原因应包含版本信息: %v", err)
	}
}
