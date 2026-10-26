/**
 * 续跑资格与阻塞原因的展示元数据。
 *
 * 这里只做"把服务端事实翻译成中文标签与说明"，不新增状态、不推断资格：
 *
 *   - `can_resume` 是服务端在查询时刻给出的唯一结论，前端不得用任务状态、工作流状态或
 *     代码保存状态推导它；
 *   - `blocking` 是服务端给出的阻塞分类，未知分类必须原样展示，不省略、不归并为普通失败；
 *   - `code_save_action` 只回答"能否再次执行代码保存"，不等于整个工作流可以恢复；
 *   - `next_actions` 是服务端给出的建议文本，不是可自动执行的指令。
 *
 * 取值来源：
 *   - 阻塞分类见 `internal/app/resume_eligibility.go` 的 `blockingReasons` 与 `errBlockingClass`；
 *   - 代码保存动作见 `internal/domain/code_save_recovery.go` 的
 *     `CodeSaveResumeProceed` / `CodeSaveResumeVerify` / `CodeSaveResumeBlocked`。
 */
import type {
  CodeSaveResumeAction,
  ResumeBlockingCode,
  ResumeEligibilityResponse,
} from "@/lib/api";

/** 一条阻塞分类的展示结论 */
export type ResumeBlockingMeta = {
  /** 中文标签；未知分类时为服务端原始代码 */
  label: string;
  /** 该阻塞项的具体含义，用于解释"为什么不能续跑" */
  description: string;
  /** 是否为前端已知分类；false 时不得附加任何业务解释 */
  known: boolean;
};

/**
 * 已知阻塞分类的解释。
 *
 * `task_deleted` 与 `cancel_requested` 来自任务自身状态；四个 `container_*` 来自
 * `containerBlocking` 的真实核验结果；`prerequisite_unmet` 是"无法归类的前置条件失败"
 * 的兜底分类，不能读成"某个具体的容器问题"；两个 `code_save_*` 来自代码保存判定。
 */
export const RESUME_BLOCKING_META: Record<ResumeBlockingCode, ResumeBlockingMeta> = {
  task_deleted: {
    description: "任务已被逻辑删除，不能再次推进工作流。",
    known: true,
    label: "任务已删除",
  },
  cancel_requested: {
    description: "任务已记录取消请求，恢复推进与取消意图冲突，需要先由用户显式处置。",
    known: true,
    label: "任务已请求取消",
  },
  container_missing: {
    description: "任务的执行容器不存在或已被移除，需要在服务端重新准备执行环境。",
    known: true,
    label: "执行容器不存在",
  },
  container_detached: {
    description: "执行容器已解除与任务的关联，不能被自动重新接管。",
    known: true,
    label: "执行容器已解除关联",
  },
  container_not_running: {
    description: "执行容器存在但没有运行，容器内的命令无法执行。",
    known: true,
    label: "执行容器未运行",
  },
  container_unreachable: {
    description: "无法访问执行容器（连接或核验失败），当前无法确认容器可用。",
    known: true,
    label: "无法访问执行容器",
  },
  prerequisite_unmet: {
    description: "服务端判定某项前置条件未满足，但该失败不属于可归类的容器或代码保存问题。",
    known: true,
    label: "必要前置条件未满足",
  },
  code_save_unconfirmed: {
    description: "代码保存记录未能可靠写入，可能落后于真实 Git 状态，必须先做一次只读核验。",
    known: true,
    label: "代码保存记录写入未确认",
  },
  code_save_indeterminate: {
    description: "代码保存仍有结果不明的外部副作用，或已核验但无法取得可信事实，禁止自动重放。",
    known: true,
    label: "代码保存结果不确定",
  },
};

/** 读取阻塞分类的展示元数据；未知分类原样展示，不猜测语义 */
export function resumeBlockingMeta(code: string): ResumeBlockingMeta {
  const known = RESUME_BLOCKING_META[code as ResumeBlockingCode];
  return (
    known ?? {
      description: "服务端返回了当前版本未定义的阻塞分类，请按原始代码排查。",
      known: false,
      label: code,
    }
  );
}

/** 代码保存续跑动作的展示元数据 */
export type ResumeActionMeta = {
  /** 中文标签；未知取值时为服务端原始值 */
  label: string;
  /** 该动作的确切含义 */
  description: string;
  /** 标签颜色，与 Ant Design 的语义色一致 */
  color: string;
  /** 是否为前端已知取值 */
  known: boolean;
  /** 是否需要用户关注（必须先核验或被阻止） */
  attention: boolean;
};

/** 全部代码保存续跑动作，覆盖 Go 端 `domain.CodeSaveResumeAction` 的每一个取值 */
export const CODE_SAVE_RESUME_ACTION_META: Record<CodeSaveResumeAction, ResumeActionMeta> = {
  proceed: {
    attention: false,
    color: "success",
    description: "没有结果不明的外部副作用，可以再次执行代码保存；这不代表整个工作流已经可以恢复。",
    known: true,
    label: "允许继续代码保存",
  },
  verify: {
    attention: true,
    color: "warning",
    description: "必须先做一次只读核验；这是「需要核验」，不是「已经核验」。",
    known: true,
    label: "需先核验",
  },
  blocked: {
    attention: true,
    color: "error",
    description: "已核验但无法取得可信事实，禁止自动重放，必须人工介入。",
    known: true,
    label: "禁止自动推进",
  },
};

/** 代码保存续跑动作缺省时的固定文案：不推断为允许继续 */
export const CODE_SAVE_RESUME_ACTION_MISSING_LABEL = "未提供代码保存判定";

/** 读取代码保存续跑动作的展示元数据；未知取值原样展示 */
export function codeSaveResumeActionMeta(action?: string): ResumeActionMeta {
  if (action === undefined || action === "") {
    return {
      attention: true,
      color: "default",
      description: "服务端没有给出代码保存续跑判定。",
      known: false,
      label: CODE_SAVE_RESUME_ACTION_MISSING_LABEL,
    };
  }
  const known = CODE_SAVE_RESUME_ACTION_META[action as CodeSaveResumeAction];
  return (
    known ?? {
      attention: true,
      color: "default",
      description: "服务端返回了当前版本未定义的代码保存判定，请按原始取值排查。",
      known: false,
      label: action,
    }
  );
}

/** 整体资格结论的展示结论 */
export type ResumeVerdict = {
  /** 面向用户的结论标题 */
  title: string;
  /** 结论说明，只描述服务端结论本身 */
  description: string;
  /** 标签颜色 */
  color: "success" | "warning";
  /** 是否需要用户关注 */
  attention: boolean;
};

/**
 * 把服务端 `can_resume` 翻译为结论文案。
 *
 * 只依据服务端的布尔值：为 true 时也只能表述为"当前查询结果允许继续推进"，因为恢复
 * 动作执行前服务端仍会重新检查前置条件；为 false 时明确说明存在阻塞。
 */
export function resumeVerdict(canResume: boolean): ResumeVerdict {
  if (canResume) {
    return {
      attention: false,
      color: "success",
      description: "这是查询时刻的结论：服务端在真正执行恢复前仍会重新检查前置条件。",
      title: "当前查询结果允许继续推进",
    };
  }
  return {
    attention: true,
    color: "warning",
    description: "服务端判定当前存在阻塞，不能按当前条件直接续跑；请按下方阻塞项与建议处理。",
    title: "当前存在阻塞，不能按当前条件直接续跑",
  };
}

/** 资格摘要缺省时的中性提示：不由前端编造结论 */
export const RESUME_SUMMARY_MISSING = "服务端未提供资格摘要";

/** 读取服务端摘要；为空时返回中性提示 */
export function resumeSummary(response: ResumeEligibilityResponse): string {
  const summary = response.summary?.trim();
  if (summary) return summary;
  return RESUME_SUMMARY_MISSING;
}

/** 服务端阻塞列表；`null` 与缺失都按"没有给出阻塞项"处理 */
export function resumeBlockingList(response: ResumeEligibilityResponse): string[] {
  return response.blocking ?? [];
}

/** 服务端下一步建议；`null` 与缺失都按"没有建议"处理 */
export function resumeNextActions(response: ResumeEligibilityResponse): string[] {
  return response.next_actions ?? [];
}

/**
 * 下一步建议是否为可直接执行的命令。
 *
 * 服务端目前给出的是 CLI 命令文本（例如 `agentflow code verify --record <id>`），它只是
 * 建议，不是前端可以自动执行的指令。这里仅用于在界面上如实标注"需在 CLI 执行"。
 */
export function looksLikeCliCommand(action: string): boolean {
  return /^\s*agentflow\s/.test(action);
}

/**
 * 续跑资格中代码保存记录的核验结论适用范围说明。
 *
 * 与 `codeSaves.recoveryScope` 语义一致，但续跑资格记录没有 `stage` 字段（服务端只输出
 * 需要关注的记录），因此不能复用同一个函数：这里如实说明"服务端未给出该结论对应的阶段"，
 * 避免把记录级核验结论读成三个 Git 阶段都已核验。
 */
export function resumeRecoveryScope(): string {
  return "核验结论只针对服务端记录的某一个阶段；它不代表提交、推送、合并三个阶段都已核验。";
}
