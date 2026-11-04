import { Alert, Form, Input, Modal, Select } from "antd";
import { Button } from "lingting-react-ui";
import { useEffect, useMemo, useState } from "react";

import { ApiError, createProject, updateProject, type Project } from "@/lib/api";
import {
  isDefaultHostMissing,
  toCreateProjectRequest,
  toProjectFormValues,
  toUpdateProjectRequest,
  type ProjectFormValues,
} from "@/pages/projects/projects";
import type { HostQueryState } from "@/pages/tasks/useHosts";

/** 把未知错误转成可读说明，保留服务端返回的业务提示 */
function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动。";
    if (error.kind === "timeout") return "请求超时，结果无法确认，请刷新列表核对。";
    if (error.kind === "aborted") return "请求已取消。";
    return error.message;
  }
  return error instanceof Error ? error.message : "未知错误";
}

export type ProjectFormModalProps = {
  open: boolean;
  /** 编辑时传入真实项目记录；为空表示新增 */
  project?: Project;
  onCancel: () => void;
  onSaved: (project: Project) => void;
  /**
   * 执行节点查询状态。
   *
   * 由页面统一查询后传入：加载中、查询失败与空列表是三种不同事实，弹窗不得把失败
   * 当成"没有节点"，也不得在前端判断节点是否可执行。
   */
  hostState: HostQueryState & { reload: () => void };
};

/**
 * 项目新增 / 编辑弹窗。
 *
 * 表单只写非机密元数据：仓库地址内嵌凭据会被服务端拒绝，因此这里不做本地清洗，
 * 也不提供任何凭据字段。项目标识只在新增时可填，修改时不可变——它同时是缓存共享
 * 范围键与远程管理目录名，改名会让同一项目的任务落到不同位置。
 *
 * 环境版本可以留空，表示使用当前可用版本；留空不会被当作某个具体版本。
 *
 * 默认执行节点是可选项：可选项只能来自服务端返回的节点列表，也可以选择"不设置"。
 * 选定节点只是保存配置偏好，不代表该节点已通过环境预检或当前可用于真实远程执行。
 */
export function ProjectFormModal({
  open,
  project,
  onCancel,
  onSaved,
  hostState,
}: ProjectFormModalProps) {
  const [form] = Form.useForm<ProjectFormValues>();
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();

  const editing = Boolean(project);
  const initialValues = useMemo<Partial<ProjectFormValues>>(() => {
    if (!project) return { environment_version: undefined };
    return toProjectFormValues(project) as Partial<ProjectFormValues>;
  }, [project]);

  // 节点可选项只来自服务端返回的列表；"不设置"始终可选，使清空旧引用永远有入口。
  const hostOptions = useMemo(() => {
    const unset = { label: "不设置默认执行节点", value: "" };
    if (hostState.status === "error") return [unset];
    return [
      unset,
      ...hostState.hosts.map((host) => ({ label: `${host.name}（${host.id}）`, value: host.id })),
    ];
  }, [hostState.hosts, hostState.status]);
  const hostQueryFailed = hostState.status === "error";
  // 项目指向的节点可能已被逻辑删除：这类项目仍须可编辑，并允许清除旧引用。
  const defaultHostMissing = isDefaultHostMissing({
    hostID: project?.default_host_id,
    hosts: hostState.hosts,
    loaded: hostState.status === "ready",
  });
  // 打开弹窗时按当前记录重置表单：同一个弹窗复用于新增与编辑，残留取值会误导用户。
  useEffect(() => {
    if (!open) return;
    setError(undefined);
    form.resetFields();
    if (project) {
      form.setFieldsValue(toProjectFormValues(project) as Partial<ProjectFormValues>);
    }
  }, [form, open, project]);

  const handleOk = async () => {
    setError(undefined);
    let values: ProjectFormValues;
    try {
      values = await form.validateFields();
    } catch {
      // 校验失败时 Form 已在字段下方给出提示，这里不重复。
      return;
    }
    setSubmitting(true);
    try {
      const saved = project
        ? await updateProject(project.id, toUpdateProjectRequest(values))
        : await createProject(toCreateProjectRequest(values));
      onSaved(saved.data);
    } catch (cause) {
      setError(describeError(cause));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      cancelText="取消"
      confirmLoading={submitting}
      destroyOnHidden
      okText={editing ? "保存" : "创建"}
      open={open}
      title={editing ? "修改项目" : "新增项目"}
      width={640}
      onCancel={() => {
        setError(undefined);
        onCancel();
      }}
      onOk={() => void handleOk()}
    >
      {error && <Alert description={error} showIcon style={{ marginBottom: 16 }} type="error" />}
      {hostQueryFailed && (
        <Alert
          action={<Button onClick={hostState.reload} size="small" text="重试" />}
          description={`${hostState.error} 在节点列表恢复之前，默认执行节点只能保持当前取值或选择"不设置"。`}
          showIcon
          style={{ marginBottom: 16 }}
          title="加载执行节点失败"
          type="error"
        />
      )}
      {defaultHostMissing && (
        <Alert
          description={
            `项目当前指向的执行节点 ${project?.default_host_id} 不在服务端返回的节点列表中（可能已被逻辑删除）。` +
            "保留它不影响保存其它配置，也可以重新选择一个节点，或选择「不设置默认执行节点」清除该引用。"
          }
          showIcon
          style={{ marginBottom: 16 }}
          title="默认执行节点不在节点列表中"
          type="warning"
        />
      )}
      {hostState.status === "ready" && hostState.hosts.length === 0 && (
        <Alert
          description="服务端还没有登记任何执行节点，此时只能选择「不设置默认执行节点」。"
          showIcon
          style={{ marginBottom: 16 }}
          title="暂无可选执行节点"
          type="info"
        />
      )}
      <Form form={form} initialValues={initialValues} layout="vertical" preserve={false}>
        <Form.Item
          extra="项目标识同时是缓存共享范围与远程管理工作目录名，创建后不可修改"
          label="项目标识"
          name="id"
          rules={[
            { message: "请输入项目标识", required: true },
            {
              message: "只能包含小写字母、数字与连字符，且不超过 32 个字符",
              pattern: /^[a-z0-9][a-z0-9-]{0,31}$/,
            },
          ]}
        >
          <Input disabled={editing} placeholder="例如 demo-service" />
        </Form.Item>
        <Form.Item
          label="项目名称"
          name="name"
          rules={[{ message: "请输入项目名称", required: true }]}
        >
          <Input placeholder="例如 订单服务" />
        </Form.Item>
        <Form.Item
          extra="只允许 ssh://、https:// 与 host:path 三种形式；内嵌凭据的地址会被拒绝"
          label="仓库地址"
          name="repository_url"
          rules={[{ message: "请输入仓库地址", required: true }]}
        >
          <Input placeholder="ssh://git.example.com/team/repo" />
        </Form.Item>
        <Form.Item
          label="基础分支"
          name="base_branch"
          rules={[{ message: "请输入基础分支", required: true }]}
        >
          <Input placeholder="main" />
        </Form.Item>
        <Form.Item
          label="语言环境"
          name="environment"
          rules={[{ message: "请输入语言环境名称", required: true }]}
        >
          <Input placeholder="例如 go、node" />
        </Form.Item>
        <Form.Item
          extra="留空表示使用当前可用版本，不会被当作某个具体版本"
          label="环境版本（可选）"
          name="environment_version"
        >
          <Input placeholder="例如 v0001" />
        </Form.Item>
        <Form.Item
          extra="每行或逗号分隔一条；类型必须由该语言环境定义"
          label="缓存类型（可选）"
          name="caches"
        >
          <Input.TextArea placeholder={"go-mod\nnode-npm"} rows={2} />
        </Form.Item>
        <Form.Item
          extra="必须是 /workspace 之下的安全绝对路径；留空使用默认目录"
          label="容器内工作目录（可选）"
          name="work_dir"
        >
          <Input placeholder="/workspace/project" />
        </Form.Item>
        <Form.Item
          extra={
            "只登记默认执行节点的配置偏好：选定节点不代表它已通过环境预检或当前可用于真实远程执行，" +
            "真实可用性由服务端在发起时核验；留空表示不设置默认节点"
          }
          label="默认执行节点（可选）"
          name="default_host_id"
        >
          <Select
            aria-label="默认执行节点"
            allowClear
            disabled={hostState.status === "loading"}
            loading={hostState.status === "loading"}
            optionFilterProp="label"
            options={hostOptions}
            placeholder={
              hostState.status === "loading"
                ? "正在加载执行节点…"
                : hostQueryFailed
                  ? "执行节点列表不可用"
                  : "不设置默认执行节点"
            }
            showSearch
          />
        </Form.Item>
      </Form>
    </Modal>
  );
}
