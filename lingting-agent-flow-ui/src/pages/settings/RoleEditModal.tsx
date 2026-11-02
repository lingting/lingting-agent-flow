/**
 * 编辑固定角色的可编辑配置。
 *
 * 只提交 `system_prompt`、`model` 与 `enabled`：角色标识、名称、类型与 Runner 由固定角色
 * 定义决定，工具策略本阶段只展示不可编辑。打开时从服务端读取完整配置，保存后使用服务端
 * 返回的真实保存结果刷新展示，不根据本地入参推测保存结果。
 */
import { Alert, Flex, Input, Modal, Switch, Tag, Typography } from "antd";
import { useEffect, useRef, useState } from "react";

import { fetchRole, type Role, type RoleDetail, updateRole } from "@/lib/api";
import {
  describeRoleError,
  roleDutyLabel,
  toolsPolicySummary,
} from "@/pages/settings/settings-roles";
import { roleTypeLabel } from "@/pages/tasks/selectors";

export type RoleEditModalProps = {
  /** 目标角色；为空时不打开对话框 */
  role?: Role;
  onCancel: () => void;
  /** 保存成功后回调，调用方据此重新读取角色列表 */
  onSaved: (role: RoleDetail) => void;
  open: boolean;
};

export function RoleEditModal({ onCancel, onSaved, open, role }: RoleEditModalProps) {
  const [detail, setDetail] = useState<RoleDetail>();
  const [prompt, setPrompt] = useState("");
  const [model, setModel] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  /** 只接受最近一次读取的结果，避免快速切换角色时旧配置覆盖新配置 */
  const loadRef = useRef(0);
  const roleID = role?.id ?? "";

  useEffect(() => {
    if (!open || !roleID) return;
    const requestID = loadRef.current + 1;
    loadRef.current = requestID;
    setDetail(undefined);
    setPrompt("");
    setModel("");
    setError(undefined);
    setNotice(undefined);
    setLoading(true);
    fetchRole(roleID)
      .then(({ data }) => {
        if (loadRef.current !== requestID) return;
        setDetail(data);
        setPrompt(data.system_prompt);
        setModel(data.model);
        setEnabled(data.enabled);
      })
      .catch((cause: unknown) => {
        if (loadRef.current !== requestID) return;
        // 读取失败时不给出可编辑的空表单：那会让用户以为配置本来就是空的。
        setError(`读取角色配置失败：${describeRoleError(cause)}`);
      })
      .finally(() => {
        if (loadRef.current !== requestID) return;
        setLoading(false);
      });
  }, [open, roleID]);

  const handleClose = () => {
    loadRef.current += 1;
    setDetail(undefined);
    setPrompt("");
    setModel("");
    setError(undefined);
    setNotice(undefined);
    onCancel();
  };
  /** 任一字段被修改后清除成功提示：避免下一次"保存"被当成关闭操作 */
  const markDirty = () => {
    if (notice) setNotice(undefined);
  };

  const handleSave = async () => {
    if (!detail) return;
    setSaving(true);
    setError(undefined);
    setNotice(undefined);
    try {
      const { data } = await updateRole(detail.id, {
        enabled,
        model,
        system_prompt: prompt,
      });
      setDetail(data);
      setPrompt(data.system_prompt);
      setModel(data.model);
      setEnabled(data.enabled);
      setNotice("角色配置已保存");
      onSaved(data);
    } catch (cause) {
      setError(`保存失败：${describeRoleError(cause)}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      destroyOnHidden
      okButtonProps={{ disabled: loading || !detail, loading: saving }}
      okText="保存"
      open={open}
      title={role ? `编辑角色：${role.name}` : "编辑角色"}
      width={720}
      onCancel={handleClose}
      onOk={() => {
        if (notice) {
          handleClose();
          return;
        }
        void handleSave();
      }}
    >
      <Flex gap="middle" vertical>
        {error && <Alert showIcon description={error} title="操作失败" type="error" />}
        {notice && <Alert showIcon description={notice} title="已保存" type="success" />}
        {detail && (
          <>
            <Flex gap="small" wrap>
              <Typography.Text type="secondary">{`角色标识：${detail.id}`}</Typography.Text>
              <Typography.Text type="secondary">{`类型：${roleTypeLabel(detail.type)}`}</Typography.Text>
              <Typography.Text type="secondary">{`执行器：${detail.runner}`}</Typography.Text>
            </Flex>
            <Typography.Text type="secondary">
              {`职责：${roleDutyLabel(detail.type)}`}
            </Typography.Text>
            <Flex gap={4} vertical>
              <Typography.Text>系统 Prompt</Typography.Text>
              <Input.TextArea
                aria-label="系统 Prompt"
                autoSize={{ maxRows: 18, minRows: 8 }}
                disabled={loading || saving}
                placeholder="请输入该角色的系统提示词"
                spellCheck={false}
                value={prompt}
                onChange={(event) => {
                  markDirty();
                  setPrompt(event.target.value);
                }}
              />
            </Flex>
            <Flex gap={4} vertical>
              <Typography.Text>模型</Typography.Text>
              <Input
                aria-label="模型"
                disabled={loading || saving}
                placeholder="例如 provider/model-id"
                value={model}
                onChange={(event) => {
                  markDirty();
                  setModel(event.target.value);
                }}
              />
              <Typography.Text type="secondary">
                模型标识由本机 Pi 配置决定，必须是实际可用的模型。
              </Typography.Text>
            </Flex>
            <Flex align="center" gap="small">
              <Typography.Text>启用</Typography.Text>
              <Switch
                aria-label="启用角色"
                checked={enabled}
                disabled={loading || saving}
                onChange={(next) => {
                  markDirty();
                  setEnabled(next);
                }}
              />
              <Typography.Text type="secondary">
                禁用后该角色不参与新任务，已运行的会话不受影响。
              </Typography.Text>
            </Flex>
            <Flex gap={4} vertical>
              <Flex align="center" gap="small">
                <Typography.Text>工具策略</Typography.Text>
                <Tag>暂不可编辑</Tag>
              </Flex>
              <Typography.Text type="secondary">
                {toolsPolicySummary(detail.tools_policy)}
              </Typography.Text>
              <Typography.Text type="secondary">
                工具策略的实际执行层限制尚未实现，因此本阶段不提供编辑入口。
              </Typography.Text>
            </Flex>
          </>
        )}
        {loading && <Alert showIcon title="正在读取角色配置…" type="info" />}
      </Flex>
    </Modal>
  );
}
