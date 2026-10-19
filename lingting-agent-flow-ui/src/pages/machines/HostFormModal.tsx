import { Form, Input, InputNumber, Modal, Space, Switch, Typography } from "antd";
import { useEffect, useState } from "react";

import {
  ApiError,
  createHost,
  updateHost,
  type CreateHostRequest,
  type Host,
  type UpdateHostRequest,
} from "@/lib/api";

/** 表单字段：与接口请求体保持一致 */
type HostFormValues = {
  name?: string;
  host: string;
  port: number;
  username: string;
  workspace_root?: string;
  private_key_path: string;
  enabled?: boolean;
  default?: boolean;
};

/** 默认端口，与 Go 端 domain.DefaultSSHPort 一致 */
const DEFAULT_PORT = 22;

/**
 * 把表单值转换为新增请求体，省略空字段。
 *
 * 认证方式固定为无口令私钥，因此请求体不再包含 auth_method。
 */
export function toCreateHostRequest(values: HostFormValues): CreateHostRequest {
  const request: CreateHostRequest = {
    default: values.default ?? false,
    host: values.host.trim(),
    name: values.name?.trim() || undefined,
    port: values.port,
    private_key_path: values.private_key_path.trim(),
    username: values.username.trim(),
    workspace_root: values.workspace_root?.trim() || undefined,
  };
  for (const key of Object.keys(request) as (keyof CreateHostRequest)[]) {
    if (request[key] === undefined) {
      delete request[key];
    }
  }
  return request;
}

/** 把表单值转换为修改请求体，只保留允许修改的字段 */
export function toUpdateHostRequest(values: HostFormValues): UpdateHostRequest {
  return {
    default: values.default ?? false,
    enabled: values.enabled ?? true,
    host: values.host.trim(),
    name: values.name?.trim() || undefined,
    port: values.port,
    private_key_path: values.private_key_path.trim(),
    username: values.username.trim(),
    workspace_root: values.workspace_root?.trim() || undefined,
  };
}

/** 把未知错误转成可读说明，保留服务端返回的业务提示 */
function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动后重试。";
    if (error.kind === "timeout") return "请求超时，请稍后重试。";
    return error.message;
  }
  return error instanceof Error ? error.message : "未知错误";
}

/** 新增或修改执行节点的弹窗表单 */
export function HostFormModal({
  host,
  onCancel,
  onSaved,
  open,
}: {
  /** 为空表示新增 */
  host?: Host;
  onCancel: () => void;
  onSaved: (host: Host) => void;
  open: boolean;
}) {
  const [form] = Form.useForm<HostFormValues>();
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (!open) return;
    setError(undefined);
    form.setFieldsValue({
      default: host?.is_default ?? false,
      enabled: host?.enabled ?? true,
      host: host?.host ?? "",
      name: host?.name ?? "",
      port: host?.port ?? DEFAULT_PORT,
      private_key_path: host?.private_key_path ?? "",
      username: host?.username ?? "",
      workspace_root: host?.workspace_root ?? "",
    });
  }, [form, host, open]);

  const handleSubmit = async () => {
    const values = await form.validateFields();
    setSubmitting(true);
    setError(undefined);
    try {
      const { data } = host
        ? await updateHost(host.id, toUpdateHostRequest(values))
        : await createHost(toCreateHostRequest(values));
      onSaved(data);
    } catch (cause) {
      setError(describeError(cause));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      confirmLoading={submitting}
      destroyOnHidden
      okText={host ? "保存" : "创建"}
      onCancel={onCancel}
      onOk={() => void handleSubmit()}
      open={open}
      title={host ? "修改执行节点" : "新增执行节点"}
    >
      <Form form={form} layout="vertical" preserve={false}>
        <Form.Item label="节点名称" name="name">
          <Input placeholder="省略时使用主机地址" />
        </Form.Item>
        <Form.Item
          label="主机地址"
          name="host"
          rules={[{ message: "请填写 IP 地址或主机名", required: true }]}
        >
          <Input placeholder="192.0.2.10 或 build.example.com" />
        </Form.Item>
        <Space size="middle" style={{ display: "flex" }}>
          <Form.Item label="SSH 端口" name="port" style={{ flex: 1 }}>
            <InputNumber max={65535} min={1} style={{ width: "100%" }} />
          </Form.Item>
          <Form.Item
            label="登录用户名"
            name="username"
            rules={[{ message: "请填写 SSH 登录用户名", required: true }]}
            style={{ flex: 1 }}
          >
            <Input placeholder="deploy" />
          </Form.Item>
        </Space>
        <Form.Item label="远程工作空间" name="workspace_root">
          <Input placeholder="~/.lingting/agentflow（~ 指远程用户 Home）" />
        </Form.Item>
        <Form.Item
          extra="只支持无口令私钥：私钥文件不会被复制或修改，AgentFlow 只保存其路径。"
          label="本机私钥文件路径"
          name="private_key_path"
          rules={[{ message: "必须指定私钥文件路径", required: true }]}
        >
          <Input placeholder="~/.ssh/id_ed25519" />
        </Form.Item>
        {host && (
          <Form.Item label="启用" name="enabled" valuePropName="checked">
            <Switch />
          </Form.Item>
        )}
        <Form.Item label="设为默认节点" name="default" valuePropName="checked">
          <Switch />
        </Form.Item>
        {error && <Typography.Text type="danger">{error}</Typography.Text>}
      </Form>
    </Modal>
  );
}
