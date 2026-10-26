import { describe, expect, it } from "vitest";

import type { Host, Role } from "../../src/lib/api";
import {
  enabledRolesOfType,
  hostSelectable,
  hostSelectOptions,
  hostUnavailableReason,
  isEnabledRoleOfType,
  resolveHostSelection,
  resolveRoleSelection,
  roleSelectOptions,
} from "../../src/pages/tasks/selectors";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 一份角色样本，字段与 Go 端 `httpapi.RoleResponse` 一致 */
function role(overrides: Partial<Role> = {}): Role {
  return {
    enabled: true,
    id: "role-1",
    model: "group/free",
    name: "角色",
    runner: "pi",
    type: "planner",
    ...overrides,
  };
}

/** 一份执行节点样本，字段与 Go 端 `httpapi.HostResponse` 一致 */
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
    private_key_path: "/home/user/.ssh/id_ed25519",
    updated_at: "2026-10-26T08:00:00Z",
    usable_for_remote_task: true,
    username: "dev",
    validation_generation: 1,
    validation_status: "normal",
    workspace_root: "/home/dev/agentflow",
    ...overrides,
  };
}

/** 一组覆盖三种类型与启用状态的完整角色列表 */
function roleFixture(): Role[] {
  return [
    role({ id: "planner-on", name: "规划启用" }),
    role({ enabled: false, id: "planner-off", name: "规划禁用" }),
    role({ id: "dev-on", name: "开发启用", type: "developer" }),
    role({ enabled: false, id: "dev-off", name: "开发禁用", type: "developer" }),
    role({ id: "val-on", name: "验证启用", type: "validator" }),
    role({ enabled: false, id: "val-off", name: "验证禁用", type: "validator" }),
  ];
}

describe("角色按 type 分类", () => {
  it("只返回指定类型且已启用的角色", () => {
    const roles = roleFixture();

    expect(enabledRolesOfType(roles, "planner").map((item) => item.id)).toEqual(["planner-on"]);
    expect(enabledRolesOfType(roles, "developer").map((item) => item.id)).toEqual(["dev-on"]);
    expect(enabledRolesOfType(roles, "validator").map((item) => item.id)).toEqual(["val-on"]);
  });

  it("enabled=false 的角色不会进入可选项", () => {
    const roles = roleFixture();

    for (const type of ["planner", "developer", "validator"] as const) {
      expect(enabledRolesOfType(roles, type).every((item) => item.enabled)).toBe(true);
    }
    expect(isEnabledRoleOfType(roles, "planner", "planner-off")).toBe(false);
    expect(isEnabledRoleOfType(roles, "planner", "dev-on")).toBe(false);
    expect(isEnabledRoleOfType(roles, "planner", "planner-on")).toBe(true);
  });

  it("禁用角色仍作为禁用选项展示，便于用户诊断", () => {
    const options = roleSelectOptions(roleFixture(), "planner");

    expect(options.map((option) => option.value)).toEqual(["planner-on", "planner-off"]);
    expect(options[0]?.disabled).toBe(false);
    expect(options[1]?.disabled).toBe(true);
    expect(options[1]?.label).toContain("已禁用");
    // 展示真实名称，模型与执行器只作为辅助信息
    expect(options[0]?.label).toContain("规划启用");
    expect(options[0]?.label).toContain("group/free");
  });

  it("提交值必须是角色真实 id，而不是名称", () => {
    const options = roleSelectOptions(roleFixture(), "developer");

    expect(options[0]?.value).toBe("dev-on");
    expect(options[0]?.label).not.toBe(options[0]?.value);
  });
});

describe("执行节点可用性", () => {
  it("已启用且服务端标记可用的节点可选", () => {
    expect(hostSelectable(host())).toBe(true);
    expect(hostUnavailableReason(host())).toBeUndefined();
  });

  it("已禁用的节点不可选并给出真实原因", () => {
    const disabled = host({ enabled: false, usable_for_remote_task: false });

    expect(hostSelectable(disabled)).toBe(false);
    expect(hostUnavailableReason(disabled)).toBe("该执行节点已禁用");
  });

  it("预检未通过的节点不可选，原因来自服务端字段", () => {
    const abnormal = host({
      usable_for_remote_task: false,
      validation_message: "SSH 连接失败",
      validation_status: "abnormal",
    });

    expect(hostSelectable(abnormal)).toBe(false);
    expect(hostUnavailableReason(abnormal)).toBe("SSH 连接失败");
  });

  it("预检中的节点不可选，且不把 checking 显示为可用", () => {
    const checking = host({ usable_for_remote_task: false, validation_status: "checking" });

    expect(hostSelectable(checking)).toBe(false);
    expect(hostUnavailableReason(checking)).toBe("环境预检尚未完成");
  });

  it("选项展示名称与地址端口，不展示私钥路径", () => {
    const options = hostSelectOptions([
      host(),
      host({ enabled: false, id: "host-2", name: "停用节点", usable_for_remote_task: false }),
    ]);

    expect(options[0]?.value).toBe("host-1");
    expect(options[0]?.label).toContain("构建机");
    expect(options[0]?.label).toContain("dev@10.0.0.11:22");
    expect(options[0]?.disabled).toBe(false);
    expect(options[1]?.disabled).toBe(true);
    expect(options[1]?.unavailableReason).toBe("该执行节点已禁用");
    for (const option of options) {
      expect(option.label).not.toContain("id_ed25519");
    }
  });
});

describe("执行节点选择校验", () => {
  it("未加载完成时拒绝提交", () => {
    const result = resolveHostSelection({ hostID: "host-1", hosts: [host()], loaded: false });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("尚未加载完成");
  });

  it("未选择节点时拒绝提交", () => {
    const result = resolveHostSelection({ hostID: "  ", hosts: [host()], loaded: true });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toBe("请选择执行节点");
  });

  it("节点不在当前列表中时拒绝提交", () => {
    const result = resolveHostSelection({ hostID: "host-9", hosts: [host()], loaded: true });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("不在当前节点列表中");
  });

  it("已禁用节点不能用于提交", () => {
    const result = resolveHostSelection({
      hostID: "host-1",
      hosts: [host({ enabled: false, usable_for_remote_task: false })],
      loaded: true,
    });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("已被禁用");
  });

  it("预检未通过的节点不能用于提交", () => {
    const result = resolveHostSelection({
      hostID: "host-1",
      hosts: [host({ usable_for_remote_task: false, validation_message: "Docker 版本过低" })],
      loaded: true,
    });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("Docker 版本过低");
  });

  it("合法节点返回真实 id", () => {
    const result = resolveHostSelection({
      hostID: " host-1 ",
      hosts: [host()],
      loaded: true,
    });

    expect(result).toEqual({ host_id: "host-1", ok: true });
  });
});

describe("角色选择校验", () => {
  const roles = roleFixture();

  it("角色未加载完成时拒绝提交", () => {
    const result = resolveRoleSelection({
      developerRoleID: "dev-on",
      loaded: false,
      plannerRoleID: "planner-on",
      roles,
    });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("尚未加载完成");
  });

  it("缺少规划角色时不能提交", () => {
    const result = resolveRoleSelection({ developerRoleID: "dev-on", loaded: true, roles });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("规划角色");
  });

  it("缺少开发角色时不能提交", () => {
    const result = resolveRoleSelection({ loaded: true, plannerRoleID: "planner-on", roles });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("开发角色");
  });

  it("没有启用的必需角色时不能提交", () => {
    const onlyDisabled = [
      role({ enabled: false, id: "planner-off", type: "planner" }),
      role({ enabled: false, id: "dev-off", type: "developer" }),
    ];

    const result = resolveRoleSelection({
      developerRoleID: "dev-off",
      loaded: true,
      plannerRoleID: "planner-off",
      roles: onlyDisabled,
    });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("没有启用的规划角色");
  });

  it("选了已禁用角色时拒绝提交", () => {
    const result = resolveRoleSelection({
      developerRoleID: "dev-on",
      loaded: true,
      plannerRoleID: "planner-off",
      roles,
    });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("所选规划角色不可用");
  });

  it("选了错误类型的角色时拒绝提交", () => {
    const result = resolveRoleSelection({
      developerRoleID: "planner-on",
      loaded: true,
      plannerRoleID: "planner-on",
      roles,
    });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("所选开发角色不可用");
  });

  it("验证角色可选：未选择时不产生空字符串字段", () => {
    const result = resolveRoleSelection({
      developerRoleID: "dev-on",
      loaded: true,
      plannerRoleID: "planner-on",
      roles,
      validatorRoleID: "   ",
    });

    expect(result).toEqual({
      developer_role_id: "dev-on",
      ok: true,
      planner_role_id: "planner-on",
      validator_role_id: undefined,
    });
    if (result.ok) {
      expect(result).not.toHaveProperty("validator_role_id");
    }
  });

  it("选了已禁用的验证角色时拒绝提交", () => {
    const result = resolveRoleSelection({
      developerRoleID: "dev-on",
      loaded: true,
      plannerRoleID: "planner-on",
      roles,
      validatorRoleID: "val-off",
    });

    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("所选验证角色不可用");
  });

  it("合法选择返回三个真实角色 id", () => {
    const result = resolveRoleSelection({
      developerRoleID: " dev-on ",
      loaded: true,
      plannerRoleID: " planner-on ",
      roles,
      validatorRoleID: "val-on",
    });

    expect(result).toEqual({
      developer_role_id: "dev-on",
      ok: true,
      planner_role_id: "planner-on",
      validator_role_id: "val-on",
    });
  });
});
