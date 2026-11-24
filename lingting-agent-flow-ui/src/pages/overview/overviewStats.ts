/**
 * 概览页的数据装配：把服务端计数转换成值卡片。
 *
 * 约束：
 * - 计数全部来自服务端（`/overview/*`），前端不自行统计、不把失败当成 0、不推测进度；
 * - 已知状态按 Go 端定义顺序展示，缺失的键补 0（0 表示该状态确实没有记录）；
 * - 响应里多出来的未知状态原样追加，不丢弃、不改名成已知状态。
 */
import type { StatCardItem, StatCardTone } from "@/components/stat-cards";
import {
  fetchOverviewHosts,
  fetchOverviewProjects,
  fetchOverviewRequirements,
  fetchOverviewTasks,
  type OverviewStatusCountsResponse,
} from "@/lib/api";
import { VALIDATION_STATUS_META } from "@/pages/machines/status";
import { TASK_STATUS_META } from "@/pages/tasks/status";

/** 一个已知状态的展示定义 */
type StatusDefinition = {
  /** 服务端持久化的状态取值 */
  value: string;
  /** 中文标签 */
  label: string;
  /** 值的语义色 */
  tone: StatCardTone;
};

/** 需求状态的展示定义，覆盖 Go 端 `domain.RequirementStatus` 的每一个取值 */
const REQUIREMENT_STATUS_DEFINITIONS: readonly StatusDefinition[] = [
  { label: "排队中", tone: "default", value: "queued" },
  { label: "处理中", tone: "default", value: "processing" },
  { label: "已完成", tone: "success", value: "completed" },
  { label: "失败", tone: "error", value: "failed" },
  { label: "已取消", tone: "default", value: "cancelled" },
];

/**
 * 把 antd 标签颜色收敛为值卡片的语义色。
 *
 * `processing` 是"仍在推进"的中性提示，不与成功或失败混同，因此落到中性色。
 */
function tagColorTone(color: string): StatCardTone {
  switch (color) {
    case "success":
      return "success";
    case "error":
      return "error";
    case "warning":
      return "warning";
    default:
      return "default";
  }
}

/** 任务状态的展示定义，顺序与 `TASK_STATUS_META` 一致 */
const TASK_STATUS_DEFINITIONS: readonly StatusDefinition[] = Object.entries(TASK_STATUS_META).map(
  ([value, meta]) => ({ label: meta.label, tone: tagColorTone(meta.color), value }),
);

/** 执行节点校验状态的展示定义，顺序与 `VALIDATION_STATUS_META` 一致 */
const HOST_STATUS_DEFINITIONS: readonly StatusDefinition[] = Object.entries(
  VALIDATION_STATUS_META,
).map(([value, meta]) => ({ label: meta.label, tone: tagColorTone(meta.color), value }));

/**
 * 生成"总数 + 各状态"的卡片。
 *
 * 总数取服务端返回的 `total`，不是本地求和：两者不一致时以服务端为准，前端不做修正。
 */
function statusCountItems(
  response: OverviewStatusCountsResponse,
  definitions: readonly StatusDefinition[],
): StatCardItem[] {
  const known = new Set(definitions.map((definition) => definition.value));
  const items: StatCardItem[] = [
    { key: "total", label: "总数", value: response.total },
    ...definitions.map((definition) => ({
      key: definition.value,
      label: definition.label,
      tone: definition.tone,
      value: response.counts[definition.value] ?? 0,
    })),
  ];
  for (const [status, count] of Object.entries(response.counts)) {
    if (known.has(status)) continue;
    items.push({
      hint: "服务端返回的未知状态",
      key: status,
      label: status,
      tone: "warning",
      value: count,
    });
  }
  return items;
}

/** 读取需求计数 */
export async function loadRequirementStats(signal: AbortSignal): Promise<StatCardItem[]> {
  const { data } = await fetchOverviewRequirements({ signal });
  return statusCountItems(data, REQUIREMENT_STATUS_DEFINITIONS);
}

/** 读取项目计数：总数与两项配置维度事实 */
export async function loadProjectStats(signal: AbortSignal): Promise<StatCardItem[]> {
  const { data } = await fetchOverviewProjects({ signal });
  return [
    { key: "total", label: "项目总数", value: data.total },
    { key: "with_default_host", label: "已设置默认执行节点", value: data.with_default_host },
    { key: "with_caches", label: "已配置缓存类型", value: data.with_caches },
  ];
}

/** 读取任务计数 */
export async function loadTaskStats(signal: AbortSignal): Promise<StatCardItem[]> {
  const { data } = await fetchOverviewTasks({ signal });
  return statusCountItems(data, TASK_STATUS_DEFINITIONS);
}

/** 读取机器（执行节点）计数 */
export async function loadHostStats(signal: AbortSignal): Promise<StatCardItem[]> {
  const { data } = await fetchOverviewHosts({ signal });
  return statusCountItems(data, HOST_STATUS_DEFINITIONS);
}
