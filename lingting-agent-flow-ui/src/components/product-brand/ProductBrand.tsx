import { Avatar, Flex, Typography } from "antd";
import { SidebarCollapsed, useSidebarLayout } from "lingting-react-ui";

import "./ProductBrand.css";

export type ProductBrandProps = {
  icon: string;
  title: string;
};

/**
 * 产品品牌信息：产品图标与名称。侧栏收起时只保留图标。
 *
 * 结构与参考项目 lingting-ai-gateway 的 ProductBrand 一致，宽度约束与侧栏收起行为跟随 LRI 侧栏状态。
 */
export function ProductBrand({ icon, title }: ProductBrandProps) {
  const { collapsed, sidebarDisplay } = useSidebarLayout();
  const showTitle = collapsed === SidebarCollapsed.Expanded || sidebarDisplay === "drawer";

  return (
    <Flex align="center" className="product-brand" gap={12} justify="center">
      <Flex align="center" className="product-brand-icon" justify="center">
        <Avatar alt={title} shape="square" size={30} src={icon} />
      </Flex>
      {showTitle && (
        <Typography.Text className="product-brand-title" ellipsis strong>
          {title}
        </Typography.Text>
      )}
    </Flex>
  );
}
