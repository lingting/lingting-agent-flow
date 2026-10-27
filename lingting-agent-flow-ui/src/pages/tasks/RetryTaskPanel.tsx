/**
 * 重试资格与显式重试操作面板。
 *
 * 数据与职责：
 *   - 资格来自 `GET /api/v1/tasks/{taskID}/retry-eligibility`，`can_retry` 是唯一判据；
 *   - 操作走 `POST /api/v1/tasks/{taskID}/retry`，由服务端决定重开哪个执行周期、
 *     准备容器、生成计划并启动编排；前端不拼接任何启动流程；
 *   - 请求成功后按响应字段分开展示"任务已重开"与"工作流是否启动"，请求失败如实展示服务端
 *     错误，结果不确定时明确提示无法确认并且不自动重复提交。
 *
 * 本面板不自动重试、不自动恢复工作流，也不把资格查询并入 5 秒自动刷新：它只在任务切换与
 * 用户显式刷新时读取资格。
 */
import { Alert, Descriptions, Flex, Space, Typography } from "antd";
import { Button, LinkButton } from "lingting-react-ui";

import type { Task } from "@/lib/api";
import { launchStageMeta } from "@/pages/tasks/launch";
import {
  canSubmitRetry,
  describeRetryResult,
  RETRY_BLOCKED_WITHOUT_ELIGIBILITY,
  retryBlockingList,
  retryBlockingMeta,
  retrySummary,
  retryVerdict,
} from "@/pages/tasks/retry";
import { isKnownTaskStatus, TASK_STATUS_LABEL } from "@/pages/tasks/status";
import { useTaskRetry } from "@/pages/tasks/useTaskRetry";

/** 由 `TASK_STATUS_META` 派生的"状态取值 → 中文标签"映射，不新增第二套状态名 */
const STATUS_LABELS: Record<string, string> = TASK_STATUS_LABEL;

/** 任务状态展示：已知取值用中文标签，未知取值原样展示服务端取值 */
function RetryStatusLine({ status }: { status: string }) {
  if (isKnownTaskStatus(status)) {
    return <Typography.Text>{TASK_STATUS_LABEL[status]}</Typography.Text>;
  }
  return (
    <Space size={8} wrap>
      <Typography.Text>{status}</Typography.Text>
      <Typography.Text type="secondary">服务端返回的状态取值未在当前版本中定义</Typography.Text>
    </Space>
  );
}

/**
 * 重试资格与重试操作面板。
 *
 * `onRefreshTask` 由父组件提供：动作之后必须重新读取任务详情与工作流进度，返回值是刷新
 * 失败的项，用于单独提示。
 */
export function RetryTaskPanel({
  onRefreshTask,
  task,
}: {
  onRefreshTask: () => Promise<string[]>;
  task: Task;
}) {
  const { clearOutcome, eligibility, operation, reloadEligibility, retry } = useTaskRetry({
    refresh: onRefreshTask,
    taskID: task.id,
  });

  const snapshot = eligibility.status === "ready" ? eligibility.data : undefined;
  // 资格未确认（从未查询成功、查询失败或快照不属于当前任务）时一律禁用操作。
  const allowed = canSubmitRetry({
    eligibility: snapshot,
    eligibilityTaskID: snapshot?.task_id,
    running: operation.running,
    taskID: task.id,
  });
  const snapshotTaskID = snapshot?.task_id?.trim();
  const staleSnapshot = snapshot !== undefined && snapshotTaskID !== task.id;

  return (
    <Flex gap="small" vertical>
      <Space align="center" size={8} wrap>
        <Typography.Text strong>重试任务</Typography.Text>
        <Button
          disabled={!allowed}
          loading={operation.running}
          onClick={() => void retry()}
          size="small"
          text={operation.running ? "重试中" : "重试任务"}
          tooltip={
            allowed
              ? "重试是真实执行操作：服务端会重开任务、准备执行环境并尝试启动工作流。"
              : RETRY_BLOCKED_WITHOUT_ELIGIBILITY
          }
          type="primary"
        />
        <LinkButton onClick={reloadEligibility} text="刷新重试资格" />
      </Space>
      <Typography.Text type="secondary">
        资格由服务端依据任务状态、任务执行权、执行记录、步骤占用与外部副作用计算，是查询时刻的
        只读快照。允许重试不代表任务会成功；真正重试时服务端仍会重新判定资格并重新准备执行环境。
      </Typography.Text>

      {eligibility.status === "loading" && (
        <Typography.Text type="secondary">正在查询重试资格…</Typography.Text>
      )}
      {eligibility.status === "error" && (
        <Alert
          action={<Button onClick={reloadEligibility} size="small" text="重试" />}
          description={`${eligibility.error} 重试资格当前未知，不能据此发起重试。`}
          showIcon
          title="查询重试资格失败"
          type="error"
        />
      )}
      {eligibility.status === "ready" && eligibility.error && (
        <Alert
          action={<Button onClick={reloadEligibility} size="small" text="重试" />}
          description={`${eligibility.error} 下方展示的是上一次成功查询的结果，不是最新状态。`}
          showIcon
          title="重试资格刷新失败"
          type="warning"
        />
      )}

      {snapshot && (
        <Flex gap={4} vertical>
          <Alert
            description={retryVerdict(snapshot.can_retry).description}
            showIcon
            title={retryVerdict(snapshot.can_retry).title}
            type={retryVerdict(snapshot.can_retry).color === "success" ? "success" : "warning"}
          />
          <Descriptions column={1} size="small">
            <Descriptions.Item label="服务端摘要">{retrySummary(snapshot)}</Descriptions.Item>
            <Descriptions.Item label="任务状态">
              <RetryStatusLine status={String(snapshot.task_status)} />
            </Descriptions.Item>
          </Descriptions>
          {staleSnapshot && (
            <Alert
              description="该资格结论描述的不是当前打开的任务，已经失效；请重新查询重试资格。"
              showIcon
              title="资格结论不属于当前任务"
              type="warning"
            />
          )}

          <Flex gap={4} vertical>
            <Typography.Text strong>阻塞原因</Typography.Text>
            {retryBlockingList(snapshot).length === 0 ? (
              <Typography.Text type="secondary">
                服务端本次没有返回阻塞项；资格结论仍以服务端给出的 can_retry 为准。
              </Typography.Text>
            ) : (
              retryBlockingList(snapshot).map((code) => {
                const meta = retryBlockingMeta(code);
                return (
                  <Flex gap={2} key={code} vertical>
                    <Space size={8} wrap>
                      <Typography.Text type="warning">{meta.label}</Typography.Text>
                      {!meta.known && <Typography.Text code>{code}</Typography.Text>}
                    </Space>
                    <Typography.Text type="secondary">{meta.description}</Typography.Text>
                  </Flex>
                );
              })
            )}
          </Flex>
        </Flex>
      )}

      {operation.running && (
        <Alert
          description="请求已发出，服务端正在重开任务并尝试启动工作流。该操作是同步的，请等待真实结果返回后再进行下一步。"
          showIcon
          title="正在执行重试"
          type="info"
        />
      )}
      {operation.failure && (
        <Alert
          closable
          description={operation.failure}
          onClose={clearOutcome}
          showIcon
          title="重试请求失败"
          type="error"
        />
      )}
      {operation.uncertain && (
        <Alert description={operation.uncertain} showIcon title="请求结果无法确认" type="warning" />
      )}
      {operation.outcome && <RetryOutcomePanel onClose={clearOutcome} result={operation.outcome} />}
      {operation.refreshError && (
        <Alert description={operation.refreshError} showIcon title="状态刷新失败" type="warning" />
      )}
    </Flex>
  );
}

/**
 * 一次重试的真实结果。
 *
 * 只有 `workflow_started=true` 才展示为"已启动工作流"；其余情况明确说明任务已重开但工作流
 * 没有启动，并给出服务端返回的失败阶段与原因。任务状态、最终结论与收尾状态是另外三个独立
 * 事实，这里都不由响应推断。
 */
function RetryOutcomePanel({
  onClose,
  result,
}: {
  onClose: () => void;
  result: NonNullable<ReturnType<typeof useTaskRetry>["operation"]["outcome"]>;
}) {
  const outcome = describeRetryResult(result, {
    stageLabel: (stage) => launchStageMeta(stage).label,
    statusLabels: STATUS_LABELS,
  });
  return (
    <Alert
      closable
      description={
        <Flex gap={2} vertical>
          {outcome.details.map((detail) => (
            <Typography.Text key={detail}>{detail}</Typography.Text>
          ))}
          <Typography.Text type="secondary">
            请求成功只说明服务端完成了本次操作；任务是否成功仍要以最终业务结论与收尾闭环为准。
          </Typography.Text>
        </Flex>
      }
      onClose={onClose}
      showIcon
      title={outcome.title}
      type={outcome.kind === "started" ? "success" : "warning"}
    />
  );
}
