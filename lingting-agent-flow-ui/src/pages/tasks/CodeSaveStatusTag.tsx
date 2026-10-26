/**
 * 代码保存状态的统一标签渲染。
 *
 * 已知取值用 `DictTag` 呈现（标签与颜色来自 `codeSaves.ts` 的单一映射）；未知取值原样
 * 展示，绝不因为字典缺项而渲染成空白，也不会被回落到某个成功文案。
 *
 * 单独抽出是为了让任务详情里的代码保存面板与续跑资格面板共用同一套渲染规则，
 * 避免两处各写一套、逐渐产生冲突的展示语义。
 */
import { Typography } from "antd";
import { DictTag, type DictItem } from "lingting-react-ui";

import type { CodeSaveStatusMeta } from "@/pages/tasks/codeSaves";

/** 把展示颜色映射到 Typography 的语义类型 */
function textType(color: string): "success" | "warning" | "danger" | "secondary" {
  switch (color) {
    case "success":
      return "success";
    case "warning":
    case "processing":
      return "warning";
    case "error":
      return "danger";
    default:
      return "secondary";
  }
}

/** 代码保存状态标签：已知取值走字典，未知取值原样展示 */
export function CodeSaveStatusTag({
  dict,
  meta,
  value,
}: {
  dict: DictItem<string>[];
  meta: CodeSaveStatusMeta;
  value?: string;
}) {
  if (meta.known && value) {
    return <DictTag dict={dict} value={value} />;
  }
  return <Typography.Text type={textType(meta.color)}>{meta.label}</Typography.Text>;
}
