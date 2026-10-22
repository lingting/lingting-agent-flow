package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/domain"
)

// CodeSaveVerifyRequest 是一次只读恢复核验的输入。
//
// 只允许给出记录标识与可选的容器/工作区覆盖：记录本身从数据库读取，调用方不能
// 指定任意仓库状态作为"已核验事实"。
type CodeSaveVerifyRequest struct {
	// RecordID 是待核验的代码保存记录标识。
	RecordID string
	// ContainerID 是可选覆盖的容器登记记录，空值使用记录中的容器。
	ContainerID string
	// WorkDir 是容器内 Git 工作区路径，空值使用默认项目目录。
	WorkDir string
}

// CodeSaveVerifyResult 是一次只读恢复核验的结果。
type CodeSaveVerifyResult struct {
	// Record 是核验之前从数据库读到的记录。
	Record domain.CodeSaveRecord
	// Observation 是只读核验得到的现场事实。
	Observation domain.CodeSaveObservation
	// Decision 是依据记录与现场得出的恢复结论。
	Decision domain.CodeSaveRecoveryDecision
	// Updated 是写入核验结论之后的记录；写入失败时仍是原始记录。
	Updated domain.CodeSaveRecord
	// Persisted 表示核验结论是否已写回数据库。
	Persisted bool
}

// VerifyCodeSave 只读核验一次代码保存记录对应的真实仓库状态。
//
// 它不提交、不推送、不合并、不删除容器：核验的职责只是把"记录里是 pending"与
// "Git 到底做了什么"分开，为人工决策提供依据。核验结论会写回记录的
// recovery_status，除此之外不改动任何状态字段——尤其不会把 pending 改成 succeeded。
//
// 核验失败（容器不可访问、远程不可达）不会返回错误，而是返回 unverifiable 的
// 结论：无法确认时必须保留不确定性，不能伪造成"操作没有发生"。
func (s *ContainerService) VerifyCodeSave(
	ctx context.Context,
	request CodeSaveVerifyRequest,
) (CodeSaveVerifyResult, error) {
	recordID := domain.ID(strings.TrimSpace(request.RecordID))
	if err := recordID.Validate(); err != nil {
		return CodeSaveVerifyResult{}, fmt.Errorf("核验代码保存记录失败: %w", err)
	}
	record, err := s.store.GetCodeSaveRecord(ctx, recordID)
	if err != nil {
		return CodeSaveVerifyResult{}, fmt.Errorf("核验代码保存记录失败: %w", err)
	}
	result := CodeSaveVerifyResult{Record: *record, Updated: *record}
	containerID := strings.TrimSpace(request.ContainerID)
	if containerID == "" {
		containerID = record.ContainerID
	}
	container, err := s.loadContainer(ctx, containerID)
	if err != nil {
		return result, err
	}
	if err := ensureManaged(container); err != nil {
		return result, fmt.Errorf("核验代码保存记录失败: %w", err)
	}
	verifyErr := s.withRemoteDocker(
		ctx,
		container.HostID.String(),
		false,
		func(ctx context.Context, session *remoteDocker) error {
			unlock := s.locks.Lock(container.ID.String())
			defer unlock()
			saver, saverErr := codesaver.New(codesaver.Options{
				Executor: &containerSourceExecutor{session: session},
				Guard:    &sessionContainerGuard{service: s, session: session},
				Recorder: codeSaveRecorder{store: s.store},
			})
			if saverErr != nil {
				return fmt.Errorf("核验代码保存记录失败: %w", saverErr)
			}
			observation := saver.Observe(ctx, codesaver.Request{
				MachineID:   container.HostID.String(),
				ContainerID: container.ID.String(),
				DockerID:    container.DockerID,
				ProjectID:   record.ProjectID,
				TaskID:      record.TaskID,
				WorkDir:     request.WorkDir,
				BaseBranch:  record.BaseBranch,
				RemoteName:  record.RemoteName,
			}, *record)
			result.Observation = observation
			result.Decision = domain.DecideCodeSaveRecovery(*record, observation)
			return nil
		},
	)
	if verifyErr != nil {
		return result, verifyErr
	}
	if err := result.Updated.MarkRecovery(result.Decision.Status, time.Now()); err != nil {
		// 结论无法写入状态机时仍然返回判定结果：现场事实比记录更重要。
		return result, nil
	}
	if err := s.store.UpdateCodeSaveRecord(ctx, result.Updated); err != nil {
		result.Updated = *record
		return result, nil
	}
	result.Persisted = true
	return result, nil
}
