// 任务收尾闭环判定的单元测试。
//
// 这些用例固定的是"任务结论是否闭环"的派生规则与"收尾停滞如何收敛"的规则：
// 它们回答的是"某个状态组合是否可以被当作成功"，而不是某个方法调用是否成功。
package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// closeoutTask 构造一个处于指定状态的任务。
func closeoutTask(t *testing.T, status domain.TaskStatus) domain.Task {
	t.Helper()
	task, err := domain.NewTask(
		domain.NewID(),
		"ssh://git.example.com/team/repo",
		"main",
		"",
		workflowRunAt,
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.Status = status
	return task
}

// completedRun 构造一个已完成的执行记录。
func completedRun(t *testing.T, taskID domain.ID) domain.WorkflowRun {
	t.Helper()
	step := workflowStep(t, taskID, 1, "步骤")
	run, err := domain.NewWorkflowRun(taskID, []domain.Step{step}, workflowRunAt)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := run.Start("worker", time.Minute, workflowRunAt); err != nil {
		t.Fatalf("登记执行权失败: %v", err)
	}
	if err := run.Complete("完成", workflowRunAt); err != nil {
		t.Fatalf("标记完成失败: %v", err)
	}
	return run
}

// runningRun 构造一个仍持有有效执行权的执行记录。
func runningRun(t *testing.T, taskID domain.ID) domain.WorkflowRun {
	t.Helper()
	step := workflowStep(t, taskID, 1, "步骤")
	run, err := domain.NewWorkflowRun(taskID, []domain.Step{step}, workflowRunAt)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := run.Start("worker", time.Hour, workflowRunAt); err != nil {
		t.Fatalf("登记执行权失败: %v", err)
	}
	return run
}

// TestDecideTaskCloseoutViewCombinations 覆盖收尾闭环派生规则的关键组合。
func TestDecideTaskCloseoutViewCombinations(t *testing.T) {
	t.Parallel()
	taskID := domain.NewID()
	completed := completedRun(t, taskID)
	running := runningRun(t, taskID)
	blocked := completed
	blocked.Status = domain.WorkflowRunBlocked
	now := workflowRunAt.Add(time.Minute)

	cases := []struct {
		name string
		task domain.Task
		run  *domain.WorkflowRun
		want domain.TaskCloseoutState
	}{
		{
			name: "任务完成且执行记录完成才算确认",
			task: func() domain.Task {
				task := closeoutTask(t, domain.TaskStatusCompleted)
				task.FinalResult = domain.TaskResultSucceeded
				return task
			}(),
			run:  &completed,
			want: domain.TaskCloseoutConfirmed,
		},
		{
			name: "任务完成但执行记录需要人工核验属于不一致",
			task: func() domain.Task {
				task := closeoutTask(t, domain.TaskStatusCompleted)
				task.FinalResult = domain.TaskResultSucceeded
				return task
			}(),
			run:  &blocked,
			want: domain.TaskCloseoutInconsistent,
		},
		{
			name: "任务完成但没有执行记录属于不一致",
			task: func() domain.Task {
				task := closeoutTask(t, domain.TaskStatusCompleted)
				task.FinalResult = domain.TaskResultSucceeded
				return task
			}(),
			want: domain.TaskCloseoutInconsistent,
		},
		{
			name: "任务完成但结论不是成功属于未闭环",
			task: closeoutTask(t, domain.TaskStatusCompleted),
			run:  &completed,
			want: domain.TaskCloseoutUnconfirmed,
		},
		{
			name: "任务完成而执行记录仍在推进属于进行中",
			task: func() domain.Task {
				task := closeoutTask(t, domain.TaskStatusCompleted)
				task.FinalResult = domain.TaskResultSucceeded
				return task
			}(),
			run:  &running,
			want: domain.TaskCloseoutPending,
		},
		{
			name: "任务未完成而执行记录仍在推进属于进行中",
			task: closeoutTask(t, domain.TaskStatusDeveloping),
			run:  &running,
			want: domain.TaskCloseoutPending,
		},
		{
			name: "任务未完成且没有执行权属于未闭环",
			task: closeoutTask(t, domain.TaskStatusSaving),
			run:  &blocked,
			want: domain.TaskCloseoutUnconfirmed,
		},
		{
			name: "已取消任务由取消语义独占",
			task: closeoutTask(t, domain.TaskStatusCancelled),
			run:  &completed,
			want: domain.TaskCloseoutCancelled,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := domain.DecideTaskCloseoutView(domain.TaskCloseoutViewInput{
				Task: testCase.task,
				Run:  testCase.run,
				Now:  now,
			})
			if got != testCase.want {
				t.Fatalf("派生结论不正确: 期望 %s，实际 %s", testCase.want, got)
			}
			if strings.TrimSpace(got.Summary()) == "" {
				t.Fatal("派生结论必须给出可读说明")
			}
		})
	}
}

// TestTaskCloseoutInconsistentNeedsAttention 验证不一致现场需要人工关注。
func TestTaskCloseoutInconsistentNeedsAttention(t *testing.T) {
	t.Parallel()
	if !domain.TaskCloseoutInconsistent.NeedsAttention() {
		t.Fatal("不一致现场必须需要人工关注")
	}
	if domain.TaskCloseoutConfirmed.NeedsAttention() {
		t.Fatal("已确认的收尾不需要人工关注")
	}
}

// TestDecideTaskCloseoutRecoveryStalledInSaving 验证停滞任务被收敛。
func TestDecideTaskCloseoutRecoveryStalledInSaving(t *testing.T) {
	t.Parallel()
	task := closeoutTask(t, domain.TaskStatusSaving)
	now := workflowRunAt.Add(time.Minute)
	cases := []struct {
		name   string
		run    *domain.WorkflowRun
		decide domain.WorkflowCloseoutDecision
		want   domain.TaskStatus
		compl  bool
	}{
		{
			name: "执行记录已完成且证据齐备则补齐成功",
			run: func() *domain.WorkflowRun {
				run := completedRun(t, task.ID)
				return &run
			}(),
			decide: domain.WorkflowCloseoutDecision{Status: domain.WorkflowRunCompleted},
			want:   domain.TaskStatusCompleted,
			compl:  true,
		},
		{
			name: "执行记录已完成但证据不成立则要求核验",
			run: func() *domain.WorkflowRun {
				run := completedRun(t, task.ID)
				return &run
			}(),
			decide: domain.WorkflowCloseoutDecision{
				Status:     domain.WorkflowRunRecoveryRequired,
				ErrorClass: "session_not_persisted",
			},
			want: domain.TaskStatusRecoveryRequired,
		},
		{
			name: "执行记录阻塞且无完成结论则要求核验",
			run: func() *domain.WorkflowRun {
				run := completedRun(t, task.ID)
				run.Status = domain.WorkflowRunBlocked
				return &run
			}(),
			want: domain.TaskStatusRecoveryRequired,
		},
		{
			name: "没有任何执行记录则要求核验",
			run:  nil,
			want: domain.TaskStatusRecoveryRequired,
		},
		{
			name: "仍有有效执行权时不抢占",
			run: func() *domain.WorkflowRun {
				run := runningRun(t, task.ID)
				return &run
			}(),
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			result := domain.DecideTaskCloseoutRecovery(domain.TaskCloseoutRecoveryInput{
				Task:     task,
				Run:      testCase.run,
				Decision: testCase.decide,
				Now:      now,
			})
			if testCase.want == "" {
				if result.Needed {
					t.Fatalf("仍有执行权时不应收敛，实际需要收敛到 %s", result.Next)
				}
				return
			}
			if !result.Needed {
				t.Fatal("停滞在收尾阶段的任务必须被收敛")
			}
			if result.Next != testCase.want {
				t.Fatalf("收敛目标不正确: 期望 %s，实际 %s", testCase.want, result.Next)
			}
			if result.Complete != testCase.compl {
				t.Fatalf("完成判定不正确: %+v", result)
			}
		})
	}
}

// TestDecideTaskCloseoutRecoveryIgnoresOtherStatuses 验证只处理收尾阶段。
func TestDecideTaskCloseoutRecoveryIgnoresOtherStatuses(t *testing.T) {
	t.Parallel()
	now := workflowRunAt.Add(time.Minute)
	for _, status := range []domain.TaskStatus{
		domain.TaskStatusQueued,
		domain.TaskStatusDeveloping,
		domain.TaskStatusPlanning,
		domain.TaskStatusCompleted,
		domain.TaskStatusCancelled,
	} {
		task := closeoutTask(t, status)
		result := domain.DecideTaskCloseoutRecovery(domain.TaskCloseoutRecoveryInput{
			Task: task,
			Now:  now,
		})
		if result.Needed {
			t.Fatalf("状态 %s 不属于收尾停滞，不应被本入口收敛", status)
		}
	}
}

// TestTaskCloseoutInconsistentSummaryMentionsEvidence 验证不一致说明可读且不含结论。
func TestTaskCloseoutInconsistentSummaryMentionsEvidence(t *testing.T) {
	t.Parallel()
	task := closeoutTask(t, domain.TaskStatusCompleted)
	summary := domain.TaskCloseoutInconsistentSummary(task, nil)
	for _, keyword := range []string{"没有任何工作流执行记录", "不得当作确定成功"} {
		if !strings.Contains(summary, keyword) {
			t.Fatalf("不一致说明缺少 %q: %s", keyword, summary)
		}
	}
}
