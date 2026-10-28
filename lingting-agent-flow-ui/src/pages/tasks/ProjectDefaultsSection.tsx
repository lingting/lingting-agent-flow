/**
 * 创建任务表单里"项目默认执行配置"的收起摘要与展开入口。
 *
 * 只负责展示：取值来自表单，来源来自 `projectTaskPrefill` 记录的交互事实，展开状态由
 * 调用方持有（表单局部状态，不引入全局状态或持久化）。
 */
import { Button, Descriptions, Space, Typography } from "antd";

import type { ProjectExecutionSummaryRow } from "@/pages/tasks/projectExecutionSummary";

/** 展开入口文案 */
export const EXPAND_PROJECT_FIELDS_LABEL = "展开修改仓库地址、基础分支与工作目录";

/** 收起入口文案 */
export const COLLAPSE_PROJECT_FIELDS_LABEL = "收起仓库地址、基础分支与工作目录";

export type ProjectDefaultsSectionProps = {
  /** 展开后的字段容器标识，用于 `aria-controls` */
  controls: string;
  /** 是否已展开 */
  expanded: boolean;
  /** 尚未选择关联项目时的提示；为空表示不显示 */
  hint?: string;
  /** 摘要行 */
  rows: ProjectExecutionSummaryRow[];
  onToggle: () => void;
};

export function ProjectDefaultsSection({
  controls,
  expanded,
  hint,
  onToggle,
  rows,
}: ProjectDefaultsSectionProps) {
  return (
    <Space direction="vertical" size={4} style={{ display: "flex", marginBottom: 16 }}>
      <Space align="center" size={8} wrap>
        <Button
          aria-controls={controls}
          aria-expanded={expanded}
          size="small"
          type="link"
          onClick={onToggle}
        >
          {expanded ? COLLAPSE_PROJECT_FIELDS_LABEL : EXPAND_PROJECT_FIELDS_LABEL}
        </Button>
        {!expanded && (
          <Typography.Text type="secondary">默认继承关联项目，无需重复填写</Typography.Text>
        )}
      </Space>
      {!expanded && (
        <>
          {hint && <Typography.Text type="warning">{hint}</Typography.Text>}
          <Descriptions column={1} size="small">
            {rows.map((row) => (
              <Descriptions.Item key={row.field} label={row.label}>
                <Space size={8} wrap>
                  <Typography.Text type={row.missing ? "danger" : undefined}>
                    {row.value}
                  </Typography.Text>
                  {row.source && <Typography.Text type="secondary">{row.source}</Typography.Text>}
                </Space>
              </Descriptions.Item>
            ))}
          </Descriptions>
        </>
      )}
    </Space>
  );
}
