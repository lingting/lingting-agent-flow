package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
)

// openRepairCycleSegment 是"显式开启执行周期"动作的路径段。
const openRepairCycleSegment = "open-repair-cycle"

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
