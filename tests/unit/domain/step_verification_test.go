// Package domain_test 提供步骤验证状态与失败反馈的单元测试。
//
// 用例只覆盖纯函数：不访问数据库、不启动进程，因此可以在任意环境完整执行。
package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// TestDecideStepCloseout 覆盖收口决策的全部组合。
func TestDecideStepCloseout(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		input      domain.StepCloseoutInput
		wantStatus domain.StepStatus
		wantVerify bool
	}{
		{
			name:       "开发失败时不启动验证",
			input:      domain.StepCloseoutInput{Outcome: domain.StepExecutionFailed},
			wantStatus: domain.StepStatusFailed,
		},
		{
			name:       "开发超时按失败处理",
			input:      domain.StepCloseoutInput{Outcome: domain.StepExecutionTimedOut},
			wantStatus: domain.StepStatusFailed,
		},
		{
			name:       "开发取消收敛为取消",
			input:      domain.StepCloseoutInput{Outcome: domain.StepExecutionCancelled},
			wantStatus: domain.StepStatusCancelled,
		},
		{
			name:       "开发中断需要恢复核验",
			input:      domain.StepCloseoutInput{Outcome: domain.StepExecutionInterrupted},
			wantStatus: domain.StepStatusRecoveryRequired,
		},
		{
			name: "开发成功但未验证不得完成",
			input: domain.StepCloseoutInput{
				Outcome:      domain.StepExecutionCompleted,
				Verification: domain.StepVerificationUnverified,
			},
			wantStatus: domain.StepStatusRecoveryRequired,
		},
		{
			name: "验证通过才允许完成",
			input: domain.StepCloseoutInput{
				Outcome:         domain.StepExecutionCompleted,
				VerificationRan: true,
				Verification:    domain.StepVerificationPassed,
			},
			wantStatus: domain.StepStatusCompleted,
			wantVerify: true,
		},
		{
			name: "验证未通过时步骤失败",
			input: domain.StepCloseoutInput{
				Outcome:         domain.StepExecutionCompleted,
				VerificationRan: true,
				Verification:    domain.StepVerificationFailed,
			},
			wantStatus: domain.StepStatusFailed,
		},
		{
			name: "验证被取消时不得判通过",
			input: domain.StepCloseoutInput{
				Outcome:         domain.StepExecutionCompleted,
				VerificationRan: true,
				Verification:    domain.StepVerificationCancelled,
			},
			wantStatus: domain.StepStatusCancelled,
		},
		{
			name: "验证基础设施异常需要恢复核验",
			input: domain.StepCloseoutInput{
				Outcome:         domain.StepExecutionCompleted,
				VerificationRan: true,
				Verification:    domain.StepVerificationInfrastructure,
			},
			wantStatus: domain.StepStatusRecoveryRequired,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := domain.DecideStepCloseout(testCase.input)
			if decision.Status != testCase.wantStatus {
				t.Fatalf(
					"收口状态不符: 期望 %s，实际 %s",
					testCase.wantStatus,
					decision.Status,
				)
			}
			if decision.Verified() != testCase.wantVerify {
				t.Fatalf("Verified 不符: 期望 %v，实际 %v", testCase.wantVerify, decision.Verified())
			}
			if strings.TrimSpace(decision.Summary) == "" {
				t.Fatalf("收口摘要不应为空")
			}
		})
	}
}

// TestDeriveStepVerificationState 覆盖验证状态的推导规则。
func TestDeriveStepVerificationState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		run     *domain.ValidationRun
		results []domain.ValidationCheckResult
		want    domain.StepVerificationState
	}{
		{
			name: "没有验证记录时未验证",
			want: domain.StepVerificationUnverified,
		},
		{
			name: "验证运行中",
			run:  &domain.ValidationRun{Status: domain.ValidationRunStatusRunning},
			want: domain.StepVerificationRunning,
		},
		{
			name: "验证通过",
			run: &domain.ValidationRun{
				Status:  domain.ValidationRunStatusCompleted,
				Verdict: domain.VerificationVerdictPassed,
			},
			want: domain.StepVerificationPassed,
		},
		{
			name: "验证失败",
			run: &domain.ValidationRun{
				Status:  domain.ValidationRunStatusCompleted,
				Verdict: domain.VerificationVerdictFailed,
			},
			want: domain.StepVerificationFailed,
		},
		{
			name: "验证被取消",
			run: &domain.ValidationRun{
				Status:  domain.ValidationRunStatusCancelled,
				Verdict: domain.VerificationVerdictFailed,
			},
			want: domain.StepVerificationCancelled,
		},
		{
			name: "验证整体超时属于基础设施异常",
			run: &domain.ValidationRun{
				Status:  domain.ValidationRunStatusTimedOut,
				Verdict: domain.VerificationVerdictFailed,
			},
			want: domain.StepVerificationInfrastructure,
		},
		{
			name: "必需检查无法启动属于基础设施异常",
			run: &domain.ValidationRun{
				Status:  domain.ValidationRunStatusCompleted,
				Verdict: domain.VerificationVerdictFailed,
			},
			results: []domain.ValidationCheckResult{
				{CheckID: "build", Required: true, Status: domain.ValidationCheckStatusError},
			},
			want: domain.StepVerificationInfrastructure,
		},
		{
			name: "必需检查退出码失败是代码验证失败",
			run: &domain.ValidationRun{
				Status:  domain.ValidationRunStatusCompleted,
				Verdict: domain.VerificationVerdictFailed,
			},
			results: []domain.ValidationCheckResult{
				{CheckID: "build", Required: true, Status: domain.ValidationCheckStatusFailed},
			},
			want: domain.StepVerificationFailed,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			state := domain.DeriveStepVerificationState(testCase.run, testCase.results)
			if state != testCase.want {
				t.Fatalf("验证状态不符: 期望 %s，实际 %s", testCase.want, state)
			}
			if state.Passed() != (testCase.want == domain.StepVerificationPassed) {
				t.Fatalf("Passed 判断不符")
			}
		})
	}
}

// TestDeriveStepVerificationStateFromMatchesRecord 验证便捷形式与记录形式一致。
func TestDeriveStepVerificationStateFromMatchesRecord(t *testing.T) {
	t.Parallel()
	results := []domain.ValidationCheckResult{
		{CheckID: "build", Required: true, Status: domain.ValidationCheckStatusPassed},
	}
	fromRecord := domain.DeriveStepVerificationState(&domain.ValidationRun{
		Status:  domain.ValidationRunStatusCompleted,
		Verdict: domain.VerificationVerdictPassed,
	}, results)
	fromFields := domain.DeriveStepVerificationStateFrom(
		domain.ValidationRunStatusCompleted,
		domain.VerificationVerdictPassed,
		results,
	)
	if fromRecord != fromFields {
		t.Fatalf("两种推导结果不一致: %s vs %s", fromRecord, fromFields)
	}
}

// TestRedactSensitiveText 验证凭据类内容会被过滤。
func TestRedactSensitiveText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		input    string
		mustHide []string
		mustKeep []string
	}{
		{
			name:     "Bearer 令牌",
			input:    "curl -H 'Authorization: Bearer abc123token' https://example.com",
			mustHide: []string{"abc123token"},
			mustKeep: []string{"Authorization"},
		},
		{
			name:     "键值形式的口令",
			input:    "登录失败 password=hunter2 请检查",
			mustHide: []string{"hunter2"},
			mustKeep: []string{"password"},
		},
		{
			name:     "URL 内嵌凭据",
			input:    "clone https://user:secret@git.example.com/team/repo",
			mustHide: []string{"secret"},
			mustKeep: []string{"git.example.com"},
		},
		{
			name:     "API Key",
			input:    "配置 api_key=sk-abcdef 无效",
			mustHide: []string{"sk-abcdef"},
			mustKeep: []string{"api_key"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			redacted := domain.RedactSensitiveText(testCase.input)
			for _, hidden := range testCase.mustHide {
				if strings.Contains(redacted, hidden) {
					t.Fatalf("敏感内容未被过滤: %s", redacted)
				}
			}
			for _, kept := range testCase.mustKeep {
				if !strings.Contains(redacted, kept) {
					t.Fatalf("非敏感内容被误删: %s", redacted)
				}
			}
		})
	}
}

// TestClipFeedbackText 验证反馈文本的长度限制。
func TestClipFeedbackText(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("错误信息", 1000)
	clipped := domain.ClipFeedbackText(long, domain.MaxFeedbackOutputSummary)
	if len(clipped) > domain.MaxFeedbackOutputSummary+32 {
		t.Fatalf("裁剪后长度超出上限: %d", len(clipped))
	}
	if !strings.Contains(clipped, "已截断") {
		t.Fatalf("裁剪后应包含截断标记: %s", clipped)
	}
	short := "编译失败"
	if got := domain.ClipFeedbackText(short, domain.MaxFeedbackReason); got != short {
		t.Fatalf("未超限时不应改变内容: %s", got)
	}
	if got := domain.ClipFeedbackText("   ", domain.MaxFeedbackReason); got != "" {
		t.Fatalf("空白内容应返回空字符串: %q", got)
	}
}

// TestNewValidationFailureFeedback 验证失败反馈的构建与脱敏。
func TestNewValidationFailureFeedback(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	attempt := domain.Attempt{
		ID:         "attempt-1",
		TaskID:     "task-1",
		StepID:     "step-1",
		SessionIDs: []domain.ID{"session-1"},
	}
	run := domain.ValidationRun{
		ID:             "run-1",
		TaskID:         "task-1",
		StepID:         "step-1",
		Status:         domain.ValidationRunStatusCompleted,
		Verdict:        domain.VerificationVerdictFailed,
		FailureSummary: "必需检查未通过: build",
	}
	results := []domain.ValidationCheckResult{
		{
			CheckID:       "build",
			CheckName:     "build",
			Required:      true,
			Status:        domain.ValidationCheckStatusFailed,
			ExitCode:      1,
			FailureReason: "退出码 1 不在允许范围内",
			StderrSummary: "password=hunter2 编译错误",
			StartedAt:     now,
			FinishedAt:    now,
		},
		{
			CheckID:   "lint",
			CheckName: "lint",
			Required:  false,
			Status:    domain.ValidationCheckStatusPassed,
		},
	}
	feedback := domain.NewValidationFailureFeedback(domain.ValidationFeedbackInput{
		TaskID:    "task-1",
		StepID:    "step-1",
		Attempt:   attempt,
		Run:       run,
		Results:   results,
		Developer: attempt.SessionIDs,
	}, now)
	if feedback.DeveloperAttemptID != attempt.ID {
		t.Fatalf("反馈未关联开发尝试")
	}
	if feedback.ValidationRunID != run.ID {
		t.Fatalf("反馈未关联验证记录")
	}
	if len(feedback.Checks) != 1 {
		t.Fatalf("只应保留未通过的检查: %d", len(feedback.Checks))
	}
	if feedback.Checks[0].ExitCode != 1 {
		t.Fatalf("退出码不符: %d", feedback.Checks[0].ExitCode)
	}
	if strings.Contains(feedback.Checks[0].StderrSummary, "hunter2") {
		t.Fatalf("反馈未过滤敏感内容: %s", feedback.Checks[0].StderrSummary)
	}
	if !feedback.HasBlockingChecks() {
		t.Fatalf("必需检查失败应阻止成功")
	}
	if len(feedback.FailedCheckNames()) != 1 || feedback.FailedCheckNames()[0] != "build" {
		t.Fatalf("失败检查名不符: %v", feedback.FailedCheckNames())
	}
	if len(feedback.DeveloperSessionIDs) != 1 {
		t.Fatalf("反馈应保留开发会话引用: %v", feedback.DeveloperSessionIDs)
	}
}
