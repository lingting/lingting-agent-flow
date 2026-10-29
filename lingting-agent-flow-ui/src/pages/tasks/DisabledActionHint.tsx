/**
 * 禁用操作的原因提示。
 *
 * 禁用按钮的可用性原因必须能被鼠标与键盘同时获得，但两类底层按钮的表现不同：
 *   - `Button`（antd）的 `disabled` 是真正的 DOM 禁用属性，浏览器不会在禁用控件上派发
 *     鼠标事件，直接挂在按钮上的 Tooltip 永远不会打开；
 *   - `LinkButton` 渲染的是 `<a>`，禁用只体现为样式与 `onClick` 置空，元素本身不可聚焦，
 *     键盘用户拿不到提示。
 * 因此在操作不可用时用一层可聚焦的 `<span>` 承载 Tooltip：键盘聚焦与鼠标悬停都能看到同一
 * 条原因，`aria-label` 让读屏软件在聚焦时也能读到"哪个操作、为什么不可用"。antd Tooltip 的
 * 默认 trigger 只有 `hover`，所以这里显式声明 `["hover", "focus"]`，否则键盘聚焦不会打开提
 * 示，等于只有鼠标用户能拿到原因。
 *
 * `reason` 为空表示操作可用：此时不渲染任何包装与提示，避免出现误导性的禁用说明。原因由
 * 调用方按当前任务 / 当前步骤重新计算，切换对象后不会残留旧对象的原因。
 */
import { Tooltip } from "antd";
import type { ReactNode } from "react";

import "@/pages/tasks/DisabledActionHint.css";

export function DisabledActionHint({
  children,
  label,
  reason,
}: {
  /** 操作名称，用于读屏软件说明"哪个操作不可用" */
  label: string;
  /** 不可用原因；为空表示操作可用，此时不渲染任何包装 */
  reason: string;
  children: ReactNode;
}) {
  if (!reason) return <>{children}</>;
  return (
    <Tooltip title={reason} trigger={["hover", "focus"]}>
      <span aria-label={`${label}不可用：${reason}`} className="disabled-action-hint" tabIndex={0}>
        {children}
      </span>
    </Tooltip>
  );
}
