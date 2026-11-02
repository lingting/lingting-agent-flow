/**
 * 设置中心「角色管理」的展示规则。
 *
 * 角色集合固定为四个，由服务端初始化：这里只决定展示顺序、中文标签与"哪些字段可以编辑"，
 * 不复制后端的校验规则。数据全部来自服务端，是否启用、模型与提示词都按接口返回值展示。
 */
import type { Role, RoleType } from "@/lib/api";
import { ApiError } from "@/lib/api";

/** 固定角色标识与设置页展示顺序：角色集合固定为四个，不可新增或删除 */
const FIXED_ROLE_IDS = ["requirement_analyst", "planner", "developer", "validator"] as const;

/** 角色类型的职责说明，取值与 Go 端固定的四个角色一致 */
const ROLE_DUTY: Record<RoleType, string> = {
  requirement_analyst: "分析用户需求与项目现状，只读",
  planner: "生成实施方案与任务拆分，同时承担任务内部计划，只读",
  developer: "按开发步骤实现代码，可读写并执行命令",
  validator: "审阅代码与验证证据，只读",
};

/** 工具能力的中文标签，取值与 Go 端 `domain.ToolPermission` 一致 */
const PERMISSION_LABEL: Record<string, string> = {
  read: "读取",
  write: "写入",
  execute: "执行命令",
};

/** 读取角色职责说明；未知类型原样展示 */
export function roleDutyLabel(type: string): string {
  return ROLE_DUTY[type as RoleType] ?? type;
}

/** 读取工具能力标签；未知取值原样展示 */
export function permissionLabel(permission: string): string {
  return PERMISSION_LABEL[permission] ?? permission;
}

/**
 * 按固定顺序挑选四个固定角色。
 *
 * 只展示服务端确实返回的角色：缺失的角色不会被本地补造，也不会显示默认值冒充已保存的配置。
 */
export function fixedRoleRows(roles: Role[]): Role[] {
  const byID = new Map(roles.map((role) => [role.id, role]));
  const rows: Role[] = [];
  for (const id of FIXED_ROLE_IDS) {
    const role = byID.get(id);
    if (role) rows.push(role);
  }
  return rows;
}

/** Pi 内置工具的中文标签，取值与 Go 端 `domain.PiTool*` 一致 */
const TOOL_LABEL: Record<string, string> = {
  read: "读取文件",
  grep: "搜索内容",
  find: "查找路径",
  ls: "列出目录",
  edit: "编辑文件",
  write: "写入文件",
  bash: "执行命令",
};

/** 读取工具标签；未知工具名原样展示，不隐藏服务端返回的值 */
export function toolLabel(tool: string): string {
  return TOOL_LABEL[tool] ?? tool;
}

/**
 * 把工具列表渲染成一句说明。
 *
 * 空列表表示本次调用不加载任何工具，因此显示"无"而不是"不限制"。
 */
export function toolsSummary(tools: readonly string[] | undefined): string {
  if (!tools || tools.length === 0) return "无";
  return tools.map(toolLabel).join("、");
}

/** 把能力列表渲染成一句说明，空列表表示未声明任何能力 */
export function permissionsSummary(permissions: readonly string[] | undefined): string {
  if (!permissions || permissions.length === 0) return "无";
  return permissions.map(permissionLabel).join("、");
}

/**
 * 取出已保存取值中仍属于系统能力上限的部分。
 *
 * 上限由服务端返回：超出上限的旧配置在服务端不会生效，界面也不把它展示为可选项，
 * 保存时按上限收敛，而不是把无效取值继续提交回去。
 */
export function clampToCeiling<T extends string>(
  values: readonly T[] | undefined,
  ceiling: readonly T[] | undefined,
): T[] {
  if (!values || values.length === 0) return [];
  if (!ceiling) return [...values];
  return values.filter((value) => ceiling.includes(value));
}

/** 把未知错误转成可读说明，保留服务端返回的真实原因 */
export function describeRoleError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动。";
    if (error.kind === "timeout") return "请求超时，请稍后重试。";
    if (error.kind === "aborted") return "请求已取消。";
    return error.message;
  }
  return error instanceof Error ? error.message : "未知错误";
}
