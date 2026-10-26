import { Alert, Form, Input, Modal, Select, Space, Switch, Typography } from "antd";
import { useMemo, useState } from "react";

import { ApiError, createTask, type CreateTaskRequest, type Environment } from "@/lib/api";
import { EnvironmentSelector } from "@/pages/tasks/EnvironmentSelector";
import { resolveEnvironmentSelection } from "@/pages/tasks/environments";
import { useEnvironments } from "@/pages/tasks/useEnvironments";

/** 表单字段：与接口请求体保持一致，数组类字段以换行分隔输入 */
type CreateTaskFormValues = {
  name: string;
  description: string;
  project_id?: string;
  priority: "low" | "normal" | "high";
  host_id: string;
  repository_url: string;
  base_branch: string;
  environment: string;
  environment_version?: string;
  work_dir?: string;
  commands?: string;
  acceptance_criteria?: string;
  planner_role_id: string;
  developer_role_id: string;
  validator_role_id?: string;
  merge_after_push?: boolean;
};

/** 把多行文本转换为去空的字符串数组 */
function splitLines(value?: string): string[] | undefined {
  if (!value) return undefined;
  const items = value
    .split("\n")
    .map((item) => item.trim())
    .filter((item) => item.length > 0);
  return items.length > 0 ? items : undefined;
}

/** 把表单值转换为接口请求体，省略空字段 */
export function toCreateTaskRequest(values: CreateTaskFormValues): CreateTaskRequest {
  const request: CreateTaskRequest = {
    acceptance_criteria: splitLines(values.acceptance_criteria),
    base_branch: values.base_branch.trim(),
    commands: splitLines(values.commands),
    description: values.description.trim(),
    developer_role_id: values.developer_role_id.trim(),
    environment: values.environment.trim(),
    environment_version: values.environment_version?.trim() || undefined,
    host_id: values.host_id.trim(),
    merge_after_push: values.merge_after_push ?? false,
    name: values.name.trim(),
    planner_role_id: values.planner_role_id.trim(),
    priority: values.priority,
    project_id: values.project_id?.trim() || undefined,
    repository_url: values.repository_url.trim(),
    validator_role_id: values.validator_role_id?.trim() || undefined,
    work_dir: values.work_dir?.trim() || undefined,
  };
  // 未填写的可选字段直接移除，避免请求体里出现 undefined 键。
  for (const key of Object.keys(request) as (keyof CreateTaskRequest)[]) {
    if (request[key] === undefined) {
      delete request[key];
    }
  }
  return request;
}

/** 把未知错误转成可读说明 */
function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动。";
    if (error.kind === "timeout") return "请求超时，请稍后重试。";
    return error.message;
  }
  return error instanceof Error ? error.message : "未知错误";
}

export type CreateTaskModalProps = {
  open: boolean;
  onCancel: () => void;
  onCreated: () => void | Promise<void>;
};

/**
 * 创建任务弹窗。
 *
 * 任务创建只落库，不会触发执行：表单不提供状态、开始时间等执行期字段，创建成功后
 * 由用户在任务列表显式发起统一启动（`POST /tasks/{id}/start`）。
 *
 * 语言环境与版本来自真实的环境查询接口，不再手工填写名称：
 *   - 环境列表按当前执行节点查询，节点变化时重新加载并清空旧选择；
 *   - 只有 `usable=true` 的版本可以被选中，不可用版本展示构建状态但被禁用；
 *   - 没有合法可用版本时阻止提交，不猜测一个"看起来合适"的配置。
 *
 * 执行节点与角色仍沿用现有手工填写方式（正式选择器归 P4-M2-4）。
 */
export function CreateTaskModal({ open, onCancel, onCreated }: CreateTaskModalProps) {
  const [form] = Form.useForm<CreateTaskFormValues>();
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();
  // 表单是唯一取值来源：选择器只写表单，展示值由 useWatch 派生。
  // 执行节点直接决定环境查询，节点变化时 useEnvironments 会自动重新加载。
  const watchedHostID = Form.useWatch("host_id", form);
  const selectedEnvironment = Form.useWatch("environment", form);
  const selectedVersion = Form.useWatch("environment_version", form);
  const environments = useEnvironments(watchedHostID);

  const initialValues = useMemo<Partial<CreateTaskFormValues>>(
    () => ({ merge_after_push: false, priority: "normal" }),
    [],
  );

  /**
   * 执行节点变化时清除与旧节点关联的环境与版本选择。
   *
   * 只在节点字段本身变化时触发：环境选择器写入的字段不会连带清空自己。
   */
  const handleValuesChange = (changed: Partial<CreateTaskFormValues>) => {
    if ("host_id" in changed) {
      form.setFieldsValue({ environment: undefined, environment_version: undefined });
    }
  };
  /** 环境变化：版本必须重新选择，旧环境的版本不适用于新环境 */
  const handleEnvironmentChange = (environment?: Environment) => {
    form.setFieldsValue({ environment: environment?.name, environment_version: undefined });
  };

  const handleOk = async () => {
    setError(undefined);
    let values: CreateTaskFormValues;
    try {
      values = await form.validateFields();
    } catch {
      // 校验失败时 Form 已经在字段下方给出提示，这里不再重复提示。
      return;
    }
    const selection = resolveEnvironmentSelection({
      environments: environments.environments,
      loaded: environments.status === "ready",
      name: values.environment,
      version: values.environment_version,
    });
    if (!selection.ok) {
      setError(selection.error);
      return;
    }
    setSubmitting(true);
    try {
      await createTask(
        toCreateTaskRequest({
          ...values,
          environment: selection.environment,
          environment_version: selection.environment_version,
        }),
      );
      form.resetFields();
      await onCreated();
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
      okText="创建"
      open={open}
      title="创建任务"
      width={720}
      onCancel={() => {
        setError(undefined);
        onCancel();
      }}
      onOk={() => void handleOk()}
    >
      {error && <Alert showIcon description={error} style={{ marginBottom: 16 }} type="error" />}
      <Form
        form={form}
        initialValues={initialValues}
        layout="vertical"
        preserve={false}
        onValuesChange={handleValuesChange}
      >
        <Form.Item
          label="任务名称"
          name="name"
          rules={[{ message: "请输入任务名称", required: true }, { max: 200 }]}
        >
          <Input placeholder="例如：实现登录接口" />
        </Form.Item>
        <Form.Item
          label="任务描述"
          name="description"
          rules={[{ message: "请输入任务描述", required: true }, { max: 4000 }]}
        >
          <Input.TextArea
            placeholder="描述执行目标与验收标准"
            rows={3}
            showCount
            maxLength={4000}
          />
        </Form.Item>
        <Form.Item label="关联项目标识（可选）" name="project_id">
          <Input placeholder="例如 demo-service，仅允许小写字母、数字与连字符" />
        </Form.Item>
        <Form.Item label="优先级" name="priority">
          <Select
            options={[
              { label: "低", value: "low" },
              { label: "普通", value: "normal" },
              { label: "高", value: "high" },
            ]}
          />
        </Form.Item>

        <Typography.Text strong>执行配置</Typography.Text>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 8 }}>
          执行节点与角色仍需要手工填写标识（正式选择器归 P4-M2-4）。这些配置只被保存，
          创建任务不会触发执行；创建成功后需在任务列表显式点击"启动"。
        </Typography.Paragraph>
        <Form.Item
          label="执行节点标识"
          name="host_id"
          rules={[{ message: "请输入执行节点标识", required: true }]}
        >
          <Input placeholder="执行节点 ID" />
        </Form.Item>
        <Form.Item
          label="仓库地址"
          name="repository_url"
          rules={[{ message: "请输入仓库地址", required: true }]}
        >
          <Input placeholder="ssh://git.example.com/team/repo" />
        </Form.Item>
        <Space align="start" size="middle" style={{ display: "flex" }}>
          <Form.Item
            label="基础分支"
            name="base_branch"
            rules={[{ message: "请输入基础分支", required: true }]}
            style={{ flex: 1 }}
          >
            <Input placeholder="main" />
          </Form.Item>
        </Space>
        <Form.Item label="语言环境与版本" required>
          <EnvironmentSelector
            environment={selectedEnvironment}
            hostID={watchedHostID}
            query={environments}
            version={selectedVersion}
            onEnvironmentChange={handleEnvironmentChange}
            onVersionChange={(value) => form.setFieldsValue({ environment_version: value })}
          />
        </Form.Item>
        {/* 环境与版本仍作为表单字段保存，但只能由上面的选择器写入，不接受手工输入。 */}
        <Form.Item hidden name="environment">
          <Input />
        </Form.Item>
        <Form.Item hidden name="environment_version">
          <Input />
        </Form.Item>
        <Space align="start" size="middle" style={{ display: "flex" }}>
          <Form.Item
            label="规划角色标识"
            name="planner_role_id"
            rules={[{ message: "请输入规划角色标识", required: true }]}
            style={{ flex: 1 }}
          >
            <Input placeholder="规划角色 ID" />
          </Form.Item>
          <Form.Item
            label="开发角色标识"
            name="developer_role_id"
            rules={[{ message: "请输入开发角色标识", required: true }]}
            style={{ flex: 1 }}
          >
            <Input placeholder="开发角色 ID" />
          </Form.Item>
          <Form.Item label="验证角色标识（可选）" name="validator_role_id" style={{ flex: 1 }}>
            <Input placeholder="验证角色 ID" />
          </Form.Item>
        </Space>
        <Form.Item label="容器内工作目录（可选）" name="work_dir">
          <Input placeholder="/workspace/project" />
        </Form.Item>
        <Form.Item
          label="必需验证命令（可选）"
          name="commands"
          extra="每行一条，形式为「名称=可执行文件 参数...」"
        >
          <Input.TextArea placeholder={"build=go build ./...\ntest=go test ./..."} rows={2} />
        </Form.Item>
        <Form.Item label="验收条件（可选）" name="acceptance_criteria" extra="每行一条">
          <Input.TextArea rows={2} />
        </Form.Item>
        <Form.Item label="推送成功后合并基础分支" name="merge_after_push" valuePropName="checked">
          <Switch />
        </Form.Item>
      </Form>
    </Modal>
  );
}
