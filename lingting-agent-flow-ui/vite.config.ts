import { resolve } from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite-plus";

import { peerDependencies } from "lingting-react-ui/package.json";

// LRI 的 peerDependencies 是宿主去重列表的唯一来源。缺少任一条目时，构建产物会包含
// 同一包的两份实现，React Context 与模块级单例无法跨实例共享；该问题在 dev 下常被
// 预打包掩盖，只在构建产物中复现。LRI 新增 peer 依赖时必须同步补齐。
const dedupe = Object.keys(peerDependencies);

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": resolve(import.meta.dirname, "src"),
    },
    dedupe,
  },
  fmt: {
    ignorePatterns: ["node_modules", "dist", "test-results", "playwright-report", "*.yaml", "*.md"],
  },
  lint: {
    ignorePatterns: ["node_modules/**", "dist/**", "test-results/**", "playwright-report/**"],
    options: {
      typeAware: true,
      typeCheck: true,
    },
  },
  server: {
    host: "127.0.0.1",
    port: 26390,
    strictPort: true,
  },
  preview: {
    host: "127.0.0.1",
    port: 26391,
    strictPort: true,
  },
  build: {
    outDir: "dist",
  },
  test: {
    include: ["tests/**/*.test.ts"],
  },
});
