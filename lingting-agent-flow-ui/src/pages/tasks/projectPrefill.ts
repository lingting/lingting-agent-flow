/**
 * 创建任务时从项目继承执行配置。
 *
 * 规则固定为"调用方显式填写的值优先，缺省继承项目"，与 Go 端
 * `app.TaskService.prepareTaskRequest` 的继承顺序（`firstNonBlank` / `inheritList`）一致；
 * 这里只负责在表单里预填，服务端仍是最终权威。
 *
 * 三条约束：
 *   1. **用户显式设置过的字段不会被覆盖**：切换项目只更新仍在使用项目默认值的字段；
 *   2. **空值等于"未覆盖"**：与 `firstNonBlank` 一致，清空字段表示仍然继承项目配置，
 *      因此清空不会让该字段变成"用户显式设置"；
 *   3. **版本必须属于当前环境**：环境被换掉（或环境不存在）时，旧环境的版本一律清空，
 *      不会把别的环境的版本留在表单里。
 *
 * 环境与版本只在当前执行节点的环境列表已加载时才预填：跨节点的旧选择会被提交校验
 * 拒绝，提前塞进一个该节点并不存在的环境只会制造必然失败的配置。
 */
import type { Environment, Project } from "@/lib/api";
import { findTaskEnvironment, usableVersions } from "@/pages/tasks/environments";

/** 可以由项目默认值填充的表单字段 */
export type ProjectPrefillField =
  | "base_branch"
  | "environment"
  | "environment_version"
  | "repository_url"
  | "work_dir";

/** 需要写入表单的取值；出现 `undefined` 表示必须清空该字段 */
export type ProjectPrefillValues = Partial<Record<ProjectPrefillField, string | undefined>>;

/** 预填结果 */
export type ProjectTaskPrefillResult = {
  /** 要写入表单的取值 */
  values: ProjectPrefillValues;
  /** 本次写入的取值中哪些来自项目默认配置（只表达表单交互事实，不代表服务端最终配置） */
  applied: ProjectPrefillField[];
  /** 项目锁定的环境在当前执行节点不可用时的说明 */
  environmentUnavailable?: string;
};

/** 预填输入 */
export type ProjectTaskPrefillInput = {
  project?: Project;
  environments: Environment[];
  environmentsLoaded: boolean;
  /** 用户显式设置过取值的字段；不传表示全部字段仍使用项目默认值 */
  userOverrides?: readonly ProjectPrefillField[];
  /** 表单当前的环境与版本，用于保证版本始终属于当前生效的环境 */
  current?: { environment?: string; environment_version?: string };
};

/** 由项目默认值填充的文本字段 */
export const PROJECT_TEXT_FIELDS = ["repository_url", "base_branch", "work_dir"] as const;

/** 取值来源说明：来自项目默认配置 */
export function projectDefaultSourceLabel(project: Project): string {
  return `来自项目「${project.name}」的默认配置`;
}

/** 取值来源说明：用户显式设置 */
export const MANUAL_OVERRIDE_LABEL = "已手动设置，不会随项目切换被覆盖";

/** 恢复项目默认值的操作文案 */
export const RESTORE_PROJECT_DEFAULT_LABEL = "恢复项目默认值";

/**
 * 依据项目与当前执行节点的环境列表计算方法预填值。
 *
 * 没有项目时返回空结果，表示"不改动表单现有取值"，而不是把字段清空。
 */
export function projectTaskPrefill(input: ProjectTaskPrefillInput): ProjectTaskPrefillResult {
  const project = input.project;
  if (!project) return { applied: [], values: {} };
  const overrides = new Set(input.userOverrides ?? []);
  const values: ProjectPrefillValues = {};
  const applied: ProjectPrefillField[] = [];
  let environmentUnavailable: string | undefined;

  // 文本字段：用户显式改过的保留，其余跟随项目。
  for (const field of PROJECT_TEXT_FIELDS) {
    if (overrides.has(field)) continue;
    values[field] = project[field];
    applied.push(field);
  }

  const currentVersion = input.current?.environment_version?.trim();
  let effectiveEnvironment = input.current?.environment?.trim();

  // 环境：用户显式选过就不动；否则按当前节点真实可用的环境填入。
  if (!overrides.has("environment") && input.environmentsLoaded) {
    const preferred = findTaskEnvironment(input.environments, project.environment);
    if (preferred && usableVersions(preferred).length > 0) {
      values.environment = preferred.name;
      applied.push("environment");
      effectiveEnvironment = preferred.name;
    } else if (preferred) {
      // 预填一个没有可用版本的环境只会制造必然失败的配置，还会掩盖真实原因。
      environmentUnavailable = `项目环境「${preferred.name}」在当前执行节点没有可用版本，未自动填入`;
    } else {
      environmentUnavailable = `项目环境「${project.environment}」不在当前执行节点的环境列表中，未自动填入`;
    }
  }

  if (!input.environmentsLoaded) return { applied, values };

  const environment = findTaskEnvironment(input.environments, effectiveEnvironment);
  if (!environment) {
    // 没有生效环境就谈不上版本：残留的版本一律清空。
    if (currentVersion) values.environment_version = undefined;
    return { applied, values, environmentUnavailable };
  }

  const usable = usableVersions(environment);
  const projectVersion = project.environment_version?.trim();
  const preferredVersion =
    environment.name === project.environment.trim() &&
    projectVersion &&
    usable.some((item) => item.version === projectVersion)
      ? projectVersion
      : undefined;
  const userChoseVersion = overrides.has("environment_version");
  const environmentRewritten = values.environment !== undefined;

  if (preferredVersion && !userChoseVersion) {
    values.environment_version = preferredVersion;
    applied.push("environment_version");
    return { applied, values, environmentUnavailable };
  }
  if (userChoseVersion || !environmentRewritten) {
    // 保留用户的版本或原有版本，但只在与当前生效环境一致时保留。
    if (currentVersion && !usable.some((item) => item.version === currentVersion)) {
      values.environment_version = undefined;
    }
    return { applied, values, environmentUnavailable };
  }
  // 环境被项目默认值改写，而项目没有给出该环境的可用版本：旧环境的版本不再适用。
  if (currentVersion) values.environment_version = undefined;
  return { applied, values, environmentUnavailable };
}

/**
 * 判断字段当前取值的来源，用于提示"这是项目默认值还是你手动设置的"。
 *
 * 只表达表单交互事实：`applied` 表示该值由项目默认值写入且之后没有被用户改过。
 */
export function projectFieldSourceLabel(input: {
  applied: boolean;
  overridden: boolean;
  project?: Project;
}): string | undefined {
  if (input.overridden) return MANUAL_OVERRIDE_LABEL;
  if (input.applied && input.project) return projectDefaultSourceLabel(input.project);
  return undefined;
}
