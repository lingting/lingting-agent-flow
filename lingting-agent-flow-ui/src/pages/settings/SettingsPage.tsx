/**
 * 设置中心。
 *
 * 提供两项已经真实可用的能力：环境管理与固定角色管理。两者都直接读写服务端：
 * 环境属于执行节点、版本只追加、构建由服务端后台执行；角色集合固定为四个，
 * 只有系统 Prompt、模型与启用状态可以修改。其他设置项尚未实现，因此不放置占位入口，
 * 避免用户误以为功能已经可用。
 */
import { Flex, Typography } from "antd";

import { EnvironmentManagementPanel } from "@/pages/settings/EnvironmentManagementPanel";
import { RoleManagementPanel } from "@/pages/settings/RoleManagementPanel";

export function SettingsPage() {
  return (
    <Flex gap="middle" vertical style={{ width: "100%" }}>
      <Flex gap={4} vertical>
        <Typography.Title level={4} style={{ margin: 0 }}>
          设置
        </Typography.Title>
        <Typography.Text type="secondary">
          管理执行节点上的环境定义与历史版本，并配置四个固定角色的系统 Prompt、模型与启用状态。
        </Typography.Text>
      </Flex>
      <Flex gap={4} vertical>
        <Typography.Title level={5} style={{ margin: 0 }}>
          环境管理
        </Typography.Title>
        <EnvironmentManagementPanel />
      </Flex>
      <Flex gap={4} vertical>
        <Typography.Title level={5} style={{ margin: 0 }}>
          角色管理
        </Typography.Title>
        <RoleManagementPanel />
      </Flex>
    </Flex>
  );
}
