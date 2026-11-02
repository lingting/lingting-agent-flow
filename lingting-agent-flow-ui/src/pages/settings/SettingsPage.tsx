/**
 * 设置中心。
 *
 * 本轮只提供「环境管理」这一项已经真实可用的能力：环境属于执行节点，版本只追加，
 * 构建由服务端后台执行。角色管理与基础配置查看属于后续里程碑，这里不放置占位入口，
 * 避免用户误以为功能已经可用。
 */
import { Flex, Typography } from "antd";

import { EnvironmentManagementPanel } from "@/pages/settings/EnvironmentManagementPanel";

export function SettingsPage() {
  return (
    <Flex gap="middle" vertical style={{ width: "100%" }}>
      <Flex gap={4} vertical>
        <Typography.Title level={4} style={{ margin: 0 }}>
          设置
        </Typography.Title>
        <Typography.Text type="secondary">
          管理执行节点上的环境定义与历史版本，查看构建状态与构建日志。
        </Typography.Text>
      </Flex>
      <Flex gap={4} vertical>
        <Typography.Title level={5} style={{ margin: 0 }}>
          环境管理
        </Typography.Title>
        <EnvironmentManagementPanel />
      </Flex>
    </Flex>
  );
}
