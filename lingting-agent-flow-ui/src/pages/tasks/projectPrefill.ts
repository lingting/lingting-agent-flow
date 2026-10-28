/**
 * 创建任务时从项目继承执行配置。
 *
 * 规则固定为"调用方显式填写的值优先，缺省继承项目"，与 Go 端
 * `app.TaskService.prepareTaskRequest` 的继承顺序一致；这里只负责在表单里预填，
 * 服务端仍是最终权威。
 *
 * 环境与版本只在当前执行节点的环境列表已加载时才预填：跨节点的旧选择会被提交校验
 * 拒绝，提前塞进一个该节点并不存在的环境只会制造必然失败的配置。
 */
import type { Environment, Project } from "@/lib/api";
import { findTaskEnvironment, usableVersions } from "@/pages/tasks/environments";

/** 可以从项目继承到任务表单的字段 */
export type ProjectTaskPrefill = {
  repository_url: string;
  base_branch: string;
  work_dir: string;
  environment: string;
  environment_version: string;
};

/**
 * 依据项目与当前执行节点的环境列表计算方法预填值。
 *
 * 没有项目时返回空对象，表示"不改动表单现有取值"，而不是把字段清空。
 */
export function projectTaskPrefill(input: {
  project?: Project;
  environments: Environment[];
  environmentsLoaded: boolean;
}): Partial<ProjectTaskPrefill> {
  const project = input.project;
  if (!project) return {};
  const prefill: Partial<ProjectTaskPrefill> = {
    base_branch: project.base_branch,
    repository_url: project.repository_url,
    work_dir: project.work_dir,
  };
  if (!input.environmentsLoaded) return prefill;
  const environment = findTaskEnvironment(input.environments, project.environment);
  if (!environment) return prefill;
  const usableVersionsOfEnvironment = usableVersions(environment);
  // 该环境在当前节点没有任何可用版本时不预填：提交校验会直接拒绝，预填只会制造
  // 必然失败的配置，还会掩盖"这个环境暂不可用"这个真实事实。
  if (usableVersionsOfEnvironment.length === 0) return prefill;
  prefill.environment = environment.name;
  const version = project.environment_version?.trim();
  if (!version) return prefill;
  const usable = usableVersionsOfEnvironment.some((item) => item.version === version);
  if (usable) {
    prefill.environment_version = version;
  }
  return prefill;
}
