import { PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Alert,
  Button,
  Descriptions,
  Flex,
  Popconfirm,
  Select,
  Space,
  Tag,
  Typography,
} from "antd";

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
import {
  canCancelTask,
  canDeleteTask,
  TASK_STATUS_OPTIONS,
  taskStatusMeta,
} from "@/pages/tasks/status";

/** 默认页大小，与后端默认值一致 */
const DEFAULT_PAGE_SIZE = 20;

/** 页大小选项 */
const PAGE_SIZE_OPTIONS = [10, 20, 50, 100];

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

/** 任务状态标签 */
function StatusTag({ status }: { status: TaskStatus }) {
  const meta = taskStatusMeta(status);
  return <Tag color={meta.color}>{meta.label}</Tag>;
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
        <StatusTag status={task.status} />
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
 * 页面不做任何状态推断：状态一律按接口返回值展示，不会把尚未执行的任务显示为成功或失败。
 */
export function TasksPage() {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [total, setTotal] = useState(0);
  const [current, setCurrent] = useState(1);
  const [size, setSize] = useState(DEFAULT_PAGE_SIZE);
  const [statuses, setStatuses] = useState<TaskStatus[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string>();
  const [actionError, setActionError] = useState<string>();
  const [selected, setSelected] = useState<Task>();
  const [detailLoading, setDetailLoading] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const requestRef = useRef(0);

  const load = useCallback(async () => {
    const requestID = requestRef.current + 1;
    requestRef.current = requestID;
    setLoading(true);
    setError(undefined);
    try {
      const { data } = await fetchTasks({
        current,
        size,
        status: statuses.length > 0 ? statuses : undefined,
      });
      // 只接受最后一次请求的结果，避免过期响应覆盖当前筛选条件。
      if (requestRef.current !== requestID) return;
      setTasks(data.records);
      setTotal(data.total);
    } catch (cause) {
      if (requestRef.current !== requestID) return;
      setTasks([]);
      setTotal(0);
      setError(describeError(cause));
    } finally {
      if (requestRef.current === requestID) {
        setLoading(false);
      }
    }
  }, [current, size, statuses]);

  useEffect(() => {
    void load();
  }, [load]);

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
        await load();
        if (selected?.id === task.id) {
          await openDetail(task.id);
        }
      } catch (cause) {
        setActionError(describeError(cause));
      }
    },
    [load, openDetail, selected?.id],
  );

  const handleDelete = useCallback(
    async (task: Task) => {
      setActionError(undefined);
      try {
        await deleteTask(task.id);
        if (selected?.id === task.id) {
          setSelected(undefined);
        }
        await load();
      } catch (cause) {
        setActionError(describeError(cause));
      }
    },
    [load, selected?.id],
  );

  const totalPages = Math.max(1, Math.ceil(total / size));
  const emptyText = useMemo(() => {
    if (loading) return "正在加载任务…";
    if (error) return "加载失败";
    if (statuses.length > 0) return "当前筛选条件下没有任务";
    return "还没有任务，点击「创建任务」开始";
  }, [error, loading, statuses.length]);

  return (
    <Flex gap="middle" vertical style={{ width: "100%" }}>
      <Flex align="center" gap="small" justify="space-between" wrap>
        <Space wrap>
          <Select
            allowClear
            mode="multiple"
            options={TASK_STATUS_OPTIONS}
            placeholder="按状态筛选"
            style={{ minWidth: 220 }}
            value={statuses}
            onChange={(value) => {
              setStatuses(value as TaskStatus[]);
              setCurrent(1);
            }}
          />
          <Button icon={<ReloadOutlined />} onClick={() => void load()}>
            刷新
          </Button>
        </Space>
        <Button icon={<PlusOutlined />} type="primary" onClick={() => setCreateOpen(true)}>
          创建任务
        </Button>
      </Flex>

      {error && (
        <Alert
          action={
            <Button size="small" onClick={() => void load()}>
              重试
            </Button>
          }
          showIcon
          title="加载任务失败"
          description={error}
          type="error"
        />
      )}
      {actionError && (
        <Alert closable showIcon title="操作失败" description={actionError} type="error" />
      )}

      {selected && (
        <Flex gap="small" vertical>
          <Flex align="center" gap="small" justify="space-between">
            <Typography.Text strong>任务详情</Typography.Text>
            <Button size="small" type="text" onClick={() => setSelected(undefined)}>
              关闭
            </Button>
          </Flex>
          {detailLoading ? (
            <Typography.Text type="secondary">正在加载详情…</Typography.Text>
          ) : (
            <TaskDetail task={selected} />
          )}
        </Flex>
      )}

      <div style={{ overflowX: "auto" }}>
        <table style={{ borderCollapse: "collapse", width: "100%" }}>
          <thead>
            <tr>
              {["任务名称", "状态", "创建时间", "更新时间", "操作"].map((title) => (
                <th
                  key={title}
                  style={{
                    borderBottom: "1px solid rgba(0,0,0,0.08)",
                    padding: "8px 12px",
                    textAlign: "left",
                    whiteSpace: "nowrap",
                  }}
                >
                  <Typography.Text strong>{title}</Typography.Text>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {tasks.map((task) => (
              <tr key={task.id}>
                <td style={{ borderBottom: "1px solid rgba(0,0,0,0.06)", padding: "8px 12px" }}>
                  <Flex gap={4} vertical>
                    <Typography.Link onClick={() => void openDetail(task.id)}>
                      {task.name}
                    </Typography.Link>
                    <Typography.Text type="secondary" ellipsis style={{ maxWidth: 320 }}>
                      {task.description}
                    </Typography.Text>
                  </Flex>
                </td>
                <td style={{ borderBottom: "1px solid rgba(0,0,0,0.06)", padding: "8px 12px" }}>
                  <StatusTag status={task.status} />
                </td>
                <td
                  style={{
                    borderBottom: "1px solid rgba(0,0,0,0.06)",
                    padding: "8px 12px",
                    whiteSpace: "nowrap",
                  }}
                >
                  {formatTime(task.created_at)}
                </td>
                <td
                  style={{
                    borderBottom: "1px solid rgba(0,0,0,0.06)",
                    padding: "8px 12px",
                    whiteSpace: "nowrap",
                  }}
                >
                  {formatTime(task.updated_at)}
                </td>
                <td style={{ borderBottom: "1px solid rgba(0,0,0,0.06)", padding: "8px 12px" }}>
                  <Space>
                    <Button size="small" onClick={() => void openDetail(task.id)}>
                      详情
                    </Button>
                    <Button
                      disabled={!canCancelTask(task)}
                      size="small"
                      onClick={() => void handleCancel(task)}
                    >
                      取消
                    </Button>
                    <Popconfirm
                      description="删除后任务不再出现在列表中，历史记录仍会保留。"
                      disabled={!canDeleteTask(task)}
                      okText="删除"
                      title="确认删除该任务？"
                      onConfirm={() => void handleDelete(task)}
                    >
                      <Button danger disabled={!canDeleteTask(task)} size="small">
                        删除
                      </Button>
                    </Popconfirm>
                  </Space>
                </td>
              </tr>
            ))}
            {tasks.length === 0 && (
              <tr>
                <td colSpan={5} style={{ padding: "24px 12px", textAlign: "center" }}>
                  <Typography.Text type="secondary">{emptyText}</Typography.Text>
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <Flex align="center" gap="small" justify="end" wrap>
        <Typography.Text type="secondary">共 {total} 条</Typography.Text>
        <Select
          options={PAGE_SIZE_OPTIONS.map((value) => ({ label: `${value} 条/页`, value }))}
          style={{ width: 120 }}
          value={size}
          onChange={(value) => {
            setSize(value);
            setCurrent(1);
          }}
        />
        <Button disabled={current <= 1} size="small" onClick={() => setCurrent(current - 1)}>
          上一页
        </Button>
        <Typography.Text>
          {current} / {totalPages}
        </Typography.Text>
        <Button
          disabled={current >= totalPages}
          size="small"
          onClick={() => setCurrent(current + 1)}
        >
          下一页
        </Button>
      </Flex>

      <CreateTaskModal
        open={createOpen}
        onCancel={() => setCreateOpen(false)}
        onCreated={async () => {
          setCreateOpen(false);
          setCurrent(1);
          await load();
        }}
      />
    </Flex>
  );
}
