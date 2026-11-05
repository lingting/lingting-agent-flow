package scheduler_test

import (
	"testing"

	"github.com/lingting/agentflow/internal/domain"
)

// 本文件提供调度器测试共用的现场构造辅助：状态迁移、步骤写入与容器关联。
//
// 这些辅助只写数据库，不接触执行权登记：调度器与启动流程才是被测代码。

// moveTaskTo 把任务推进到指定状态。
func (f *schedulerFixture) moveTaskTo(t *testing.T, taskID domain.ID, status domain.TaskStatus) {
	t.Helper()
	task, err := f.db.GetTask(f.ctx, taskID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	for _, next := range statusPath(task.Status, status) {
		if err := task.Transition(next, f.now); err != nil {
			t.Fatalf("任务迁移到 %s 失败: %v", next, err)
		}
	}
	if err := f.db.UpdateTask(f.ctx, *task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
}

// statusPath 返回从当前状态到目标状态的合法迁移路径。
func statusPath(from, to domain.TaskStatus) []domain.TaskStatus {
	if from == to {
		return nil
	}
	switch to {
	case domain.TaskStatusPlanned:
		return []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusPlanning,
			domain.TaskStatusPlanned,
		}
	case domain.TaskStatusDeveloping:
		return []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusPlanning,
			domain.TaskStatusPlanned,
			domain.TaskStatusDeveloping,
		}
	default:
		return []domain.TaskStatus{to}
	}
}

// writeStep 写入一个待执行的开发步骤。
func (f *schedulerFixture) writeStep(
	t *testing.T,
	taskID domain.ID,
	sequence int,
) domain.Step {
	t.Helper()
	role, err := domain.NewRole(
		"开发工程师",
		domain.RoleTypeDeveloper,
		"provider/model",
		"角色提示词",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		f.now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := f.db.CreateRole(f.ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	step, err := domain.NewAgentStep(
		taskID,
		sequence,
		domain.StepTypeDevelop,
		"实现接口",
		"按需求实现",
		"",
		role,
		domain.VerificationSpec{},
		domain.RetryConfig{MaxAttempts: 1},
		f.now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := f.db.CreateStep(f.ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	return step
}

// attachContainer 写入一条属于该任务的容器记录并把引用写回任务。
func (f *schedulerFixture) attachContainer(
	t *testing.T,
	taskID domain.ID,
	label string,
) string {
	t.Helper()
	task, err := f.db.GetTask(f.ctx, taskID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	container, err := domain.NewContainer(
		"",
		*task.HostID,
		"agentflow-"+label,
		domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		domain.FormatVersionLabel(1),
		isolationBuildHash,
		isolationImageID,
		f.now,
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	container.TaskID = &taskID
	container.DockerID = "docker-" + container.ID.String()
	container.State = domain.ContainerStateRunning
	container.DockerState = domain.DockerStateRunning
	if err := f.db.CreateContainer(f.ctx, container); err != nil {
		t.Fatalf("写入容器记录失败: %v", err)
	}
	updated := *task
	if err := updated.SetContainer(container.ID.String()); err != nil {
		t.Fatalf("设置任务容器失败: %v", err)
	}
	if err := f.db.UpdateTaskIfStatus(f.ctx, updated, []domain.TaskStatus{task.Status}); err != nil {
		t.Fatalf("更新任务容器失败: %v", err)
	}
	return container.ID.String()
}
