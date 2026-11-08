/**
 * 需求进度查询。
 *
 * 进度只来自 `GET /requirements/{id}/progress`：服务端按关联任务的真实状态聚合，前端
 * 不做二次计算，也不使用时间戳或模型输出推断完成度。轮询是只读请求，不会触发执行。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, fetchRequirementProgress, type RequirementProgressResponse } from "@/lib/api";

/** 需求进度查询状态 */
export type RequirementProgressState =
  /** 尚未选择需求，或正在加载 */
  | { status: "loading"; progress?: undefined }
  /** 查询成功 */
  | { status: "ready"; progress: RequirementProgressResponse }
  /** 查询失败；错误来自接口，不是"没有任务" */
  | { status: "error"; progress?: undefined; error: string };

/** 把未知错误转成可读说明 */
export function describeRequirementProgressError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动。";
    if (error.kind === "timeout") return "需求进度查询超时，请稍后重试。";
    if (error.kind === "aborted") return "需求进度查询已取消。";
    return error.message;
  }
  return error instanceof Error ? error.message : "未知错误";
}

/** 查询指定需求的进度；`requirementID` 为空时不发请求 */
export function useRequirementProgress(requirementID?: string) {
  const [state, setState] = useState<RequirementProgressState>({ status: "loading" });
  const requestRef = useRef(0);

  const load = useCallback(async (): Promise<void> => {
    if (!requirementID) {
      setState({ status: "loading" });
      return;
    }
    const requestID = requestRef.current + 1;
    requestRef.current = requestID;
    try {
      const { data } = await fetchRequirementProgress(requirementID);
      if (requestRef.current !== requestID) return;
      setState({ progress: data, status: "ready" });
    } catch (cause) {
      if (requestRef.current !== requestID) return;
      setState({ error: describeRequirementProgressError(cause), status: "error" });
    }
  }, [requirementID]);

  useEffect(() => {
    void load();
    return () => {
      // 卸载后不再接受在途请求的结果。
      requestRef.current += 1;
    };
  }, [load]);

  return { ...state, reload: load };
}
