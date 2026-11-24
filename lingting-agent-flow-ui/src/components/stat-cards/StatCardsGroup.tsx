/**
 * 值卡片分组的外层布局（第 1 层组件）。
 *
 * 结构固定为：标题 + 刷新按钮在上，下面是 flex row，每个值各自一张卡片。它只负责布局
 * 与空/加载/失败状态，不发起请求，也不知道数据来自哪个接口。
 */
import { ReloadOutlined } from "@ant-design/icons";
import { Alert, Button, Card, Flex, Skeleton, theme, Typography } from "antd";

import "./StatCardsGroup.css";
import type { StatCardItem, StatCardsError, StatCardTone } from "./types";

export type StatCardsGroupProps = {
  /** 分组标题 */
  title: string;
  /** 值卡片；空数组时显示 emptyText */
  items: readonly StatCardItem[];
  /** 正在加载：没有可展示数据时显示骨架屏，已有数据时只让刷新按钮转圈 */
  loading?: boolean;
  /** 失败提示；非空时显示在值区上方，值区仍保留上一次成功的结论 */
  error?: StatCardsError;
  /** 刷新入口；缺省时不显示刷新按钮 */
  onRefresh?: () => void;
  /** 值区为空时的说明 */
  emptyText?: string;
  /** 分组附加说明，显示在值区下方 */
  footer?: string;
};

/** 语义色到 antd 文本类型的映射；`default` 表示沿用正文颜色 */
const TONE_TEXT_TYPE: Record<StatCardTone, "success" | "warning" | "danger" | undefined> = {
  default: undefined,
  error: "danger",
  success: "success",
  warning: "warning",
};

/** 加载中且还没有数据时的骨架卡片数量：足够占位，又不暗示具体指标 */
const SKELETON_COUNT = 3;

/** 单个值卡片：卡片本身带阴影，与外层 ghost 分组形成层次 */
function StatValueCard({ item, shadow }: { item: StatCardItem; shadow: string }) {
  return (
    <Card className="stat-cards-group__card" size="small" styles={{ root: { boxShadow: shadow } }}>
      <Flex gap={2} vertical>
        <Typography.Text className="stat-cards-group__label" type="secondary">
          {item.label}
        </Typography.Text>
        <Typography.Text
          className="stat-cards-group__value"
          strong
          type={TONE_TEXT_TYPE[item.tone ?? "default"]}
        >
          {item.value}
        </Typography.Text>
        {item.hint && (
          <Typography.Text className="stat-cards-group__hint" type="secondary">
            {item.hint}
          </Typography.Text>
        )}
      </Flex>
    </Card>
  );
}

/** 值卡片分组 */
export function StatCardsGroup({
  title,
  items,
  loading = false,
  error,
  onRefresh,
  emptyText = "暂无数据",
  footer,
}: StatCardsGroupProps) {
  const showSkeleton = loading && items.length === 0;
  // 加载失败且没有任何数据时只显示失败提示：此时"暂无数据"会被误读成"确实一条都没有"。
  const showEmpty = !showSkeleton && items.length === 0 && !error;
  const { token } = theme.useToken();
  // 标题与刷新按钮在同一行：刷新紧跟标题，不放到标题栏最右侧。
  const header = (
    <Flex align="center" gap="small">
      <Typography.Text strong>{title}</Typography.Text>
      {onRefresh && (
        <Button
          icon={<ReloadOutlined />}
          loading={loading}
          onClick={onRefresh}
          size="small"
          type="text"
        >
          刷新
        </Button>
      )}
    </Flex>
  );

  return (
    <Card
      className="stat-cards-group"
      size="small"
      // 纯 ghost：无边框、无阴影、无背景；阴影只给内层的值卡片。
      styles={{ body: { width: "100%" }, root: { background: "transparent", boxShadow: "none" } }}
      title={header}
      variant="borderless"
    >
      <Flex gap="small" vertical>
        {error && (
          <Alert description={error.description} showIcon title={error.title} type="error" />
        )}
        {showSkeleton && (
          <Flex className="stat-cards-group__values" gap="small" wrap>
            {Array.from({ length: SKELETON_COUNT }, (_unused, index) => (
              <Card
                className="stat-cards-group__card"
                key={index}
                size="small"
                styles={{ root: { boxShadow: token.boxShadowTertiary } }}
              >
                <Skeleton active paragraph={false} title={{ width: 72 }} />
              </Card>
            ))}
          </Flex>
        )}
        {!showSkeleton && items.length > 0 && (
          <Flex align="stretch" className="stat-cards-group__values" gap="small" wrap>
            {items.map((item) => (
              <StatValueCard item={item} key={item.key} shadow={token.boxShadowTertiary} />
            ))}
          </Flex>
        )}
        {showEmpty && <Typography.Text type="secondary">{emptyText}</Typography.Text>}
        {footer && (
          <Typography.Text className="stat-cards-group__footer" type="secondary">
            {footer}
          </Typography.Text>
        )}
      </Flex>
    </Card>
  );
}
