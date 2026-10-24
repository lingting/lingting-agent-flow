package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
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
	// Updated 是写入核验结论之后的记录；写入失败或冲突时是数据库中的真实记录。
	Updated domain.CodeSaveRecord
	// Persisted 表示核验结论是否已写回数据库。
	Persisted bool
}

// VerifyCodeSave 只读核验一次代码保存记录对应的真实仓库状态。
//
// 它不提交、不推送、不合并、不删除容器：核验的职责只是把"记录里是 pending"与
// "Git 到底做了什么"分开，为人工决策提供依据。核验确认某个 Git 操作已经发生时，
// 结论会连同该阶段的状态与提交 SHA 一起写回记录；确认未发生或无法确认时只写
// recovery_status，绝不把 pending 伪造成 succeeded。
//
// 写回以读取时的恢复结论为条件：核验期间记录若被其他执行推进，写入按冲突处理，
// 不会用旧值覆盖新事实，调用方必须依据重新读取到的真实记录决定下一步。
//
// 错误分类（调用方据此判断恢复资格，不需要解析错误文本）：
//   - 参数错误、记录不存在、容器登记记录不存在：返回错误，核验没有开始；
//   - 容器已删除、已脱离管理、执行节点不可达、Docker 不可访问、Git 查询失败：
//     不返回错误，而是给出 unverifiable 结论并落盘——无法确认时必须保留不确定性，
//     不能伪造成"操作没有发生"，也不能让记录永远停在 pending 使恢复无出路；
//   - 结论写回失败或冲突：不返回错误，但 Persisted 为 false，Updated 是数据库中的
//     真实记录，调用方不得把这次核验报告为恢复成功。
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
		return s.degradeCodeSaveUnverifiable(ctx, result, *record, err)
	}
	if err := ensureManaged(container); err != nil {
		// 容器已删除、已脱离管理或状态不可用时，核验无法进行，但结论必须落盘：
		// 不落盘会让记录永远停在 pending，恢复只能反复要求核验同一现场，
		// 用户也就永远无法判断"这条副作用到底做了没有"。
		return s.degradeCodeSaveUnverifiable(ctx, result, *record, err)
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
		// 现场无法核验（节点不可达、Docker 不可访问、Git 查询失败）不得伪装成
		// "操作没有发生"：这里把它降级为 unverifiable 结论，同时保留原始错误供调用方
		// 记录诊断，但结论本身已经落盘，因此仍然可以走正常的核验写回路径。
		result.Observation = domain.CodeSaveObservation{
			ReadFailed: true,
			ReadError:  errorSummary(verifyErr),
		}
		result.Decision = domain.DecideCodeSaveRecovery(*record, result.Observation)
		s.debug(
			ctx,
			"代码保存核验无法取得可信现场",
			slog.String("record", record.ID.String()),
			slog.String("error", errorSummary(verifyErr)),
		)
	}
	return s.persistCodeSaveRecovery(ctx, result, *record)
}

// degradeCodeSaveUnverifiable 把"无法开始核验"的现场降级为 unverifiable 结论并落盘。
//
// 它覆盖"目标记录或容器登记记录不存在"之外的全部前置失败：容器已删除、已脱离管理、
// 容器登记记录不可读。这些情况下本次核验没有取得任何可信事实，因此结论只能是
// unverifiable，而不是"操作没有发生"。
//
// 记录或容器登记记录本身不存在时仍返回错误：那是参数错误，不是现场不可核验。
func (s *ContainerService) degradeCodeSaveUnverifiable(
	ctx context.Context,
	result CodeSaveVerifyResult,
	record domain.CodeSaveRecord,
	cause error,
) (CodeSaveVerifyResult, error) {
	if errors.Is(cause, storage.ErrNotFound) {
		return result, fmt.Errorf("核验代码保存记录失败: %w", cause)
	}
	result.Observation = domain.CodeSaveObservation{
		ReadFailed: true,
		ReadError:  errorSummary(cause),
	}
	result.Decision = domain.DecideCodeSaveRecovery(record, result.Observation)
	s.debug(
		ctx,
		"代码保存核验无法取得可信现场，结论降级为 unverifiable",
		slog.String("record", record.ID.String()),
		slog.String("error", errorSummary(cause)),
	)
	return s.persistCodeSaveRecovery(ctx, result, record)
}

// persistCodeSaveRecovery 把核验结论写回记录。
//
// 写入是"只读核验的最后一公里"，必须同时满足四点：
//  1. 状态迁移先经领域状态机校验，已确认的事实不会因为一次查询失败被改写；
//  2. 已确认发生的阶段事实（阶段状态 + 提交 SHA）必须一起落盘：否则"已确认发生"
//     只存在于返回值里，数据库仍停在 pending，恢复会反复要求核验同一现场；
//  3. 不用整条记录覆盖读取之后可能已被推进的提交/推送/合并状态，也不改写与核验
//     无关的字段（分支、容器、仓库地址、基线）；
//  4. 以读取时的恢复结论为条件写入，冲突时重新读取真实记录，绝不覆盖新事实。
//
// 写入失败不会被伪造成成功：Persisted 保持 false，Updated 回落到数据库中的真实值。
func (s *ContainerService) persistCodeSaveRecovery(
	ctx context.Context,
	result CodeSaveVerifyResult,
	record domain.CodeSaveRecord,
) (CodeSaveVerifyResult, error) {
	expected := record.RecoveryStatus
	candidate, err := record.ApplyRecovery(result.Decision, time.Now())
	if err != nil {
		// 结论无法通过状态机时仍然返回判定结果：现场事实比记录更重要，
		// 但不得据此认为恢复已经闭环。
		return result, nil
	}
	writeErr := s.store.UpdateCodeSaveRecoveryResult(ctx, candidate, expected)
	if writeErr != nil {
		result.Updated = *s.reloadCodeSaveRecord(ctx, record)
		return result, nil
	}
	result.Updated = candidate
	result.Persisted = true
	return result, nil
}

// reloadCodeSaveRecord 在写入冲突时重新读取记录。
//
// 读取失败时退回调用方手里的旧记录：返回旧值虽然过时，但不会被误认为最新事实，
// 因为 Persisted 仍然是 false。
func (s *ContainerService) reloadCodeSaveRecord(
	ctx context.Context,
	fallback domain.CodeSaveRecord,
) *domain.CodeSaveRecord {
	current, err := s.store.GetCodeSaveRecord(ctx, fallback.ID)
	if err != nil || current == nil {
		return &fallback
	}
	return current
}
