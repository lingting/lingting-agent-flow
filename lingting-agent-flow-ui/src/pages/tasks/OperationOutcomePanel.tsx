/**
 * 执行结论展示。
 *
 * 三类信息分别呈现，互不覆盖：
 *   - 服务端返回的执行结论（成功或未成功，逐条事实来自响应字段）；
 *   - 请求失败的业务错误（服务端给出的 message）；
 *   - 动作之后的现场刷新失败（不影响已经取得的执行结论）。
 */
import { Alert, Flex, Typography } from "antd";

import type { ExecutionOperationsState } from "@/pages/tasks/useExecutionOperations";
import { OPERATION_LABEL } from "@/pages/tasks/useExecutionOperations";

/** 动作目标的说明文字，例如"步骤 step-1 的确定性验证" */
function describeTarget(key: keyof typeof OPERATION_LABEL, stepID?: string): string {
  const label = OPERATION_LABEL[key];
  if (!stepID) return label;
  return `${label}（步骤 ${stepID}）`;
}

/** 执行结论面板 */
export function OperationOutcomePanel({ state }: { state: ExecutionOperationsState }) {
  const { failure, outcome, refreshError, running, uncertain } = state;
  if (!running && !outcome && !failure && !refreshError && !uncertain) return null;

  return (
    <Flex gap="small" vertical>
      {running && (
        <Alert
          description="请求已发出，服务端正在执行。该操作是同步的，请等待真实结果返回后再进行下一步。"
          showIcon
          title={`正在执行：${describeTarget(running.key, running.stepID)}`}
          type="info"
        />
      )}
      {outcome && (
        <Alert
          description={
            <Flex gap={2} vertical>
              {outcome.result.details.map((detail) => (
                <Typography.Text key={detail}>{detail}</Typography.Text>
              ))}
            </Flex>
          }
          showIcon
          title={`${describeTarget(outcome.key, outcome.stepID)}：${outcome.result.title}`}
          type={outcome.result.kind}
        />
      )}
      {failure && (
        <Alert
          description={
            <Flex gap={2} vertical>
              <Typography.Text>{failure.message}</Typography.Text>
              {/* 服务端补充的执行详情只陈述已经发生的事实，不改变"未通过"的结论 */}
              {failure.facts && failure.facts.length > 0 && (
                <>
                  <Typography.Text type="secondary">
                    服务端本次执行详情（不代表操作成功）：
                  </Typography.Text>
                  {failure.facts.map((fact) => (
                    <Typography.Text key={fact}>{fact}</Typography.Text>
                  ))}
                </>
              )}
            </Flex>
          }
          showIcon
          title={`${describeTarget(failure.key, failure.stepID)}${
            failure.facts ? "未通过" : "失败"
          }`}
          type="error"
        />
      )}
      {/* 结果不确定与服务端拒绝分别提示：前者意味着请求可能已经生效，不得自动重发 */}
      {uncertain && (
        <Alert description={uncertain} showIcon title="操作结果无法确认" type="warning" />
      )}
      {refreshError && (
        <Alert description={refreshError} showIcon title="状态刷新失败" type="warning" />
      )}
    </Flex>
  );
}
