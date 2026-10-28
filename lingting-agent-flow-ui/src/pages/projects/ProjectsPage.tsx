import { PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import type { ActionType } from "@ant-design/pro-components";
import { Alert, Flex, Space, Typography } from "antd";
import { Button, type ExTableColumn, ExTable, LinkButton } from "lingting-react-ui";
import { useCallback, useMemo, useRef, useState } from "react";

import { ApiError, deleteProject, fetchProjects, type Project } from "@/lib/api";
import { ProjectFormModal } from "@/pages/projects/ProjectFormModal";
import {
  describeProjectCaches,
  describeProjectEnvironment,
  PROJECT_CACHES_FALLBACK,
  PROJECT_VERSION_FALLBACK,
} from "@/pages/projects/projects";

/** 把未知错误转成可读说明，保留服务端返回的业务提示 */
function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动。";
    if (error.kind === "timeout") return "请求超时，请稍后重试。";
    if (error.kind === "aborted") return "请求已取消。";
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
 * 项目管理页。
 *
 * 项目登记"同一份代码 + 同一套执行环境"：仓库地址、基础分支、语言环境与版本、缓存
 * 类型、容器内工作目录。任务必须关联项目，未在创建任务时给出的字段从项目继承。
 *
 * 页面只操作真实接口：列表来自 `GET /api/v1/projects`，新增 / 修改 / 删除分别对应
 * `POST` / `PUT` / `DELETE`。删除是逻辑删除，历史任务与代码保存记录仍保留项目标识，
 * 页面必须把这层后果写清楚，不能只说"已删除"。
 */
export function ProjectsPage() {
  const actionRef = useRef<ActionType>(null);
  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<Project>();
  const [loadError, setLoadError] = useState<string>();
  const [actionError, setActionError] = useState<string>();

  const reload = useCallback(() => {
    // ProTable 的 reload 返回 Promise；这里不需要等待结果，失败由 request 内部记录。
    void actionRef.current?.reload();
  }, []);

  const handleDelete = useCallback(
    async (project: Project) => {
      setActionError(undefined);
      try {
        await deleteProject(project.id);
        reload();
      } catch (cause) {
        setActionError(describeError(cause));
      }
    },
    [reload],
  );

  const columns = useMemo<ExTableColumn<Project & Record<string, unknown>>[]>(
    () => [
      {
        dataIndex: "name",
        render: (_dom, record) => (
          <Flex gap={2} vertical>
            <Typography.Text strong>{record.name}</Typography.Text>
            <Typography.Text type="secondary">{record.id}</Typography.Text>
          </Flex>
        ),
        search: false,
        title: "项目",
      },
      {
        dataIndex: "repository_url",
        render: (_dom, record) => (
          <Typography.Text ellipsis style={{ maxWidth: 260 }}>
            {record.repository_url}
          </Typography.Text>
        ),
        search: false,
        title: "仓库地址",
      },
      {
        dataIndex: "base_branch",
        search: false,
        title: "基础分支",
      },
      {
        dataIndex: "environment",
        render: (_dom, record) => describeProjectEnvironment(record),
        search: false,
        title: "环境",
      },
      {
        dataIndex: "caches",
        render: (_dom, record) => (
          <Typography.Text type={record.caches.length === 0 ? "secondary" : undefined}>
            {describeProjectCaches(record)}
          </Typography.Text>
        ),
        search: false,
        title: "缓存类型",
      },
      {
        dataIndex: "work_dir",
        search: false,
        title: "工作目录",
      },
      {
        dataIndex: "updated_at",
        render: (_dom, record) => formatTime(record.updated_at),
        search: false,
        title: "最后更新",
      },
      {
        dataIndex: "option",
        render: (_dom, record) => (
          <Space>
            <LinkButton
              onClick={() => {
                setEditing(record);
                setFormOpen(true);
              }}
              text="修改"
            />
            <LinkButton
              confirm={{
                cancelText: "取消",
                description:
                  "删除只标记项目下线：历史任务、代码获取与代码保存记录仍保留该标识，" +
                  "任务自身的配置快照也不变。删除后不能再创建关联该项目的任务。",
                okText: "确认删除",
                title: `确认删除项目「${record.name}」？`,
              }}
              danger
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
    [handleDelete],
  );

  /**
   * 列表请求。
   *
   * 请求失败时清空数据并记录错误，由页面顶部提示给出重试入口，不把失败伪装成空列表：
   * 任务必须关联项目，把失败显示成"没有项目"会误导用户去新建而不是排查服务端。
   */
  const request = useCallback(async () => {
    setLoadError(undefined);
    try {
      const { data } = await fetchProjects();
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
          title="加载项目失败"
          type="error"
        />
      )}
      {actionError && (
        <Alert closable description={actionError} showIcon title="操作失败" type="error" />
      )}

      <Alert
        description={
          "项目把仓库地址、基础分支、语言环境与版本、缓存类型、容器内工作目录登记一次。" +
          "任务必须关联项目，创建任务时未填写的字段从项目继承；" +
          "项目标识同时决定项目级缓存的共享范围与远程管理工作目录，因此创建后不可修改。" +
          `环境版本留空表示使用${PROJECT_VERSION_FALLBACK}，不等于某个具体版本；` +
          `缓存类型为空显示为「${PROJECT_CACHES_FALLBACK}」。`
        }
        showIcon
        title="项目的作用"
        type="info"
      />

      <ExTable<Project & Record<string, unknown>>
        actionRef={actionRef}
        columns={columns}
        locale={{
          // 加载失败与"确实没有项目"是两种事实：失败时不能显示要求用户去新增项目的空状态。
          emptyText: loadError
            ? "项目数据加载失败，请重试"
            : "尚未登记项目：任务必须关联项目，请先新增一个项目",
        }}
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
            text="新增项目"
            type="primary"
          />,
        ]}
      />

      <ProjectFormModal
        open={formOpen}
        project={editing}
        onCancel={() => setFormOpen(false)}
        onSaved={() => {
          setFormOpen(false);
          reload();
        }}
      />
    </Flex>
  );
}
