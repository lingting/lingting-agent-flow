# lingting-agent-flow-ui

AgentFlow 的 Web 前端。包含基础工程、应用外壳、路由、占位页面，以及通过统一 API 客户端接入的
后端连接状态。生产构建产物由 Go 程序通过 `//go:embed` 嵌入并随单可执行文件交付。

## 技术栈

| 项目       | 版本                                                         |
| ---------- | ------------------------------------------------------------ |
| Node.js    | 22.22.3（Volta 固定）                                        |
| pnpm       | 11.24.0                                                      |
| Vite+      | 0.2.6（`vite` 别名指向 `@voidzero-dev/vite-plus-core@0.2.6`） |
| React      | 19.2.9                                                       |
| TypeScript | 5.9.3                                                        |
| 路由       | `@tanstack/react-router` 1.170.45（经 `lingting-react-ui` 布局内部使用） |
| UI 组件    | `lingting-react-ui` **2026.10.0**（精确锁定）                |
| 样式       | Tailwind CSS 4.1.18                                          |
| 测试       | Vitest 4.1.10（`vp test`）、Playwright 1.64.1                |

版本以 `pnpm-lock.yaml` 为准，上表是本次实际安装并验证的结果。

## UI 组件库

`lingting-react-ui` 是唯一的 UI 组件使用入口，版本精确锁定为 `2026.10.0`：

```bash
pnpm add lingting-react-ui@2026.10.0
```

- 从包根入口导入：`import { AppSidebarLayout, UserStore, Button } from "lingting-react-ui"`。
- 样式随入口自动引入（包内 `sideEffects` 与构建产物已把全局样式接回各入口），本工程只需在
  `src/index.css` 提供 `html` / `body` / `#root` 的高度链与 `body` 的滚动行为。
- 该库把第三方运行时依赖声明在 `peerDependencies`，由本工程提供实际实例。`vite.config.ts` 的
  `resolve.dedupe` **直接由 `lingting-react-ui/package.json` 的 `peerDependencies` 推导**，避免
  手工列表与依赖脱节；缺少去重时构建产物会出现同一包的两份实例，React Context 与模块级单例
  无法跨实例共享（dev 下会被预打包掩盖，只在构建产物中复现）。
- 组件选择优先级：`lingting-react-ui` → Ant Design Pro Components → Ant Design。

## 启动与检查

在 `lingting-agent-flow-ui/` 目录下执行：

```bash
pnpm install
pnpm dev                    # vp dev，开发服务器 http://127.0.0.1:26390
pnpm build                  # 生产构建，产物写入 ../internal/webui/dist
pnpm build:assets           # 仅执行 vp build，不补回占位文件
pnpm lint                   # vp fmt && vp lint --fix && tsc --noEmit
pnpm test                   # vp test run，单元测试
pnpm exec playwright test   # 基础布局与连接状态浏览器测试
```

### 开发代理

开发服务器把 `/api` 代理到 Go HTTP 服务（默认 `http://127.0.0.1:26380`）。开发时需要同时
启动 Go 服务：

```bash
# 仓库根目录
make serve
```

需要其他后端地址时设置 `AGENTFLOW_DEV_API_TARGET`（例如 `http://127.0.0.1:26381`）。该变量
只影响开发代理，不会进入生产构建产物。

### 生产构建

`pnpm build` 把产物直接写入 Go 的嵌入包目录 `internal/webui/dist/`（`//go:embed` 无法引用包
目录之外的文件），随后补回 `.gitkeep`，保证仓库在构建后仍可编译。构建脚本会校验
`index.html`、`assets/` 下的 `.js` 与 `.css` 是否齐全，任一缺失即失败。

单文件生产构建的推荐入口是仓库根目录的 `make release`。

## 目录结构

```text
src/
├── main.tsx                 应用入口：只挂载 AppShell
├── index.css                应用级样式入口（高度链与 body 滚动）
├── app/
│   ├── AppShell.tsx         应用外壳：连接状态 Provider + LRI 布局 + 头部指示器
│   └── navigation.tsx       导航与路由的唯一定义处、根重定向目标、产品名
├── lib/api/
│   ├── client.ts            统一请求层：路径校验、超时、取消与错误分类
│   ├── endpoints.ts         已实现接口（健康检查、系统信息）
│   ├── types.ts             公共响应类型
│   ├── useApiConnection.ts  连接状态 Hook（首次检查前为 checking）
│   └── ApiConnectionContext.tsx  应用内共享同一份连接状态
├── components/
│   ├── api-connection-status/  头部连接指示器与错误文案
│   ├── page-placeholder/    占位页内容
│   └── product-brand/       侧栏产品标识
└── pages/
    ├── overview/            概览（含真实连接状态面板）
    ├── tasks/               任务
    ├── projects/            项目
    ├── machines/            机器
    ├── environments/        环境
    ├── settings/            设置
    └── not-found/           404 页面
scripts/
└── build-webui.mjs          生产构建入口：vp build + 产物校验 + 占位文件补回
tests/
├── unit/                    工程配置、导航元数据、API 客户端与连接文案测试（Vitest）
├── e2e/                     基础布局与连接状态浏览器测试（Playwright）
└── internal/                测试工作空间与工程读取辅助
```

## API 连接层

- 所有请求经由 `src/lib/api/` 的统一客户端发出，页面不直接调用 `fetch`。
- 路径一律为同源相对路径（如 `/api/v1/health`），不硬编码 `localhost`、端口或后端完整 URL；
  开发环境靠 Vite+ 代理，生产环境由同一个 Go 进程提供页面与 API。
- 错误按类别区分：`http`（非成功状态码）、`network`（连接失败）、`timeout`（超时）、
  `aborted`（主动取消）、`invalid_response`（响应不是合法 JSON）。超时与取消不会被显示成
  服务端业务错误。
- 连接状态只有 `checking` / `connected` / `disconnected` 三种，首次检查完成前不显示已连接。
  挂载、窗口重新获得焦点、页面从 bfcache 恢复时各检查一次，并按 30 秒间隔轮询。
- 连接指示器只表示 AgentFlow HTTP API 的可用性，不代表 SSH、Docker、远程机器或 Pi Agent
  已经就绪。


## 路由与布局

- 应用外壳使用 `lingting-react-ui` 的 `AppSidebarLayout`，它内部基于
  `@tanstack/react-router` 生成路由树，并提供侧边栏、头部与内容区。
- 导航、页面标题、菜单高亮与文档标题全部由 `src/app/navigation.tsx` 的同一份路由元数据派生，
  不在页面内重复声明路径或标题。
- `/` 由布局的 `rootRedirectTo` 重定向到 `/overview`。**应用内不注册根路径路由**：LRI 布局在
  生成路由树时会在根路由下自行插入 `/` 重定向，业务路由再声明 `/` 会产生重复路由。
- 未知路径由 `src/pages/not-found` 渲染；该页面由布局放在侧边栏内容区内，因此不再包裹主题容器。
- 页面直接渲染内容，不使用 `PageContainer` 与页面标题区，也不渲染面包屑：AgentFlow 的导航是
  一级平铺菜单，没有真实层级可表达。
- 窄视口（`< 768px`）下侧栏由布局自动切换为抽屉，内容区独占宽度；桌面端保持完整侧栏。

## 测试

- 单元测试（`tests/unit`）：`pnpm test`，覆盖 `lingting-react-ui` 实际安装版本、依赖去重配置的
  推导方式、脚本命令、导航元数据、API 客户端（成功、非成功状态码、非法 JSON、网络失败、
  超时、取消、错误结构解析、路径不带开发地址）、连接文案与构建输出配置。
- 浏览器测试（`tests/e2e`）：`pnpm exec playwright test`，由 Playwright 自动启动 `vp dev`，覆盖
  六个路由、根重定向、刷新后状态、导航切换、404、桌面与窄视口布局、水平溢出与渲染错误，以及
  通过拦截 `/api/v1/health` 验证的已连接、未连接、503 与服务端错误说明。
- 两个测试入口都会输出 `TEST_ARTIFACT_DIR=<绝对路径>`，产物写入操作系统临时目录
  `lingting-agent-flow-ui-tests/run-*`，测试结束后保留，不自动清理。
- 测试默认不依赖 Go 后端、Docker、SSH、Pi CLI 或真实模型服务。

## 参考项目对照

参考项目 `lingting-ai-gateway`（`lingting-ai-gateway-ui`）的实际实现与本次借鉴点：

| 参考项目实现                                       | 本次处理                                       |
| -------------------------------------------------- | ---------------------------------------------- |
| `main.tsx` 用 `AppSidebarLayout` 挂载整站布局      | 沿用                                           |
| `src/router.tsx` 用 `menuRoutes` 数组定义导航      | 沿用，集中到 `src/app/navigation.tsx`          |
| `ProductBrand` 组件承载侧栏产品标识                | 沿用其结构，收窄为 `icon: string` 的本地组件   |
| `index.css` 提供高度链与 `body` 滚动               | 沿用                                           |
| `resolve.dedupe` 手工列举 LRI 的 peer 依赖         | 改为从 `lingting-react-ui/package.json` 推导   |
| `@lri` 别名、`@lingting/ai-gateway-sdk` workspace  | 不引入：本项目直接依赖发布包 `lingting-react-ui` |
| `server.proxy` 指向 `26384` 后端                    | 沿用代理思路，目标改为 Go 服务的 `26380`，可用环境变量覆盖 |
| `mode: desktop` 构建                               | 不引入：桌面构建尚未实现                        |
| `routeType="hash"`（桌面端）                       | 不引入：Web 端使用浏览器 history 路由          |

## 尚未实现

任务、项目、机器、环境的业务 API 与页面，以及 SSH 连接、Docker 容器操作、Git 工作流与 Pi Agent
调用均不在本步骤范围内。页面中的说明文字只描述规划，不代表功能已经可用。
