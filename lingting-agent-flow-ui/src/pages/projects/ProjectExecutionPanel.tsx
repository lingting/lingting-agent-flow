/**
 * 项目执行面板：执行状态、开始 / 停止动作与项目内任务列表。
 *
 * 约束：
 * - 状态、任务顺序与阻塞原因全部来自 `GET /projects/{id}/execution`，页面不推断进度；
 * - 开始 / 停止成功后重新读取服务端状态；网络中断时只提示"结果未知"并重新查询；
 * - 不提供提前启动某个后续任务的入口：顺序由方案确认固定，服务端只领取队首任务。
 */
import { ReloadOutlined } from "@ant-design/icons";
import { Alert, Descriptions, Drawer, Flex, Space, Table, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { Button, useRouter } from "lingting-react-ui";
import { useMemo } from "react";

import type { Project, ProjectExecutionSnapshot, ProjectExecutionTask } from "@/lib/api";
import {
  describeBlockingReason,
  describeCounts,
  progressSummary,
  projectExecutionStatusMeta,
  projectTaskStatusMeta,
} from "@/pages/projects/projectExecution";
import { useProjectExecution } from "@/pages/projects/useProjectExecution";

/** 面板属性 */
export type ProjectExecutionPanelProps = {
  /** 当前查看的项目 */
  project: Project;
  /** 关闭面板 */
  onClose: () => void;
};

/**
 * 开始按钮的禁用原因。
 *
 * 返回非空字符串表示禁用，并作为提示展示：禁用必须有原因，不能让用户面对一个灰按钮
 * 却不知道服务端缺什么。
 */
function describeStartDisabledReason(snapshot?: ProjectExecutionSnapshot): string | undefined {
  if (!snapshot) return "尚未读取到项目执行状态";
  if (snapshot.lease_active) return "项目已持有执行权，正在按顺序领取任务";
  if (snapshot.counts.running > 0) return "项目内已有任务在执行，等待其真实收敛";
  if (snapshot.blocked) {
    return `队首任务需要人工处置：${describeBlockingReason(snapshot.blocked_reason) ?? snapshot.blocked_reason}`;
  }
  if (snapshot.counts.pending === 0) return "项目内没有待执行任务";
  return undefined;
}

/** 停止按钮的禁用原因 */
function describeStopDisabledReason(snapshot?: ProjectExecutionSnapshot): string | undefined {
  if (!snapshot) return "尚未读取到项目执行状态";
  if (snapshot.stop_requested) return "已请求停止，正在等待当前任务真实收敛";
  if (!snapshot.lease_active && snapshot.counts.running === 0) return "项目当前没有执行";
  return undefined;
}

/** 项目执行面板 */
export function ProjectExecutionPanel({ project, onClose }: ProjectExecutionPanelProps) {
  const { navigate } = useRouter();
  const execution = useProjectExecution(project.id);
  const snapshot = execution.status === "ready" ? execution.snapshot : undefined;
  const startDisabled = describeStartDisabledReason(snapshot);
  const stopDisabled = describeStopDisabledReason(snapshot);
  const statusMeta = projectExecutionStatusMeta(snapshot?.status ?? "idle");

  const columns = useMemo<ColumnsType<ProjectExecutionTask>>(
    () => [
      {
        dataIndex: "position",
        title: "顺序",
        width: 64,
      },
      {
        dataIndex: "requirement_title",
        render: (_value, record) => (
          <Flex gap={0} vertical>
            <Typography.Text strong>{record.requirement_title || record.requirement_id}</Typography.Text>
            <Typography.Text type="secondary">
              {record.requirement_sequence === null
                ? "历史任务（无需求内顺序）"
                : `需求内第 ${record.requirement_sequence} 个任务`}
            </Typography.Text>
          </Flex>
        ),
        title: "需求",
      },
      {
        dataIndex: "status",
        render: (_value, record) => {
          const meta = projectTaskStatusMeta(record.status);
          return (
            <Space size={4} vertical>
              <Tag color={meta.color}>{meta.label}</Tag>
              {record.cancel_requested && <Tag color="warning">已请求取消</Tag>}
            </Space>
          );
        },
        title: "任务状态",
      },
      {
        dataIndex: "blocking",
        render: (_value, record) => {
          if (!record.blocking) return <Typography.Text type="secondary">—</Typography.Text>;
          return (
            <Typography.Text type="warning">
              {describeBlockingReason(record.blocking_reason) ?? record.blocking_reason}
            </Typography.Text>
          );
        },
        title: "阻塞原因",
      },
      {
        dataIndex: "id",
        render: (_value, record) => (
          <Space size={4} vertical>
            <Typography.Text code>{record.id}</Typography.Text>
            {record.failure_summary && (
              <Typography.Text type="danger">{record.failure_summary}</Typography.Text>
            )}
          </Space>
        ),
        title: "任务",
      },
      {
        dataIndex: "option",
        render: (_value, record) => (
          <Button
            onClick={() =>
              void navigate(`/requirements?requirement_id=${encodeURIComponent(record.requirement_id)}`)
            }
            text="需求进度"
            type="link"
          />
        ),
        title: "操作",
        width: 110,
      },
    ],
    [navigate],
  );

  return (
    <Drawer
      destroyOnHidden
      open
      title={`项目执行 · ${project.name}`}
      width={960}
      onClose={onClose}
    >
      <Flex gap="middle" vertical>
        {execution.action.error && (
          <Alert
            closable
            description={execution.action.error}
            showIcon
            title={execution.action.unknown ? "操作结果未知" : "操作失败"}
            type={execution.action.unknown ? "warning" : "error"}
          />
        )}
        {execution.status === "error" && (
          <Alert
            action={<Button onClick={() => void execution.reload()} size="small" text="重试" />}
            description={execution.error}
            showIcon
            title="加载项目执行状态失败"
            type="error"
          />
        )}
        {execution.status === "loading" && (
          <Typography.Text type="secondary">正在读取项目执行状态…</Typography.Text>
        )}

        {snapshot && (
          <>
            <Flex align="center" gap="small" wrap>
              <Tag color={statusMeta.color}>{statusMeta.label}</Tag>
              <Typography.Text type="secondary">{statusMeta.description}</Typography.Text>
            </Flex>

            {snapshot.blocked && (
              <Alert
                showIcon
                title="项目被前序任务阻塞，后续任务不会被自动领取"
                type="warning"
                description={
                  <Flex gap={2} vertical>
                    <Typography.Text>
                      {describeBlockingReason(snapshot.blocked_reason) ?? snapshot.blocked_reason}
                    </Typography.Text>
                    {snapshot.blocked_task_id && (
                      <Typography.Text code>{snapshot.blocked_task_id}</Typography.Text>
                    )}
                    <Typography.Text type="secondary">
                      处理方式：在任务页核验或重试该任务；处理完成后重新开始项目执行。
                    </Typography.Text>
                  </Flex>
                }
              />
            )}

            {snapshot.stop_requested && (
              <Alert
                closable={false}
                description={
                  snapshot.stopping
                    ? "已请求停止领取新任务；当前任务仍在收敛，真实结束后状态才会变为已停止。"
                    : "已请求停止领取新任务，当前没有在途任务。"
                }
                showIcon
                title="停止请求已记录"
                type="info"
              />
            )}

            <Descriptions bordered column={2} size="small">
              <Descriptions.Item label="项目进度">{progressSummary(snapshot.counts)}</Descriptions.Item>
              <Descriptions.Item label="任务统计">
                {describeCounts(snapshot.counts)}
              </Descriptions.Item>
              <Descriptions.Item label="正式需求数">{snapshot.requirements}</Descriptions.Item>
              <Descriptions.Item label="执行权">
                {snapshot.lease_active
                  ? `有效（epoch ${snapshot.lease_epoch ?? "-"}，到期 ${formatTime(snapshot.lease_expires_at)}）`
                  : "未持有"}
              </Descriptions.Item>
              <Descriptions.Item label="当前任务">
                {snapshot.current_task_id ? (
                  <Typography.Text code>{snapshot.current_task_id}</Typography.Text>
                ) : (
                  "无"
                )}
              </Descriptions.Item>
              <Descriptions.Item label="下一个任务">
                {snapshot.next_task_id ? (
                  <Typography.Text code>{snapshot.next_task_id}</Typography.Text>
                ) : (
                  "无"
                )}
              </Descriptions.Item>
            </Descriptions>

            <Space wrap>
              <Button
                disabled={Boolean(startDisabled) || execution.action.submitting}
                loading={execution.action.submitting}
                onClick={() => void execution.start()}
                text="开始执行"
                type="primary"
              />
              <Button
                danger
                disabled={Boolean(stopDisabled) || execution.action.submitting}
                onClick={() => void execution.cancel()}
                text="停止执行"
              />
              <Button
                icon={<ReloadOutlined />}
                onClick={() => void execution.reload()}
                text="刷新状态"
              />
            </Space>
            {startDisabled && (
              <Typography.Text type="secondary">开始不可用：{startDisabled}</Typography.Text>
            )}
            {stopDisabled && !snapshot.stop_requested && (
              <Typography.Text type="secondary">停止不可用：{stopDisabled}</Typography.Text>
            )}

            <Table<ProjectExecutionTask>
              columns={columns}
              dataSource={execution.tasks}
              locale={{
                emptyText: "该项目还没有正式任务：任务来自方案确认，确认后才会出现在这里",
              }}
              pagination={false}
              rowKey="id"
              size="small"
            />
          </>
        )}
      </Flex>
    </Drawer>
  );
}

/** 时间展示：使用本地时区，缺省显示占位符 */
function formatTime(value?: string): string {
  if (!value) return "—";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "—";
  return parsed.toLocaleString();
}
