import { PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import type { ActionType } from "@ant-design/pro-components";
import { Alert, Flex, Space, Typography } from "antd";
import {
  Button,
  DictTag,
  type DictItem,
  type ExTableColumn,
  ExTable,
  LinkButton,
} from "lingting-react-ui";
import { useCallback, useMemo, useRef, useState } from "react";

import { ApiError, deleteHost, fetchHosts, type Host, validateHost } from "@/lib/api";
import { HostFormModal } from "@/pages/machines/HostFormModal";
import { HostValidationPanel } from "@/pages/machines/HostValidationPanel";
import { canDeleteHost, canRevalidate, canUseForRemoteTask } from "@/pages/machines/status";

/** 总体状态字典，取值与 Go 端 domain.ValidationStatus 一致 */
const VALIDATION_STATUS_DICT: DictItem<string>[] = [
  { label: "校验中", tagColor: "processing", value: "checking" },
  { label: "正常", tagColor: "success", value: "normal" },
  { label: "异常", tagColor: "error", value: "abnormal" },
];

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

/**
 * 机器（执行节点）管理页。
 *
 * 展示真实的后端状态：总体校验状态、失败原因与三项单项结果都由接口返回，
 * 首次加载前不显示任何默认成功状态。远程任务相关操作只在服务端判定可用时开放，
 * 前端禁用只是提示，真正的限制由后端准入校验实施。
 */
export function MachinesPage() {
  const actionRef = useRef<ActionType>(null);
  const [selected, setSelected] = useState<Host>();
  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<Host>();
  const [loadError, setLoadError] = useState<string>();
  const [actionError, setActionError] = useState<string>();

  const reload = useCallback(() => {
    // ProTable 的 reload 返回 Promise；这里不需要等待结果，失败由 request 内部记录。
    void actionRef.current?.reload();
  }, []);

  /** 打开详情：展示列表返回的最新校验结果 */
  const openDetail = useCallback((host: Host) => {
    setSelected(host);
  }, []);

  const handleRevalidate = useCallback(
    async (host: Host) => {
      setActionError(undefined);
      try {
        const { data } = await validateHost(host.id);
        setSelected(data);
        reload();
      } catch (cause) {
        setActionError(describeError(cause));
      }
    },
    [reload],
  );

  const handleDelete = useCallback(
    async (host: Host) => {
      setActionError(undefined);
      try {
        await deleteHost(host.id);
        if (selected?.id === host.id) setSelected(undefined);
        reload();
      } catch (cause) {
        setActionError(describeError(cause));
      }
    },
    [reload, selected?.id],
  );

  const columns = useMemo<ExTableColumn<Host & Record<string, unknown>>[]>(
    () => [
      {
        dataIndex: "name",
        render: (_dom, record) => (
          <Flex gap={4} vertical>
            <LinkButton onClick={() => openDetail(record)} text={record.name} />
            <Typography.Text type="secondary" ellipsis style={{ maxWidth: 320 }}>
              {record.username}@{record.host}:{record.port}
            </Typography.Text>
          </Flex>
        ),
        search: false,
        title: "节点名称",
      },
      {
        dataIndex: "auth_method",
        render: (_dom, record) => record.auth_method,
        search: false,
        title: "认证方式",
      },
      {
        dataIndex: "validation_status",
        render: (_dom, record) => (
          <DictTag dict={VALIDATION_STATUS_DICT} value={record.validation_status} />
        ),
        search: false,
        title: "校验状态",
      },
      {
        dataIndex: "validation_message",
        render: (_dom, record) => (
          <Typography.Text
            ellipsis
            style={{ maxWidth: 320 }}
            type={record.validation_status === "abnormal" ? "danger" : "secondary"}
          >
            {record.validation_message || "—"}
          </Typography.Text>
        ),
        search: false,
        title: "校验摘要",
      },
      {
        dataIndex: "validation_completed_at",
        render: (_dom, record) => formatTime(record.validation_completed_at),
        search: false,
        title: "校验完成时间",
      },
      {
        dataIndex: "usable_for_remote_task",
        render: (_dom, record) => (canUseForRemoteTask(record) ? "可用" : "不可用"),
        search: false,
        title: "远程任务",
      },
      {
        dataIndex: "option",
        render: (_dom, record) => (
          <Space>
            <LinkButton onClick={() => openDetail(record)} text="详情" />
            <LinkButton
              disabled={!canRevalidate(record)}
              onClick={() => void handleRevalidate(record)}
              text="重新校验"
            />
            <LinkButton
              onClick={() => {
                setEditing(record);
                setFormOpen(true);
              }}
              text="修改"
            />
            <LinkButton
              confirm={{
                description: "删除只移除本机记录，不会删除远程工作空间或远程 Docker 资源。",
                title: "确认删除该执行节点？",
              }}
              danger
              disabled={!canDeleteHost(record)}
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
    [handleDelete, handleRevalidate, openDetail],
  );

  /**
   * 列表请求。
   *
   * 请求失败时清空数据并记录错误，由页面顶部提示给出重试入口，不把失败伪装成空列表。
   */
  const request = useCallback(async () => {
    setLoadError(undefined);
    try {
      const { data } = await fetchHosts();
      return { records: data.records, total: data.total };
    } catch (cause) {
      setLoadError(describeError(cause));
      return { records: [], total: 0 };
    }
  }, []);

  return (
    <Flex gap="middle" vertical style={{ width: "100%" }}>
      {loadError && (
        <Alert
          action={<Button onClick={reload} size="small" text="重试" />}
          description={loadError}
          showIcon
          title="加载执行节点失败"
          type="error"
        />
      )}
      {actionError && (
        <Alert closable description={actionError} showIcon title="操作失败" type="error" />
      )}

      <Alert
        description={
          "环境预检依次检查 SSH 连接、Docker CLI 帮助信息与基础 Dockerfile 的外部 FROM 镜像元数据。" +
          "第一版 Docker 检查只基于 CLI 帮助信息，不创建或启动容器，也不拉取镜像层，" +
          "因此「正常」不代表该 Docker Engine 一定能成功创建并运行 AgentFlow 所需的容器。"
        }
        showIcon
        title="校验范围说明"
        type="info"
      />

      {selected && (
        <Flex gap="small" vertical>
          <Flex align="center" gap="small" justify="space-between">
            <Typography.Text strong>节点详情</Typography.Text>
            <Button onClick={() => setSelected(undefined)} size="small" text="关闭" type="text" />
          </Flex>
          <HostValidationPanel host={selected} />
        </Flex>
      )}

      <ExTable<Host & Record<string, unknown>>
        actionRef={actionRef}
        columns={columns}
        locale={{ emptyText: "尚未配置执行节点" }}
        pagination={false}
        request={request}
        rowKey="id"
        search={false}
        toolBarRender={() => [
          <Button icon={<ReloadOutlined />} key="reload" onClick={reload} text="刷新" />,
          <Button
            icon={<PlusOutlined />}
            key="create"
            onClick={() => {
              setEditing(undefined);
              setFormOpen(true);
            }}
            text="新增节点"
            type="primary"
          />,
        ]}
      />

      <HostFormModal
        host={editing}
        onCancel={() => setFormOpen(false)}
        onSaved={(host) => {
          setFormOpen(false);
          setSelected(host);
          reload();
        }}
        open={formOpen}
      />
    </Flex>
  );
}
