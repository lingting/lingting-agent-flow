// Package domain_test 验证任务级工作区初始化的纯函数判定与状态机。
//
// 用例只依赖 domain 包：不读数据库、不启动进程、不访问网络，因此可以并发执行。
package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// workspaceTestCommit 是测试使用的固定 commit SHA。
const workspaceTestCommit = "1111111111111111111111111111111111111111"

// newWorkspaceRecord 构造一条 pending 状态的任务级工作区初始化记录。
func newWorkspaceRecord(t *testing.T) domain.TaskWorkspaceInitRecord {
	t.Helper()
	record, err := domain.NewTaskWorkspaceInitRecord(
		"",
		"task-a",
		"container-a",
		"docker-container-a",
		domain.CodeSourceGit,
		domain.DefaultContainerWorkDir,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造工作区初始化记录失败: %v", err)
	}
	return record
}

// TestTaskWorkspaceInitRecordDefaults 校验新记录的默认取值。
func TestTaskWorkspaceInitRecordDefaults(t *testing.T) {
	t.Parallel()
	record := newWorkspaceRecord(t)
	if record.Status != domain.TaskWorkspaceInitStatusPending {
		t.Fatalf("新记录状态应为 pending，实际 %s", record.Status)
	}
	if record.TaskBranch != "agent/task-a" {
		t.Fatalf("任务分支不符: %s", record.TaskBranch)
	}
	if record.FinishedAt != nil {
		t.Fatal("新记录不应有结束时间")
	}
	if record.Succeeded() {
		t.Fatal("pending 不得表示工作区已就绪")
	}
}

// TestTaskWorkspaceInitStatusMachine 校验状态迁移的合法性与终态语义。
func TestTaskWorkspaceInitStatusMachine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		start   domain.TaskWorkspaceInitStatus
		target  domain.TaskWorkspaceInitStatus
		wantErr bool
	}{
		{"pending 可进入 running", domain.TaskWorkspaceInitStatusPending, domain.TaskWorkspaceInitStatusRunning, false},
		{"pending 可直接失败", domain.TaskWorkspaceInitStatusPending, domain.TaskWorkspaceInitStatusFailed, false},
		{"pending 可进入不确定", domain.TaskWorkspaceInitStatusPending, domain.TaskWorkspaceInitStatusUncertain, false},
		{"running 可成功", domain.TaskWorkspaceInitStatusRunning, domain.TaskWorkspaceInitStatusSucceeded, false},
		{"running 可跳过", domain.TaskWorkspaceInitStatusRunning, domain.TaskWorkspaceInitStatusSkipped, false},
		{"running 可超时", domain.TaskWorkspaceInitStatusRunning, domain.TaskWorkspaceInitStatusTimedOut, false},
		{"不确定可收敛为成功", domain.TaskWorkspaceInitStatusUncertain, domain.TaskWorkspaceInitStatusSucceeded, false},
		{"不确定可收敛为失败", domain.TaskWorkspaceInitStatusUncertain, domain.TaskWorkspaceInitStatusFailed, false},
		{"不确定不得回到 running", domain.TaskWorkspaceInitStatusUncertain, domain.TaskWorkspaceInitStatusRunning, true},
		{"成功是终态", domain.TaskWorkspaceInitStatusSucceeded, domain.TaskWorkspaceInitStatusRunning, true},
		{"失败不得重开", domain.TaskWorkspaceInitStatusFailed, domain.TaskWorkspaceInitStatusRunning, true},
		{"超时不得重开", domain.TaskWorkspaceInitStatusTimedOut, domain.TaskWorkspaceInitStatusRunning, true},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			record := newWorkspaceRecord(t)
			record.Status = testCase.start
			err := workspaceTransition(t, &record, testCase.target)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("状态 %s → %s 应被拒绝", testCase.start, testCase.target)
				}
				if !errors.Is(err, domain.ErrInvalidTransition) {
					t.Fatalf("应返回状态迁移错误，实际 %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("状态 %s → %s 应被允许，实际 %v", testCase.start, testCase.target, err)
			}
		})
	}
}

// workspaceTransition 通过领域方法验证一次迁移。
func workspaceTransition(
	t *testing.T,
	record *domain.TaskWorkspaceInitRecord,
	target domain.TaskWorkspaceInitStatus,
) error {
	t.Helper()
	switch target {
	case domain.TaskWorkspaceInitStatusRunning:
		return record.MarkRunning(domain.CodeFetchStageProbe, time.Now().UTC())
	case domain.TaskWorkspaceInitStatusSucceeded:
		return record.MarkSucceeded(
			"main",
			domain.GitBranchSelectionOriginHead,
			workspaceTestCommit,
			domain.TaskBranchSourceNew.String(),
			workspaceTestCommit,
			time.Now().UTC(),
		)
	case domain.TaskWorkspaceInitStatusSkipped:
		return record.MarkSkipped(workspaceTestCommit, time.Now().UTC())
	default:
		return record.MarkFailed(
			target,
			domain.CodeFetchStageClone,
			"code",
			"原因",
			time.Now().UTC(),
		)
	}
}

// TestTaskWorkspaceSucceededRequiresCommit 校验成功记录必须带 commit。
func TestTaskWorkspaceSucceededRequiresCommit(t *testing.T) {
	t.Parallel()
	record := newWorkspaceRecord(t)
	if err := record.MarkRunning(domain.CodeFetchStageProbe, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	err := record.MarkSucceeded(
		"main",
		domain.GitBranchSelectionOriginHead,
		workspaceTestCommit,
		domain.TaskBranchSourceNew.String(),
		"",
		time.Now().UTC(),
	)
	if err == nil {
		t.Fatal("缺少 commit 的成功记录必须被拒绝")
	}
}

// TestTaskWorkspaceFailedRequiresSummary 校验失败记录必须带原因。
func TestTaskWorkspaceFailedRequiresSummary(t *testing.T) {
	t.Parallel()
	record := newWorkspaceRecord(t)
	if err := record.MarkRunning(domain.CodeFetchStageProbe, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	err := record.MarkFailed(
		domain.TaskWorkspaceInitStatusFailed,
		domain.CodeFetchStageClone,
		"code",
		"   ",
		time.Now().UTC(),
	)
	if err == nil {
		t.Fatal("缺少失败原因的失败记录必须被拒绝")
	}
}

// TestTaskWorkspaceRejectsInvalidWorkDir 校验工作目录必须落在容器内工作区。
func TestTaskWorkspaceRejectsInvalidWorkDir(t *testing.T) {
	t.Parallel()
	_, err := domain.NewTaskWorkspaceInitRecord(
		"",
		"task-a",
		"container-a",
		"docker-container-a",
		domain.CodeSourceGit,
		"/tmp/outside",
		time.Now().UTC(),
	)
	if err == nil {
		t.Fatal("工作区之外的目录必须被拒绝")
	}
}

// TestDecideTaskWorkspaceNext 校验"是否需要初始化工作区"的判定规则。
func TestDecideTaskWorkspaceNext(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		state   domain.TaskWorkspaceState
		want    bool
		wantErr bool
	}{
		{
			name:    "容器不可用时拒绝判定",
			state:   domain.TaskWorkspaceState{ContainerUsable: false},
			wantErr: true,
		},
		{
			name:  "没有记录时需要初始化",
			state: domain.TaskWorkspaceState{ContainerUsable: true},
			want:  true,
		},
		{
			name: "已成功时不需要初始化",
			state: domain.TaskWorkspaceState{
				ContainerUsable: true,
				Record: &domain.TaskWorkspaceInitRecord{
					ContainerID: "container-a",
					Status:      domain.TaskWorkspaceInitStatusSucceeded,
				},
			},
			want: false,
		},
		{
			name: "已跳过时不需要初始化",
			state: domain.TaskWorkspaceState{
				ContainerUsable: true,
				Record: &domain.TaskWorkspaceInitRecord{
					ContainerID: "container-a",
					Status:      domain.TaskWorkspaceInitStatusSkipped,
				},
			},
			want: false,
		},
		{
			name: "失败后需要重新初始化",
			state: domain.TaskWorkspaceState{
				ContainerUsable: true,
				Record: &domain.TaskWorkspaceInitRecord{
					ContainerID: "container-a",
					Status:      domain.TaskWorkspaceInitStatusFailed,
				},
			},
			want: true,
		},
		{
			name: "超时后需要重新初始化",
			state: domain.TaskWorkspaceState{
				ContainerUsable: true,
				Record: &domain.TaskWorkspaceInitRecord{
					ContainerID: "container-a",
					Status:      domain.TaskWorkspaceInitStatusTimedOut,
				},
			},
			want: true,
		},
		{
			name: "结果不确定时禁止重放",
			state: domain.TaskWorkspaceState{
				ContainerUsable: true,
				Record: &domain.TaskWorkspaceInitRecord{
					ContainerID: "container-a",
					Status:      domain.TaskWorkspaceInitStatusUncertain,
				},
			},
			wantErr: true,
		},
		{
			name: "仍在推进时禁止抢占",
			state: domain.TaskWorkspaceState{
				ContainerUsable: true,
				Record: &domain.TaskWorkspaceInitRecord{
					ContainerID: "container-a",
					Status:      domain.TaskWorkspaceInitStatusRunning,
				},
			},
			wantErr: true,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			needed, err := domain.DecideTaskWorkspaceNext(testCase.state)
			if testCase.wantErr {
				if err == nil {
					t.Fatal("必须返回状态错误")
				}
				if !errors.Is(err, domain.ErrInvalidState) {
					t.Fatalf("应返回状态错误，实际 %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("判定失败: %v", err)
			}
			if needed != testCase.want {
				t.Fatalf("需要初始化应为 %v，实际 %v", testCase.want, needed)
			}
		})
	}
}

// TestDecideTaskWorkspaceNextUncertainMessage 校验不确定结论给出可读原因。
func TestDecideTaskWorkspaceNextUncertainMessage(t *testing.T) {
	t.Parallel()
	_, err := domain.DecideTaskWorkspaceNext(domain.TaskWorkspaceState{
		ContainerUsable: true,
		Record: &domain.TaskWorkspaceInitRecord{
			TaskID:      "task-a",
			ContainerID: "container-a",
			Status:      domain.TaskWorkspaceInitStatusUncertain,
			Stage:       domain.CodeFetchStageClone,
		},
	})
	if err == nil {
		t.Fatal("必须返回错误")
	}
	message := err.Error()
	if !strings.Contains(message, "container-a") || !strings.Contains(message, "不确定") {
		t.Fatalf("错误信息应说明容器与不确定结论，实际 %s", message)
	}
}

// TestDecideTaskWorkspaceRecovery 校验中断残留的收敛判定。
func TestDecideTaskWorkspaceRecovery(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		status   domain.TaskWorkspaceInitStatus
		want     bool
		wantNext domain.TaskWorkspaceInitStatus
	}{
		{
			name:     "pending 收敛为不确定",
			status:   domain.TaskWorkspaceInitStatusPending,
			want:     true,
			wantNext: domain.TaskWorkspaceInitStatusUncertain,
		},
		{
			name:     "running 收敛为不确定",
			status:   domain.TaskWorkspaceInitStatusRunning,
			want:     true,
			wantNext: domain.TaskWorkspaceInitStatusUncertain,
		},
		{
			name:   "成功记录不改写",
			status: domain.TaskWorkspaceInitStatusSucceeded,
			want:   false,
		},
		{
			name:   "不确定记录不改写",
			status: domain.TaskWorkspaceInitStatusUncertain,
			want:   false,
		},
		{
			name:   "失败记录不改写",
			status: domain.TaskWorkspaceInitStatusFailed,
			want:   false,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			needed, next := domain.DecideTaskWorkspaceRecovery(domain.TaskWorkspaceRecoveryInput{
				Record: domain.TaskWorkspaceInitRecord{Status: testCase.status},
			})
			if needed != testCase.want {
				t.Fatalf("是否需要收敛应为 %v，实际 %v", testCase.want, needed)
			}
			if testCase.want && next != testCase.wantNext {
				t.Fatalf("目标状态应为 %s，实际 %s", testCase.wantNext, next)
			}
		})
	}
}

// TestTaskWorkspaceInterruptedSummaryIsReadable 校验收敛摘要说明来源。
func TestTaskWorkspaceInterruptedSummaryIsReadable(t *testing.T) {
	t.Parallel()
	summary := domain.TaskWorkspaceInterruptedSummary()
	if !strings.Contains(summary, "进程已退出") {
		t.Fatalf("摘要应说明结论来自启动恢复，实际 %s", summary)
	}
	if !strings.Contains(summary, "不能重放") {
		t.Fatalf("摘要应说明不得重放代码获取，实际 %s", summary)
	}
}
