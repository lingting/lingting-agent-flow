/**
 * 前端生产构建入口。
 *
 * 构建产物直接写入 Go 的嵌入包目录 `internal/webui/dist`（`//go:embed` 无法引用
 * 包目录之外的文件）。Vite 会清空该目录，因此构建结束后需要补回占位文件，
 * 否则全新检出的仓库会因为目录为空而无法编译。
 *
 * 任一环节失败都以非零退出码结束，不会让"打包漏了前端"的情况静默通过。
 */
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

const projectRoot = resolve(import.meta.dirname, "..");
const repoRoot = resolve(projectRoot, "..");
const distDir = join(repoRoot, "internal", "webui", "dist");
const placeholder = join(distDir, ".gitkeep");
const placeholderContent = `# 占位文件：前端生产构建产物写入本目录（由仓库根目录 Makefile 的 web-build 目标生成）。
# 保留该文件使全新检出的仓库满足 //go:embed 的编译要求；正式发布前必须执行前端构建。
`;

function fail(message) {
  process.stderr.write(`错误: ${message}\n`);
  process.exit(1);
}

// vpBinary 返回本项目的 Vite+ 可执行文件路径。
//
// 直接依赖 node_modules/.bin 中的入口，而不是依赖全局 PATH：从仓库根目录执行
// `make release` 或 `node scripts/build-webui.mjs` 时同样能定位到已安装的版本。
function vpBinary() {
  const local = join(
    projectRoot,
    "node_modules",
    ".bin",
    process.platform === "win32" ? "vp.CMD" : "vp",
  );
  return existsSync(local) ? local : "vp";
}
// 1. 执行前端生产构建。
const build = spawnSync(vpBinary(), ["build"], {
  cwd: projectRoot,
  shell: process.platform === "win32",
  stdio: "inherit",
});
if (build.error) {
  fail(`无法执行 vp build: ${build.error.message}`);
}
if (build.status !== 0) {
  fail(`前端生产构建失败，退出码 ${build.status ?? "unknown"}`);
}

// 2. 确认构建产物完整生成。
if (!existsSync(distDir)) {
  fail(`构建产物目录不存在: ${distDir}`);
}
const indexFile = join(distDir, "index.html");
if (!existsSync(indexFile)) {
  fail(`构建产物缺少 index.html: ${indexFile}`);
}
const assetsDir = join(distDir, "assets");
const assets = existsSync(assetsDir) ? readdirSync(assetsDir) : [];
if (!assets.some((name) => name.endsWith(".js"))) {
  fail(`构建产物缺少 JavaScript 资源: ${assetsDir}`);
}
if (!assets.some((name) => name.endsWith(".css"))) {
  fail(`构建产物缺少 CSS 资源: ${assetsDir}`);
}

// 3. 补回占位文件，保证仓库在构建后仍然满足 go:embed 的编译要求。
mkdirSync(distDir, { recursive: true });
writeFileSync(placeholder, placeholderContent, "utf8");

process.stdout.write(`前端构建产物已写入 ${distDir}\n`);
