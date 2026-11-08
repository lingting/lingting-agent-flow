/**
 * 需求分析预览。
 *
 * 八个字段全部展示，顺序固定；服务端返回空列表时如实说明，不补造内容，也不隐藏字段。
 */
import { Flex, Typography } from "antd";

import type { RequirementAnalysis } from "@/lib/api";
import { requirementAnalysisSections } from "@/pages/planning/planning";

/** 需求分析预览面板 */
export function RequirementAnalysisPanel(props: { analysis?: RequirementAnalysis | null }) {
  const sections = requirementAnalysisSections(props.analysis);
  if (sections.length === 0) {
    return <Typography.Text type="secondary">这一版没有可展示的需求分析快照。</Typography.Text>;
  }
  return (
    <Flex data-testid="planning-requirement-analysis" gap="middle" vertical>
      {sections.map((section) => (
        <Flex gap={2} key={section.key} vertical>
          <Typography.Text strong>{section.title}</Typography.Text>
          {section.kind === "text" ? (
            <Typography.Paragraph style={{ marginBottom: 0, whiteSpace: "pre-wrap" }}>
              {section.text?.trim() ? section.text : "服务端返回空内容"}
            </Typography.Paragraph>
          ) : section.items && section.items.length > 0 ? (
            <ul style={{ margin: 0, paddingLeft: 20 }}>
              {section.items.map((item, index) => (
                <li key={`${section.key}-${index}`}>{item}</li>
              ))}
            </ul>
          ) : (
            <Typography.Text type="secondary">服务端返回空列表</Typography.Text>
          )}
        </Flex>
      ))}
    </Flex>
  );
}
