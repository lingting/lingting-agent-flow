package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
)

// 实时事件流的协议常量。
const (
	// eventStreamSegment 是任务事件实时流的路径段。
	eventStreamSegment = "stream"
	// lastEventIDHeader 是 SSE 标准的断线恢复游标请求头。
	//
	// 浏览器自动重连时会回传上一次收到的 `id`；本项目自己管理重连（连接前先确定
	// 游标），因此同时接受查询参数 `last_event_id`，两者语义相同。
	lastEventIDHeader = "Last-Event-ID"
	// lastEventIDQuery 是显式传入游标的查询参数名。
	lastEventIDQuery = "last_event_id"
	// eventStreamPollInterval 是增量读取已持久化事件的周期。
	//
	// 它只决定"新事件最晚多久送达"，不决定事件是否丢失：每轮都按游标读取，
	// 因此周期与丢失无关，只与延迟有关。取值偏保守，避免为一个只读通道制造负载。
	eventStreamPollInterval = time.Second
	// eventStreamHeartbeatInterval 是空闲心跳注释的间隔。
	//
	// 心跳不携带业务数据，只让中间层知道连接仍然活着；缺少心跳时，空闲连接会被
	// 代理或操作系统静默切断，客户端只能看到连接消失而无法判断原因。
	eventStreamHeartbeatInterval = 15 * time.Second
	// eventStreamHeartbeatFrame 是心跳帧内容（SSE 注释行）。
	eventStreamHeartbeatFrame = ": ping\n\n"
)

// eventStreamHandler 提供任务事件的只读实时流。
//
// 它只推送已经持久化的事件：读取入口是 `app.HistoryQueryService`，对外字段复用
// `eventResponse`，因此原始 payload、内部路径与凭据仍按历史查询的安全边界处理。
// 它不追加事件、不改变执行结果，也不参与任何状态迁移。
type eventStreamHandler struct {
	history *app.HistoryQueryService
	logger  *slog.Logger
	// shutdown 在 HTTP 服务开始关闭时被关闭，用于让推送循环及时退出。
	//
	// 为 nil 表示没有关闭信号（例如单元测试直接构造处理器），此时只依赖请求上下
	// 文取消；select 上的 nil 通道永远不就绪，语义与"没有该信号"一致。
	shutdown <-chan struct{}
}

// newEventStreamHandler 构造实时事件流处理器。
func newEventStreamHandler(
	history *app.HistoryQueryService,
	logger *slog.Logger,
	shutdown <-chan struct{},
) *eventStreamHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &eventStreamHandler{history: history, logger: logger, shutdown: shutdown}
}

// parseTaskEventStreamPath 判断路径是否指向任务事件实时流。
//
// 判定按路径段数量与段名进行，不使用前缀匹配：`{taskID}/events` 的历史查询仍由
// `parseTaskEventsPath` 处理，两个形状互不影响。
func parseTaskEventStreamPath(path string) (taskID string, ok bool) {
	remainder := strings.TrimPrefix(path, taskPathPrefix)
	if remainder == path {
		return "", false
	}
	segments := splitPathSegments(remainder)
	if len(segments) != 3 || segments[1] != eventsSegment || segments[2] != eventStreamSegment {
		return "", false
	}
	if !validTaskPathID(segments[0]) {
		return "", false
	}
	return segments[0], true
}

// parseEventStreamCursor 解析断线恢复游标。
//
// 优先级为 Last-Event-ID 请求头、`last_event_id` 查询参数：前者是浏览器自动重连
// 使用的标准位置，后者用于显式指定起点。两者都缺省时返回 hasCursor=false，由调用
// 方按"从当前最新事件开始"处理。
func parseEventStreamCursor(r *http.Request) (cursor int64, hasCursor bool, err error) {
	raw := strings.TrimSpace(r.Header.Get(lastEventIDHeader))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get(lastEventIDQuery))
	}
	if raw == "" {
		return 0, false, nil
	}
	parsed, parseErr := strconv.ParseInt(raw, 10, 64)
	if parseErr != nil || parsed < 0 {
		return 0, false, fmt.Errorf("事件游标（%s 或 %s）必须是不小于 0 的整数", lastEventIDHeader, lastEventIDQuery)
	}
	return parsed, true, nil
}

// serveTaskEventStream 分发任务事件实时流请求。
func (rt *router) serveTaskEventStream(w http.ResponseWriter, r *http.Request, taskID string) {
	if rt.eventStream == nil || rt.eventStream.history == nil {
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
	rt.eventStream.streamTaskEvents(w, r, taskID)
}

// streamTaskEvents 处理 GET /api/v1/tasks/{taskID}/events/stream。
//
// 响应头一旦写出就无法再改成普通 JSON 错误响应，因此全部可以判定的错误（方法、
// 流式能力、游标、任务归属）都在写响应头之前处理；流开始之后的错误只结束连接并
// 记录服务端诊断信息。
func (h *eventStreamHandler) streamTaskEvents(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(
			w,
			r,
			h.logger,
			http.StatusMethodNotAllowed,
			CodeMethodNotAllowed,
			"事件流只支持 GET 请求",
		)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		// 不支持流式刷新的连接无法提供 SSE：宁可直接拒绝，也不要让调用方收到
		// 一堆被缓冲到连接结束才出现的事件。
		writeError(
			w,
			r,
			h.logger,
			http.StatusInternalServerError,
			CodeInternal,
			"当前连接不支持流式响应",
		)
		return
	}
	cursor, hasCursor, err := parseEventStreamCursor(r)
	if err != nil {
		writeError(w, r, h.logger, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}
	// 归属校验与起点校验都必须在写响应头之前完成。
	latest, err := h.history.LatestTaskEventID(r.Context(), taskID)
	if err != nil {
		h.writeStreamOpenError(w, r, err)
		return
	}
	if !hasCursor {
		// 没有游标表示"只推送从现在开始写入的事件"：整份历史属于分页查询的职责，
		// 在这里重放一次只会让调用方拿到重复数据。
		cursor = latest
	} else if cursor > latest {
		// 超过该任务最新事件标识的游标不可能来自本任务的真实事件：拒绝而不是静默
		// 等待，否则客户端的一个错误会表现成"连接正常但永远没有事件"。
		writeError(
			w,
			r,
			h.logger,
			http.StatusBadRequest,
			CodeInvalidRequest,
			"事件游标超出该任务当前最新的已持久化事件",
		)
		return
	}
	writeEventStreamHeaders(w.Header())
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	h.pumpStream(r.Context(), w, flusher, taskID, cursor)
}

// writeEventStreamHeaders 写出 SSE 需要的响应头。
func writeEventStreamHeaders(header http.Header) {
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	// 事件流不能被任何中间层缓存或转换（压缩/transfer 重编码会破坏增量语义）。
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	// 反向代理（例如 nginx）默认会缓冲响应体，显式关闭以免事件被攒着不发。
	header.Set("X-Accel-Buffering", "no")
}

// pumpStream 按固定周期把游标之后的事件推送给调用方，直到底层连接或服务结束。
func (h *eventStreamHandler) pumpStream(
	ctx context.Context,
	w io.Writer,
	flusher http.Flusher,
	taskID string,
	cursor int64,
) {
	h.logger.InfoContext(
		ctx,
		"事件流已建立",
		slog.String("task_id", taskID),
		slog.Int64("after_event_id", cursor),
	)
	poll := time.NewTicker(eventStreamPollInterval)
	defer poll.Stop()
	heartbeat := time.NewTicker(eventStreamHeartbeatInterval)
	defer heartbeat.Stop()

	// 连接建立后立即补发一次：历史查询与流建立之间的时间差已经被游标覆盖，这里
	// 只是不让这批事件等到第一个周期才出现。
	next, err := h.writePendingEvents(ctx, w, flusher, taskID, cursor)
	if err != nil {
		h.logStreamFailure(ctx, taskID, err)
		return
	}
	cursor = next

	for {
		select {
		case <-ctx.Done():
			// 客户端断开或被取消：请求上下文已结束，日志仍然需要写出去。
			h.logger.InfoContext(
				context.WithoutCancel(ctx),
				"事件流结束",
				slog.String("task_id", taskID),
				slog.String("reason", "client_closed"),
				slog.Int64("cursor", cursor),
			)
			return
		case <-h.shutdown:
			h.logger.InfoContext(
				context.WithoutCancel(ctx),
				"事件流结束",
				slog.String("task_id", taskID),
				slog.String("reason", "server_shutdown"),
				slog.Int64("cursor", cursor),
			)
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(w, eventStreamHeartbeatFrame); err != nil {
				h.logStreamFailure(ctx, taskID, err)
				return
			}
			flusher.Flush()
		case <-poll.C:
			next, err := h.writePendingEvents(ctx, w, flusher, taskID, cursor)
			if err != nil {
				h.logStreamFailure(ctx, taskID, err)
				return
			}
			cursor = next
		}
	}
}

// writePendingEvents 连续读取并写出游标之后的全部事件，返回新的游标。
//
// 读取到空批或不足一批即停止：积压会被连续取完，不必等下一个周期。游标只在整批
// 全部写出成功后推进，写失败时保留原游标并结束连接，避免把未送达的事件当成已送达。
func (h *eventStreamHandler) writePendingEvents(
	ctx context.Context,
	w io.Writer,
	flusher http.Flusher,
	taskID string,
	cursor int64,
) (int64, error) {
	for {
		if err := ctx.Err(); err != nil {
			return cursor, err
		}
		batch, err := h.history.ListTaskEventsAfter(ctx, taskID, app.TaskEventStreamRequest{
			AfterID: cursor,
		})
		if err != nil {
			return cursor, err
		}
		if len(batch.Events) == 0 {
			return cursor, nil
		}
		for _, summary := range batch.Events {
			payload, err := json.Marshal(eventResponse(summary))
			if err != nil {
				return cursor, fmt.Errorf("编码事件 %d 失败: %w", summary.Event.ID, err)
			}
			if err := writeEventStreamEvent(w, summary.Event.ID, payload); err != nil {
				return cursor, err
			}
		}
		flusher.Flush()
		cursor = batch.Cursor
		if len(batch.Events) < app.DefaultEventStreamBatchSize {
			return cursor, nil
		}
	}
}

// writeEventStreamEvent 写出一条 SSE 消息帧。
//
// 帧里只有协议字段：`id` 是真实事件标识（浏览器重连时回传 Last-Event-ID），`data`
// 是经审查的事件 JSON。刻意不使用 `event:` 字段——调用方按默认的 message 监听，
// 因此不需要为推送额外定义一套事件类型名。
func writeEventStreamEvent(w io.Writer, eventID int64, payload []byte) error {
	if _, err := fmt.Fprintf(w, "id: %d\n", eventID); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "data: %s\n\n", payload)
	return err
}

// writeStreamOpenError 把开始流之前的应用层错误映射为普通 JSON 错误响应。
func (h *eventStreamHandler) writeStreamOpenError(w http.ResponseWriter, r *http.Request, err error) {
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
		h.logger.ErrorContext(r.Context(), "事件流建立失败", slog.String("error", message))
		message = "服务内部错误"
	}
	writeError(w, r, h.logger, status, code, message)
}

// logStreamFailure 记录流开始之后的内部错误。
//
// 此时响应头已经写出，不能再写普通 JSON 响应：只记录诊断信息并结束连接，让客户端
// 按 SSE 语义重连（重连会带上游标，未送达的事件仍然会被补发）。
func (h *eventStreamHandler) logStreamFailure(ctx context.Context, taskID string, err error) {
	if ctx.Err() != nil {
		// 连接已被调用方关闭：写失败是预期结果，不记为服务端内部错误。
		h.logger.InfoContext(
			context.WithoutCancel(ctx),
			"事件流结束",
			slog.String("task_id", taskID),
			slog.String("reason", "client_closed"),
		)
		return
	}
	h.logger.ErrorContext(
		context.WithoutCancel(ctx),
		"事件流因内部错误中断",
		slog.String("task_id", taskID),
		slog.String("error", err.Error()),
	)
}
