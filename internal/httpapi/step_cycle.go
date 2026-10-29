package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
)

// 执行周期路径段。
const (
	// openRepairCycleSegment 是"显式开启执行周期"动作的路径段。
	openRepairCycleSegment = "open-repair-cycle"
	// repairCycleEligibilitySegment 是"能否开启新的执行周期"只读查询的路径段。
	repairCycleEligibilitySegment = "repair-cycle-eligibility"
)

// OpenRepairCycleResponse 是一次执行周期开启的对外表示。
//
// 结构只描述周期登记与预算事实：`opened` 为 true 只说明新的预算作用域已登记，
// 不代表步骤已经执行、验证已经通过或任务已经完成。
type OpenRepairCycleResponse struct {
	TaskID string `json:"task_id"`
	StepID string `json:"step_id"`
	// CycleID 是新登记的执行周期标识，后续执行记录都归属于它。
	CycleID string `json:"cycle_id"`
	// CycleSequence 是该步骤内的周期序号。
	CycleSequence int `json:"cycle_sequence"`
	// MaxAttempts 是新周期生效的尝试次数上限（含首轮）。
	MaxAttempts int `json:"max_attempts"`
	// PreviousConsumed 是上一周期已消耗的尝试数。
	//
	// 它解释"为什么允许重置"：只有上一周期预算已用尽或策略不再允许修复时才允许
	// 开启新周期，调用方可以据此核验。
	PreviousConsumed int       `json:"previous_consumed"`
	OpenedAt         time.Time `json:"opened_at"`
}

// serveOpenRepairCycle 处理
// POST /api/v1/tasks/{taskID}/steps/{stepID}/open-repair-cycle。
//
// 该入口只登记预算作用域，不启动任何 Agent 调用：开启成功后仍需通过
// execute-and-validate 执行。这样"重置预算"与"执行"是两个可审计的独立动作。
func (h *stepExecutionHandler) serveOpenRepairCycle(
	w http.ResponseWriter,
	r *http.Request,
	taskID, stepID string,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(
			w,
			r,
			h.logger,
			http.StatusMethodNotAllowed,
			CodeMethodNotAllowed,
			"该接口只支持 POST 请求",
		)
		return
	}
	if h.workflow == nil {
		writeError(
			w,
			r,
			h.logger,
			http.StatusServiceUnavailable,
			CodeInternal,
			"步骤开发—验证闭环服务未初始化",
		)
		return
	}
	result, err := h.workflow.OpenRepairCycle(r.Context(), app.OpenRepairCycleRequest{
		TaskID: taskID,
		StepID: stepID,
	})
	if err != nil {
		h.writeWorkflowError(w, r, app.ExecuteAndValidateResult{}, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, OpenRepairCycleResponse{
		TaskID:           result.TaskID.String(),
		StepID:           result.StepID.String(),
		CycleID:          result.CycleID.String(),
		CycleSequence:    result.CycleSequence,
		MaxAttempts:      result.MaxAttempts,
		PreviousConsumed: result.PreviousConsumed,
		OpenedAt:         result.OpenedAt,
	})
}

// parseOpenRepairCyclePath 判断路径是否指向"显式开启执行周期"动作。
//
// 只有 {taskID}/steps/{stepID}/open-repair-cycle 这一种形状成立；其余形状返回
// false，由执行记录查询或任务处理器给出各自的 404 / 405 语义。
func parseOpenRepairCyclePath(path string) (taskID, stepID string, ok bool) {
	remainder := strings.TrimPrefix(path, taskPathPrefix)
	if remainder == path {
		return "", "", false
	}
	segments := splitPathSegments(remainder)
	if len(segments) != 4 {
		return "", "", false
	}
	if segments[1] != stepsSegment || segments[3] != openRepairCycleSegment {
		return "", "", false
	}
	if !validTaskPathID(segments[0]) || !validTaskPathID(segments[2]) {
		return "", "", false
	}
	return segments[0], segments[2], true
}

// RepairCycleEligibilityResponse 是"能否开启新的执行周期"的只读结论。
//
// 字段分三组，互不替代：
//   - 归属与状态：`task_id` / `step_id` / `task_status` / `step_status`；
//   - 结论：`can_open_cycle` 只有在阻塞列表为空时为 true，`blocking` 是稳定类别，
//     `summary` 是面向用户的说明；
//   - 预算事实：`active_cycle` / `cycle_sequence` / `max_attempts` /
//     `consumed_attempts` / `remaining_attempts` / `attempts`，全部来自服务端依据
//     持久化记录计算的 `domain.DecideRepairBudget`，不是前端推断。
//
// `can_open_cycle` 为 true 只表示预算作用域可以登记：它不代表可以执行开发、
// 验证已经通过或任务即将完成。该结论只描述读取时刻的现场，写入口仍会重新校验。
type RepairCycleEligibilityResponse struct {
	TaskID     string `json:"task_id"`
	StepID     string `json:"step_id"`
	TaskStatus string `json:"task_status"`
	StepStatus string `json:"step_status"`
	// CanOpenCycle 表示当前读取到的服务端事实是否允许显式开启新的执行周期。
	CanOpenCycle bool `json:"can_open_cycle"`
	// Blocking 是稳定阻塞类别；允许时为空数组。
	Blocking []string `json:"blocking"`
	// Summary 是面向用户的准确说明。
	Summary string `json:"summary"`
	// ActiveCycle 表示是否存在活动执行周期。
	ActiveCycle bool `json:"active_cycle"`
	// CycleSequence 是判定所用周期的序号；没有任何周期登记时为 0。
	CycleSequence int `json:"cycle_sequence"`
	// MaxAttempts 是该周期生效的尝试次数上限（含首轮）。
	MaxAttempts int `json:"max_attempts"`
	// ConsumedAttempts 是该周期已消耗的尝试数（含首轮）。
	ConsumedAttempts int `json:"consumed_attempts"`
	// RemainingAttempts 是该周期剩余可用尝试数。
	RemainingAttempts int `json:"remaining_attempts"`
	// Attempts 是该步骤的历史尝试总数。
	Attempts int `json:"attempts"`
}

// serveRepairCycleEligibility 处理
// GET /api/v1/tasks/{taskID}/steps/{stepID}/repair-cycle-eligibility。
//
// 只读：不登记周期、不结束周期、不写事件、不改任务或步骤状态、不领取任务租约、
// 不启动 Agent，也不产生任何远程副作用。写操作仍然只有 open-repair-cycle。
func (h *stepExecutionHandler) serveRepairCycleEligibility(
	w http.ResponseWriter,
	r *http.Request,
	taskID, stepID string,
) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(
			w,
			r,
			h.logger,
			http.StatusMethodNotAllowed,
			CodeMethodNotAllowed,
			"该接口只支持 GET 请求",
		)
		return
	}
	if h.workflow == nil {
		writeError(
			w,
			r,
			h.logger,
			http.StatusServiceUnavailable,
			CodeInternal,
			"步骤开发—验证闭环服务未初始化",
		)
		return
	}
	result, err := h.workflow.RepairCycleEligibility(r.Context(), app.RepairCycleEligibilityRequest{
		TaskID: taskID,
		StepID: stepID,
	})
	if err != nil {
		// 读取失败与内部异常走统一错误映射：资格未知不能被读成"不允许开启"。
		h.writeWorkflowError(w, r, app.ExecuteAndValidateResult{}, err)
		return
	}
	blocking := make([]string, 0, len(result.Blocking))
	for _, blocker := range result.Blocking {
		blocking = append(blocking, string(blocker))
	}
	writeJSON(w, r, h.logger, http.StatusOK, RepairCycleEligibilityResponse{
		TaskID:            result.TaskID.String(),
		StepID:            result.StepID.String(),
		TaskStatus:        string(result.TaskStatus),
		StepStatus:        string(result.StepStatus),
		CanOpenCycle:      result.CanOpenCycle,
		Blocking:          blocking,
		Summary:           result.Summary,
		ActiveCycle:       result.ActiveCycle,
		CycleSequence:     result.CycleSequence,
		MaxAttempts:       result.MaxAttempts,
		ConsumedAttempts:  result.ConsumedAttempts,
		RemainingAttempts: result.RemainingAttempts,
		Attempts:          result.Attempts,
	})
}

// parseRepairCycleEligibilityPath 判断路径是否指向执行周期资格查询。
//
// 只有 {taskID}/steps/{stepID}/repair-cycle-eligibility 这一种形状成立；其余形状
// 返回 false，由执行记录查询或任务处理器给出各自的 404 / 405 语义。
func parseRepairCycleEligibilityPath(path string) (taskID, stepID string, ok bool) {
	remainder := strings.TrimPrefix(path, taskPathPrefix)
	if remainder == path {
		return "", "", false
	}
	segments := splitPathSegments(remainder)
	if len(segments) != 4 {
		return "", "", false
	}
	if segments[1] != stepsSegment || segments[3] != repairCycleEligibilitySegment {
		return "", "", false
	}
	if !validTaskPathID(segments[0]) || !validTaskPathID(segments[2]) {
		return "", "", false
	}
	return segments[0], segments[2], true
}
