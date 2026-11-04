package domain

import (
	"strings"
	"time"
)

// PlanningSenderType 表示一条规划消息的发送方。
//
// 取值集合固定为四类，不提供自定义 Agent 角色体系：用户、需求分析师、方案规划师
// 与系统。未知取值仍然可以被读回，见存储层的读取路径。
type PlanningSenderType string

// 规划消息发送方取值。
const (
	// PlanningSenderUser 表示用户输入。
	PlanningSenderUser PlanningSenderType = "user"
	// PlanningSenderRequirementAnalyst 表示需求分析师的结构化输出。
	PlanningSenderRequirementAnalyst PlanningSenderType = "requirement_analyst"
	// PlanningSenderPlanner 表示方案规划师的输出。
	PlanningSenderPlanner PlanningSenderType = "planner"
	// PlanningSenderSystem 表示系统记录（例如运行环境说明、失败诊断）。
	PlanningSenderSystem PlanningSenderType = "system"
)

// Validate 校验发送方类型是否属于已知取值。
func (t PlanningSenderType) Validate() error {
	switch t {
	case PlanningSenderUser,
		PlanningSenderRequirementAnalyst,
		PlanningSenderPlanner,
		PlanningSenderSystem:
		return nil
	default:
		return invalidField("PlanningMessage", "sender_type", "未知的发送方 "+string(t))
	}
}

// IsPlanningRole 判断发送方是否是规划角色的输出。
//
// 一轮分析完成后产生的角色消息只允许来自需求分析师与方案规划师：用户消息必须经
// 用户提交入口写入，系统消息由系统记录产生，两者都不能在收敛分析结果时被伪造。
// 未知取值一律返回 false，不把它当作合法角色。
func (t PlanningSenderType) IsPlanningRole() bool {
	switch t {
	case PlanningSenderRequirementAnalyst, PlanningSenderPlanner:
		return true
	default:
		return false
	}
}

// PlanningMessage 是规划会话中的一条消息。
//
// 序号在会话内唯一且稳定：它由存储层在写入事务内分配，调用方不能自带序号覆盖历史
// 消息。消息只追加不覆盖，历史对话不因后续轮次被改写。
type PlanningMessage struct {
	ID ID `json:"id"`
	// PlanningSessionID 是消息所属规划会话。
	PlanningSessionID ID `json:"planning_session_id"`
	// Sequence 是会话内的消息序号，从 1 开始且唯一。
	Sequence int `json:"sequence"`
	// SenderType 是消息发送方类型。
	SenderType PlanningSenderType `json:"sender_type"`
	// RoleID 是发送消息的角色配置标识；用户与系统消息为空。
	RoleID *ID `json:"role_id,omitempty"`
	// Content 是消息正文。
	Content string `json:"content"`
	// CreatedAt 是消息创建时间。
	CreatedAt time.Time `json:"created_at"`
}

// NewPlanningMessage 创建并校验一条规划消息。
//
// 序号作为参数传入，因为它由存储层在事务内分配：这里是存储层分配完成后构造实体的
// 唯一入口，写入路径不会接受调用方自带的序号。
func NewPlanningMessage(
	sessionID ID,
	sequence int,
	sender PlanningSenderType,
	roleID *ID,
	content string,
	at time.Time,
) (PlanningMessage, error) {
	message := PlanningMessage{
		ID:                NewID(),
		PlanningSessionID: sessionID,
		Sequence:          sequence,
		SenderType:        sender,
		RoleID:            roleID,
		Content:           content,
		CreatedAt:         normalizeTime(at),
	}
	if err := message.Validate(); err != nil {
		return PlanningMessage{}, err
	}
	return message, nil
}

// Validate 校验规划消息是否完整。
//
// 正文只校验"不只是空白"，不裁剪原始内容：用户输入与模型输出的空白可能具有含义，
// 写入与读回必须保持同一份文本。
func (m PlanningMessage) Validate() error {
	if err := m.ID.Validate(); err != nil {
		return err
	}
	if err := m.PlanningSessionID.Validate(); err != nil {
		return err
	}
	if m.Sequence <= 0 {
		return invalidField("PlanningMessage", "sequence", "消息序号必须大于 0")
	}
	if err := m.SenderType.Validate(); err != nil {
		return err
	}
	if m.RoleID != nil {
		if err := m.RoleID.Validate(); err != nil {
			return err
		}
	}
	if strings.TrimSpace(m.Content) == "" {
		return invalidField("PlanningMessage", "content", "消息内容不能为空")
	}
	if m.CreatedAt.IsZero() {
		return invalidField("PlanningMessage", "created_at", "创建时间不能为空")
	}
	return nil
}
