/**
 * 代码保存核验操作的唯一状态实现。
 *
 * 三类事实分别保存，互不覆盖：
 *   - 记录列表（由 `useTaskCodeSaves` 维护，本 Hook 不写入）；
 *   - 每条记录的核验结果（本 Hook 的 `entries`，按真实 `record.id` 归档）；
 *   - 请求失败与结果不确定（同一份 entry 内独立字段）。
 *
 * 硬约束：
 *   1. 只有用户显式点击才发起请求：没有任何 effect 会在渲染、列表刷新、自动刷新或任务切换
 *      时调用核验接口；
 *   2. 不自动重发：超时、网络中断与取消之后都由用户决定是否再次核验；
 *   3. 同一记录在途时拒绝重复提交，不同记录的在途状态互不影响；
 *   4. 切换任务后旧任务的所有核验结果立刻失效，迟到的响应一律丢弃。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, verifyCodeSave } from "@/lib/api";
import {
  acceptVerifyResponse,
  beginVerifyRequest,
  createVerifyGate,
  isUncertainVerifyError,
  responseBelongsTo,
  VERIFY_UNKNOWN_OUTCOME_HINT,
  verifyTicketKey,
  type CodeSaveVerifyEntry,
} from "@/pages/tasks/codeSaveVerification";
import { describeApiError } from "@/pages/tasks/useExecutionRecords";

/** Hook 的输入 */
export type UseCodeSaveVerificationOptions = {
  /** 当前打开的任务标识；为空时不发起任何请求 */
  taskID?: string;
  /**
   * 一次核验结束后重新读取记录列表。
   *
   * 列表刷新失败不得抹除已取得的核验结果，因此这里不接收返回值：刷新失败由列表自身的
   * 错误状态呈现，核验结果照常保留。
   */
  refresh?: () => void;
};

/** Hook 的输出 */
export type CodeSaveVerification = {
  /** 按记录标识归档的核验状态；没有发起过核验的记录不在其中 */
  entries: Record<string, CodeSaveVerifyEntry>;
  /** 显式核验一条记录；在途或任务已切换时直接返回，不发送请求 */
  verify: (recordID: string) => Promise<void>;
  /** 清除一条记录的操作提示（保留已取得的核验响应） */
  clearEntry: (recordID: string) => void;
};

/**
 * 管理指定任务的代码保存核验操作。
 *
 * `taskID` 变化会清空全部结果：它们描述的是另一个任务的记录，不能显示在当前详情里。
 */
export function useCodeSaveVerification(
  options: UseCodeSaveVerificationOptions,
): CodeSaveVerification {
  const trimmed = options.taskID?.trim() ?? "";
  const refresh = options.refresh;

  const [entries, setEntries] = useState<Record<string, CodeSaveVerifyEntry>>({});

  /** 当前打开的任务标识：供异步回调判断是否已切换任务 */
  const activeTaskRef = useRef("");
  activeTaskRef.current = trimmed;
  const gateRef = useRef(createVerifyGate());
  /** 同步的在途标记：同一帧内的连续点击不会让 state 及时更新 */
  const runningRef = useRef(new Set<string>());
  /** 卸载标记：卸载后不再用迟到响应更新界面 */
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    // 任务变化（含关闭详情）时旧结果立刻失效：票据与在途标记一起重置，迟到的响应
    // 会因为任务标识不符而被丢弃。
    gateRef.current = createVerifyGate();
    runningRef.current = new Set<string>();
    setEntries({});
  }, [trimmed]);

  /** 只更新指定记录的 entry，其他记录的状态保持不动 */
  const patchEntry = useCallback((recordID: string, patch: Partial<CodeSaveVerifyEntry>) => {
    setEntries((current) => ({
      ...current,
      [recordID]: { ...current[recordID], running: false, ...patch },
    }));
  }, []);

  const verify = useCallback(
    async (recordID: string) => {
      const taskID = activeTaskRef.current;
      const record = recordID?.trim() ?? "";
      if (!taskID || !record) return;
      const key = verifyTicketKey(taskID, record);
      // 同一记录的重复点击直接丢弃，不会发出第二个请求。
      if (runningRef.current.has(key)) return;

      runningRef.current.add(key);
      const ticket = beginVerifyRequest(gateRef.current, taskID, record);
      // 新的核验一开始，上一次的结论就不再适用：只保留在途标记，其余字段清空。
      setEntries((current) => ({ ...current, [record]: { running: true } }));

      const accepted = () =>
        mountedRef.current &&
        acceptVerifyResponse(gateRef.current, ticket, activeTaskRef.current, false);

      try {
        const { data } = await verifyCodeSave(taskID, record);
        if (!accepted()) return;
        if (!responseBelongsTo(data, record)) {
          // 服务端返回的记录不属于本次核验的记录：宁可报错，也不把结论挂到别的记录上。
          patchEntry(record, {
            failure: "服务端返回的核验结果与本次请求的记录不一致，请重新读取记录后重试。",
            running: false,
          });
          return;
        }
        patchEntry(record, { response: data, running: false });
      } catch (cause) {
        if (!accepted()) return;
        const uncertain =
          cause instanceof ApiError && isUncertainVerifyError(cause.kind)
            ? VERIFY_UNKNOWN_OUTCOME_HINT
            : undefined;
        patchEntry(record, { failure: describeApiError(cause), running: false, uncertain });
      } finally {
        runningRef.current.delete(key);
      }

      // 请求真正结束后重新读取一次记录列表：服务端可能已经写回核验结论。
      // 刷新失败不影响上面已经取得的核验结果。
      if (!mountedRef.current || activeTaskRef.current !== taskID) return;
      refresh?.();
    },
    [patchEntry, refresh],
  );

  const clearEntry = useCallback((recordID: string) => {
    setEntries((current) => {
      const existing = current[recordID];
      if (!existing) return current;
      const next = { ...current };
      if (existing.response) {
        // 只清除提示，保留已取得的核验响应。
        next[recordID] = { response: existing.response, running: existing.running };
      } else {
        delete next[recordID];
      }
      return next;
    });
  }, []);

  return { clearEntry, entries, verify };
}
