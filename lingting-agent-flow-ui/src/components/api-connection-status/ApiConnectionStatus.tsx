import { ReloadOutlined } from "@ant-design/icons";
import { Badge, Button, Flex, Tooltip, Typography } from "antd";

import type { ApiError } from "@/lib/api";
import type { ConnectionState } from "@/lib/api/useApiConnection";

import "./ApiConnectionStatus.css";

export type ApiConnectionStatusProps = {
  /** 连接状态快照 */
  state: ConnectionState;
  /** 手动重新检查 */
  onRetry: () => void;
};

/** 状态对应的展示文案、颜色与提示 */
const PRESENTATION = {
  checking: { color: "processing", label: "检查中", title: "正在检查 AgentFlow HTTP API" },
  connected: { color: "success", label: "已连接", title: "AgentFlow HTTP API 可用" },
  disconnected: { color: "error", label: "未连接", title: "无法连接 AgentFlow HTTP API" },
} as const;

/**
 * 生成失败原因的说明文字。
 *
 * 只描述类别与恢复建议，不展示请求体或任何凭据。
 */
export function describeConnectionError(error: ApiError | undefined): string {
  if (!error) {
    return "无法连接 AgentFlow HTTP API";
  }
  switch (error.kind) {
    case "timeout":
      return "健康检查超时，请确认 AgentFlow 服务是否繁忙后重试";
    case "aborted":
      return "健康检查已取消";
    case "invalid_response":
      return "服务返回的内容无法解析，可能不是 AgentFlow API";
    case "http":
      return error.message;
    case "network":
      return "无法连接 AgentFlow 服务，请确认已启动 agentflow web serve";
    default:
      return "无法连接 AgentFlow HTTP API";
  }
}

/**
 * 头部连接指示器：只表示 AgentFlow HTTP API 的可用性。
 *
 * 它不表示 SSH、Docker、远程机器或 Pi Agent 已经连接。
 */
export function ApiConnectionStatus({ state, onRetry }: ApiConnectionStatusProps) {
  const presentation = PRESENTATION[state.status];
  const errorText =
    state.status === "disconnected" ? describeConnectionError(state.error) : presentation.title;
  const badge = (
    <Flex align="center" className="api-connection-status" gap={6}>
      <Badge status={presentation.color} />
      <Typography.Text className="api-connection-status__label" type="secondary">
        {presentation.label}
      </Typography.Text>
    </Flex>
  );
  return (
    <Flex align="center" gap={4}>
      <Tooltip title={errorText}>{badge}</Tooltip>
      {state.status === "disconnected" && (
        <Tooltip title="重新检查连接">
          <Button
            aria-label="重新检查连接"
            icon={<ReloadOutlined />}
            size="small"
            type="text"
            onClick={onRetry}
          />
        </Tooltip>
      )}
    </Flex>
  );
}
