/**
 * 步骤列表视图。
 *
 * 只展示接口真实返回的字段：顺序、标题、类型、状态、失败摘要与起止时间。计划是否
 * 持久化不靠前端推断——步骤列表来自数据库，列表为空就是"没有已持久化的计划步骤"。
 *
 * 起止时间来自 `started_at` / `finished_at`：缺失时显示中性占位符，正在执行的步骤
 * 不显示完成时间，绝不使用 `updated_at` 或任何推算值冒充。
 *
 * 每一项都可以被选中，选中后由父组件加载该步骤的尝试、会话、验证历史与操作入口。
 */
import { Alert, Flex, Space, Typography } from "antd";
import { DictTag, LinkButton } from "lingting-react-ui";

import type { Step } from "@/lib/api";
import {
  isKnownStepStatus,
  STEP_STATUS_DICT,
  stepStatusMeta,
  stepTypeLabel,
  validationStateLabel,
} from "@/pages/tasks/steps";
import { stepTimeDisplay, stepTimeSummary } from "@/pages/tasks/stepTimes";
import type { LoadState } from "@/pages/tasks/useExecutionRecords";

/** 单个步骤行 */
function StepRow({
  selected,
  step,
  validationState,
  onSelect,
}: {
  step: Step;
  selected: boolean;
  validationState?: string;
  onSelect: (step: Step) => void;
}) {
  const meta = stepStatusMeta(step.status);
  const times = stepTimeDisplay(step);
  return (
    <Flex
      align="flex-start"
      gap="small"
      justify="space-between"
      style={{
        background: selected ? "rgba(22, 119, 255, 0.08)" : undefined,
        borderRadius: 6,
        padding: "6px 8px",
      }}
    >
      <Flex align="center" gap="small" style={{ minWidth: 0 }} wrap>
        <Typography.Text type="secondary" style={{ width: 28 }}>
          {step.sequence}
        </Typography.Text>
        <LinkButton
          disabled={selected}
          onClick={() => onSelect(step)}
          text={step.name || step.id}
        />
        <Typography.Text type="secondary">{stepTypeLabel(step.step_type)}</Typography.Text>
        {isKnownStepStatus(step.status) ? (
          <DictTag dict={STEP_STATUS_DICT} value={step.status} />
        ) : (
          <Typography.Text type="secondary">{step.status}</Typography.Text>
        )}
        {validationState && (
          <Typography.Text type="secondary">
            验证：{validationStateLabel(validationState)}
          </Typography.Text>
        )}
        {step.current_attempt_id && (
          <Typography.Text code type="secondary">
            尝试 {step.current_attempt_id}
          </Typography.Text>
        )}
      </Flex>
      <Flex align="flex-end" gap={2} style={{ flexShrink: 0 }} vertical>
        {step.error && (
          <Typography.Text style={{ maxWidth: 260 }} type="danger" ellipsis>
            {step.error}
          </Typography.Text>
        )}
        {/*
          起止时间直接来自服务端字段：缺失显示占位符，正在执行时不显示完成时间，
          也不显示任何由 created_at / updated_at 推算出来的时刻。
        */}
        <Typography.Text type="secondary">
          开始 {times.startedText} · 结束 {times.finishedText}
        </Typography.Text>
        <Typography.Text type="secondary">
          {meta.active ? "执行中" : stepTimeSummary(step)}
        </Typography.Text>
      </Flex>
    </Flex>
  );
}

/**
 * 步骤列表面板。
 *
 * `state` 的四种状态分别呈现：加载中、请求失败（带重试）、空列表、有数据。
 * 空列表只表示"计划里还没有步骤"，不能显示成"计划已完成"。
 */
export function StepListPanel({
  state,
  selectedStepID,
  validationStates,
  onReload,
  onSelect,
}: {
  state: LoadState<Step[]>;
  selectedStepID?: string;
  validationStates?: Record<string, string>;
  onReload: () => void;
  onSelect: (step: Step) => void;
}) {
  if (state.status === "idle") {
    return <Typography.Text type="secondary">请选择任务后查看计划步骤。</Typography.Text>;
  }
  if (state.status === "loading") {
    return <Typography.Text type="secondary">正在加载计划步骤…</Typography.Text>;
  }
  if (state.status === "error") {
    return (
      <Alert
        action={<LinkButton onClick={onReload} text="重试" />}
        description={state.error}
        showIcon
        title="加载步骤失败"
        type="error"
      />
    );
  }

  const steps = state.data;
  return (
    <Flex gap="small" vertical>
      {state.error && (
        <Alert
          action={<LinkButton onClick={onReload} text="重试" />}
          description={state.error}
          showIcon
          title="步骤刷新失败，下面展示的可能是旧数据"
          type="warning"
        />
      )}
      <Space align="center" size={8}>
        <Typography.Text strong>计划步骤</Typography.Text>
        <Typography.Text type="secondary">
          {steps.length === 0 ? "尚无已持久化的计划步骤" : `共 ${steps.length} 个步骤`}
        </Typography.Text>
        <LinkButton onClick={onReload} text="刷新" />
      </Space>
      {steps.length === 0 ? (
        <Alert
          description="数据库里还没有该任务的计划步骤。计划需要通过统一启动或计划生成接口真实写入后才能执行；列表为空不代表计划已完成。"
          showIcon
          title="没有计划步骤"
          type="info"
        />
      ) : (
        <Flex gap={4} vertical>
          {steps.map((step) => (
            <StepRow
              key={step.id}
              onSelect={onSelect}
              selected={step.id === selectedStepID}
              step={step}
              validationState={validationStates?.[step.id]}
            />
          ))}
        </Flex>
      )}
    </Flex>
  );
}
