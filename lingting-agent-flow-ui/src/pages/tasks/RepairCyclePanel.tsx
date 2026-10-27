/**
 * 步骤修复周期历史面板（P4-M3-8 / G16）。
 *
 * 只回答"这个步骤经历过哪些显式修复周期"：周期序号、开启与结束时间、开启来源与
 * 尝试上限。它不推断业务成功或验证通过，也不发起任何副作用。
 *
 * `closed_at` 缺失只表示"还没有记录结束时间"，必须显示中性占位符；周期列表为空
 * 只表示该步骤没有被显式开启过新周期，不能解释为"从未执行"。
 */
import { Alert, Flex, Space, Typography } from "antd";
import { LinkButton } from "lingting-react-ui";

import type { RepairCycle } from "@/lib/api";
import { REPAIR_CYCLE_EMPTY_TEXT, repairCycleDisplay } from "@/pages/tasks/history";
import type { LoadState } from "@/pages/tasks/useExecutionRecords";

/** 单个周期行 */
function RepairCycleRow({ cycle }: { cycle: RepairCycle }) {
  const display = repairCycleDisplay(cycle);
  return (
    <Flex
      align="flex-start"
      gap="small"
      justify="space-between"
      style={{ borderRadius: 6, padding: "4px 0" }}
    >
      <Flex align="center" gap={8} wrap>
        <Typography.Text strong>{display.sequenceText}</Typography.Text>
        <Typography.Text type="secondary">开启 {display.openedText}</Typography.Text>
        <Typography.Text type="secondary">
          结束 {display.closedText}
          {display.closeHint ? `（${display.closeHint}）` : ""}
        </Typography.Text>
      </Flex>
      <Flex align="flex-end" gap={2} vertical>
        <Typography.Text type="secondary">来源 {display.openedByText}</Typography.Text>
        <Typography.Text type="secondary">尝试上限 {display.maxAttemptsText}</Typography.Text>
        {/* 周期标识用次级文本呈现：它只在跨记录核对时才需要，不占主视线 */}
        <Typography.Text code type="secondary">
          {cycle.id}
        </Typography.Text>
      </Flex>
    </Flex>
  );
}

/**
 * 修复周期历史面板。
 *
 * 四种状态分别呈现：缺少步骤（idle）、加载中、请求失败（带重试）、就绪（可能为空）。
 * 刷新失败时保留上一次成功取得的记录并单独提示，不把失败显示成"暂无周期"。
 */
export function RepairCyclePanel({
  onReload,
  state,
}: {
  onReload: () => void;
  state: LoadState<RepairCycle[]>;
}) {
  if (state.status === "idle") {
    return <Typography.Text type="secondary">选择步骤后查看修复周期历史。</Typography.Text>;
  }
  if (state.status === "loading") {
    return <Typography.Text type="secondary">正在加载修复周期历史…</Typography.Text>;
  }
  if (state.status === "error") {
    return (
      <Alert
        action={<LinkButton onClick={onReload} text="重试" />}
        description={state.error}
        showIcon
        title="加载修复周期历史失败"
        type="error"
      />
    );
  }

  const cycles = state.data;
  return (
    <Flex gap="small" vertical>
      {state.error && (
        <Alert
          action={<LinkButton onClick={onReload} text="重试" />}
          description={state.error}
          showIcon
          title="修复周期历史刷新失败，下面展示的可能是旧数据"
          type="warning"
        />
      )}
      <Space align="center" size={8} wrap>
        <Typography.Text strong>修复周期历史</Typography.Text>
        <Typography.Text type="secondary">
          {cycles.length === 0 ? REPAIR_CYCLE_EMPTY_TEXT : `共 ${cycles.length} 个周期`}
        </Typography.Text>
        <LinkButton onClick={onReload} text="刷新" />
      </Space>
      {cycles.length === 0 ? (
        <Typography.Text type="secondary">
          该步骤没有被显式开启过的执行周期。周期只由用户显式开启动作产生，列表为空不代表该步骤从未执行。
        </Typography.Text>
      ) : (
        <Flex gap={4} vertical>
          {cycles.map((cycle) => (
            <RepairCycleRow cycle={cycle} key={cycle.id} />
          ))}
        </Flex>
      )}
    </Flex>
  );
}
