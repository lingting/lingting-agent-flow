// 代码获取残留恢复判定的单元测试。
//
// 这些用例只验证纯函数规则：不依赖数据库、容器、Git 或 SSH。它们固定的是
// "什么情况下允许收敛、什么情况下必须保持不确定"这一组语义，而不是调用顺序。
package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// TestDecideCodeFetchRecoveryConvergesInterrupted 验证中断记录收敛为 uncertain。
func TestDecideCodeFetchRecoveryConvergesInterrupted(t *testing.T) {
	t.Parallel()
	for _, status := range []domain.CodeFetchStatus{
		domain.CodeFetchStatusPending,
		domain.CodeFetchStatusRunning,
	} {
		record := newCodeFetch(t)
		if err := record.MarkFailed(status, domain.CodeFetchStageProbe, "", "", time.Now().UTC()); err != nil {
			// pending 无法直接 MarkFailed 到自身；用状态机允许的路径构造现场。
			record.Status = status
		}
		needed, next := domain.DecideCodeFetchRecovery(domain.CodeFetchRecoveryInput{
			Record: record,
		})
		if !needed {
			t.Fatalf("状态 %s 的中断记录应收敛", status)
		}
		if next != domain.CodeFetchStatusUncertain {
			t.Fatalf("收敛目标必须是 uncertain，实际 %s", next)
		}
	}
}

// succeededCodeFetch 构造一条已成功的代码获取记录。
//
// 状态机要求先进入 running 才能结束：直接写终态会被拒绝，因此这里按真实路径迁移。
func succeededCodeFetch(t *testing.T) domain.CodeFetchRecord {
	t.Helper()
	record := newCodeFetch(t)
	if err := record.MarkRunning(domain.CodeFetchStageClone, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	if err := record.MarkSucceeded(
		"main",
		domain.GitBranchSelectionExplicit,
		testBaseSHA,
		domain.TaskBranchSourceNew.String(),
		testHeadSHA,
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("标记成功失败: %v", err)
	}
	return record
}

// TestDecideCodeFetchRecoveryKeepsTerminalRecords 验证终态记录不被改写。
func TestDecideCodeFetchRecoveryKeepsTerminalRecords(t *testing.T) {
	t.Parallel()
	record := succeededCodeFetch(t)
	needed, _ := domain.DecideCodeFetchRecovery(domain.CodeFetchRecoveryInput{Record: record})
	if needed {
		t.Fatal("已成功的记录不得被启动恢复改写")
	}
}

// TestDecideCodeFetchRecoveryKeepsLiveOwner 验证有执行者时不抢占。
func TestDecideCodeFetchRecoveryKeepsLiveOwner(t *testing.T) {
	t.Parallel()
	record := newCodeFetch(t)
	needed, _ := domain.DecideCodeFetchRecovery(domain.CodeFetchRecoveryInput{
		Record:    record,
		LiveOwner: true,
	})
	if needed {
		t.Fatal("任务仍持有有效执行权时不得收敛代码获取记录")
	}
}

// TestDecideCodeFetchResumeBlocksUncertain 验证不确定现场禁止重放。
func TestDecideCodeFetchResumeBlocksUncertain(t *testing.T) {
	t.Parallel()
	record := newCodeFetch(t)
	record.Status = domain.CodeFetchStatusUncertain
	decision := domain.DecideCodeFetchResume([]domain.CodeFetchRecord{record})
	if decision.Action != domain.CodeFetchResumeBlocked {
		t.Fatalf("不确定现场必须阻止重放，实际 %s", decision.Action)
	}
	if decision.ErrorClass != "code_fetch_uncertain" {
		t.Fatalf("错误分类不正确: %s", decision.ErrorClass)
	}
}

// TestDecideCodeFetchResumeRequiresVerifyWhileActive 验证活动记录要求先核验。
func TestDecideCodeFetchResumeRequiresVerifyWhileActive(t *testing.T) {
	t.Parallel()
	record := newCodeFetch(t)
	decision := domain.DecideCodeFetchResume([]domain.CodeFetchRecord{record})
	if decision.Action != domain.CodeFetchResumeVerify {
		t.Fatalf("活动记录要求先核验，实际 %s", decision.Action)
	}
}

// TestDecideCodeFetchResumeProceedsWhenConverged 验证全部收敛后可以继续。
func TestDecideCodeFetchResumeProceedsWhenConverged(t *testing.T) {
	t.Parallel()
	record := succeededCodeFetch(t)
	decision := domain.DecideCodeFetchResume([]domain.CodeFetchRecord{record})
	if decision.Action != domain.CodeFetchResumeProceed {
		t.Fatalf("已收敛的记录不应阻止继续，实际 %s（%s）", decision.Action, decision.Summary)
	}
}

// TestDecideCodeFetchResumePrefersBlockedOverVerify 验证更保守的结论优先。
func TestDecideCodeFetchResumePrefersBlockedOverVerify(t *testing.T) {
	t.Parallel()
	uncertain := newCodeFetch(t)
	uncertain.Status = domain.CodeFetchStatusUncertain
	active := newCodeFetch(t)
	decision := domain.DecideCodeFetchResume([]domain.CodeFetchRecord{active, uncertain})
	if decision.Action != domain.CodeFetchResumeBlocked {
		t.Fatalf("同时存在活动与不确定记录时必须取最保守结论，实际 %s", decision.Action)
	}
}

// TestDecideCodeFetchVerificationRequiresConclusiveFacts 验证只在事实充分时收敛。
func TestDecideCodeFetchVerificationRequiresConclusiveFacts(t *testing.T) {
	t.Parallel()
	record := newCodeFetch(t)
	record.Status = domain.CodeFetchStatusUncertain
	cases := []struct {
		name         string
		verification domain.CodeFetchVerification
		converged    bool
		status       domain.CodeFetchStatus
	}{
		{
			name:         "核验失败保持不确定",
			verification: domain.CodeFetchVerification{ReadFailed: true, ReadError: "节点不可达"},
		},
		{
			name:         "目录不存在可判未获取",
			verification: domain.CodeFetchVerification{WorkDirExists: false},
			converged:    true,
			status:       domain.CodeFetchStatusFailed,
		},
		{
			name: "目录存在但不是仓库保持不确定",
			verification: domain.CodeFetchVerification{
				WorkDirExists: true,
			},
		},
		{
			name: "分支不匹配保持不确定",
			verification: domain.CodeFetchVerification{
				WorkDirExists:   true,
				IsGitRepository: true,
				Branch:          "main",
				Head:            testHeadSHA,
			},
		},
		{
			name: "HEAD 不可读保持不确定",
			verification: domain.CodeFetchVerification{
				WorkDirExists:   true,
				IsGitRepository: true,
				Branch:          record.TaskBranch,
			},
		},
		{
			name: "事实齐备收敛为成功",
			verification: domain.CodeFetchVerification{
				WorkDirExists:   true,
				IsGitRepository: true,
				Branch:          record.TaskBranch,
				Head:            testHeadSHA,
			},
			converged: true,
			status:    domain.CodeFetchStatusSucceeded,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := domain.DecideCodeFetchVerification(record, testCase.verification)
			if decision.Converged != testCase.converged {
				t.Fatalf("收敛结论不正确: %+v", decision)
			}
			if testCase.converged && decision.Status != testCase.status {
				t.Fatalf("收敛状态不正确: %s", decision.Status)
			}
			if strings.TrimSpace(decision.Summary) == "" {
				t.Fatal("核验结论必须给出可读说明")
			}
		})
	}
}

// TestCodeFetchInterruptedSummaryMentionsRecovery 验证收敛摘要说明来源。
func TestCodeFetchInterruptedSummaryMentionsRecovery(t *testing.T) {
	t.Parallel()
	summary := domain.CodeFetchInterruptedSummary()
	for _, keyword := range []string{"进程已退出", "核验", "不能重放"} {
		if !strings.Contains(summary, keyword) {
			t.Fatalf("收敛摘要缺少 %q: %s", keyword, summary)
		}
	}
}
