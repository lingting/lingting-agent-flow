/**
 * 任务执行现场面板：计划、步骤、尝试、验证与操作入口。
 *
 * 组织方式对应真实业务链路：任务详情 → 计划步骤 → 步骤详情 → 尝试与验证结果 →
 * 明确的后续动作。所有动作都只作用于当前真实记录，不接受手工输入的资源标识。
 *
 * 数据与动作的来源分工：
 *   - 步骤列表来自 `GET /tasks/{id}/steps`；
 *   - 尝试、验证历史与会话来自执行记录只读接口；
 *   - 动作通过 `useExecutionOperations` 调用真实写接口；
 *   - 动作结束后的刷新只更新展示，不改变已经取得的执行结论。
 */
import { Alert, Flex, Space, Typography } from "antd";
import { Button, LinkButton } from "lingting-react-ui";
import { useCallback, useMemo, useState } from "react";

import type { Step, Task } from "@/lib/api";
import { OperationOutcomePanel } from "@/pages/tasks/OperationOutcomePanel";
import { StepDetailPanel } from "@/pages/tasks/StepDetailPanel";
import { StepListPanel } from "@/pages/tasks/StepListPanel";
import { canGeneratePlan, canRunSteps } from "@/pages/tasks/steps";
import { useRepairCycleEligibility } from "@/pages/tasks/useRepairCycleEligibility";
import { EventTimelinePanel } from "@/pages/tasks/EventTimelinePanel";
import { useExecutionOperations, OPERATION_LABEL } from "@/pages/tasks/useExecutionOperations";
import {
  describeApiError,
  useStepRecords,
  useTaskEvents,
  useTaskSteps,
} from "@/pages/tasks/useExecutionRecords";

/**
 * 任务执行面板。
 *
 * `onRefreshTask` 由父组件提供：动作之后任务状态与工作流进度都可能变化，必须重新
 * 读取。它返回刷新失败的项说明，用于单独提示，不覆盖执行结论。
 *
 * `stepsToken` 由父组件的自动刷新提供：只有服务端返回的步骤相关事实真的变化时才会递增，
 * 因此本面板不自行建定时器，也不会按固定周期无条件重复请求步骤与执行记录。
 */
export function TaskExecutionPanel({
  onRefreshTask,
  stepsToken,
  task,
}: {
  onRefreshTask: () => Promise<string[]>;
  stepsToken?: number;
  task: Task;
}) {
  /**
   * 选中的步骤必须与它所属的任务一起保存。
   *
   * 切换任务时本组件不会被卸载，若只保存步骤标识，切换后的第一帧仍会按上一个任务的步骤
   * 标识发起一次必然失败的查询，并把上一个任务的选中态短暂留在新任务界面上。因此选择里
   * 同时记录任务标识，只有它与当前任务一致时才视为选中。
   */
  const [selection, setSelection] = useState<{ stepID: string; taskID: string }>();
  const selectedStepID = selection?.taskID === task.id ? selection.stepID : undefined;
  const stepsState = useTaskSteps(task.id, stepsToken);
  const records = useStepRecords(task.id, selectedStepID, stepsToken);
  /**
   * 「开启新执行周期」的服务端资格。
   *
   * 它只回答"这一步现在能不能开启新周期"，与步骤/尝试/验证的只读加载器分开：
   *   - 只读，不登记周期、不执行任何 Agent 调用；
   *   - 与当前任务 + 选中步骤绑定，切换任一方都会作废旧快照；
   *   - 不并入 5 秒任务快照循环：只在首次读取、步骤事实真的变化（`stepsToken`）与
   *     动作结束后重新查询。
   */
  const repairEligibility = useRepairCycleEligibility({
    stepID: selectedStepID,
    taskID: task.id,
    token: stepsToken,
  });
  /**
   * 事件时间线独立于步骤选择：事件属于任务维度，切换步骤不应该重新读取历史。
   *
   * 它只在打开任务、手动刷新、"加载更多"与父组件给出的 `stepsToken` 变化时读取，
   * 不参与 5 秒自动刷新——把整份历史放进刷新循环会制造无谓的查询负载。
   */
  const events = useTaskEvents(task.id, stepsToken);

  const steps = stepsState.state.status === "ready" ? stepsState.state.data : [];
  const selectedStep = useMemo(
    () => steps.find((step) => step.id === selectedStepID),
    [selectedStepID, steps],
  );

  /** 动作结束后重新读取全部现场；失败项返回给调用方单独提示 */
  const refreshAll = useCallback(async () => {
    const failures: string[] = [];
    stepsState.reload();
    records.reload();
    // 任何动作都可能改变"能否开启新执行周期"的现场（步骤占用、预算、周期收敛），
    // 因此动作结束后必须重新查询资格，而不是继续沿用动作前的结论。
    repairEligibility.reload();
    try {
      const taskFailures = await onRefreshTask();
      failures.push(...taskFailures);
    } catch (cause) {
      failures.push(`任务状态（${describeApiError(cause)}）`);
    }
    return failures;
  }, [onRefreshTask, records.reload, repairEligibility.reload, stepsState.reload]);

  const operations = useExecutionOperations({ refresh: refreshAll, taskID: task.id });

  const clearOperation = operations.clear;
  const handleSelectStep = useCallback(
    (step: Step) => {
      setSelection({ stepID: step.id, taskID: task.id });
      clearOperation();
    },
    [clearOperation, task.id],
  );

  const busy = operations.state.running !== undefined;

  return (
    <Flex gap="middle" vertical>
      <Space align="center" size={8} wrap>
        <Typography.Text strong>执行操作</Typography.Text>
        <Button
          disabled={busy || !canGeneratePlan(task, steps)}
          loading={operations.state.running?.key === "plan"}
          onClick={() => void operations.plan()}
          size="small"
          text={OPERATION_LABEL.plan}
          tooltip="生成计划会真实调用 Planner，并要求任务已有受管理容器与规划角色；已有计划时服务端拒绝覆盖。"
        />
        <Button
          disabled={busy || !canRunSteps(task, steps)}
          loading={operations.state.running?.key === "execute-steps"}
          onClick={() => void operations.executeSteps()}
          size="small"
          text={OPERATION_LABEL["execute-steps"]}
          tooltip="按数据库中的计划顺序依次执行开发步骤，失败即停止；不包含验证、提交、推送与合并。"
        />
        <LinkButton onClick={stepsState.reload} text="刷新步骤" />
      </Space>
      <Typography.Text type="secondary">
        新任务必须先通过任务列表的「启动」完成环境准备、计划生成与工作流启动；
        这里的入口只用于在既有执行现场上做显式推进与诊断。
      </Typography.Text>

      <OperationOutcomePanel state={operations.state} />

      <StepListPanel
        onReload={stepsState.reload}
        onSelect={handleSelectStep}
        selectedStepID={selectedStepID}
        state={stepsState.state}
      />

      {selectedStep ? (
        <StepDetailPanel
          key={selectedStep.id}
          onReload={records.reload}
          operations={operations}
          records={records.state}
          recordsToken={stepsToken}
          repairEligibility={repairEligibility.state}
          step={selectedStep}
          task={task}
        />
      ) : (
        steps.length > 0 && (
          <Alert
            description="点击上方步骤名称查看该步骤的执行尝试、验证历史与可发起的操作。"
            showIcon
            title="尚未选择步骤"
            type="info"
          />
        )
      )}

      {/*
        事件时间线放在步骤详情之后：它是任务维度的历史，一次可展示 20 条以上并支持
        「加载更多」，若插在步骤列表与步骤详情之间，用户点开某个步骤后必须越过整段历史
        才能看到刚选中的步骤详情与操作入口。读取时机不变（仍不参与 5 秒自动刷新）。
      */}
      <EventTimelinePanel
        onLoadMore={events.loadMore}
        onReload={events.reload}
        state={events.state}
      />
    </Flex>
  );
}
