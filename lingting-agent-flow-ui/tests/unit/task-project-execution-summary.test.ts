import { describe, expect, it } from "vitest";

import type { Project } from "../../src/lib/api";
import {
  DEFAULT_WORK_DIR_LABEL,
  NO_PROJECT_HINT,
  projectExecutionSummary,
  UNSET_FIELD_LABEL,
} from "../../src/pages/tasks/projectExecutionSummary";
import {
  MANUAL_OVERRIDE_LABEL,
  projectDefaultSourceLabel,
} from "../../src/pages/tasks/projectPrefill";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 一份项目样本 */
function project(overrides: Partial<Project> = {}): Project {
  return {
    base_branch: "main",
    caches: ["go-mod"],
    created_at: "2026-10-28T02:00:00Z",
    environment: "go",
    environment_version: "v0001",
    id: "demo-service",
    name: "示例服务",
    repository_url: "ssh://git.example.com/team/repo",
    updated_at: "2026-10-28T02:00:00Z",
    work_dir: "/workspace/project",
    ...overrides,
  };
}

/** 未选择项目时表单的真实取值：三个字段都为空 */
const emptyCurrent = { base_branch: "", repository_url: "", work_dir: "" };

describe("项目默认执行配置摘要", () => {
  it("未选择项目时如实显示未填写，不给出来自项目的说明", () => {
    const rows = projectExecutionSummary({
      applied: [],
      current: emptyCurrent,
      overridden: [],
      project: undefined,
    });

    expect(rows.map((row) => row.field)).toEqual(["repository_url", "base_branch", "work_dir"]);
    expect(rows[0]?.value).toBe(UNSET_FIELD_LABEL);
    expect(rows[0]?.missing).toBe(true);
    expect(rows[1]?.missing).toBe(true);
    // 工作目录是可选项：空值表示使用默认目录，不属于"缺失"。
    expect(rows[2]?.value).toBe(DEFAULT_WORK_DIR_LABEL);
    expect(rows[2]?.missing).toBe(false);
    expect(rows.every((row) => row.source === undefined)).toBe(true);
    expect(NO_PROJECT_HINT).toContain("尚未选择关联项目");
  });

  it("由项目默认值填入时摘要显示真实取值与来源", () => {
    const sample = project();
    const rows = projectExecutionSummary({
      applied: ["repository_url", "base_branch", "work_dir"],
      current: {
        base_branch: sample.base_branch,
        repository_url: sample.repository_url,
        work_dir: sample.work_dir,
      },
      overridden: [],
      project: sample,
    });

    expect(rows.map((row) => row.value)).toEqual([
      "ssh://git.example.com/team/repo",
      "main",
      "/workspace/project",
    ]);
    expect(rows.every((row) => row.source === projectDefaultSourceLabel(sample))).toBe(true);
    expect(rows.every((row) => !row.missing)).toBe(true);
  });

  it("用户覆盖后摘要显示覆盖值，而不是项目原始值", () => {
    // 摘要必须与表单取值一致：把项目地址显示成任务配置会误导用户。
    const sample = project();
    const rows = projectExecutionSummary({
      applied: ["base_branch", "work_dir"],
      current: {
        base_branch: sample.base_branch,
        repository_url: "ssh://git.example.com/fork/repo",
        work_dir: sample.work_dir,
      },
      overridden: ["repository_url"],
      project: sample,
    });

    expect(rows[0]?.value).toBe("ssh://git.example.com/fork/repo");
    expect(rows[0]?.source).toBe(MANUAL_OVERRIDE_LABEL);
    expect(rows[1]?.source).toBe(projectDefaultSourceLabel(sample));
    expect(rows[0]?.value).not.toBe(sample.repository_url);
  });

  it("必填字段被清空后按未填写提示，且不再声称来自项目", () => {
    // 与 Go 端 firstNonBlank 一致：空值表示未覆盖，项目默认值会在提交时生效；
    // 因此界面只能提示"这里有值需要出现在请求体里"，不能把空输入显示成继承值。
    const sample = project();
    const rows = projectExecutionSummary({
      applied: ["base_branch"],
      current: { base_branch: sample.base_branch, repository_url: "   ", work_dir: "" },
      overridden: [],
      project: sample,
    });

    expect(rows[0]?.value).toBe(UNSET_FIELD_LABEL);
    expect(rows[0]?.missing).toBe(true);
    expect(rows[0]?.source).toBeUndefined();
  });

  it("工作目录被显式清空时仍显示默认目录语义", () => {
    const sample = project();
    const rows = projectExecutionSummary({
      applied: [],
      current: { base_branch: "main", repository_url: sample.repository_url, work_dir: "" },
      overridden: [],
      project: sample,
    });

    expect(rows[2]?.value).toBe(DEFAULT_WORK_DIR_LABEL);
    expect(rows[2]?.missing).toBe(false);
    expect(rows[2]?.source).toBeUndefined();
  });

  it("字段顺序固定，与展开后的字段顺序一致", () => {
    const rows = projectExecutionSummary({
      applied: [],
      current: emptyCurrent,
      overridden: [],
      project: project(),
    });

    expect(rows.map((row) => row.label)).toEqual(["仓库地址", "基础分支", "容器内工作目录"]);
  });
});
