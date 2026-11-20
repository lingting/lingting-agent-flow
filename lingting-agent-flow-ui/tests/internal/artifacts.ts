import { mkdirSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

/**
 * 测试产物根目录（R6 D7）。
 *
 * 统一执行器按用例注入 `AGENTFLOW_UI_ARTIFACT_DIR`：一条 Case 一个文件，产物落在该 Case
 * 自己的 `artifacts/` 目录里，不再另外生成随机目录。独立运行（`pnpm test`）没有注入时退回
 * `<OS 临时目录>/lingting-agent-flow-ui-tests/run-<随机名>`，目录保留以便排查失败现场。
 */
const ARTIFACT_ROOT = join(tmpdir(), "lingting-agent-flow-ui-tests");

function resolveArtifactDir() {
  const injected = (process.env.AGENTFLOW_UI_ARTIFACT_DIR ?? "").trim();
  if (injected !== "") {
    mkdirSync(injected, { recursive: true });
    return injected;
  }
  mkdirSync(ARTIFACT_ROOT, { recursive: true });
  return mkdtempSync(join(ARTIFACT_ROOT, "run-"));
}

export const TEST_ARTIFACT_DIR = resolveArtifactDir();

/** 在测试工作空间下创建并返回一个子目录，用于隔离单个用例的产物。 */
export function artifactDir(name: string) {
  const dir = join(TEST_ARTIFACT_DIR, name);
  mkdirSync(dir, { recursive: true });
  return dir;
}

/**
 * 输出 `TEST_ARTIFACT_DIR=<绝对路径>`，每个测试入口都必须调用一次。
 */
export function printArtifactDir() {
  process.stdout.write(`TEST_ARTIFACT_DIR=${TEST_ARTIFACT_DIR}\n`);
}
