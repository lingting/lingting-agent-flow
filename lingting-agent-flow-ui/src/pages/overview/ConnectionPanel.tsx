import { ReloadOutlined } from "@ant-design/icons";
import { Alert, Button, Card, Descriptions, Flex, Tag, Typography } from "antd";

import { describeConnectionError } from "@/components/api-connection-status";
import { PagePlaceholder } from "@/components/page-placeholder";
import { useApiConnectionContext } from "@/lib/api/ApiConnectionContext";
import type { HealthResponse } from "@/lib/api";

/** 检查项名称到中文说明的映射，未知名称原样展示，不伪造含义 */
const CHECK_LABELS: Record<string, string> = {
  database: "SQLite 数据库",
  http_api: "HTTP API",
};

/** 单项检查结果标签 */
function CheckTag({ message, status }: { message?: string; status: string }) {
  return status === "pass" ? (
    <Tag color="success">正常</Tag>
  ) : (
    <Tag color="error">{message ?? "不可用"}</Tag>
  );
}

/** 从错误响应中取出健康检查详情；不是健康检查响应时返回 undefined */
function healthFromError(body: unknown): HealthResponse | undefined {
  if (typeof body !== "object" || body === null) return undefined;
  const candidate = body as Partial<HealthResponse>;
  return candidate.status === "ok" || candidate.status === "degraded"
    ? (candidate as HealthResponse)
    : undefined;
}

/** 逐项检查结果；没有可展示的详情时不渲染 */
function HealthDetails({ health }: { health: HealthResponse }) {
  return (
    <Descriptions column={1} size="small">
      <Descriptions.Item label="总体状态">
        <Tag color={health.status === "ok" ? "success" : "warning"}>
          {health.status === "ok" ? "正常" : "部分不可用"}
        </Tag>
      </Descriptions.Item>
      {health.checks.map((item) => (
        <Descriptions.Item key={item.name} label={CHECK_LABELS[item.name] ?? item.name}>
          <CheckTag message={item.message} status={item.status} />
        </Descriptions.Item>
      ))}
      <Descriptions.Item label="检查时间">
        <Typography.Text type="secondary">
          {new Date(health.checked_at).toLocaleString()}
        </Typography.Text>
      </Descriptions.Item>
    </Descriptions>
  );
}

/**
 * 连接状态面板：展示真实的健康检查结果。
 *
 * 它只反映 AgentFlow HTTP API 与本地数据库的可用性，不代表 SSH、Docker 或 Pi Agent 已就绪。
 */
export function ConnectionPanel() {
  const { check, state } = useApiConnectionContext();
  const { error, health, status } = state;
  // 后端返回 503 时同样带有健康检查详情，用于说明是哪一项不可用。
  const failedHealth = status === "disconnected" ? healthFromError(error?.body) : undefined;

  return (
    <Flex gap="middle" vertical style={{ width: "100%" }}>
      <Card
        extra={
          <Button icon={<ReloadOutlined />} size="small" onClick={() => void check()}>
            重新检查
          </Button>
        }
        size="small"
        title="后端连接"
      >
        {status === "checking" && (
          <Typography.Text type="secondary">正在检查 AgentFlow HTTP API…</Typography.Text>
        )}
        {status === "disconnected" && (
          <Flex gap="small" vertical>
            <Alert
              description={describeConnectionError(error)}
              showIcon
              title="无法连接 AgentFlow 服务"
              type="error"
            />
            {failedHealth && <HealthDetails health={failedHealth} />}
          </Flex>
        )}
        {status === "connected" && health && <HealthDetails health={health} />}
      </Card>
      <Flex gap={4} vertical>
        <PagePlaceholder description="AgentFlow 的总览入口，用于汇总需求、任务与执行状态。" />
        <Typography.Text type="secondary">
          连接指示器只表示 AgentFlow HTTP API 的可用性，不代表 SSH、Docker、远程机器或 Pi Agent
          已经就绪。
        </Typography.Text>
      </Flex>
    </Flex>
  );
}
