/**
 * 设置中心「环境管理」面板。
 *
 * 职责：
 *   - 让用户显式选择执行节点（环境属于具体节点，前端不猜默认值）；
 *   - 展示该节点上真实存在的环境与版本，可用性只来自服务端；
 *   - 提供初始化工作空间、编辑 Dockerfile 生成新版本、构建与查看构建日志的入口；
 *   - 所有写操作都要先满足"节点允许远程操作"这一服务端事实，前端禁用只是提示。
 */
import { Alert, Flex, Select, Space, Tag, Typography } from "antd";
import { useCallback, useMemo, useState } from "react";

import type { Environment, EnvironmentType } from "@/lib/api";
import { Button, type ExTableColumn, ExTable } from "lingting-react-ui";

import { useEnvironments } from "@/pages/tasks/useEnvironments";
import { EnvironmentDetailPanel } from "@/pages/settings/EnvironmentDetailPanel";
import { EnvironmentInitModal } from "@/pages/settings/EnvironmentInitModal";
import { useEnvironmentHosts } from "@/pages/settings/useEnvironmentHosts";
import {
  canManageEnvironments,
  environmentLabel,
  environmentTypeLabel,
  environmentVersionStats,
  environmentWriteBlockedReason,
  hostSelectOptions,
} from "@/pages/settings/settings-environments";

/** 环境行：列表展示用，附带从服务端版本记录派生的统计 */
type EnvironmentRow = Environment & Record<string, unknown>;

export function EnvironmentManagementPanel() {
  const hosts = useEnvironmentHosts();
  const [hostID, setHostID] = useState<string>();
  const [selected, setSelected] = useState<{ name: string; type: EnvironmentType }>();
  const [initOpen, setInitOpen] = useState(false);

  const environments = useEnvironments(hostID);
  const selectedHost = hosts.hosts.find((host) => host.id === hostID);
  const blockedReason = environmentWriteBlockedReason(selectedHost);

  const reloadAll = useCallback(() => {
    environments.reload();
    hosts.reload();
  }, [environments.reload, hosts.reload]);

  const current = useMemo(
    () =>
      selected
        ? environments.environments.find(
            (environment) =>
              environment.type === selected.type && environment.name === selected.name,
          )
        : undefined,
    [environments.environments, selected],
  );

  /**
   * 列表行：ExTable 的 rowKey 只接受字段名，因此额外补一个稳定的行标识。
   * 环境标识由服务端返回的 type/name 组成，不需要也不允许前端另造标识。
   */
  const rows = useMemo<EnvironmentRow[]>(
    () =>
      environments.environments.map((environment) => ({
        ...environment,
        rowKey: `${environment.type}/${environment.name}`,
      })),
    [environments.environments],
  );

  const columns = useMemo<ExTableColumn<EnvironmentRow>[]>(
    () => [
      {
        dataIndex: "type",
        render: (_dom, record) => environmentTypeLabel(record.type),
        search: false,
        title: "类型",
      },
      {
        dataIndex: "name",
        render: (_dom, record) => (
          <Typography.Text strong>{environmentLabel(record)}</Typography.Text>
        ),
        search: false,
        title: "环境",
      },
      {
        dataIndex: "current_version",
        render: (_dom, record) =>
          record.current_version ? (
            <Tag color="success">{record.current_version}</Tag>
          ) : (
            <Typography.Text type="secondary">无可用版本</Typography.Text>
          ),
        search: false,
        title: "当前可用版本",
      },
      {
        dataIndex: "latest_version",
        render: (_dom, record) => record.latest_version,
        search: false,
        title: "最新创建版本",
      },
      {
        dataIndex: "versions",
        render: (_dom, record) => {
          const stats = environmentVersionStats(record);
          return `${stats.total} 个（可用 ${stats.usable}，失败/未知 ${stats.problematic}）`;
        },
        search: false,
        title: "历史版本",
      },
      {
        dataIndex: "option",
        render: (_dom, record) => (
          <Space>
            <Button
              disabled={selected?.type === record.type && selected?.name === record.name}
              onClick={() => setSelected({ name: record.name, type: record.type })}
              size="small"
              text="管理"
            />
          </Space>
        ),
        search: false,
        title: "操作",
        valueType: "option",
      },
    ],
    [selected?.name, selected?.type],
  );

  return (
    <Flex gap="middle" vertical style={{ width: "100%" }}>
      <Alert
        showIcon
        description={
          "环境属于具体执行节点。请先选择节点：环境定义、版本与构建状态都来自该节点上的真实记录，" +
          "构建由服务端在后台执行，可用性只能由服务端判定。"
        }
        title="环境按执行节点隔离"
        type="info"
      />

      {hosts.status === "error" && (
        <Alert
          action={<Button onClick={hosts.reload} size="small" text="重试" />}
          description={hosts.error}
          showIcon
          title="加载执行节点失败"
          type="error"
        />
      )}

      <Flex align="center" gap="small" wrap>
        <Typography.Text>执行节点</Typography.Text>
        <Select
          aria-label="执行节点"
          loading={hosts.status === "loading"}
          options={hostSelectOptions(hosts.hosts)}
          placeholder={hosts.status === "loading" ? "正在加载节点…" : "请选择执行节点"}
          style={{ minWidth: 360 }}
          value={hostID}
          onChange={(value) => {
            // 切换节点后清空与旧节点关联的选择，避免在新节点上展示旧节点的环境。
            setHostID(value);
            setSelected(undefined);
          }}
        />
        <Button disabled={!hostID} onClick={reloadAll} text="刷新" />
        <Button
          disabled={!hostID || !canManageEnvironments(selectedHost)}
          onClick={() => setInitOpen(true)}
          text="初始化环境工作空间"
          type="primary"
        />
      </Flex>

      {!hostID && (
        <Alert
          showIcon
          description="选择执行节点后会查询该节点上真实存在的环境与版本。"
          type="info"
        />
      )}
      {hostID && blockedReason && (
        <Alert
          showIcon
          description={`${blockedReason} 你仍然可以查看已存在的环境与版本，但不能发起写操作。`}
          title="该节点当前不能执行远程环境操作"
          type="warning"
        />
      )}
      {hostID && environments.status === "loading" && environments.environments.length === 0 && (
        <Alert showIcon title="正在查询该节点上的环境…" type="info" />
      )}
      {hostID && environments.status === "error" && (
        <Alert
          action={<Button onClick={environments.reload} size="small" text="重试" />}
          description={environments.error}
          showIcon
          title="环境查询失败"
          type="error"
        />
      )}
      {hostID && environments.status === "ready" && environments.environments.length === 0 && (
        <Alert
          showIcon
          description="该节点上还没有任何环境版本。可以先初始化环境工作空间，再编辑 Dockerfile 生成新版本并构建。"
          title="没有环境记录"
          type="warning"
        />
      )}

      {hostID && environments.environments.length > 0 && (
        <ExTable<EnvironmentRow>
          columns={columns}
          dataSource={rows}
          locale={{ emptyText: "该节点还没有环境记录" }}
          pagination={false}
          rowKey="rowKey"
          search={false}
        />
      )}

      {selectedHost && current && (
        <EnvironmentDetailPanel
          // 以节点与环境为 key：切换目标时重建面板，旧请求与轮询结果不会写回新界面。
          key={`${selectedHost.id}|${current.type}|${current.name}`}
          environment={current}
          host={selectedHost}
          onChanged={reloadAll}
        />
      )}

      <EnvironmentInitModal
        host={selectedHost}
        open={initOpen}
        onCancel={() => setInitOpen(false)}
        onFinished={() => reloadAll()}
      />
    </Flex>
  );
}
