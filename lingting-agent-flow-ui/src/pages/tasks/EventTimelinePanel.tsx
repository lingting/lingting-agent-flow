/**
 * 任务事件时间线面板（P4-M3-8 / G15，实时区域见 P4 事件实时流）。
 *
 * 只回答"何时发生了什么"：事件时间、中文类型说明、真实关联标识与经审查的摘要。
 * 历史列表不参与 5 秒自动刷新——它只在打开任务、手动刷新与"加载更多"时读取，
 * 避免把整份历史放进刷新循环。
 *
 * 面板同时承载两个互不替代的区域：
 *   - 历史列表：按 `offset` 分页，可能只加载了一部分，条数由服务端 `total` 决定；
 *   - 实时区域：只累积事件推送连接建立之后收到的事件。
 * 两者刻意不合并：把最新几条混进一个不完整的历史列表，会让用户以为中间的历史也
 * 已经显示出来了。实时区域也不代表"任务状态"，阶段结论仍以任务与工作流接口为准。
 *
 * 状态必须可区分：加载中、加载失败（可重试）、空列表、有数据，以及"推送连接断开"
 * 这一独立事实。失败不能被显示成"没有事件"，空列表也不能被解释为"任务从未执行"。
 */
import { Alert, Flex, Space, Tag, Typography } from "antd";
import { LinkButton } from "lingting-react-ui";

import type { TaskEvent } from "@/lib/api";
import {
  eventRefs,
  eventSummaryText,
  eventTypeLabel,
  formatHistoryTime,
  hasMoreEvents,
} from "@/pages/tasks/history";
import type { EventTimelineState } from "@/pages/tasks/useExecutionRecords";
import type { TaskEventStreamState } from "@/pages/tasks/useTaskEventStream";

/** 单条事件行：时间、类型、关联标识与摘要 */
function EventRow({ event }: { event: TaskEvent }) {
  const refs = eventRefs(event);
  return (
    <Flex gap={2} vertical>
      <Space align="center" size={8} wrap>
        <Typography.Text type="secondary" style={{ minWidth: 148 }}>
          {formatHistoryTime(event.created_at)}
        </Typography.Text>
        <Typography.Text strong>{eventTypeLabel(event)}</Typography.Text>
        {!event.type_known && (
          <Tag color="default">该类型未在当前版本定义，原始类型：{event.event_type}</Tag>
        )}
        {refs.stepID && (
          <Typography.Text code type="secondary">
            步骤 {refs.stepID}
          </Typography.Text>
        )}
        {refs.attemptID && (
          <Typography.Text code type="secondary">
            尝试 {refs.attemptID}
          </Typography.Text>
        )}
        {refs.sessionID && (
          <Typography.Text code type="secondary">
            会话 {refs.sessionID}
          </Typography.Text>
        )}
      </Space>
      <Typography.Text type="secondary">{eventSummaryText(event)}</Typography.Text>
    </Flex>
  );
}

/** 实时区域的状态说明文本；只描述推送通道，不描述任务状态 */
function streamStatusLabel(status: TaskEventStreamState["status"]): string {
  switch (status) {
    case "connecting":
      return "正在连接";
    case "live":
      return "已连接";
    case "reconnecting":
      return "正在重连";
    case "error":
      return "已停止";
    default:
      return "未连接";
  }
}

/**
 * 实时事件区域。
 *
 * 它只展示事件推送连接建立之后收到的事件，因此与上方分页历史是两个不相交的部分：
 * 历史列表是否连续由它自己的"已显示 N / M 条"与「加载更多」如实表达，这里不合并、
 * 不重排、也不补齐中间记录。
 */
function LiveEventsSection({
  onReconnect,
  stream,
}: {
  onReconnect: () => void;
  stream: TaskEventStreamState;
}) {
  return (
    <Flex gap={4} vertical>
      <Space align="center" size={8} wrap>
        <Typography.Text strong>实时事件</Typography.Text>
        <Typography.Text type={stream.status === "live" ? "success" : "secondary"}>
          {streamStatusLabel(stream.status)}
        </Typography.Text>
        {stream.events.length > 0 && (
          <Typography.Text type="secondary">连接后新增 {stream.events.length} 条</Typography.Text>
        )}
        {stream.status === "error" && <LinkButton onClick={onReconnect} text="重新连接" />}
      </Space>
      {stream.hint && (
        <Alert
          description={stream.hint}
          showIcon
          title={stream.status === "error" ? "事件推送已停止" : "事件推送连接中断"}
          type={stream.status === "error" ? "warning" : "info"}
        />
      )}
      {stream.status === "live" && stream.events.length === 0 && (
        <Typography.Text type="secondary">
          已连接，暂无新增事件。服务端只推送这条连接建立之后写入的事件。
        </Typography.Text>
      )}
      {stream.events.length > 0 && (
        <Flex gap={6} vertical>
          {stream.events.map((event) => (
            <EventRow event={event} key={event.id} />
          ))}
        </Flex>
      )}
      <Typography.Text type="secondary">
        实时区域只包含这条连接之后的事件；更早的历史请看上方分页列表，两者不会合并显示。
      </Typography.Text>
    </Flex>
  );
}

/**
 * 事件时间线面板。
 *
 * `onReload`、`onLoadMore` 与 `onReconnect` 都只发起只读操作：不会触发执行、验证、
 * 重试、恢复或代码保存。`state` 是历史分页的加载状态，`stream` 是推送通道的状态，
 * 两者分开处理——历史为空、历史加载失败、推送连接断开是三种不同的事实。
 */
export function EventTimelinePanel({
  onLoadMore,
  onReconnect,
  onReload,
  state,
  stream,
}: {
  onLoadMore: () => void;
  onReconnect: () => void;
  onReload: () => void;
  state: EventTimelineState;
  stream: TaskEventStreamState;
}) {
  if (state.status === "idle") {
    return <Typography.Text type="secondary">请选择任务后查看事件时间线。</Typography.Text>;
  }
  if (state.status === "loading") {
    return <Typography.Text type="secondary">正在加载事件时间线…</Typography.Text>;
  }
  if (state.status === "error") {
    return (
      <Alert
        action={<LinkButton onClick={onReload} text="重试" />}
        description={state.error}
        showIcon
        title="加载事件时间线失败"
        type="error"
      />
    );
  }

  return (
    <Flex gap="small" vertical>
      {state.error && (
        <Alert
          action={<LinkButton onClick={onReload} text="重试" />}
          description={state.error}
          showIcon
          title="事件时间线刷新失败，下面展示的可能是旧数据"
          type="warning"
        />
      )}
      <Space align="center" size={8} wrap>
        <Typography.Text strong>事件时间线</Typography.Text>
        <Typography.Text type="secondary">
          {state.events.length === 0
            ? "暂无事件记录"
            : `已显示 ${state.events.length} / ${state.total} 条`}
        </Typography.Text>
        {/*
          未读提示放在可见的表头：实时区域位于整段历史之后，不给出提示时用户不会知道
          下方还有新的阶段事件。它只说明"收到了几条事件"，不代表任务状态。
        */}
        {stream.events.length > 0 && (
          <Typography.Text type="success">
            实时新增 {stream.events.length} 条（见下方实时事件）
          </Typography.Text>
        )}
        <LinkButton onClick={onReload} text="刷新" />
      </Space>
      {state.events.length === 0 ? (
        <Alert
          description="数据库里还没有该任务的事件记录。事件由服务端在真实执行过程中追加，列表为空不代表任务从未启动，请结合任务状态与工作流进度判断。"
          showIcon
          title="暂无事件记录"
          type="info"
        />
      ) : (
        <Flex gap={6} vertical>
          {state.events.map((event) => (
            <EventRow event={event} key={event.id} />
          ))}
        </Flex>
      )}
      {hasMoreEvents(state.events.length, state.total) && (
        <LinkButton
          onClick={onLoadMore}
          text={state.loadingMore ? "正在加载更多事件…" : "加载更多事件"}
        />
      )}
      <LiveEventsSection onReconnect={onReconnect} stream={stream} />
    </Flex>
  );
}
