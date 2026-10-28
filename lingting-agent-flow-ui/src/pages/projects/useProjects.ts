/**
 * 项目列表查询。
 *
 * 数据来自 `GET /api/v1/projects`：加载中、查询失败、空列表是三种不同状态。查询
 * 失败不会被伪装成空列表——项目是任务的必填关联对象，把失败显示成"没有项目"会让
 * 用户以为需要新建项目，而不是去检查服务端。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, fetchProjects, type Project } from "@/lib/api";

/** 项目查询状态 */
export type ProjectQueryState =
  /** 首次加载尚未开始或正在加载 */
  | { status: "loading"; projects: Project[] }
  /** 查询成功；列表可能为空，表示服务端还没有登记任何项目 */
  | { status: "ready"; projects: Project[] }
  /** 查询失败；错误来自接口，不是空列表 */
  | { status: "error"; projects: Project[]; error: string };

/** 把未知错误转成可读说明，保留服务端返回的业务提示 */
export function describeProjectQueryError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动。";
    if (error.kind === "timeout") return "项目查询超时，请稍后重试。";
    if (error.kind === "aborted") return "项目查询已取消。";
    return error.message;
  }
  return error instanceof Error ? error.message : "未知错误";
}

/**
 * 查询全部未删除的项目。
 *
 * `status` 是唯一状态判据：调用方不得把 `error` 当作空列表，也不得在 `loading`
 * 或 `error` 时提交一个前端猜测的项目标识。
 */
export function useProjects(): ProjectQueryState & { reload: () => void } {
  const [state, setState] = useState<ProjectQueryState>({ projects: [], status: "loading" });
  const [attempt, setAttempt] = useState(0);
  /** 只接受最近一次请求的结果，避免重试后旧响应覆盖新状态 */
  const requestRef = useRef(0);

  const reload = useCallback(() => {
    setState({ projects: [], status: "loading" });
    setAttempt((value) => value + 1);
  }, []);

  useEffect(() => {
    const requestID = requestRef.current + 1;
    requestRef.current = requestID;
    const controller = new AbortController();
    fetchProjects({ signal: controller.signal })
      .then(({ data }) => {
        if (controller.signal.aborted || requestRef.current !== requestID) return;
        setState({ projects: data.records, status: "ready" });
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted || requestRef.current !== requestID) return;
        setState({ error: describeProjectQueryError(cause), projects: [], status: "error" });
      });
    return () => controller.abort();
  }, [attempt]);

  return { ...state, reload };
}
