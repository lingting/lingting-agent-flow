/**
 * 设置中心「角色管理」面板。
 *
 * 职责：
 *   - 展示四个固定角色及其名称、职责、模型与启用状态；
 *   - 通过对话框编辑系统 Prompt、模型与启用状态，保存后重新读取服务端结果；
 *   - 不提供新增与删除入口：角色集合由服务端固定，工具策略只展示不可编辑。
 */
import { Alert, Flex, Tag, Typography } from "antd";
import { useCallback, useMemo, useState } from "react";

import type { Role } from "@/lib/api";
import { Button, type ExTableColumn, ExTable } from "lingting-react-ui";

import { RoleEditModal } from "@/pages/settings/RoleEditModal";
import { fixedRoleRows, roleDutyLabel } from "@/pages/settings/settings-roles";
import { roleTypeLabel } from "@/pages/tasks/selectors";
import { useRoles } from "@/pages/tasks/useRoles";

/** 角色行：列表展示用，附带 ExTable 需要的稳定行标识 */
type RoleRow = Role & Record<string, unknown>;

/** 固定角色数量：服务端初始化时保证存在这四个角色 */
const FIXED_ROLE_TOTAL = 4;

export function RoleManagementPanel() {
  const roles = useRoles();
  const [editing, setEditing] = useState<Role>();

  const rows = useMemo<RoleRow[]>(
    () =>
      fixedRoleRows(roles.roles).map((role) => ({
        ...role,
        rowKey: role.id,
      })),
    [roles.roles],
  );

  const closeEditor = useCallback(() => setEditing(undefined), []);

  /** 保存成功后重新读取列表：界面只展示服务端返回的真实配置 */
  const handleSaved = useCallback(() => {
    roles.reload();
  }, [roles.reload]);

  const columns = useMemo<ExTableColumn<RoleRow>[]>(
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
        title: "角色",
      },
      {
        dataIndex: "type",
        render: (_dom, record) => (
          <Flex gap={2} vertical>
            <Typography.Text>{roleTypeLabel(record.type)}</Typography.Text>
            <Typography.Text type="secondary">{roleDutyLabel(record.type)}</Typography.Text>
          </Flex>
        ),
        search: false,
        title: "职责",
      },
      {
        dataIndex: "model",
        render: (_dom, record) => record.model,
        search: false,
        title: "模型",
      },
      {
        dataIndex: "enabled",
        render: (_dom, record) =>
          record.enabled ? <Tag color="success">已启用</Tag> : <Tag color="error">已禁用</Tag>,
        search: false,
        title: "状态",
      },
      {
        dataIndex: "option",
        render: (_dom, record) => (
          <Button onClick={() => setEditing(record)} size="small" text="编辑" />
        ),
        search: false,
        title: "操作",
        valueType: "option",
      },
    ],
    [],
  );

  return (
    <Flex gap="middle" vertical style={{ width: "100%" }}>
      <Alert
        showIcon
        description={
          "角色集合固定为四个，不能新增或删除。可以修改系统 Prompt、模型与启用状态，" +
          "保存后立即对新的调用生效；工具策略本阶段只展示，暂不可编辑。"
        }
        title="固定四角色"
        type="info"
      />

      {roles.status === "error" && (
        <Alert
          action={<Button onClick={roles.reload} size="small" text="重试" />}
          description={roles.error}
          showIcon
          title="加载角色失败"
          type="error"
        />
      )}

      <Flex align="center" gap="small" wrap>
        <Button onClick={roles.reload} text="刷新" />
        <Typography.Text type="secondary">
          禁用角色仍显示在列表中，但不能用于新任务。
        </Typography.Text>
      </Flex>

      {roles.status === "loading" && rows.length === 0 && (
        <Alert showIcon title="正在查询角色…" type="info" />
      )}
      {roles.status === "ready" && rows.length < FIXED_ROLE_TOTAL && (
        <Alert
          showIcon
          description="服务端没有返回完整的固定角色集合，缺失的角色不会在本地补造。请检查服务启动日志。"
          title={`只查询到 ${rows.length} 个固定角色`}
          type="warning"
        />
      )}

      <ExTable<RoleRow>
        columns={columns}
        dataSource={rows}
        locale={{ emptyText: "没有角色记录" }}
        pagination={false}
        rowKey="rowKey"
        search={false}
      />

      <RoleEditModal
        // 以角色标识为 key：切换角色时重建对话框，旧请求结果不会写回新界面。
        key={editing?.id ?? "role-editor"}
        open={Boolean(editing)}
        role={editing}
        onCancel={closeEditor}
        onSaved={handleSaved}
      />
    </Flex>
  );
}
