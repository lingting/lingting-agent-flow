import { Alert, Form, Input, Modal, Select, Space, Switch, Typography } from "antd";
import { useEffect, useMemo, useState } from "react";

import { ApiError, createTask, type CreateTaskRequest, type Environment } from "@/lib/api";
import {
  describeProjectEnvironment,
  findProject,
  projectSelectOptions,
  resolveProjectSelection,
} from "@/pages/projects/projects";
import { useProjects } from "@/pages/projects/useProjects";
import { EnvironmentSelector } from "@/pages/tasks/EnvironmentSelector";
import { resolveEnvironmentSelection } from "@/pages/tasks/environments";
import { projectTaskPrefill } from "@/pages/tasks/projectPrefill";
import {
  hostSelectOptions,
  resolveHostSelection,
  resolveRoleSelection,
  roleSelectOptions,
  ROLE_TYPE_META,
} from "@/pages/tasks/selectors";
import { useEnvironments } from "@/pages/tasks/useEnvironments";
import { useHosts } from "@/pages/tasks/useHosts";
import { useRoles } from "@/pages/tasks/useRoles";

/** 表单字段：与接口请求体保持一致，数组类字段以换行分隔输入 */
type CreateTaskFormValues = {
  name: string;
  description: string;
  project_id: string;
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
    project_id: values.project_id.trim(),
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
 * 四类外部标识全部来自真实接口，不再手工填写：
 *   - 项目：`GET /api/v1/projects`，任务必须关联项目，未填写的执行配置从项目继承；
 *   - 执行节点：`GET /api/v1/hosts`，只有已启用且 `usable_for_remote_task` 的节点可选；
 *   - 角色：`GET /api/v1/roles`，按 `type` 分类并排除 `enabled=false` 的记录；
 *   - 语言环境与版本：`GET /api/v1/environments?host_id=…`，节点变化时重新加载。
 *
 * 任一查询失败、为空或尚未加载完成时都不提交：前端禁用只是提示，服务端仍是最终权威。
 */
export function CreateTaskModal({ open, onCancel, onCreated }: CreateTaskModalProps) {
  const [form] = Form.useForm<CreateTaskFormValues>();
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();
  // 表单是唯一取值来源：选择器只写表单，展示值由 useWatch 派生。
  // 执行节点直接决定环境查询，节点变化时 useEnvironments 会自动重新加载。
  const watchedHostID = Form.useWatch("host_id", form);
  const watchedProjectID = Form.useWatch("project_id", form);
  const selectedEnvironment = Form.useWatch("environment", form);
  const selectedVersion = Form.useWatch("environment_version", form);
  const hosts = useHosts();
  const roles = useRoles();
  const projects = useProjects();
  const environments = useEnvironments(watchedHostID);
  const projectOptions = useMemo(
    () => projectSelectOptions(projects.projects),
    [projects.projects],
  );
  const selectedProject = useMemo(
    () => findProject(projects.projects, watchedProjectID),
    [projects.projects, watchedProjectID],
  );

  const hostOptions = useMemo(() => hostSelectOptions(hosts.hosts), [hosts.hosts]);
  const plannerOptions = useMemo(() => roleSelectOptions(roles.roles, "planner"), [roles.roles]);
  const developerOptions = useMemo(
    () => roleSelectOptions(roles.roles, "developer"),
    [roles.roles],
  );
  const validatorOptions = useMemo(
    () => roleSelectOptions(roles.roles, "validator"),
    [roles.roles],
  );
  const hasEnabledPlanner = plannerOptions.some((option) => !option.disabled);
  const hasEnabledDeveloper = developerOptions.some((option) => !option.disabled);

  const initialValues = useMemo<Partial<CreateTaskFormValues>>(
    () => ({ merge_after_push: false, priority: "normal" }),
    [],
  );

  /**
   * 计算当前项目可以预填到表单的字段。
   *
   * 环境与版本只在当前执行节点的环境列表已加载时才预填：跨节点的旧选择会被提交校验
   * 拒绝，提前塞进一个该节点并不存在的环境只会制造必然失败的配置。
   */
  const projectPrefillFor = (projectID?: string) =>
    projectTaskPrefill({
      environments: environments.environments,
      environmentsLoaded: environments.status === "ready",
      project: findProject(projects.projects, projectID),
    });

  /**
   * 表单字段联动。
   *
   *   - 选择项目：预填仓库地址、基础分支、工作目录，环境与版本按当前节点的真实环境
   *     列表决定是否预填；用户之后仍可手工覆盖，服务端以请求体为准。
   *   - 执行节点变化：清除与旧节点关联的环境与版本，避免提交跨节点的旧选择。
   */
  const handleValuesChange = (changed: Partial<CreateTaskFormValues>) => {
    if ("host_id" in changed) {
      form.setFieldsValue({ environment: undefined, environment_version: undefined });
    }
    if ("project_id" in changed) {
      form.setFieldsValue(projectPrefillFor(changed.project_id));
    }
  };

  /**
   * 环境列表就绪后补齐项目预填。
   *
   * 覆盖"先选项目、后选节点"的顺序：节点变化会清空环境与版本，等新节点的环境列表
   * 加载完成后才谈得上按项目预填。已经选过环境时不覆盖用户的选择。
   */
  useEffect(() => {
    if (environments.status !== "ready") return;
    if (!watchedProjectID) return;
    if (form.getFieldValue("environment")) return;
    const prefill = projectPrefillFor(watchedProjectID);
    if (!prefill.environment) return;
    form.setFieldsValue({
      environment: prefill.environment,
      environment_version: prefill.environment_version,
    });
    // projectPrefillFor 依赖 environments 与 projects，两者已在依赖列表中体现。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [environments.status, environments.environments, projects.projects, watchedProjectID, form]);

  /** 环境变化：版本必须重新选择，旧环境的版本不适用于新环境 */
  const handleEnvironmentChange = (environment?: Environment) => {
    form.setFieldsValue({ environment: environment?.name, environment_version: undefined });
  };

  /**
   * 提交前的完整校验。
   *
   * 依次确认：节点、角色、环境三份数据都已成功加载，选择合法且属于当前节点，
   * 然后才把真实标识写入请求体。任何一步失败都给出可读原因并阻止提交。
   */
  const handleOk = async () => {
    setError(undefined);
    let values: CreateTaskFormValues;
    try {
      values = await form.validateFields();
    } catch {
      // 校验失败时 Form 已经在字段下方给出提示，这里不再重复提示。
      return;
    }
    const projectSelection = resolveProjectSelection({
      loaded: projects.status === "ready",
      projectID: values.project_id,
      projects: projects.projects,
    });
    if (!projectSelection.ok) {
      setError(projectSelection.error);
      return;
    }
    const hostSelection = resolveHostSelection({
      hostID: values.host_id,
      hosts: hosts.hosts,
      loaded: hosts.status === "ready",
    });
    if (!hostSelection.ok) {
      setError(hostSelection.error);
      return;
    }
    const roleSelection = resolveRoleSelection({
      developerRoleID: values.developer_role_id,
      loaded: roles.status === "ready",
      plannerRoleID: values.planner_role_id,
      roles: roles.roles,
      validatorRoleID: values.validator_role_id,
    });
    if (!roleSelection.ok) {
      setError(roleSelection.error);
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
          developer_role_id: roleSelection.developer_role_id,
          environment: selection.environment,
          environment_version: selection.environment_version,
          host_id: hostSelection.host_id,
          project_id: projectSelection.project.id,
          planner_role_id: roleSelection.planner_role_id,
          validator_role_id: roleSelection.validator_role_id,
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
        <Form.Item label="关联项目" required>
          <Space direction="vertical" size={4} style={{ display: "flex" }}>
            {projects.status === "loading" && <Alert showIcon title="正在加载项目…" type="info" />}
            {projects.status === "error" && (
              <Alert
                action={<Typography.Link onClick={projects.reload}>重试</Typography.Link>}
                description={projects.error}
                showIcon
                title="项目查询失败"
                type="error"
              />
            )}
            {projects.status === "ready" && projects.projects.length === 0 && (
              <Alert
                description="服务端还没有登记任何项目。任务必须关联项目，请先在项目页新增一个项目。"
                showIcon
                title="没有可用的项目"
                type="warning"
              />
            )}
            {projects.status === "ready" && projects.projects.length > 0 && (
              <Form.Item
                noStyle
                name="project_id"
                rules={[{ message: "请选择项目", required: true }]}
              >
                <Select
                  aria-label="关联项目"
                  optionFilterProp="label"
                  options={projectOptions}
                  placeholder="选择项目"
                  showSearch
                />
              </Form.Item>
            )}
          </Space>
          {selectedProject && (
            <Typography.Text type="secondary">
              仓库 {selectedProject.repository_url} · 分支 {selectedProject.base_branch} · 环境{" "}
              {describeProjectEnvironment(selectedProject)}
            </Typography.Text>
          )}
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
          执行节点与角色来自真实接口，只有服务端标记可用的记录才能选择。这些配置只被保存，
          创建任务不会触发执行；创建成功后需在任务列表显式点击"启动"。
        </Typography.Paragraph>

        <Form.Item label="执行节点" required>
          <Space direction="vertical" size={4} style={{ display: "flex" }}>
            {hosts.status === "loading" && <Alert showIcon title="正在加载执行节点…" type="info" />}
            {hosts.status === "error" && (
              <Alert
                action={<Typography.Link onClick={hosts.reload}>重试</Typography.Link>}
                description={hosts.error}
                showIcon
                title="执行节点查询失败"
                type="error"
              />
            )}
            {hosts.status === "ready" && hosts.hosts.length === 0 && (
              <Alert
                description="服务端还没有登记任何执行节点，请先在机器页新增并等待环境预检通过。"
                showIcon
                title="没有可用的执行节点"
                type="warning"
              />
            )}
            {hosts.status === "ready" && hosts.hosts.length > 0 && (
              <Form.Item
                noStyle
                name="host_id"
                rules={[{ message: "请选择执行节点", required: true }]}
              >
                <Select
                  aria-label="执行节点"
                  disabled={hosts.hosts.every(
                    (host) => !host.enabled || !host.usable_for_remote_task,
                  )}
                  options={hostOptions.map((option) => ({
                    disabled: option.disabled,
                    label: option.label,
                    title: option.unavailableReason,
                    value: option.value,
                  }))}
                  placeholder="选择执行节点"
                />
              </Form.Item>
            )}
          </Space>
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

        <Typography.Text strong>角色配置</Typography.Text>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 8 }}>
          角色来自真实接口，按类型区分；已禁用角色展示为不可选。规划角色与开发角色必填，
          验证角色可选。
        </Typography.Paragraph>
        {roles.status === "loading" && (
          <Alert showIcon style={{ marginBottom: 16 }} title="正在加载角色…" type="info" />
        )}
        {roles.status === "error" && (
          <Alert
            action={<Typography.Link onClick={roles.reload}>重试</Typography.Link>}
            description={roles.error}
            showIcon
            style={{ marginBottom: 16 }}
            title="角色查询失败"
            type="error"
          />
        )}
        {roles.status === "ready" && roles.roles.length === 0 && (
          <Alert
            description="服务端没有任何角色记录，无法创建任务。请检查角色初始化配置。"
            showIcon
            style={{ marginBottom: 16 }}
            title="没有可用的角色"
            type="warning"
          />
        )}
        {roles.status === "ready" && roles.roles.length > 0 && (
          <>
            {!hasEnabledPlanner && (
              <Alert
                description="当前没有启用的规划角色，无法创建任务。"
                showIcon
                style={{ marginBottom: 16 }}
                title="缺少启用的规划角色"
                type="warning"
              />
            )}
            {!hasEnabledDeveloper && (
              <Alert
                description="当前没有启用的开发角色，无法创建任务。"
                showIcon
                style={{ marginBottom: 16 }}
                title="缺少启用的开发角色"
                type="warning"
              />
            )}
            {validatorOptions.every((option) => option.disabled) && (
              <Alert
                description="当前没有启用的验证角色，任务将只做确定性验证。"
                showIcon
                style={{ marginBottom: 16 }}
                title="没有启用的验证角色"
                type="info"
              />
            )}
            <Space align="start" size="middle" style={{ display: "flex" }}>
              <Form.Item
                label={ROLE_TYPE_META.planner.label}
                name="planner_role_id"
                rules={[{ message: "请选择规划角色", required: true }]}
                style={{ flex: 1 }}
              >
                <Select
                  aria-label="规划角色"
                  disabled={!hasEnabledPlanner}
                  options={plannerOptions.map((option) => ({ ...option, title: option.label }))}
                  placeholder="选择规划角色"
                />
              </Form.Item>
              <Form.Item
                label={ROLE_TYPE_META.developer.label}
                name="developer_role_id"
                rules={[{ message: "请选择开发角色", required: true }]}
                style={{ flex: 1 }}
              >
                <Select
                  aria-label="开发角色"
                  disabled={!hasEnabledDeveloper}
                  options={developerOptions.map((option) => ({ ...option, title: option.label }))}
                  placeholder="选择开发角色"
                />
              </Form.Item>
              <Form.Item
                label={`${ROLE_TYPE_META.validator.label}（可选）`}
                name="validator_role_id"
                style={{ flex: 1 }}
              >
                <Select
                  allowClear
                  aria-label="验证角色"
                  options={validatorOptions.map((option) => ({ ...option, title: option.label }))}
                  placeholder="不启用语义审查"
                />
              </Form.Item>
            </Space>
          </>
        )}

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
