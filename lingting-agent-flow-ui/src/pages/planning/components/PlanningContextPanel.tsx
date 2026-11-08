/**
 * 需求工作台的上下文区：项目、执行节点与会话。
 *
 * 三条硬约束：
 * - 必须先选择项目：未选择项目时不提供新建会话与提交需求的入口；
 * - 执行节点不自动选择：有项目默认节点时优先使用，没有时由用户显式选择，已有会话绑定
 *   的节点不允许在页面上改指；
 * - 会话列表与「没有会话」是两种事实：查询失败必须显示错误，不能伪装成空列表。
 */
import { PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import { Alert, Flex, Select, Space, Tag, Typography } from "antd";
import { Button, LinkButton } from "lingting-react-ui";

import type { Host, Project } from "@/lib/api";
import type { PlanningSessionListState } from "@/pages/planning/hooks/usePlanningSessions";
import {
  formatPlanningTime,
  planningStatusColor,
  planningStatusLabel,
  type PlanningHostReadiness,
} from "@/pages/planning/planning";
import { describeProjectDefaultHost, projectSelectOptions } from "@/pages/projects/projects";
import type { ProjectQueryState } from "@/pages/projects/useProjects";
import type { HostQueryState } from "@/pages/tasks/useHosts";

/** 执行节点在页面上的展示文本；名称只能来自服务端列表 */
function describeHost(hostID: string, hosts: Host[], loaded: boolean): string {
  const host = hosts.find((item) => item.id === hostID);
  if (host) return `${host.name}（${host.id}）`;
  return describeProjectDefaultHost({ hostID, hosts, loaded });
}

/** 会话列表区 */
function SessionList(input: {
  state: PlanningSessionListState;
  sessionID?: string;
  onSelectSession: (sessionID?: string) => void;
  onReload: () => void;
}) {
  const { state } = input;
  if (state.status === "loading") {
    return <Typography.Text type="secondary">正在查询该项目的规划会话…</Typography.Text>;
  }
  if (state.status === "error") {
    return (
      <Alert
        action={<Button onClick={input.onReload} size="small" text="重试" />}
        description={state.error}
        showIcon
        title="查询规划会话失败"
        type="error"
      />
    );
  }
  if (state.status === "ready" && state.sessions.length === 0) {
    return (
      <Typography.Text type="secondary">
        该项目还没有规划会话：点击「新建会话」后会得到服务端生成的会话标识。
      </Typography.Text>
    );
  }
  return (
    <Flex data-testid="planning-session-list" gap={4} vertical>
      {state.sessions.map((session) => {
        const active = session.id === input.sessionID;
        return (
          <Flex align="center" gap={8} key={session.id} wrap>
            <Button
              onClick={() => input.onSelectSession(session.id)}
              size="small"
              text={active ? "当前会话" : "选择会话"}
              type={active ? "primary" : "default"}
            />
            <Typography.Text code>{session.id}</Typography.Text>
            <Tag color={planningStatusColor(session.status)}>
              {planningStatusLabel(session.status)}
            </Tag>
            <Typography.Text type="secondary">
              最后更新 {formatPlanningTime(session.updated_at)}
            </Typography.Text>
          </Flex>
        );
      })}
    </Flex>
  );
}

/** 执行节点区：已绑定、项目默认与需要显式选择三种现场分别展示 */
function PlanningHostSection(props: {
  project: Project;
  hosts: HostQueryState & { reload: () => void };
  sessionHostID?: string;
  selectedHostID?: string;
  onSelectHost: (hostID?: string) => void;
  hostReadiness: PlanningHostReadiness;
}) {
  const hostsLoaded = props.hosts.status === "ready";
  const defaultHostID = props.project.default_host_id?.trim();
  const binding = props.sessionHostID ? (
    <Space align="center" size={8} wrap>
      <Typography.Text>执行节点</Typography.Text>
      <Typography.Text code>{props.sessionHostID}</Typography.Text>
      <Typography.Text type="secondary">
        会话已绑定该执行节点，页面不允许改指其他节点。
      </Typography.Text>
    </Space>
  ) : defaultHostID ? (
    <Space align="center" size={8} wrap>
      <Typography.Text>执行节点</Typography.Text>
      <Typography.Text code>{defaultHostID}</Typography.Text>
      <Typography.Text type="secondary">
        {`取自项目默认执行节点（${describeHost(defaultHostID, props.hosts.hosts, hostsLoaded)}）；` +
          "它只是配置偏好，不代表节点已通过预检，分析失败会如实展示。"}
      </Typography.Text>
    </Space>
  ) : (
    <Space align="center" size={8} wrap>
      <Typography.Text>执行节点</Typography.Text>
      <Select
        allowClear
        aria-label="选择执行节点"
        loading={props.hosts.status === "loading"}
        onChange={(value?: string) => props.onSelectHost(value)}
        options={props.hosts.hosts.map((host) => ({
          label: `${host.name}（${host.id}）`,
          value: host.id,
        }))}
        placeholder="请显式选择执行节点"
        style={{ minWidth: 280 }}
        value={props.selectedHostID}
      />
      <Typography.Text type="secondary">
        项目没有默认执行节点，必须由你选择；页面不会自动选中列表中的第一个节点。
      </Typography.Text>
    </Space>
  );
  return (
    <Flex gap="middle" vertical>
      {binding}
      {props.hostReadiness.blockedReason && (
        <Alert
          description={props.hostReadiness.blockedReason}
          showIcon
          title="暂不能发起分析"
          type="warning"
        />
      )}
      {props.hosts.status === "error" && (
        <Alert
          action={<Button onClick={props.hosts.reload} size="small" text="重试" />}
          description={props.hosts.error}
          showIcon
          title="查询执行节点失败"
          type="error"
        />
      )}
    </Flex>
  );
}

/** 上下文区面板 */
export function PlanningContextPanel(props: {
  projects: ProjectQueryState & { reload: () => void };
  hosts: HostQueryState & { reload: () => void };
  projectID?: string;
  project?: Project;
  onSelectProject: (projectID?: string) => void;
  sessions: PlanningSessionListState;
  sessionID?: string;
  onSelectSession: (sessionID?: string) => void;
  onReloadSessions: () => void;
  creating: boolean;
  createError?: string;
  onCreateSession: () => void;
  sessionHostID?: string;
  selectedHostID?: string;
  onSelectHost: (hostID?: string) => void;
  hostReadiness: PlanningHostReadiness;
}) {
  const projectsLoaded = props.projects.status === "ready";
  return (
    <Flex gap="middle" vertical>
      <Typography.Text strong>规划上下文</Typography.Text>

      <Space align="center" size={8} wrap>
        <Typography.Text>项目</Typography.Text>
        <Select
          aria-label="选择项目"
          loading={props.projects.status === "loading"}
          onChange={(value?: string) => props.onSelectProject(value)}
          options={projectSelectOptions(props.projects.projects)}
          placeholder="请选择项目"
          style={{ minWidth: 280 }}
          value={props.projectID}
        />
        <Typography.Text type="secondary">
          规划会话、消息与方案版本都归属于一个真实登记的项目。
        </Typography.Text>
      </Space>

      {props.projects.status === "error" && (
        <Alert
          action={<Button onClick={props.projects.reload} size="small" text="重试" />}
          description={props.projects.error}
          showIcon
          title="加载项目失败"
          type="error"
        />
      )}
      {projectsLoaded && props.projects.projects.length === 0 && (
        <Alert
          description="服务端还没有登记任何项目。请先在「项目」页新增项目，再回到需求工作台。"
          showIcon
          title="没有可用项目"
          type="warning"
        />
      )}

      {props.project && (
        <PlanningHostSection
          hostReadiness={props.hostReadiness}
          hosts={props.hosts}
          onSelectHost={props.onSelectHost}
          project={props.project}
          selectedHostID={props.selectedHostID}
          sessionHostID={props.sessionHostID}
        />
      )}

      <Space align="center" size={8} wrap>
        <Typography.Text strong>规划会话</Typography.Text>
        <Button
          disabled={!props.projectID}
          icon={<PlusOutlined />}
          loading={props.creating}
          onClick={props.onCreateSession}
          size="small"
          text="新建会话"
          type="primary"
        />
        <LinkButton
          disabled={!props.projectID}
          icon={<ReloadOutlined />}
          onClick={props.onReloadSessions}
          text="刷新会话列表"
        />
        {!props.projectID && (
          <Typography.Text type="secondary">先选择项目，才能创建或打开规划会话。</Typography.Text>
        )}
      </Space>

      {props.createError && (
        <Alert
          closable
          description={props.createError}
          showIcon
          title="新建规划会话失败"
          type="error"
        />
      )}

      {props.projectID && (
        <SessionList
          onReload={props.onReloadSessions}
          onSelectSession={props.onSelectSession}
          sessionID={props.sessionID}
          state={props.sessions}
        />
      )}
    </Flex>
  );
}
