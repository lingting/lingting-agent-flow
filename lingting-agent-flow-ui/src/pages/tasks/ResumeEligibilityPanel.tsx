/**
 * 任务续跑资格与阻塞原因面板（只读）。
 *
 * 数据来自 `GET /api/v1/tasks/{taskID}/resume-eligibility`：服务端把任务状态、最近一次
 * 工作流执行记录、代码保存记录与容器前置条件聚合成一份统一结论。本面板只负责如实呈现：
 *
 *   1. 资格结论（`can_resume` + `summary`），并明确说明这是查询时刻的快照；
 *   2. 任务状态与工作流执行状态（`run` 缺省时说明服务端没有返回记录，不补造）；
 *   3. 阻塞原因（`blocking` 逐项展示，未知分类原样展示）；
 *   4. 代码保存续跑判定（`code_save_action` + `code_save_summary`）；
 *   5. 需要关注的代码保存记录（三个 Git 阶段与记录维度两个状态分别展示）；
 *   6. 服务端给出的下一步动作（原文展示，不解析成自动执行指令）。
 *
 * 本面板不发起恢复、不触发核验、不自动刷新：恢复按钮与工作流操作仍由任务页既有入口承担，
 * 且服务端在真正执行恢复前会重新检查前置条件。
 */
import { Alert, Descriptions, Flex, Space, Typography } from "antd";
import { Button, DictTag, LinkButton } from "lingting-react-ui";

import type { ResumeEligibilityCodeSave, ResumeEligibilityResponse, Task } from "@/lib/api";
import { CodeSaveStatusTag } from "@/pages/tasks/CodeSaveStatusTag";
import {
  CODE_SAVE_COMMIT_DICT,
  CODE_SAVE_MERGE_DICT,
  CODE_SAVE_PERSIST_DICT,
  CODE_SAVE_PUSH_DICT,
  CODE_SAVE_RECOVERY_DICT,
  commitStatusMeta,
  mergeStatusMeta,
  persistStatusMeta,
  pushStatusMeta,
  recoveryStatusMeta,
} from "@/pages/tasks/codeSaves";
import {
  codeSaveResumeActionMeta,
  looksLikeCliCommand,
  resumeBlockingList,
  resumeBlockingMeta,
  resumeNextActions,
  resumeSummary,
  resumeRecoveryScope,
  resumeVerdict,
} from "@/pages/tasks/resumeEligibility";
import {
  isKnownTaskStatus,
  isKnownWorkflowRunStatus,
  TASK_STATUS_META,
  WORKFLOW_RUN_STATUS_DICT,
} from "@/pages/tasks/status";
import { useResumeEligibility } from "@/pages/tasks/useResumeEligibility";

/** 时间展示：使用本地时区，缺省显示占位符 */
function formatTime(value?: string): string {
  if (!value) return "—";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "—";
  return parsed.toLocaleString();
}

/** 单条"需要关注的代码保存记录" */
function ResumeCodeSaveBlock({ record }: { record: ResumeEligibilityCodeSave }) {
  const action = codeSaveResumeActionMeta(record.action);
  return (
    <Flex
      gap={4}
      style={{ border: "1px solid rgba(5, 5, 5, 0.08)", borderRadius: 8, padding: 12 }}
      vertical
    >
      <Space align="center" size={8} wrap>
        <Typography.Text code>{record.record_id}</Typography.Text>
        <Typography.Text type="secondary">任务分支</Typography.Text>
        <Typography.Text code>{record.task_branch}</Typography.Text>
      </Space>

      <Descriptions column={1} size="small">
        <Descriptions.Item label="记录级判定">
          <Space direction="vertical" size={2}>
            <Typography.Text type={action.attention ? "warning" : "success"}>
              {action.label}
            </Typography.Text>
            <Typography.Text type="secondary">{action.description}</Typography.Text>
            {!action.known && (
              <Typography.Text type="secondary">服务端原始取值：{record.action}</Typography.Text>
            )}
            {record.summary && <Typography.Text type="secondary">{record.summary}</Typography.Text>}
            {record.error_class && (
              <Typography.Text type="secondary">
                错误分类：<Typography.Text code>{record.error_class}</Typography.Text>
              </Typography.Text>
            )}
          </Space>
        </Descriptions.Item>
        <Descriptions.Item label="提交状态">
          <CodeSaveStatusTag
            dict={CODE_SAVE_COMMIT_DICT}
            meta={commitStatusMeta(record.commit_status)}
            value={record.commit_status}
          />
        </Descriptions.Item>
        <Descriptions.Item label="推送状态">
          <CodeSaveStatusTag
            dict={CODE_SAVE_PUSH_DICT}
            meta={pushStatusMeta(record.push_status)}
            value={record.push_status}
          />
        </Descriptions.Item>
        <Descriptions.Item label="合并状态">
          <CodeSaveStatusTag
            dict={CODE_SAVE_MERGE_DICT}
            meta={mergeStatusMeta(record.merge_status)}
            value={record.merge_status}
          />
        </Descriptions.Item>
        <Descriptions.Item label="记录写入状态">
          <CodeSaveStatusTag
            dict={CODE_SAVE_PERSIST_DICT}
            meta={persistStatusMeta(record.persist_status)}
            value={record.persist_status}
          />
        </Descriptions.Item>
        <Descriptions.Item label="核验结论">
          <Space direction="vertical" size={2}>
            <CodeSaveStatusTag
              dict={CODE_SAVE_RECOVERY_DICT}
              meta={recoveryStatusMeta(record.recovery_status)}
              value={record.recovery_status}
            />
            <Typography.Text type="secondary">{resumeRecoveryScope()}</Typography.Text>
          </Space>
        </Descriptions.Item>
      </Descriptions>
    </Flex>
  );
}

/** 任务状态展示：已知取值用中文标签，未知取值原样展示服务端取值 */
function TaskStatusLine({ status }: { status: string }) {
  if (isKnownTaskStatus(status)) {
    return <Typography.Text>{TASK_STATUS_META[status].label}</Typography.Text>;
  }
  return (
    <Space size={8} wrap>
      <Typography.Text>{status}</Typography.Text>
      <Typography.Text type="secondary">服务端返回的状态取值未在当前版本中定义</Typography.Text>
    </Space>
  );
}

/** 已取得快照时的完整展示 */
function ResumeEligibilityBody({ data }: { data: ResumeEligibilityResponse }) {
  const verdict = resumeVerdict(data.can_resume);
  const blocking = resumeBlockingList(data);
  const nextActions = resumeNextActions(data);
  const codeSave = codeSaveResumeActionMeta(data.code_save_action);
  const run = data.run;

  return (
    <Flex gap="small" vertical>
      <Alert
        description={verdict.description}
        showIcon
        title={verdict.title}
        type={verdict.color === "success" ? "success" : "warning"}
      />

      <Descriptions column={1} size="small">
        <Descriptions.Item label="服务端摘要">{resumeSummary(data)}</Descriptions.Item>
        <Descriptions.Item label="任务状态">
          <TaskStatusLine status={data.task_status} />
        </Descriptions.Item>
        <Descriptions.Item label="工作流执行记录">
          {run ? (
            <Space direction="vertical" size={2}>
              <Space size={8} wrap>
                {isKnownWorkflowRunStatus(run.status) ? (
                  <DictTag dict={WORKFLOW_RUN_STATUS_DICT} value={run.status} />
                ) : (
                  <Typography.Text>{run.status}</Typography.Text>
                )}
                <Typography.Text code>{run.run_id}</Typography.Text>
                <Typography.Text type="secondary">阶段：{run.stage}</Typography.Text>
              </Space>
              <Typography.Text type="secondary">
                {`进度：已完成 ${run.completed_steps} / ${run.total_steps} 步`}
              </Typography.Text>
              {run.error_summary && (
                <Typography.Text type="danger">停止原因：{run.error_summary}</Typography.Text>
              )}
              <Typography.Text type="secondary">
                更新时间：{formatTime(run.updated_at)}
              </Typography.Text>
            </Space>
          ) : (
            <Typography.Text type="secondary">
              服务端未返回工作流执行记录；这不表示任务可以续跑，资格仍以 can_resume 为准。
            </Typography.Text>
          )}
        </Descriptions.Item>
      </Descriptions>

      <Flex gap={4} vertical>
        <Typography.Text strong>阻塞原因</Typography.Text>
        {blocking.length === 0 ? (
          <Typography.Text type="secondary">
            服务端本次没有返回阻塞项；资格结论仍以服务端给出的 can_resume 为准。
          </Typography.Text>
        ) : (
          blocking.map((code) => {
            const meta = resumeBlockingMeta(code);
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

      <Descriptions column={1} size="small" title="代码保存续跑判定">
        <Descriptions.Item label="判定">
          <Space direction="vertical" size={2}>
            <Typography.Text type={codeSave.attention ? "warning" : "success"}>
              {codeSave.label}
            </Typography.Text>
            <Typography.Text type="secondary">{codeSave.description}</Typography.Text>
            {!codeSave.known && (
              <Typography.Text type="secondary">
                服务端原始取值：{data.code_save_action}
              </Typography.Text>
            )}
          </Space>
        </Descriptions.Item>
        <Descriptions.Item label="判定说明">
          {data.code_save_summary || (
            <Typography.Text type="secondary">服务端未提供代码保存判定说明</Typography.Text>
          )}
        </Descriptions.Item>
      </Descriptions>

      <Flex gap={4} vertical>
        <Typography.Text strong>需要关注的代码保存记录</Typography.Text>
        <Typography.Text type="secondary">
          记录级判定只说明该条记录能否继续保存；提交、推送、合并与记录可信度仍是各自独立的事实。
        </Typography.Text>
        {data.code_save_records.length === 0 ? (
          <Typography.Text type="secondary">
            服务端没有返回需要关注的代码保存记录；这不代表代码已经成功保存到远端。
          </Typography.Text>
        ) : (
          data.code_save_records.map((record) => (
            <ResumeCodeSaveBlock key={record.record_id} record={record} />
          ))
        )}
      </Flex>

      <Flex gap={4} vertical>
        <Typography.Text strong>下一步动作</Typography.Text>
        {nextActions.length === 0 ? (
          <Typography.Text type="secondary">
            服务端没有提供后续动作建议；请按上方阻塞原因与真实执行记录判断下一步。
          </Typography.Text>
        ) : (
          nextActions.map((action) => (
            <Flex gap={2} key={action} vertical>
              <Typography.Text code>{action}</Typography.Text>
              <Typography.Text type="secondary">
                {looksLikeCliCommand(action)
                  ? "这是服务端给出的 CLI 建议，需由你在命令行显式执行；页面不会自动运行。"
                  : "这是服务端给出的建议文本；页面不会据此自动执行任何操作。"}
              </Typography.Text>
            </Flex>
          ))
        )}
      </Flex>
    </Flex>
  );
}

/**
 * 续跑资格面板。
 *
 * 加载中、查询成功、查询失败（带重试）、刷新失败（保留旧快照）四种状态分别呈现：
 * 查询失败绝不显示为"允许续跑"，刷新失败也不会抹掉上一次真实结果。
 */
export function ResumeEligibilityPanel({ task, token }: { task: Task; token?: number }) {
  const { state, reload } = useResumeEligibility(task.id, token);

  return (
    <Flex gap="small" vertical>
      <Space align="center" size={8} wrap>
        <Typography.Text strong>续跑资格与阻塞原因</Typography.Text>
        <LinkButton onClick={reload} text="刷新续跑资格" />
      </Space>
      <Typography.Text type="secondary">
        该结论由服务端依据任务、最近一次工作流执行记录、代码保存记录与容器前置条件计算，
        是查询时刻的只读快照。它不触发恢复或核验；真正执行恢复时服务端仍会重新检查前置条件。
      </Typography.Text>

      {state.status === "loading" && (
        <Typography.Text type="secondary">正在查询续跑资格…</Typography.Text>
      )}
      {state.status === "error" && (
        <Alert
          action={<Button onClick={reload} size="small" text="重试" />}
          description={state.error}
          showIcon
          title="查询续跑资格失败"
          type="error"
        />
      )}
      {state.status === "ready" && state.error && (
        <Alert
          action={<Button onClick={reload} size="small" text="重试" />}
          description={`${state.error} 下方展示的是上一次成功查询的结果，不是最新状态。`}
          showIcon
          title="续跑资格刷新失败"
          type="warning"
        />
      )}

      {state.status === "ready" && <ResumeEligibilityBody data={state.data} />}
    </Flex>
  );
}
