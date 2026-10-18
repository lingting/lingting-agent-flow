import { createContext, useContext, type ReactNode } from "react";

import {
  useApiConnection,
  type ConnectionState,
  type UseApiConnectionOptions,
} from "./useApiConnection";

/** 连接上下文的值 */
export type ApiConnectionContextValue = {
  /** 连接状态快照 */
  state: ConnectionState;
  /** 立即重新检查 */
  check: () => Promise<void>;
};

const ApiConnectionContext = createContext<ApiConnectionContextValue | undefined>(undefined);

export type ApiConnectionProviderProps = {
  children: ReactNode;
  /** 轮询间隔，0 表示不轮询；测试中传 0 以避免定时器 */
  pollIntervalMs?: number;
};

/**
 * 在全应用内共享同一份连接状态。
 *
 * 头部指示器与页面都读取这里的状态，避免各自发起重复的健康检查并显示互相矛盾的结论。
 */
export function ApiConnectionProvider({ children, pollIntervalMs }: ApiConnectionProviderProps) {
  const options: UseApiConnectionOptions = pollIntervalMs === undefined ? {} : { pollIntervalMs };
  const { check, state } = useApiConnection(options);
  return (
    <ApiConnectionContext.Provider value={{ check, state }}>
      {children}
    </ApiConnectionContext.Provider>
  );
}

/** 读取连接上下文；缺少 Provider 时直接报错，避免静默降级成假状态 */
export function useApiConnectionContext(): ApiConnectionContextValue {
  const value = useContext(ApiConnectionContext);
  if (!value) {
    throw new Error("useApiConnectionContext 必须在 ApiConnectionProvider 内使用");
  }
  return value;
}
