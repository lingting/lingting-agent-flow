/**
 * 设置中心「环境管理」面板。
 *
 * 职责：
 *   - 默认选中第一个执行节点，允许切换：环境属于具体节点，查询只针对所选节点；
 *   - 展示该节点上真实存在的环境与版本，可用性只来自服务端；
 *   - 提供初始化工作空间、编辑 Dockerfile 生成新版本、构建与查看构建日志的入口；
 *   - 所有写操作都要先满足"节点允许远程操作"这一服务端事实，前端禁用只是提示。
 */
import { Flex, Select, Space, Tag, Typography } from "antd";
import { useCallback, useEffect, useMemo, useState } from "react";

import type { Environment, EnvironmentType } from "@/lib/api";
import { Button, type ExTableColumn, ExTable } from "lingting-react-ui";

import { useEnvironments } from "@/pages/tasks/useEnvironments";
import { DisabledActionHint } from "@/pages/tasks/DisabledActionHint";
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

  /** 默认选中第一个执行节点：节点列表就绪后立即查询该节点上的环境，用户仍可切换 */
  const firstHostID = hosts.hosts[0]?.id;
  useEffect(() => {
    if (hostID === undefined && firstHostID !== undefined) setHostID(firstHostID);
  }, [firstHostID, hostID]);

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

  /** 表格空态：查询失败与空列表是两种不同状态，不能都显示成"没有环境记录" */
  let emptyText = "该节点还没有环境记录";
  if (environments.status === "loading") {
    emptyText = "正在查询该节点上的环境…";
  } else if (environments.status === "error") {
    emptyText = "环境查询失败";
  }

  /**
   * 执行节点工具行尾部的单条提示。
   *
   * 一次只提示一个问题：节点加载失败时后续状态都不可信，优先级最高；其次是节点不可写，
   * 最后是环境查询失败。
   */
  let notice: { text: string; type: "danger" | "warning" } | undefined;
  if (hosts.status === "error") {
    notice = { text: `加载执行节点失败：${hosts.error}`, type: "danger" };
  } else if (blockedReason) {
    notice = { text: `该节点当前不能执行远程环境操作：${blockedReason}`, type: "warning" };
  } else if (environments.status === "error") {
    notice = { text: `环境查询失败：${environments.error}`, type: "danger" };
  }

  /** 初始化入口不可用的原因：按钮禁用时必须能说明为什么 */
  let initBlockedReason = "";
  if (!hostID) {
    initBlockedReason = "尚未选中执行节点";
  } else if (!canManageEnvironments(selectedHost)) {
    initBlockedReason = blockedReason || "该节点当前不允许远程环境操作";
  }

  return (
    <Flex gap="middle" vertical style={{ width: "100%" }}>
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
        <Button onClick={reloadAll} text="刷新" />
        <DisabledActionHint label="初始化环境工作空间" reason={initBlockedReason}>
          <Button
            disabled={!hostID || !canManageEnvironments(selectedHost)}
            onClick={() => setInitOpen(true)}
            text="初始化环境工作空间"
            type="primary"
          />
        </DisabledActionHint>
        {notice && <Typography.Text type={notice.type}>{notice.text}</Typography.Text>}
      </Flex>

      {hostID && (
        <ExTable<EnvironmentRow>
          columns={columns}
          dataSource={rows}
          locale={{ emptyText }}
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
