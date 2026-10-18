import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join } from "node:path";

const require = createRequire(import.meta.url);
const projectRoot = join(import.meta.dirname, "..", "..");

/** 读取项目根目录相对路径下的文件内容 */
export function readProjectFile(relativePath: string) {
  return readFileSync(join(projectRoot, relativePath), "utf8");
}

/** 项目根目录绝对路径 */
export const PROJECT_ROOT = projectRoot;

/** 已安装的依赖版本；未安装时返回 undefined */
export function installedVersion(packageName: string) {
  try {
    return (require(`${packageName}/package.json`) as { version: string }).version;
  } catch {
    return undefined;
  }
}

/** 执行一条项目内命令并返回输出，命令失败时抛出包含 stderr 的错误 */
export function runProjectCommand(command: string, args: readonly string[]) {
  return execFileSync(command, args, {
    cwd: projectRoot,
    encoding: "utf8",
    shell: process.platform === "win32",
    stdio: ["ignore", "pipe", "pipe"],
  });
}
