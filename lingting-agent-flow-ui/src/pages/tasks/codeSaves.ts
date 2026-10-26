/**
 * 代码保存记录的展示元数据。
 *
 * 这里只做"把服务端事实翻译成中文标签与提示"，不新增状态、不推断 Git 结果，
 * 也不把多个维度合并成一个总体结论。五个维度必须始终分开：
 *
 *   - `commit_status`：Git 提交做了什么；
 *   - `push_status`：任务分支是否推送到远端；
 *   - `merge_status`：基础分支合并的结果（含"根本没有请求合并"）；
 *   - `persist_status`：这条记录本身是否被可靠写入（与 Git 无关）；
 *   - `recovery_status`：只读核验对 `stage` 指向阶段的结论（与上面两者都不同）。
 *
 * 因此 `unknown` / `pending` / `unconfirmed` / `unverifiable` 一律不得回落到成功
 * 文案；未知取值原样展示，让"服务端新增了状态"始终可见。
 */
import type { DictItem } from "lingting-react-ui";

import type {
  CodeSaveCommitStatus,
  CodeSaveMergeStatus,
  CodeSavePersistStatus,
  CodeSavePushStatus,
  CodeSaveRecord,
  CodeSaveRecoveryStatus,
} from "@/lib/api";

/** 一条展示结论：中文标签、颜色、是否已知取值、是否需要用户注意 */
export type CodeSaveStatusMeta = {
  /** 中文标签；未知取值时为服务端原始值 */
  label: string;
  /** 标签颜色，与 Ant Design 的语义色一致 */
  color: string;
  /** 是否为前端已知取值；false 时不得附加任何语义解释 */
  known: boolean;
  /** 是否需要用户关注（结果不确定、冲突、失败、未核验等） */
  attention: boolean;
};

/** 全部提交状态，覆盖 Go 端 `domain.CommitStatus` 的每一个取值 */
export const CODE_SAVE_COMMIT_META: Record<CodeSaveCommitStatus, CodeSaveStatusMeta> = {
  pending: { attention: false, color: "default", known: true, label: "等待提交" },
  not_attempted: { attention: false, color: "default", known: true, label: "未尝试提交" },
  no_changes: { attention: false, color: "default", known: true, label: "没有可提交的变更" },
  succeeded: { attention: false, color: "success", known: true, label: "提交成功" },
  failed: { attention: true, color: "error", known: true, label: "提交失败" },
  unknown: { attention: true, color: "warning", known: true, label: "提交结果不确定，需核验" },
};

/** 全部推送状态，覆盖 Go 端 `domain.PushStatus` 的每一个取值 */
export const CODE_SAVE_PUSH_META: Record<CodeSavePushStatus, CodeSaveStatusMeta> = {
  pending: { attention: false, color: "default", known: true, label: "等待推送" },
  not_attempted: { attention: false, color: "default", known: true, label: "未尝试推送" },
  succeeded: { attention: false, color: "success", known: true, label: "推送成功" },
  failed: { attention: true, color: "error", known: true, label: "推送失败" },
  conflict: { attention: true, color: "error", known: true, label: "远程存在冲突" },
  unknown: { attention: true, color: "warning", known: true, label: "推送结果不确定，需核验" },
};

/**
 * 全部合并状态，覆盖 Go 端 `domain.MergeStatus` 的每一个取值。
 *
 * `not_requested` 与 `skipped` 都不是失败，也不是已合并：前者表示从未请求合并，
 * 后者表示请求了但没有执行。它们必须与 `succeeded` / `failed` / `conflict` 分开读。
 */
export const CODE_SAVE_MERGE_META: Record<CodeSaveMergeStatus, CodeSaveStatusMeta> = {
  pending: { attention: false, color: "default", known: true, label: "等待合并" },
  not_requested: { attention: false, color: "default", known: true, label: "未请求合并" },
  skipped: { attention: false, color: "default", known: true, label: "未执行合并" },
  succeeded: { attention: false, color: "success", known: true, label: "合并成功" },
  failed: { attention: true, color: "error", known: true, label: "合并失败" },
  conflict: { attention: true, color: "error", known: true, label: "合并冲突" },
  unknown: { attention: true, color: "warning", known: true, label: "合并结果不确定，需核验" },
};

/**
 * 记录写入可信度，覆盖 Go 端 `domain.PersistStatus` 的每一个取值。
 *
 * `unconfirmed` 表示记录可能落后于真实 Git 现场：它不是 Git 失败，也不能被当作
 * "记录完整可信"。
 */
export const CODE_SAVE_PERSIST_META: Record<CodeSavePersistStatus, CodeSaveStatusMeta> = {
  persisted: { attention: false, color: "success", known: true, label: "记录已可靠写入" },
  unconfirmed: { attention: true, color: "warning", known: true, label: "记录写入未确认" },
};

/**
 * 只读核验结论，覆盖 Go 端 `domain.RecoveryStatus` 的每一个取值。
 *
 * `unverified`（尚未核验）与 `unverifiable`（核验过但无法取得可信事实）必须区分，
 * 两者都不得读成成功或失败；结论只针对记录上的 `stage` 那一个阶段。
 */
export const CODE_SAVE_RECOVERY_META: Record<CodeSaveRecoveryStatus, CodeSaveStatusMeta> = {
  unverified: { attention: true, color: "default", known: true, label: "尚未核验" },
  confirmed_applied: { attention: false, color: "success", known: true, label: "已确认操作发生" },
  confirmed_not_applied: {
    attention: false,
    color: "default",
    known: true,
    label: "已确认操作未发生",
  },
  unverifiable: { attention: true, color: "warning", known: true, label: "无法取得可信结论" },
};

/** 记录写入可信度缺省时的固定文案：不推断为已写入 */
export const CODE_SAVE_PERSIST_MISSING_LABEL = "未提供记录写入状态";
/** 核验结论缺省时的固定文案：不推断为已核验 */
export const CODE_SAVE_RECOVERY_MISSING_LABEL = "未提供核验结论";
/** 阶段缺省时的固定文案 */
export const CODE_SAVE_STAGE_MISSING_LABEL = "未记录阶段";

/** 阶段取值到中文标签；取值来自 `domain.CodeSaveRecord.Stage` 的写入点 */
export const CODE_SAVE_STAGE_LABEL: Record<string, string> = {
  commit: "提交阶段",
  push: "推送阶段",
  merge: "合并阶段",
};

/** 提交状态字典，供 DictTag 使用（仅已知取值） */
export const CODE_SAVE_COMMIT_DICT: DictItem<string>[] = toDict(CODE_SAVE_COMMIT_META);
/** 推送状态字典，供 DictTag 使用（仅已知取值） */
export const CODE_SAVE_PUSH_DICT: DictItem<string>[] = toDict(CODE_SAVE_PUSH_META);
/** 合并状态字典，供 DictTag 使用（仅已知取值） */
export const CODE_SAVE_MERGE_DICT: DictItem<string>[] = toDict(CODE_SAVE_MERGE_META);
/** 记录写入状态字典，供 DictTag 使用（仅已知取值） */
export const CODE_SAVE_PERSIST_DICT: DictItem<string>[] = toDict(CODE_SAVE_PERSIST_META);
/** 核验结论字典，供 DictTag 使用（仅已知取值） */
export const CODE_SAVE_RECOVERY_DICT: DictItem<string>[] = toDict(CODE_SAVE_RECOVERY_META);

/** 把元数据映射转换成 DictTag 需要的形状 */
function toDict(meta: Record<string, CodeSaveStatusMeta>): DictItem<string>[] {
  return Object.entries(meta).map(([value, item]) => ({
    label: item.label,
    tagColor: item.color,
    value,
  }));
}

/** 通用查找：已知取值返回元数据，未知或缺失原样展示 */
function lookup(
  meta: Record<string, CodeSaveStatusMeta>,
  value: string | undefined,
  missingLabel: string,
): CodeSaveStatusMeta {
  if (value === undefined || value === "") {
    return { attention: true, color: "default", known: false, label: missingLabel };
  }
  return meta[value] ?? { attention: true, color: "default", known: false, label: value };
}

/** 提交状态展示元数据 */
export function commitStatusMeta(value?: string): CodeSaveStatusMeta {
  return lookup(CODE_SAVE_COMMIT_META, value, "未提供提交状态");
}

/** 推送状态展示元数据 */
export function pushStatusMeta(value?: string): CodeSaveStatusMeta {
  return lookup(CODE_SAVE_PUSH_META, value, "未提供推送状态");
}

/** 合并状态展示元数据 */
export function mergeStatusMeta(value?: string): CodeSaveStatusMeta {
  return lookup(CODE_SAVE_MERGE_META, value, "未提供合并状态");
}

/** 记录写入可信度展示元数据 */
export function persistStatusMeta(value?: string): CodeSaveStatusMeta {
  return lookup(CODE_SAVE_PERSIST_META, value, CODE_SAVE_PERSIST_MISSING_LABEL);
}

/** 核验结论展示元数据 */
export function recoveryStatusMeta(value?: string): CodeSaveStatusMeta {
  return lookup(CODE_SAVE_RECOVERY_META, value, CODE_SAVE_RECOVERY_MISSING_LABEL);
}

/** 阶段中文标签；未知阶段原样展示 */
export function stageLabel(stage?: string): string {
  if (stage === undefined || stage === "") return CODE_SAVE_STAGE_MISSING_LABEL;
  return CODE_SAVE_STAGE_LABEL[stage] ?? stage;
}

/** 合并是否被请求：只有 `merge_enabled` 为 true 才代表用户授权了合并 */
export function mergeRequested(record: CodeSaveRecord): boolean {
  return record.merge_enabled === true;
}

/**
 * 合并状态的补充说明。
 *
 * 合并结果必须结合 `merge_enabled` 一起读：没有请求合并时，`not_requested` 只是
 * 事实陈述，不能显示成失败，也不能暗示"已合并主分支"。
 */
export function mergeSummary(record: CodeSaveRecord): string {
  if (!mergeRequested(record)) {
    return "本次保存没有请求合并基础分支；这不表示合并失败，也不表示已经合并。";
  }
  switch (record.merge_status) {
    case "succeeded":
      return "已按请求合并基础分支。";
    case "failed":
      return "已请求合并但合并失败；基础分支未确认被更新。";
    case "conflict":
      return "合并遇到冲突，需要人工处理；服务端没有自动选择任何一侧。";
    case "unknown":
      return "合并结果不确定，必须核验后才能判断是否已合并。";
    case "skipped":
      return "已请求合并，但本次没有执行合并。";
    case "pending":
      return "等待执行合并。";
    case "not_requested":
      return "记录为未请求合并，与本次保存的授权状态不一致，需人工核验。";
    default:
      return `服务端给出的合并状态 ${record.merge_status} 未在当前版本中定义。`;
  }
}

/**
 * 推送状态的结果提示。
 *
 * `unknown` 必须明确说明"结果尚不确定"，不能表述为失败后可直接重试：未确认的推送
 * 可能已经到达远端，重试会改变远端历史。
 */
export function pushSummary(record: CodeSaveRecord): string | undefined {
  switch (record.push_status) {
    case "unknown":
      return "推送结果尚不确定，可能已经到达远端；必须先核验远程分支，不能直接重试。";
    case "conflict":
      return "远程分支存在冲突，任务分支没有覆盖远端历史。";
    case "failed":
      return "推送失败，任务分支没有确认到达远端。";
    case "not_attempted":
      return "本次记录没有尝试推送。";
    case "pending":
      return "等待推送。";
    default:
      return undefined;
  }
}

/** 合并状态的结果提示（仅不确定与冲突需要额外说明） */
export function mergeWarning(record: CodeSaveRecord): string | undefined {
  if (record.merge_status === "unknown") {
    return "合并结果尚不确定，必须核验后才能判断基础分支是否被更新。";
  }
  return undefined;
}

/**
 * 记录可信度与核验结论的警示文案。
 *
 * 只陈述服务端给出的字段事实，不推断 Git 是否成功。返回空数组表示没有需要额外
 * 提示的内容。
 */
export function recordWarnings(record: CodeSaveRecord): string[] {
  const warnings: string[] = [];
  if (record.persist_status === "unconfirmed") {
    warnings.push("该记录写入未确认，可能落后于真实 Git 现场，不能当作完整可信的记录。");
  }
  if (!isKnown(CODE_SAVE_PERSIST_META, record.persist_status)) {
    warnings.push(`记录写入状态取值 ${record.persist_status} 未在当前版本中定义。`);
  }
  if (record.recovery_status === "unverified") {
    warnings.push("该记录尚未核验，结论仅来自写入时的现场，不代表已经确认。");
  }
  if (record.recovery_status === "unverifiable") {
    warnings.push("核验无法取得可信事实，必须人工核验，不能当作成功或失败。");
  }
  if (!isKnown(CODE_SAVE_RECOVERY_META, record.recovery_status)) {
    warnings.push(`核验结论取值 ${record.recovery_status} 未在当前版本中定义。`);
  }
  return warnings;
}

/**
 * 核验结论的适用阶段说明。
 *
 * 记录里提交、推送、合并依次推进，但恢复结论只有一个字段，语义上属于"第一个仍
 * 待确认的阶段"。因此必须与 `stage` 一起读，不能当作三个阶段都已核验。
 */
export function recoveryScope(record: CodeSaveRecord): string {
  return `核验结论只针对${stageLabel(record.stage)}；它不代表提交、推送、合并三个阶段都已核验。`;
}

/** 判断某个枚举字段是否为前端已知取值 */
function isKnown(meta: Record<string, CodeSaveStatusMeta>, value?: string): boolean {
  return value !== undefined && value !== "" && Object.hasOwn(meta, value);
}

/** 提交状态是否为已知取值 */
export function isKnownCommitStatus(value?: string): value is CodeSaveCommitStatus {
  return isKnown(CODE_SAVE_COMMIT_META, value);
}

/** 推送状态是否为已知取值 */
export function isKnownPushStatus(value?: string): value is CodeSavePushStatus {
  return isKnown(CODE_SAVE_PUSH_META, value);
}

/** 合并状态是否为已知取值 */
export function isKnownMergeStatus(value?: string): value is CodeSaveMergeStatus {
  return isKnown(CODE_SAVE_MERGE_META, value);
}

/** 记录写入状态是否为已知取值 */
export function isKnownPersistStatus(value?: string): value is CodeSavePersistStatus {
  return isKnown(CODE_SAVE_PERSIST_META, value);
}

/** 核验结论是否为已知取值 */
export function isKnownRecoveryStatus(value?: string): value is CodeSaveRecoveryStatus {
  return isKnown(CODE_SAVE_RECOVERY_META, value);
}

/**
 * 该记录是否需要用户注意。
 *
 * 只依据五个维度里"不确定 / 失败 / 冲突 / 未确认"的事实，不从任务状态或步骤结果
 * 推断代码保存是否完整。
 */
export function recordNeedsAttention(record: CodeSaveRecord): boolean {
  return (
    commitStatusMeta(record.commit_status).attention ||
    pushStatusMeta(record.push_status).attention ||
    mergeStatusMeta(record.merge_status).attention ||
    persistStatusMeta(record.persist_status).attention ||
    recoveryStatusMeta(record.recovery_status).attention
  );
}
