/**
 * 概览页：需求 / 项目 / 任务 / 机器的数量汇总，健康检查放在最底下。
 *
 * 每个汇总分组的标题、刷新按钮与布局一致，数据各自独立请求：一个分组失败只影响它自己，
 * 不会让整页变成未知。计数全部来自服务端 `/api/v1/overview/*`，页面不做本地统计。
 */
import { Flex } from "antd";

import { PagePlaceholder } from "@/components/page-placeholder";
import { StatCardsSection } from "@/components/stat-cards";

import { ConnectionPanel } from "./ConnectionPanel";
import {
  loadHostStats,
  loadProjectStats,
  loadRequirementStats,
  loadTaskStats,
} from "./overviewStats";

/** 概览页 */
export function OverviewPage() {
  return (
    <Flex gap="middle" vertical style={{ width: "100%" }}>
      <PagePlaceholder description="AgentFlow 的总览入口，用于汇总需求、任务与执行状态。" />
      <StatCardsSection
        emptyText="还没有需求：需求在方案确认后生成"
        request={loadRequirementStats}
        title="需求"
      />
      <StatCardsSection emptyText="还没有登记项目" request={loadProjectStats} title="项目" />
      <StatCardsSection emptyText="还没有任务" request={loadTaskStats} title="任务" />
      <StatCardsSection emptyText="还没有登记执行节点" request={loadHostStats} title="机器" />
      <ConnectionPanel />
    </Flex>
  );
}
