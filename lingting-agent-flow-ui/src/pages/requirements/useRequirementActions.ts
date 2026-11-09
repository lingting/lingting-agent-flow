/**
 * 需求的停止与删除动作。
 *
 * 约定：
 * - 停止复用既有任务取消接口（`POST /tasks/{id}/cancel`），只作用于当前需求下真正需要
 *   停止的任务；请求成功后重新读取需求、任务与进度，不用本地状态代替服务端事实；
 * - 删除调用需求聚合删除接口；`409` 表示现场未收敛或并发冲突，页面必须重新读取服务端事实；
 * - 有副作用的请求不自动重试：连接中断或超时只提示"结果未知"并重新查询，是否已删除一律以
 *   重新读取的结果为准；
 * - 需求被真实删除后由页面清理选择与列表，这里只报告真实结果。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import { cancelTask, deleteRequirement, type DeleteRequirementResponse } from "@/lib/api";
import {
  describeRequirementActionError,
  isUnknownRequirementActionOutcome,
} from "@/pages/requirements/requirementActions";

/** 需求停止 / 删除动作的展示状态 */
export type RequirementActionState = {
  /** 是否有停止请求在途 */
  stopping: boolean;
  /** 是否有删除请求在途 */
  deleting: boolean;
  /** 动作失败的说明 */
  error?: string;
  /** 结果未知提示：请求未能确认成功或失败，页面已重新读取服务端状态 */
  unknown?: string;
};

/** 动作结束后页面需要执行的回调 */
export type RequirementActionHandlers = {
  /** 重新读取需求详情、任务与进度；停止与删除失败后都必须调用 */
  reloadFacts: () => Promise<void> | void;
  /** 需求被真实删除：清理选择、列表与相关操作状态 */
  removed: (result: DeleteRequirementResponse) => Promise<void> | void;
};

/** 需求停止与删除的动作入口 */
export function useRequirementActions(
  requirementID: string | undefined,
  handlers: RequirementActionHandlers,
) {
  const [state, setState] = useState<RequirementActionState>({
    deleting: false,
    stopping: false,
  });
  // 回调只在事件触发时读取，避免调用方每次渲染产生新函数导致动作身份漂移。
  const handlersRef = useRef(handlers);
  handlersRef.current = handlers;

  /**
   * 逐个请求取消任务。
   *
   * 顺序发送而不是并发：同一需求内可能仍有一个任务在途，逐个请求让失败点确定，也避免
   * 一次性提交多个有副作用的请求。任一请求失败即停止后续请求，并如实展示错误。
   */
  const stop = useCallback(
    async (taskIDs: string[]): Promise<void> => {
      if (!requirementID || taskIDs.length === 0) return;
      setState({ deleting: false, stopping: true });
      try {
        for (const taskID of taskIDs) {
          await cancelTask(taskID);
        }
        setState({ deleting: false, stopping: false });
      } catch (cause) {
        setState({
          deleting: false,
          error: describeRequirementActionError(cause),
          stopping: false,
          unknown: isUnknownRequirementActionOutcome(cause)
            ? "这次停止请求的结果未知，已重新读取需求状态。"
            : undefined,
        });
      } finally {
        await handlersRef.current.reloadFacts();
      }
    },
    [requirementID],
  );

  /** 删除当前需求；无论成功与否都以服务端重新读取的结果为准 */
  const remove = useCallback(async (): Promise<void> => {
    if (!requirementID) return;
    setState({ deleting: true, stopping: false });
    try {
      const { data } = await deleteRequirement(requirementID);
      setState({ deleting: false, stopping: false });
      await handlersRef.current.removed(data);
    } catch (cause) {
      setState({
        deleting: false,
        error: describeRequirementActionError(cause),
        stopping: false,
        unknown: isUnknownRequirementActionOutcome(cause)
          ? "这次删除请求的结果未知，已重新读取需求状态。"
          : undefined,
      });
      await handlersRef.current.reloadFacts();
    }
  }, [requirementID]);

  // 切换需求时清除上一条需求的错误说明与在途标记：旧状态不得出现在新需求的操作里。
  useEffect(() => {
    setState({ deleting: false, stopping: false });
  }, [requirementID]);

  return { ...state, remove, stop };
}
