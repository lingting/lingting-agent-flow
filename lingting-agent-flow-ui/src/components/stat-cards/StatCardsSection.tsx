/**
 * 自动加载的值卡片分组（第 2 层组件）。
 *
 * 调用方只提供标题与请求函数：组件在挂载时自动发起一次请求、渲染加载与失败状态，并把
 * 刷新按钮接回同一个请求函数。它不聚合数据、不补 0、不缓存结果，也不共享请求状态——
 * 每个分组各自独立加载，一个分组失败不会影响其他分组。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError } from "@/lib/api";

import { StatCardsGroup } from "./StatCardsGroup";
import type { StatCardItem, StatCardsError } from "./types";

export type StatCardsSectionProps = {
  /** 分组标题 */
  title: string;
  /**
   * 读取分组数据的请求函数。
   *
   * 组件会传入中止信号：请求被取消或组件卸载时调用方必须停止等待，不能把过期响应
   * 当成最新结论。
   */
  request: (signal: AbortSignal) => Promise<StatCardItem[]>;
  /** 值区为空时的说明 */
  emptyText?: string;
  /** 分组附加说明，显示在值区下方 */
  footer?: string;
};

/** 把请求失败转成可读说明；保留服务端返回的业务提示 */
function describeRequestError(cause: unknown): string {
  if (cause instanceof ApiError) {
    switch (cause.kind) {
      case "network":
        return "无法连接 AgentFlow 服务，请确认服务已启动";
      case "timeout":
        return "请求超时，请稍后重试";
      case "aborted":
        return "请求已取消";
      case "invalid_response":
        return "服务返回的内容无法解析，可能不是 AgentFlow API";
      default:
        return cause.message;
    }
  }
  return cause instanceof Error ? cause.message : "加载概览数据失败";
}

/** 自动加载的值卡片分组 */
export function StatCardsSection({ title, request, emptyText, footer }: StatCardsSectionProps) {
  const [items, setItems] = useState<StatCardItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<StatCardsError>();
  const controllerRef = useRef<AbortController | null>(null);
  // 请求函数随页面重渲染会产生新引用：用 ref 保存最新实现，避免重复发起请求。
  const requestRef = useRef(request);

  useEffect(() => {
    requestRef.current = request;
  }, [request]);

  const load = useCallback(async () => {
    controllerRef.current?.abort();
    const controller = new AbortController();
    controllerRef.current = controller;
    setLoading(true);
    setError(undefined);
    try {
      const next = await requestRef.current(controller.signal);
      if (controller.signal.aborted) return;
      setItems(next);
    } catch (cause) {
      if (controller.signal.aborted) return;
      setError({ description: describeRequestError(cause), title: "加载概览数据失败" });
    } finally {
      if (controllerRef.current === controller) {
        controllerRef.current = null;
        setLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    void load();
    return () => {
      controllerRef.current?.abort();
      controllerRef.current = null;
    };
  }, [load]);

  return (
    <StatCardsGroup
      emptyText={emptyText}
      error={error}
      footer={footer}
      items={items}
      loading={loading}
      onRefresh={() => void load()}
      title={title}
    />
  );
}
