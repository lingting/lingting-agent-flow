package domain

import (
	"fmt"
	"strings"
	"time"
)

// PlanningSessionStatus 表示一次需求规划会话所处的阶段。
//
// 它与 TaskStatus / WorkflowRunStatus 是不同层次的概念，不得互相替代：规划阶段只
// 产生消息、分析与方案草案，不创建正式 Task，也不进入任务执行状态机。
type PlanningSessionStatus string

// 规划会话状态取值。
const (
	// PlanningSessionActive 表示会话已建立，等待用户输入需求。
	PlanningSessionActive PlanningSessionStatus = "active"
	// PlanningSessionAnalyzing 表示一轮分析正在运行。
	//
	// 同一会话同一时刻只允许一轮：该状态期间的追加消息请求必须被拒绝，不得并发
	// 启动第二个规划调用。
	PlanningSessionAnalyzing PlanningSessionStatus = "analyzing"
	// PlanningSessionPreviewReady 表示本轮方案已生成并落成 Revision，等待用户决定。
	PlanningSessionPreviewReady PlanningSessionStatus = "preview_ready"
	// PlanningSessionConfirmed 表示用户已确认某一版 Revision，会话结束。
	PlanningSessionConfirmed PlanningSessionStatus = "confirmed"
	// PlanningSessionAbandoned 表示用户放弃本次规划，会话结束。
	PlanningSessionAbandoned PlanningSessionStatus = "abandoned"
	// PlanningSessionFailed 表示本轮分析失败。
	//
	// 它不是普通终态：只有显式"重新运行分析"的业务操作才能让它回到 analyzing，
	// 追加消息不得隐式重置失败结论。
	PlanningSessionFailed PlanningSessionStatus = "failed"
)

// planningSessionStatusMachine 定义规划会话状态的迁移。
//
// 关键约束：
//   - confirmed 与 abandoned 是终态，任何路径都不能重新打开；
//   - failed 只能经 analyzing 重新开始一轮分析，不允许直接回到 active；
//   - analyzing 只能收敛为 preview_ready 或 failed：一轮运行在途时既不接受新消息，
//     也不接受放弃，必须先等本轮收敛；
//   - preview_ready 允许回到 analyzing，用于用户在同一容器、同一代码快照上继续对话。
var planningSessionStatusMachine = statusMachine{
	entity: "PlanningSession",
	transitions: map[string][]string{
		string(PlanningSessionActive): {
			string(PlanningSessionAnalyzing),
			string(PlanningSessionAbandoned),
		},
		string(PlanningSessionAnalyzing): {
			string(PlanningSessionPreviewReady),
			string(PlanningSessionFailed),
		},
		string(PlanningSessionPreviewReady): {
			string(PlanningSessionAnalyzing),
			string(PlanningSessionConfirmed),
			string(PlanningSessionAbandoned),
		},
		string(PlanningSessionFailed): {
			string(PlanningSessionAnalyzing),
		},
	},
	terminal: []string{
		string(PlanningSessionConfirmed),
		string(PlanningSessionAbandoned),
	},
}

// Validate 校验规划会话状态是否属于已知状态。
func (s PlanningSessionStatus) Validate() error {
	if planningSessionStatusMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("PlanningSession", "status", "未知状态 "+string(s))
}

// IsTerminal 判断规划会话是否已经结束且不可重新打开。
//
// 只有 confirmed 与 abandoned 是终态；failed 允许显式重新运行分析。
func (s PlanningSessionStatus) IsTerminal() bool {
	return planningSessionStatusMachine.isTerminal(string(s))
}

// IsFinished 判断规划会话是否已经不再运行。
//
// 它在终态之外还包含 failed，用于"是否需要释放规划容器"之类的收尾判断：失败会话
// 同样不再占用运行环境，但保留显式的重试入口。
func (s PlanningSessionStatus) IsFinished() bool {
	return s.IsTerminal() || s == PlanningSessionFailed
}

// IsActive 判断规划会话是否仍是进行中的会话。
//
// 未知状态一律返回 false：读取路径保留未知取值，但不得把未知取值当作"仍在进行"。
func (s PlanningSessionStatus) IsActive() bool {
	switch s {
	case PlanningSessionActive, PlanningSessionAnalyzing, PlanningSessionPreviewReady:
		return true
	default:
		return false
	}
}

// CanAcceptMessage 判断当前状态是否允许追加消息。
//
// analyzing 期间拒绝：一轮分析在途时追加消息会让同一次运行出现两个提示词来源。
// 终态与失败状态同样拒绝，失败只能通过显式的重新运行分析回到 analyzing。
func (s PlanningSessionStatus) CanAcceptMessage() bool {
	switch s {
	case PlanningSessionActive, PlanningSessionPreviewReady:
		return true
	default:
		return false
	}
}

// PlanningSession 是一次需求规划会话的持久化记录。
//
// 它保存规划阶段本身的全部事实：所属项目、状态、规划容器与执行节点、固定代码快照
// 以及两个角色的 Pi 会话标识。规划会话与 Task / Step / Attempt / Session 执行记录
// 是两套模型，不得写进同一批表：规划阶段不产生正式任务。
type PlanningSession struct {
	ID ID `json:"id"`
	// ProjectID 是会话所属项目标识。
	//
	// 项目标识同时决定代码快照的来源与缓存共享范围，因此必须是真实存在且未被删除
	// 的项目；引用完整性由应用层校验，数据库不声明外键。
	ProjectID string `json:"project_id"`
	// Status 是会话状态。
	Status PlanningSessionStatus `json:"status"`
	// HostID 是承载规划容器的执行节点；本机执行时为空。
	HostID *ID `json:"host_id,omitempty"`
	// ContainerID 是规划容器的登记记录标识，尚未准备时为空。
	ContainerID string `json:"container_id,omitempty"`
	// SourceCommit 是规划容器内代码快照固定的 commit SHA。
	//
	// 空值表示快照尚未准备；非空时必须是完整的提交 SHA，多轮规划复用同一个值，
	// 从而保证所有分析都基于同一份代码。
	SourceCommit string `json:"source_commit,omitempty"`
	// AnalystSessionID 是需求分析师角色的 Pi 侧会话标识。
	//
	// 本阶段只保存不解释：会话文件的创建、路径生成与上下文复用由规划运行时负责，
	// 它不属于 sessions 表的执行记录。
	AnalystSessionID string `json:"analyst_session_id,omitempty"`
	// PlannerSessionID 是方案规划师角色的 Pi 侧会话标识。
	PlannerSessionID string `json:"planner_session_id,omitempty"`
	// AdoptedRevisionID 是用户确认采纳的方案版本标识，只有 confirmed 会话才有值。
	AdoptedRevisionID *ID `json:"adopted_revision_id,omitempty"`
	// ErrorSummary 是最近一次分析失败的原因摘要。
	ErrorSummary string    `json:"error_summary,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	// FinishedAt 是会话停止运行的时间；只有 finished 状态才有值。
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// NewPlanningSession 创建一条处于 active 状态的规划会话。
func NewPlanningSession(projectID string, at time.Time) (PlanningSession, error) {
	now := normalizeTime(at)
	session := PlanningSession{
		ID:        NewID(),
		ProjectID: strings.TrimSpace(projectID),
		Status:    PlanningSessionActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := session.Validate(); err != nil {
		return PlanningSession{}, err
	}
	return session, nil
}

// Validate 校验规划会话是否完整。
//
// 它同时约束状态与时间字段的一致性：finished 状态必须记录结束时间，未结束的会话
// 不能带结束时间，采纳的 Revision 只能出现在 confirmed 会话上。
func (s PlanningSession) Validate() error {
	if err := s.ID.Validate(); err != nil {
		return err
	}
	if err := s.validateProjectID(); err != nil {
		return err
	}
	if err := s.Status.Validate(); err != nil {
		return err
	}
	if s.HostID != nil {
		if err := s.HostID.Validate(); err != nil {
			return err
		}
	}
	if commit := strings.TrimSpace(s.SourceCommit); commit != "" {
		if err := ValidateGitCommitSHA("source_commit", commit); err != nil {
			return err
		}
	}
	if s.AdoptedRevisionID != nil {
		if err := s.AdoptedRevisionID.Validate(); err != nil {
			return err
		}
		if s.Status != PlanningSessionConfirmed {
			return invalidField(
				"PlanningSession",
				"adopted_revision_id",
				"只有已确认的规划会话才能记录采纳的方案版本",
			)
		}
	}
	if s.CreatedAt.IsZero() {
		return invalidField("PlanningSession", "created_at", "创建时间不能为空")
	}
	if s.UpdatedAt.IsZero() {
		return invalidField("PlanningSession", "updated_at", "更新时间不能为空")
	}
	if s.UpdatedAt.Before(s.CreatedAt) {
		return invalidField("PlanningSession", "updated_at", "更新时间不能早于创建时间")
	}
	if s.Status.IsFinished() && s.FinishedAt == nil {
		return invalidField("PlanningSession", "finished_at", "已结束的规划会话必须记录结束时间")
	}
	if !s.Status.IsFinished() && s.FinishedAt != nil {
		return invalidField("PlanningSession", "finished_at", "未结束的规划会话不能记录结束时间")
	}
	return nil
}

// validateProjectID 校验会话必须关联一个书写合法的项目标识。
//
// 空值不是"未指定"：规划会话离开项目就没有代码来源，因此这里直接拒绝。
func (s PlanningSession) validateProjectID() error {
	id := strings.TrimSpace(s.ProjectID)
	if id == "" {
		return invalidField("PlanningSession", "project_id", "规划会话必须关联项目")
	}
	return ValidateProjectID(id)
}

// Transition 把规划会话迁移到下一个状态，并维护更新时间与结束时间。
//
// 迁移合法性只由状态机决定；业务上的"何时允许迁移"由各语义方法（开始分析、重新
// 运行分析、确认、放弃、失败）表达，调用方不得绕过它们直接赋值状态字段。
func (s *PlanningSession) Transition(next PlanningSessionStatus, at time.Time) error {
	if s == nil {
		return invalidField("PlanningSession", "", "实体为空")
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if s.Status == next {
		return nil
	}
	if err := planningSessionStatusMachine.validate(string(s.Status), string(next)); err != nil {
		return err
	}
	now := normalizeTime(at)
	s.Status = next
	s.UpdatedAt = now
	if next.IsFinished() {
		finished := now
		s.FinishedAt = &finished
	} else {
		s.FinishedAt = nil
	}
	return nil
}

// BeginAnalysis 开始一轮新的分析。
//
// 只有 active 与 preview_ready 可以开始：前者是首轮分析，后者是用户在同一容器、
// 同一代码快照上继续对话。失败会话必须走 RetryAnalysis，避免把失败结论静默当作
// 普通继续。
func (s *PlanningSession) BeginAnalysis(at time.Time) error {
	if s == nil {
		return invalidField("PlanningSession", "", "实体为空")
	}
	switch s.Status {
	case PlanningSessionActive, PlanningSessionPreviewReady:
		return s.Transition(PlanningSessionAnalyzing, at)
	default:
		return fmt.Errorf(
			"%w: 规划会话处于 %s，不能开始新一轮分析",
			ErrInvalidState,
			s.Status,
		)
	}
}

// RetryAnalysis 让失败的规划会话重新开始一轮分析。
//
// 它是 failed 唯一允许的出口：普通消息追加不得隐式重置失败状态，只有用户显式触发
// 的重新运行才能回到 analyzing。
func (s *PlanningSession) RetryAnalysis(at time.Time) error {
	if s == nil {
		return invalidField("PlanningSession", "", "实体为空")
	}
	if s.Status != PlanningSessionFailed {
		return fmt.Errorf("%w: 规划会话处于 %s，只有失败会话可以重新运行分析", ErrInvalidState, s.Status)
	}
	if err := s.Transition(PlanningSessionAnalyzing, at); err != nil {
		return err
	}
	s.ErrorSummary = ""
	return nil
}

// MarkPreviewReady 记录本轮方案已生成并落成 Revision。
func (s *PlanningSession) MarkPreviewReady(at time.Time) error {
	if s == nil {
		return invalidField("PlanningSession", "", "实体为空")
	}
	return s.Transition(PlanningSessionPreviewReady, at)
}

// Confirm 采纳某一版 Revision 并结束会话。
//
// 状态迁移与采纳版本同时写入：已确认的会话必须能指出它采纳了哪一版方案。
func (s *PlanningSession) Confirm(revisionID ID, at time.Time) error {
	if s == nil {
		return invalidField("PlanningSession", "", "实体为空")
	}
	if err := revisionID.Validate(); err != nil {
		return err
	}
	// 只有待确认的会话可以确认：confirmed 是终态，重复确认会改写已采纳的方案版本。
	if s.Status != PlanningSessionPreviewReady {
		return fmt.Errorf(
			"%w: 规划会话处于 %s，只有待确认的会话才能确认",
			ErrInvalidState,
			s.Status,
		)
	}
	if err := s.Transition(PlanningSessionConfirmed, at); err != nil {
		return err
	}
	adopted := revisionID
	s.AdoptedRevisionID = &adopted
	return nil
}

// Abandon 放弃本次规划并结束会话。
func (s *PlanningSession) Abandon(at time.Time) error {
	if s == nil {
		return invalidField("PlanningSession", "", "实体为空")
	}
	return s.Transition(PlanningSessionAbandoned, at)
}

// Fail 记录本轮分析失败。
//
// 失败结论必须落库：容器与快照随后释放，进程退出后不再有内存状态可依据。
func (s *PlanningSession) Fail(summary string, at time.Time) error {
	if s == nil {
		return invalidField("PlanningSession", "", "实体为空")
	}
	if s.Status != PlanningSessionAnalyzing {
		return fmt.Errorf(
			"%w: 规划会话处于 %s，只有运行中的分析才能记为失败",
			ErrInvalidState,
			s.Status,
		)
	}
	if err := s.Transition(PlanningSessionFailed, at); err != nil {
		return err
	}
	s.ErrorSummary = strings.TrimSpace(summary)
	return nil
}

// BindRuntime 记录规划会话使用的执行节点、规划容器与固定代码快照。
//
// 绑定运行期字段不改变状态，也不受终态限制：容器与快照的创建与释放由规划运行时
// 负责，状态判定仍由上面的语义方法承担。
func (s *PlanningSession) BindRuntime(hostID *ID, containerID, sourceCommit string, at time.Time) error {
	if s == nil {
		return invalidField("PlanningSession", "", "实体为空")
	}
	commit := strings.TrimSpace(sourceCommit)
	if commit != "" {
		if err := ValidateGitCommitSHA("source_commit", commit); err != nil {
			return err
		}
	}
	var host *ID
	if hostID != nil {
		if err := hostID.Validate(); err != nil {
			return err
		}
		value := *hostID
		host = &value
	}
	s.HostID = host
	s.ContainerID = strings.TrimSpace(containerID)
	s.SourceCommit = commit
	s.UpdatedAt = normalizeTime(at)
	return nil
}

// BindRoleSessions 记录两个规划角色的 Pi 侧会话标识。
//
// 标识形式由规划运行时决定，这里只做去空白与落库；同一会话多轮复用同一对标识，
// 因此重复写入相同取值是幂等的。两个角色必须各自独立：两个取值相同说明上下文会串线，
// 直接拒绝而不是落库后由调用方自行发现。
func (s *PlanningSession) BindRoleSessions(analystSessionID, plannerSessionID string, at time.Time) error {
	if s == nil {
		return invalidField("PlanningSession", "", "实体为空")
	}
	analyst := strings.TrimSpace(analystSessionID)
	planner := strings.TrimSpace(plannerSessionID)
	if analyst != "" && analyst == planner {
		return invalidField(
			"PlanningSession",
			"analyst_session_id",
			"两个角色的 Pi 会话标识不能相同",
		)
	}
	s.AnalystSessionID = analyst
	s.PlannerSessionID = planner
	s.UpdatedAt = normalizeTime(at)
	return nil
}
