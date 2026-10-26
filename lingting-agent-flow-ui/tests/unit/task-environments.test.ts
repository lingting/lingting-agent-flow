import { describe, expect, it } from "vitest";

import type { Environment, EnvironmentVersion } from "../../src/lib/api";
import {
  buildStatusLabel,
  environmentSelectOptions,
  findTaskEnvironment,
  resolveEnvironmentSelection,
  taskEnvironments,
  usableVersions,
  versionSelectOptions,
} from "../../src/pages/tasks/environments";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 一份环境版本样本 */
function version(overrides: Partial<EnvironmentVersion> = {}): EnvironmentVersion {
  return {
    build_status: "succeeded",
    created_at: "2026-10-26T08:00:00Z",
    image_ref: "agentflow-language-go:v0001-abcdefabcdef",
    number: 1,
    usable: true,
    version: "v0001",
    ...overrides,
  };
}

/** 一份环境样本 */
function environment(overrides: Partial<Environment> = {}): Environment {
  return {
    latest_version: "v0001",
    name: "go",
    type: "language",
    versions: [version()],
    ...overrides,
  };
}

describe("环境与版本选项", () => {
  it("只把语言环境用于任务创建，基础环境不参与", () => {
    const environments = [
      environment({ name: "base", type: "base" }),
      environment({ name: "go" }),
      environment({ name: "node" }),
    ];

    expect(taskEnvironments(environments).map((item) => item.name)).toEqual(["go", "node"]);
  });

  it("多个环境与多个版本都来自真实数据", () => {
    const environments = [
      environment({
        current_version: "v0002",
        latest_version: "v0003",
        name: "go",
        versions: [
          version({ number: 1, usable: false, version: "v0001" }),
          version({ number: 2, version: "v0002" }),
          version({ build_status: "building", number: 3, usable: false, version: "v0003" }),
        ],
      }),
      environment({ latest_version: "v0001", name: "node" }),
    ];

    const options = environmentSelectOptions(environments);
    expect(options.map((option) => option.value)).toEqual(["go", "node"]);
    expect(options[0]?.label).toContain("当前可用 v0002");
    expect(options[0]?.label).toContain("最新创建 v0003");
    expect(options[0]?.disabled).toBe(false);

    const versions = versionSelectOptions(environments[0]);
    expect(versions.map((option) => option.value)).toEqual(["v0001", "v0002", "v0003"]);
    expect(versions.map((option) => option.disabled)).toEqual([true, false, true]);
    expect(versions[2]?.buildStatus).toBe("building");
  });

  it("current_version 与 latest_version 不同时不把最新版本当成可用版本", () => {
    const env = environment({
      current_version: "v0001",
      latest_version: "v0002",
      versions: [
        version({ number: 1, version: "v0001" }),
        version({ build_status: "failed", number: 2, usable: false, version: "v0002" }),
      ],
    });

    expect(usableVersions(env).map((item) => item.version)).toEqual(["v0001"]);
    expect(environmentSelectOptions([env])[0]?.label).toContain("当前可用 v0001");
    expect(environmentSelectOptions([env])[0]?.label).toContain("最新创建 v0002");
  });

  it("没有可用版本的环境仍然展示但被禁用并给出原因", () => {
    const env = environment({
      current_version: undefined,
      latest_version: "v0002",
      versions: [
        version({
          build_status: "failed",
          error_summary: "镜像核实未通过",
          number: 2,
          usable: false,
          version: "v0002",
        }),
      ],
    });

    const option = environmentSelectOptions([env])[0];
    expect(option?.disabled).toBe(true);
    expect(option?.label).toContain("无可用版本");
    expect(option?.unavailableReason).toContain("暂不可用于任务启动");
  });

  it("构建失败与构建中的版本展示真实状态与诊断摘要", () => {
    const env = environment({
      versions: [
        version({
          build_status: "failed",
          error_summary: "构建失败: 镜像核实未通过",
          usable: false,
        }),
        version({ build_status: "building", number: 2, usable: false, version: "v0002" }),
      ],
    });

    const options = versionSelectOptions(env);
    expect(options[0]?.label).toContain("构建失败");
    expect(options[0]?.errorSummary).toBe("构建失败: 镜像核实未通过");
    expect(options[1]?.label).toContain("构建中");
    expect(options[1]?.errorSummary).toBeUndefined();
  });

  it("未知构建状态原样展示", () => {
    expect(buildStatusLabel("queued_by_platform")).toBe("queued_by_platform");
    expect(buildStatusLabel("succeeded")).toBe("构建成功");
  });

  it("按名称查找语言环境，忽略空白与基础环境", () => {
    const environments = [environment({ name: "base", type: "base" }), environment({ name: "go" })];

    expect(findTaskEnvironment(environments, " go ")?.name).toBe("go");
    expect(findTaskEnvironment(environments, "base")).toBeUndefined();
    expect(findTaskEnvironment(environments, "  ")).toBeUndefined();
  });
});

describe("提交前的环境校验", () => {
  const env = environment({
    current_version: "v0001",
    latest_version: "v0002",
    versions: [
      version({ number: 1, version: "v0001" }),
      version({ build_status: "failed", number: 2, usable: false, version: "v0002" }),
    ],
  });

  it("选中的可用环境与版本被映射为任务创建请求字段", () => {
    const result = resolveEnvironmentSelection({
      environments: [env],
      loaded: true,
      name: "go",
      version: "v0001",
    });

    expect(result).toEqual({ environment: "go", environment_version: "v0001", ok: true });
  });

  it("环境尚未加载完成时阻止提交", () => {
    const result = resolveEnvironmentSelection({
      environments: [],
      loaded: false,
      name: "go",
      version: "v0001",
    });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("环境数据尚未加载完成");
  });

  it("未选择环境或版本时阻止提交", () => {
    const noEnvironment = resolveEnvironmentSelection({
      environments: [env],
      loaded: true,
      version: "v0001",
    });
    expect(noEnvironment.ok).toBe(false);

    const noVersion = resolveEnvironmentSelection({
      environments: [env],
      loaded: true,
      name: "go",
    });
    expect(noVersion.ok).toBe(false);
    if (!noVersion.ok) expect(noVersion.error).toContain("请选择环境版本");
  });

  it("跨节点的旧选择被拒绝", () => {
    const result = resolveEnvironmentSelection({
      environments: [env],
      loaded: true,
      name: "node",
      version: "v0001",
    });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("不在当前执行节点的环境列表中");
  });

  it("不属于所选环境的版本被拒绝", () => {
    const result = resolveEnvironmentSelection({
      environments: [env],
      loaded: true,
      name: "go",
      version: "v9999",
    });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("不属于环境 go");
  });

  it("不可用版本被拒绝并给出构建状态", () => {
    const result = resolveEnvironmentSelection({
      environments: [env],
      loaded: true,
      name: "go",
      version: "v0002",
    });

    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.error).toContain("当前不可用");
      expect(result.error).toContain("构建失败");
    }
  });

  it("环境没有任何可用版本时不能提交", () => {
    const broken = environment({
      latest_version: "v0002",
      versions: [version({ build_status: "building", number: 2, usable: false, version: "v0002" })],
    });

    const result = resolveEnvironmentSelection({
      environments: [broken],
      loaded: true,
      name: "go",
      version: "v0002",
    });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("没有可用版本");
  });
});
