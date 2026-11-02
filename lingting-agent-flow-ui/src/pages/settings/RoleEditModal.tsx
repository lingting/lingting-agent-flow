/**
 * 编辑固定角色的可编辑配置。
 *
 * 可提交的字段是 `system_prompt`、`model`、`enabled` 与 `tools_policy`：角色标识、名称、
 * 类型与 Runner 由固定角色定义决定。工具策略的能力上限、可选工具与实际生效工具全部来自
 * 服务端返回值，界面只按其展示，不自行判定权限。打开时读取完整配置，保存后使用服务端
 * 返回的真实保存结果刷新展示，不根据本地入参推测保存结果。
 */
import { Alert, Checkbox, Flex, Input, Modal, Switch, Tag, Typography } from "antd";
import { useEffect, useRef, useState } from "react";

import { fetchRole, type Role, type RoleDetail, type ToolPermission, updateRole } from "@/lib/api";
import {
  clampToCeiling,
  describeRoleError,
  permissionLabel,
  permissionsSummary,
  roleDutyLabel,
  toolLabel,
  toolsSummary,
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
  const [permissions, setPermissions] = useState<ToolPermission[]>([]);
  const [allowTools, setAllowTools] = useState<string[]>([]);
  const [denyTools, setDenyTools] = useState<string[]>([]);
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
    setPermissions([]);
    setAllowTools([]);
    setDenyTools([]);
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
        // 能力只保留系统上限内的取值：超出上限的旧配置不会生效，保存时按上限收敛。
        setPermissions(clampToCeiling(data.tools_policy.permissions, data.capability_ceiling));
        setAllowTools(data.tools_policy.allow_tools ?? []);
        setDenyTools(data.tools_policy.deny_tools ?? []);
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
    setPermissions([]);
    setAllowTools([]);
    setDenyTools([]);
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
        tools_policy: {
          allow_tools: allowTools,
          deny_tools: denyTools,
          permissions,
        },
      });
      setDetail(data);
      setPrompt(data.system_prompt);
      setModel(data.model);
      setEnabled(data.enabled);
      setPermissions(clampToCeiling(data.tools_policy.permissions, data.capability_ceiling));
      setAllowTools(data.tools_policy.allow_tools ?? []);
      setDenyTools(data.tools_policy.deny_tools ?? []);
      setNotice("角色配置已保存");
      onSaved(data);
    } catch (cause) {
      setError(`保存失败：${describeRoleError(cause)}`);
    } finally {
      setSaving(false);
    }
  };

  /** 已保存但超出系统上限的能力：不会生效，保存后按上限收敛 */
  const beyondCeiling = detail
    ? detail.tools_policy.permissions.filter(
        (permission) => !detail.capability_ceiling.includes(permission),
      )
    : [];
  /** 只能在系统上限内选择工具，可选列表由服务端给出 */
  const toolOptions = (detail?.available_tools ?? []).map((tool) => ({
    label: toolLabel(tool),
    value: tool,
  }));

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
            <Flex gap="small" vertical>
              <Flex align="center" gap="small" wrap>
                <Typography.Text>工具策略</Typography.Text>
                <Tag>{`系统能力上限：${permissionsSummary(detail.capability_ceiling)}`}</Tag>
              </Flex>
              <Typography.Text type="secondary">
                能力上限由角色身份固定，不能通过配置突破；AgentFlow 托管的 Pi 调用不会加载 MCP
                工具与扩展。
              </Typography.Text>
              {beyondCeiling.length > 0 && (
                <Alert
                  showIcon
                  description={`${permissionsSummary(beyondCeiling)} 不会生效，保存后将按系统能力上限收敛。`}
                  title="已保存的能力超出系统上限"
                  type="warning"
                />
              )}
              <Flex gap={4} vertical>
                <Typography.Text>能力</Typography.Text>
                <Checkbox.Group
                  disabled={loading || saving}
                  options={detail.capability_ceiling.map((permission) => ({
                    label: permissionLabel(permission),
                    value: permission,
                  }))}
                  value={permissions}
                  onChange={(checked) => {
                    markDirty();
                    setPermissions(checked);
                  }}
                />
              </Flex>
              <Flex gap={4} vertical>
                <Typography.Text>允许工具</Typography.Text>
                <Checkbox.Group
                  disabled={loading || saving}
                  options={toolOptions}
                  value={allowTools}
                  onChange={(checked) => {
                    markDirty();
                    setAllowTools(checked);
                  }}
                />
                <Typography.Text type="secondary">
                  留空表示不按名称限制已授权的工具；只能选择系统能力上限内的内置工具。
                </Typography.Text>
              </Flex>
              <Flex gap={4} vertical>
                <Typography.Text>拒绝工具</Typography.Text>
                <Checkbox.Group
                  disabled={loading || saving}
                  options={toolOptions}
                  value={denyTools}
                  onChange={(checked) => {
                    markDirty();
                    setDenyTools(checked);
                  }}
                />
                <Typography.Text type="secondary">
                  拒绝优先于允许；执行命令对应 bash，它可以写文件，不属于只读工具。
                </Typography.Text>
              </Flex>
              <Typography.Text type="secondary">
                {`已保存配置实际生效的工具：${toolsSummary(detail.effective_tools)}`}
              </Typography.Text>
            </Flex>
          </>
        )}
        {loading && <Alert showIcon title="正在读取角色配置…" type="info" />}
      </Flex>
    </Modal>
  );
}
