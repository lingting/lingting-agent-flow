/**
 * 修复周期开启资格的唯一状态实现。
 *
 * 职责边界：
 *   - 只发起 `GET .../repair-cycle-eligibility` 这一条只读请求；
 *   - 不登记周期、不发起执行、不写任何业务状态，也不做自动重试；
 *   - 结论全部交给 `repairCycleEligibility.ts` 的纯函数翻译，本文件不判断业务规则。
 *
 * 竞态与归属约束：
 *   - 请求带「序号 + 任务/步骤归属」票据，切换任务或步骤后旧响应一律丢弃；
 *   - 快照与它的归属一起保存，归属变化立刻作废，避免把上一个步骤的资格用于新步骤；
 *   - 刷新失败保留上一次成功快照并明确标注它可能已过期，但**不允许**据此放行；
 *   - 组件卸载后不再用迟到响应更新状态。
 *
 * 刷新时机：由 `token`（服务端执行相关事实变化）与调用方的显式刷新驱动，不并入
 * 5 秒任务快照循环，也不自建定时器。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import { fetchStepRepairCycleEligibility, type RepairCycleEligibilityResponse } from "@/lib/api";
import {
  acceptResponse,
  beginRequest,
  createRequestGate,
  type RequestGate,
} from "@/pages/tasks/requestGate";
import type { RepairCycleEligibilityState } from "@/pages/tasks/repairCycleEligibility";
import { describeApiError } from "@/pages/tasks/useExecutionRecords";

/** 资格 Hook 的输入 */
export type UseRepairCycleEligibilityOptions = {
  /** 当前打开的任务标识 */
  taskID?: string;
  /** 当前选中的步骤标识 */
  stepID?: string;
  /** 外部刷新令牌：服务端执行相关事实变化时由调用方递增 */
  token?: number;
  /**
   * 额外的失效令牌：例如步骤详情被重新打开。
   *
   * 它与 `token` 语义相同，只为让调用方能在"步骤相关事实按预期已变化但其他字段
   * 未变"的现场下强制重新查询，不改变任何判定规则。
   */
  reloadToken?: number;
};

/** 资格 Hook 的输出 */
export type RepairCycleEligibility = {
  state: RepairCycleEligibilityState;
  /** 重新查询资格；不清除已取得的快照 */
  reload: () => void;
};

/** 归属键：任务与步骤共同决定一份快照属于谁 */
function ownerKey(taskID: string, stepID: string): string {
  return `${taskID}/${stepID}`;
}

/**
 * 管理指定任务与步骤的修复周期开启资格。
 *
 * 缺少任务或步骤标识时不发起查询（`idle`），因此按钮不会被误判为可用。
 */
export function useRepairCycleEligibility(
  options: UseRepairCycleEligibilityOptions,
): RepairCycleEligibility {
  const taskID = options.taskID?.trim() ?? "";
  const stepID = options.stepID?.trim() ?? "";
  const token = options.token;
  const reloadToken = options.reloadToken;
  const owner = ownerKey(taskID, stepID);

  const [state, setState] = useState<RepairCycleEligibilityState>({ status: "idle" });
  const [nonce, setNonce] = useState(0);
  /** 最近一次成功取得的快照及其归属 */
  const snapshotRef = useRef<{ owner: string; data: RepairCycleEligibilityResponse } | undefined>(
    undefined,
  );
  /** 当前归属：供异步回调判断是否已切换任务或步骤 */
  const activeOwnerRef = useRef("");
  activeOwnerRef.current = owner;
  const gateRef = useRef<RequestGate>(createRequestGate());
  /** 卸载标记：卸载后不再用迟到响应更新界面 */
  const mountedRef = useRef(true);

  const reload = useCallback(() => setNonce((value) => value + 1), []);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    // 归属变化时票据重置：旧请求即使返回也不会被采用。
    gateRef.current = createRequestGate();
    if (!taskID || !stepID) {
      snapshotRef.current = undefined;
      setState({ status: "idle" });
      return;
    }
    // 归属不同即立刻作废缓存：它描述的是另一个任务或另一个步骤的资格。
    const kept = snapshotRef.current?.owner === owner ? snapshotRef.current : undefined;
    snapshotRef.current = kept;
    // 有同归属的旧快照时保留它用于说明现场，但标记为"正在重新查询"：判定的纯函数
    // 不会据此放行，因此刷新期间不会出现"用旧结论允许提交"的窗口。
    setState(kept ? { data: kept.data, refreshing: true, status: "ready" } : { status: "loading" });

    const ticket = beginRequest(gateRef.current, owner);
    const controller = new AbortController();
    fetchStepRepairCycleEligibility(taskID, stepID, { signal: controller.signal })
      .then(({ data }) => {
        if (
          !acceptResponse(
            gateRef.current,
            ticket,
            activeOwnerRef.current,
            controller.signal.aborted,
          ) ||
          !mountedRef.current
        ) {
          return;
        }
        snapshotRef.current = { data, owner };
        setState({ data, status: "ready" });
      })
      .catch((cause: unknown) => {
        if (
          !acceptResponse(
            gateRef.current,
            ticket,
            activeOwnerRef.current,
            controller.signal.aborted,
          ) ||
          !mountedRef.current
        ) {
          return;
        }
        const message = describeApiError(cause);
        const previous = snapshotRef.current;
        if (previous && previous.owner === owner) {
          // 刷新失败：保留上一次真实结论并明确标注它不是最新查询，页面据此禁止提交。
          setState({ data: previous.data, error: message, status: "ready" });
          return;
        }
        setState({ error: message, status: "error" });
      });
    return () => controller.abort();
  }, [nonce, owner, reloadToken, stepID, taskID, token]);

  return { reload, state };
}
