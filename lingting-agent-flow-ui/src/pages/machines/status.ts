/**
 * 执行节点校验状态的展示元数据。
 *
 * 状态取值直接来自 Go 端 `domain.ValidationStatus` 与 `domain.CheckStatus`，
 * 这里只补充中文标签与颜色，不新增状态、不改变语义，也不把 `checking` 显示为成功。
 */
import type { Host, ValidationCheckStatus, ValidationStatus } from "@/lib/api";

/** 总体状态展示信息 */
export type ValidationStatusMeta = {
  /** 中文标签 */
  label: string;
  /** 标签颜色，与 Ant Design 的语义色一致 */
  color: string;
  /** 是否允许用于真实远程任务执行 */
  usable: boolean;
};

/** 三种总体状态的展示元数据，覆盖 Go 端的每一个取值 */
export const VALIDATION_STATUS_META: Record<ValidationStatus, ValidationStatusMeta> = {
  checking: { color: "processing", label: "校验中", usable: false },
  normal: { color: "success", label: "正常", usable: true },
  abnormal: { color: "error", label: "异常", usable: false },
};

/** 状态筛选与字典的选项，顺序与状态定义一致 */
export const VALIDATION_STATUS_OPTIONS = Object.entries(VALIDATION_STATUS_META).map(
  ([value, meta]) => ({
    label: meta.label,
    value: value as ValidationStatus,
  }),
);

/**
 * 读取总体状态元数据。
 *
 * 未知状态原样展示为不可用：服务端返回了本版本不认识的状态时，不能猜测它可用。
 */
export function validationStatusMeta(status: ValidationStatus): ValidationStatusMeta {
  return (
    VALIDATION_STATUS_META[status] ?? {
      color: "default",
      label: String(status),
      usable: false,
    }
  );
}

/** 单项检查状态的展示元数据 */
export const CHECK_STATUS_META: Record<ValidationCheckStatus, { label: string; color: string }> = {
  passed: { color: "success", label: "通过" },
  failed: { color: "error", label: "失败" },
  timed_out: { color: "warning", label: "超时" },
  skipped: { color: "default", label: "已跳过" },
  not_run: { color: "default", label: "未执行" },
};

/** 读取单项检查状态元数据；未知取值原样展示 */
export function checkStatusMeta(status: ValidationCheckStatus): { label: string; color: string } {
  return CHECK_STATUS_META[status] ?? { color: "default", label: String(status) };
}

/** 校验项的中文标题，与 Go 端的检查项名称对应 */
export const VALIDATION_CHECK_TITLES: Record<string, string> = {
  ssh_connection: "SSH 连接",
  docker_version: "Docker 版本与 API 兼容性",
  docker_cli_help: "Docker CLI 帮助信息",
  base_image_metadata: "基础镜像元数据",
};

/**
 * 是否允许对节点执行远程任务。
 *
 * 只信任服务端返回的 `usable_for_remote_task`：该字段由后端按与准入校验相同的规则
 * 计算，前端不得自行推断，也不得在本地改写状态绕过限制。
 */
export function canUseForRemoteTask(host: Host): boolean {
  return host.usable_for_remote_task === true;
}

/** 是否允许触发重新校验：校验进行中时不需要重复触发 */
export function canRevalidate(host: Host): boolean {
  return host.validation_status !== "checking";
}

/** 是否允许删除：删除只影响本机记录，服务端会拒绝仍有未结束任务的节点 */
export function canDeleteHost(host: Host): boolean {
  return host.validation_status !== "checking";
}
