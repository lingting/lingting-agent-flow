import { mkdirSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

/**
 * 测试工作空间根目录：`<OS 临时目录>/lingting-agent-flow-ui-tests/<随机名>`。
 *
 * 目录在测试入口启动时创建，测试结束后保留，不自动清理，便于排查失败现场。
 */
const ARTIFACT_ROOT = join(tmpdir(), "lingting-agent-flow-ui-tests");

mkdirSync(ARTIFACT_ROOT, { recursive: true });

export const TEST_ARTIFACT_DIR = mkdtempSync(join(ARTIFACT_ROOT, "run-"));

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
