/**
 * 执行类操作的统一调用与状态管理。
 *
 * 设计约束：
 *   - 每个动作独立记录"正在执行哪个目标"，同一时刻只允许一个动作在途，重复点击
 *     不会发出第二个请求；
 *   - 成功结论只来自服务端响应字段，请求失败时展示服务端返回的业务错误，不伪造成功；
 *   - 动作结束后统一刷新执行现场；刷新失败单独记录，不覆盖已经取得的执行结论；
 *   - 任何动作都不会自动重试：这些操作有真实副作用（容器内命令、Agent 调用、
 *     Git 操作、预算登记），重放必须由用户基于最新状态显式触发。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import {
  ApiError,
  executeAndValidateStep as callExecuteAndValidate,
  executeStep as callExecuteStep,
  executeSteps as callExecuteSteps,
  generatePlan as callGeneratePlan,
  initializeAttemptCode,
  openRepairCycle as callOpenRepairCycle,
  validateAttempt as callValidateAttempt,
  type Attempt,
  type ExecuteAndValidateStepRequest,
  type ExecuteStepRequest,
  type ExecuteStepsRequest,
  type InitializeCodeRequest,
  type PlanRequest,
  type Step,
  type ValidateAttemptRequest,
} from "@/lib/api";
import {
  describeExecuteAndValidateResult,
  describeExecuteStepResult,
  describeExecuteStepsResult,
  describeInitializeResult,
  describeOpenRepairCycleResult,
  describePlanResult,
  describeValidateResult,
  executeAndValidateResultFromBody,
  isUncertainOperationError,
  OPERATION_UNKNOWN_OUTCOME_HINT,
  type OperationOutcome,
} from "@/pages/tasks/steps";
import { describeApiError } from "@/pages/tasks/useExecutionRecords";

/** 可发起的执行类动作 */
export type OperationKey =
  | "plan"
  | "execute-step"
  | "execute-steps"
  | "execute-and-validate"
  | "validate"
  | "initialize-code"
  | "open-repair-cycle";

/** 一次动作的目标：用于把结论与具体步骤 / 尝试对应起来 */
export type OperationTarget = {
  key: OperationKey;
  stepID?: string;
  attemptID?: string;
};

/**
 * 一次失败的展示信息。
 *
 * `facts` 只在服务端确实在失败响应中给出了结构化执行事实（`operation_result`）时存在，
 * 逐条说明这次执行真实发生了什么。它与 `message` 一起展示，但不会改变"这次操作没有
 * 成功"的结论，也不能被读作验证通过。
 */
export type OperationFailure = OperationTarget & {
  message: string;
  facts?: string[];
};

/** 动作区的完整状态 */
export type ExecutionOperationsState = {
  /** 正在执行的动作；存在时所有动作按钮都应禁用 */
  running?: OperationTarget;
  /** 最近一次成功结论 */
  outcome?: OperationTarget & { result: OperationOutcome };
  /** 最近一次请求失败的业务错误 */
  failure?: OperationFailure;
  /**
   * 请求结果无法确认（超时、网络中断或调用方取消）。
   *
   * 它与 `failure` 同时存在：`failure` 给出底层错误说明，本字段明确"结果不确定，不得重发"，
   * 从而与明确的服务端拒绝区分开来。
   */
  uncertain?: string;
  /** 动作后的现场刷新失败说明；不覆盖上面的结论 */
  refreshError?: string;
};

/** 动作标题，用于按钮与结果面板 */
export const OPERATION_LABEL: Record<OperationKey, string> = {
  plan: "生成计划",
  "execute-step": "执行步骤",
  "execute-steps": "顺序执行",
  "execute-and-validate": "开发—验证闭环",
  validate: "确定性验证",
  "initialize-code": "初始化代码",
  "open-repair-cycle": "开启新执行周期",
};

/**
 * 从失败响应中取出闭环执行事实，并翻译为逐条说明。
 *
 * 只有开发—验证闭环会在失败响应里附加 `operation_result`；其他动作的错误响应没有该
 * 字段，因此显示方式完全不变。超时、网络中断与调用方取消拿不到服务端响应，这里返回
 * `undefined`，由 `uncertain` 单独提示"结果无法确认"。
 */
function failureFactsOf(cause: unknown): string[] | undefined {
  if (!(cause instanceof ApiError)) return undefined;
  const result = executeAndValidateResultFromBody(cause.body);
  if (!result) return undefined;
  return describeExecuteAndValidateResult(result).details;
}

/**
 * 执行类操作的调用入口。
 *
 * `refresh` 由调用方提供：动作结束后必须重新读取步骤、尝试、验证与工作流状态，
 * 返回值是刷新过程中失败的项，用于单独提示。
 */
export function useExecutionOperations(options: {
  taskID?: string;
  refresh: () => Promise<string[]>;
}) {
  const { taskID, refresh } = options;
  const trimmedTaskID = taskID?.trim() ?? "";
  const [state, setState] = useState<ExecutionOperationsState>({});
  /** 同步的在途标记：同一帧内的连续点击不会让 state 及时更新 */
  const runningRef = useRef(false);
  /** 动作票据：任务切换后旧任务的迟到响应一律丢弃，不会归属到新任务 */
  const ticketRef = useRef(0);
  /** 当前打开的任务标识：供异步回调判断是否已切换任务 */
  const activeTaskRef = useRef(trimmedTaskID);
  activeTaskRef.current = trimmedTaskID;
  /** 卸载标记：卸载后不再用迟到响应更新界面 */
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  /**
   * 任务切换（含关闭详情）时，上一个任务的动作结论立刻失效。
   *
   * 票据同时递增：仍在途的请求返回后不会把结论写进新任务的界面，也不会触发一次属于
   * 旧任务的现场刷新。切换任务不等于取消请求，服务端可能已经执行了该操作。
   */
  useEffect(() => {
    ticketRef.current += 1;
    runningRef.current = false;
    setState({});
  }, [trimmedTaskID]);

  const runOperation = useCallback(
    async (target: OperationTarget, action: () => Promise<OperationOutcome>) => {
      if (runningRef.current) return;
      if (!trimmedTaskID) {
        setState({
          failure: { ...target, message: "缺少任务标识，无法发起操作" },
        });
        return;
      }
      const startedFor = trimmedTaskID;
      const ticket = ticketRef.current + 1;
      ticketRef.current = ticket;
      // 任务切换后票据递增且 activeTaskRef 变化，迟到的响应与刷新都会被丢弃。
      const accepted = () =>
        mountedRef.current && ticketRef.current === ticket && activeTaskRef.current === startedFor;
      runningRef.current = true;
      setState({ running: target });
      try {
        const result = await action();
        if (accepted()) setState({ outcome: { ...target, result } });
      } catch (cause) {
        if (accepted()) {
          // 超时、网络中断与取消意味着"服务端可能已经执行"：必须与明确的服务端拒绝区分，
          // 并明确提示不自动重发。
          const uncertain =
            cause instanceof ApiError && isUncertainOperationError(cause.kind)
              ? OPERATION_UNKNOWN_OUTCOME_HINT
              : undefined;
          // 服务端在失败响应里给出结构化执行事实时一并保留：它与错误说明并列展示，
          // 既不会把失败读成成功，也不会因为只显示一条消息而丢掉已经发生的执行细节。
          setState({
            failure: {
              ...target,
              message: describeApiError(cause),
              facts: failureFactsOf(cause),
            },
            uncertain,
          });
        }
      } finally {
        runningRef.current = false;
        // 已切换任务或已卸载时不再刷新：那次刷新属于旧任务的现场。
        if (accepted()) {
          let failures: string[] = [];
          try {
            failures = await refresh();
          } catch (cause) {
            failures = [describeApiError(cause)];
          }
          setState((current) => ({
            ...current,
            refreshError:
              failures.length > 0
                ? `动作已结束，但状态刷新失败：${failures.join("、")}。执行结论仍然有效，请手动刷新确认最新状态。`
                : undefined,
            running: undefined,
          }));
        }
      }
    },
    [refresh, trimmedTaskID],
  );

  const clear = useCallback(() => {
    if (runningRef.current) return;
    setState({});
  }, []);

  const plan = useCallback(
    (body: PlanRequest = {}) =>
      runOperation({ key: "plan" }, async () => {
        const { data } = await callGeneratePlan(taskID ?? "", body);
        return describePlanResult(data);
      }),
    [runOperation, taskID],
  );

  const executeStep = useCallback(
    (step: Step, body: ExecuteStepRequest = {}) =>
      runOperation({ key: "execute-step", stepID: step.id }, async () => {
        const { data } = await callExecuteStep(taskID ?? "", step.id, body);
        return describeExecuteStepResult(data);
      }),
    [runOperation, taskID],
  );

  const executeSteps = useCallback(
    (body: ExecuteStepsRequest = {}) =>
      runOperation({ key: "execute-steps" }, async () => {
        const { data } = await callExecuteSteps(taskID ?? "", body);
        return describeExecuteStepsResult(data);
      }),
    [runOperation, taskID],
  );

  const executeAndValidate = useCallback(
    (step: Step, body: ExecuteAndValidateStepRequest = {}) =>
      runOperation({ key: "execute-and-validate", stepID: step.id }, async () => {
        const { data } = await callExecuteAndValidate(taskID ?? "", step.id, body);
        return describeExecuteAndValidateResult(data);
      }),
    [runOperation, taskID],
  );

  const validate = useCallback(
    (step: Step, attempt: Attempt, body: ValidateAttemptRequest = {}) =>
      runOperation({ attemptID: attempt.id, key: "validate", stepID: step.id }, async () => {
        const { data } = await callValidateAttempt(taskID ?? "", step.id, attempt.id, body);
        return describeValidateResult(data);
      }),
    [runOperation, taskID],
  );

  const initializeCode = useCallback(
    (step: Step, attempt: Attempt, body: InitializeCodeRequest = {}) =>
      runOperation({ attemptID: attempt.id, key: "initialize-code", stepID: step.id }, async () => {
        const { data } = await initializeAttemptCode(taskID ?? "", step.id, attempt.id, body);
        return describeInitializeResult(data);
      }),
    [runOperation, taskID],
  );

  const openRepairCycle = useCallback(
    (step: Step) =>
      runOperation({ key: "open-repair-cycle", stepID: step.id }, async () => {
        const { data } = await callOpenRepairCycle(taskID ?? "", step.id);
        return describeOpenRepairCycleResult(data);
      }),
    [runOperation, taskID],
  );

  return {
    clear,
    executeAndValidate,
    executeStep,
    executeSteps,
    initializeCode,
    openRepairCycle,
    plan,
    state,
    validate,
  };
}

/** 动作 hook 的返回类型，供子组件声明属性 */
export type ExecutionOperations = ReturnType<typeof useExecutionOperations>;
