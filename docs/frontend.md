# 前端与 Web 交付

本文件描述前端工程约束，以及「前端产物如何随单个 Go 可执行文件交付」的规则。服务端启动顺序与后台
服务注册见 [workspace.md](workspace.md)，发布流程见 [release.md](release.md)。

## 1. 工程约束

- 前端根目录固定为仓库根目录下的 `lingting-agent-flow-ui/`，与 Go 代码不共享模块。
- 组件库固定为 `lingting-react-ui@2026.10.0`，必须精确锁定版本；不得升级、降级或替换为其他版本，也
  不得绕开该库另建一套 UI 设计体系。
- 依赖管理与构建工具固定为 pnpm 与 Vite+（`vp dev` / `vp build` / `vp lint` / `vp test`）。
- 组件选择优先级为 `lingting-react-ui` → Ant Design Pro Components → Ant Design。Ant Design 组件必须在
  确认前两级没有等效能力后才直接使用；任务列表必须使用 `ExTable` 承载分页、筛选与加载状态。
- 参考项目为 `lingting-ai-gateway`（本地 `D:\code\lingting\lingting-ai-gateway\lingting-ai-gateway-ui`）。
  重要布局、路由与导航实现必须先阅读其真实源码，再决定借鉴点；不得只按文档推测，也不得复制只对其
  自身生效的别名、SDK workspace、代理端口与构建模式。
- 导航、页面标题与当前路由高亮必须由同一份路由元数据派生，禁止在多处重复定义路径与标题。
- 前端不直接访问 SQLite，不执行 SSH、Docker、Git 或 Pi 命令，也不承载工作流状态迁移。
- 不得使用虚构数据伪装真实业务状态，不得渲染会使用户误以为业务功能已可用的操作入口。

## 2. 状态与操作可用性

- 任务页面的状态标签必须由 `src/pages/tasks/status.ts` 的单一映射派生，取值与 Go 端 `domain.TaskStatus`
  一一对应；禁止新增前端状态名，也禁止把 `queued` 或中间状态显示为成功或失败。
- 操作可用性必须依据接口返回的 `status_finished` 判定：已停止执行的任务（`completed`、`failed`、
  `cancelled`）不可取消但可以删除，其余状态可以取消但不可删除。
- `status_terminal` 只表达"是否终态"（`completed`、`cancelled`），不得用它代替删除判定。
- 不得在前端伪造状态或跳过服务端校验。
- 环境管理内嵌在设置页（`src/pages/settings/`），不再有独立的环境路由与导航入口。环境可用性只能取自
  服务端返回的 `usable` / `usable_for_remote_task`，不得由版本号、`image_ref`、最新版本或节点名称推断；
  构建是否在途、是否失败、结果是否不确定都必须按服务端返回展示，`202`（已受理）不得显示为构建成功。
- 环境版本只追加：编辑动作只能提交新版本，界面不得暗示可以修改历史版本的 Dockerfile 或构建结果。

## 3. API 调用

- 所有请求必须经由 `lingting-agent-flow-ui/src/lib/api/` 的统一客户端发出；页面不得直接调用 `fetch`，
  也不得各自解析 JSON 或错误。
- 源码固定使用同源相对 API 路径（例如 `/api/v1/health`），禁止在生产构建中硬编码 `localhost`、开发
  端口、局域网 IP 或后端完整 URL。
- 连接状态只能来自真实健康检查响应，禁止使用固定假数据；首次检查完成前不得显示已连接。连接指示器
  只表示 AgentFlow HTTP API 的可用性，不得表达为 SSH、Docker、远程机器或 Pi Agent 已连接。

## 4. 开发模式

- 开发模式保留两个进程：Vite+ 开发服务器负责页面热更新，Go HTTP 服务负责 API。
- Vite+ 的 `/api` 代理只作用于开发环境，代理目标通过 `AGENTFLOW_DEV_API_TARGET` 或开发配置设置。

## 5. 单文件交付

- 生产模式只交付一个 Go 可执行文件：API 与前端静态资源由同一进程提供，前端构建产物通过 `//go:embed`
  嵌入，禁止要求用户在发布后手工复制 HTML、CSS 或 JavaScript。
- 前端构建输出目录固定为 `internal/webui/dist/`；该目录必须保留 `.gitkeep`，使全新检出的仓库仍满足
  `go:embed` 的编译要求。缺少真实构建产物时不得静默返回空白页面。
- 仓库根目录 `Makefile` 的 `release` 是唯一推荐的单文件生产构建入口，必须先构建前端再编译 Go；禁止先
  编译 Go 再构建前端，否则二进制中会残留旧 UI。
- 正式构建缺少 `index.html` 时必须失败，不得产出空白页面或仅含占位资源的可执行文件。
- `agentflow server` 默认开启 `RequireWebUI`，使缺少前端产物的二进制在启动阶段直接失败；只有开发模式
  （页面由 Vite+ 提供）才允许显式 `--require-web-ui=false`。

## 6. 服务入口与请求分发

- `agentflow server` 是 Web 服务与后台服务的唯一启动入口：同一进程内依次初始化配置、数据库与后台
  服务，再启动 HTTP 服务，并由同一处生命周期代码负责启动失败清理与优雅关闭。
- 启动顺序固定为：配置 → 日志与公共依赖 → 数据库 → 后台服务 → HTTP 服务；关闭顺序相反。任一必需
  组件失败或关键后台服务异常退出时，已启动组件必须按逆序清理，不留监听端口与 goroutine。
- 数据库是必需组件：初始化失败时服务必须返回错误并停止启动，不得带着未初始化的存储在后台服务或
  HTTP 层继续运行。
- 后台服务通过 `internal/lifecycle` 的 `Service` 契约（`Name` / `Start` / `Stop`）注册，启动顺序与注册
  顺序一致，停止顺序相反；禁止在 HTTP Handler 内创建长期运行的后台服务。
- 所有后端接口必须使用 `/api` 前缀，当前版本挂在 `/api/v1` 下；新增接口不得使用 `/api` 之外的路径空间。
- API 路径判定必须按路径段边界进行（`/api`、`/api/...`），禁止用 `strings.HasPrefix(path, "/api")`
  之类的判断，否则 `/apiary`、`/apix` 会被误判为 API。
- 请求分发顺序固定为：API 保留路径 → 真实存在的静态资源 → 缺失静态资源 404 → 只有符合页面规则的
  GET/HEAD 请求才允许 SPA 回退到 `index.html`。非 GET/HEAD 的页面请求返回 405，不得回退为 HTML。
- `/api` 与 `/api/...` 是保留路径空间，永远不能回退为 `index.html`；`/api` 本身返回 API 风格的错误响应，
  `/api` 下的未知路径也必须返回统一 JSON 错误。
- 静态资源以文件系统真实存在为准；`assets/` 目录下的路径即使没有扩展名也视为资源，缺失时必须返回
  404，不得伪装成成功的 HTML。带内容哈希的产物使用长期缓存，`index.html` 使用重新验证策略。

## 7. 前端测试

- 前端测试位于 `lingting-agent-flow-ui/tests/`，测试产物写入操作系统临时目录下的随机唯一目录并保留，
  不得自动清理。
- 不得声称未运行的测试已经通过；跳过与失败必须如实报告。测试授权的通用规则见 [testing.md](testing.md)。
