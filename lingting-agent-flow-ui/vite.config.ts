import { resolve } from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite-plus";

import { peerDependencies } from "lingting-react-ui/package.json";

// LRI 的 peerDependencies 是宿主去重列表的唯一来源。缺少任一条目时，构建产物会包含
// 同一包的两份实现，React Context 与模块级单例无法跨实例共享；该问题在 dev 下常被
// 预打包掩盖，只在构建产物中复现。LRI 新增 peer 依赖时必须同步补齐。
const dedupe = Object.keys(peerDependencies);

// Go HTTP 服务的开发监听地址。只用于开发代理，不会进入生产构建产物：
// 前端源码始终请求同源相对路径 `/api/v1/...`。
const DEV_API_TARGET = process.env.AGENTFLOW_DEV_API_TARGET ?? "http://127.0.0.1:26380";

// 前端生产构建产物目录，即 Go 的嵌入包目录。
const WEBUI_DIST_DIR = resolve(import.meta.dirname, "..", "internal", "webui", "dist");

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": resolve(import.meta.dirname, "src"),
    },
    dedupe,
  },
  fmt: {
    ignorePatterns: [
      "node_modules",
      "dist",
      "test-results",
      "playwright-report",
      "*.yaml",
      "*.md",
      "**/internal/webui/dist/**",
    ],
  },
  lint: {
    ignorePatterns: [
      "node_modules/**",
      "dist/**",
      "test-results/**",
      "playwright-report/**",
      "**/internal/webui/dist/**",
    ],
    options: {
      typeAware: true,
      typeCheck: true,
    },
  },
  server: {
    host: "127.0.0.1",
    port: 26390,
    // 开发环境把 /api 转发到 Go 服务：前端始终使用同源相对路径，生产环境由
    // Go 进程自身提供页面与 API，因此两套环境的请求路径完全一致。
    proxy: {
      "/api": {
        changeOrigin: false,
        target: DEV_API_TARGET,
      },
    },
    strictPort: true,
  },
  preview: {
    host: "127.0.0.1",
    port: 26391,
    strictPort: true,
  },
  build: {
    // 生产构建直接写入 Go 的嵌入包目录：`//go:embed` 无法引用包目录之外的文件，
    // 因此不能把产物留在 lingting-agent-flow-ui/dist。
    emptyOutDir: true,
    outDir: WEBUI_DIST_DIR,
  },
  test: {
    include: ["tests/**/*.test.ts"],
  },
});
