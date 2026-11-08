/**
 * 需求工作台的页面编排。
 *
 * 这里只做三件事：
 * - 维护页面选择（项目 → 会话 → 执行节点）；
 * - 组合会话列表、会话事实与写操作三个 hook；
 * - 把"切项目即丢弃当前会话上下文"这条规则固定在一处。
 *
 * 会话状态、消息与方案版本都来自服务端事实，页面不缓存也不补造。
 */
import { useCallback, useState } from "react";

import type { PlanningSession } from "@/lib/api";
import {
  planningCapabilities,
  resolvePlanningHost,
  type PlanningCapabilities,
  type PlanningHostReadiness,
} from "@/pages/planning/planning";
import { usePlanningAnalysisRun } from "@/pages/planning/hooks/usePlanningAnalysisRun";
import { usePlanningSessionFacts } from "@/pages/planning/hooks/usePlanningSessionFacts";
import { usePlanningSessions } from "@/pages/planning/hooks/usePlanningSessions";
import { findProject } from "@/pages/projects/projects";
import { useProjects } from "@/pages/projects/useProjects";
import { useHosts } from "@/pages/tasks/useHosts";

/** 页面选择与派生事实 */
export type PlanningWorkbench = {
  projects: ReturnType<typeof useProjects>;
  hosts: ReturnType<typeof useHosts>;
  sessions: ReturnType<typeof usePlanningSessions>;
  facts: ReturnType<typeof usePlanningSessionFacts>;
  run: ReturnType<typeof usePlanningAnalysisRun>;
  /** 当前选中的项目标识 */
  projectID?: string;
  /** 当前选中的规划会话标识 */
  sessionID?: string;
  /** 当前会话记录；会话尚未加载完成时为 undefined */
  session?: PlanningSession;
  /** 当前项目记录 */
  project?: ReturnType<typeof findProject>;
  /** 本轮分析使用的执行节点判定 */
  hostReadiness: PlanningHostReadiness;
  /** 用户显式选择的执行节点 */
  selectedHostID?: string;
  /** 当前会话开放的操作入口 */
  capabilities: PlanningCapabilities;
  selectProject: (projectID?: string) => void;
  selectSession: (sessionID?: string) => void;
  selectHost: (hostID?: string) => void;
  createSession: () => Promise<void>;
  /** 提交需求并立即发起本轮分析；返回用户消息是否已由服务端持久化 */
  submitRequirement: (content: string) => Promise<boolean>;
  /** 显式恢复 / 重试当前会话 */
  resumeAnalysis: () => Promise<void>;
  abandonSession: () => Promise<void>;
};

export function usePlanningWorkbench(): PlanningWorkbench {
  const projects = useProjects();
  const hosts = useHosts();
  const [projectID, setProjectID] = useState<string>();
  const [selectedHostID, setSelectedHostID] = useState<string>();
  const [sessionID, setSessionID] = useState<string>();

  const sessions = usePlanningSessions(projectID);
  const facts = usePlanningSessionFacts(sessionID);
  const { reload: reloadFacts } = facts;
  const { reload: reloadSessions } = sessions;

  /**
   * 重新读取服务端事实。
   *
   * 会话列表刷新失败不影响消息与方案版本的展示：两者用不同请求读取，失败原因会被
   * 合并返回，由动作区单独提示。
   */
  const refresh = useCallback(async () => {
    const failures = await reloadFacts();
    reloadSessions();
    return failures;
  }, [reloadFacts, reloadSessions]);

  const run = usePlanningAnalysisRun({ refresh, sessionID });

  const project = findProject(projects.projects, projectID);
  const session = facts.state.status === "ready" ? facts.state.facts.session : undefined;

  const hostReadiness = resolvePlanningHost({
    hostsStatus:
      hosts.status === "ready" ? "ready" : hosts.status === "error" ? "error" : "loading",
    projectDefaultHostID: project?.default_host_id,
    selectedHostID,
    sessionHostID: session?.host_id,
  });

  const capabilities = planningCapabilities({
    running: run.state.running !== undefined,
    status: session?.status ?? "",
  });

  /** 切换项目：清空执行节点选择与当前会话，避免把上一个项目的上下文带进新项目 */
  const selectProject = useCallback((nextProjectID?: string) => {
    setProjectID(nextProjectID?.trim() || undefined);
    setSelectedHostID(undefined);
    setSessionID(undefined);
  }, []);

  const selectSession = useCallback((nextSessionID?: string) => {
    setSessionID(nextSessionID?.trim() || undefined);
  }, []);

  const selectHost = useCallback((nextHostID?: string) => {
    setSelectedHostID(nextHostID?.trim() || undefined);
  }, []);

  const createSession = useCallback(async () => {
    const created = await sessions.create();
    if (created) setSessionID(created.id);
  }, [sessions]);

  const submitRequirement = useCallback(
    async (content: string): Promise<boolean> => {
      if (!capabilities.canAcceptMessage) return false;
      if (hostReadiness.blockedReason) return false;
      return run.submit(content, hostReadiness.hostID);
    },
    [capabilities.canAcceptMessage, hostReadiness.blockedReason, hostReadiness.hostID, run],
  );

  const resumeAnalysis = useCallback(async () => {
    if (!capabilities.canResumeAnalysis) return;
    if (hostReadiness.blockedReason) return;
    await run.resumeAnalysis(hostReadiness.hostID);
  }, [capabilities.canResumeAnalysis, hostReadiness.blockedReason, hostReadiness.hostID, run]);

  const abandonSession = useCallback(async () => {
    if (!capabilities.canAbandon) return;
    await run.abandon();
  }, [capabilities.canAbandon, run]);

  return {
    abandonSession,
    capabilities,
    createSession,
    facts,
    hostReadiness,
    hosts,
    project,
    projectID,
    projects,
    resumeAnalysis,
    run,
    selectHost,
    selectProject,
    selectSession,
    selectedHostID,
    session,
    sessionID,
    sessions,
    submitRequirement,
  };
}
