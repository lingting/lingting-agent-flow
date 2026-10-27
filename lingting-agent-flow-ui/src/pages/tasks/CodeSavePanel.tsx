/**
 * 任务代码保存记录面板。
 *
 * 记录属于任务维度，不属于某一个被选中的步骤：一条记录通过 `attempt_id` 与执行
 * 历史关联，但展示位置固定在任务详情下，避免把"代码保存"错误地挂到某个步骤。
 *
 * 五组事实各自独立展示，任何一组都不能替代另一组：
 *   1. 基本信息（记录标识、关联尝试、分支、时间、阶段）；
 *   2. 提交结果（`commit_status` + `commit_sha` + `commit_message`）；
 *   3. 推送结果（`push_status` + `remote_commit_sha`）；
 *   4. 合并结果（`merge_enabled` + `merge_status` + `merge_sha`）；
 *   5. 记录可信度与核验结论（`persist_status` / `recovery_status`）。
 *
 * 面板本身不提交、不推送、不合并，也不自动核验：每条记录各自提供一个显式核验入口，
 * 由用户点击后调用 `POST …/code-saves/{recordID}/verify-code-save`，结果按记录标识归档
 * 并展示在记录自己的区域里。
 */
import { Alert, Descriptions, Flex, Space, Typography } from "antd";
import { Button, LinkButton } from "lingting-react-ui";

import type { CodeSaveRecord, Task } from "@/lib/api";
import { CodeSaveStatusTag as StatusTag } from "@/pages/tasks/CodeSaveStatusTag";
import { CodeSaveVerificationSection } from "@/pages/tasks/CodeSaveVerificationSection";
import {
  CODE_SAVE_COMMIT_DICT,
  CODE_SAVE_MERGE_DICT,
  CODE_SAVE_PERSIST_DICT,
  CODE_SAVE_PUSH_DICT,
  CODE_SAVE_RECOVERY_DICT,
  commitStatusMeta,
  mergeStatusMeta,
  mergeSummary,
  mergeWarning,
  persistStatusMeta,
  pushStatusMeta,
  pushSummary,
  recordWarnings,
  recoveryScope,
  recoveryStatusMeta,
  stageLabel,
} from "@/pages/tasks/codeSaves";
import type { CodeSaveVerifyEntry } from "@/pages/tasks/codeSaveVerification";
import { useCodeSaveVerification } from "@/pages/tasks/useCodeSaveVerification";
import { useTaskCodeSaves } from "@/pages/tasks/useExecutionRecords";

/** 时间展示：使用本地时区，缺省显示占位符 */
function formatTime(value?: string): string {
  if (!value) return "—";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "—";
  return parsed.toLocaleString();
}

/**
 * 单条记录的展示。
 *
 * 记录自身的五组事实与核验结果是两块独立内容：核验只补充"现场看到了什么、判定是什么、
 * 结论有没有落盘"，不会改写记录本身的字段，也不会影响其他记录。
 */
function CodeSaveRecordBlock({
  entry,
  onClearVerify,
  onVerify,
  record,
}: {
  entry?: CodeSaveVerifyEntry;
  onClearVerify: () => void;
  onVerify: () => void;
  record: CodeSaveRecord;
}) {
  const commit = commitStatusMeta(record.commit_status);
  const push = pushStatusMeta(record.push_status);
  const merge = mergeStatusMeta(record.merge_status);
  const persist = persistStatusMeta(record.persist_status);
  const recovery = recoveryStatusMeta(record.recovery_status);
  const warnings = recordWarnings(record);
  const pushNote = pushSummary(record);
  const mergeNote = mergeWarning(record);

  return (
    <Flex
      gap={4}
      style={{ border: "1px solid rgba(5, 5, 5, 0.08)", borderRadius: 8, padding: 12 }}
      vertical
    >
      <Space align="center" size={8} wrap>
        <Typography.Text code>{record.id}</Typography.Text>
        <Typography.Text type="secondary">阶段：{stageLabel(record.stage)}</Typography.Text>
        {record.attempt_id ? (
          <Typography.Text type="secondary">关联尝试 {record.attempt_id}</Typography.Text>
        ) : (
          <Typography.Text type="secondary">未关联执行尝试</Typography.Text>
        )}
      </Space>

      <Descriptions column={2} size="small">
        <Descriptions.Item label="任务分支">
          <Typography.Text code>{record.task_branch}</Typography.Text>
        </Descriptions.Item>
        <Descriptions.Item label="基础分支">
          {record.base_branch || <Typography.Text type="secondary">未识别</Typography.Text>}
        </Descriptions.Item>
        <Descriptions.Item label="远程名称">{record.remote_name}</Descriptions.Item>
        <Descriptions.Item label="仓库地址">
          {record.repository || <Typography.Text type="secondary">未记录</Typography.Text>}
        </Descriptions.Item>
        <Descriptions.Item label="创建时间">{formatTime(record.created_at)}</Descriptions.Item>
        <Descriptions.Item label="开始时间">{formatTime(record.started_at)}</Descriptions.Item>
        <Descriptions.Item label="结束时间">{formatTime(record.finished_at)}</Descriptions.Item>
        <Descriptions.Item label="更新时间">{formatTime(record.updated_at)}</Descriptions.Item>
      </Descriptions>

      <Descriptions column={1} size="small" title="提交">
        <Descriptions.Item label="提交状态">
          <Space size={8} wrap>
            <StatusTag dict={CODE_SAVE_COMMIT_DICT} meta={commit} value={record.commit_status} />
            {record.commit_message && <Typography.Text>{record.commit_message}</Typography.Text>}
          </Space>
        </Descriptions.Item>
        {record.commit_sha && (
          <Descriptions.Item label="提交 commit">
            <Typography.Text code>{record.commit_sha}</Typography.Text>
          </Descriptions.Item>
        )}
      </Descriptions>

      <Descriptions column={1} size="small" title="推送">
        <Descriptions.Item label="推送状态">
          <StatusTag dict={CODE_SAVE_PUSH_DICT} meta={push} value={record.push_status} />
        </Descriptions.Item>
        {record.remote_commit_sha && (
          <Descriptions.Item label="远程 commit">
            <Typography.Text code>{record.remote_commit_sha}</Typography.Text>
          </Descriptions.Item>
        )}
        {pushNote && (
          <Descriptions.Item label="推送说明">
            <Typography.Text type="secondary">{pushNote}</Typography.Text>
          </Descriptions.Item>
        )}
      </Descriptions>

      <Descriptions column={1} size="small" title="合并">
        <Descriptions.Item label="是否请求合并">
          {record.merge_enabled ? "已请求合并基础分支" : "未请求合并基础分支"}
        </Descriptions.Item>
        <Descriptions.Item label="合并状态">
          <StatusTag dict={CODE_SAVE_MERGE_DICT} meta={merge} value={record.merge_status} />
        </Descriptions.Item>
        {record.merge_sha && (
          <Descriptions.Item label="合并后 commit">
            <Typography.Text code>{record.merge_sha}</Typography.Text>
          </Descriptions.Item>
        )}
        <Descriptions.Item label="合并说明">
          <Typography.Text type="secondary">{mergeSummary(record)}</Typography.Text>
        </Descriptions.Item>
      </Descriptions>

      <Descriptions column={1} size="small" title="记录可信度与核验">
        <Descriptions.Item label="记录写入状态">
          <Space size={8} wrap>
            <StatusTag dict={CODE_SAVE_PERSIST_DICT} meta={persist} value={record.persist_status} />
            {record.persist_error && (
              <Typography.Text type="danger">{record.persist_error}</Typography.Text>
            )}
          </Space>
        </Descriptions.Item>
        <Descriptions.Item label="核验结论">
          <Space direction="vertical" size={2}>
            <StatusTag
              dict={CODE_SAVE_RECOVERY_DICT}
              meta={recovery}
              value={record.recovery_status}
            />
            <Typography.Text type="secondary">{recoveryScope(record)}</Typography.Text>
          </Space>
        </Descriptions.Item>
        {record.error_code && (
          <Descriptions.Item label="错误分类">
            <Typography.Text code>{record.error_code}</Typography.Text>
          </Descriptions.Item>
        )}
        {record.error_summary && (
          <Descriptions.Item label="失败摘要">
            <Typography.Text type="danger">{record.error_summary}</Typography.Text>
          </Descriptions.Item>
        )}
      </Descriptions>

      {mergeNote && <Typography.Text type="warning">{mergeNote}</Typography.Text>}
      {warnings.map((warning) => (
        <Typography.Text key={warning} type="warning">
          {warning}
        </Typography.Text>
      ))}

      <CodeSaveVerificationSection entry={entry} onClear={onClearVerify} onVerify={onVerify} />
    </Flex>
  );
}

/**
 * 代码保存记录面板。
 *
 * 加载中、加载失败（带重试）、空记录、有记录、刷新失败但保留旧数据五种状态分别
 * 呈现：空记录只说明"该任务当前没有代码保存记录"，不代表任务成功或工作流完成。
 */
export function CodeSavePanel({ task, token }: { task: Task; token?: number }) {
  const { state, reload } = useTaskCodeSaves(task.id, token);
  // 核验是独立的操作状态：它不会写入记录列表，也不会因为列表刷新失败而丢失。
  const verification = useCodeSaveVerification({ refresh: reload, taskID: task.id });

  return (
    <Flex gap="small" vertical>
      <Space align="center" size={8} wrap>
        <Typography.Text strong>代码保存记录</Typography.Text>
        <LinkButton onClick={reload} text="刷新代码保存" />
      </Space>
      <Typography.Text type="secondary">
        记录按服务端顺序展示（最新在前）。提交、推送、合并是三个独立事实；记录写入状态与
        核验结论同样独立。每条记录可以单独发起只读核验，本面板不触发任何 Git 操作。
      </Typography.Text>

      {state.status === "loading" && (
        <Typography.Text type="secondary">正在加载代码保存记录…</Typography.Text>
      )}
      {state.status === "error" && (
        <Alert
          action={<Button onClick={reload} size="small" text="重试" />}
          description={state.error}
          showIcon
          title="加载代码保存记录失败"
          type="error"
        />
      )}
      {state.status === "ready" && state.error && (
        <Alert
          action={<Button onClick={reload} size="small" text="重试" />}
          description={`${state.error} 下方展示的是上一次成功获取的记录，可能不是最新状态。`}
          showIcon
          title="代码保存记录刷新失败"
          type="warning"
        />
      )}

      {state.status === "ready" &&
        (state.data.length === 0 ? (
          <Typography.Text type="secondary">
            该任务当前没有代码保存记录。这只说明还没有产生保存记录，不代表任务成功、代码无须
            保存或工作流已经完成。
          </Typography.Text>
        ) : (
          <Flex gap="small" vertical>
            <Typography.Text type="secondary">{`共 ${state.data.length} 条记录`}</Typography.Text>
            {state.data.map((record) => (
              <CodeSaveRecordBlock
                entry={verification.entries[record.id]}
                key={record.id}
                onClearVerify={() => verification.clearEntry(record.id)}
                onVerify={() => void verification.verify(record.id)}
                record={record}
              />
            ))}
          </Flex>
        ))}
    </Flex>
  );
}
