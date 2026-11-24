/**
 * 健康检查面板：展示真实的健康检查结果。
 *
 * 它只反映 AgentFlow HTTP API 与本地数据库的可用性，不代表 SSH、Docker 或 Pi Agent 已就绪。
 * 状态来自全应用共享的连接上下文，与头部连接指示器使用同一份结论，不在这里另发一次请求。
 */
import { describeConnectionError } from "@/components/api-connection-status";
import { StatCardsGroup, type StatCardItem } from "@/components/stat-cards";
import { useApiConnectionContext } from "@/lib/api/ApiConnectionContext";
import type { HealthCheck, HealthResponse } from "@/lib/api";

/** 检查项名称到中文说明的映射，未知名称原样展示，不伪造含义 */
const CHECK_LABELS: Record<string, string> = {
  database: "SQLite 数据库",
  http_api: "HTTP API",
};

/** 从错误响应中取出健康检查详情；不是健康检查响应时返回 undefined */
function healthFromError(body: unknown): HealthResponse | undefined {
  if (typeof body !== "object" || body === null) return undefined;
  const candidate = body as Partial<HealthResponse>;
  return candidate.status === "ok" || candidate.status === "degraded"
    ? (candidate as HealthResponse)
    : undefined;
}

/** 单项检查结果转成一张值卡片；失败原因写在卡片说明里，不隐藏 */
function checkItem(check: HealthCheck): StatCardItem {
  const passed = check.status === "pass";
  return {
    hint: passed ? undefined : check.message,
    key: `check:${check.name}`,
    label: CHECK_LABELS[check.name] ?? check.name,
    tone: passed ? "success" : "error",
    value: passed ? "正常" : "不可用",
  };
}

/** 健康检查结果转成值卡片；没有结果时返回空数组，由加载与失败状态表达 */
function healthItems(health: HealthResponse | undefined): StatCardItem[] {
  if (!health) return [];
  const healthy = health.status === "ok";
  return [
    {
      key: "overall",
      label: "总体状态",
      tone: healthy ? "success" : "warning",
      value: healthy ? "正常" : "部分不可用",
    },
    ...health.checks.map(checkItem),
    {
      key: "checked_at",
      label: "检查时间",
      value: new Date(health.checked_at).toLocaleString(),
    },
  ];
}

/** 健康检查面板 */
export function ConnectionPanel() {
  const { check, state } = useApiConnectionContext();
  const { error, health, status } = state;
  // 后端返回 503 时同样带有健康检查详情，用于说明是哪一项不可用。
  const failedHealth = status === "disconnected" ? healthFromError(error?.body) : undefined;
  const visibleHealth = status === "connected" ? health : failedHealth;

  return (
    <StatCardsGroup
      emptyText="尚未取得健康检查结果"
      error={
        status === "disconnected"
          ? {
              description: describeConnectionError(error),
              title: "无法连接 AgentFlow 服务",
            }
          : undefined
      }
      footer={
        "连接状态只表示 AgentFlow HTTP API 与本地数据库的可用性，" +
        "不代表 SSH、Docker、远程机器或 Pi Agent 已经就绪。"
      }
      items={healthItems(visibleHealth)}
      loading={status === "checking"}
      onRefresh={() => void check()}
      title="健康检查"
    />
  );
}
