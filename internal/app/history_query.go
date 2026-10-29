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

// 事件时间线与修复周期历史查询的错误类别。
//
// 与执行记录查询一致：Handler 通过 errors.Is 判断类别并映射 HTTP 状态码，不解析
// 错误文本；"目标不属于指定任务"与"目标不存在"返回同一类别，避免用状态码差异
// 探测其他任务的数据。
var (
	// ErrHistoryNotFound 表示任务不存在（或已逻辑删除）、步骤不存在，或步骤不属于该任务。
	ErrHistoryNotFound = errors.New("app: 历史记录不存在")
	// ErrHistoryInvalidInput 表示分页参数不合法。
	ErrHistoryInvalidInput = errors.New("app: 历史查询参数非法")
)

// 事件摘要的长度上限。
//
// 摘要会进入 HTTP 响应并被浏览器渲染，因此必须有明确上限：完整负载既不安全
// （历史上写入了内部工作目录、容器标识与命令输出片段），也不适合一次性展示。
const (
	// MaxEventSummaryLength 是单条事件摘要保留的字符数上限。
	MaxEventSummaryLength = 512
	// MaxEventPayloadBytes 是参与摘要提取的负载字节上限。
	//
	// 历史事件可能写入超长字段，读取时先按该上限截断再脱敏，避免把整段输出
	// 载入内存后才发现无法展示。
	MaxEventPayloadBytes = 4096
)

// 实时事件流的增量读取上限。
const (
	// DefaultEventStreamBatchSize 是单次增量读取的事件条数。
	//
	// 它只影响一次数据库往返取多少行，不影响协议语义：读取方按游标继续读直到
	// 没有新事件为止，因此积压会被连续取完，而不是被固定周期切成慢速增长。
	DefaultEventStreamBatchSize = 100
	// MaxEventStreamBatchSize 是调用方可指定的最大增量条数，与历史分页上限一致。
	MaxEventStreamBatchSize = MaxTaskPageSize
)

// EventSummary 是一条事件的对外安全表示。
//
// 它只包含经过审查的稳定字段：事件类型、时间、关联标识与该事件可公开的摘要。
// 原始 payload 不在其中——payload 的结构由各写入点自行决定，且历史上出现过
// 内部路径与错误输出，不能无差别输出到 Web。
type EventSummary struct {
	// Event 是原始事件记录，Payload 已被剥离。
	Event domain.Event
	// Label 是事件类型的中文说明；未知类型为空，由调用方原样显示类型名。
	Label string
	// Summary 是脱敏并截断后的事件摘要；没有可公开内容时为空。
	Summary string
	// KnownType 表示事件类型是否属于当前版本定义的类型。
	KnownType bool
}

// EventTimelinePage 是一页事件时间线。
type EventTimelinePage struct {
	// Events 按 (created_at, id) 升序排列，与存储层顺序一致。
	Events []EventSummary
	// Total 是满足筛选条件的事件总数。
	Total int
	// LatestEventID 是本次读取时刻该任务最新的自增事件标识，没有事件时为 0。
	//
	// 它是实时推送的起点依据：客户端把历史读取时刻拿到的该值作为游标建立事件流，
	// 就能覆盖"历史查询与流连接之间"写入的事件，不留下静默丢事件的时间窗口。
	LatestEventID int64
	// Limit 与 Offset 是本次生效的分页取值，用于响应回显。
	Limit  int
	Offset int
}

// EventTimelineRequest 是事件时间线的查询参数。
type EventTimelineRequest struct {
	// Types 按事件类型筛选，为空表示不限制。
	//
	// 不校验类型是否属于当前版本：历史事件的类型必须仍可被查询。
	Types []string
	// Limit 是页大小；小于等于 0 使用默认值，超过上限被拒绝。
	Limit int
	// Offset 是跳过的记录条数，小于 0 视为 0。
	Offset int
}

// TaskEventStreamRequest 是实时事件流的增量读取参数。
type TaskEventStreamRequest struct {
	// AfterID 是已经推送到的事件标识，只读取严格大于它的记录。
	//
	// 0 表示"从该任务最早的事件开始"；负数不是合法游标，返回参数错误而不是被
	// 静默当成 0——把非法游标当 0 会让客户端收到重复的历史并在错误的方向上漂移。
	AfterID int64
	// Limit 是单次读取条数；小于等于 0 使用默认值，超过上限被拒绝。
	Limit int
}

// TaskEventStreamBatch 是一次增量读取的结果。
type TaskEventStreamBatch struct {
	// Events 按 id 升序排列，与游标所属的单调序列一致。
	Events []EventSummary
	// Cursor 是本批最后一条事件的标识；没有新事件时等于请求的 AfterID。
	//
	// 调用方只能在确认本批全部写出成功后用它推进游标，否则未写出的事件会被跳过。
	Cursor int64
}

// RepairCycleHistory 是某个步骤的全部修复周期登记。
type RepairCycleHistory struct {
	// TaskID / StepID 是本次查询的归属，供调用方核对。
	TaskID domain.ID
	StepID domain.ID
	// Cycles 按周期序号升序排列。
	Cycles []domain.RepairCycle
}

// HistoryQueryService 提供事件时间线与修复周期历史的只读查询。
//
// 该服务不追加事件、不开辟周期、不推进任何状态：它只读取已经持久化的事实，
// 因此可以在任何时候安全地重复调用。
type HistoryQueryService struct {
	store storage.Store
}

// NewHistoryQueryService 构造历史查询服务。
func NewHistoryQueryService(store storage.Store) (*HistoryQueryService, error) {
	if store == nil {
		return nil, fmt.Errorf("历史查询服务初始化失败: 存储不能为空")
	}
	return &HistoryQueryService{store: store}, nil
}

// ListTaskEvents 返回指定任务的事件时间线分页。
//
// 任务不存在或已逻辑删除时返回 ErrHistoryNotFound：返回空列表会让调用方无法
// 区分"任务没有事件"与"任务不存在"。空列表只表示该任务没有匹配的事件记录，
// 不能据此推断任务从未执行。
func (s *HistoryQueryService) ListTaskEvents(
	ctx context.Context,
	taskID string,
	request EventTimelineRequest,
) (EventTimelinePage, error) {
	id, err := requireHistoryTask(ctx, s.store, taskID)
	if err != nil {
		return EventTimelinePage{}, err
	}
	limit, offset, err := normalizeHistoryPaging(request.Limit, request.Offset)
	if err != nil {
		return EventTimelinePage{}, err
	}
	filter := storage.EventFilter{Limit: limit, Offset: offset}
	for _, raw := range request.Types {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		filter.Types = append(filter.Types, domain.EventType(trimmed))
	}
	// 先统计再取页：两个查询都只读，因此顺序不影响正确性；总数用于分页元数据。
	total, err := s.store.CountEventsByTask(ctx, id, filter)
	if err != nil {
		return EventTimelinePage{}, fmt.Errorf("统计任务事件失败: %w", err)
	}
	// 最新事件标识与分页同批读取：客户端用它作为实时流的起点，两次读取之间的
	// 时间差正是"切换窗口"，这里不给它额外扩大的机会。
	latest, err := s.store.LatestEventIDByTask(ctx, id)
	if err != nil {
		return EventTimelinePage{}, fmt.Errorf("读取最新事件标识失败: %w", err)
	}
	events, err := s.store.ListEventsByTask(ctx, id, filter)
	if err != nil {
		return EventTimelinePage{}, fmt.Errorf("查询任务事件失败: %w", err)
	}
	page := EventTimelinePage{
		Events:        make([]EventSummary, 0, len(events)),
		LatestEventID: latest,
		Limit:         limit,
		Offset:        offset,
		Total:         total,
	}
	for _, event := range events {
		page.Events = append(page.Events, describeEvent(event))
	}
	return page, nil
}

// ListTaskEventsAfter 返回指定任务中游标之后写入的事件。
//
// 它是实时推送的唯一读取入口：只做一次按 (task_id, id) 索引的范围查询，不统计
// 总数、不使用偏移量，因此单次成本不随该任务的历史长度增长。返回的事件同样经过
// `describeEvent` 处理——推送与历史必须共用同一套安全摘要，否则实时通道会成为
// 绕过脱敏的后门。
//
// 任务不存在或已逻辑删除返回 ErrHistoryNotFound，游标非法返回 ErrHistoryInvalidInput。
func (s *HistoryQueryService) ListTaskEventsAfter(
	ctx context.Context,
	taskID string,
	request TaskEventStreamRequest,
) (TaskEventStreamBatch, error) {
	id, err := requireHistoryTask(ctx, s.store, taskID)
	if err != nil {
		return TaskEventStreamBatch{}, err
	}
	if request.AfterID < 0 {
		return TaskEventStreamBatch{}, fmt.Errorf(
			"%w: 事件游标不能为负数",
			ErrHistoryInvalidInput,
		)
	}
	limit := request.Limit
	if limit <= 0 {
		limit = DefaultEventStreamBatchSize
	}
	if limit > MaxEventStreamBatchSize {
		return TaskEventStreamBatch{}, fmt.Errorf(
			"%w: 增量读取条数不能超过 %d",
			ErrHistoryInvalidInput,
			MaxEventStreamBatchSize,
		)
	}
	events, err := s.store.ListEventsByTaskAfterID(ctx, id, request.AfterID, limit)
	if err != nil {
		return TaskEventStreamBatch{}, fmt.Errorf("查询增量事件失败: %w", err)
	}
	batch := TaskEventStreamBatch{
		Events: make([]EventSummary, 0, len(events)),
		Cursor: request.AfterID,
	}
	for _, event := range events {
		batch.Events = append(batch.Events, describeEvent(event))
		batch.Cursor = event.ID
	}
	return batch, nil
}

// LatestTaskEventID 返回指定任务当前最新的自增事件标识，没有事件时为 0。
//
// 除了给出实时流的默认起点，它还是"任务是否存在且未删除"的一次廉价校验：流一旦
// 开始就无法再写普通 JSON 错误响应，所以归属校验必须发生在写响应头之前。
func (s *HistoryQueryService) LatestTaskEventID(ctx context.Context, taskID string) (int64, error) {
	id, err := requireHistoryTask(ctx, s.store, taskID)
	if err != nil {
		return 0, err
	}
	latest, err := s.store.LatestEventIDByTask(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("读取最新事件标识失败: %w", err)
	}
	return latest, nil
}

// ListStepRepairCycles 返回指定步骤的修复周期历史。
//
// 归属校验分两步：先确认任务真实存在且未删除，再确认步骤属于该任务。只校验两个
// 标识各自存在会让调用方用 A 任务的路径读到 B 任务的步骤历史。
//
// 查询是纯读取：不开启新周期、不结束活动周期、不创建 Attempt，也不触碰预算判定。
func (s *HistoryQueryService) ListStepRepairCycles(
	ctx context.Context,
	taskID, stepID string,
) (RepairCycleHistory, error) {
	step, err := requireHistoryStep(ctx, s.store, taskID, stepID)
	if err != nil {
		return RepairCycleHistory{}, err
	}
	cycles, err := s.store.ListRepairCyclesByStep(ctx, step.ID)
	if err != nil {
		return RepairCycleHistory{}, fmt.Errorf("查询修复周期历史失败: %w", err)
	}
	return RepairCycleHistory{
		TaskID: step.TaskID,
		StepID: step.ID,
		Cycles: cycles,
	}, nil
}

// requireHistoryTask 读取未被逻辑删除的任务。
func requireHistoryTask(
	ctx context.Context,
	store storage.Store,
	taskID string,
) (domain.ID, error) {
	id, err := parseRecordID(taskID)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrHistoryNotFound, err)
	}
	task, err := store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", fmt.Errorf("%w: 任务 %s", ErrHistoryNotFound, id.String())
		}
		return "", fmt.Errorf("读取任务失败: %w", err)
	}
	if task.Deleted() {
		return "", fmt.Errorf("%w: 任务 %s", ErrHistoryNotFound, id.String())
	}
	return id, nil
}

// requireHistoryStep 读取步骤并确认它属于指定任务。
func requireHistoryStep(
	ctx context.Context,
	store storage.Store,
	taskID, stepID string,
) (domain.Step, error) {
	id, err := requireHistoryTask(ctx, store, taskID)
	if err != nil {
		return domain.Step{}, err
	}
	parsed, err := parseRecordID(stepID)
	if err != nil {
		return domain.Step{}, fmt.Errorf("%w: %w", ErrHistoryNotFound, err)
	}
	step, err := store.GetStep(ctx, parsed)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Step{}, fmt.Errorf("%w: 步骤 %s", ErrHistoryNotFound, parsed.String())
		}
		return domain.Step{}, fmt.Errorf("读取步骤失败: %w", err)
	}
	if step.TaskID != id {
		return domain.Step{}, fmt.Errorf("%w: 步骤 %s", ErrHistoryNotFound, parsed.String())
	}
	return *step, nil
}

// normalizeHistoryPaging 校验并归一化分页参数。
//
// 与任务列表、执行记录共用同一套默认值与上限：超出上限返回参数错误，而不是静默
// 截断或读取全表。
func normalizeHistoryPaging(limit, offset int) (int, int, error) {
	size := limit
	if size <= 0 {
		size = DefaultTaskPageSize
	}
	if size > MaxTaskPageSize {
		return 0, 0, fmt.Errorf(
			"%w: 页大小不能超过 %d",
			ErrHistoryInvalidInput,
			MaxTaskPageSize,
		)
	}
	skip := offset
	if skip < 0 {
		skip = 0
	}
	return size, skip, nil
}

// eventSummaryFields 是参与事件摘要的负载字段，按优先顺序排列。
//
// 白名单而不是黑名单：payload 由各写入点自行拼装，只有在这里显式登记过的、
// 经核查不含路径、命令输出或凭据的字段才会进入 Web。新增字段默认不可见，
// 因此上游"顺手多写一个字段"不会自动变成对外泄露。
var eventSummaryFields = []string{
	"summary",
	"repair_summary",
	"reason",
	"failure_stage",
	"error_class",
	"status",
	"outcome",
	"verdict",
}

// 摘要字段的中文标签；没有标签的字段直接输出取值（例如 summary 本身就是句子）。
var eventSummaryFieldLabels = map[string]string{
	"reason":        "原因",
	"failure_stage": "失败阶段",
	"error_class":   "错误分类",
	"status":        "状态",
	"outcome":       "结论",
	"verdict":       "验证结论",
}

// eventLabelMap 是事件类型的中文说明。
//
// 未知类型不在此表中：调用方必须原样显示类型名，不能统一显示成"未知事件"而让
// 用户失去可区分的信息。
var eventLabelMap = map[domain.EventType]string{
	domain.EventTypeTaskCreated:              "任务创建",
	domain.EventTypeTaskStarted:              "任务开始",
	domain.EventTypePlanCreated:              "计划生成",
	domain.EventTypeStepStarted:              "步骤开始",
	domain.EventTypeStepCompleted:            "步骤完成",
	domain.EventTypeAttemptStarted:           "执行尝试开始",
	domain.EventTypeAttemptFailed:            "执行尝试失败",
	domain.EventTypeAttemptInterrupted:       "执行尝试中断",
	domain.EventTypeSessionStarted:           "Agent 会话开始",
	domain.EventTypeSessionCompleted:         "Agent 会话结束",
	domain.EventTypeVerificationStarted:      "验证开始",
	domain.EventTypeVerificationPassed:       "验证通过",
	domain.EventTypeVerificationFailed:       "验证未通过",
	domain.EventTypeVerificationSkipped:      "验证跳过",
	domain.EventTypeTaskCompleted:            "任务完成",
	domain.EventTypeTaskFailed:               "任务失败",
	domain.EventTypeTaskCancelled:            "任务取消",
	domain.EventTypeTaskCancelRequested:      "收到取消请求",
	domain.EventTypeTaskRecoveryRequired:     "需要人工核验",
	domain.EventTypeTaskRetrying:             "任务重试中",
	domain.EventTypeTaskRetried:              "任务已重试",
	domain.EventTypeTaskClaimed:              "任务领取",
	domain.EventTypeTaskReleased:             "任务租约释放",
	domain.EventTypeRequirementSubmitted:     "需求提交",
	domain.EventTypeContainerCreated:         "容器创建",
	domain.EventTypeContainerCleaned:         "容器清理",
	domain.EventTypeContainerRetained:        "容器保留",
	domain.EventTypeSourceFetched:            "代码获取",
	domain.EventTypeCodeSaved:                "代码保存",
	domain.EventTypeCodeSaveRecoveryRequired: "代码保存需要人工核验",
	domain.EventTypeWorkflowStarted:          "工作流开始",
	domain.EventTypeWorkflowResumed:          "工作流恢复",
	domain.EventTypeWorkflowStepPassed:       "工作流步骤通过",
	domain.EventTypeWorkflowBlocked:          "工作流已停止",
	domain.EventTypeWorkflowCanceled:         "工作流取消",
	domain.EventTypeWorkflowCompleted:        "工作流完成",
	domain.EventTypeWorkflowFailed:           "工作流失败",
	domain.EventTypeWorkflowRecovery:         "工作流需要人工核验",
	domain.EventTypeWorkflowCodeSaved:        "工作流代码保存",
	domain.EventTypeRepairRound:              "修复轮次记录",
	domain.EventTypeRepairCycleOpened:        "开启修复周期",
}

// EventTypeLabel 返回事件类型的中文说明；未登记的类型返回空字符串。
func EventTypeLabel(eventType domain.EventType) string {
	return eventLabelMap[eventType]
}

// describeEvent 把存储层事件转换为安全的对外表示。
//
// 三件事必须同时成立：
//   - 原始 payload 一律不输出，只有白名单字段进入摘要；
//   - 摘要做凭据脱敏并按长度截断，避免把令牌或超长输出带到浏览器；
//   - 未知事件类型保留原始类型名，使其仍可被识别。
func describeEvent(event domain.Event) EventSummary {
	return EventSummary{
		Event:     withoutPayload(event),
		Label:     EventTypeLabel(event.Type),
		KnownType: event.Type.Known(),
		Summary:   eventPayloadSummary(event.Payload),
	}
}

// withoutPayload 返回剥离负载后的事件副本。
func withoutPayload(event domain.Event) domain.Event {
	event.Payload = nil
	return event
}

// eventPayloadSummary 从事件负载中提取可公开的摘要。
//
// 负载不合法、为空或没有任何白名单字段时返回空字符串：调用方必须显示中性
// 占位符，而不是把原始负载当作摘要展示。
func eventPayloadSummary(payload json.RawMessage) string {
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" || trimmed == "{}" {
		return ""
	}
	// 先截断再解析：历史负载可能很长，没必要为了一条摘要解析完整内容。
	if len(trimmed) > MaxEventPayloadBytes {
		trimmed = trimmed[:MaxEventPayloadBytes]
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
		return ""
	}
	parts := make([]string, 0, len(eventSummaryFields))
	for _, name := range eventSummaryFields {
		text := eventFieldText(fields[name])
		if text == "" {
			continue
		}
		if label := eventSummaryFieldLabels[name]; label != "" {
			text = label + " " + text
		}
		parts = append(parts, text)
		if len(parts) >= 3 {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return domain.ClipFeedbackText(strings.Join(parts, "；"), MaxEventSummaryLength)
}

// eventFieldText 把白名单字段的值转换为简短文本；不可展示的类型返回空字符串。
func eventFieldText(value any) string {
	switch typed := value.(type) {
	case string:
		return domain.ClipFeedbackText(typed, MaxEventSummaryLength)
	case bool:
		if typed {
			return "是"
		}
		return "否"
	case float64:
		if typed == float64(int64(typed)) {
			return fmt.Sprintf("%d", int64(typed))
		}
		return fmt.Sprintf("%g", typed)
	default:
		// nil、对象与数组不进入摘要：嵌套结构无法逐字段审查。
		return ""
	}
}
