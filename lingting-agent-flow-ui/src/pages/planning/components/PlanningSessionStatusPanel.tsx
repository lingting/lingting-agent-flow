/**
 * 会话状态区：服务端状态、当前阶段说明与显式动作入口。
 *
 * 展示原则：
 * - 状态与阶段说明都取自服务端记录，页面不推断"应该已经成功"；
 * - 「正在执行」只在页面确实持有在途请求时显示，服务端仍为 analyzing 但没有在途请求时
 *   必须显示为需要人工确认的现场；
 * - 重试与放弃都是显式动作，执行权与状态迁移由服务端判定。
 */
import { Alert, Flex, Space, Tag, Tooltip, Typography } from "antd";
import { Button } from "lingting-react-ui";

import type { PlanningSession } from "@/lib/api";
import type {
  PlanningRunAction,
  PlanningRunState,
} from "@/pages/planning/hooks/usePlanningAnalysisRun";
import {
  formatPlanningTime,
  planningStatusColor,
  planningStatusLabel,
  type PlanningCapabilities,
  type PlanningErrorFacts,
  type PlanningStageDescription,
} from "@/pages/planning/planning";

/** 阶段说明对应的提示类型 */
function stageAlertType(stage: PlanningStageDescription): "error" | "info" | "success" | "warning" {
  switch (stage.key) {
    case "failed":
      return "error";
    case "preview_ready":
      return "success";
    case "server_analyzing":
    case "unknown":
      return "warning";
    default:
      return "info";
  }
}

/** 动作失败提示的展示类型：冲突与不确定都用警告，避免显示成模型执行失败 */
function errorAlertType(error: PlanningErrorFacts): "error" | "warning" {
  return error.conflict || error.uncertain ? "warning" : "error";
}

/** 会话状态面板 */
export function PlanningSessionStatusPanel(props: {
  session: PlanningSession;
  stage: PlanningStageDescription;
  capabilities: PlanningCapabilities;
  run: PlanningRunState;
  reloading: boolean;
  /** 恢复 / 重试的前置条件未满足时的原因 */
  resumeBlockedReason?: string;
  onReload: () => void;
  onResume: () => void;
  onAbandon: () => void;
}) {
  const running: PlanningRunAction | undefined = props.run.running;
  const busy = running !== undefined;
  const resumeLabel = props.session.status === "failed" ? "重试分析" : "恢复分析";
  return (
    <Flex gap="middle" vertical>
      <Space align="center" size={8} wrap>
        <Typography.Text strong>规划会话</Typography.Text>
        <Typography.Text code>{props.session.id}</Typography.Text>
        <Tag color={planningStatusColor(props.session.status)}>
          {planningStatusLabel(props.session.status)}
        </Tag>
        <Typography.Text type="secondary">
          创建 {formatPlanningTime(props.session.created_at)} · 最后更新{" "}
          {formatPlanningTime(props.session.updated_at)}
        </Typography.Text>
      </Space>

      <Alert
        description={props.stage.detail}
        showIcon
        title={props.stage.label}
        type={stageAlertType(props.stage)}
      />

      {props.run.error && (
        <Alert
          closable
          description={props.run.error.message}
          showIcon
          title={props.run.error.uncertain ? "分析结果无法确认" : "本次操作没有成功"}
          type={errorAlertType(props.run.error)}
        />
      )}
      {props.run.refreshError && (
        <Alert
          description={props.run.refreshError}
          showIcon
          title="刷新服务端状态失败"
          type="warning"
        />
      )}

      <Space size={8} wrap>
        <Button
          disabled={busy}
          loading={props.reloading}
          onClick={props.onReload}
          size="small"
          text="重新读取服务端状态"
        />
        {props.capabilities.canResumeAnalysis && (
          <Tooltip title={props.resumeBlockedReason}>
            <Button
              disabled={busy || Boolean(props.resumeBlockedReason)}
              loading={running === "resume"}
              onClick={props.onResume}
              size="small"
              text={resumeLabel}
              type="primary"
            />
          </Tooltip>
        )}
        {props.capabilities.canAbandon && (
          <Button
            confirm={{
              cancelText: "取消",
              description:
                "放弃只把会话标记为 abandoned：消息、方案版本与分析记录全部保留，之后不能继续追加需求。",
              okText: "确认放弃",
              title: "确认放弃本次规划？",
            }}
            danger
            disabled={busy}
            loading={running === "abandon"}
            onConfirm={props.onAbandon}
            size="small"
            text="放弃规划"
          />
        )}
        {running === "analyze" && (
          <Typography.Text type="secondary">
            正在等待服务端完成两阶段分析；这次请求不设客户端超时，也不会因为切换页面被取消。
          </Typography.Text>
        )}
      </Space>

      {!props.capabilities.known && (
        <Typography.Text type="secondary">
          未知状态不开放写操作：页面不会把它当作成功、失败或可继续编辑的会话。
        </Typography.Text>
      )}
      {props.capabilities.known && !props.capabilities.writable && (
        <Typography.Text type="secondary">
          当前状态不开放普通消息编辑：本阶段只提供只读展示。
        </Typography.Text>
      )}
    </Flex>
  );
}
