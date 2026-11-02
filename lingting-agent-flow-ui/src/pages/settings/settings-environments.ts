/**
 * 设置中心「环境管理」的展示与判定规则。
 *
 * 全部数据来自服务端：环境可用性只认 `version.usable` 与 `host.usable_for_remote_task`，
 * 前端不从版本号、`image_ref`、构建时间或节点名称推断任何结论。这里只补充中文标签、
 * 展示顺序与"是否允许发起写操作"的提示，不改变服务端语义。
 */
import type {
  BuildStatus,
  Environment,
  EnvironmentType,
  EnvironmentVersion,
  Host,
} from "@/lib/api";
import { ApiError } from "@/lib/api";

/** 环境类型的中文标签，取值与 Go 端 `domain.EnvironmentType` 一致 */
const ENVIRONMENT_TYPE_LABEL: Record<EnvironmentType, string> = {
  base: "基础环境",
  language: "语言环境",
};

/** 读取环境类型标签；未知取值原样展示 */
export function environmentTypeLabel(type: string): string {
  return ENVIRONMENT_TYPE_LABEL[type as EnvironmentType] ?? type;
}

/** 构建状态的展示元数据 */
export type BuildStatusMeta = {
  label: string;
  /** 与 Ant Design 语义色一致 */
  color: string;
  /**
   * 是否已有构建正在执行（此时不应重复提交）。
   *
   * 只有 `building` 为真：`pending` 只是"尚未构建"，正是需要发起构建的状态。
   */
  inFlight: boolean;
};

/**
 * 构建状态展示元数据，覆盖 Go 端 `domain.BuildStatus` 的每一个取值。
 *
 * `uncertain` 用警告色而不是错误色：它表达"结果无法确认"，不是"确定失败"。
 */
export const BUILD_STATUS_META: Record<BuildStatus, BuildStatusMeta> = {
  building: { color: "processing", inFlight: true, label: "构建中" },
  failed: { color: "error", inFlight: false, label: "构建失败" },
  pending: { color: "default", inFlight: false, label: "待构建" },
  succeeded: { color: "success", inFlight: false, label: "构建成功" },
  uncertain: { color: "warning", inFlight: false, label: "结果未知" },
};

/** 读取构建状态元数据；未知取值按"不可用且非在途"展示，不猜测成成功 */
export function buildStatusMeta(status: string): BuildStatusMeta {
  return (
    BUILD_STATUS_META[status as BuildStatus] ?? {
      color: "default",
      inFlight: false,
      label: String(status),
    }
  );
}

/** 是否允许再次触发构建：在途构建与已成功版本都不需要重复提交 */
export function canSubmitBuild(version?: EnvironmentVersion): boolean {
  if (!version) return false;
  const meta = buildStatusMeta(version.build_status);
  if (meta.inFlight) return false;
  return version.build_status !== "succeeded";
}

/** 环境展示名称：基础环境没有语言名称，统一显示为 base */
export function environmentLabel(environment: Pick<Environment, "type" | "name">): string {
  if (environment.type === "base") return "base";
  return environment.name;
}

/** 环境版本统计，用于列表摘要；数据全部来自服务端返回的版本记录 */
export type EnvironmentVersionStats = {
  /** 版本总数 */
  total: number;
  /** 服务端标记为可用的版本数 */
  usable: number;
  /** 构建失败或结果未知的版本数 */
  problematic: number;
};

/** 计算环境版本统计 */
export function environmentVersionStats(environment: Environment): EnvironmentVersionStats {
  let usable = 0;
  let problematic = 0;
  for (const version of environment.versions) {
    if (version.usable) usable += 1;
    if (version.build_status === "failed" || version.build_status === "uncertain") {
      problematic += 1;
    }
  }
  return { problematic, total: environment.versions.length, usable };
}

/**
 * 版本展示顺序：按版本号降序。
 *
 * 最新创建的版本排在最前，便于判断"当前可用版本"与"最新版本"的差异；排序只影响展示，
 * 不改变任何可用性判定。
 */
export function sortVersionsNewestFirst(versions: EnvironmentVersion[]): EnvironmentVersion[] {
  return [...versions].sort((left, right) => right.number - left.number);
}

/**
 * 该执行节点是否允许发起环境写操作。
 *
 * 只信任服务端计算的 `usable_for_remote_task`：它由后端按与真实远程操作相同的准入
 * 规则计算。前端禁用按钮只是提示，真正的拒绝发生在服务端。
 */
export function canManageEnvironments(host?: Host): boolean {
  if (!host) return false;
  return host.usable_for_remote_task === true;
}

/** 解释为什么该节点不能发起环境写操作；可用时返回 undefined */
export function environmentWriteBlockedReason(host?: Host): string | undefined {
  if (!host) return "尚未选择执行节点";
  if (canManageEnvironments(host)) return undefined;
  if (host.validation_status === "checking") {
    return "该执行节点的环境预检尚未完成，暂时不能执行远程环境操作。";
  }
  const detail = host.validation_message?.trim();
  if (detail) {
    return `该执行节点不可用（${host.validation_status}）：${detail}`;
  }
  return `该执行节点不可用（${host.validation_status}），请先完成环境预检。`;
}

/** 节点选择项：展示真实的节点名称、地址与可用性，不隐藏不可用节点 */
export type HostSelectOption = {
  value: string;
  label: string;
  disabled: boolean;
  title: string;
};

/** 构造执行节点下拉选项；不可用节点仍然展示并给出原因 */
export function hostSelectOptions(hosts: Host[]): HostSelectOption[] {
  return hosts.map((host) => {
    const reason = environmentWriteBlockedReason(host);
    const state = host.usable_for_remote_task ? "可用于环境操作" : "不可用于环境操作";
    return {
      disabled: false,
      label: `${host.name}（${host.host}:${host.port}）· ${state}`,
      title: reason ?? "该节点可用于环境写操作",
      value: host.id,
    };
  });
}

/** 把未知错误转成可读说明，保留服务端返回的真实原因 */
export function describeEnvironmentError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动。";
    if (error.kind === "timeout") return "请求超时，请稍后重试。";
    if (error.kind === "aborted") return "请求已取消。";
    return error.message;
  }
  return error instanceof Error ? error.message : "未知错误";
}

/**
 * 构建状态失败时的稳定错误码，与 Go 端 `httpapi` 的错误码一致。
 *
 * 它们出现在构建状态查询上：失败与"结果未知"必须分别展示，不能都显示成"构建失败"。
 */
export const ENVIRONMENT_BUILD_FAILED_CODE = "environment_build_failed";
export const ENVIRONMENT_BUILD_UNCERTAIN_CODE = "environment_build_uncertain";
export const ENVIRONMENT_BUILD_IN_PROGRESS_CODE = "environment_build_in_progress";

/** 构建状态查询的结论 */
export type BuildStatusOutcome =
  | { kind: "running"; version: EnvironmentVersion }
  | { kind: "succeeded"; version: EnvironmentVersion }
  | { kind: "failed"; message: string }
  | { kind: "uncertain"; message: string };

/**
 * 把构建状态查询的结果归一为四种结论。
 *
 * 服务端用状态码表达结论：失败与结果未知分别返回 500 + 稳定错误码。这里不把未知错误
 * 压缩成"失败"——`error` 结论由调用方原样展示服务端说明。
 */
export function resolveBuildStatusOutcome(
  result: { version: EnvironmentVersion; in_progress: boolean } | undefined,
  error: unknown,
): BuildStatusOutcome | { kind: "error"; message: string } {
  if (error) {
    if (error instanceof ApiError) {
      if (error.code === ENVIRONMENT_BUILD_FAILED_CODE) {
        return { kind: "failed", message: error.message };
      }
      if (error.code === ENVIRONMENT_BUILD_UNCERTAIN_CODE) {
        return { kind: "uncertain", message: error.message };
      }
    }
    return { kind: "error", message: describeEnvironmentError(error) };
  }
  if (!result) {
    return { kind: "error", message: "构建状态查询没有返回结果。" };
  }
  if (result.version.build_status === "succeeded") {
    return { kind: "succeeded", version: result.version };
  }
  if (result.version.build_status === "failed") {
    return {
      kind: "failed",
      message: result.version.error_summary || "构建失败，服务端未记录原因。",
    };
  }
  if (result.version.build_status === "uncertain") {
    return {
      kind: "uncertain",
      message: result.version.error_summary || "构建结果无法确认，请显式重新构建。",
    };
  }
  return { kind: "running", version: result.version };
}

/** 构建状态是否仍在推进（用于决定是否继续轮询） */
export function buildStillRunning(status: string | undefined): boolean {
  return status === "pending" || status === "building";
}

/** 判断错误是否表示"该环境已有构建在进行"，用于拦截重复提交 */
export function isBuildInProgressError(error: unknown): boolean {
  return error instanceof ApiError && error.code === ENVIRONMENT_BUILD_IN_PROGRESS_CODE;
}

/**
 * 把语言环境名称输入规范化为名称数组。
 *
 * 支持逗号（含中文逗号）与空白分隔；空项被丢弃，重复项由服务端按名称去重，
 * 前端不猜测或补全任何语言名称。
 */
export function parseLanguageNames(raw: string): string[] {
  return raw
    .split(/[\s,，]+/)
    .map((item) => item.trim())
    .filter((item) => item.length > 0);
}
