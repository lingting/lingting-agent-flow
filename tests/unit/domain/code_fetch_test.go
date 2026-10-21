// 代码初始化领域模型的单元测试。
//
// 这些用例只验证状态机、字段校验与状态迁移规则，不依赖数据库、Git、SSH 或 Docker。
package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// 测试使用的固定 commit SHA。
const (
	testBaseSHA = "1111111111111111111111111111111111111111"
	testHeadSHA = "2222222222222222222222222222222222222222"
)

// newCodeFetch 构造一条待执行的代码初始化记录。
func newCodeFetch(t *testing.T) domain.CodeFetchRecord {
	t.Helper()
	record, err := domain.NewCodeFetchRecord(
		"",
		"demo",
		"task-1",
		"step-1",
		"attempt-1",
		domain.CodeSourceGit,
		domain.DefaultContainerWorkDir,
		"container-1",
		"docker-container-1",
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造代码初始化记录失败: %v", err)
	}
	return record
}

// TestNewCodeFetchRecordDefaults 校验新建记录的默认取值。
func TestNewCodeFetchRecordDefaults(t *testing.T) {
	t.Parallel()
	record := newCodeFetch(t)
	if record.Status != domain.CodeFetchStatusPending {
		t.Fatalf("初始状态应为 pending，实际 %s", record.Status)
	}
	if record.TaskBranch != "agent/task-1" {
		t.Fatalf("任务分支必须由 TaskBranchName 生成，实际 %s", record.TaskBranch)
	}
	if record.SourceType != domain.CodeSourceGit {
		t.Fatalf("第一版获取方式必须是 git，实际 %s", record.SourceType)
	}
	if record.Succeeded() {
		t.Fatal("pending 记录不得报告代码已就绪")
	}
	if record.FinishedAt != nil {
		t.Fatal("未结束的记录不应有结束时间")
	}
}

// TestCodeFetchRejectsInvalidTaskID 校验非法任务标识无法生成任务分支。
func TestCodeFetchRejectsInvalidTaskID(t *testing.T) {
	t.Parallel()
	cases := []string{"", "  ", "Task-1", "-task", "task/1", "task 1"}
	for _, taskID := range cases {
		_, err := domain.NewCodeFetchRecord(
			"",
			"",
			taskID,
			"step-1",
			"attempt-1",
			domain.CodeSourceGit,
			domain.DefaultContainerWorkDir,
			"container-1",
			"",
			time.Now().UTC(),
		)
		if err == nil {
			t.Fatalf("任务标识 %q 应当被拒绝", taskID)
		}
		if !errors.Is(err, domain.ErrInvalidValue) {
			t.Fatalf("任务标识 %q 的错误类别不符: %v", taskID, err)
		}
	}
}

// TestCodeFetchRejectsInvalidWorkDir 校验目标目录必须位于容器工作区之下。
func TestCodeFetchRejectsInvalidWorkDir(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"workspace/project",
		"/root/project",
		"/workspace",
		"/workspace/../etc",
		"~/.lingting/agentflow",
	}
	for _, workDir := range cases {
		_, err := domain.NewCodeFetchRecord(
			"",
			"",
			"task-1",
			"step-1",
			"attempt-1",
			domain.CodeSourceGit,
			workDir,
			"container-1",
			"",
			time.Now().UTC(),
		)
		if err == nil {
			t.Fatalf("目标目录 %q 应当被拒绝", workDir)
		}
	}
}

// TestCodeFetchRejectsUnknownSourceType 校验未知获取方式被拒绝。
func TestCodeFetchRejectsUnknownSourceType(t *testing.T) {
	t.Parallel()
	_, err := domain.NewCodeFetchRecord(
		"",
		"",
		"task-1",
		"step-1",
		"attempt-1",
		domain.CodeSourceType("rsync"),
		domain.DefaultContainerWorkDir,
		"container-1",
		"",
		time.Now().UTC(),
	)
	if err == nil {
		t.Fatal("未知获取方式必须被拒绝")
	}
}

// TestCodeFetchSucceededRequiresCommit 校验成功记录必须带可核验 commit。
func TestCodeFetchSucceededRequiresCommit(t *testing.T) {
	t.Parallel()
	record := newCodeFetch(t)
	if err := record.MarkRunning(domain.CodeFetchStageProbe, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行状态失败: %v", err)
	}
	if err := record.MarkSucceeded(
		"main",
		domain.GitBranchSelectionOriginHead,
		testBaseSHA,
		domain.TaskBranchSourceNew.String(),
		"",
		time.Now().UTC(),
	); err == nil {
		t.Fatal("缺少 commit 的成功记录必须被拒绝")
	}
}

// TestCodeFetchSucceededRequiresBranchSource 校验成功记录必须给出分支来源。
func TestCodeFetchSucceededRequiresBranchSource(t *testing.T) {
	t.Parallel()
	record := newCodeFetch(t)
	if err := record.MarkRunning(domain.CodeFetchStageProbe, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行状态失败: %v", err)
	}
	if err := record.MarkSucceeded(
		"main",
		domain.GitBranchSelectionOriginHead,
		testBaseSHA,
		"",
		testHeadSHA,
		time.Now().UTC(),
	); err == nil {
		t.Fatal("缺少分支来源的成功记录必须被拒绝")
	}
}

// TestCodeFetchTerminalCannotBeReopened 校验终态记录不会被迟到的结果改写。
func TestCodeFetchTerminalCannotBeReopened(t *testing.T) {
	t.Parallel()
	record := newCodeFetch(t)
	if err := record.MarkRunning(domain.CodeFetchStageClone, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行状态失败: %v", err)
	}
	if err := record.MarkFailed(
		domain.CodeFetchStatusFailed,
		domain.CodeFetchStageClone,
		"repository_access_failed",
		"无法访问仓库",
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("标记失败状态失败: %v", err)
	}
	if err := record.MarkSucceeded(
		"main",
		domain.GitBranchSelectionOriginHead,
		testBaseSHA,
		domain.TaskBranchSourceNew.String(),
		testHeadSHA,
		time.Now().UTC(),
	); err == nil {
		t.Fatal("已失败的记录不得被改写成成功")
	}
	if !errors.Is(record.MarkRunning(domain.CodeFetchStageProbe, time.Now().UTC()), domain.ErrInvalidTransition) {
		t.Fatal("终态记录不得重新进入运行状态")
	}
}

// TestCodeFetchSkippedCountsAsReady 校验 skipped 也算代码已就绪。
func TestCodeFetchSkippedCountsAsReady(t *testing.T) {
	t.Parallel()
	record := newCodeFetch(t)
	if err := record.MarkRunning(domain.CodeFetchStageProbe, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行状态失败: %v", err)
	}
	if err := record.MarkSkipped(testHeadSHA, time.Now().UTC()); err != nil {
		t.Fatalf("标记跳过状态失败: %v", err)
	}
	if !record.Succeeded() {
		t.Fatal("skipped 必须报告代码已就绪")
	}
	if record.CommitSHA != testHeadSHA {
		t.Fatalf("commit 不符: %s", record.CommitSHA)
	}
}

// TestCodeFetchFailedRequiresSummary 校验失败记录必须给出原因。
func TestCodeFetchFailedRequiresSummary(t *testing.T) {
	t.Parallel()
	record := newCodeFetch(t)
	if err := record.MarkRunning(domain.CodeFetchStageClone, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行状态失败: %v", err)
	}
	if err := record.MarkFailed(
		domain.CodeFetchStatusFailed,
		domain.CodeFetchStageClone,
		"",
		"",
		time.Now().UTC(),
	); err == nil {
		t.Fatal("没有失败原因的失败记录必须被拒绝")
	}
}

// TestCodeFetchFailureSummaryTruncated 校验失败摘要被裁剪为单行。
func TestCodeFetchFailureSummaryTruncated(t *testing.T) {
	t.Parallel()
	record := newCodeFetch(t)
	if err := record.MarkRunning(domain.CodeFetchStageClone, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行状态失败: %v", err)
	}
	summary := "第一行原因\n第二行细节\n" + strings.Repeat("x", 400)
	if err := record.MarkFailed(
		domain.CodeFetchStatusFailed,
		domain.CodeFetchStageClone,
		"repository_access_failed",
		summary,
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("标记失败状态失败: %v", err)
	}
	if strings.Contains(record.ErrorSummary, "\n") {
		t.Fatal("失败摘要必须折叠为单行")
	}
	if len(record.ErrorSummary) > 210 {
		t.Fatalf("失败摘要未裁剪: %d", len(record.ErrorSummary))
	}
}

// TestCodeFetchRejectsBaseBranchEqualToTaskBranch 校验基础分支不能与任务分支相同。
func TestCodeFetchRejectsBaseBranchEqualToTaskBranch(t *testing.T) {
	t.Parallel()
	record := newCodeFetch(t)
	record.BaseBranch = record.TaskBranch
	if err := record.Validate(); err == nil {
		t.Fatal("基础分支与任务分支相同必须被拒绝")
	}
}

// TestTaskBranchSourceValidation 校验分支来源取值。
func TestTaskBranchSourceValidation(t *testing.T) {
	t.Parallel()
	valid := []domain.TaskBranchSource{
		domain.TaskBranchSourceNew,
		domain.TaskBranchSourceRemote,
		domain.TaskBranchSourceLocal,
	}
	for _, source := range valid {
		if err := source.Validate(); err != nil {
			t.Fatalf("分支来源 %s 应当合法: %v", source, err)
		}
	}
	if err := domain.TaskBranchSource("cherry-pick").Validate(); err == nil {
		t.Fatal("未知分支来源必须被拒绝")
	}
}

// TestCodeFetchStatusTerminal 校验状态终态判定。
func TestCodeFetchStatusTerminal(t *testing.T) {
	t.Parallel()
	terminal := []domain.CodeFetchStatus{
		domain.CodeFetchStatusSucceeded,
		domain.CodeFetchStatusSkipped,
		domain.CodeFetchStatusFailed,
		domain.CodeFetchStatusCancelled,
		domain.CodeFetchStatusTimedOut,
	}
	for _, status := range terminal {
		if !status.IsTerminal() {
			t.Fatalf("状态 %s 应当是终态", status)
		}
	}
	for _, status := range []domain.CodeFetchStatus{
		domain.CodeFetchStatusPending,
		domain.CodeFetchStatusRunning,
		domain.CodeFetchStatusUncertain,
	} {
		if status.IsTerminal() {
			t.Fatalf("状态 %s 不应当是终态", status)
		}
	}
}
