import {
  ClusterOutlined,
  DashboardOutlined,
  DeploymentUnitOutlined,
  ProjectOutlined,
  SettingOutlined,
  UnorderedListOutlined,
} from "@ant-design/icons";
import type { MenuRouteDefinition } from "lingting-react-ui";

import { EnvironmentsPage } from "@/pages/environments";
import { MachinesPage } from "@/pages/machines";
import { OverviewPage } from "@/pages/overview";
import { ProjectsPage } from "@/pages/projects";
import { SettingsPage } from "@/pages/settings";
import { TasksPage } from "@/pages/tasks";

/** 导航与路由的唯一定义处：菜单标签、页面标题、当前路由高亮都由它派生 */
export const navigationRoutes: readonly MenuRouteDefinition[] = [
  {
    component: OverviewPage,
    icon: <DashboardOutlined />,
    path: "overview",
    title: "概览",
  },
  {
    component: TasksPage,
    icon: <UnorderedListOutlined />,
    path: "tasks",
    title: "任务",
  },
  {
    component: ProjectsPage,
    icon: <ProjectOutlined />,
    path: "projects",
    title: "项目",
  },
  {
    component: MachinesPage,
    icon: <DeploymentUnitOutlined />,
    path: "machines",
    title: "机器",
  },
  {
    component: EnvironmentsPage,
    icon: <ClusterOutlined />,
    path: "environments",
    title: "环境",
  },
  {
    component: SettingsPage,
    icon: <SettingOutlined />,
    path: "settings",
    title: "设置",
  },
];

/** 根路径重定向目标：由 LRI 布局接管 `/`，应用内不注册根路径路由 */
export const ROOT_REDIRECT_TO = "/overview";

/** 产品名称，同时作为文档标题前缀 */
export const PRODUCT_TITLE = "AgentFlow";
