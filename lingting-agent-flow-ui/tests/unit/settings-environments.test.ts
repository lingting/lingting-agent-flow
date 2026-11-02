import { describe, expect, it } from "vitest";

import { ApiError, type Environment, type EnvironmentVersion, type Host } from "@/lib/api";
import { printArtifactDir } from "../internal/artifacts";
import {
  buildStatusMeta,
  buildStillRunning,
  canManageEnvironments,
  canSubmitBuild,
  environmentTypeLabel,
  environmentVersionStats,
  environmentWriteBlockedReason,
  hostSelectOptions,
  isBuildInProgressError,
  parseLanguageNames,
  resolveBuildStatusOutcome,
  sortVersionsNewestFirst,
} from "@/pages/settings/settings-environments";

printArtifactDir();

/** 构造一个执行节点 */
function host(overrides: Partial<Host> = {}): Host {
  return {
    auth_method: "private_key",
    created_at: "2026-10-26T08:00:00Z",
    enabled: true,
    host: "10.0.0.11",
    id: "host-1",
    is_default: false,
    last_connect_status: "ok",
    name: "构建机",
    port: 22,
    private_key_path: "/home/dev/.ssh/id_ed25519",
    updated_at: "2026-10-26T08:00:00Z",
    usable_for_remote_task: true,
    username: "dev",
    validation_generation: 1,
    validation_status: "normal",
    workspace_root: "/home/dev/agentflow",
    ...overrides,
  } as Host;
}

/** 构造一个环境版本 */
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

/** 构造一个环境 */
function environment(overrides: Partial<Environment> = {}): Environment {
  return {
    current_version: "v0001",
    latest_version: "v0001",
    name: "go",
    type: "language",
    versions: [version()],
    ...overrides,
  };
}

describe("环境展示元数据", () => {
  it("构造状态覆盖 Go 端全部取值，uncertain 单独展示", () => {
    // pending 只是"尚未构建"：它必须仍然可以发起构建，因此不算在途。
    expect(buildStatusMeta("pending")).toMatchObject({ label: "待构建", inFlight: false });
    expect(buildStatusMeta("building")).toMatchObject({ label: "构建中", inFlight: true });
    expect(buildStatusMeta("succeeded")).toMatchObject({ label: "构建成功", inFlight: false });
    expect(buildStatusMeta("failed")).toMatchObject({ label: "构建失败", inFlight: false });
    expect(buildStatusMeta("uncertain")).toMatchObject({
      color: "warning",
      inFlight: false,
      label: "结果未知",
    });
  });

  it("未知构建状态按不可用且非在途展示，不猜测成成功", () => {
    const meta = buildStatusMeta("weird");
    expect(meta.inFlight).toBe(false);
    expect(meta.label).toBe("weird");
    expect(meta.color).toBe("default");
  });

  it("环境类型标签与 Go 端 domain.EnvironmentType 对应", () => {
    expect(environmentTypeLabel("base")).toBe("基础环境");
    expect(environmentTypeLabel("language")).toBe("语言环境");
    expect(environmentTypeLabel("other")).toBe("other");
  });

  it("版本统计只依据服务端返回的记录", () => {
    const env = environment({
      versions: [
        version({ number: 1 }),
        version({ build_status: "failed", number: 2, usable: false, version: "v0002" }),
        version({ build_status: "uncertain", number: 3, usable: false, version: "v0003" }),
      ],
    });
    expect(environmentVersionStats(env)).toEqual({ problematic: 2, total: 3, usable: 1 });
  });

  it("版本按版本号降序展示，且不修改原数组", () => {
    const versions = [version({ number: 1 }), version({ number: 3 }), version({ number: 2 })];
    const sorted = sortVersionsNewestFirst(versions);
    expect(sorted.map((item) => item.number)).toEqual([3, 2, 1]);
    expect(versions.map((item) => item.number)).toEqual([1, 3, 2]);
  });
});

describe("操作可用性判定", () => {
  it("只有非在途且未成功的版本可以提交构建", () => {
    // 待构建与失败/未知都可以（重新）发起构建；只有真正在途或已成功的不需要重复提交。
    expect(canSubmitBuild(version({ build_status: "pending" }))).toBe(true);
    expect(canSubmitBuild(version({ build_status: "building" }))).toBe(false);
    expect(canSubmitBuild(version({ build_status: "succeeded" }))).toBe(false);
    expect(canSubmitBuild(version({ build_status: "failed" }))).toBe(true);
    expect(canSubmitBuild(version({ build_status: "uncertain" }))).toBe(true);
    expect(canSubmitBuild(undefined)).toBe(false);
  });

  it("节点可用性只信任服务端计算的 usable_for_remote_task", () => {
    expect(canManageEnvironments(host())).toBe(true);
    expect(canManageEnvironments(host({ usable_for_remote_task: false }))).toBe(false);
    expect(canManageEnvironments(undefined)).toBe(false);
  });

  it("不可用节点给出服务端原因，校验中单独说明", () => {
    expect(environmentWriteBlockedReason(host())).toBeUndefined();
    expect(environmentWriteBlockedReason(undefined)).toBe("尚未选择执行节点");
    expect(
      environmentWriteBlockedReason(
        host({ usable_for_remote_task: false, validation_status: "checking" }),
      ),
    ).toContain("环境预检尚未完成");
    const abnormal = environmentWriteBlockedReason(
      host({
        usable_for_remote_task: false,
        validation_message: "SSH 连接失败",
        validation_status: "abnormal",
      }),
    );
    expect(abnormal).toContain("abnormal");
    expect(abnormal).toContain("SSH 连接失败");
  });

  it("节点选项展示真实地址与可用性，不隐藏不可用节点", () => {
    const options = hostSelectOptions([
      host(),
      host({ host: "10.0.0.12", id: "host-2", name: "备用机", usable_for_remote_task: false }),
    ]);
    expect(options).toHaveLength(2);
    expect(options[0].label).toContain("10.0.0.11:22");
    expect(options[0].label).toContain("可用于环境操作");
    expect(options[1].disabled).toBe(false);
    expect(options[1].label).toContain("不可用于环境操作");
  });

  it("构建进行中的错误码可被识别", () => {
    const conflict = new ApiError("http", "已有构建进行中", {
      code: "environment_build_in_progress",
      status: 409,
    });
    expect(isBuildInProgressError(conflict)).toBe(true);
    expect(isBuildInProgressError(new Error("其他错误"))).toBe(false);
  });
});

describe("构建状态归一", () => {
  it("失败与结果未知分别保留服务端原因", () => {
    const failed = resolveBuildStatusOutcome(
      undefined,
      new ApiError("http", "构建失败: 退出码 1", {
        code: "environment_build_failed",
        status: 500,
      }),
    );
    expect(failed).toEqual({ kind: "failed", message: "构建失败: 退出码 1" });

    const uncertain = resolveBuildStatusOutcome(
      undefined,
      new ApiError("http", "构建被中断", {
        code: "environment_build_uncertain",
        status: 500,
      }),
    );
    expect(uncertain).toEqual({ kind: "uncertain", message: "构建被中断" });
  });

  it("其他错误不被压缩成构建失败", () => {
    const outcome = resolveBuildStatusOutcome(
      undefined,
      new ApiError("network", "无法连接 AgentFlow 服务"),
    );
    expect(outcome.kind).toBe("error");
  });

  it("成功与在途结论来自版本记录", () => {
    expect(
      resolveBuildStatusOutcome({ in_progress: false, version: version() }, undefined),
    ).toMatchObject({ kind: "succeeded" });
    expect(
      resolveBuildStatusOutcome(
        { in_progress: true, version: version({ build_status: "building" }) },
        undefined,
      ),
    ).toMatchObject({ kind: "running" });
  });

  it("本地失败记录与结果未知记录都给出真实原因", () => {
    expect(
      resolveBuildStatusOutcome(
        {
          in_progress: false,
          version: version({ build_status: "failed", error_summary: "退出码 2" }),
        },
        undefined,
      ),
    ).toEqual({ kind: "failed", message: "退出码 2" });
    expect(
      resolveBuildStatusOutcome(
        { in_progress: false, version: version({ build_status: "uncertain" }) },
        undefined,
      ),
    ).toMatchObject({ kind: "uncertain" });
  });

  it("仍在推进的状态只有待构建与构建中", () => {
    expect(buildStillRunning("pending")).toBe(true);
    expect(buildStillRunning("building")).toBe(true);
    expect(buildStillRunning("succeeded")).toBe(false);
    expect(buildStillRunning("uncertain")).toBe(false);
    expect(buildStillRunning(undefined)).toBe(false);
  });
});

describe("初始化语言环境输入", () => {
  it("支持逗号、空白与中文逗号分隔，并去重前保持顺序", () => {
    expect(parseLanguageNames("go, node")).toEqual(["go", "node"]);
    expect(parseLanguageNames("go，node  python")).toEqual(["go", "node", "python"]);
    expect(parseLanguageNames("  ")).toEqual([]);
  });
});
