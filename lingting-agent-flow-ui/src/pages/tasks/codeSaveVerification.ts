/**
 * 代码保存核验（`POST …/code-saves/{recordID}/verify-code-save`）的展示规则与竞态判定。
 *
 * 本模块是纯函数与纯数据：不发起请求、不读写浏览器状态，因此"五组事实互不替代"、
 * "任务切换丢弃迟到响应""未知取值原样保留"等行为都能被直接断言。
 *
 * 四条硬约束（全部来自 `internal/httpapi/code_recovery.go` 与
 * `internal/app/code_save_verify.go` 的真实实现）：
 *   1. `observation.read_failed` 只说明"本次只读核验有没有完成"，不是操作结论；
 *   2. `decision.status` / `decision.stage` 是领域判定，`persisted` 只说明结论有没有落盘；
 *   3. `persisted=true` 不等于核验通过，`persisted=false` 也不代表响应里没有结论；
 *   4. `safe_to_retry` 是服务端判定字段，不能被读成"页面可以自动重试"。
 */
import type {
  CodeSaveDecision,
  CodeSaveObservation,
  CodeSaveRecordView,
  CodeSaveVerifyResponse,
} from "@/lib/api";
import {
  commitStatusMeta,
  mergeStatusMeta,
  persistStatusMeta,
  pushStatusMeta,
  recoveryStatusMeta,
  stageLabel,
  type CodeSaveStatusMeta,
} from "@/pages/tasks/codeSaves";

/** 现场观察的一项：标签、展示值，以及该值是否为"服务端没有返回"的中性占位符 */
export type ObservationRow = {
  key: string;
  label: string;
  value: string;
  /** true 表示服务端没有给出该字段，展示的是中性占位符，不得读成"没有变化" */
  missing: boolean;
};

/** 缺失字段的固定中性说明：不推断"没有变化"，也不推断"远程与本地一致" */
export const OBSERVATION_MISSING_SUFFIX = "（缺失不代表没有变化，需结合记录与判定理解）";

/** 现场观察的三类缺失占位符，各自配一句针对性的中性说明 */
export const OBSERVATION_MISSING_HEAD = `服务端未返回 HEAD commit${OBSERVATION_MISSING_SUFFIX}`;
export const OBSERVATION_MISSING_REMOTE_TASK = `服务端未返回远程任务分支 commit${OBSERVATION_MISSING_SUFFIX}`;
export const OBSERVATION_MISSING_REMOTE_BASE = `服务端未返回远程基础分支 commit${OBSERVATION_MISSING_SUFFIX}`;

/** 判定说明缺失时的固定中性文案 */
export const DECISION_SUMMARY_MISSING = "服务端未提供判定说明";

/**
 * 把 `observation` 展开成可逐行展示的现场事实。
 *
 * `read_failed=true` 时其余字段一律不可信，因此读取失败那一行必须明确说明这一点；
 * 缺失的 commit 字段只展示中性占位符，绝不被改写成"未发生变化"。
 */
export function observationRows(observation: CodeSaveObservation): ObservationRow[] {
  const rows: ObservationRow[] = [
    {
      key: "read_failed",
      label: "只读核验是否失败",
      missing: false,
      value: observation.read_failed
        ? "是：本次只读核验没有完成，其余现场字段不可信"
        : "否：只读查询已完成（这只说明现场可读，不代表操作已经发生）",
    },
  ];
  if (observation.read_error) {
    rows.push({
      key: "read_error",
      label: "读取失败原因",
      missing: false,
      value: observation.read_error,
    });
  }
  rows.push(
    commitRow("head_commit", "当前 HEAD commit", observation.head_commit, OBSERVATION_MISSING_HEAD),
    commitRow(
      "remote_task_commit",
      "远程任务分支 commit",
      observation.remote_task_commit,
      OBSERVATION_MISSING_REMOTE_TASK,
    ),
    commitRow(
      "remote_base_commit",
      "远程基础分支 commit",
      observation.remote_base_commit,
      OBSERVATION_MISSING_REMOTE_BASE,
    ),
  );
  return rows;
}

/** 构造一行 commit 事实：有真实值就展示真实值，缺失时展示中性占位符 */
function commitRow(key: string, label: string, value: string | undefined, missing: string) {
  const trimmed = value?.trim();
  if (!trimmed) {
    return { key, label, missing: true, value: missing };
  }
  return { key, label, missing: false, value: trimmed };
}

/** `safe_to_retry` 的展示结论：保留服务端真实布尔值，并给出准确的业务说明 */
export type SafeToRetryView = {
  /** 服务端返回的原始值；非布尔时是字符串化后的原始内容 */
  raw: string;
  label: string;
  description: string;
  /** 是否需要用户注意（false 与非法值都需要） */
  attention: boolean;
};

/**
 * 解释 `decision.safe_to_retry`。
 *
 * 该字段只回答"重新执行对应操作是否安全"，不构成执行许可：页面不会因此自动提交、推送、
 * 合并或重试，也不会自动再次核验。
 */
export function safeToRetryView(value: boolean | undefined): SafeToRetryView {
  if (value === true) {
    return {
      attention: false,
      description:
        "这只说明服务端确认该阶段的操作没有发生、且记录本身可信；重试仍是有副作用的操作，" +
        "必须由你显式发起，页面不会自动重试。",
      label: "服务端返回 safe_to_retry = true",
      raw: "true",
    };
  }
  if (value === false) {
    return {
      attention: true,
      description:
        "服务端没有确认该操作可以安全重试：现场可能不确定，或记录本身不可信。" +
        "此时重试可能产生重复提交或覆盖远程历史，请先人工核验。",
      label: "服务端返回 safe_to_retry = false",
      raw: "false",
    };
  }
  return {
    attention: true,
    description: "服务端没有返回可用的 safe_to_retry，因此不得据此判断可以安全重试。",
    label: "服务端未返回 safe_to_retry",
    raw: String(value),
  };
}

/** 一次核验判定的展示结论 */
export type DecisionView = {
  /** 判定状态标签元数据；未知取值原样展示且不是成功色 */
  status: CodeSaveStatusMeta;
  /** 判定针对的阶段；缺失时为中性文案，未知取值原样展示 */
  stage: string;
  /** 服务端判定说明；缺失时为中性文案 */
  summary: string;
  safeToRetry: SafeToRetryView;
};

/** 把 `decision` 展开为可逐项展示的判定事实 */
export function decisionView(decision: CodeSaveDecision): DecisionView {
  const summary = decision.summary?.trim();
  return {
    safeToRetry: safeToRetryView(decision.safe_to_retry),
    stage: stageLabel(decision.stage),
    status: recoveryStatusMeta(decision.status),
    summary: summary && summary.length > 0 ? summary : DECISION_SUMMARY_MISSING,
  };
}

/** 结果持久化状态的展示结论 */
export type VerifyPersistView = {
  persisted: boolean;
  type: "success" | "warning";
  title: string;
  description: string;
};

/**
 * 解释 `persisted`。
 *
 * 它只回答"核验结论有没有成功写回数据库"：成功写回的结论也可能是"无法取得可信事实"，
 * 因此不能与"核验通过"混为一谈；未写回时响应里的观察与判定仍然是真实事实。
 */
export function verifyPersistView(persisted: boolean): VerifyPersistView {
  if (persisted) {
    return {
      description:
        "这只说明本次观察与判定已按接口契约写回记录，不代表代码保存成功或核验通过；" +
        "请以上方的现场观察与判定为准。",
      persisted: true,
      title: "核验结论已写入记录",
      type: "success",
    };
  }
  return {
    description:
      "本次观察与判定没有成功写回数据库（可能因为写入冲突或存储失败）。上面的现场观察与判定" +
      "仍然是本次核验取得的真实事实，但记录里的核验结论可能仍是旧值；请用“刷新代码保存”" +
      "重新读取最新记录后再决定下一步。",
    persisted: false,
    title: "核验结论未成功持久化",
    type: "warning",
  };
}

/** 一个记录维度的前后对比行 */
export type RecordComparisonRow = {
  key: string;
  label: string;
  /** 核验前（响应里的 `record`） */
  before: string;
  /** 核验后（响应里的 `updated`） */
  after: string;
  changed: boolean;
};

/**
 * 对比核验前后的记录视图。
 *
 * `record` 与 `updated` 是两个时间点的记录：这里只做逐维度对照，不用 `updated` 去改写
 * 其他记录，也不把字段缺失解释成"状态被清除"。
 */
export function compareRecordSnapshots(
  before: CodeSaveRecordView,
  updated: CodeSaveRecordView,
): RecordComparisonRow[] {
  const rows: Array<{ key: string; label: string; before: string; after: string }> = [
    {
      after: withCommit(commitStatusMeta(updated.commit_status).label, updated.commit_sha),
      before: withCommit(commitStatusMeta(before.commit_status).label, before.commit_sha),
      key: "commit",
      label: "提交",
    },
    {
      after: withCommit(pushStatusMeta(updated.push_status).label, updated.remote_commit_sha),
      before: withCommit(pushStatusMeta(before.push_status).label, before.remote_commit_sha),
      key: "push",
      label: "推送",
    },
    {
      after: withCommit(mergeStatusMeta(updated.merge_status).label, updated.merge_sha),
      before: withCommit(mergeStatusMeta(before.merge_status).label, before.merge_sha),
      key: "merge",
      label: "合并",
    },
    {
      after: persistStatusMeta(updated.persist_status).label,
      before: persistStatusMeta(before.persist_status).label,
      key: "persist",
      label: "记录写入状态",
    },
    {
      after: recoveryStatusMeta(updated.recovery_status).label,
      before: recoveryStatusMeta(before.recovery_status).label,
      key: "recovery",
      label: "核验结论",
    },
  ];
  return rows.map((row) => ({ ...row, changed: row.before !== row.after }));
}

/** 组合状态标签与 commit：有 SHA 时一并展示，缺失时不补造 */
function withCommit(label: string, sha?: string): string {
  const trimmed = sha?.trim();
  return trimmed ? `${label}（${trimmed}）` : label;
}

/** 请求结果不确定时的固定说明：不自动重发，也不声称核验没有发生 */
export const VERIFY_UNKNOWN_OUTCOME_HINT =
  "无法确认服务端是否已完成本次核验并写回结论。请先用“刷新代码保存”重新读取记录，" +
  "确认现场后再由你显式决定是否再次核验；页面不会自动重复提交。";

/** 判断错误是否属于"结果不确定"（超时、网络中断或调用方取消） */
export function isUncertainVerifyError(kind: string): boolean {
  return kind === "timeout" || kind === "network" || kind === "aborted";
}

/**
 * 核验请求的票据。
 *
 * 每条记录各自维护一个序号：同一记录的多次核验只有最后一次生效，不同记录之间互不影响；
 * 任务标识与记录标识一起写进票据，切换任务或记录后迟到的响应一律丢弃。
 */
export type VerifyTicket = { requestID: number; taskID: string; recordID: string };

/** 票据计数器：按记录独立计数 */
export type VerifyGate = { requests: Record<string, number> };

/** 新建票据计数器 */
export function createVerifyGate(): VerifyGate {
  return { requests: {} };
}

/** 票据的键：任务 + 记录 */
export function verifyTicketKey(taskID: string, recordID: string): string {
  return `${taskID}/${recordID}`;
}

/** 发起一次核验：该记录的序号递增并返回本次票据 */
export function beginVerifyRequest(
  gate: VerifyGate,
  taskID: string,
  recordID: string,
): VerifyTicket {
  const key = verifyTicketKey(taskID, recordID);
  const next = (gate.requests[key] ?? 0) + 1;
  gate.requests[key] = next;
  return { recordID, requestID: next, taskID };
}

/**
 * 判断一次核验响应是否仍然有效。
 *
 * 三个条件都必须成立：请求没有被取消、它仍是该记录最后一次发起的请求、它描述的任务仍是
 * 当前打开的任务。任一不成立都必须丢弃，不能覆盖更新的结果。
 */
export function acceptVerifyResponse(
  gate: VerifyGate,
  ticket: VerifyTicket,
  activeTaskID: string | undefined,
  aborted: boolean,
): boolean {
  if (aborted) return false;
  if ((activeTaskID ?? "") !== ticket.taskID) return false;
  const key = verifyTicketKey(ticket.taskID, ticket.recordID);
  return gate.requests[key] === ticket.requestID;
}

/** 一次核验操作在界面上的状态：全部来自真实响应或真实错误 */
export type CodeSaveVerifyEntry = {
  /** 请求是否在途；在途时禁用该记录的重复提交 */
  running: boolean;
  /** 最近一次真实响应；请求失败或刷新失败都不清除它 */
  response?: CodeSaveVerifyResponse;
  /** 请求失败的业务错误说明；此时没有任何核验结论可展示 */
  failure?: string;
  /** 结果不确定（超时 / 网络中断 / 取消）的明确提示 */
  uncertain?: string;
  /** 核验之后重新读取记录列表失败的说明 */
  refreshError?: string;
};

/** 判断一条响应是否属于给定记录 */
export function responseBelongsTo(response: CodeSaveVerifyResponse, recordID: string): boolean {
  return response.record.record_id === recordID || response.updated.record_id === recordID;
}

/** 判断该记录的核验入口是否可用：只在没有在途请求时可再次点击 */
export function canSubmitVerify(entry: CodeSaveVerifyEntry | undefined): boolean {
  return entry?.running !== true;
}
