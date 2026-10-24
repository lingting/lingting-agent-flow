package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// 本文件覆盖任务租约的持久化身份与 fencing 语义。
//
// 这些用例只验证纯领域判定：真正的原子性、条件更新与并发互斥由存储层的集成测试
// （tests/integration/local/scheduler）用真实 SQLite 证明，不能用这里的纯函数替代。

// leasedTask 构造一个带指定租约的任务。
func leasedTask(t *testing.T, owner string, epoch int64, expiresAt time.Time) domain.Task {
	t.Helper()
	task, err := domain.NewTask(domain.NewID(), "ssh://git.example.com/team/repo", "main", "", time.Now())
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.LeaseOwner = owner
	task.LeaseEpoch = epoch
	if !expiresAt.IsZero() {
		task.LeaseExpiresAt = &expiresAt
	}
	return task
}

// TestTaskLeaseHandleValidate 校验租约身份的必填项。
func TestTaskLeaseHandleValidate(t *testing.T) {
	t.Parallel()
	valid := domain.TaskLeaseHandle{TaskID: domain.NewID(), Owner: "worker", Epoch: 1}
	cases := []struct {
		name   string
		handle domain.TaskLeaseHandle
		wantOK bool
	}{
		{name: "完整身份", handle: valid, wantOK: true},
		{name: "缺少任务", handle: domain.TaskLeaseHandle{Owner: "worker", Epoch: 1}},
		{name: "空执行者", handle: domain.TaskLeaseHandle{TaskID: valid.TaskID, Epoch: 1}},
		{name: "代次为零", handle: domain.TaskLeaseHandle{TaskID: valid.TaskID, Owner: "worker"}},
		{name: "代次为负", handle: domain.TaskLeaseHandle{TaskID: valid.TaskID, Owner: "worker", Epoch: -1}},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := testCase.handle.Validate()
			if testCase.wantOK && err != nil {
				t.Fatalf("应通过校验，实际 %v", err)
			}
			if !testCase.wantOK {
				if err == nil {
					t.Fatal("应拒绝非法租约身份")
				}
				if !errors.Is(err, domain.ErrInvalidValue) &&
					!errors.Is(err, domain.ErrEmptyID) {
					t.Fatalf("应返回字段校验错误，实际 %v", err)
				}
			}
		})
	}
}

// TestTaskHoldsLease 校验执行权判定同时要求 owner、代次与有效期。
func TestTaskHoldsLease(t *testing.T) {
	t.Parallel()
	now := time.Now()
	live := now.Add(time.Hour)
	expired := now.Add(-time.Hour)
	owner := "worker-a"
	cases := []struct {
		name  string
		task  domain.Task
		epoch int64
		want  bool
	}{
		{
			name:  "全部匹配且有效",
			task:  leasedTask(t, owner, 3, live),
			epoch: 3,
			want:  true,
		},
		{
			name:  "代次不匹配",
			task:  leasedTask(t, owner, 4, live),
			epoch: 3,
			want:  false,
		},
		{
			name:  "owner 不匹配",
			task:  leasedTask(t, "worker-b", 3, live),
			epoch: 3,
			want:  false,
		},
		{
			name:  "租约已过期",
			task:  leasedTask(t, owner, 3, expired),
			epoch: 3,
			want:  false,
		},
		{
			name:  "没有租约",
			task:  leasedTask(t, "", 0, time.Time{}),
			epoch: 1,
			want:  false,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			handle := domain.TaskLeaseHandle{
				TaskID: testCase.task.ID,
				Owner:  owner,
				Epoch:  testCase.epoch,
			}
			if got := domain.TaskHoldsLease(testCase.task, handle, now); got != testCase.want {
				t.Fatalf("执行权判定不符: 期望 %v，实际 %v", testCase.want, got)
			}
		})
	}
}

// TestDecideTaskLeaseLoss 校验失效原因分类可区分接管、过期与释放。
func TestDecideTaskLeaseLoss(t *testing.T) {
	t.Parallel()
	now := time.Now()
	live := now.Add(time.Hour)
	expired := now.Add(-time.Minute)
	handle := func(task domain.Task, epoch int64) domain.TaskLeaseHandle {
		return domain.TaskLeaseHandle{TaskID: task.ID, Owner: "worker-a", Epoch: epoch}
	}
	cases := []struct {
		name string
		task domain.Task
		want domain.TaskLeaseLossReason
	}{
		{
			name: "仍持有",
			task: leasedTask(t, "worker-a", 2, live),
			want: domain.TaskLeaseLossNone,
		},
		{
			name: "已被新代次接管",
			task: leasedTask(t, "worker-a", 3, live),
			want: domain.TaskLeaseLossSuperseded,
		},
		{
			name: "已被其他执行者接管",
			task: leasedTask(t, "worker-b", 2, live),
			want: domain.TaskLeaseLossSuperseded,
		},
		{
			name: "已过期",
			task: leasedTask(t, "worker-a", 2, expired),
			want: domain.TaskLeaseLossExpired,
		},
		{
			name: "已释放",
			task: leasedTask(t, "", 0, time.Time{}),
			want: domain.TaskLeaseLossReleased,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := domain.DecideTaskLeaseLoss(testCase.task, handle(testCase.task, 2), now)
			if got != testCase.want {
				t.Fatalf("失效原因不符: 期望 %s，实际 %s", testCase.want, got)
			}
		})
	}
}

// TestDecideTaskLeaseRenewal 校验续期必须同时满足 owner、代次与有效期。
func TestDecideTaskLeaseRenewal(t *testing.T) {
	t.Parallel()
	now := time.Now()
	live := now.Add(time.Hour)
	expired := now.Add(-time.Minute)

	liveTask := leasedTask(t, "worker-a", 2, live)
	liveHandle := domain.TaskLeaseHandle{TaskID: liveTask.ID, Owner: "worker-a", Epoch: 2}
	if decision := domain.DecideTaskLeaseRenewal(liveTask, liveHandle, now); !decision.Allowed {
		t.Fatalf("持有有效租约时允许续期，实际拒绝: %+v", decision)
	}

	superseded := leasedTask(t, "worker-a", 3, live)
	if decision := domain.DecideTaskLeaseRenewal(superseded, liveHandle, now); decision.Allowed {
		t.Fatal("被新代次接管后不得续期")
	} else if decision.Reason != domain.TaskLeaseLossSuperseded {
		t.Fatalf("拒绝原因应为接管，实际 %s", decision.Reason)
	}

	expiredTask := leasedTask(t, "worker-a", 2, expired)
	if decision := domain.DecideTaskLeaseRenewal(expiredTask, liveHandle, now); decision.Allowed {
		t.Fatal("租约过期后不得通过迟到续期恢复所有权")
	} else if decision.Reason != domain.TaskLeaseLossExpired {
		t.Fatalf("拒绝原因应为过期，实际 %s", decision.Reason)
	}

	releasedTask := leasedTask(t, "", 0, time.Time{})
	if decision := domain.DecideTaskLeaseRenewal(releasedTask, liveHandle, now); decision.Allowed {
		t.Fatal("租约释放后不得续期")
	}
}

// TestCheckTaskLeaseRelease 校验"没有释放任何记录"不被当作成功。
func TestCheckTaskLeaseRelease(t *testing.T) {
	t.Parallel()
	outcome, err := domain.CheckTaskLeaseRelease(1)
	if err != nil {
		t.Fatalf("释放命中记录时不应返回错误: %v", err)
	}
	if outcome != domain.TaskLeaseReleased {
		t.Fatalf("结论应为已释放，实际 %s", outcome)
	}
	outcome, err = domain.CheckTaskLeaseRelease(0)
	if err == nil {
		t.Fatal("未命中任何记录时必须返回冲突")
	}
	if outcome != domain.TaskLeaseReleaseConflict {
		t.Fatalf("结论应为冲突，实际 %s", outcome)
	}
	if !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("应返回状态错误类别，实际 %v", err)
	}
}

// TestTaskStatusExplicitSchedulingSets 校验显式入口与自动调度的状态集合。
//
// 自动调度集合必须严格小于显式入口集合，且两者都不含 failed：失败任务的重新开始
// 属于独立的用户显式重试入口。
func TestTaskStatusExplicitSchedulingSets(t *testing.T) {
	t.Parallel()
	automatic := domain.AutomaticSchedulingStatuses()
	if len(automatic) != 1 || automatic[0] != domain.TaskStatusQueued {
		t.Fatalf("自动调度集合必须只有 queued，实际 %v", automatic)
	}
	launch := domain.ExplicitLaunchStatuses()
	if len(launch) == 0 {
		t.Fatal("显式启动集合不能为空")
	}
	if containsStatus(launch, domain.TaskStatusFailed) {
		t.Fatal("显式启动集合不得包含 failed")
	}
	for _, status := range launch {
		if !status.CanStart() {
			t.Fatalf("显式启动集合中的 %s 必须允许 CanStart", status)
		}
	}
	planning := domain.ExplicitPlanningStatuses()
	if len(planning) == 0 {
		t.Fatal("显式计划集合不能为空")
	}
	if containsStatus(planning, domain.TaskStatusFailed) {
		t.Fatal("显式计划集合不得包含 failed")
	}
	for _, status := range planning {
		if !status.CanStartPlanning() {
			t.Fatalf("显式计划集合中的 %s 必须允许 CanStartPlanning", status)
		}
	}
	workflow := domain.ExplicitWorkflowStatuses()
	if len(workflow) == 0 {
		t.Fatal("显式工作流集合不能为空")
	}
	for _, status := range workflow {
		if !status.AllowsStepExecution() {
			t.Fatalf("显式工作流集合中的 %s 必须允许推进步骤", status)
		}
	}
	if containsStatus(workflow, domain.TaskStatusQueued) {
		t.Fatal("显式工作流集合不得包含 queued：必须先完成环境准备与计划")
	}
}

// containsStatus 判断集合中是否存在指定状态。
func containsStatus(statuses []domain.TaskStatus, target domain.TaskStatus) bool {
	for _, status := range statuses {
		if status == target {
			return true
		}
	}
	return false
}
