/**
 * 方案版本预览：历史版本切换 + 需求分析 + 实施方案。
 *
 * 版本只追加：页面可以切换查看历史版本，任何时候都不提供编辑或覆盖旧版本的入口。
 * 默认选中最新版本；新一轮分析追加版本后自动跟随最新版本，用户手动选中的历史版本
 * 在数据没有新增时保持不变。
 */
import { Alert, Flex, Radio, Space, Tag, Typography } from "antd";
import { useEffect, useMemo, useRef, useState } from "react";

import type { PlanningRevision } from "@/lib/api";
import { ImplementationPlanPanel } from "@/pages/planning/components/ImplementationPlanPanel";
import { RequirementAnalysisPanel } from "@/pages/planning/components/RequirementAnalysisPanel";
import {
  formatPlanningTime,
  planningRevisionLabel,
  resolvePlanningRevisionSelection,
  sortPlanningRevisions,
} from "@/pages/planning/planning";

/** 方案版本预览面板 */
export function PlanningRevisionPanel(props: { revisions: PlanningRevision[] }) {
  const revisions = useMemo(() => sortPlanningRevisions(props.revisions), [props.revisions]);
  const latestID = revisions.at(-1)?.id;
  const [selectedID, setSelectedID] = useState<string | undefined>(latestID);
  const previousLatestRef = useRef<string | undefined>(latestID);

  useEffect(() => {
    const previousLatestID = previousLatestRef.current;
    setSelectedID((current) =>
      resolvePlanningRevisionSelection({ previousLatestID, revisions, selectedID: current }),
    );
    previousLatestRef.current = latestID;
  }, [latestID, revisions]);

  if (revisions.length === 0) {
    return (
      <Flex gap="middle" vertical>
        <Typography.Text strong>方案预览</Typography.Text>
        <Alert
          description="该会话还没有方案版本。只有两阶段分析成功并完成持久化后，服务端才会追加 Revision。"
          showIcon
          title="还没有可预览的方案"
          type="info"
        />
      </Flex>
    );
  }

  const selected = revisions.find((revision) => revision.id === selectedID) ?? revisions.at(-1);
  return (
    <Flex gap="middle" vertical>
      <Space align="center" size={8} wrap>
        <Typography.Text strong>方案预览</Typography.Text>
        <Typography.Text type="secondary">
          共 {revisions.length} 个版本，默认展示最新版本；历史版本只读，旧版本不会被覆盖。
        </Typography.Text>
      </Space>

      <Radio.Group
        aria-label="选择方案版本"
        buttonStyle="solid"
        onChange={(event) => setSelectedID(String(event.target.value))}
        optionType="button"
        options={revisions.map((revision) => ({
          label: planningRevisionLabel(revision),
          value: revision.id,
        }))}
        value={selected?.id}
      />

      {selected && (
        <Flex gap="middle" vertical>
          <Space align="center" size={8} wrap>
            <Tag color={selected.id === latestID ? "success" : "default"}>
              {planningRevisionLabel(selected)}
              {selected.id === latestID ? "（最新）" : ""}
            </Tag>
            <Typography.Text type="secondary">
              创建时间 {formatPlanningTime(selected.created_at)}
            </Typography.Text>
            <Typography.Text type="secondary">
              快照结构版本 {selected.snapshot_version}
            </Typography.Text>
            <Typography.Text code>{selected.id}</Typography.Text>
          </Space>

          <Typography.Text strong>需求分析</Typography.Text>
          <RequirementAnalysisPanel analysis={selected.requirement_analysis} />

          <Typography.Text strong>实施方案</Typography.Text>
          <ImplementationPlanPanel plan={selected.implementation_plan} />
        </Flex>
      )}
    </Flex>
  );
}
