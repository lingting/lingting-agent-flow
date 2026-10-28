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
  fetchTasks,
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
import { CodeSavePanel } from "@/pages/tasks/CodeSavePanel";
import { ResumeEligibilityPanel } from "@/pages/tasks/ResumeEligibilityPanel";
import { RetryTaskPanel } from "@/pages/tasks/RetryTaskPanel";
import { TaskContainerPanel } from "@/pages/tasks/TaskContainerPanel";
import { TaskExecutionPanel } from "@/pages/tasks/TaskExecutionPanel";
import { canStartTask, describeLaunchResult } from "@/pages/tasks/launch";
import {
  canCancelTask,
  canCancelWorkflow,
  canDeleteTask,
  TASK_STATUS_META,
  WORKFLOW_RUN_STATUS_DICT,
} from "@/pages/tasks/status";
import { useTaskLiveSnapshot } from "@/pages/tasks/useTaskLiveSnapshot";

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
              <Typography.Text type="secondary">
                服务端原始取值：{task.final_result}
              </Typography.Text>
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
      {/*
        步骤的顺序、状态、失败原因与起止时间只在下方「计划步骤」中展示。

        这里曾经也渲染一份步骤概览，但两份列表来自同一次读取的不同接口，状态取值一处是
        中文标签、一处是服务端原始值，用户无法判断哪一份是权威信息；因此只保留单一
        权威位置，并在本面板明确指路。
      */}
      {status.steps.length > 0 && (
        <Typography.Text type="secondary">
          步骤顺序、状态与失败原因以「计划步骤」为准；本面板的数字只汇总执行记录。
        </Typography.Text>
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
  /**
   * 当前打开的任务标识。
   *
   * 任务详情与工作流进度全部由 `useTaskLiveSnapshot` 按这个标识维护：打开详情即开始
   * 自动刷新，关闭详情（置空）立即停止。这里不再保存详情快照，避免出现两份数据源。
   */
  const [selectedTaskID, setSelectedTaskID] = useState<string>();
  const [createOpen, setCreateOpen] = useState(false);
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
   * 任务详情与工作流进度的唯一数据源。
   *
   * 自动刷新只读取状态：不启动、不取消、不恢复、不重试，也不会把刷新失败当作工作流终止。
   */
  const { autoRefreshOn, refreshHint, refreshNow, refreshTask, snapshot, tokens } =
    useTaskLiveSnapshot(selectedTaskID);
  const selected = snapshot.task;
  const workflow = snapshot.workflow;
  const detailLoading =
    snapshot.loading || (selected === undefined && snapshot.error === undefined);
  const workflowLoading = snapshot.loading;

  /** 当前选中任务的最新副本：供 Actions 读取，避免闭包读到旧值 */
  const selectedRef = useRef<Task>(undefined);
  selectedRef.current = selected;

  /** 重新拉取当前页真实数据 */
  const reload = useCallback(() => void actionRef.current?.reload(), []);

  /**
   * 执行动作结束后重新读取任务与工作流状态。
   *
   * 与自动刷新共用同一套读取实现：只返回刷新失败的项，执行结论由动作面板单独保存，
   * 刷新失败不会覆盖它，也不会被当作工作流终止。
   */
  const refreshTaskDetail = useCallback(async (): Promise<string[]> => refreshNow(), [refreshNow]);

  /**
   * 打开任务详情。
   *
   * 这里只记录任务标识：任务详情、工作流进度与自动刷新周期都由 `useTaskLiveSnapshot`
   * 负责，页面不再自己发起一次重复请求。详情查询失败时由快照给出可读错误，
   * 不会把失败伪装成"任务不存在"以外的其他状态。
   */
  const openDetail = useCallback((taskID: string) => {
    setActionError(undefined);
    setSelectedTaskID(taskID);
  }, []);

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
      const failures = await refreshTask(taskID);
      reload();
      setLaunchRefreshError(
        failures.length > 0
          ? `启动请求已结束，但状态刷新失败：${failures.join("、")}。页面可能不是最新状态，请手动刷新确认。`
          : undefined,
      );
    },
    [refreshTask, reload],
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
        await refreshNow();
      } catch (cause) {
        setActionError(describeError(cause));
      }
    },
    [refreshNow],
  );

  /**
   * 恢复入口已迁到 `ResumeEligibilityPanel`：可用性必须来自服务端的续跑资格，
   * 页面级不再保留按工作流状态判定的第二个恢复入口。
   */
  const handleCancel = useCallback(
    async (task: Task) => {
      setActionError(undefined);
      try {
        await cancelTask(task.id);
        reload();
        if (selectedTaskID === task.id) {
          await refreshNow();
        }
      } catch (cause) {
        setActionError(describeError(cause));
      }
    },
    [refreshNow, reload, selectedTaskID],
  );

  const handleDelete = useCallback(
    async (task: Task) => {
      setActionError(undefined);
      try {
        await deleteTask(task.id);
        if (selectedTaskID === task.id) {
          // 记录已删除：关闭详情并停止自动刷新。
          setSelectedTaskID(undefined);
        }
        reload();
      } catch (cause) {
        setActionError(describeError(cause));
      }
    },
    [reload, selectedTaskID],
  );

  const columns = useMemo<ExTableColumn<Task & Record<string, unknown>>[]>(
    () => [
      {
        dataIndex: "name",
        render: (_dom, record) => (
          <Flex gap={4} vertical>
            <LinkButton onClick={() => openDetail(record.id)} text={record.name} />
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
            <LinkButton onClick={() => openDetail(record.id)} text="详情" />
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

      {snapshot.error && !selected && (
        <Alert
          action={<Button onClick={() => void refreshNow()} size="small" text="重试" />}
          description={snapshot.error}
          showIcon
          title="加载任务详情失败"
          type="error"
        />
      )}

      {/* 详情区域只在已取得任务或正在加载时出现：首次加载失败由上面的错误提示呈现，
          不渲染一个空的详情框冒充任务存在 */}
      {(selected || (selectedTaskID !== undefined && detailLoading)) && (
        <Flex gap="small" vertical>
          <Flex align="center" gap="small" justify="space-between">
            <Typography.Text strong>任务详情</Typography.Text>
            <Button
              onClick={() => setSelectedTaskID(undefined)}
              size="small"
              text="关闭"
              type="text"
            />
          </Flex>
          {detailLoading ? (
            <Typography.Text type="secondary">正在加载详情…</Typography.Text>
          ) : selected ? (
            <TaskDetail task={selected} />
          ) : null}
          {snapshot.refreshError && (
            <Alert
              action={<Button onClick={() => void refreshNow()} size="small" text="重试" />}
              description={snapshot.refreshError}
              showIcon
              // 标题必须与当前的刷新机制一致：自动刷新已停止时不存在"自动刷新失败"，
              // 那一次失败只可能来自手动刷新，否则与同一行的"自动刷新已停止"自相矛盾。
              title={autoRefreshOn ? "自动刷新失败" : "刷新失败"}
              type="warning"
            />
          )}
          {refreshHint && <Typography.Text type="secondary">{refreshHint}</Typography.Text>}
          <Flex align="center" gap="small">
            <Typography.Text strong>工作流进度</Typography.Text>
            <Button onClick={() => void refreshNow()} size="small" text="刷新进度" />
            <Typography.Text type="secondary">
              {autoRefreshOn ? "执行期间每 5 秒自动刷新" : "自动刷新已停止"}
            </Typography.Text>
            <Button
              disabled={!selected || !canCancelWorkflow(workflow?.run)}
              onClick={() => selected && void handleCancelWorkflow(selected.id)}
              size="small"
              text="取消工作流"
            />
          </Flex>
          {workflowLoading && !workflow ? (
            <Typography.Text type="secondary">正在加载工作流进度…</Typography.Text>
          ) : workflow ? (
            <WorkflowPanel status={workflow} />
          ) : (
            <Typography.Text type="secondary">尚未启动工作流</Typography.Text>
          )}
          {selected && (
            <TaskExecutionPanel
              onRefreshTask={refreshTaskDetail}
              stepsToken={tokens.steps}
              task={selected}
            />
          )}
          {selected && <CodeSavePanel task={selected} token={tokens.codeSaves} />}
          {selected && (
            <ResumeEligibilityPanel
              onRefreshTask={refreshTaskDetail}
              task={selected}
              token={tokens.resume}
            />
          )}
          {selected && (
            <TaskContainerPanel
              onRefreshTask={refreshTaskDetail}
              task={selected}
              token={tokens.containers}
            />
          )}
          {selected && <RetryTaskPanel onRefreshTask={refreshTaskDetail} task={selected} />}
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
