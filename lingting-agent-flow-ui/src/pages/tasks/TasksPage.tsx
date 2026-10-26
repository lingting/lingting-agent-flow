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
  cancelTaskWorkflow,
  deleteTask,
  fetchTask,
  fetchTaskWorkflow,
  fetchTasks,
  resumeTaskWorkflow,
  startTask,
  type Task,
  type TaskStatus,
  type WorkflowStatusResponse,
} from "@/lib/api";
import { CreateTaskModal } from "@/pages/tasks/CreateTaskModal";
import {
  closeoutDisplay,
  closeoutSummary,
  conclusionWarning,
  taskResultDisplay,
} from "@/pages/tasks/closeout";
import { TaskExecutionPanel } from "@/pages/tasks/TaskExecutionPanel";
import { canStartTask, describeLaunchResult } from "@/pages/tasks/launch";
import {
  canCancelTask,
  canCancelWorkflow,
  canDeleteTask,
  canResumeWorkflow,
  TASK_STATUS_META,
  WORKFLOW_RUN_STATUS_DICT,
} from "@/pages/tasks/status";

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

/** 把展示结论的颜色映射到 Typography 的语义类型 */
function textType(color: string): "success" | "warning" | "danger" | "secondary" {
  switch (color) {
    case "success":
      return "success";
    case "warning":
    case "processing":
      return "warning";
    case "error":
      return "danger";
    default:
      return "secondary";
  }
}

/**
 * 任务详情面板：只展示接口实际返回的字段。
 *
 * 任务状态、最终业务结论与收尾闭环是三行独立事实：
 *   - 状态表达"处在哪个业务阶段"，不能当作结论；
 *   - `final_result` 是服务端给出的最终业务结论，缺省时明确写"未提供"；
 *   - `closeout` 与 `closeout_summary` 表达结论是否闭环，未确认时给出警示。
 * 三者都不由前端推导，也不互相替代。
 */
function TaskDetail({ task }: { task: Task }) {
  const result = taskResultDisplay(task.final_result, task.closeout);
  const closeout = closeoutDisplay(task.closeout);
  const warning = conclusionWarning(task);
  return (
    <Flex gap="small" vertical>
      <Descriptions column={1} size="small">
        <Descriptions.Item label="任务标识">
          <Typography.Text code>{task.id}</Typography.Text>
        </Descriptions.Item>
        <Descriptions.Item label="描述">{task.description}</Descriptions.Item>
        <Descriptions.Item label="任务状态">
          <Space size={8} wrap>
            <DictTag dict={STATUS_DICT} value={task.status} />
            <Typography.Text type="secondary">状态只表达业务阶段，不代表最终结论</Typography.Text>
          </Space>
        </Descriptions.Item>
        <Descriptions.Item label="最终业务结论">
          <Space size={8} wrap>
            <Typography.Text type={textType(result.color)}>{result.label}</Typography.Text>
            {task.final_result && !result.known && (
              <Typography.Text type="secondary">服务端原始取值：{task.final_result}</Typography.Text>
            )}
          </Space>
        </Descriptions.Item>
        <Descriptions.Item label="收尾闭环">
          <Space direction="vertical" size={2}>
            <Space size={8} wrap>
              <Typography.Text type={textType(closeout.color)}>{closeout.label}</Typography.Text>
              {task.closeout && !closeout.known && (
                <Typography.Text type="secondary">服务端原始取值：{task.closeout}</Typography.Text>
              )}
            </Space>
            <Typography.Text type="secondary">{closeoutSummary(task)}</Typography.Text>
          </Space>
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
      {warning && (
        <Alert
          description={warning}
          showIcon
          title={
            task.closeout === "inconsistent" ? "收尾结论不一致，需要人工核验" : "任务尚未确认完成"
          }
          type={task.closeout === "inconsistent" ? "error" : "warning"}
        />
      )}
    </Flex>
  );
}

/**
 * 工作流进度面板。
 *
 * 数据全部来自真实接口：没有执行记录时显示"尚未启动"，不伪造进度；
 * `code_saver_available` 为 false 时明确说明任务不能判定为完整成功。
 */
function WorkflowPanel({ status }: { status: WorkflowStatusResponse }) {
  const run = status.run;
  return (
    <Flex gap="small" vertical>
      <Descriptions column={1} size="small">
        <Descriptions.Item label="工作流状态">
          {run ? (
            <DictTag dict={WORKFLOW_RUN_STATUS_DICT} value={run.status} />
          ) : (
            <Typography.Text type="secondary">尚未启动工作流</Typography.Text>
          )}
        </Descriptions.Item>
        {run && (
          <>
            <Descriptions.Item label="执行记录">
              <Typography.Text code>{run.run_id}</Typography.Text>
            </Descriptions.Item>
            <Descriptions.Item label="进度">
              {`已完成 ${run.completed_steps} / ${run.total_steps} 步`}
            </Descriptions.Item>
            <Descriptions.Item label="当前步骤">
              {run.current_step_id ? (
                <Typography.Text code>{run.current_step_id}</Typography.Text>
              ) : (
                <Typography.Text type="secondary">无</Typography.Text>
              )}
            </Descriptions.Item>
            <Descriptions.Item label="更新时间">{formatTime(run.updated_at)}</Descriptions.Item>
            {run.error_summary && (
              <Descriptions.Item label="停止原因">
                <Typography.Text type="danger">{run.error_summary}</Typography.Text>
              </Descriptions.Item>
            )}
            {!run.code_saver_available && (
              <Descriptions.Item label="代码保存">
                <Typography.Text type="warning">
                  当前服务未提供代码保存能力，任务不会因为步骤全部通过而被判定为完整成功。
                </Typography.Text>
              </Descriptions.Item>
            )}
          </>
        )}
      </Descriptions>
      {status.steps.length > 0 && (
        <Flex gap={4} vertical>
          <Typography.Text strong>步骤进度</Typography.Text>
          {status.steps.map((step) => (
            <Flex align="center" gap="small" key={step.step_id}>
              <Typography.Text type="secondary" style={{ width: 28 }}>
                {step.sequence}
              </Typography.Text>
              <Typography.Text style={{ minWidth: 180 }}>{step.title}</Typography.Text>
              <Typography.Text code>{step.status}</Typography.Text>
              {step.error && <Typography.Text type="danger">{step.error}</Typography.Text>}
            </Flex>
          ))}
        </Flex>
      )}
    </Flex>
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
  const [workflow, setWorkflow] = useState<WorkflowStatusResponse>();
  const [workflowLoading, setWorkflowLoading] = useState(false);
  /** 正在启动的任务标识：用于阻止重复提交并给出进行中反馈 */
  const [startingTaskID, setStartingTaskID] = useState<string>();
  /**
   * 进行中标记的同步副本。
   *
   * 同一帧内的连续点击不会让 state 及时更新，因此用 ref 做第一道防重复提交；
   * 真正的并发安全仍由服务端任务租约与状态机保证。
   */
  const launchingRef = useRef(false);
  /** 最近一次启动的服务端结论（只在请求成功返回时设置） */
  const [launchOutcome, setLaunchOutcome] = useState<{
    taskName: string;
    result: ReturnType<typeof describeLaunchResult>;
  }>();
  /** 启动结论之后的刷新失败提示：不覆盖已得到的启动结论 */
  const [launchRefreshError, setLaunchRefreshError] = useState<string>();
  /**
   * 当前选中任务的最新副本。
   *
   * 执行动作的回调需要在异步完成后读取"当前选中的任务"，但闭包里的 state 是旧值，
   * 因此用 ref 保存最新副本，避免把任务标识写死在回调依赖里。
   */
  const selectedRef = useRef<Task>(undefined);

  /** 重新拉取当前页真实数据 */
  const reload = useCallback(() => void actionRef.current?.reload(), []);

  const loadWorkflow = useCallback(async (taskID: string) => {
    setWorkflowLoading(true);
    try {
      const { data } = await fetchTaskWorkflow(taskID);
      setWorkflow(data);
    } catch (cause) {
      // 工作流状态查询失败不影响任务详情展示，但必须如实提示，不伪造进度。
      setWorkflow(undefined);
      setActionError(describeError(cause));
    } finally {
      setWorkflowLoading(false);
    }
  }, []);

  /**
   * 执行动作结束后重新读取任务与工作流状态。
   *
   * 只返回刷新失败的项：执行结论由动作面板单独保存，刷新失败不会覆盖它。
   */
  const refreshTaskDetail = useCallback(async (): Promise<string[]> => {
    const failures: string[] = [];
    try {
      const { data } = await fetchTask(selectedRef.current?.id ?? "");
      setSelected(data);
    } catch (cause) {
      failures.push(`任务详情（${describeError(cause)}）`);
    }
    try {
      const { data } = await fetchTaskWorkflow(selectedRef.current?.id ?? "");
      setWorkflow(data);
    } catch (cause) {
      failures.push(`工作流进度（${describeError(cause)}）`);
    }
    return failures;
  }, []);

  const openDetail = useCallback(
    async (taskID: string) => {
      setDetailLoading(true);
      setActionError(undefined);
      try {
        const { data } = await fetchTask(taskID);
        setSelected(data);
        selectedRef.current = data;
      } catch (cause) {
        setSelected(undefined);
        selectedRef.current = undefined;
        setActionError(describeError(cause));
      } finally {
        setDetailLoading(false);
      }
      await loadWorkflow(taskID);
    },
    [loadWorkflow],
  );

  /**
   * 启动请求结束后重新读取服务端事实。
   *
   * 启动接口会真实创建容器、初始化工作区、生成计划并启动工作流，因此无论响应是成功
   * 还是失败，都必须以数据库中的真实状态为准刷新页面，不能依赖本地对象。
   *
   * 刷新失败单独记录：已经得到的启动结论不会被抹除，只提示状态可能不是最新。
   */
  const refreshAfterLaunch = useCallback(
    async (taskID: string) => {
      const failures: string[] = [];
      try {
        const { data } = await fetchTask(taskID);
        setSelected((current) => (current?.id === taskID ? data : current));
        if (selectedRef.current?.id === taskID) selectedRef.current = data;
      } catch (cause) {
        failures.push(`任务详情（${describeError(cause)}）`);
      }
      reload();
      try {
        const { data } = await fetchTaskWorkflow(taskID);
        setWorkflow(data);
      } catch (cause) {
        failures.push(`工作流进度（${describeError(cause)}）`);
      }
      setLaunchRefreshError(
        failures.length > 0
          ? `启动请求已结束，但状态刷新失败：${failures.join("、")}。页面可能不是最新状态，请手动刷新确认。`
          : undefined,
      );
    },
    [reload],
  );

  /**
   * 显式启动任务：走统一启动 `POST /tasks/{id}/start`。
   *
   * 该入口由服务端依次准备容器、初始化工作区、生成并持久化计划、启动工作流，因此
   * 新任务（`queued`、无容器、无步骤）必须用它，而不是 `execute-workflow`。
   *
   * 不做任何乐观状态更新，也不自动重试：启动流程可能已经产生容器等外部副作用，
   * 重试必须由用户基于最新状态显式触发。
   */
  const handleStartTask = useCallback(
    async (task: Task) => {
      if (launchingRef.current) return;
      launchingRef.current = true;
      setActionError(undefined);
      setLaunchRefreshError(undefined);
      setLaunchOutcome(undefined);
      setStartingTaskID(task.id);
      try {
        const { data } = await startTask(task.id);
        setLaunchOutcome({ result: describeLaunchResult(data), taskName: task.name });
      } catch (cause) {
        // HTTP 失败：只展示服务端给出的错误，不根据前端预期推断服务端状态。
        setActionError(describeError(cause));
      } finally {
        await refreshAfterLaunch(task.id);
        launchingRef.current = false;
        setStartingTaskID(undefined);
      }
    },
    [refreshAfterLaunch],
  );

  /** 请求取消当前工作流：取消请求先落盘，最终状态由真实收敛结果决定 */
  const handleCancelWorkflow = useCallback(
    async (taskID: string) => {
      setActionError(undefined);
      try {
        await cancelTaskWorkflow(taskID);
        await loadWorkflow(taskID);
      } catch (cause) {
        setActionError(describeError(cause));
      }
    },
    [loadWorkflow],
  );

  /** 恢复已停止的工作流：只从已确认的进度继续 */
  const handleResumeWorkflow = useCallback(
    async (taskID: string, runID: string) => {
      setActionError(undefined);
      try {
        await resumeTaskWorkflow(taskID, { run_id: runID });
        await loadWorkflow(taskID);
      } catch (cause) {
        setActionError(describeError(cause));
      }
    },
    [loadWorkflow],
  );

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
              disabled={!canStartTask(record) || startingTaskID === record.id}
              loading={startingTaskID === record.id}
              onClick={() => void handleStartTask(record)}
              text={startingTaskID === record.id ? "启动中" : "启动"}
            />
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
    [handleCancel, handleDelete, handleStartTask, openDetail, startingTaskID],
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
      {launchOutcome && (
        <Alert
          closable
          description={launchOutcome.result.description}
          onClose={() => setLaunchOutcome(undefined)}
          showIcon
          title={`${launchOutcome.result.title}：${launchOutcome.taskName}`}
          type={launchOutcome.result.kind === "started" ? "success" : "warning"}
        />
      )}
      {launchRefreshError && (
        <Alert
          closable
          description={launchRefreshError}
          onClose={() => setLaunchRefreshError(undefined)}
          showIcon
          title="状态刷新失败"
          type="warning"
        />
      )}

      {selected && (
        <Flex gap="small" vertical>
          <Flex align="center" gap="small" justify="space-between">
            <Typography.Text strong>任务详情</Typography.Text>
            <Button
              onClick={() => {
                setSelected(undefined);
                selectedRef.current = undefined;
              }}
              size="small"
              text="关闭"
              type="text"
            />
          </Flex>
          {detailLoading ? (
            <Typography.Text type="secondary">正在加载详情…</Typography.Text>
          ) : (
            <TaskDetail task={selected} />
          )}
          <Flex align="center" gap="small">
            <Typography.Text strong>工作流进度</Typography.Text>
            <Button onClick={() => void loadWorkflow(selected.id)} size="small" text="刷新" />
            <Button
              disabled={!canCancelWorkflow(workflow?.run)}
              onClick={() => void handleCancelWorkflow(selected.id)}
              size="small"
              text="取消工作流"
            />
            <Button
              disabled={!canResumeWorkflow(workflow?.run)}
              onClick={() => {
                const runID = workflow?.run?.run_id;
                if (runID) void handleResumeWorkflow(selected.id, runID);
              }}
              size="small"
              text="恢复工作流"
              type="primary"
            />
          </Flex>
          {workflowLoading && !workflow ? (
            <Typography.Text type="secondary">正在加载工作流进度…</Typography.Text>
          ) : workflow ? (
            <WorkflowPanel status={workflow} />
          ) : (
            <Typography.Text type="secondary">尚未启动工作流</Typography.Text>
          )}
          {selected && <TaskExecutionPanel onRefreshTask={refreshTaskDetail} task={selected} />}
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
