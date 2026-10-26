/**
 * 创建任务表单的角色查询。
 *
 * 数据来自 `GET /api/v1/roles`：加载中、查询失败、空列表是三种不同状态，
 * 查询失败不会被伪装成"没有角色"，也不会静默回退到硬编码角色。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, fetchRoles, type Role } from "@/lib/api";

/** 角色查询状态 */
export type RoleQueryState =
  /** 首次加载尚未开始或正在加载 */
  | { status: "loading"; roles: Role[] }
  /** 查询成功；列表可能为空，表示数据库里确实没有角色记录 */
  | { status: "ready"; roles: Role[] }
  /** 查询失败；错误来自接口，不是空列表 */
  | { status: "error"; roles: Role[]; error: string };

/** 把未知错误转成可读说明，保留服务端返回的业务提示 */
export function describeRoleQueryError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动。";
    if (error.kind === "timeout") return "角色查询超时，请稍后重试。";
    if (error.kind === "aborted") return "角色查询已取消。";
    return error.message;
  }
  return error instanceof Error ? error.message : "未知错误";
}

/**
 * 查询全部角色。
 *
 * 返回的列表包含已禁用角色，调用方必须自行按 `enabled` 过滤；
 * `status` 是唯一的状态判据，调用方不得把 `error` 当作空列表。
 */
export function useRoles(): RoleQueryState & { reload: () => void } {
  const [state, setState] = useState<RoleQueryState>({ roles: [], status: "loading" });
  const [attempt, setAttempt] = useState(0);
  /** 只接受最近一次请求的结果，避免重试后旧响应覆盖新状态 */
  const requestRef = useRef(0);

  const reload = useCallback(() => {
    setState({ roles: [], status: "loading" });
    setAttempt((value) => value + 1);
  }, []);

  useEffect(() => {
    const requestID = requestRef.current + 1;
    requestRef.current = requestID;
    const controller = new AbortController();
    fetchRoles({ signal: controller.signal })
      .then(({ data }) => {
        if (controller.signal.aborted || requestRef.current !== requestID) return;
        setState({ roles: data.records, status: "ready" });
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted || requestRef.current !== requestID) return;
        setState({ error: describeRoleQueryError(cause), roles: [], status: "error" });
      });
    return () => controller.abort();
  }, [attempt]);

  return { ...state, reload };
}
