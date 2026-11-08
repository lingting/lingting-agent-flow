/**
 * 规划消息区。
 *
 * 展示服务端真实持久化的消息：用户输入、需求分析师输出、方案规划师输出与系统记录都
 * 分别展示，不合并成一段没有来源的文本；顺序按服务端分配的序号。
 */
import { Alert, Flex, Space, Tag, Typography } from "antd";
import { Button } from "lingting-react-ui";

import type { PlanningFactsState } from "@/pages/planning/hooks/usePlanningSessionFacts";
import {
  formatPlanningTime,
  planningSenderColor,
  planningSenderLabel,
  sortPlanningMessages,
} from "@/pages/planning/planning";

/** 单条消息 */
function MessageBlock(input: {
  senderType: string;
  sequence: number;
  createdAt: string;
  content: string;
}) {
  return (
    <Flex data-testid="planning-message" gap={2} vertical>
      <Space align="center" size={8} wrap>
        <Tag color={planningSenderColor(input.senderType)}>
          {planningSenderLabel(input.senderType)}
        </Tag>
        <Typography.Text type="secondary">第 {input.sequence} 条</Typography.Text>
        <Typography.Text type="secondary">{formatPlanningTime(input.createdAt)}</Typography.Text>
      </Space>
      <Typography.Paragraph style={{ marginBottom: 0, whiteSpace: "pre-wrap" }}>
        {input.content}
      </Typography.Paragraph>
    </Flex>
  );
}

/** 消息区面板 */
export function PlanningMessagePanel(props: { state: PlanningFactsState; onReload: () => void }) {
  if (props.state.status === "idle") return null;
  if (props.state.status === "loading") {
    return <Typography.Text type="secondary">正在读取规划会话、消息与方案版本…</Typography.Text>;
  }
  if (props.state.status === "error") {
    return (
      <Alert
        action={<Button onClick={props.onReload} size="small" text="重试" />}
        description={props.state.error}
        showIcon
        title="读取规划会话失败"
        type="error"
      />
    );
  }
  const messages = sortPlanningMessages(props.state.facts.messages);
  return (
    <Flex gap="middle" vertical>
      <Space align="center" size={8} wrap>
        <Typography.Text strong>规划消息</Typography.Text>
        <Typography.Text type="secondary">
          {messages.length === 0 ? "还没有任何消息" : `共 ${messages.length} 条`}
        </Typography.Text>
      </Space>
      {messages.length === 0 ? (
        <Typography.Text type="secondary">
          该会话还没有消息记录。消息只由服务端写入：用户消息来自你的提交，分析师与规划师
          消息来自真实的两阶段分析。
        </Typography.Text>
      ) : (
        <Flex data-testid="planning-messages" gap="middle" vertical>
          {messages.map((message) => (
            <MessageBlock
              content={message.content}
              createdAt={message.created_at}
              key={message.id}
              senderType={message.sender_type}
              sequence={message.sequence}
            />
          ))}
        </Flex>
      )}
    </Flex>
  );
}
