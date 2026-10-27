/**
 * 任务容器面板：展示该任务真实关联的容器记录，并提供显式 delete / detach 入口。
 *
 * 数据与职责：
 *   - 容器记录来自 `GET /api/v1/tasks/{taskID}/containers`，是操作对象的唯一来源；
 *     不从任务状态、`container_id` 或记录顺序猜出归属；
 *   - 两个动作分别走 `POST .../{delete}` 与 `POST .../{detach}`，不合并为"清理"；
 *   - 破坏性动作先弹出确认，确认框里显示**真实容器标识**与后果；
 *   - 请求在途时禁用同一容器的两个动作，避免重复提交；
 *   - 操作结束后按真实响应展示结论，并刷新容器列表与任务详情；刷新失败不抹除结论；
 *   - 结果不确定（超时 / 网络中断 / 取消）时明确提示需要核验，不自动重发；
 *   - 处置成功后**不**调用重试入口，也不启动工作流。
 */
import { Alert, Descriptions, Flex, Modal, Space, Typography } from "antd";
import { Button, DictTag, type DictItem } from "lingting-react-ui";
import { useCallback, useState } from "react";

import type { ContainerDisposalAction, Task, TaskContainer } from "@/lib/api";
import {
  containerActionBlocked,
  containerActionBlockedReason,
  containerEndReasonLabel,
  containerOriginMeta,
  containerStateMeta,
  describeDisposalOutcome,
  disposalActionMeta,
  isKnownContainerOrigin,
  isKnownContainerState,
} from "@/pages/tasks/containers";
import { useTaskContainers } from "@/pages/tasks/useTaskContainers";

/** 由容器管理状态元数据派生 DictTag 需要的形状，不新增第二套状态名 */
const CONTAINER_STATE_DICT: DictItem<string>[] = [
  "creating",
  "created",
  "running",
  "stopped",
  "unknown",
  "detached",
  "removed",
].map((value) => ({
  label: containerStateMeta(value).label,
  tagColor:
    containerStateMeta(value).color === "default" ? undefined : containerStateMeta(value).color,
  value,
}));

/** 时间展示：使用本地时区，缺省显示占位符 */
function formatTime(value?: string): string {
  if (!value) return "—";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "—";
  return parsed.toLocaleString();
}

/** 容器标识展示：完整标识可见，便于与 CLI、日志对照 */
function ContainerIdentity({ container }: { container: TaskContainer }) {
  return (
    <Space size={8} wrap>
      <Typography.Text code>{container.id}</Typography.Text>
      <Typography.Text type="secondary">{container.name || "未提供名称"}</Typography.Text>
    </Space>
  );
}

/** 单条容器记录的展示与操作 */
function ContainerRow({
  blockedReason,
  container,
  operation,
  onDispose,
}: {
  blockedReason?: string;
  container: TaskContainer;
  operation: ReturnType<typeof useTaskContainers>["operation"];
  onDispose: (containerID: string, action: ContainerDisposalAction) => void;
}) {
  const originMeta = containerOriginMeta(container.origin);
  const running =
    operation.running?.containerID === container.id ? operation.running.action : undefined;
  // 同一容器在途时两个动作都不可用：避免对同一现场并发发起两个有副作用的请求。
  const busy = running !== undefined;
  const deleteBlocked = busy || containerActionBlocked(container, "delete");
  const detachBlocked = busy || containerActionBlocked(container, "detach");
  const outcome = operation.outcome?.containerID === container.id ? operation.outcome : undefined;
  const failure = operation.failure?.containerID === container.id ? operation.failure : undefined;

  return (
    <Flex gap={4} vertical>
      <Descriptions column={1} size="small">
        <Descriptions.Item label="容器">
          <ContainerIdentity container={container} />
        </Descriptions.Item>
        <Descriptions.Item label="与任务的关系">
          <Space size={8} wrap>
            {isKnownContainerOrigin(container.origin) ? (
              <DictTag
                dict={[{ label: originMeta.label, tagColor: undefined, value: container.origin }]}
                value={container.origin}
              />
            ) : (
              // 未知关联方式原样展示，不猜测它是当前代次还是历史代次。
              <Typography.Text code>{container.origin || "未提供"}</Typography.Text>
            )}
            {container.origin === "history" && (
              <Typography.Text type="secondary">
                这是该任务的历史容器，不是当前使用的那一条
              </Typography.Text>
            )}
          </Space>
        </Descriptions.Item>
        <Descriptions.Item label="AgentFlow 管理状态">
          <Space size={8} wrap>
            {isKnownContainerState(container.state) ? (
              <DictTag dict={CONTAINER_STATE_DICT} value={container.state} />
            ) : (
              // 未知状态原样展示：不得被 DictTag 的兜底吞掉，也不得显示为已成功。
              <Typography.Text code>{container.state || "未提供"}</Typography.Text>
            )}
            {!isKnownContainerState(container.state) && (
              <Typography.Text type="secondary">
                服务端返回的状态取值未在当前版本中定义
              </Typography.Text>
            )}
          </Space>
        </Descriptions.Item>
        <Descriptions.Item label="Docker 观测状态">
          {container.docker_state || (
            <Typography.Text type="secondary">服务端未提供</Typography.Text>
          )}
        </Descriptions.Item>
        <Descriptions.Item label="脱离管理">
          {container.detached ? (
            <Space size={8} wrap>
              <Typography.Text>已脱离</Typography.Text>
              <Typography.Text type="secondary">
                时间：{formatTime(container.detached_at)}
              </Typography.Text>
            </Space>
          ) : (
            <Typography.Text type="secondary">未脱离（仍受 AgentFlow 自动管理）</Typography.Text>
          )}
        </Descriptions.Item>
        <Descriptions.Item label="结束原因">
          {containerEndReasonLabel(container.end_reason)}
        </Descriptions.Item>
        <Descriptions.Item label="创建时间">{formatTime(container.created_at)}</Descriptions.Item>
        <Descriptions.Item label="结束时间">{formatTime(container.finished_at)}</Descriptions.Item>
      </Descriptions>

      {blockedReason && <Typography.Text type="secondary">{blockedReason}</Typography.Text>}

      <Space size={8} wrap>
        <Button
          danger
          disabled={deleteBlocked}
          loading={running === "delete"}
          onClick={() => onDispose(container.id, "delete")}
          size="small"
          text={running === "delete" ? "删除中" : "删除容器"}
        />
        <Button
          disabled={detachBlocked}
          loading={running === "detach"}
          onClick={() => onDispose(container.id, "detach")}
          size="small"
          text={running === "detach" ? "脱离中" : "脱离管理"}
        />
      </Space>

      {running && (
        <Typography.Text type="secondary">
          {running === "delete"
            ? "正在请求服务端删除该容器；请求同步执行，请等待真实结果返回。"
            : "正在请求服务端让该容器脱离管理；请求同步执行，请等待真实结果返回。"}
        </Typography.Text>
      )}

      {outcome && (
        <DisposalOutcomeAlert
          action={outcome.action}
          detail={outcome.data.detail}
          outcome={outcome.data.outcome}
          container={outcome.data.container}
        />
      )}
      {failure && (
        <Alert
          description={`${failure.message} 本次请求没有取得可展示的处置结论，页面不会自动重发。`}
          showIcon
          title={failure.action === "delete" ? "删除容器请求失败" : "脱离管理请求失败"}
          type="error"
        />
      )}
    </Flex>
  );
}

/** 一次处置结论的展示 */
function DisposalOutcomeAlert({
  action,
  container,
  detail,
  outcome,
}: {
  action: ContainerDisposalAction;
  container: TaskContainer;
  detail?: string;
  outcome: string;
}) {
  const view = describeDisposalOutcome({ action, container, detail, outcome });
  const type = view.kind === "success" ? "success" : view.kind === "error" ? "error" : "warning";
  return (
    <Alert
      description={
        <Flex gap={2} vertical>
          {view.details.map((line) => (
            <Typography.Text key={line}>{line}</Typography.Text>
          ))}
        </Flex>
      }
      showIcon
      title={`${view.title}：${action === "delete" ? "删除容器" : "脱离管理"}`}
      type={type}
    />
  );
}

/**
 * 任务容器面板。
 *
 * `onRefreshTask` 由父组件提供：处置之后必须重新读取任务详情与工作流进度，返回值是刷新
 * 失败的项，用于单独提示。
 */
export function TaskContainerPanel({
  onRefreshTask,
  task,
  token,
}: {
  onRefreshTask: () => Promise<string[]>;
  task: Task;
  token?: number;
}) {
  const { clearOutcome, dispose, list, operation, reload } = useTaskContainers({
    refreshTask: onRefreshTask,
    taskID: task.id,
    token,
  });
  /** 待确认的处置意图：只在用户确认后才发请求 */
  const [pending, setPending] = useState<{
    action: ContainerDisposalAction;
    container: TaskContainer;
  }>();

  const requestDispose = useCallback(
    (container: TaskContainer, action: ContainerDisposalAction) => {
      setPending({ action, container });
    },
    [],
  );

  const confirm = useCallback(() => {
    if (!pending) return;
    const target = pending;
    setPending(undefined);
    void dispose(target.container.id, target.action);
  }, [dispose, pending]);

  const records = list.status === "ready" ? list.data.records : [];
  const pendingMeta = pending ? disposalActionMeta(pending.action) : undefined;

  return (
    <Flex gap="small" vertical>
      <Space align="center" size={8} wrap>
        <Typography.Text strong>任务容器</Typography.Text>
        <Button onClick={reload} size="small" text="刷新容器" />
        <Typography.Text type="secondary">
          删除与脱离管理是两个独立动作；页面不会自动重发，也不会因处置成功而重开任务。
        </Typography.Text>
      </Space>

      {list.status === "loading" && (
        <Typography.Text type="secondary">正在读取任务容器…</Typography.Text>
      )}
      {list.status === "error" && (
        <Alert
          action={<Button onClick={reload} size="small" text="重试" />}
          description={`${list.error} 当前没有可信的容器记录，因此不提供操作入口。`}
          showIcon
          title="读取任务容器失败"
          type="error"
        />
      )}
      {list.status === "ready" && list.error && (
        <Alert
          action={<Button onClick={reload} size="small" text="重试" />}
          description={`${list.error} 下方展示的是上一次成功读取的结果，可能不是最新状态。`}
          showIcon
          title="容器列表刷新失败"
          type="warning"
        />
      )}

      {operation.uncertain && (
        <Alert description={operation.uncertain} showIcon title="请求结果无法确认" type="warning" />
      )}
      {operation.refreshError && (
        <Alert
          closable
          description={operation.refreshError}
          onClose={clearOutcome}
          showIcon
          title="状态刷新失败"
          type="warning"
        />
      )}

      {list.status === "ready" && records.length === 0 && (
        <Typography.Text type="secondary">
          该任务当前没有容器登记记录。容器由环境准备阶段创建，本面板不会创建容器。
        </Typography.Text>
      )}

      {list.status === "ready" &&
        records.map((container) => (
          <ContainerRow
            blockedReason={containerActionBlockedReason(container)}
            container={container}
            key={container.id}
            onDispose={(containerID, action) => {
              const target = records.find((item) => item.id === containerID);
              if (!target) return;
              requestDispose(target, action);
            }}
            operation={operation}
          />
        ))}

      <Modal
        cancelText="取消"
        centered
        destroyOnHidden
        okButtonProps={{ danger: pendingMeta?.danger }}
        okText={pendingMeta ? `确认${pendingMeta.buttonLabel}` : "确认"}
        onCancel={() => setPending(undefined)}
        onOk={confirm}
        open={pending !== undefined}
        title={pendingMeta?.confirmTitle ?? "确认处置容器"}
      >
        {pending && pendingMeta && (
          <Flex gap="small" vertical>
            <Typography.Text>{pendingMeta.confirmDescription}</Typography.Text>
            <Descriptions column={1} size="small">
              <Descriptions.Item label="操作对象">
                <Typography.Text code>{pending.container.id}</Typography.Text>
              </Descriptions.Item>
              <Descriptions.Item label="容器名称">
                {pending.container.name || "未提供"}
              </Descriptions.Item>
              <Descriptions.Item label="当前管理状态">
                {containerStateMeta(pending.container.state).label}
              </Descriptions.Item>
              <Descriptions.Item label="所属任务">
                {task.name}（<Typography.Text code>{task.id}</Typography.Text>）
              </Descriptions.Item>
            </Descriptions>
          </Flex>
        )}
      </Modal>
    </Flex>
  );
}
