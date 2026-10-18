import { PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import type { ActionType } from "@ant-design/pro-components";
import { Alert, Descriptions, Flex, Space, Typography } from "antd";
import {
  Button,
  DictSelect,
  DictTag,
  type DictItem,
  type ExTableColumn,
  type ExTableRequestParams,
  ExTable,
  LinkButton,
} from "lingting-react-ui";
import { useCallback, useMemo, useRef, useState } from "react";

import {
  ApiError,
  cancelTask,
  deleteTask,
  fetchTask,
  fetchTasks,
  type Task,
  type TaskStatus,
} from "@/lib/api";
import { CreateTaskModal } from "@/pages/tasks/CreateTaskModal";
import { canCancelTask, canDeleteTask, TASK_STATUS_META } from "@/pages/tasks/status";

/** 默认页大小，与后端默认值一致 */
const DEFAULT_PAGE_SIZE = 20;

/**
 * 任务状态字典。
 *
 * 取值与中文标签完全来自 `status.ts` 的单一映射，这里只把它转换成 DictTag 需要的形状，
 * 不在页面里重新定义状态名或语义。
 */
const STATUS_DICT: DictItem<string>[] = Object.entries(TASK_STATUS_META).map(([value, meta]) => ({
  label: meta.label,
  tagColor: meta.color,
  value,
}));

/** 把未知错误转成可读说明，保留服务端返回的业务提示 */
function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动后重试。";
    if (error.kind === "timeout") return "请求超时，请稍后重试。";
    return error.message;
  }
  return error instanceof Error ? error.message : "未知错误";
}

/** 时间展示：使用本地时区，缺省显示占位符 */
function formatTime(value?: string): string {
  if (!value) return "—";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "—";
  return parsed.toLocaleString();
}

/** 任务详情面板：只展示接口实际返回的字段 */
function TaskDetail({ task }: { task: Task }) {
  return (
    <Descriptions column={1} size="small">
      <Descriptions.Item label="任务标识">
        <Typography.Text code>{task.id}</Typography.Text>
      </Descriptions.Item>
      <Descriptions.Item label="描述">{task.description}</Descriptions.Item>
      <Descriptions.Item label="状态">
        <DictTag dict={STATUS_DICT} value={task.status} />
      </Descriptions.Item>
      <Descriptions.Item label="关联项目">
        {task.project_id || <Typography.Text type="secondary">未关联</Typography.Text>}
      </Descriptions.Item>
      <Descriptions.Item label="创建时间">{formatTime(task.created_at)}</Descriptions.Item>
      <Descriptions.Item label="更新时间">{formatTime(task.updated_at)}</Descriptions.Item>
      <Descriptions.Item label="开始时间">{formatTime(task.started_at)}</Descriptions.Item>
      <Descriptions.Item label="结束时间">{formatTime(task.finished_at)}</Descriptions.Item>
      {task.cancel_requested && (
        <Descriptions.Item label="取消请求时间">
          {formatTime(task.cancel_requested_at)}
        </Descriptions.Item>
      )}
      {task.error && (
        <Descriptions.Item label="错误摘要">
          <Typography.Text type="danger">{task.error}</Typography.Text>
        </Descriptions.Item>
      )}
    </Descriptions>
  );
}

/**
 * 任务页面：展示真实任务数据，支持创建、查看详情、取消与删除。
 *
 * 页面不做任何状态推断：状态一律按接口返回值展示，不会把尚未执行的任务显示为成功或失败；
 * 取消与删除的可用性依据接口返回的 `status_finished`，与服务端规则保持一致。
 */
export function TasksPage() {
  const actionRef = useRef<ActionType>(undefined);
  const [statuses, setStatuses] = useState<TaskStatus[]>([]);
  const [loadError, setLoadError] = useState<string>();
  const [actionError, setActionError] = useState<string>();
  const [selected, setSelected] = useState<Task>();
  const [detailLoading, setDetailLoading] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);

  /** 重新拉取当前页真实数据 */
  const reload = useCallback(() => void actionRef.current?.reload(), []);

  const openDetail = useCallback(async (taskID: string) => {
    setDetailLoading(true);
    setActionError(undefined);
    try {
      const { data } = await fetchTask(taskID);
      setSelected(data);
    } catch (cause) {
      setSelected(undefined);
      setActionError(describeError(cause));
    } finally {
      setDetailLoading(false);
    }
  }, []);

  const handleCancel = useCallback(
    async (task: Task) => {
      setActionError(undefined);
      try {
        await cancelTask(task.id);
        reload();
        if (selected?.id === task.id) {
          await openDetail(task.id);
        }
      } catch (cause) {
        setActionError(describeError(cause));
      }
    },
    [openDetail, reload, selected?.id],
  );

  const handleDelete = useCallback(
    async (task: Task) => {
      setActionError(undefined);
      try {
        await deleteTask(task.id);
        if (selected?.id === task.id) {
          setSelected(undefined);
        }
        reload();
      } catch (cause) {
        setActionError(describeError(cause));
      }
    },
    [reload, selected?.id],
  );

  const columns = useMemo<ExTableColumn<Task & Record<string, unknown>>[]>(
    () => [
      {
        dataIndex: "name",
        render: (_dom, record) => (
          <Flex gap={4} vertical>
            <LinkButton onClick={() => void openDetail(record.id)} text={record.name} />
            <Typography.Text type="secondary" ellipsis style={{ maxWidth: 320 }}>
              {record.description}
            </Typography.Text>
          </Flex>
        ),
        search: false,
        title: "任务名称",
      },
      {
        dataIndex: "status",
        dict: STATUS_DICT,
        render: (_dom, record) => <DictTag dict={STATUS_DICT} value={record.status} />,
        search: false,
        title: "状态",
      },
      {
        dataIndex: "created_at",
        render: (_dom, record) => formatTime(record.created_at),
        search: false,
        title: "创建时间",
      },
      {
        dataIndex: "updated_at",
        render: (_dom, record) => formatTime(record.updated_at),
        search: false,
        title: "更新时间",
      },
      {
        dataIndex: "option",
        render: (_dom, record) => (
          <Space>
            <LinkButton onClick={() => void openDetail(record.id)} text="详情" />
            <LinkButton
              disabled={!canCancelTask(record)}
              onClick={() => void handleCancel(record)}
              text="取消"
            />
            <LinkButton
              confirm={{
                description: "删除后任务不再出现在列表中，历史记录仍会保留。",
                title: "确认删除该任务？",
              }}
              danger
              disabled={!canDeleteTask(record)}
              onConfirm={() => handleDelete(record)}
              text="删除"
            />
          </Space>
        ),
        search: false,
        title: "操作",
        valueType: "option",
      },
    ],
    [handleCancel, handleDelete, openDetail],
  );

  /**
   * 分页请求。
   *
   * 筛选条件通过闭包读取页面状态：ProTable 会在 request 变化时自动重新请求，
   * 因此切换状态筛选后列表与总数都会刷新；`current` 与 `size` 仍由分页控件提供。
   *
   * 请求失败时清空数据并记录错误，由页面顶部的提示给出重试入口，不把失败伪装成空列表。
   */
  const request = useCallback(
    async (params: { current: number; size: number }, query: { statuses?: TaskStatus[] }) => {
      setLoadError(undefined);
      const selected = query.statuses ?? [];
      try {
        const { data } = await fetchTasks({
          current: params.current,
          size: params.size,
          status: selected.length > 0 ? selected : undefined,
        });
        return { records: data.records, total: data.total };
      } catch (cause) {
        setLoadError(describeError(cause));
        return { records: [], total: 0 };
      }
    },
    [],
  );

  return (
    <Flex gap="middle" vertical style={{ width: "100%" }}>
      {loadError && (
        <Alert
          action={<Button onClick={reload} size="small" text="重试" />}
          description={loadError}
          showIcon
          title="加载任务失败"
          type="error"
        />
      )}
      {actionError && (
        <Alert closable description={actionError} showIcon title="操作失败" type="error" />
      )}

      {selected && (
        <Flex gap="small" vertical>
          <Flex align="center" gap="small" justify="space-between">
            <Typography.Text strong>任务详情</Typography.Text>
            <Button onClick={() => setSelected(undefined)} size="small" text="关闭" type="text" />
          </Flex>
          {detailLoading ? (
            <Typography.Text type="secondary">正在加载详情…</Typography.Text>
          ) : (
            <TaskDetail task={selected} />
          )}
        </Flex>
      )}

      <ExTable<Task & Record<string, unknown>, { statuses: TaskStatus[] }>
        actionRef={actionRef}
        columns={columns}
        locale={{ emptyText: "没有符合条件的任务" }}
        pagination={{ defaultPageSize: DEFAULT_PAGE_SIZE, pageSizeOptions: [10, 20, 50, 100] }}
        // params 在运行时会被合并进 request 参数，是筛选变化的重新请求信号；
        // 库把该属性类型固定为分页参数，这里按实际形状断言。
        params={{ statuses } as unknown as ExTableRequestParams}
        request={request}
        rowKey="id"
        search={false}
        toolBarRender={() => [
          <DictSelect
            allowClear
            dict={STATUS_DICT}
            key="status-filter"
            mode="multiple"
            placeholder="按状态筛选"
            style={{ minWidth: 220 }}
            value={statuses}
            onChange={(value) => {
              setStatuses((value ?? []) as TaskStatus[]);
            }}
          />,
          <Button icon={<ReloadOutlined />} key="reload" onClick={reload} text="刷新" />,
          <Button
            icon={<PlusOutlined />}
            key="create"
            onClick={() => setCreateOpen(true)}
            text="创建任务"
            type="primary"
          />,
        ]}
      />

      <CreateTaskModal
        onCancel={() => setCreateOpen(false)}
        onCreated={() => {
          setCreateOpen(false);
          reload();
        }}
        open={createOpen}
      />
    </Flex>
  );
}
