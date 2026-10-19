package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// CodeSaveRequest 是一次代码保存的输入。
type CodeSaveRequest struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// ProjectID 是可选的项目标识。
	ProjectID string
	// TaskID 是任务标识，任务分支固定为 agent/<task-id>。
	TaskID string
	// AttemptID 是发起本次保存的执行尝试标识，可为空。
	AttemptID string
	// WorkDir 是容器内 Git 工作区路径，空值使用默认项目目录。
	WorkDir string
	// BaseBranch 是显式指定的基础分支，空值表示自动识别。
	BaseBranch string
	// MergeAfterPush 表示在推送成功后尝试合并基础分支。
	MergeAfterPush bool
	// CommitMessage 是提交信息，空值使用代码构造的默认信息。
	CommitMessage string
	// Author 是可选提交身份，空值表示使用仓库已有的有效身份。
	Author domain.GitAuthor
	// RemoteName 是 Git 远程名称，空值使用 origin。
	RemoteName string
	// Operation 是本次要执行的操作组合，零值等价于 save。
	Operation codesaver.Operation
}

// CodeSaveResult 是一次代码保存的结果。
type CodeSaveResult struct {
	// Result 是代码保存组件返回的结果。
	Result codesaver.Result
	// Container 是本次操作使用的容器登记记录。
	Container domain.Container
}

// SaveCode 在受管理的任务容器内部执行一次代码保存。
//
// 代码始终留在容器内部：提交、推送与合并都通过容器内 Git 执行，不把代码复制到
// 宿主机或本机数据目录。容器只被核验与使用，不会被启动、停止或删除。
func (s *ContainerService) SaveCode(
	ctx context.Context,
	request CodeSaveRequest,
) (CodeSaveResult, error) {
	if strings.TrimSpace(request.TaskID) == "" {
		return CodeSaveResult{}, fmt.Errorf("代码保存失败: 任务标识不能为空")
	}
	container, err := s.loadContainer(ctx, request.ContainerID)
	if err != nil {
		return CodeSaveResult{}, err
	}
	if err := ensureManaged(container); err != nil {
		return CodeSaveResult{}, fmt.Errorf("代码保存失败: %w", err)
	}
	var outcome CodeSaveResult
	err = s.withRemoteDocker(
		ctx,
		container.HostID.String(),
		false,
		func(ctx context.Context, session *remoteDocker) error {
			unlock := s.locks.Lock(container.ID.String())
			defer unlock()
			current, loadErr := s.loadContainer(ctx, request.ContainerID)
			if loadErr != nil {
				return loadErr
			}
			if err := ensureManaged(current); err != nil {
				return fmt.Errorf("代码保存失败: %w", err)
			}
			if _, observeErr := s.observe(ctx, session, current); observeErr != nil {
				return fmt.Errorf("代码保存前核验容器失败: %w", observeErr)
			}
			saver, saverErr := codesaver.New(codesaver.Options{
				Executor: &containerSourceExecutor{session: session},
				Guard:    &sessionContainerGuard{service: s, session: session},
				Recorder: codeSaveRecorder{store: s.store},
			})
			if saverErr != nil {
				return fmt.Errorf("代码保存失败: %w", saverErr)
			}
			saveRequest := codesaver.Request{
				MachineID:      current.HostID.String(),
				ContainerID:    current.ID.String(),
				DockerID:       current.DockerID,
				ProjectID:      strings.TrimSpace(request.ProjectID),
				TaskID:         strings.TrimSpace(request.TaskID),
				AttemptID:      strings.TrimSpace(request.AttemptID),
				WorkDir:        request.WorkDir,
				BaseBranch:     request.BaseBranch,
				MergeAfterPush: request.MergeAfterPush,
				CommitMessage:  request.CommitMessage,
				Author:         request.Author,
				RemoteName:     request.RemoteName,
				Operation:      request.Operation,
			}
			result, saveErr := runCodeSave(ctx, saver, saveRequest)
			outcome = CodeSaveResult{Result: result, Container: current}
			return saveErr
		},
	)
	if err != nil {
		return outcome, err
	}
	s.debug(
		ctx,
		"代码保存已完成",
		slog.String("container", container.ID.String()),
		slog.String("task", request.TaskID),
		slog.String("task_branch", outcome.Result.Record.TaskBranch),
		slog.String("commit", outcome.Result.Record.CommitSHA),
		slog.String("push_status", outcome.Result.Record.PushStatus.String()),
		slog.String("merge_status", outcome.Result.Record.MergeStatus.String()),
	)
	return outcome, nil
}

// runCodeSave 按操作组合选择代码保存入口。
func runCodeSave(
	ctx context.Context,
	saver *codesaver.Saver,
	request codesaver.Request,
) (codesaver.Result, error) {
	switch request.Operation {
	case codesaver.OperationPush:
		return saver.Push(ctx, request)
	case codesaver.OperationMerge:
		return saver.Merge(ctx, request)
	default:
		return saver.Run(ctx, request)
	}
}

// CodeSaveStatus 返回指定任务的代码保存记录。
func (s *ContainerService) CodeSaveStatus(
	ctx context.Context,
	taskID string,
) ([]domain.CodeSaveRecord, error) {
	trimmed := strings.TrimSpace(taskID)
	if trimmed == "" {
		return nil, fmt.Errorf("读取代码保存记录失败: 任务标识不能为空")
	}
	records, err := s.store.ListCodeSaveRecordsByTask(ctx, trimmed)
	if err != nil {
		return nil, fmt.Errorf("读取代码保存记录失败: %w", err)
	}
	return records, nil
}

// codeSaveRecorder 把存储层适配为代码保存的记录仓储。
type codeSaveRecorder struct {
	store storage.Store
}

// Create 写入一条代码保存记录。
func (r codeSaveRecorder) Create(ctx context.Context, record domain.CodeSaveRecord) error {
	return r.store.CreateCodeSaveRecord(ctx, record)
}

// Update 更新代码保存记录。
func (r codeSaveRecorder) Update(ctx context.Context, record domain.CodeSaveRecord) error {
	return r.store.UpdateCodeSaveRecord(ctx, record)
}

// Get 读取代码保存记录。
func (r codeSaveRecorder) Get(
	ctx context.Context,
	id domain.ID,
) (*domain.CodeSaveRecord, error) {
	return r.store.GetCodeSaveRecord(ctx, id)
}

// ListByTask 按任务列出代码保存记录。
func (r codeSaveRecorder) ListByTask(
	ctx context.Context,
	taskID string,
) ([]domain.CodeSaveRecord, error) {
	return r.store.ListCodeSaveRecordsByTask(ctx, taskID)
}

// 编译期确认适配器满足代码保存侧接口。
var _ codesaver.Recorder = codeSaveRecorder{}
