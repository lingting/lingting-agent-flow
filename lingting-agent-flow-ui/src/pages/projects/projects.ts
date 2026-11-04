/**
 * 项目页面的展示与表单取值规则。
 *
 * 这里只做纯函数转换：项目数据全部来自 `GET /api/v1/projects`，可用性、唯一性与
 * 归属校验都在服务端；前端不得自行补齐标识、猜测环境版本或把查询失败当成空列表。
 */
import type { CreateProjectRequest, Host, Project, UpdateProjectRequest } from "@/lib/api";

/** 项目环境版本说明：空值表示使用当前可用版本，不等于某个具体版本 */
export const PROJECT_VERSION_FALLBACK = "当前可用版本";

/** 缓存类型为空时的中性说明 */
export const PROJECT_CACHES_FALLBACK = "未声明";

/** 项目未设置默认执行节点时的中性说明 */
export const PROJECT_DEFAULT_HOST_FALLBACK = "未设置";

/** 返回项目环境的可读描述 */
export function describeProjectEnvironment(project: Project): string {
  const version = project.environment_version?.trim();
  return `${project.environment} · ${version && version.length > 0 ? version : PROJECT_VERSION_FALLBACK}`;
}

/** 返回缓存类型的可读描述 */
export function describeProjectCaches(project: Project): string {
  if (project.caches.length === 0) return PROJECT_CACHES_FALLBACK;
  return project.caches.join(", ");
}

/**
 * 返回项目默认执行节点的可读描述。
 *
 * 节点名称只能来自服务端返回的节点列表，前端不缓存、不猜测；节点查询尚未成功时不
 * 断言"节点不存在"，只展示项目记录里的原始标识。节点已被逻辑删除时标识仍然显示，
 * 使用户能看出项目仍指向一个不再出现于列表中的节点。
 */
export function describeProjectDefaultHost(input: {
  /** 项目记录中的默认节点标识 */
  hostID?: string | null;
  /** 服务端返回的执行节点列表 */
  hosts: Host[];
  /** 节点查询是否已经成功完成 */
  loaded: boolean;
}): string {
  const trimmed = input.hostID?.trim();
  if (!trimmed) return PROJECT_DEFAULT_HOST_FALLBACK;
  const host = input.hosts.find((item) => item.id === trimmed);
  if (host) return `${host.name}（${host.id}）`;
  if (!input.loaded) return trimmed;
  return `${trimmed}（节点未在列表中）`;
}

/** 判断项目记录的默认节点是否不在已加载的节点列表中 */
export function isDefaultHostMissing(input: {
  hostID?: string | null;
  hosts: Host[];
  loaded: boolean;
}): boolean {
  const trimmed = input.hostID?.trim();
  if (!trimmed || !input.loaded) return false;
  return !input.hosts.some((item) => item.id === trimmed);
}

/**
 * 项目下拉选项。
 *
 * 只有服务端真实返回的项目才会成为可选项；已逻辑删除的项目不会出现在列表里，
 * 因此这里不做额外过滤，也不提供"手填项目标识"的入口。
 */
export type ProjectSelectOption = {
  value: string;
  label: string;
};

/** 构造项目下拉选项 */
export function projectSelectOptions(projects: Project[]): ProjectSelectOption[] {
  return projects.map((project) => ({
    label: `${project.name}（${project.id}）`,
    value: project.id,
  }));
}

/** 按标识查找项目 */
export function findProject(projects: Project[], projectID?: string): Project | undefined {
  const trimmed = projectID?.trim();
  if (!trimmed) return undefined;
  return projects.find((project) => project.id === trimmed);
}

/** 项目表单取值 */
export type ProjectFormValues = {
  id: string;
  name: string;
  repository_url: string;
  base_branch: string;
  environment: string;
  environment_version?: string;
  caches?: string;
  work_dir?: string;
  /** 默认执行节点标识；空值表示不设置默认节点 */
  default_host_id?: string;
};

/** 把多行/逗号分隔文本转换为去空的缓存类型数组 */
export function splitCacheTypes(value?: string): string[] | undefined {
  if (!value) return undefined;
  const items = value
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter((item) => item.length > 0);
  return items.length > 0 ? items : undefined;
}

/** 把缓存类型数组转换为表单文本 */
export function joinCacheTypes(caches?: string[]): string | undefined {
  if (!caches || caches.length === 0) return undefined;
  return caches.join("\n");
}

/** 把表单值转换为新增项目请求体 */
export function toCreateProjectRequest(values: ProjectFormValues): CreateProjectRequest {
  const request: CreateProjectRequest = {
    base_branch: values.base_branch.trim(),
    caches: splitCacheTypes(values.caches),
    environment: values.environment.trim(),
    environment_version: values.environment_version?.trim() || undefined,
    id: values.id.trim(),
    name: values.name.trim(),
    repository_url: values.repository_url.trim(),
    work_dir: values.work_dir?.trim() || undefined,
    // 不设置默认节点时省略该字段，由服务端落成 NULL，而不是空字符串。
    default_host_id: values.default_host_id?.trim() || undefined,
  };
  for (const key of Object.keys(request) as (keyof CreateProjectRequest)[]) {
    if (request[key] === undefined) {
      delete request[key];
    }
  }
  return request;
}

/** 把表单值转换为整体更新请求体；项目标识不可修改 */
export function toUpdateProjectRequest(values: ProjectFormValues): UpdateProjectRequest {
  const request: UpdateProjectRequest = {
    base_branch: values.base_branch.trim(),
    caches: splitCacheTypes(values.caches),
    environment: values.environment.trim(),
    environment_version: values.environment_version?.trim() || undefined,
    name: values.name.trim(),
    repository_url: values.repository_url.trim(),
    work_dir: values.work_dir?.trim() || undefined,
    // 更新是整体替换：清空选择时显式提交空字符串，让后端把默认节点落成 NULL，
    // 而不是靠"字段缺失"表达保留旧值。
    default_host_id: values.default_host_id?.trim() ?? "",
  };
  for (const key of Object.keys(request) as (keyof UpdateProjectRequest)[]) {
    if (request[key] === undefined) {
      delete request[key];
    }
  }
  return request;
}

/** 把项目记录转换为表单初值（编辑场景） */
export function toProjectFormValues(project: Project): ProjectFormValues {
  return {
    base_branch: project.base_branch,
    caches: joinCacheTypes(project.caches),
    environment: project.environment,
    environment_version: project.environment_version,
    id: project.id,
    name: project.name,
    repository_url: project.repository_url,
    work_dir: project.work_dir,
    // null 表示项目没有默认节点：表单用空值表达"未设置"，不伪造节点标识。
    default_host_id: project.default_host_id ?? undefined,
  };
}

/** 项目选择的校验结果：成功时给出可以提交的项目标识 */
export type ProjectSelectionResult = { ok: true; project: Project } | { ok: false; error: string };

/**
 * 校验创建任务时的项目选择。
 *
 * 任何一步不成立都返回可读原因，调用方据此阻止提交：不提交空项目、不在项目列表尚未
 * 加载完成时提交，也不把前端记下的标识当成真实记录——任务必须落在服务端已登记的项目上。
 */
export function resolveProjectSelection(input: {
  /** 表单中的项目标识 */
  projectID?: string;
  /** 服务端返回的项目列表 */
  projects: Project[];
  /** 项目查询是否已经成功完成 */
  loaded: boolean;
}): ProjectSelectionResult {
  if (!input.loaded) {
    return { error: "项目数据尚未加载完成，请等待项目查询成功后再创建任务", ok: false };
  }
  const projectID = input.projectID?.trim();
  if (!projectID) {
    return { error: "请选择项目：任务必须关联一个真实存在的项目", ok: false };
  }
  const project = findProject(input.projects, projectID);
  if (!project) {
    return { error: `项目 ${projectID} 不在服务端返回的项目列表中，请重新选择`, ok: false };
  }
  return { ok: true, project };
}
