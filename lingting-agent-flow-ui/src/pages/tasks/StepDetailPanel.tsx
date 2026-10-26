/**
 * 步骤详情与操作区。
 *
 * 结构对应真实业务链路：步骤 → 尝试 → 验证结果 → 明确的后续动作。
 *
 * 三条硬约束：
 *   - 所有动作的任务、步骤与尝试标识都来自当前真实记录，没有手填入口；
 *   - 动作进行中禁用全部动作按钮，重复点击不会发出第二个请求；
 *   - 服务端拒绝原因原样展示，前端不把它翻译成"成功"或"重试即可"。
 */
import { Alert, Descriptions, Flex, Space, Typography } from "antd";
import { Button, DictTag, type DictItem, LinkButton } from "lingting-react-ui";
import { useState } from "react";

import type { Attempt, Step, Task, ValidationHistoryResponse } from "@/lib/api";
import {
  ATTEMPT_STATUS_META,
  attemptStatusMeta,
  canExecuteAndValidate,
  canExecuteStep,
  canInitializeAttempt,
  canOpenRepairCycle,
  canValidateAttempt,
  CHECK_STATUS_LABEL,
  checkStatusLabel,
  isKnownStepStatus,
  STEP_STATUS_DICT,
  stepStatusMeta,
  stepTypeLabel,
  validationStateLabel,
  validationStatusLabel,
  verdictMeta,
  VERDICT_META,
} from "@/pages/tasks/steps";
import type { ExecutionOperations } from "@/pages/tasks/useExecutionOperations";
import { OPERATION_LABEL } from "@/pages/tasks/useExecutionOperations";
import type { LoadState, StepRecords } from "@/pages/tasks/useExecutionRecords";
import { useValidationFeedback } from "@/pages/tasks/useExecutionRecords";
/** 尝试状态字典 */
const ATTEMPT_STATUS_DICT: DictItem<string>[] = Object.entries(ATTEMPT_STATUS_META).map(
  ([value, meta]) => ({ label: meta.label, tagColor: meta.color, value }),
);

/** 验证结论字典 */
const VERDICT_DICT: DictItem<string>[] = Object.entries(VERDICT_META).map(([value, meta]) => ({
  label: meta.label,
  tagColor: meta.color,
  value,
}));

/** 时间展示：使用本地时区，缺省显示占位符 */
function formatTime(value?: string): string {
  if (!value) return "—";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "—";
  return parsed.toLocaleString();
}

/** 验证失败反馈：按验证记录精确查询，没有反馈时明确说明，不当作验证通过 */
function FeedbackPanel({
  taskID,
  stepID,
  runID,
}: {
  taskID: string;
  stepID: string;
  runID: string;
}) {
  const state = useValidationFeedback(taskID, stepID, runID);
  if (state.status === "idle" || state.status === "loading") {
    return <Typography.Text type="secondary">正在加载失败反馈…</Typography.Text>;
  }
  if (state.status === "not_found") {
    return (
      <Typography.Text type="secondary">
        该轮验证没有失败反馈记录（验证通过，或反馈未写入）。
      </Typography.Text>
    );
  }
  if (state.status === "error") {
    return <Alert description={state.error} showIcon title="加载失败反馈失败" type="warning" />;
  }
  const feedback = state.feedback;
  return (
    <Flex gap={4} vertical>
      <Space align="center" size={8}>
        <Typography.Text strong>失败反馈</Typography.Text>
        <DictTag dict={VERDICT_DICT} value={feedback.verdict} />
        <Typography.Text type="secondary">开发尝试 {feedback.developer_attempt_id}</Typography.Text>
        {feedback.infrastructure_error && (
          <Typography.Text type="warning">基础设施异常，未取得可用证据</Typography.Text>
        )}
      </Space>
      {feedback.summary && <Typography.Text type="secondary">{feedback.summary}</Typography.Text>}
      {feedback.checks.map((check) => (
        <Flex gap={4} key={`${check.check_id}-${check.check_name}`} vertical>
          <Space align="center" size={8}>
            <Typography.Text>{check.check_name || check.check_id}</Typography.Text>
            <Typography.Text type="secondary">{check.required ? "必需" : "可选"}</Typography.Text>
            <Typography.Text type={check.status === "passed" ? undefined : "danger"}>
              {checkStatusLabel(check.status)}
            </Typography.Text>
            <Typography.Text type="secondary">退出码 {check.exit_code}</Typography.Text>
            {check.timed_out && <Typography.Text type="warning">超时</Typography.Text>}
            {check.cancelled && <Typography.Text type="warning">已取消</Typography.Text>}
          </Space>
          {check.failure_reason && (
            <Typography.Text type="danger">{check.failure_reason}</Typography.Text>
          )}
          {check.error_detail && (
            <Typography.Text type="secondary">{check.error_detail}</Typography.Text>
          )}
          {check.stderr_summary && (
            <Typography.Text code type="secondary">
              {check.stderr_summary}
            </Typography.Text>
          )}
        </Flex>
      ))}
    </Flex>
  );
}

/** 一次验证记录：流程状态、结论、检查统计与逐项结果 */
function ValidationRunBlock({
  taskID,
  stepID,
  run,
  latest,
}: {
  taskID: string;
  stepID: string;
  run: ValidationHistoryResponse["records"][number];
  latest: boolean;
}) {
  const [showFeedback, setShowFeedback] = useState(false);
  return (
    <Flex gap={4} vertical>
      <Space align="center" size={8} wrap>
        {latest && <Typography.Text strong>最新验证</Typography.Text>}
        <Typography.Text code>{run.id}</Typography.Text>
        <Typography.Text type="secondary">流程 {validationStatusLabel(run.status)}</Typography.Text>
        <DictTag dict={VERDICT_DICT} value={run.verdict} />
        <Typography.Text type="secondary">
          通过 {run.passed_checks} / 未通过 {run.failed_checks} / 跳过 {run.skipped_checks}
        </Typography.Text>
        <Typography.Text type="secondary">尝试 {run.attempt_id}</Typography.Text>
        <Typography.Text type="secondary">{formatTime(run.started_at)}</Typography.Text>
      </Space>
      {run.failure_summary && (
        <Typography.Text type="danger">{run.failure_summary}</Typography.Text>
      )}
      {run.review_ran && (
        <Typography.Text type="secondary">
          语义审查：{run.review_status ?? "—"}
          {run.review_summary ? `，${run.review_summary}` : ""}
        </Typography.Text>
      )}
      {run.checks && run.checks.length > 0 && (
        <Flex gap={2} vertical>
          {run.checks.map((check) => (
            <Space align="center" key={`${run.id}-${check.check_id}`} size={8} wrap>
              <Typography.Text>{check.check_name || check.check_id}</Typography.Text>
              <Typography.Text type="secondary">{check.required ? "必需" : "可选"}</Typography.Text>
              <Typography.Text type={check.status === "passed" ? undefined : "danger"}>
                {CHECK_STATUS_LABEL[check.status] ?? check.status}
              </Typography.Text>
              <Typography.Text type="secondary">退出码 {check.exit_code}</Typography.Text>
              {check.failure_reason && (
                <Typography.Text type="danger">{check.failure_reason}</Typography.Text>
              )}
            </Space>
          ))}
        </Flex>
      )}
      <LinkButton
        onClick={() => setShowFeedback((value) => !value)}
        text={showFeedback ? "收起失败反馈" : "查看失败反馈"}
      />
      {showFeedback && <FeedbackPanel runID={run.id} stepID={stepID} taskID={taskID} />}
    </Flex>
  );
}

/** 单个尝试：状态、结论与"验证 / 初始化代码"两个动作 */
function AttemptBlock({
  attempt,
  operations,
  task,
  step,
  sessions,
}: {
  attempt: Attempt;
  operations: ExecutionOperations;
  task: Task;
  step: Step;
  sessions: StepRecords["sessions"][string];
}) {
  const busy = operations.state.running !== undefined;
  const meta = attemptStatusMeta(attempt.status);
  return (
    <Flex gap={4} vertical>
      <Space align="center" size={8} wrap>
        <Typography.Text strong>第 {attempt.attempt_number} 次尝试</Typography.Text>
        <DictTag dict={ATTEMPT_STATUS_DICT} value={attempt.status} />
        <Typography.Text code>{attempt.id}</Typography.Text>
        <Typography.Text type="secondary">退出码 {attempt.exit_code}</Typography.Text>
        <Typography.Text type="secondary">{formatTime(attempt.started_at)}</Typography.Text>
        {attempt.verification_verdict && (
          <Typography.Text type="secondary">
            验证判定 {verdictMeta(attempt.verification_verdict).label}
          </Typography.Text>
        )}
      </Space>
      {attempt.error && (
        <Typography.Text type={meta.color === "error" ? "danger" : undefined}>
          {attempt.error}
        </Typography.Text>
      )}
      {attempt.result_summary && (
        <Typography.Text type="secondary">{attempt.result_summary}</Typography.Text>
      )}
      {sessions.length > 0 && (
        <Flex gap={2} vertical>
          {sessions.map((session) => (
            <Space align="center" key={session.id} size={8} wrap>
              <Typography.Text type="secondary">会话</Typography.Text>
              <Typography.Text code>{session.id}</Typography.Text>
              <Typography.Text type="secondary">
                {session.agent_role} / {session.model}
              </Typography.Text>
              <Typography.Text type="secondary">
                {session.status}
                {session.run_status ? `（${session.run_status}）` : ""}
              </Typography.Text>
              <Typography.Text type="secondary">
                归档 {session.archive_status} / 导出 {session.export_status}
              </Typography.Text>
            </Space>
          ))}
        </Flex>
      )}
      <Space size={8} wrap>
        <Button
          disabled={busy || !canValidateAttempt(task, attempt)}
          loading={
            operations.state.running?.key === "validate" &&
            operations.state.running.attemptID === attempt.id
          }
          onClick={() => void operations.validate(step, attempt)}
          size="small"
          text={OPERATION_LABEL.validate}
        />
        <Button
          disabled={busy || !canInitializeAttempt(task, attempt)}
          loading={
            operations.state.running?.key === "initialize-code" &&
            operations.state.running.attemptID === attempt.id
          }
          onClick={() => void operations.initializeCode(step, attempt)}
          size="small"
          text={OPERATION_LABEL["initialize-code"]}
        />
        <Typography.Text type="secondary">
          验证与初始化代码都需要任务容器仍在受管理运行中，缺失时服务端返回前置条件错误。
        </Typography.Text>
      </Space>
    </Flex>
  );
}

/**
 * 步骤详情面板。
 *
 * 面板本身不做任何状态推断：步骤状态、尝试结论与验证结论全部来自接口，
 * 操作按钮的可用性只是提示，服务端仍是最终权威。
 */
export function StepDetailPanel({
  operations,
  records,
  step,
  task,
  onReload,
}: {
  operations: ExecutionOperations;
  records: LoadState<StepRecords>;
  step: Step;
  task: Task;
  onReload: () => void;
}) {
  const [showAllValidations, setShowAllValidations] = useState(false);
  const busy = operations.state.running !== undefined;
  const history = records.status === "ready" ? records.data.validations : undefined;
  const attempts = records.status === "ready" ? records.data.attempts : [];
  const sessions = records.status === "ready" ? records.data.sessions : {};
  const validations = history?.records ?? [];
  const visibleValidations = showAllValidations ? validations : validations.slice(-2);

  return (
    <Flex gap="small" vertical>
      <Space align="center" size={8} wrap>
        <Typography.Text strong>步骤详情</Typography.Text>
        {isKnownStepStatus(step.status) ? (
          <DictTag dict={STEP_STATUS_DICT} value={step.status} />
        ) : (
          <Typography.Text type="secondary">{step.status}</Typography.Text>
        )}
        <Typography.Text type="secondary">
          第 {step.sequence} 步 · {stepTypeLabel(step.step_type)}
        </Typography.Text>
        {step.current_attempt_id && (
          <Typography.Text type="warning">
            当前被尝试 {step.current_attempt_id} 占用
          </Typography.Text>
        )}
      </Space>

      <Descriptions column={2} size="small">
        <Descriptions.Item label="步骤标识">
          <Typography.Text code>{step.id}</Typography.Text>
        </Descriptions.Item>
        <Descriptions.Item label="步骤状态">{stepStatusMeta(step.status).label}</Descriptions.Item>
        <Descriptions.Item label="目标" span={2}>
          {step.objective || <Typography.Text type="secondary">未填写</Typography.Text>}
        </Descriptions.Item>
        <Descriptions.Item label="开始时间">{formatTime(step.started_at)}</Descriptions.Item>
        <Descriptions.Item label="结束时间">{formatTime(step.finished_at)}</Descriptions.Item>
        <Descriptions.Item label="验证状态" span={2}>
          {validationStateLabel(history?.state)}
        </Descriptions.Item>
        {step.error && (
          <Descriptions.Item label="失败摘要" span={2}>
            <Typography.Text type="danger">{step.error}</Typography.Text>
          </Descriptions.Item>
        )}
        {step.failure_stage && (
          <Descriptions.Item label="失败阶段" span={2}>
            {step.failure_stage}
          </Descriptions.Item>
        )}
        {step.output_summary && (
          <Descriptions.Item label="输出摘要" span={2}>
            {step.output_summary}
          </Descriptions.Item>
        )}
      </Descriptions>

      <Space size={8} wrap>
        <Button
          disabled={busy || !canExecuteStep(task, step)}
          loading={
            operations.state.running?.key === "execute-step" &&
            operations.state.running.stepID === step.id
          }
          onClick={() => void operations.executeStep(step)}
          size="small"
          text={OPERATION_LABEL["execute-step"]}
        />
        <Button
          disabled={busy || !canExecuteAndValidate(task, step)}
          loading={
            operations.state.running?.key === "execute-and-validate" &&
            operations.state.running.stepID === step.id
          }
          onClick={() => void operations.executeAndValidate(step)}
          size="small"
          text={OPERATION_LABEL["execute-and-validate"]}
          type="primary"
        />
        <Button
          disabled={busy || !canOpenRepairCycle(task, step)}
          loading={
            operations.state.running?.key === "open-repair-cycle" &&
            operations.state.running.stepID === step.id
          }
          onClick={() => void operations.openRepairCycle(step)}
          size="small"
          text={OPERATION_LABEL["open-repair-cycle"]}
        />
      </Space>
      <Typography.Text type="secondary">
        单步骤执行只调用 Developer，不包含验证；开发—验证闭环在开发通过后立即执行确定性验证，
        只有验证真实通过才会把步骤收口为已完成。开启新执行周期只登记预算，不会自动执行。
      </Typography.Text>

      {records.status === "loading" && (
        <Typography.Text type="secondary">正在加载执行记录…</Typography.Text>
      )}
      {records.status === "error" && (
        <Alert description={records.error} showIcon title="加载执行记录失败" type="error" />
      )}
      {records.status === "ready" && records.error && (
        <Alert description={records.error} showIcon title="执行记录刷新失败" type="warning" />
      )}

      {records.status === "ready" && (
        <>
          <Space align="center" size={8}>
            <Typography.Text strong>执行尝试</Typography.Text>
            <Typography.Text type="secondary">
              {attempts.length === 0 ? "还没有执行尝试" : `共 ${attempts.length} 次`}
            </Typography.Text>
            <LinkButton onClick={onReload} text="刷新" />
          </Space>
          {attempts.length === 0 ? (
            <Typography.Text type="secondary">
              该步骤还没有任何真实执行尝试；执行记录只由服务端写入，前端不会伪造。
            </Typography.Text>
          ) : (
            <Flex gap="middle" vertical>
              {attempts.map((attempt) => (
                <AttemptBlock
                  attempt={attempt}
                  key={attempt.id}
                  operations={operations}
                  sessions={sessions[attempt.id] ?? []}
                  step={step}
                  task={task}
                />
              ))}
            </Flex>
          )}

          <Space align="center" size={8}>
            <Typography.Text strong>验证历史</Typography.Text>
            <Typography.Text type="secondary">
              {validations.length === 0 ? "还没有验证记录" : `共 ${validations.length} 次`}
            </Typography.Text>
            {validations.length > 2 && (
              <LinkButton
                onClick={() => setShowAllValidations((value) => !value)}
                text={showAllValidations ? "只看最近两次" : "查看全部"}
              />
            )}
          </Space>
          {validations.length === 0 ? (
            <Typography.Text type="secondary">
              该步骤还没有验证记录：验证结论只能来自真实执行的检查，不能由前端推断。
            </Typography.Text>
          ) : (
            <Flex gap="middle" vertical>
              {visibleValidations.map((run) => (
                <ValidationRunBlock
                  key={run.id}
                  latest={run.id === history?.latest?.id}
                  run={run}
                  stepID={step.id}
                  taskID={task.id}
                />
              ))}
            </Flex>
          )}
        </>
      )}
    </Flex>
  );
}
