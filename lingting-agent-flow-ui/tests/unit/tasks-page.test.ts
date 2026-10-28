import { describe, expect, it } from "vitest";

import type { Task, TaskStatus } from "../../src/lib/api";
import {
  canCancelTask,
  canDeleteTask,
  TASK_STATUS_META,
  TASK_STATUS_OPTIONS,
  taskStatusMeta,
} from "../../src/pages/tasks/status";
import { toCreateTaskRequest } from "../../src/pages/tasks/CreateTaskModal";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 一份任务样本，仅用于判定操作可用性 */
function task(overrides: Partial<Task> = {}): Task {
  return {
    cancel_requested: false,
    created_at: "2025-01-01T00:00:00Z",
    description: "描述",
    id: "task-1",
    name: "任务",
    project_id: "",
    status: "queued",
    status_finished: false,
    status_terminal: false,
    updated_at: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("任务状态元数据", () => {
  it("覆盖 Go 端的全部状态取值", () => {
    const expected: TaskStatus[] = [
      "queued",
      "preparing",
      "planning",
      "planned",
      "developing",
      "verifying",
      "saving",
      "cancelling",
      "retrying",
      "paused",
      "recovery_required",
      "completed",
      "failed",
      "cancelled",
    ];
    expect(Object.keys(TASK_STATUS_META).sort()).toEqual([...expected].sort());
    expect(TASK_STATUS_OPTIONS.map((option) => option.value)).toEqual(expected);
  });

  it("不把等待执行或中间状态标注为成功或失败", () => {
    for (const status of ["queued", "preparing", "developing", "cancelling"] as TaskStatus[]) {
      const meta = taskStatusMeta(status);
      expect(meta.color).not.toBe("success");
      expect(meta.color).not.toBe("error");
    }
    expect(taskStatusMeta("completed").color).toBe("success");
    expect(taskStatusMeta("failed").color).toBe("error");
  });

  it("每个状态都有中文标签", () => {
    for (const meta of Object.values(TASK_STATUS_META)) {
      expect(meta.label.length).toBeGreaterThan(0);
      expect(meta.label).not.toMatch(/[a-z_]/);
    }
  });

  it("未知状态原样展示，不猜测语义", () => {
    const meta = taskStatusMeta("unknown_status" as TaskStatus);
    expect(meta.label).toBe("unknown_status");
    expect(meta.active).toBe(false);
  });
});

describe("任务操作可用性", () => {
  it("已停止执行的任务不能取消", () => {
    for (const status of ["completed", "failed", "cancelled"] as TaskStatus[]) {
      expect(canCancelTask(task({ status, status_finished: true }))).toBe(false);
    }
  });

  it("等待执行与执行中的任务可以取消", () => {
    expect(canCancelTask(task({ status: "queued" }))).toBe(true);
    expect(canCancelTask(task({ status: "developing" }))).toBe(true);
  });

  it("取消中的任务不重复发起取消", () => {
    expect(canCancelTask(task({ status: "cancelling" }))).toBe(false);
  });

  it("已停止执行的任务可以删除，执行中的任务不能删除", () => {
    for (const status of ["completed", "failed", "cancelled"] as TaskStatus[]) {
      expect(canDeleteTask(task({ status, status_finished: true }))).toBe(true);
    }
    expect(canDeleteTask(task({ status: "queued" }))).toBe(false);
    expect(canDeleteTask(task({ status: "developing" }))).toBe(false);
    expect(canDeleteTask(task({ status: "cancelling" }))).toBe(false);
  });

  it("failed 不是终态但已停止执行：可删除、不可取消", () => {
    const failed = task({ status: "failed", status_finished: true, status_terminal: false });
    expect(canDeleteTask(failed)).toBe(true);
    expect(canCancelTask(failed)).toBe(false);
  });
});

describe("创建任务请求转换", () => {
  /** 一份表单值样本 */
  function formValues() {
    return {
      acceptance_criteria: "构建通过\n测试通过",
      base_branch: " main ",
      commands: "build=go build ./...\n\n test=go test ./... ",
      description: " 接入真实接口 ",
      developer_role_id: " developer ",
      environment: " go ",
      host_id: " host-1 ",
      merge_after_push: false,
      name: " 实现登录 ",
      planner_role_id: " planner ",
      priority: "normal" as const,
      project_id: "   ",
      repository_url: " ssh://git.example.com/team/repo ",
      validator_role_id: "  ",
      work_dir: " /workspace/project ",
    };
  }

  it("去除首尾空白并按行拆分数组字段", () => {
    const request = toCreateTaskRequest(formValues());
    expect(request.name).toBe("实现登录");
    expect(request.description).toBe("接入真实接口");
    expect(request.base_branch).toBe("main");
    expect(request.host_id).toBe("host-1");
    expect(request.commands).toEqual(["build=go build ./...", "test=go test ./..."]);
    expect(request.acceptance_criteria).toEqual(["构建通过", "测试通过"]);
  });

  it("省略空的可选字段而不是发送空字符串", () => {
    const request = toCreateTaskRequest(formValues());
    expect(request).not.toHaveProperty("validator_role_id");
    expect(request).not.toHaveProperty("environment_version");
  });

  it("项目标识是必填字段：原样透传并交由服务端判定", () => {
    // 项目标识不再是可省略字段：前端不静默丢弃空值，也不猜测一个默认项目，
    // 空值提交后由服务端返回参数错误。
    const request = toCreateTaskRequest(formValues());
    expect(request.project_id).toBe("");
    const withProject = toCreateTaskRequest({ ...formValues(), project_id: " demo-service " });
    expect(withProject.project_id).toBe("demo-service");
  });

  it("不生成状态、开始时间或完成时间字段", () => {
    const request = toCreateTaskRequest(formValues()) as Record<string, unknown>;
    for (const forbidden of ["status", "started_at", "finished_at", "created_at", "updated_at"]) {
      expect(request).not.toHaveProperty(forbidden);
    }
  });

  it("空的多行输入转换为 undefined 而不是空数组", () => {
    const request = toCreateTaskRequest({
      ...formValues(),
      commands: "  \n ",
      acceptance_criteria: "",
    });
    expect(request.commands).toBeUndefined();
    expect(request.acceptance_criteria).toBeUndefined();
  });
});
