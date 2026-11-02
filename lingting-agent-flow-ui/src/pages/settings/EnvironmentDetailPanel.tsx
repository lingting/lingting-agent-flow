/**
 * 环境详情与版本操作面板。
 *
 * 数据与职责：
 *   - 版本列表来自 `GET /api/v1/environments`，可用性只认服务端返回的 `usable`；
 *   - Dockerfile 与构建日志按需从服务端读取（远程读取），读取失败不会被显示成空内容；
 *   - 构建通过 `POST …/build` 受理，随后轮询 `…/build-status` 读取真实结论：失败与
 *     "结果未知"分别展示服务端给出的原因，绝不显示成构建成功；
 *   - 同一环境在途构建期间禁用重复提交；服务端返回 409 时如实提示；
 *   - 操作结束后由调用方重新读取环境列表，界面不自行推算新的构建状态。
 */
import { Alert, Descriptions, Flex, Space, Tag, Typography } from "antd";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  fetchEnvironmentBuildLog,
  fetchEnvironmentBuildStatus,
  fetchEnvironmentDockerfile,
  startEnvironmentBuild,
  type Environment,
  type EnvironmentVersion,
  type Host,
} from "@/lib/api";
import { Button, type ExTableColumn, ExTable } from "lingting-react-ui";

import { EnvironmentVersionModal } from "@/pages/settings/EnvironmentVersionModal";
import {
  buildStatusMeta,
  buildStillRunning,
  canSubmitBuild,
  describeEnvironmentError,
  environmentTypeLabel,
  environmentWriteBlockedReason,
  isBuildInProgressError,
  resolveBuildStatusOutcome,
  sortVersionsNewestFirst,
  type BuildStatusOutcome,
} from "@/pages/settings/settings-environments";

export type EnvironmentDetailPanelProps = {
  /** 目标环境 */
  environment: Environment;
  /** 目标执行节点 */
  host: Host;
  /** 请求重新读取真实持久化数据 */
  onChanged: () => void;
};

/** 远程内容读取状态 */
type RemoteContentState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "ready"; content: string; remotePath: string }
  | { status: "error"; error: string };

/** 构建轮询状态 */
type BuildWatch = {
  versionNumber: number;
  outcome: BuildStatusOutcome | { kind: "error"; message: string };
};

/** 轮询间隔与次数上限：结论由服务端给出，前端只负责读取 */
const BUILD_POLL_INTERVAL_MS = 2000;
const BUILD_POLL_LIMIT = 150;

/** 等待若干毫秒 */
function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, milliseconds);
  });
}

/** 时间展示：使用本地时区，缺省显示占位符 */
function formatTime(value?: string): string {
  if (!value) return "—";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "—";
  return parsed.toLocaleString();
}

export function EnvironmentDetailPanel({
  environment,
  host,
  onChanged,
}: EnvironmentDetailPanelProps) {
  const versions = useMemo(
    () => sortVersionsNewestFirst(environment.versions),
    [environment.versions],
  );
  // 默认选中服务端给出的当前可用版本；没有可用版本时退回最新创建版本，仅作为展示初值。
  const initialNumber = useMemo(() => {
    const current = versions.find((version) => version.version === environment.current_version);
    return (current ?? versions[0])?.number;
  }, [environment.current_version, versions]);

  const [selectedNumber, setSelectedNumber] = useState<number | undefined>(initialNumber);
  const [dockerfile, setDockerfile] = useState<RemoteContentState>({ status: "idle" });
  const [buildLog, setBuildLog] = useState<RemoteContentState>({ status: "idle" });
  const [buildWatch, setBuildWatch] = useState<BuildWatch>();
  const [pendingBuild, setPendingBuild] = useState<number>();
  const [actionError, setActionError] = useState<string>();
  const [editing, setEditing] = useState<EnvironmentVersion>();
  /** 轮询令牌：切换环境、卸载或发起新构建时递增，旧轮询据此停止写入状态 */
  const pollRef = useRef(0);

  const selected = versions.find((version) => version.number === selectedNumber);
  const blockedReason = environmentWriteBlockedReason(host);

  useEffect(() => {
    return () => {
      // 组件卸载（例如切换执行节点或环境）时丢弃在途轮询，避免迟到响应写回界面。
      pollRef.current += 1;
    };
  }, []);

  const loadDockerfile = useCallback(
    async (version: EnvironmentVersion) => {
      setDockerfile({ status: "loading" });
      try {
        const { data } = await fetchEnvironmentDockerfile(
          host.id,
          environment.type,
          environment.name,
          version.number,
        );
        setDockerfile({ content: data.content, remotePath: data.remote_path, status: "ready" });
      } catch (cause) {
        setDockerfile({ error: describeEnvironmentError(cause), status: "error" });
      }
    },
    [environment.name, environment.type, host.id],
  );

  const loadBuildLog = useCallback(
    async (version: EnvironmentVersion) => {
      setBuildLog({ status: "loading" });
      try {
        const { data } = await fetchEnvironmentBuildLog(
          host.id,
          environment.type,
          environment.name,
          version.number,
        );
        setBuildLog({ content: data.content, remotePath: data.remote_path, status: "ready" });
      } catch (cause) {
        // 日志读取失败（含日志不存在）必须显示错误：不能渲染成空日志。
        setBuildLog({ error: describeEnvironmentError(cause), status: "error" });
      }
    },
    [environment.name, environment.type, host.id],
  );

  const watchBuild = useCallback(
    async (versionNumber: number) => {
      const token = pollRef.current + 1;
      pollRef.current = token;
      for (let attempt = 0; attempt < BUILD_POLL_LIMIT; attempt += 1) {
        await delay(BUILD_POLL_INTERVAL_MS);
        if (pollRef.current !== token) return;
        try {
          const { data } = await fetchEnvironmentBuildStatus(
            host.id,
            environment.type,
            environment.name,
            versionNumber,
          );
          if (pollRef.current !== token) return;
          const outcome = resolveBuildStatusOutcome(data, undefined);
          setBuildWatch({ outcome, versionNumber });
          if (!buildStillRunning(data.version.build_status)) {
            onChanged();
            return;
          }
        } catch (cause) {
          if (pollRef.current !== token) return;
          const outcome = resolveBuildStatusOutcome(undefined, cause);
          setBuildWatch({ outcome, versionNumber });
          // 失败、结果未知与其他错误都停止轮询：结论已由服务端给出或需要人工处理。
          if (outcome.kind !== "running") {
            onChanged();
            return;
          }
        }
      }
      if (pollRef.current !== token) return;
      setBuildWatch({
        outcome: {
          kind: "error",
          message: "构建仍在后台执行，服务端尚未给出结论；请稍后刷新查看真实状态。",
        },
        versionNumber,
      });
    },
    [environment.name, environment.type, host.id, onChanged],
  );

  const handleBuild = useCallback(
    async (version: EnvironmentVersion) => {
      setActionError(undefined);
      setPendingBuild(version.number);
      setBuildWatch(undefined);
      try {
        // 202 只表示"已受理"，构建在后台执行；这里不据此显示成功。
        await startEnvironmentBuild(environment.type, environment.name, {
          host_id: host.id,
          version: version.number,
        });
        setBuildWatch({
          outcome: { kind: "running", version },
          versionNumber: version.number,
        });
        void watchBuild(version.number);
      } catch (cause) {
        if (isBuildInProgressError(cause)) {
          setActionError("该环境已有构建在进行中，请等待其结束后重试。");
        } else {
          setActionError(describeEnvironmentError(cause));
        }
      } finally {
        setPendingBuild(undefined);
      }
    },
    [environment.name, environment.type, host.id, watchBuild],
  );

  const columns = useMemo<ExTableColumn<EnvironmentVersion & Record<string, unknown>>[]>(
    () => [
      {
        dataIndex: "version",
        render: (_dom, record) => (
          <Space>
            <Typography.Text strong>{record.version}</Typography.Text>
            {record.version === environment.current_version && <Tag color="success">当前可用</Tag>}
            {record.version === environment.latest_version && <Tag>最新创建</Tag>}
          </Space>
        ),
        search: false,
        title: "版本",
      },
      {
        dataIndex: "build_status",
        render: (_dom, record) => {
          const meta = buildStatusMeta(record.build_status);
          return <Tag color={meta.color}>{meta.label}</Tag>;
        },
        search: false,
        title: "构建状态",
      },
      {
        dataIndex: "usable",
        render: (_dom, record) => (record.usable ? "可用" : "不可用"),
        search: false,
        title: "可用于任务",
      },
      {
        dataIndex: "parent_version",
        render: (_dom, record) => record.parent_version || "—",
        search: false,
        title: "父基础版本",
      },
      {
        dataIndex: "created_at",
        render: (_dom, record) => formatTime(record.created_at),
        search: false,
        title: "创建时间",
      },
      {
        dataIndex: "built_at",
        render: (_dom, record) => formatTime(record.built_at),
        search: false,
        title: "构建完成时间",
      },
      {
        dataIndex: "error_summary",
        render: (_dom, record) => (
          <Typography.Text
            ellipsis
            style={{ maxWidth: 260 }}
            type={record.error_summary ? "danger" : "secondary"}
          >
            {record.error_summary || "—"}
          </Typography.Text>
        ),
        search: false,
        title: "失败/未知原因",
      },
      {
        dataIndex: "option",
        render: (_dom, record) => {
          const building =
            Boolean(pendingBuild === record.number) ||
            buildStatusMeta(record.build_status).inFlight ||
            (buildWatch?.versionNumber === record.number && buildWatch.outcome.kind === "running");
          return (
            <Space>
              <Button
                disabled={selectedNumber === record.number}
                onClick={() => {
                  setSelectedNumber(record.number);
                  setDockerfile({ status: "idle" });
                  setBuildLog({ status: "idle" });
                }}
                size="small"
                text="选中"
              />
              <Button
                disabled={Boolean(blockedReason) || !canSubmitBuild(record) || building}
                onClick={() => void handleBuild(record)}
                size="small"
                text={building ? "构建中…" : "构建"}
              />
              <Button
                // 历史版本永远不可改写：这里的编辑动作只生成新版本。
                disabled={Boolean(blockedReason)}
                onClick={() => setEditing(record)}
                size="small"
                text="编辑为新版本"
              />
            </Space>
          );
        },
        search: false,
        title: "操作",
        valueType: "option",
      },
    ],
    [
      blockedReason,
      buildWatch,
      environment.current_version,
      environment.latest_version,
      handleBuild,
      pendingBuild,
      selectedNumber,
    ],
  );

  return (
    <Flex gap="middle" vertical style={{ width: "100%" }}>
      {blockedReason && (
        <Alert showIcon description={blockedReason} title="当前节点不可写" type="warning" />
      )}
      {actionError && (
        <Alert
          closable
          description={actionError}
          showIcon
          title="操作失败"
          type="error"
          onClose={() => setActionError(undefined)}
        />
      )}

      <Descriptions
        column={4}
        items={[
          { key: "type", label: "环境类型", children: environmentTypeLabel(environment.type) },
          { key: "name", label: "环境名称", children: environment.name },
          {
            key: "current",
            label: "当前可用版本",
            children: environment.current_version ?? "无可用版本",
          },
          { key: "latest", label: "最新创建版本", children: environment.latest_version },
        ]}
        size="small"
      />

      {buildWatch && <BuildWatchNotice watch={buildWatch} />}

      <ExTable<EnvironmentVersion & Record<string, unknown>>
        columns={columns}
        locale={{ emptyText: "该环境还没有任何版本" }}
        pagination={false}
        rowKey="number"
        search={false}
        dataSource={versions as (EnvironmentVersion & Record<string, unknown>)[]}
      />

      <Flex align="center" gap="small" justify="space-between" wrap>
        <Typography.Text strong>
          {selected ? `版本 ${selected.version} 的远程内容` : "选择一个版本查看远程内容"}
        </Typography.Text>
        <Space>
          <Button
            disabled={!selected}
            onClick={() => selected && void loadDockerfile(selected)}
            text="查看 Dockerfile"
          />
          <Button
            disabled={!selected}
            onClick={() => selected && void loadBuildLog(selected)}
            text="查看构建日志"
          />
        </Space>
      </Flex>

      <RemoteContentPanel state={dockerfile} title="Dockerfile" />
      <RemoteContentPanel state={buildLog} title="构建日志" />

      <EnvironmentVersionModal
        environment={environment}
        host={host}
        open={Boolean(editing)}
        source={editing}
        onCancel={() => setEditing(undefined)}
        onCreated={() => {
          setEditing(undefined);
          onChanged();
        }}
      />
    </Flex>
  );
}

/** 构建轮询结论提示 */
function BuildWatchNotice({ watch }: { watch: BuildWatch }) {
  const { outcome } = watch;
  if (outcome.kind === "running") {
    return (
      <Alert
        showIcon
        description={`版本 ${outcome.version.version} 的构建已在后台执行；状态来自服务端轮询，不代表构建成功。`}
        title="构建进行中"
        type="info"
      />
    );
  }
  if (outcome.kind === "succeeded") {
    return (
      <Alert
        showIcon
        description={`版本 ${outcome.version.version} 已构建成功，镜像 ID ${outcome.version.image_id ?? "—"}。`}
        title="构建成功"
        type="success"
      />
    );
  }
  if (outcome.kind === "failed") {
    return (
      <Alert
        showIcon
        description={outcome.message}
        title={`版本 ${watch.versionNumber} 构建失败`}
        type="error"
      />
    );
  }
  if (outcome.kind === "uncertain") {
    return (
      <Alert
        showIcon
        description={outcome.message}
        title={`版本 ${watch.versionNumber} 构建结果无法确认`}
        type="warning"
      />
    );
  }
  return (
    <Alert
      showIcon
      description={outcome.message}
      title={`版本 ${watch.versionNumber} 构建状态未知`}
      type="warning"
    />
  );
}

/** 远程内容（Dockerfile / 构建日志）展示 */
function RemoteContentPanel({ state, title }: { state: RemoteContentState; title: string }) {
  if (state.status === "idle") return null;
  if (state.status === "loading") {
    return <Alert showIcon title={`正在读取${title}…`} type="info" />;
  }
  if (state.status === "error") {
    return <Alert showIcon description={state.error} title={`读取${title}失败`} type="error" />;
  }
  return (
    <Flex gap={4} vertical>
      <Flex align="center" gap="small" justify="space-between">
        <Typography.Text strong>{title}</Typography.Text>
        <Typography.Text type="secondary" ellipsis style={{ maxWidth: 480 }}>
          {state.remotePath}
        </Typography.Text>
      </Flex>
      <pre
        style={{
          background: "rgba(0,0,0,0.03)",
          borderRadius: 6,
          margin: 0,
          maxHeight: 320,
          overflow: "auto",
          padding: 12,
          whiteSpace: "pre-wrap",
        }}
      >
        {state.content}
      </pre>
    </Flex>
  );
}
