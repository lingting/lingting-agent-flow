/**
 * 方案确认面板：采纳某一版 Revision 并展示服务端真实生成的正式实体。
 *
 * 规则：
 * - 确认只能针对当前选中的 Revision，页面不允许改写任务列表或任务顺序；
 * - 二次确认是显式动作（弹窗内再次确认），不是普通导航按钮；
 * - 确认成功后只展示服务端返回的需求与任务，不提供"开始执行"入口；
 * - 已确认会话只读展示采纳结果；读取不到时如实说明，不补造实体。
 */
import { Alert, Button, Descriptions, Flex, Modal, Space, Table, Tag, Typography } from "antd";
import { useState } from "react";

import type { PlanningRevision } from "@/lib/api";
import type {
  PlanningAdoptedState,
  PlanningConfirmState,
} from "@/pages/planning/hooks/usePlanningConfirm";
import {
  confirmedTaskRows,
  describePlanningConfirmSummary,
  formatPlanningTime,
  type ConfirmedTaskRow,
  type PlanningConfirmAvailability,
} from "@/pages/planning/planning";

/** 确认任务表的列定义 */
const CONFIRM_TASK_COLUMNS = [
  {
    dataIndex: "order",
    key: "order",
    render: (value: number | undefined) => (value === undefined ? "—" : value),
    title: "需求内顺序",
    width: 110,
  },
  { dataIndex: "title", key: "title", title: "任务标题" },
  {
    dataIndex: "status",
    key: "status",
    render: (value: string) => (
      <Tag color={value === "queued" ? "default" : "processing"}>{value}</Tag>
    ),
    title: "状态",
    width: 140,
  },
  {
    dataIndex: "task_id",
    key: "task_id",
    render: (value: string) => <Typography.Text code>{value}</Typography.Text>,
    title: "任务标识",
  },
];

/** 确认后的结果展示：真实需求与任务 */
function ConfirmedResult(props: {
  requirementID: string;
  title: string;
  status: string;
  tasks: ConfirmedTaskRow[];
}) {
  return (
    <Flex gap="small" vertical>
      <Descriptions
        column={1}
        items={[
          {
            children: <Typography.Text code>{props.requirementID}</Typography.Text>,
            key: "id",
            label: "正式需求标识",
          },
          { children: props.title, key: "title", label: "需求标题" },
          { children: <Tag color="default">{props.status}</Tag>, key: "status", label: "需求状态" },
          {
            children: `${props.tasks.length} 个正式任务，顺序由服务端按下标写入`,
            key: "tasks",
            label: "任务数量",
          },
        ]}
        size="small"
      />
      <Table<ConfirmedTaskRow>
        columns={CONFIRM_TASK_COLUMNS}
        dataSource={props.tasks}
        pagination={false}
        rowKey="key"
        size="small"
      />
      <Alert
        description="任务已生成并保持待执行状态（queued）。本阶段不提供开始执行入口，调度属于后续里程碑。"
        showIcon
        title="任务保持待执行"
        type="info"
      />
    </Flex>
  );
}

/** 方案确认面板 */
export function PlanningConfirmPanel(props: {
  availability: PlanningConfirmAvailability;
  adopted: PlanningAdoptedState;
  confirmState: PlanningConfirmState;
  onConfirm: (revisionID: string) => void;
  revision?: PlanningRevision;
  /** 会话已确认：只读展示采纳结果，不再提供确认入口 */
  confirmed: boolean;
}) {
  const [modalOpen, setModalOpen] = useState(false);
  const summary = describePlanningConfirmSummary(props.revision);
  const confirming = props.confirmState.status === "confirming";
  const succeeded =
    props.confirmState.status === "succeeded" ? props.confirmState.result : undefined;

  return (
    <Flex gap="middle" vertical>
      <Typography.Text strong>方案确认</Typography.Text>

      {props.confirmState.status === "failed" && (
        <Alert
          description={
            props.confirmState.refreshError
              ? `${props.confirmState.error.message} ${props.confirmState.refreshError}`
              : props.confirmState.error.message
          }
          showIcon
          title={props.confirmState.error.conflict ? "确认被拒绝（状态冲突）" : "确认未成功"}
          type="error"
        />
      )}

      {succeeded && (
        <Alert
          description={`已采纳 Revision ${succeeded.adopted_revision_id}，确认时间 ${formatPlanningTime(
            succeeded.requirement.confirmed_at,
          )}。`}
          showIcon
          title="已生成正式需求与任务"
          type="success"
        />
      )}

      {succeeded ? (
        <ConfirmedResult
          requirementID={succeeded.requirement.id}
          status={succeeded.requirement.status}
          tasks={confirmedTaskRows(succeeded.tasks)}
          title={succeeded.requirement.title}
        />
      ) : props.adopted.status === "loading" ? (
        <Typography.Text type="secondary">正在读取该会话采纳生成的正式需求…</Typography.Text>
      ) : props.adopted.status === "error" ? (
        <Alert description={props.adopted.error} showIcon title="未能读取采纳结果" type="warning" />
      ) : props.adopted.status === "ready" ? (
        <ConfirmedResult
          requirementID={props.adopted.requirement.id}
          status={props.adopted.requirement.status}
          tasks={confirmedTaskRows(props.adopted.tasks)}
          title={props.adopted.requirement.title}
        />
      ) : !props.confirmed && summary ? (
        <Flex gap="small" vertical>
          <Space align="center" size={8} wrap>
            <Tag color="processing">{summary.revisionLabel}</Tag>
            <Typography.Text type="secondary">
              将生成 1 条正式需求与 {summary.taskCount} 个正式任务，任务顺序与服务端写入
              requirement_sequence 的顺序一致。
            </Typography.Text>
          </Space>
          <Typography.Text type="secondary">
            确认前请在弹窗中核对将生成的需求标题、需求描述与任务顺序；任务列表由服务端依据该方案版本计算，确认后不可修改。
          </Typography.Text>

          <Space align="center" size={8} wrap>
            <Button
              disabled={!props.availability.available}
              loading={confirming}
              onClick={() => setModalOpen(true)}
              type="primary"
            >
              确认采纳该版方案
            </Button>
            {props.availability.reason && (
              <Typography.Text type="secondary">{props.availability.reason}</Typography.Text>
            )}
          </Space>
        </Flex>
      ) : (
        <Alert
          description={
            props.confirmed
              ? "该会话已确认，但页面没有读取到它生成的正式需求；请刷新后重试，页面不会补造结果。"
              : "该会话还没有可确认的方案版本。"
          }
          showIcon
          title={props.confirmed ? "未读取到采纳结果" : "没有可确认的方案"}
          type="info"
        />
      )}

      <Modal
        cancelText="取消"
        closable={!confirming}
        confirmLoading={confirming}
        mask={{ closable: !confirming }}
        okText="确认生成正式需求"
        onCancel={() => setModalOpen(false)}
        onOk={() => {
          if (!summary) return;
          setModalOpen(false);
          props.onConfirm(summary.revisionID);
        }}
        open={modalOpen}
        title="确认采纳方案并生成正式实体"
      >
        {summary && (
          <Flex gap="small" vertical>
            <Typography.Text>
              将采纳 {summary.revisionLabel}（Revision 标识{" "}
              <Typography.Text code>{summary.revisionID}</Typography.Text>）。
            </Typography.Text>
            <Typography.Text>需求标题：{summary.requirement.title}</Typography.Text>
            <Typography.Text>
              任务数量：{summary.taskCount} 个，顺序为
              {summary.tasks.map((task) => ` ${task.order}. ${task.title}`).join("；")}。
            </Typography.Text>
            <Alert
              description="确认会在同一个事务内创建 1 条正式需求与全部正式任务，并把规划会话推进为已确认。任一写入失败都会整体回滚，不会留下半成品；生成后的任务保持待执行，不会自动开始执行。"
              showIcon
              title="确认后生成正式实体"
              type="warning"
            />
            <Typography.Text type="secondary">
              任务列表、任务顺序与任务配置由服务端依据该方案版本计算，确认后不可修改。
            </Typography.Text>
          </Flex>
        )}
      </Modal>
    </Flex>
  );
}
