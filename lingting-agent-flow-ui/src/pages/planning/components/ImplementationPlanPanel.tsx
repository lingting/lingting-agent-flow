/**
 * 实施方案预览。
 *
 * 任务草案不是正式任务：标识只用于表达依赖，顺序即方案中的执行顺序。任务列表使用
 * `ExTable` 承载，依赖与验收条件都必须可见，不能被折叠成一句摘要。
 */
import { Flex, Typography } from "antd";
import { type ExTableColumn, ExTable } from "lingting-react-ui";

import type { ImplementationPlan } from "@/lib/api";
import {
  implementationPlanTaskRows,
  planningTaskDependencyText,
  type PlanningPlanTaskRow,
} from "@/pages/planning/planning";

/** 任务列表列定义 */
const columns: ExTableColumn<PlanningPlanTaskRow>[] = [
  {
    dataIndex: "order",
    search: false,
    title: "顺序",
    width: 72,
  },
  {
    dataIndex: "task_id",
    render: (_dom, record) => <Typography.Text code>{record.task_id}</Typography.Text>,
    search: false,
    title: "草案任务标识",
    width: 160,
  },
  {
    dataIndex: "title",
    search: false,
    title: "任务标题",
    width: 200,
  },
  {
    dataIndex: "description",
    render: (_dom, record) => (
      <Typography.Text style={{ whiteSpace: "pre-wrap" }}>{record.description}</Typography.Text>
    ),
    search: false,
    title: "任务描述",
  },
  {
    dataIndex: "depends_on",
    render: (_dom, record) => (
      <Typography.Text type={record.depends_on.length === 0 ? "secondary" : undefined}>
        {planningTaskDependencyText(record.depends_on)}
      </Typography.Text>
    ),
    search: false,
    title: "前置依赖",
    width: 180,
  },
  {
    dataIndex: "acceptance_criteria",
    render: (_dom, record) =>
      record.acceptance_criteria.length === 0 ? (
        <Typography.Text type="secondary">服务端返回空列表</Typography.Text>
      ) : (
        <ul style={{ margin: 0, paddingLeft: 18 }}>
          {record.acceptance_criteria.map((item, index) => (
            <li key={`${record.key}-acceptance-${index}`}>{item}</li>
          ))}
        </ul>
      ),
    search: false,
    title: "验收条件",
    width: 260,
  },
];

/** 实施方案预览面板 */
export function ImplementationPlanPanel(props: { plan?: ImplementationPlan | null }) {
  const rows = implementationPlanTaskRows(props.plan);
  return (
    <Flex gap="middle" vertical>
      <Flex gap={2} vertical>
        <Typography.Text strong>方案摘要</Typography.Text>
        <Typography.Paragraph style={{ marginBottom: 0, whiteSpace: "pre-wrap" }}>
          {props.plan?.summary?.trim() ? props.plan.summary : "服务端返回空内容"}
        </Typography.Paragraph>
      </Flex>

      <Flex align="center" gap={8} wrap>
        <Typography.Text strong>任务草案</Typography.Text>
        <Typography.Text type="secondary">
          {rows.length === 0 ? "这一版没有任务草案" : `共 ${rows.length} 项，顺序即执行顺序`}
        </Typography.Text>
        <Typography.Text type="warning">
          草案只用于确认方案，不是已创建的正式任务；本阶段不会写入 Requirement、Task、 Step 或
          Attempt。
        </Typography.Text>
      </Flex>

      <ExTable<PlanningPlanTaskRow>
        columns={columns}
        dataSource={rows}
        locale={{ emptyText: "这一版没有任务草案" }}
        pagination={false}
        rowKey="key"
        search={false}
        size="small"
      />
    </Flex>
  );
}
