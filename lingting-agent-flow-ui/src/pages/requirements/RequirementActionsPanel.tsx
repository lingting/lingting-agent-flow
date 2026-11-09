/**
 * 需求详情的停止与删除操作面板。
 *
 * 约束：
 * - 删除是显式二次确认动作：弹窗内必须展示需求标题与标识、关联任务数量、各任务真实状态、
 *   历史保留说明，以及"不自动回滚 Git、不自动处置容器"的边界；
 * - 不可删除时按钮禁用并给出原因，页面不保留"一定失败"的可点击入口；
 * - 停止只作用于仍需执行者收敛的任务；`cancelling` 只展示"停止中"，不提供重复提交；
 * - 本面板不推断执行状态：所有状态都来自服务端返回的任务记录。
 */
import { Alert, Button, Flex, Modal, Space, Table, Tag, Typography } from "antd";
import { useState } from "react";

import type { ConfirmedTask } from "@/lib/api";
import {
  describeRequirementDeletionAvailability,
  describeRequirementTasksForDeletion,
  isTaskStopping,
  stoppableTasks,
} from "@/pages/requirements/requirementActions";
import type { RequirementActionState } from "@/pages/requirements/useRequirementActions";

/** 删除确认弹窗里的任务表列定义 */
const DELETE_TASK_COLUMNS = [
  {
    dataIndex: "order",
    key: "order",
    title: "顺序",
    width: 72,
  },
  {
    dataIndex: "id",
    key: "id",
    render: (value: string) => <Typography.Text code>{value}</Typography.Text>,
    title: "任务",
  },
  {
    dataIndex: "status",
    key: "status",
    render: (value: string) => <Tag>{value}</Tag>,
    title: "真实状态",
    width: 140,
  },
];

/** 需求详情的停止与删除操作面板 */
export function RequirementActionsPanel(props: {
  action: RequirementActionState;
  /** 需求标识，用于弹窗内核对 */
  requirementID: string;
  /** 需求标题，用于弹窗内核对 */
  title: string;
  /** 需求下的正式任务，按需求内顺序排列 */
  tasks: ConfirmedTask[];
  onDelete: () => void;
  onStop: (taskIDs: string[]) => void;
}) {
  const [confirmOpen, setConfirmOpen] = useState(false);
  const availability = describeRequirementDeletionAvailability(props.tasks);
  const stoppable = stoppableTasks(props.tasks);
  const stoppingTasks = props.tasks.filter((task) => isTaskStopping(task.status));
  const busy = props.action.stopping || props.action.deleting;
  const stopReason = describeStopAvailability(stoppable, stoppingTasks);

  return (
    <Flex gap="small" vertical>
      {props.action.error && (
        <Alert
          description={props.action.error}
          showIcon
          title={props.action.unknown ? "操作结果未知" : "操作未成功"}
          type="error"
        />
      )}
      {props.action.unknown && (
        <Alert
          description={`${props.action.unknown}页面已重新读取需求与任务状态，请以服务端返回的为准。`}
          showIcon
          title="结果需要确认"
          type="warning"
        />
      )}
      {stoppingTasks.length > 0 && (
        <Alert
          description={`任务 ${stoppingTasks
            .map((task) => task.id)
            .join("、")} 的停止请求已记录，等待执行者确认真实收敛；收敛前不能删除需求。`}
          showIcon
          title="任务正在停止中"
          type="info"
        />
      )}

      <Space align="center" size={8} wrap>
        <Button
          disabled={busy || stoppable.length === 0}
          loading={props.action.stopping}
          onClick={() => props.onStop(stoppable.map((task) => task.id))}
        >
          停止任务
        </Button>
        <Button
          danger
          disabled={busy || !availability.available}
          loading={props.action.deleting}
          onClick={() => setConfirmOpen(true)}
          type="primary"
        >
          删除需求
        </Button>
        {stopReason && <Typography.Text type="secondary">{stopReason}</Typography.Text>}
        {!availability.available && availability.reason && (
          <Typography.Text type="secondary">{availability.reason}</Typography.Text>
        )}
      </Space>

      <Typography.Text type="secondary">
        停止只向该需求下的任务提交取消请求，不会停止同一项目内的其他需求；删除只是逻辑删除， 回滚
        Git 与处置容器仍然需要由你自行完成。
      </Typography.Text>

      <Modal
        cancelText="取消"
        closable={!props.action.deleting}
        confirmLoading={props.action.deleting}
        mask={{ closable: !props.action.deleting }}
        okButtonProps={{ danger: true }}
        okText="确认删除需求"
        onCancel={() => setConfirmOpen(false)}
        onOk={() => {
          setConfirmOpen(false);
          props.onDelete();
        }}
        open={confirmOpen}
        title="确认删除需求"
      >
        <Flex gap="small" vertical>
          <Typography.Text>需求标题：{props.title}</Typography.Text>
          <Typography.Text>
            需求标识：<Typography.Text code>{props.requirementID}</Typography.Text>
          </Typography.Text>
          <Typography.Text>
            关联任务：{props.tasks.length} 个
            {props.tasks.length > 0
              ? `，状态依次为 ${describeRequirementTasksForDeletion(props.tasks).join("、")}`
              : "（该需求没有关联任务）"}
          </Typography.Text>
          <Table
            columns={DELETE_TASK_COLUMNS}
            dataSource={props.tasks.map((task, index) => ({
              id: task.id,
              key: task.id,
              order: task.requirement_sequence ?? index + 1,
              status: task.status,
            }))}
            locale={{ emptyText: "该需求没有关联任务" }}
            pagination={false}
            size="small"
          />
          <Alert
            description={
              <Flex gap={2} vertical>
                <Typography.Text>
                  需求与上述任务会从默认列表与详情视图中隐藏（只写入逻辑删除时间），
                  任务原有的业务状态与最终结论不会被改写。
                </Typography.Text>
                <Typography.Text>
                  Step、Attempt、Session、Event、CodeSave 与容器记录全部保留，仍然可以查询。
                </Typography.Text>
                <Typography.Text>
                  Git 仓库不会自动回滚，容器也不会被自动删除、停止或 detach：代码回滚与容器
                  处置需要你自行处理。
                </Typography.Text>
              </Flex>
            }
            showIcon
            title="删除前请确认"
            type="warning"
          />
        </Flex>
      </Modal>
    </Flex>
  );
}

/** 说明停止按钮为什么不可用或为什么当前不需要停止 */
function describeStopAvailability(
  stoppable: ConfirmedTask[],
  stoppingTasks: ConfirmedTask[],
): string | undefined {
  if (stoppable.length > 0) return undefined;
  if (stoppingTasks.length > 0) return "已有任务的停止请求在等待真实收敛，不会重复提交。";
  return "当前没有需要停止的任务：没有任务处于执行中。";
}
