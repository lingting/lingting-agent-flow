package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// PlanningAnalysisStart 是一轮分析启动后的持久化事实。
type PlanningAnalysisStart struct {
	// Session 是已推进到 analyzing 的规划会话。
	Session domain.PlanningSession
	// Message 是本次追加的用户消息；显式重新运行分析未提交补充消息时为空。
	Message *domain.PlanningMessage
}

// SubmitPlanningMessageRequest 是提交用户消息的输入。
//
// 它只接受正文：发送方固定为 user，序号由存储层在事务内分配，调用方无法自带序号
// 覆盖历史消息。
type SubmitPlanningMessageRequest struct {
	// Content 是用户输入的原文，按原样保存。
	Content string
}

// SubmitMessage 在一个事务内追加用户消息并把会话推进到 analyzing。
//
// 只有 active 与 preview_ready 会话接收普通消息：analyzing 期间一律拒绝，避免同一次
// 运行出现两个提示词来源。失败会话也不在这里重试——追加消息不得隐式重置失败结论，
// 必须调用 RetryAnalysis。
//
// 消息写入、状态迁移与条件更新共享同一个事务：任一步失败时不会出现"消息已保存但
// 分析未启动"，也不会出现"分析已启动但提示词丢失"。
func (s *PlanningSessionService) SubmitMessage(
	ctx context.Context,
	sessionIDValue string,
	request SubmitPlanningMessageRequest,
) (PlanningAnalysisStart, error) {
	sessionID, err := parsePlanningSessionID(sessionIDValue)
	if err != nil {
		return PlanningAnalysisStart{}, err
	}
	content := request.Content
	if strings.TrimSpace(content) == "" {
		return PlanningAnalysisStart{}, fmt.Errorf(
			"%w: 消息内容不能为空",
			ErrPlanningSessionInvalidInput,
		)
	}
	start := PlanningAnalysisStart{}
	err = s.store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		session, err := readPlanningSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		// 准入规则只在 domain 定义一次：这里只装配流程，不重写状态白名单。
		if !session.Status.CanAcceptMessage() {
			return fmt.Errorf(
				"%w: 规划会话处于 %s，不能接收新消息",
				ErrPlanningSessionStateConflict,
				session.Status,
			)
		}
		previous := session.Status
		at := s.now()
		message, err := tx.AppendPlanningMessage(ctx, storage.AppendPlanningMessageRequest{
			PlanningSessionID: sessionID,
			SenderType:        domain.PlanningSenderUser,
			Content:           content,
			At:                at,
		})
		if err != nil {
			return fmt.Errorf("追加规划消息失败: %w", err)
		}
		if err := session.BeginAnalysis(at); err != nil {
			return mapPlanningTransitionFailure(err)
		}
		// 条件更新以读到的状态为前置条件：并发提交时最多只有一轮分析能启动，另一轮
		// 因状态已变更而回滚，连同它追加的消息一起丢弃。
		if err := tx.UpdatePlanningSessionIfStatus(
			ctx,
			session,
			[]domain.PlanningSessionStatus{previous},
		); err != nil {
			return mapPlanningUpdateFailure(err, sessionID)
		}
		start = PlanningAnalysisStart{Session: session, Message: &message}
		return nil
	})
	if err != nil {
		return PlanningAnalysisStart{}, err
	}
	return start, nil
}

// RetryPlanningAnalysisRequest 是显式重新运行分析的输入。
type RetryPlanningAnalysisRequest struct {
	// Content 是可选的用户补充消息；空值表示直接在既有消息历史上重跑本轮分析。
	Content string
}

// RetryAnalysis 让失败的规划会话显式重新开始一轮分析。
//
// 它是 failed 唯一的出口：普通消息追加不会把失败结论重置为 analyzing。补充消息与
// 状态迁移在同一事务内完成，失败整体回滚。
func (s *PlanningSessionService) RetryAnalysis(
	ctx context.Context,
	sessionIDValue string,
	request RetryPlanningAnalysisRequest,
) (PlanningAnalysisStart, error) {
	sessionID, err := parsePlanningSessionID(sessionIDValue)
	if err != nil {
		return PlanningAnalysisStart{}, err
	}
	content := request.Content
	start := PlanningAnalysisStart{}
	err = s.store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		session, err := readPlanningSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		at := s.now()
		var message *domain.PlanningMessage
		if strings.TrimSpace(content) != "" {
			appended, err := tx.AppendPlanningMessage(ctx, storage.AppendPlanningMessageRequest{
				PlanningSessionID: sessionID,
				SenderType:        domain.PlanningSenderUser,
				Content:           content,
				At:                at,
			})
			if err != nil {
				return fmt.Errorf("追加规划消息失败: %w", err)
			}
			message = &appended
		}
		if err := session.RetryAnalysis(at); err != nil {
			return mapPlanningTransitionFailure(err)
		}
		if err := tx.UpdatePlanningSessionIfStatus(
			ctx,
			session,
			[]domain.PlanningSessionStatus{domain.PlanningSessionFailed},
		); err != nil {
			return mapPlanningUpdateFailure(err, sessionID)
		}
		start = PlanningAnalysisStart{Session: session, Message: message}
		return nil
	})
	if err != nil {
		return PlanningAnalysisStart{}, err
	}
	return start, nil
}

// PlanningRoleMessage 是一轮分析产生的角色消息。
type PlanningRoleMessage struct {
	// SenderType 只能是需求分析师或方案规划师：用户与系统消息不能在这里伪造。
	SenderType domain.PlanningSenderType
	// RoleID 是发送消息的角色配置标识，可空。
	RoleID *domain.ID
	// Content 是消息正文，按原样保存。
	Content string
}

// CompletePlanningAnalysisRequest 是一轮分析完成的输入。
//
// 结构化结果按原样保存为 JSON 快照：字段语义由后续里程碑在 domain 中定义，本层不
// 解析、不改写，也不为内部字段建立业务规则。结构合法性仍由 domain 的
// PlanningRevision 校验。
type CompletePlanningAnalysisRequest struct {
	// RequirementAnalysisJSON 是需求分析师的结构化分析快照。
	RequirementAnalysisJSON json.RawMessage
	// ImplementationPlanJSON 是方案规划师的结构化方案快照。
	ImplementationPlanJSON json.RawMessage
	// ValidationResultJSON 是本轮方案校验结果，空值表示尚未校验。
	ValidationResultJSON json.RawMessage
	// Messages 是本轮产生的角色消息，只追加不改写。
	Messages []PlanningRoleMessage
}

// CompletePlanningAnalysisResult 是一轮分析完成后的持久化事实。
type CompletePlanningAnalysisResult struct {
	// Session 是已推进到 preview_ready 的规划会话。
	Session domain.PlanningSession
	// Revision 是本次追加的方案版本，版本号由存储层在事务内分配。
	Revision domain.PlanningRevision
	// Messages 是本次追加的角色消息，序号由存储层在事务内分配。
	Messages []domain.PlanningMessage
}

// CompleteAnalysis 在一个事务内保存方案版本与角色消息，并把会话推进到 preview_ready。
//
// 执行顺序固定为：读取并确认会话处于 analyzing → 追加方案版本 → 追加角色消息 →
// MarkPreviewReady → 以 analyzing 为前置条件写回。任何一步失败都整体回滚：不会留下
// 没有状态的版本，也不会出现"未完成的输出被标记为待确认"。
//
// 它不覆盖已有版本、不修改历史消息，也不解析任务草案或创建正式 Requirement / Task。
func (s *PlanningSessionService) CompleteAnalysis(
	ctx context.Context,
	sessionIDValue string,
	request CompletePlanningAnalysisRequest,
) (CompletePlanningAnalysisResult, error) {
	sessionID, err := parsePlanningSessionID(sessionIDValue)
	if err != nil {
		return CompletePlanningAnalysisResult{}, err
	}
	if err := validatePlanningRoleMessages(request.Messages); err != nil {
		return CompletePlanningAnalysisResult{}, err
	}
	result := CompletePlanningAnalysisResult{}
	err = s.store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		session, err := readPlanningSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if session.Status != domain.PlanningSessionAnalyzing {
			return fmt.Errorf(
				"%w: 规划会话处于 %s，只有运行中的分析才能收敛结果",
				ErrPlanningSessionStateConflict,
				session.Status,
			)
		}
		at := s.now()
		revision, err := tx.AppendPlanningRevision(ctx, storage.AppendPlanningRevisionRequest{
			PlanningSessionID:       sessionID,
			RequirementAnalysisJSON: request.RequirementAnalysisJSON,
			ImplementationPlanJSON:  request.ImplementationPlanJSON,
			ValidationResultJSON:    request.ValidationResultJSON,
			At:                      at,
		})
		if err != nil {
			return mapPlanningRevisionFailure(err)
		}
		messages := make([]domain.PlanningMessage, 0, len(request.Messages))
		for _, item := range request.Messages {
			message, err := tx.AppendPlanningMessage(ctx, storage.AppendPlanningMessageRequest{
				PlanningSessionID: sessionID,
				SenderType:        item.SenderType,
				RoleID:            item.RoleID,
				Content:           item.Content,
				At:                at,
			})
			if err != nil {
				return fmt.Errorf("追加规划消息失败: %w", err)
			}
			messages = append(messages, message)
		}
		if err := session.MarkPreviewReady(at); err != nil {
			return mapPlanningTransitionFailure(err)
		}
		// 迟到结果不能覆盖其他操作已产生的状态：条件更新失败时版本与消息一起回滚。
		if err := tx.UpdatePlanningSessionIfStatus(
			ctx,
			session,
			[]domain.PlanningSessionStatus{domain.PlanningSessionAnalyzing},
		); err != nil {
			return mapPlanningUpdateFailure(err, sessionID)
		}
		result = CompletePlanningAnalysisResult{
			Session:  session,
			Revision: revision,
			Messages: messages,
		}
		return nil
	})
	if err != nil {
		return CompletePlanningAnalysisResult{}, err
	}
	return result, nil
}

// FailPlanningAnalysisRequest 是分析失败的输入。
type FailPlanningAnalysisRequest struct {
	// Summary 是失败原因摘要，保存前统一脱敏与裁剪。
	Summary string
}

// FailAnalysis 把运行中的分析收敛为失败。
//
// 摘要经 domain.ClipFeedbackText 脱敏与裁剪：只保留定位本轮失败所需的信息，不保存
// 模型密钥、私钥、SSH 凭据或完整环境变量。失败不写方案版本，也不写成功消息。
//
// 失败是显式结论而不是自动回退：状态保留为 failed，只有 RetryAnalysis 能让它重新
// 进入 analyzing。
func (s *PlanningSessionService) FailAnalysis(
	ctx context.Context,
	sessionIDValue string,
	request FailPlanningAnalysisRequest,
) (domain.PlanningSession, error) {
	sessionID, err := parsePlanningSessionID(sessionIDValue)
	if err != nil {
		return domain.PlanningSession{}, err
	}
	summary := domain.ClipFeedbackText(request.Summary, domain.MaxFeedbackReason)
	if summary == "" {
		summary = "本轮分析失败"
	}
	var session domain.PlanningSession
	err = s.store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		loaded, err := readPlanningSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if err := loaded.Fail(summary, s.now()); err != nil {
			return mapPlanningTransitionFailure(err)
		}
		if err := tx.UpdatePlanningSessionIfStatus(
			ctx,
			loaded,
			[]domain.PlanningSessionStatus{domain.PlanningSessionAnalyzing},
		); err != nil {
			return mapPlanningUpdateFailure(err, sessionID)
		}
		session = loaded
		return nil
	})
	if err != nil {
		return domain.PlanningSession{}, err
	}
	return session, nil
}

// validatePlanningRoleMessages 校验本轮角色消息的发送方与正文。
//
// 只允许规划角色（需求分析师与方案规划师）：用户消息必须经用户提交入口写入，系统
// 消息由系统记录产生，两者都不能在收敛分析结果时被伪造。
func validatePlanningRoleMessages(messages []PlanningRoleMessage) error {
	for index, item := range messages {
		if !item.SenderType.IsPlanningRole() {
			return fmt.Errorf(
				"%w: 第 %d 条消息的发送方 %q 不是规划角色",
				ErrPlanningSessionInvalidInput,
				index+1,
				item.SenderType,
			)
		}
		if strings.TrimSpace(item.Content) == "" {
			return fmt.Errorf(
				"%w: 第 %d 条消息内容不能为空",
				ErrPlanningSessionInvalidInput,
				index+1,
			)
		}
	}
	return nil
}

// mapPlanningRevisionFailure 把方案版本的写入失败映射为业务错误。
//
// 结构校验仍由 domain 的 PlanningRevision 承担：这里只把"JSON 快照不合法"与"版本号
// 已被占用"翻译成调用方可以处理的两类业务错误，不重复定义字段规则。
func mapPlanningRevisionFailure(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalidValue):
		return fmt.Errorf("%w: %w", ErrPlanningSessionInvalidInput, err)
	case errors.Is(err, storage.ErrConflict):
		return fmt.Errorf("%w: 方案版本号已被占用", ErrPlanningSessionStateConflict)
	case errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("%w: 规划会话不存在", ErrPlanningSessionNotFound)
	default:
		return fmt.Errorf("追加规划版本失败: %w", err)
	}
}
