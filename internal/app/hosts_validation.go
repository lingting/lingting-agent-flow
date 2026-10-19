package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// HostValidationSubmitter 是提交后台校验的能力。
//
// 用接口而不是具体类型注入，使执行节点服务不直接依赖后台服务的实现细节，
// 也便于测试替换。
type HostValidationSubmitter interface {
	// SubmitHost 把节点当前代次的校验提交到后台队列。
	SubmitHost(ctx context.Context, hostID string) error
}

// validationAffectingFields 判断一次更新是否影响校验结果。
//
// 连接目标、认证方式、私钥路径、远程工作空间与启用状态都会改变校验输入，
// 因此只要其中任一字段发生有效变更就必须让旧结果失效。
func validationAffectingFields(before, after domain.ExecutionHost) bool {
	return before.Host != after.Host ||
		before.Port != after.Port ||
		before.Username != after.Username ||
		before.AuthMethod != after.AuthMethod ||
		before.PrivateKeyPath != after.PrivateKeyPath ||
		before.WorkspaceRoot != after.WorkspaceRoot ||
		before.Enabled != after.Enabled
}

// scheduleValidation 提交后台校验，失败时记录明确错误并让状态可恢复。
//
// 提交失败不能留下无法恢复的 checking：这里把状态收敛为 abnormal 并写入失败原因，
// 用户随后可以手动重新校验。
func (s *ExecutionHostService) scheduleValidation(
	ctx context.Context,
	host domain.ExecutionHost,
) (domain.ExecutionHost, error) {
	if s.validator == nil {
		// 未接入校验服务时（例如纯 CLI 场景）保持 checking，由启动恢复或手动校验推进。
		return host, nil
	}
	if err := s.validator.SubmitHost(ctx, host.ID.String()); err != nil {
		if errors.Is(err, ErrValidationServiceStopped) {
			// 后台校验服务尚未启动（例如 CLI 场景）：保持 checking 即可。
			// 该状态是可恢复的——Server 启动时会重新调度，用户也可以手动校验，
			// 因此不能因为"当前没有服务"就把它判为异常。
			s.debug(
				ctx,
				"校验服务未运行，节点保持待校验状态",
				slog.String("id", host.ID.String()),
			)
			return host, nil
		}
		failErr := s.failValidation(
			ctx,
			host.ID.String(),
			host.ValidationGeneration,
			"无法提交环境预检任务: "+err.Error(),
		)
		if failErr != nil {
			s.debug(
				ctx,
				"记录校验调度失败状态时出错",
				slog.String("id", host.ID.String()),
				slog.String("error", failErr.Error()),
			)
		}
		reloaded, loadErr := s.store.GetExecutionHost(ctx, host.ID)
		if loadErr != nil {
			return host, fmt.Errorf("提交环境预检任务失败: %w", err)
		}
		return *reloaded, fmt.Errorf("提交环境预检任务失败: %w", err)
	}
	s.debug(ctx, "已提交环境预检任务", slog.String("id", host.ID.String()))
	return host, nil
}

// failValidation 把校验收敛为 abnormal 并记录原因。
func (s *ExecutionHostService) failValidation(
	ctx context.Context,
	hostID string,
	generation int64,
	reason string,
) error {
	host, err := s.store.GetExecutionHost(ctx, domain.ID(hostID))
	if err != nil {
		return err
	}
	if host.ValidationGeneration != generation {
		return nil
	}
	if failErr := host.FailValidation(generation, reason, time.Now().UTC()); failErr != nil {
		return failErr
	}
	_, err = s.store.UpdateExecutionHostValidation(ctx, *host, generation)
	return err
}

// Revalidate 重新触发指定执行节点的环境预检。
//
// 该方法只把状态置回 checking、增加代次并提交后台任务，然后立即返回当前配置：
// HTTP 请求不会等待远程连接与镜像元数据查询结束。
func (s *ExecutionHostService) Revalidate(
	ctx context.Context,
	id string,
) (domain.ExecutionHost, error) {
	host, err := s.Get(ctx, id)
	if err != nil {
		return domain.ExecutionHost{}, err
	}
	updated := host
	updated.InvalidateValidation(time.Now())
	if err := s.store.UpdateExecutionHost(ctx, updated); err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("重新校验执行节点失败: %w", err)
	}
	s.debug(
		ctx,
		"执行节点校验结果已失效",
		slog.String("id", updated.ID.String()),
		slog.Int64("generation", updated.ValidationGeneration),
	)
	return s.scheduleValidation(ctx, updated)
}

// ValidateNow 同步执行一次完整校验。
//
// 该入口供 CLI 使用：认证方式固定为无口令私钥，因此不需要任何凭据输入；
// 它把状态置回 checking、增加代次并立即执行校验，使运维人员能看到完整结果。
func (s *ExecutionHostService) ValidateNow(
	ctx context.Context,
	id string,
) (domain.ExecutionHost, error) {
	host, err := s.Get(ctx, id)
	if err != nil {
		return domain.ExecutionHost{}, err
	}
	updated := host
	updated.InvalidateValidation(time.Now())
	if err := s.store.UpdateExecutionHost(ctx, updated); err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("重新校验执行节点失败: %w", err)
	}
	if s.hostValidator == nil {
		return domain.ExecutionHost{}, fmt.Errorf("重新校验执行节点失败: 校验器未初始化")
	}
	outcome, validateErr := s.hostValidator.Validate(ctx, ValidationRequest{
		HostID: updated.ID.String(),
	})
	if validateErr != nil {
		return outcome.Host, fmt.Errorf("校验执行节点失败: %w", validateErr)
	}
	return outcome.Host, nil
}

// ValidationDetails 返回节点已保存的校验详情。
//
// 详情不可用（例如旧记录或 JSON 损坏）时返回 nil，调用方应展示总体状态与摘要，
// 不能把缺失的详情当成通过。
func (s *ExecutionHostService) ValidationDetails(
	ctx context.Context,
	id string,
) (*domain.ValidationDetails, error) {
	host, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return host.ValidationDetails, nil
}
