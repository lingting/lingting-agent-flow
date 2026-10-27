/**
 * 单条代码保存记录的核验操作与核验结果展示。
 *
 * 与 `CodeSavePanel` 的分工：面板负责列出记录，本文件只负责"这一条记录的核验入口"
 * 与"最近一次核验的真实结果"。所有取值都来自 `POST …/verify-code-save` 的真实响应，
 * 任何一块缺失都不由前端补造。
 *
 * 四个区域必须分别呈现，任何两个都不能互相替代：
 *   A. 现场观察（`observation`）——只说明只读查询看到了什么；
 *   B. 核验判定（`decision`）——领域规则给出的结论、阶段、说明与 `safe_to_retry`；
 *   C. 核验后的记录（`updated`）——与核验前的 `record` 逐维度对照；
 *   D. 结果持久化（`persisted`）——只说明结论有没有写回数据库。
 */
import { Alert, Descriptions, Flex, Space, Typography } from "antd";
import { Button } from "lingting-react-ui";

import type { CodeSaveVerifyResponse } from "@/lib/api";
import { CodeSaveStatusTag as StatusTag } from "@/pages/tasks/CodeSaveStatusTag";
import { CODE_SAVE_RECOVERY_DICT, type CodeSaveStatusMeta } from "@/pages/tasks/codeSaves";
import {
  canSubmitVerify,
  compareRecordSnapshots,
  decisionView,
  observationRows,
  verifyPersistView,
  type CodeSaveVerifyEntry,
} from "@/pages/tasks/codeSaveVerification";

/** 单条记录的核验区块 */
export function CodeSaveVerificationSection({
  entry,
  onClear,
  onVerify,
}: {
  entry?: CodeSaveVerifyEntry;
  onClear: () => void;
  onVerify: () => void;
}) {
  const running = entry?.running === true;
  return (
    <Flex gap={4} vertical>
      <Space align="center" size={8} wrap>
        <Typography.Text strong>代码保存核验</Typography.Text>
        <Button
          disabled={!canSubmitVerify(entry)}
          loading={running}
          onClick={onVerify}
          size="small"
          text={running ? "核验中" : "核验代码保存"}
          tooltip="核验是一次只读请求：服务端只执行只读 Git 查询并写回核验结论，不会提交、推送、合并或删除容器。"
        />
      </Space>
      <Typography.Text type="secondary">
        核验由你显式触发，不会因为页面刷新、自动刷新或任务切换而自动发起；请求失败或结果不确定时也不会自动重发。
      </Typography.Text>

      {running && (
        <Alert
          description="请求已发出，服务端正在容器内执行只读 Git 查询。请等待真实结果返回后再进行下一步。"
          showIcon
          title="正在核验代码保存"
          type="info"
        />
      )}
      {entry?.failure && (
        <Alert
          closable
          description={`${entry.failure} 本次请求没有取得任何可展示的核验结论，页面不会自动重新核验。`}
          onClose={onClear}
          showIcon
          title="核验请求失败"
          type="error"
        />
      )}
      {entry?.uncertain && (
        <Alert description={entry.uncertain} showIcon title="请求结果无法确认" type="warning" />
      )}
      {entry?.refreshError && (
        <Alert
          description={`${entry.refreshError} 上方的核验结果仍然有效，但代码保存记录列表可能不是最新状态。`}
          showIcon
          title="核验后刷新记录失败"
          type="warning"
        />
      )}
      {entry?.response && <VerifyResult result={entry.response} />}
    </Flex>
  );
}

/** 一次核验的真实结果：观察 / 判定 / 更新后记录 / 持久化四块分别呈现 */
function VerifyResult({ result }: { result: CodeSaveVerifyResponse }) {
  const decision = decisionView(result.decision);
  const persist = verifyPersistView(result.persisted);
  const rows = observationRows(result.observation);
  const comparison = compareRecordSnapshots(result.record, result.updated);
  const changed = comparison.filter((row) => row.changed);

  return (
    <Flex gap={4} vertical>
      <Descriptions column={1} size="small" title="A. 现场观察（只读查询见到的事实）">
        {rows.map((row) => (
          <Descriptions.Item key={row.key} label={row.label}>
            {row.missing ? (
              <Typography.Text type="secondary">{row.value}</Typography.Text>
            ) : (
              <Typography.Text code={row.key !== "read_failed" && row.key !== "read_error"}>
                {row.value}
              </Typography.Text>
            )}
          </Descriptions.Item>
        ))}
      </Descriptions>

      <Descriptions column={1} size="small" title="B. 核验判定（领域规则给出的结论）">
        <Descriptions.Item label="判定状态">
          <DecisionStatusTag meta={decision.status} value={result.decision.status} />
        </Descriptions.Item>
        <Descriptions.Item label="判定阶段">{decision.stage}</Descriptions.Item>
        <Descriptions.Item label="判定说明">{decision.summary}</Descriptions.Item>
        <Descriptions.Item label="是否可安全重试">
          <Space direction="vertical" size={2}>
            <Typography.Text type={decision.safeToRetry.attention ? "warning" : undefined}>
              {decision.safeToRetry.label}
            </Typography.Text>
            <Typography.Text type="secondary">{decision.safeToRetry.description}</Typography.Text>
          </Space>
        </Descriptions.Item>
      </Descriptions>

      <Descriptions column={1} size="small" title="C. 核验后的记录（服务端写回后读取到的真实记录）">
        {comparison.map((row) => (
          <Descriptions.Item key={row.key} label={row.label}>
            {row.changed ? (
              <Typography.Text>{`${row.before} → ${row.after}（核验前后不同）`}</Typography.Text>
            ) : (
              <Typography.Text type="secondary">{`${row.after}（核验前后相同）`}</Typography.Text>
            )}
          </Descriptions.Item>
        ))}
      </Descriptions>
      <Typography.Text type="secondary">
        {changed.length === 0
          ? "本次核验没有改变记录的提交、推送、合并、写入与核验结论；这不代表核验没有发生。"
          : "上方的核验后记录只描述这一条记录；其他代码保存记录以列表中的服务端数据为准。"}
      </Typography.Text>

      <Alert
        description={persist.description}
        showIcon
        title={`D. ${persist.title}`}
        type={persist.type}
      />
    </Flex>
  );
}

/** 判定状态标签：复用代码保存核验结论的字典，未知取值原样展示 */
function DecisionStatusTag({ meta, value }: { meta: CodeSaveStatusMeta; value?: string }) {
  return <StatusTag dict={CODE_SAVE_RECOVERY_DICT} meta={meta} value={value} />;
}
