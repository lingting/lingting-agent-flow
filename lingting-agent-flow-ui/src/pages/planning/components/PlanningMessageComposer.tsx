/**
 * 需求输入区。
 *
 * 约束：
 * - 空白输入不提交，提交过程中按钮禁用，重复点击不会发出第二个请求；
 * - 只有服务端确认用户消息已落库后输入框才清空：分析阶段的失败不会让已提交的需求回到
 *   输入框，从而避免同一条需求被重复追加；
 * - 不开放执行节点、角色、模型或超时覆盖入口，这些都由服务端决定。
 */
import { Alert, Flex, Input, Space, Typography } from "antd";
import { Button } from "lingting-react-ui";
import { useState } from "react";

import { isBlankPlanningInput } from "@/pages/planning/planning";

/** 需求输入面板 */
export function PlanningMessageComposer(props: {
  /** 当前状态是否允许追加需求 */
  disabled: boolean;
  /** 不允许追加需求时的原因 */
  disabledReason?: string;
  submitting: boolean;
  /** 返回用户消息是否已被服务端持久化 */
  onSubmit: (content: string) => Promise<boolean>;
}) {
  const [value, setValue] = useState("");
  const blank = isBlankPlanningInput(value);
  const blocked = props.disabled || props.submitting;

  const submit = async () => {
    if (blocked || blank) return;
    const persisted = await props.onSubmit(value);
    if (persisted) setValue("");
  };

  return (
    <Flex gap={4} vertical>
      <Typography.Text strong>追加需求</Typography.Text>
      <Input.TextArea
        aria-label="需求输入"
        autoSize={{ maxRows: 12, minRows: 4 }}
        disabled={props.disabled}
        onChange={(event) => setValue(event.target.value)}
        placeholder="描述这次要做的事：目标、范围、约束与验收标准都可以直接写。"
        value={value}
      />
      <Space align="center" size={8} wrap>
        <Button
          disabled={blocked || blank}
          loading={props.submitting}
          onClick={() => void submit()}
          size="small"
          text="提交需求并分析"
          type="primary"
        />
        <Typography.Text type="secondary">
          提交会先追加用户消息（会话进入分析中），再显式调用两阶段分析；只有分析成功并完成
          持久化才会出现新的方案版本。
        </Typography.Text>
      </Space>
      {props.disabled && props.disabledReason && (
        <Alert description={props.disabledReason} showIcon title="当前不能追加需求" type="info" />
      )}
      {!props.disabled && blank && (
        <Typography.Text type="secondary">空白内容不会被提交。</Typography.Text>
      )}
    </Flex>
  );
}
