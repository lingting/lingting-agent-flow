// 代码保存领域模型的单元测试。
//
// 用例只验证保存记录自身的判定规则，不依赖数据库、Git、SSH 或 Docker。
package domain_test

import (
	"testing"

	"github.com/lingting/agentflow/internal/domain"
)

// TestRequiresManualRecovery 验证需要人工核验的判定。
//
// 提交、推送与合并三个阶段的结果未知都必须先核验真实仓库状态；已经确认成功或
// 未尝试的阶段不触发人工核验。
func TestRequiresManualRecovery(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		record   domain.CodeSaveRecord
		expected bool
	}{
		{
			name: "提交结果未知",
			record: domain.CodeSaveRecord{
				CommitStatus: domain.CommitStatusUnknown,
				PushStatus:   domain.PushStatusSucceeded,
				MergeStatus:  domain.MergeStatusNotRequested,
			},
			expected: true,
		},
		{
			name: "提交与推送都成功",
			record: domain.CodeSaveRecord{
				CommitStatus: domain.CommitStatusSucceeded,
				PushStatus:   domain.PushStatusSucceeded,
				MergeStatus:  domain.MergeStatusNotRequested,
			},
			expected: false,
		},
		{
			name: "工作区没有变更且未请求合并",
			record: domain.CodeSaveRecord{
				CommitStatus: domain.CommitStatusNoChanges,
				PushStatus:   domain.PushStatusSucceeded,
				MergeStatus:  domain.MergeStatusNotRequested,
			},
			expected: false,
		},
		{
			// 提交失败但推送没有尝试时，现场是确定的：没有产生新提交。
			name: "提交失败但未推送",
			record: domain.CodeSaveRecord{
				CommitStatus: domain.CommitStatusFailed,
				PushStatus:   domain.PushStatusNotAttempted,
				MergeStatus:  domain.MergeStatusNotRequested,
			},
			expected: false,
		},
		{
			name: "推送结果未知",
			record: domain.CodeSaveRecord{
				CommitStatus: domain.CommitStatusSucceeded,
				PushStatus:   domain.PushStatusUnknown,
				MergeStatus:  domain.MergeStatusNotRequested,
			},
			expected: true,
		},
		{
			name: "推送冲突",
			record: domain.CodeSaveRecord{
				CommitStatus: domain.CommitStatusSucceeded,
				PushStatus:   domain.PushStatusConflict,
				MergeStatus:  domain.MergeStatusNotRequested,
			},
			expected: true,
		},
		{
			name: "推送失败",
			record: domain.CodeSaveRecord{
				CommitStatus: domain.CommitStatusSucceeded,
				PushStatus:   domain.PushStatusFailed,
				MergeStatus:  domain.MergeStatusNotRequested,
			},
			expected: true,
		},
		{
			name: "合并结果未知",
			record: domain.CodeSaveRecord{
				CommitStatus: domain.CommitStatusSucceeded,
				PushStatus:   domain.PushStatusSucceeded,
				MergeEnabled: true,
				MergeStatus:  domain.MergeStatusUnknown,
			},
			expected: true,
		},
		{
			name: "合并冲突",
			record: domain.CodeSaveRecord{
				CommitStatus: domain.CommitStatusSucceeded,
				PushStatus:   domain.PushStatusSucceeded,
				MergeEnabled: true,
				MergeStatus:  domain.MergeStatusConflict,
			},
			expected: true,
		},
		{
			name: "合并失败",
			record: domain.CodeSaveRecord{
				CommitStatus: domain.CommitStatusSucceeded,
				PushStatus:   domain.PushStatusSucceeded,
				MergeEnabled: true,
				MergeStatus:  domain.MergeStatusFailed,
			},
			expected: true,
		},
		{
			name: "按配置完成合并",
			record: domain.CodeSaveRecord{
				CommitStatus: domain.CommitStatusSucceeded,
				PushStatus:   domain.PushStatusSucceeded,
				MergeEnabled: true,
				MergeStatus:  domain.MergeStatusSucceeded,
			},
			expected: false,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := testCase.record.RequiresManualRecovery(); got != testCase.expected {
				t.Fatalf("期望 %v，实际 %v", testCase.expected, got)
			}
		})
	}
}
