/**
 * 设置中心。
 *
 * 提供两项已经真实可用的能力：环境管理与基础配置只读查看。两者都直接读写服务端：环境属于
 * 执行节点、版本只追加、构建由服务端后台执行；基础配置展示服务启动时实际生效的四项运行参数，
 * 不提供编辑、保存或重启入口。角色管理已独立成菜单页；其他设置项尚未实现，因此不放置占位
 * 入口，避免用户误以为功能已经可用。
 */
import { Flex, Typography } from "antd";

import { BasicConfigPanel } from "@/pages/settings/BasicConfigPanel";
import { EnvironmentManagementPanel } from "@/pages/settings/EnvironmentManagementPanel";

export function SettingsPage() {
  return (
    <Flex gap="middle" vertical style={{ width: "100%" }}>
      <Flex gap={4} vertical>
        <Typography.Title level={5} style={{ margin: 0 }}>
          环境管理
        </Typography.Title>
        <EnvironmentManagementPanel />
      </Flex>
      <BasicConfigPanel />
    </Flex>
  );
}
