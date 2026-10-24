package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// CodeFetchInspectionProbe 是只读核验容器内工作区所需的命令执行能力。
//
// 它只允许只读查询（目录是否存在、是否为 Git 仓库、当前分支与 HEAD）：
// 核验不得 Clone、checkout、reset 或删除任何内容。
type CodeFetchInspectionProbe interface {
	Exec(ctx context.Context, request ExecRequest) (ExecResult, error)
	// InspectContainer 核验容器当前状态。
	InspectContainer(ctx context.Context, request InspectContainerRequest) (ContainerDetail, error)
	// GetContainerRecord 读取容器登记记录。
	GetContainerRecord(ctx context.Context, containerID string) (domain.Container, error)
}

// CodeFetchVerifyRequest 是一次只读核验的输入。
type CodeFetchVerifyRequest struct {
	// RecordID 是待核验的代码获取记录标识。
	RecordID string
}

// CodeFetchVerifyResult 是一次只读核验的结果。
type CodeFetchVerifyResult struct {
	// Record 是核验之前读到的记录。
	Record domain.CodeFetchRecord
	// Observation 是只读核验得到的现场事实。
	Observation domain.CodeFetchVerification
	// Decision 是依据事实得出的收敛结论。
	Decision domain.CodeFetchVerificationDecision
	// Updated 是写入后的记录；未收敛时等于核验前的记录。
	Updated domain.CodeFetchRecord
	// Persisted 表示结论是否已写回数据库。
	Persisted bool
}

// CodeFetchVerifyService 是尝试级代码获取的只读核验入口。
//
// 它与任务级工作区初始化的 `uncertain` 现场对应：两者都需要"先看真实状态、再收敛"，
// 但这里只处理 Attempt 级记录，且**绝不重放**代码获取。
type CodeFetchVerifyService struct {
	store  codeFetchVerifyStore
	probe  CodeFetchInspectionProbe
	logger *slog.Logger
}

// codeFetchVerifyStore 是只读核验与收敛所需的存储能力。
type codeFetchVerifyStore interface {
	GetCodeFetchRecord(ctx context.Context, id domain.ID) (*domain.CodeFetchRecord, error)
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	UpdateCodeFetchRecordIfStatus(
		ctx context.Context,
		record domain.CodeFetchRecord,
		expected []domain.CodeFetchStatus,
	) error
}

// NewCodeFetchVerifyService 构造只读核验服务。
func NewCodeFetchVerifyService(
	store codeFetchVerifyStore,
	probe CodeFetchInspectionProbe,
	logger *slog.Logger,
) (*CodeFetchVerifyService, error) {
	if store == nil {
		return nil, fmt.Errorf("代码获取核验服务初始化失败: 存储不能为空")
	}
	if probe == nil {
		return nil, fmt.Errorf("代码获取核验服务初始化失败: 容器能力不能为空")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &CodeFetchVerifyService{store: store, probe: probe, logger: logger}, nil
}

// Verify 只读核验一条代码获取记录对应的容器内工作区。
//
// 语义固定为：
//   - 只执行只读查询，不 Clone、不 checkout、不 reset、不删除目录；
//   - 事实充分时把记录从 uncertain 收敛为 succeeded / failed；
//   - 事实不充分时保持 uncertain，并把观测原因写回记录，供人工判断；
//   - 写入带状态前置条件，并发推进时不覆盖新结论。
func (s *CodeFetchVerifyService) Verify(
	ctx context.Context,
	request CodeFetchVerifyRequest,
) (CodeFetchVerifyResult, error) {
	recordID := domain.ID(strings.TrimSpace(request.RecordID))
	if err := recordID.Validate(); err != nil {
		return CodeFetchVerifyResult{}, fmt.Errorf("核验代码获取记录失败: %w", err)
	}
	record, err := s.store.GetCodeFetchRecord(ctx, recordID)
	if err != nil {
		return CodeFetchVerifyResult{}, fmt.Errorf("核验代码获取记录失败: %w", err)
	}
	result := CodeFetchVerifyResult{Record: *record, Updated: *record}
	if record.Status != domain.CodeFetchStatusUncertain {
		// 只有不确定的记录需要核验：其余状态已经是真实执行结果，核验不得改写。
		result.Decision = domain.CodeFetchVerificationDecision{
			Summary: "记录状态为 " + record.Status.String() + "，不需要核验",
		}
		return result, nil
	}
	observation, probeErr := s.inspect(ctx, *record)
	result.Observation = observation
	result.Decision = domain.DecideCodeFetchVerification(*record, observation)
	if probeErr != nil {
		s.logger.WarnContext(
			ctx,
			"代码获取核验无法取得可信现场",
			slog.String("record", record.ID.String()),
			slog.String("error", errorSummary(probeErr)),
		)
	}
	persisted, persistErr := s.persist(ctx, *record, result)
	result.Updated = persisted
	result.Persisted = persistErr == nil
	if persistErr != nil && !errors.Is(persistErr, storage.ErrConflict) {
		return result, fmt.Errorf("核验结论未能写回记录: %w", persistErr)
	}
	return result, nil
}

// inspect 只读地观测容器内工作区事实。
func (s *CodeFetchVerifyService) inspect(
	ctx context.Context,
	record domain.CodeFetchRecord,
) (domain.CodeFetchVerification, error) {
	container, err := s.probe.GetContainerRecord(ctx, record.ContainerID)
	if err != nil {
		// 容器登记记录都不存在时无法观测：这是可诊断的现场，不是"目录不存在"。
		return domain.CodeFetchVerification{
			ReadFailed: true,
			ReadError:  "读取容器登记记录失败: " + errorSummary(err),
		}, err
	}
	if err := ensureManaged(container); err != nil {
		return domain.CodeFetchVerification{
			ReadFailed: true,
			ReadError:  "容器不受管理: " + errorSummary(err),
		}, err
	}
	workDir := strings.TrimSpace(record.WorkDir)
	if workDir == "" {
		return domain.CodeFetchVerification{
			ReadFailed: true,
			ReadError:  "记录缺少容器内工作目录",
		}, errors.New("记录缺少容器内工作目录")
	}
	exists, existsErr := s.probePath(ctx, container, workDir)
	if existsErr != nil {
		return domain.CodeFetchVerification{
			ReadFailed: true,
			ReadError:  "核验目标目录失败: " + errorSummary(existsErr),
		}, existsErr
	}
	if !exists {
		return domain.CodeFetchVerification{WorkDirExists: false}, nil
	}
	observation := domain.CodeFetchVerification{WorkDirExists: true}
	branch, branchErr := s.probeOutput(ctx, container, workDir, "rev-parse", "--abbrev-ref", "HEAD")
	if branchErr != nil {
		// 不是 Git 仓库或 HEAD 不可读：保留现场，不猜测。
		return observation, branchErr
	}
	if strings.TrimSpace(branch) == "HEAD" {
		// detached HEAD：记录里保存的是任务分支名，这里不能断言分支一致。
		observation.Branch = ""
	} else {
		observation.Branch = strings.TrimSpace(branch)
	}
	observation.IsGitRepository = observation.Branch != ""
	head, headErr := s.probeOutput(ctx, container, workDir, "rev-parse", "HEAD")
	if headErr != nil {
		return observation, headErr
	}
	observation.Head = strings.TrimSpace(head)
	return observation, nil
}

// probePath 判断容器内目标目录是否存在。
func (s *CodeFetchVerifyService) probePath(
	ctx context.Context,
	container domain.Container,
	workDir string,
) (bool, error) {
	result, err := s.probe.Exec(ctx, ExecRequest{
		ContainerID: container.ID.String(),
		Command:     []string{"test", "-e", workDir},
	})
	if err != nil {
		return false, err
	}
	return result.Result.ExitCode == 0, nil
}

// probeOutput 在容器内工作区执行一条只读 Git 查询并返回标准输出。
func (s *CodeFetchVerifyService) probeOutput(
	ctx context.Context,
	container domain.Container,
	workDir string,
	args ...string,
) (string, error) {
	result, err := s.probe.Exec(ctx, ExecRequest{
		ContainerID: container.ID.String(),
		Command:     append([]string{"git"}, args...),
		WorkDir:     workDir,
	})
	if err != nil {
		return "", err
	}
	if result.Result.ExitCode != 0 {
		return "", fmt.Errorf("容器内 Git 查询失败: %s", strings.TrimSpace(result.Result.Stderr))
	}
	return result.Result.Stdout, nil
}

// persist 把核验结论写回记录。
//
// 只有收敛结论会改写状态；保持不确定时写入观测原因，使"为什么还不能收敛"可追溯。
// 写入以 uncertain 为前置条件：并发核验中只有先完成的一方生效。
func (s *CodeFetchVerifyService) persist(
	ctx context.Context,
	record domain.CodeFetchRecord,
	result CodeFetchVerifyResult,
) (domain.CodeFetchRecord, error) {
	candidate := record
	if result.Decision.Converged {
		if result.Decision.Status == domain.CodeFetchStatusSucceeded {
			// 只读核验只能确认"工作区在任务分支且 HEAD 可读"，无法补出基础分支
			// 与分支来源，因此走带核验语义的迁移，避免臆造缺失字段。
			if err := candidate.MarkVerifiedSucceeded(
				record.TaskBranch,
				result.Observation.Head,
				record.UpdatedAt,
			); err != nil {
				return record, err
			}
		} else if err := candidate.MarkFailed(
			result.Decision.Status,
			domain.CodeFetchStageVerify,
			result.DecisionErrorCode(),
			result.Decision.Summary,
			record.UpdatedAt,
		); err != nil {
			return record, err
		}
	} else {
		candidate.ErrorCode = "verify_inconclusive"
		candidate.ErrorSummary = summarizeCodeFetchVerification(result.Decision.Summary)
	}
	writeCtx := context.WithoutCancel(ctx)
	if err := s.store.UpdateCodeFetchRecordIfStatus(
		writeCtx,
		candidate,
		[]domain.CodeFetchStatus{domain.CodeFetchStatusUncertain},
	); err != nil {
		return record, err
	}
	return candidate, nil
}

// DecisionErrorCode 返回收敛结论对应的稳定错误分类。
func (r CodeFetchVerifyResult) DecisionErrorCode() string {
	if r.Decision.Converged && r.Decision.Status == domain.CodeFetchStatusSucceeded {
		return ""
	}
	return "verify_not_fetched"
}

// summarizeCodeFetchVerification 裁剪核验说明，避免写入超长文本。
//
// 说明会写入数据库并展示给用户，因此必须与其余摘要使用同样的上限。
func summarizeCodeFetchVerification(value string) string {
	trimmed := strings.TrimSpace(value)
	const limit = 600
	if len(trimmed) > limit {
		return trimmed[:limit] + "..."
	}
	return trimmed
}
