/**
 * 任务创建表单的环境选择。
 *
 * 数据全部来自 `GET /api/v1/environments?host_id=…`：可用性（`usable`）与当前可用版本
 * （`current_version`）都由服务端计算，这里只做展示与选择校验，不从版本号、`image_ref`
 * 或构建时间推断可用性，也不把最新创建版本当成当前可用版本。
 *
 * 任务创建接口（`CreateTaskRequest`）只接受语言环境名称与版本标签，因此选中的
 * `type/name` 与版本号在这里被明确映射为 `environment` / `environment_version`；
 * 基础环境（`base`）不能作为任务的语言环境。
 */
import type { BuildStatus, Environment, EnvironmentVersion } from "@/lib/api";

/** 构建状态的中文标签，取值与 Go 端 `domain.BuildStatus` 一一对应 */
export const BUILD_STATUS_LABEL: Record<BuildStatus, string> = {
  building: "构建中",
  failed: "构建失败",
  pending: "待构建",
  succeeded: "构建成功",
};

/** 读取构建状态标签；未知取值原样展示 */
export function buildStatusLabel(status: string): string {
  return BUILD_STATUS_LABEL[status as BuildStatus] ?? status;
}

/** 环境下拉的一项 */
export type EnvironmentSelectOption = {
  value: string;
  label: string;
  disabled: boolean;
  /** 该环境暂不可用的原因，仅在 disabled 时给出 */
  unavailableReason?: string;
};

/** 版本下拉的一项 */
export type VersionSelectOption = {
  value: string;
  label: string;
  disabled: boolean;
  buildStatus: BuildStatus;
  usable: boolean;
  /** 不可用版本的诊断摘要 */
  errorSummary?: string;
};

/** 可用于任务创建的环境：任务配置只接受语言环境 */
export function taskEnvironments(environments: Environment[]): Environment[] {
  return environments.filter((environment) => environment.type === "language");
}

/** 环境的可用版本，顺序沿用服务端返回的版本号升序 */
export function usableVersions(environment?: Environment): EnvironmentVersion[] {
  if (!environment) return [];
  return environment.versions.filter((version) => version.usable);
}

/** 按名称查找语言环境 */
export function findTaskEnvironment(
  environments: Environment[],
  name?: string,
): Environment | undefined {
  const trimmed = name?.trim();
  if (!trimmed) return undefined;
  return taskEnvironments(environments).find((environment) => environment.name === trimmed);
}

/**
 * 构造环境下拉选项。
 *
 * 没有任何可用版本的环境仍然展示（让用户看到真实状态），但被禁用并给出原因：
 * 静默隐藏会让用户以为环境不存在，自动选中一个不可用版本则会制造必然失败的配置。
 */
export function environmentSelectOptions(environments: Environment[]): EnvironmentSelectOption[] {
  return taskEnvironments(environments).map((environment) => {
    const usable = usableVersions(environment);
    const parts = [environment.name];
    if (environment.current_version) {
      parts.push(`当前可用 ${environment.current_version}`);
    } else {
      parts.push("无可用版本");
    }
    if (environment.latest_version && environment.latest_version !== environment.current_version) {
      parts.push(`最新创建 ${environment.latest_version}`);
    }
    if (usable.length === 0) {
      // 不可用原因必须直接出现在选项文本里：只放在 title 属性中用户看不到。
      parts.push("暂不可用于任务启动");
    }
    return {
      disabled: usable.length === 0,
      label: parts.join(" · "),
      unavailableReason:
        usable.length === 0
          ? `环境 ${environment.name} 当前没有可用版本，暂不可用于任务启动`
          : undefined,
      value: environment.name,
    };
  });
}

/** 构造版本下拉选项；不可用版本展示构建状态与诊断摘要，但被禁用 */
export function versionSelectOptions(environment?: Environment): VersionSelectOption[] {
  if (!environment) return [];
  return environment.versions.map((version) => {
    const label = [
      version.version,
      buildStatusLabel(version.build_status),
      version.usable ? "可用" : "不可用",
    ].join(" · ");
    return {
      buildStatus: version.build_status,
      disabled: !version.usable,
      errorSummary: version.usable ? undefined : version.error_summary,
      label,
      usable: version.usable,
      value: version.version,
    };
  });
}

/** 环境选择的校验结果：成功时给出任务创建请求需要的字段 */
export type EnvironmentSelectionResult =
  | { ok: true; environment: string; environment_version: string }
  | { ok: false; error: string };

/**
 * 校验并转换环境选择。
 *
 * 任何一步不成立都返回可读原因，调用方据此阻止提交：不提交空环境、不提交跨节点的
 * 旧选择、不提交构建中或构建失败的版本，也不猜测一个"看起来合适"的版本。
 */
export function resolveEnvironmentSelection(input: {
  /** 表单中的环境名称 */
  name?: string;
  /** 表单中的版本标签 */
  version?: string;
  /** 当前执行节点上的环境列表 */
  environments: Environment[];
  /** 环境查询是否已经成功完成 */
  loaded: boolean;
}): EnvironmentSelectionResult {
  if (!input.loaded) {
    return { error: "环境数据尚未加载完成，请先填写执行节点并等待环境查询成功", ok: false };
  }
  const name = input.name?.trim();
  if (!name) {
    return { error: "请选择语言环境", ok: false };
  }
  const environment = findTaskEnvironment(input.environments, name);
  if (!environment) {
    return {
      error: `所选环境 ${name} 不在当前执行节点的环境列表中，请重新选择`,
      ok: false,
    };
  }
  if (usableVersions(environment).length === 0) {
    return {
      error: `环境 ${environment.name} 当前没有可用版本，暂不可用于任务启动`,
      ok: false,
    };
  }
  const version = input.version?.trim();
  if (!version) {
    return { error: "请选择环境版本", ok: false };
  }
  const matched = environment.versions.find((item) => item.version === version);
  if (!matched) {
    return { error: `版本 ${version} 不属于环境 ${environment.name}，请重新选择`, ok: false };
  }
  if (!matched.usable) {
    return {
      error: `版本 ${version} 当前不可用（构建状态：${buildStatusLabel(
        matched.build_status,
      )}），无法用于任务启动`,
      ok: false,
    };
  }
  return { environment: environment.name, environment_version: matched.version, ok: true };
}
