// Package app_test 验证验证引擎的四类中断语义。
//
// 用例使用真实 SQLite 与正式的 app.ValidationService，只把容器核验与命令执行替换
// 为受控替身。断言的对象是数据库里真实留下的 validation_runs 记录与执行尝试，
// 而不是方法的返回值。
package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// TestValidationCancelIsNotPassed 验证用户取消不会被判定为通过。
//
// 取消说明"没有拿到可用证据"，与"确定性检查失败"是不同事实：流程状态必须是
// cancelled，结论必须是 failed，且 error_detail 必须说明中断来源。
func TestValidationCancelIsNotPassed(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	fixture.containers.block = true
	fixture.containers.started = make(chan struct{})

	ctx, cancel := context.WithCancel(fixture.ctx)
	done := make(chan struct {
		result app.ValidationExecuteResult
		err    error
	}, 1)
	go func() {
		result, err := fixture.service.Validate(ctx, fixture.request())
		done <- struct {
			result app.ValidationExecuteResult
			err    error
		}{result: result, err: err}
	}()
	<-fixture.containers.started
	cancel()
	finished := <-done

	if finished.err != nil {
		t.Fatalf("调用方取消应返回结果而不是错误: %v", finished.err)
	}
	if finished.result.Passed {
		t.Fatal("被取消的验证不能判定通过")
	}
	if finished.result.Status != domain.ValidationRunStatusCancelled {
		t.Fatalf("流程状态应为 cancelled，实际 %s", finished.result.Status)
	}
	if finished.result.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("结论必须是不通过，实际 %s", finished.result.Verdict)
	}
	run := fixture.validationRun(t, finished.result.ValidationRunID)
	if run.Status != domain.ValidationRunStatusCancelled {
		t.Fatalf("持久化状态应为 cancelled，实际 %s", run.Status)
	}
	if run.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("持久化结论必须是不通过，实际 %s", run.Verdict)
	}
	// 中断导致无法验证，绝不能写成"必需检查未通过"。
	if containsCodeFailureWording(run.FailureSummary) {
		t.Fatalf("中断说明不得暗示代码检查失败: %q", run.FailureSummary)
	}
}

// TestValidationTimeoutIsNotPassed 验证整体超时不会被判定为通过。
func TestValidationTimeoutIsNotPassed(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build", "test"))
	fixture.containers.block = true
	fixture.containers.started = make(chan struct{})
	request := fixture.request()
	request.Timeout = 100 * time.Millisecond
	result, err := fixture.service.Validate(fixture.ctx, request)
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	if result.Passed {
		t.Fatal("超时的验证不能判定通过")
	}
	if result.Status != domain.ValidationRunStatusTimedOut {
		t.Fatalf("流程状态应为 timed_out，实际 %s", result.Status)
	}
	run := fixture.validationRun(t, result.ValidationRunID)
	if run.Status != domain.ValidationRunStatusTimedOut {
		t.Fatalf("持久化状态应为 timed_out，实际 %s", run.Status)
	}
	if run.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("持久化结论必须是不通过，实际 %s", run.Verdict)
	}
	if containsCodeFailureWording(run.FailureSummary) {
		t.Fatalf("超时说明不得暗示代码检查失败: %q", run.FailureSummary)
	}
}

// TestValidationServiceStopIsNotUserCancel 验证服务停止与用户取消被区分。
//
// 两者都会终止命令，但诊断信息必须说明真实来源，否则服务关闭会被误读为
// "用户取消了验证"。服务停止的流程状态是 failed，取消是 cancelled。
func TestValidationServiceStopIsNotUserCancel(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	fixture.containers.block = true
	fixture.containers.started = make(chan struct{})
	done := make(chan struct {
		result app.ValidationExecuteResult
		err    error
	}, 1)
	go func() {
		result, err := fixture.service.Validate(fixture.ctx, fixture.request())
		done <- struct {
			result app.ValidationExecuteResult
			err    error
		}{result: result, err: err}
	}()
	<-fixture.containers.started
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := fixture.service.Stop(stopCtx); err != nil {
		t.Fatalf("停止验证服务失败: %v", err)
	}
	finished := <-done
	if finished.err != nil {
		t.Fatalf("服务停止应返回结果而不是错误: %v", finished.err)
	}
	if finished.result.Passed {
		t.Fatal("被中断的验证不能判定通过")
	}
	// 服务停止必须收敛为 failed，不能伪装成用户取消。
	if finished.result.Status != domain.ValidationRunStatusFailed {
		t.Fatalf("服务停止的流程状态应为 failed，实际 %s", finished.result.Status)
	}
	run := fixture.validationRun(t, finished.result.ValidationRunID)
	if run.Status != domain.ValidationRunStatusFailed {
		t.Fatalf("持久化状态应为 failed，实际 %s", run.Status)
	}
	if run.Status == domain.ValidationRunStatusCancelled {
		t.Fatal("服务停止不得被记录为用户取消")
	}
	if run.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("持久化结论必须是不通过，实际 %s", run.Verdict)
	}
}

// TestValidationInterruptedChecksAreSkipped 验证中断后的检查记为 skipped 而非通过。
func TestValidationInterruptedChecksAreSkipped(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build", "test", "lint"))
	fixture.containers.block = true
	fixture.containers.started = make(chan struct{})
	request := fixture.request()
	request.Timeout = 100 * time.Millisecond
	result, err := fixture.service.Validate(fixture.ctx, request)
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	if len(result.Checks) == 0 {
		t.Fatal("中断后仍必须记录未执行的检查")
	}
	for _, check := range result.Checks {
		if check.Status.Passed() {
			t.Fatalf("中断时检查 %s 不得记为通过", check.CheckName)
		}
	}
}

// TestValidationInterruptionDoesNotCallValidator 验证中断时不会发起语义审查。
//
// 语义审查只在确定性检查全部通过之后才被允许：中断说明没有拿到可用证据，
// 此时发起模型调用既浪费资源，也会让"审查失败"被误读为"代码有问题"。
func TestValidationInterruptionDoesNotCallValidator(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	fixture.containers.block = true
	fixture.containers.started = make(chan struct{})
	reviewer := &countingReviewer{}
	service, err := app.NewValidationService(app.ValidationOptions{
		Store:      fixture.db,
		Containers: fixture.containers,
		OutputRoot: fixture.workspace.Dir("validations-review"),
		Reviewer:   reviewer,
		Logger:     fixture.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造验证引擎失败: %v", err)
	}
	startValidationService(t, service)
	// 任务声明了验证角色，因此"要不要审查"的判定真的会被走到。
	task, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	request := fixture.request()
	request.Timeout = 100 * time.Millisecond
	if _, err := service.Validate(fixture.ctx, request); err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	if reviewer.calls != 0 {
		t.Fatalf("中断的验证不得发起语义审查，实际调用 %d 次", reviewer.calls)
	}
	_ = task
}

// TestValidationInterruptionKeepsAttemptConsistent 验证中断后尝试与验证记录一致。
//
// 验证没有跑完时不得把"通过"写回尝试：attempt.verification_verdict 必须与
// validation_runs.verdict 一致，且都不是 passed。
func TestValidationInterruptionKeepsAttemptConsistent(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	fixture.containers.block = true
	fixture.containers.started = make(chan struct{})
	request := fixture.request()
	request.Timeout = 100 * time.Millisecond
	result, err := fixture.service.Validate(fixture.ctx, request)
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	run := fixture.validationRun(t, result.ValidationRunID)
	attempt, err := fixture.db.GetAttempt(fixture.ctx, fixture.attempt.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	if attempt.VerificationVerdict == domain.VerificationVerdictPassed {
		t.Fatal("中断的验证不得把通过写回执行尝试")
	}
	if attempt.VerificationVerdict != run.Verdict {
		t.Fatalf(
			"尝试判定与验证结论必须一致: %s vs %s",
			attempt.VerificationVerdict,
			run.Verdict,
		)
	}
}

// TestValidationNotPersistedIsReported 验证结论写入失败时不报告成功。
func TestValidationNotPersistedIsReported(t *testing.T) {
	t.Parallel()
	fixture := newValidationFixture(t, requiredSpec("build"))
	fixture.reply("build", checkReply{exitCode: 0})
	failing := &failingValidationStore{
		ValidationStore: fixture.db,
		finishErr:       errors.New("数据库写入失败"),
	}
	service, err := app.NewValidationService(app.ValidationOptions{
		Store:      failing,
		Containers: fixture.containers,
		OutputRoot: fixture.workspace.Dir("validations-failing"),
		Logger:     fixture.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造验证引擎失败: %v", err)
	}
	startValidationService(t, service)
	result, err := service.Validate(fixture.ctx, fixture.request())
	if !errors.Is(err, app.ErrValidationNotPersisted) {
		t.Fatalf("结论写入失败必须返回未持久化错误，实际 %v", err)
	}
	if result.Persisted {
		t.Fatal("写入失败时不得报告已持久化")
	}
	if len(result.PersistenceErrors) == 0 {
		t.Fatal("写入失败必须给出原因")
	}
}

// containsCodeFailureWording 判断文本是否暗示"代码检查失败"。
//
// 中断与"确定性检查失败"的恢复动作不同，混淆会让用户误以为代码有问题。
func containsCodeFailureWording(summary string) bool {
	for _, wording := range []string{"必需检查未通过", "检查未通过", "必需检查失败"} {
		if contains(summary, wording) {
			return true
		}
	}
	return false
}

// contains 判断文本是否包含子串。
func contains(text, sub string) bool {
	return len(sub) == 0 || (len(text) >= len(sub) && indexOf(text, sub) >= 0)
}

// indexOf 返回子串位置，不存在时返回 -1。
func indexOf(text, sub string) int {
	for index := 0; index+len(sub) <= len(text); index++ {
		if text[index:index+len(sub)] == sub {
			return index
		}
	}
	return -1
}

// countingReviewer 统计语义审查调用次数。
type countingReviewer struct {
	calls int
}

// Review 记录调用并返回"审查未通过"的受控结论。
func (r *countingReviewer) Review(
	ctx context.Context,
	request app.ReviewRequest,
) (app.ReviewOutcome, error) {
	r.calls++
	return app.ReviewOutcome{Status: domain.ReviewStatusCallFailed}, nil
}
