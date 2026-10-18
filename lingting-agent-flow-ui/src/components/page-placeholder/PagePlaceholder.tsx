import { Typography } from "antd";

export type PagePlaceholderProps = {
  description: string;
};

/**
 * 占位页内容：只有说明文字，不渲染虚构的业务数据、标题栏或可用操作。
 */
export function PagePlaceholder({ description }: PagePlaceholderProps) {
  return <Typography.Paragraph type="secondary">{description}</Typography.Paragraph>;
}
