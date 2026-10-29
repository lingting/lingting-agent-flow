package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// 历史查询相关的路径段。
const (
	// eventsSegment 是任务下事件时间线的路径段。
	eventsSegment = "events"
	// repairCyclesSegment 是步骤下修复周期历史的路径段。
	repairCyclesSegment = "repair-cycles"
)

// EventResponse 是一条事件的对外表示。
//
// `payload` 不出现在响应中：原始负载由各写入点自行拼装，历史数据里出现过内部
// 工作目录、容器标识与错误输出片段。对外只提供经审查的摘要文本；没有可公开
// 内容时 `summary` 缺省，由前端显示中性占位符。
type EventResponse struct {
	// ID 是数据库自增标识，在同一任务内单调递增，用于前端列表的稳定 key。
	ID         int64     `json:"id"`
	TaskID     string    `json:"task_id"`
	StepID     string    `json:"step_id,omitempty"`
	AttemptID  string    `json:"attempt_id,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
	EventType  string    `json:"event_type"`
	EventLabel string    `json:"event_label,omitempty"`
	TypeKnown  bool      `json:"type_known"`
	Summary    string    `json:"summary,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// EventTimelineResponse 是任务事件时间线的分页响应。
//
// 分页使用 `size` / `offset` 而不是页号：事件按时间顺序追加，位置语义比页号更
// 稳定，服务端也只需要一次偏移查询。`total` 始终是满足筛选条件的真实总数，
// 前端据此判断是否还有后续记录，不用当前页条数推算。
type EventTimelineResponse struct {
	TaskID  string          `json:"task_id"`
	Records []EventResponse `json:"records"`
	Total   int             `json:"total"`
	Size    int             `json:"size"`
	Offset  int             `json:"offset"`
	// LatestEventID 是本次读取时刻该任务最新的已持久化事件标识，没有事件时为 0。
	//
	// 它只在事件实时流上使用：调用方把它当作流的起点，就覆盖了"历史查询"与
	// "建立流连接"之间写入的事件，不必在两者之间留一个靠猜的等待窗口。
	LatestEventID int64 `json:"latest_event_id"`
}

// RepairCycleResponse 是一个修复周期的对外表示。
//
// `closed_at` 缺省表示"该周期还没有记录结束时间"，它不表示周期成功、失败或仍在
// 执行：结束时间只来自持久化记录，前端必须显示中性占位符。
type RepairCycleResponse struct {
	ID          string     `json:"id"`
	TaskID      string     `json:"task_id"`
	StepID      string     `json:"step_id"`
	Sequence    int        `json:"sequence"`
	MaxAttempts int        `json:"max_attempts"`
	OpenedBy    string     `json:"opened_by"`
	OpenedAt    time.Time  `json:"opened_at"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
}

// RepairCycleHistoryResponse 是某步骤修复周期历史的响应。
//
// 始终返回全部已登记的周期（按序号升序），不分页：周期数量只由显式开启动作
// 产生，天然很小，分页只会让"到底开过几次"更难读。
type RepairCycleHistoryResponse struct {
	TaskID  string                `json:"task_id"`
	StepID  string                `json:"step_id"`
	Records []RepairCycleResponse `json:"records"`
	Total   int                   `json:"total"`
}

// historyHandler 处理事件时间线与修复周期历史的只读查询。
type historyHandler struct {
	history *app.HistoryQueryService
	logger  *slog.Logger
}

// newHistoryHandler 构造历史查询处理器。
func newHistoryHandler(history *app.HistoryQueryService, logger *slog.Logger) *historyHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &historyHandler{history: history, logger: logger}
}

// listEvents 处理 GET /api/v1/tasks/{taskID}/events。
func (h *historyHandler) listEvents(w http.ResponseWriter, r *http.Request, taskID string) {
	if !h.allowGet(w, r) {
		return
	}
	request, err := parseEventTimelineQuery(r)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	page, err := h.history.ListTaskEvents(r.Context(), taskID, request)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	records := make([]EventResponse, 0, len(page.Events))
	for _, summary := range page.Events {
		records = append(records, eventResponse(summary))
	}
	writeJSON(w, r, h.logger, http.StatusOK, EventTimelineResponse{
		TaskID:        strings.TrimSpace(taskID),
		Records:       records,
		Total:         page.Total,
		Size:          page.Limit,
		Offset:        page.Offset,
		LatestEventID: page.LatestEventID,
	})
}

// listRepairCycles 处理 GET /api/v1/tasks/{taskID}/steps/{stepID}/repair-cycles。
func (h *historyHandler) listRepairCycles(
	w http.ResponseWriter,
	r *http.Request,
	taskID, stepID string,
) {
	if !h.allowGet(w, r) {
		return
	}
	history, err := h.history.ListStepRepairCycles(r.Context(), taskID, stepID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	records := make([]RepairCycleResponse, 0, len(history.Cycles))
	for _, cycle := range history.Cycles {
		records = append(records, repairCycleResponse(cycle))
	}
	writeJSON(w, r, h.logger, http.StatusOK, RepairCycleHistoryResponse{
		TaskID:  history.TaskID.String(),
		StepID:  history.StepID.String(),
		Records: records,
		Total:   len(records),
	})
}

// allowGet 校验请求方法，不满足时写出 405 并返回 false。
func (h *historyHandler) allowGet(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	writeError(
		w,
		r,
		h.logger,
		http.StatusMethodNotAllowed,
		CodeMethodNotAllowed,
		"该接口只支持 GET 请求",
	)
	return false
}

// writeServiceError 把应用层错误映射为统一错误响应。
//
// 参数错误 400、目标不存在或归属不符 404、其他 500；内部错误只写日志，不把底层
// 细节返回给调用方。
func (h *historyHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := CodeInternal
	switch {
	case errors.Is(err, app.ErrHistoryInvalidInput):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
	case errors.Is(err, app.ErrHistoryNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
	}
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError {
		h.logger.ErrorContext(r.Context(), "历史查询接口处理失败", slog.String("error", message))
		message = "服务内部错误"
	}
	writeError(w, r, h.logger, status, code, message)
}

// parseEventTimelineQuery 解析事件时间线的筛选与分页参数。
//
// 只支持 `type`、`size` 与 `offset`：时间范围、按会话筛选等能力当前没有真实
// 使用场景，引入它们只会增加无用的查询复杂度。
func parseEventTimelineQuery(r *http.Request) (app.EventTimelineRequest, error) {
	query := r.URL.Query()
	request := app.EventTimelineRequest{Limit: app.DefaultTaskPageSize}
	// type 支持重复参数与逗号分隔两种写法，与任务列表的 status 一致。
	for _, value := range query["type"] {
		for _, item := range strings.Split(value, ",") {
			trimmed := strings.TrimSpace(item)
			if trimmed == "" {
				continue
			}
			request.Types = append(request.Types, trimmed)
		}
	}
	if raw := strings.TrimSpace(query.Get("size")); raw != "" {
		size, err := strconv.Atoi(raw)
		if err != nil || size <= 0 {
			return app.EventTimelineRequest{}, errors.Join(
				app.ErrHistoryInvalidInput,
				errors.New("分页参数 size 必须是大于 0 的整数"),
			)
		}
		request.Limit = size
	}
	if raw := strings.TrimSpace(query.Get("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return app.EventTimelineRequest{}, errors.Join(
				app.ErrHistoryInvalidInput,
				errors.New("分页参数 offset 必须是不小于 0 的整数"),
			)
		}
		request.Offset = offset
	}
	return request, nil
}

// eventResponse 把一条事件摘要转换为对外表示。
func eventResponse(summary app.EventSummary) EventResponse {
	event := summary.Event
	response := EventResponse{
		ID:         event.ID,
		TaskID:     event.TaskID.String(),
		EventType:  string(event.Type),
		EventLabel: summary.Label,
		TypeKnown:  summary.KnownType,
		Summary:    summary.Summary,
		CreatedAt:  event.CreatedAt,
	}
	if event.StepID != nil && !event.StepID.IsEmpty() {
		response.StepID = event.StepID.String()
	}
	if event.AttemptID != nil && !event.AttemptID.IsEmpty() {
		response.AttemptID = event.AttemptID.String()
	}
	if event.SessionID != nil && !event.SessionID.IsEmpty() {
		response.SessionID = event.SessionID.String()
	}
	return response
}

// repairCycleResponse 把一个修复周期转换为对外表示。
func repairCycleResponse(cycle domain.RepairCycle) RepairCycleResponse {
	return RepairCycleResponse{
		ID:          cycle.ID.String(),
		TaskID:      cycle.TaskID.String(),
		StepID:      cycle.StepID.String(),
		Sequence:    cycle.Sequence,
		MaxAttempts: cycle.MaxAttempts,
		OpenedBy:    cycle.OpenedBy,
		OpenedAt:    cycle.OpenedAt,
		ClosedAt:    cycle.ClosedAt,
	}
}

// parseTaskEventsPath 判断路径是否指向任务事件时间线。
func parseTaskEventsPath(path string) (taskID string, ok bool) {
	remainder := strings.TrimPrefix(path, taskPathPrefix)
	if remainder == path {
		return "", false
	}
	segments := splitPathSegments(remainder)
	if len(segments) != 2 || segments[1] != eventsSegment {
		return "", false
	}
	if !validTaskPathID(segments[0]) {
		return "", false
	}
	return segments[0], true
}

// parseRepairCyclesPath 判断路径是否指向步骤修复周期历史。
func parseRepairCyclesPath(path string) (taskID, stepID string, ok bool) {
	remainder := strings.TrimPrefix(path, taskPathPrefix)
	if remainder == path {
		return "", "", false
	}
	segments := splitPathSegments(remainder)
	if len(segments) != 4 {
		return "", "", false
	}
	if segments[1] != stepsSegment || segments[3] != repairCyclesSegment {
		return "", "", false
	}
	if !validTaskPathID(segments[0]) || !validTaskPathID(segments[2]) {
		return "", "", false
	}
	return segments[0], segments[2], true
}

// serveTaskEvents 分发任务事件时间线请求。
//
// 历史查询服务为空时返回 503，而不是伪造空列表：空历史与"查询服务未装配"
// 是两件完全不同的事实。
func (rt *router) serveTaskEvents(w http.ResponseWriter, r *http.Request, taskID string) {
	if rt.history == nil || rt.history.history == nil {
		writeError(
			w,
			r,
			rt.logger,
			http.StatusServiceUnavailable,
			CodeInternal,
			"历史查询服务未初始化",
		)
		return
	}
	rt.history.listEvents(w, r, taskID)
}

// serveStepRepairCycles 分发步骤修复周期历史请求。
func (rt *router) serveStepRepairCycles(w http.ResponseWriter, r *http.Request, taskID, stepID string) {
	if rt.history == nil || rt.history.history == nil {
		writeError(
			w,
			r,
			rt.logger,
			http.StatusServiceUnavailable,
			CodeInternal,
			"历史查询服务未初始化",
		)
		return
	}
	rt.history.listRepairCycles(w, r, taskID, stepID)
}
