/**
 * 任务事件时间线面板（P4-M3-8 / G15）。
 *
 * 只回答"何时发生了什么"：事件时间、中文类型说明、真实关联标识与经审查的摘要。
 * 它不参与 5 秒自动刷新——事件历史只在打开任务、手动刷新与"加载更多"时读取，
 * 避免把整份历史放进刷新循环。
 *
 * 四种状态必须可区分：加载中、加载失败（可重试）、空列表、有数据。失败不能被
 * 显示成"没有事件"，空列表也不能被解释为"任务从未执行"。
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

/**
 * 事件时间线面板。
 *
 * `onReload` 与 `onLoadMore` 都只发起只读查询：不会触发执行、验证、重试、
 * 恢复或代码保存。`token` 变化由父组件依据服务端真实状态变化给出。
 */
export function EventTimelinePanel({
  onLoadMore,
  onReload,
  state,
}: {
  onLoadMore: () => void;
  onReload: () => void;
  state: EventTimelineState;
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
    </Flex>
  );
}
