import { describe, expect, it } from "vitest";

import { describeConnectionError } from "../../src/components/api-connection-status";
import { ApiError } from "../../src/lib/api";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

describe("连接状态文案", () => {
  it("网络失败给出启动服务的恢复提示", () => {
    const message = describeConnectionError(new ApiError("network", "无法连接"));

    expect(message).toContain("agentflow web serve");
  });

  it("超时与取消分别说明，不合并成业务错误", () => {
    expect(describeConnectionError(new ApiError("timeout", "超时"))).toContain("超时");
    expect(describeConnectionError(new ApiError("aborted", "取消"))).toContain("取消");
  });

  it("响应无法解析时提示可能不是 AgentFlow API", () => {
    expect(describeConnectionError(new ApiError("invalid_response", "无效"))).toContain(
      "AgentFlow API",
    );
  });

  it("HTTP 业务错误直接使用服务端说明", () => {
    expect(describeConnectionError(new ApiError("http", "数据库不可用", { status: 503 }))).toBe(
      "数据库不可用",
    );
  });

  it("缺少错误对象时给出默认说明", () => {
    expect(describeConnectionError(undefined)).toContain("无法连接");
  });
});

describe("连接状态默认值", () => {
  it("首次检查前不默认显示已连接", async () => {
    const source = await import("node:fs").then((fs) =>
      fs.readFileSync(new URL("../../src/lib/api/useApiConnection.ts", import.meta.url), "utf8"),
    );

    expect(source).toContain('const INITIAL_STATE: ConnectionState = { status: "checking" };');
  });

  it("连接状态只包含 checking/connected/disconnected 三种取值", async () => {
    const source = await import("node:fs").then((fs) =>
      fs.readFileSync(new URL("../../src/lib/api/useApiConnection.ts", import.meta.url), "utf8"),
    );

    expect(source).toContain(
      'export type ConnectionStatus = "checking" | "connected" | "disconnected";',
    );
  });
});
