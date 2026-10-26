/**
 * 创建任务表单的执行节点与角色选择规则。
 *
 * 数据全部来自真实接口：
 *   - 执行节点来自 `GET /api/v1/hosts`（`fetchHosts`）；
 *   - 角色来自 `GET /api/v1/roles`（`fetchRoles`）。
 *
 * 这里只做"哪些记录可以进入选项、选项如何展示、提交前如何校验"的纯函数判断，
 * 不复制后端的业务规则：
 *   - 节点是否可用于远程任务只信任服务端计算的 `usable_for_remote_task`；
 *   - 角色是否可用只看接口返回的 `enabled`，并按 `type` 分类；
 *   - 最终裁决仍在服务端（`ensureRoles`、`resolveHost`、`EnsureHostUsableForRemoteTask`）。
 */
import type { Host, Role, RoleType } from "@/lib/api";

/** 角色类型的展示顺序与中文名称，覆盖 Go 端 `domain.RoleType` 的全部取值 */
export const ROLE_TYPE_META: Record<RoleType, { label: string; description: string }> = {
  planner: { description: "负责生成并持久化计划步骤", label: "规划角色" },
  developer: { description: "负责按步骤实现代码", label: "开发角色" },
  validator: { description: "负责语义审查，只能收紧结论", label: "验证角色" },
};

/** 角色类型的中文标签；未知类型原样展示 */
export function roleTypeLabel(type: string): string {
  return ROLE_TYPE_META[type as RoleType]?.label ?? type;
}

/**
 * 按类型筛选可用于新任务的启用角色。
 *
 * 已禁用角色不进入选项：接口如实返回它们是用于诊断，不代表它们可用于创建任务。
 */
export function enabledRolesOfType(roles: Role[], type: RoleType): Role[] {
  return roles.filter((role) => role.type === type && role.enabled);
}

/** 判断某个角色 ID 是否是当前可用的该类型角色 */
export function isEnabledRoleOfType(roles: Role[], type: RoleType, roleID?: string): boolean {
  const trimmed = roleID?.trim();
  if (!trimmed) return false;
  return enabledRolesOfType(roles, type).some((role) => role.id === trimmed);
}

/** 角色下拉的一项 */
export type RoleSelectOption = {
  value: string;
  label: string;
  /** 是否禁用；接口返回但已禁用的角色会作为禁用选项展示 */
  disabled: boolean;
};

/**
 * 构造某个类型角色的下拉选项。
 *
 * 启用角色按名称展示，模型与执行器作为辅助信息；禁用角色也展示但被禁用，
 * 让用户能看出"角色存在但被禁用"，而不是误以为角色不存在。
 */
export function roleSelectOptions(roles: Role[], type: RoleType): RoleSelectOption[] {
  return roles
    .filter((role) => role.type === type)
    .map((role) => {
      const details = [role.model, role.runner].filter((item) => item.trim().length > 0);
      const suffix = details.length > 0 ? `（${details.join(" · ")}）` : "";
      return {
        disabled: !role.enabled,
        label: role.enabled ? `${role.name}${suffix}` : `${role.name}${suffix} · 已禁用`,
        value: role.id,
      };
    });
}

/**
 * 执行节点不可用的原因。
 *
 * 只根据服务端字段给出可读说明，不在前端重新判断节点是否可用：
 * `usable_for_remote_task` 由后端按与准入校验相同的规则计算。
 */
export function hostUnavailableReason(host: Host): string | undefined {
  if (!host.enabled) return "该执行节点已禁用";
  if (host.usable_for_remote_task) return undefined;
  if (host.validation_status === "checking") return "环境预检尚未完成";
  return host.validation_message?.trim() || "环境预检未通过";
}

/** 判断节点是否可以用于任务创建 */
export function hostSelectable(host: Host): boolean {
  return host.enabled && host.usable_for_remote_task;
}

/** 执行节点下拉的一项 */
export type HostSelectOption = {
  value: string;
  label: string;
  disabled: boolean;
  /** 不可用原因，仅在 disabled 时给出 */
  unavailableReason?: string;
};

/** 节点的地址展示：`用户@主机:端口`，不包含私钥路径等与选择无关的信息 */
export function hostAddress(host: Host): string {
  return `${host.username}@${host.host}:${host.port}`;
}

/**
 * 构造执行节点下拉选项。
 *
 * 不可用节点仍展示（让用户看到真实状态与原因）但被禁用；不展示私钥路径、
 * 指纹等与"选择哪个节点"无关的信息。
 */
export function hostSelectOptions(hosts: Host[]): HostSelectOption[] {
  return hosts.map((host) => {
    const reason = hostUnavailableReason(host);
    const parts = [host.name || host.id, hostAddress(host)];
    if (host.is_default) parts.push("默认节点");
    if (reason) parts.push(reason);
    return {
      disabled: reason !== undefined,
      label: parts.join(" · "),
      unavailableReason: reason,
      value: host.id,
    };
  });
}

/** 可用于任务创建的节点（已启用且通过远程任务准入） */
export function selectableHosts(hosts: Host[]): Host[] {
  return hosts.filter(hostSelectable);
}

/** 按 ID 查找节点 */
export function findHost(hosts: Host[], hostID?: string): Host | undefined {
  const trimmed = hostID?.trim();
  if (!trimmed) return undefined;
  return hosts.find((host) => host.id === trimmed);
}

/** 创建任务时必需的角色类型（验证角色在后端契约中可选） */
export const REQUIRED_ROLE_TYPES: RoleType[] = ["planner", "developer"];

/** 角色选择的校验结果：成功时给出任务创建请求需要的角色标识 */
export type RoleSelectionResult =
  | {
      ok: true;
      planner_role_id: string;
      developer_role_id: string;
      /** 未选择验证角色时缺省，不发送空字符串 */
      validator_role_id?: string;
    }
  | { ok: false; error: string };

/**
 * 校验并转换角色选择。
 *
 * 任何一步不成立都返回可读原因：角色列表未加载、必需角色未选择、所选角色不是
 * 该类型的启用角色、验证角色选了却不是启用的验证角色。不猜测默认角色。
 */
export function resolveRoleSelection(input: {
  roles: Role[];
  /** 角色查询是否已经成功完成 */
  loaded: boolean;
  plannerRoleID?: string;
  developerRoleID?: string;
  validatorRoleID?: string;
}): RoleSelectionResult {
  if (!input.loaded) {
    return { error: "角色数据尚未加载完成，请等待角色查询成功后再创建任务", ok: false };
  }
  for (const type of REQUIRED_ROLE_TYPES) {
    const value = type === "planner" ? input.plannerRoleID : input.developerRoleID;
    if (!value?.trim()) {
      return { error: `请选择${ROLE_TYPE_META[type].label}`, ok: false };
    }
  }
  if (enabledRolesOfType(input.roles, "planner").length === 0) {
    return { error: "当前没有启用的规划角色，无法创建任务", ok: false };
  }
  if (enabledRolesOfType(input.roles, "developer").length === 0) {
    return { error: "当前没有启用的开发角色，无法创建任务", ok: false };
  }
  if (!isEnabledRoleOfType(input.roles, "planner", input.plannerRoleID)) {
    return { error: `所选规划角色不可用，请重新选择`, ok: false };
  }
  if (!isEnabledRoleOfType(input.roles, "developer", input.developerRoleID)) {
    return { error: `所选开发角色不可用，请重新选择`, ok: false };
  }
  const validator = input.validatorRoleID?.trim();
  if (validator && !isEnabledRoleOfType(input.roles, "validator", validator)) {
    return { error: "所选验证角色不可用，请重新选择或留空", ok: false };
  }
  const selection = {
    developer_role_id: input.developerRoleID!.trim(),
    ok: true as const,
    planner_role_id: input.plannerRoleID!.trim(),
  };
  // 未选择验证角色时不产生该键，避免请求体出现无效空字符串。
  return validator ? { ...selection, validator_role_id: validator } : selection;
}

/** 执行节点选择的校验结果：成功时给出任务创建请求需要的节点标识 */
export type HostSelectionResult = { ok: true; host_id: string } | { ok: false; error: string };

/**
 * 校验执行节点选择。
 *
 * 节点必须来自已成功加载的列表、已启用且服务端标记可用于远程任务；未加载、
 * 未选择、不属于列表或不可用都返回可读原因，不提交猜测的节点。
 */
export function resolveHostSelection(input: {
  hosts: Host[];
  loaded: boolean;
  hostID?: string;
}): HostSelectionResult {
  if (!input.loaded) {
    return { error: "执行节点数据尚未加载完成，请等待节点查询成功后再创建任务", ok: false };
  }
  const trimmed = input.hostID?.trim();
  if (!trimmed) {
    return { error: "请选择执行节点", ok: false };
  }
  const host = findHost(input.hosts, trimmed);
  if (!host) {
    return { error: `所选执行节点 ${trimmed} 不在当前节点列表中，请重新选择`, ok: false };
  }
  if (!host.enabled) {
    return { error: `执行节点 ${host.name || host.id} 已被禁用，不能用于创建任务`, ok: false };
  }
  if (!host.usable_for_remote_task) {
    const reason = hostUnavailableReason(host) ?? "环境预检未通过";
    return { error: `执行节点 ${host.name || host.id} 当前不可用：${reason}`, ok: false };
  }
  return { host_id: host.id, ok: true };
}
