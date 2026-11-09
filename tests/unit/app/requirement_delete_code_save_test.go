package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// 本文件验证"代码保存副作用处于不同形态"时需求删除的失败关闭规则。
//
// 用例覆盖三类现场，全部经由真实 SQLite、真实仓储与真实领域判定：
//   - 从未开始：没有代码保存记录；
//   - 已经完成：提交与推送都成功，合并未请求；
//   - 结果不明：提交 / 推送 / 合并处于 pending、unknown、conflict 或核验失败；
//   - 已核验确认未发生：允许重新执行，因此不阻止删除。
//
// 断言同时检查返回错误与数据库事实：被拒绝时需求与全部任务都不得写入删除时间，
// 只有通过核验的现场才允许在同一事务内软删除。

// codeSaveCommitSHA 是测试用的合法提交 SHA。
const codeSaveCommitSHA = "0123456789abcdef0123456789abcdef01234567"

// testCodeSaveRecord 构造一条属于任务的代码保存记录。
func testCodeSaveRecord(
	t *testing.T,
	task domain.Task,
	mergeEnabled bool,
) domain.CodeSaveRecord {
	t.Helper()
	branch, err := domain.TaskBranchName(task.ID.String())
	if err != nil {
		t.Fatalf("生成任务分支名失败: %v", err)
	}
	record, err := domain.NewCodeSaveRecord(
		domain.ID(""),
		task.ID.String(),
		"container-1",
		"docker-1",
		"origin",
		"ssh://git.example.com/team/confirm-repo",
		branch,
		"main",
		mergeEnabled,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造代码保存记录失败: %v", err)
	}
	return record
}

// completedCommit 把记录推进到"提交成功"。
func completedCommit(t *testing.T, record *domain.CodeSaveRecord) {
	t.Helper()
	if err := record.MarkCommit(
		domain.CommitStatusSucceeded,
		codeSaveCommitSHA,
		"实现需求",
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("记录提交成功失败: %v", err)
	}
}

// TestDeleteCodeSaveStateMatrix 校验代码保存各种形态对需求删除的影响。
func TestDeleteCodeSaveStateMatrix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// withoutRecord 表示该现场没有任何代码保存记录（代码保存从未开始）。
		withoutRecord bool
		// mergeEnabled 决定记录是否请求合并。
		mergeEnabled bool
		// prepare 在写入数据库前把记录推进到目标形态。
		prepare func(t *testing.T, record *domain.CodeSaveRecord)
		// wantDeleted 表示本次删除应当成功。
		wantDeleted bool
	}{
		{
			name:          "从未开始",
			withoutRecord: true,
			wantDeleted:   true,
		},
		{
			name: "提交与推送已完成",
			prepare: func(t *testing.T, record *domain.CodeSaveRecord) {
				completedCommit(t, record)
				if err := record.MarkPush(
					domain.PushStatusSucceeded,
					codeSaveCommitSHA,
					time.Now().UTC(),
				); err != nil {
					t.Fatalf("记录推送成功失败: %v", err)
				}
			},
			wantDeleted: true,
		},
		{
			name: "提交仍为 pending 且未核验",
		},
		{
			name: "提交已核验但无法判定",
			prepare: func(t *testing.T, record *domain.CodeSaveRecord) {
				if err := record.MarkRecovery(
					domain.RecoveryStatusUnverifiable,
					time.Now().UTC(),
				); err != nil {
					t.Fatalf("记录核验结论失败: %v", err)
				}
			},
		},
		{
			name: "记录写入未确认",
			prepare: func(t *testing.T, record *domain.CodeSaveRecord) {
				record.MarkPersistFailure("写入数据库失败", time.Now().UTC())
			},
		},
		{
			name: "推送结果不明",
			prepare: func(t *testing.T, record *domain.CodeSaveRecord) {
				completedCommit(t, record)
				if err := record.MarkPush(
					domain.PushStatusUnknown,
					"",
					time.Now().UTC(),
				); err != nil {
					t.Fatalf("记录推送结果不明失败: %v", err)
				}
			},
		},
		{
			name: "远程历史冲突",
			prepare: func(t *testing.T, record *domain.CodeSaveRecord) {
				completedCommit(t, record)
				if err := record.MarkPush(
					domain.PushStatusConflict,
					"",
					time.Now().UTC(),
				); err != nil {
					t.Fatalf("记录推送冲突失败: %v", err)
				}
			},
		},
		{
			name:         "合并结果不明",
			mergeEnabled: true,
			prepare: func(t *testing.T, record *domain.CodeSaveRecord) {
				completedCommit(t, record)
				if err := record.MarkPush(
					domain.PushStatusSucceeded,
					codeSaveCommitSHA,
					time.Now().UTC(),
				); err != nil {
					t.Fatalf("记录推送成功失败: %v", err)
				}
			},
		},
		{
			name: "已核验确认操作未发生",
			prepare: func(t *testing.T, record *domain.CodeSaveRecord) {
				if err := record.MarkRecovery(
					domain.RecoveryStatusConfirmedNotApplied,
					time.Now().UTC(),
				); err != nil {
					t.Fatalf("记录核验结论失败: %v", err)
				}
			},
			wantDeleted: true,
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newDeleteFixture(t)
			confirmed := fixture.confirm(t, 1)
			task := fixture.moveTask(t, confirmed.Tasks[0].ID, domain.TaskStatusFailed)

			if !testCase.withoutRecord {
				record := testCodeSaveRecord(t, task, testCase.mergeEnabled)
				if testCase.prepare != nil {
					testCase.prepare(t, &record)
				}
				if err := fixture.base.db.CreateCodeSaveRecord(fixture.base.ctx, record); err != nil {
					t.Fatalf("写入代码保存记录失败: %v", err)
				}
			}

			_, err := fixture.service.Delete(fixture.base.ctx, confirmed.Requirement.ID.String())
			if !testCase.wantDeleted {
				if !errors.Is(err, app.ErrRequirementStateConflict) {
					t.Fatalf("结果不明时应拒绝删除，实际: %v", err)
				}
				fixture.requireNothingDeleted(t, confirmed)
				return
			}
			if err != nil {
				t.Fatalf("该形态应允许删除，实际: %v", err)
			}
			if fixture.requirement(t, confirmed.Requirement.ID).DeletedAt == nil {
				t.Fatal("删除成功后需求必须写入删除时间")
			}
			for _, created := range confirmed.Tasks {
				if fixture.task(t, created.ID).DeletedAt == nil {
					t.Fatalf("删除成功后任务 %s 必须一并软删除", created.ID)
				}
			}
		})
	}
}

// TestDeleteKeepsCodeSaveRecordOnRejection 校验被拒绝时不得改写代码保存记录。
func TestDeleteKeepsCodeSaveRecordOnRejection(t *testing.T) {
	t.Parallel()
	fixture := newDeleteFixture(t)
	confirmed := fixture.confirm(t, 1)
	task := fixture.moveTask(t, confirmed.Tasks[0].ID, domain.TaskStatusFailed)
	record := testCodeSaveRecord(t, task, false)
	if err := fixture.base.db.CreateCodeSaveRecord(fixture.base.ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}

	if _, err := fixture.service.Delete(
		fixture.base.ctx,
		confirmed.Requirement.ID.String(),
	); !errors.Is(err, app.ErrRequirementStateConflict) {
		t.Fatalf("结果不明时应拒绝删除，实际: %v", err)
	}

	persisted, err := fixture.base.db.GetCodeSaveRecord(fixture.base.ctx, record.ID)
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	if persisted.CommitStatus != domain.CommitStatusPending {
		t.Fatalf("删除被拒绝时不得改写提交状态: %s", persisted.CommitStatus)
	}
	if persisted.RecoveryStatus.Normalized() != domain.RecoveryStatusUnverified {
		t.Fatalf("删除被拒绝时不得改写核验结论: %s", persisted.RecoveryStatus)
	}
}
