/**
 * 需求工作台页面。
 *
 * 页面只组织面板，不自行实现请求、状态迁移或校验：
 * - 项目、会话、执行节点与消息全部来自服务端；
 * - 消息提交、两阶段分析、重试与放弃经由 `usePlanningWorkbench` 的写操作入口；
 * - Revision 预览只展示落库快照，本阶段不提供确认方案与生成正式任务的入口。
 */
import { Alert, Flex } from "antd";

import { PlanningContextPanel } from "@/pages/planning/components/PlanningContextPanel";
import { PlanningMessageComposer } from "@/pages/planning/components/PlanningMessageComposer";
import { PlanningMessagePanel } from "@/pages/planning/components/PlanningMessagePanel";
import { PlanningRevisionPanel } from "@/pages/planning/components/PlanningRevisionPanel";
import { PlanningSessionStatusPanel } from "@/pages/planning/components/PlanningSessionStatusPanel";
import { usePlanningWorkbench } from "@/pages/planning/hooks/usePlanningWorkbench";
import { describePlanningComposerState, describePlanningStage } from "@/pages/planning/planning";

export function PlanningWorkbenchPage() {
  const workbench = usePlanningWorkbench();
  const running = workbench.run.state.running !== undefined;
  const session = workbench.session;
  const stage = session
    ? describePlanningStage({
        errorSummary: session.error_summary,
        running,
        status: session.status,
      })
    : undefined;
  const composer = describePlanningComposerState({
    hasProject: Boolean(workbench.projectID),
    hasSession: Boolean(session),
    hostBlockedReason: workbench.hostReadiness.blockedReason,
    running,
    status: session?.status ?? "",
  });
  const reloadFacts = () => {
    void workbench.facts.reload();
  };

  return (
    <Flex gap="large" style={{ width: "100%" }} vertical>
      <Alert
        description={
          "选择项目并打开一个规划会话后，提交需求会先追加用户消息，再显式执行两阶段分析" +
          "（需求分析师 → 方案规划师）。分析成功并完成持久化后，页面展示新的方案版本；" +
          "历史版本只读保留。消息提交成功不等于分析成功，失败摘要与冲突都会如实展示，" +
          "页面不自动重试。本阶段不提供方案确认与正式任务生成。"
        }
        showIcon
        title="需求工作台"
        type="info"
      />

      <PlanningContextPanel
        createError={workbench.sessions.createError}
        creating={workbench.sessions.creating}
        hostReadiness={workbench.hostReadiness}
        hosts={workbench.hosts}
        onCreateSession={() => void workbench.createSession()}
        onReloadSessions={workbench.sessions.reload}
        onSelectHost={workbench.selectHost}
        onSelectProject={workbench.selectProject}
        onSelectSession={workbench.selectSession}
        project={workbench.project}
        projectID={workbench.projectID}
        projects={workbench.projects}
        selectedHostID={workbench.selectedHostID}
        sessionHostID={session?.host_id}
        sessionID={workbench.sessionID}
        sessions={workbench.sessions.state}
      />

      {session && stage && (
        <PlanningSessionStatusPanel
          capabilities={workbench.capabilities}
          onAbandon={() => void workbench.abandonSession()}
          onReload={reloadFacts}
          onResume={() => void workbench.resumeAnalysis()}
          reloading={workbench.facts.state.status === "loading"}
          resumeBlockedReason={workbench.hostReadiness.blockedReason}
          run={workbench.run.state}
          session={session}
          stage={stage}
        />
      )}

      <PlanningMessagePanel onReload={reloadFacts} state={workbench.facts.state} />

      <PlanningMessageComposer
        disabled={composer.disabled}
        disabledReason={composer.reason}
        key={workbench.sessionID ?? "no-session"}
        onSubmit={workbench.submitRequirement}
        submitting={running}
      />

      {workbench.facts.state.status === "ready" && (
        <PlanningRevisionPanel revisions={workbench.facts.state.facts.revisions} />
      )}
    </Flex>
  );
}
