# 组件评估：BuildKit Dockerfile Parser（github.com/moby/buildkit/frontend/dockerfile/parser）

> **状态：已被本轮产品架构决策取代（本文件结论不再实施）。**
> 执行节点预检已移除基础镜像元数据检查，专用 Dockerfile `FROM` 解析器（`internal/dockerfile`）与
> Distribution Inspect 一并删除。节点预检不再需要解析 Dockerfile，也不再查询 `FROM` 镜像元数据；
> 基础镜像可用性由真实的环境构建负责。因此本文就不再为预检维护 Dockerfile 解析逻辑，
> **不引入 BuildKit Parser，也不引入 `distribution/reference`**。以下内容只作为历史评估记录保留。
> **状态：审计结论，未实施。**
> 本项目当前**没有**引入 `github.com/moby/buildkit` 或 `github.com/distribution/reference`：
> `go.mod` / `go.sum` 未修改，`internal/dockerfile/`、`internal/docker/templates.go` 以及其余生产代码未修改，
> 当前仍使用自研的 `dockerfile.Parse`。本文是替换前的可行性审计记录，结论见第 11 节。
>
> **第二轮补充（2026-11-04）**：第 13–17 节补充四项内容——统一 `Parse` 与 `ValidateDockerfile` 的解析依据、
> `--privileged` 的现状与缺口、预定义 `ARG` 取值策略、BuildKit `v0.26.3` 安全核查；§11 结论同步修订。
> 第二轮同样**只改本文件**，未改生产代码、`go.mod` / `go.sum` 与测试。

- 审计对象（仓库侧）：`internal/dockerfile/dockerfile.go`、`internal/docker/templates.go`（`ValidateDockerfile`）、
  `internal/docker/context.go`（`ValidateContent`）、`internal/app/host_validation.go`（`checkBaseImages`）
- 审计对象（上游侧）：`github.com/moby/buildkit` 的 `frontend/dockerfile/parser`，以及必须一起评估的
  `frontend/dockerfile/shell`、`frontend/dockerfile/instructions`、`frontend/dockerfile/dockerfile2llb`
- 候选辅助组件：`github.com/distribution/reference@v0.6.0`（镜像引用规范化）
- 审计时间：2026-11-04
- 审计方式：
  1. 逐文件阅读本仓库实现，用全仓符号搜索核实调用链（不按文件名推测）。
  2. 从 Go 模块代理取得上游源码（`v0.26.3`、`v0.34.2` 两个版本）逐文件阅读，并用 `diff` 比较两版导出符号。
  3. 在**仓库之外**的空模块（`/tmp/bkprobe`，`go 1.24.5`）实测依赖闭包：`go get`、`go mod tidy`、
     `go list -deps`、`go list -m all`、`go build`，全部在本仓库之外执行，本仓库 `go.mod` / `go.sum` 未被触碰。
  4. 全程**未运行任何测试**（上游与本仓库都没有），未连接 Docker / SSH，未执行破坏性 Git 操作。
- 相关文档：[development.md](../development.md)（组件准入规则）、[architecture.md](../architecture.md)（执行链路边界）、
  [docker-compatibility.md](../docker-compatibility.md)（Docker 版本基线）

## 1. 当前自研实现的职责

`internal/dockerfile` 只有一个职责：**从 Dockerfile 文本里找出"需要对远端 Docker 查询元数据的外部基础镜像"**。
包注释已经明确它不做构建、不访问网络、不调用 Docker。

| 关键符号 | 实际职责 |
|---|---|
| `Parse` | 主流程：遍历逻辑行、收集全局 `ARG`、识别 `FROM`、变量展开、阶段别名与 `scratch` 跳过、镜像去重、无法解析则记录 |
| `logicalLines` | 物理行 → 逻辑行：CRLF 归一、丢弃空行与整行注释、按行尾反斜杠合并续行、记录起始行号 |
| `splitInstruction` | 逻辑行 → 指令与参数（`strings.Fields`，无引号语义） |
| `collectArg` / `unquote` | 收集 `FROM` 之前的全局 `ARG` 默认值，去掉两侧引号；无默认值的 `ARG` 故意不写入映射 |
| `parseFrom` | `FROM [--platform=…] <image> [AS <name>]` 的字段解析 |
| `expandVariables` / `variablePattern` | 用已知全局 `ARG` 替换 `${VAR}`、`${VAR:-…}`、`${VAR-…}`、`${VAR:+…}`、`${VAR+…}`、`$VAR`；仍有 `$` 残留或取值不确定则返回"无法解析"原因 |
| `ResolveReference` | 补齐默认 `:latest`：含 `@` 原样返回，最后一个路径段含 `:` 视为已有 tag，否则追加 `:latest` |

`internal/docker/templates.go` 里的 `ValidateDockerfile` 是**另一件互不相干的工作**：它是项目自有的**安全策略**检查
（非空、含 `FROM`、禁止 `:latest`、经 `ValidateContent` 拦截私钥特征与 Docker Socket），与基础镜像提取没有共享代码。

## 2. 调用链（已核实）

`dockerfile.Parse` 全仓**只有一个调用点**：

```
app.HostValidator.runChecks                       // 四项必需检查：SSH → Docker 版本 → CLI 帮助 → 基础镜像元数据
  → app.HostValidator.checkBaseImages             // internal/app/host_validation.go
    → app.HostValidator.loadBaseDockerfile        // SFTP 读取该节点最新基础环境版本的 Dockerfile
    │                                             // 无版本时回退 docker.DefaultBaseDockerfile
    → dockerfile.Parse(content)                   // ← 唯一调用点
    → docker.Runner.InspectDistribution(ctx, image.Resolved, timeout)
    │                                             // 只读 GET /v1.52/distribution/{name}/json，不拉取镜像层
    → domain.ImageMetadataResult                  // 写入 domain.ValidationCheck.Images
      → domain.ValidationDetails（JSON 存入 execution_hosts 校验结果）
```

核实要点（避免"按文件名推测"）：

1. **不进入镜像构建**。`app.EnvironmentService.buildLocked` 只把远端 Dockerfile 读回本地构造构建上下文，
   然后调用 `docker.Runner.BuildImage`（`docker build -f …`）；构建时语言环境的 `FROM ${AGENTFLOW_BASE_IMAGE}`
   由远端 `docker build` 自己展开，`BuildSpec.ParentImageRef` 来自**环境版本元数据**（父版本的 `ImageRef`），
   不是解析结果。因此解析器不参与构建参数的解析。
2. **不进入 Dockerfile 保存与版本管理**。`CreateVersion` → `ValidateDockerfile` → `createVersionLocked`
   只计算 `DockerfileSHA256` / `ContextSHA256` 并写远端版本目录与 SQLite；解析结果不出现在版本记录里。
3. **不进入镜像版本与清单记录**。`docker.ImageManifestEntry`（`manifest.go`）记录的是环境、版本、三类哈希、
   `ImageRef`、`ImageID`、`BuiltAt`，与 `dockerfile.BaseImage` 无关。
4. **消费方只用到三类字段**：`checkBaseImages` 只读取 `parsed.Skipped`、`parsed.Unresolved` 与
   `parsed.Images` 的 `Reference` / `Resolved`。`Result.Stages`、`BaseImage.Stage`、`BaseImage.Platform`、
   `BaseImage.Line` 全仓**没有任何消费方**（提示文案里的行号来自 `SkippedFrom.Line` /
   `UnresolvedFrom.Line`；`ImageMetadataResult.Platforms` 来自 Registry 响应，不来自 Dockerfile）。

`ValidateDockerfile` 的调用点（三处，全部是"用户提交 Dockerfile 时的静态校验"）：

```
internal/httpapi/environments.go        EnvironmentCreateVersionRequest.toAppRequest
internal/app/environments.go            EnvironmentService.CreateVersion
internal/cli/docker_env_edit.go         从 stdin 读入 / 编辑器返回后各一次
```

## 3. 当前功能边界与可举证的缺陷

先说明能力边界（不是缺陷）：

- 只识别 `FROM`；`COPY --from` 不参与分析（与"基础镜像元数据"这一检查目标一致）。
- 无法确定取值的 `FROM` 一律 `Unresolved` 并让整项检查失败——这是**fail-closed 设计**，方向正确。
- `scratch`、内部阶段别名不产生网络查询；镜像按 `Resolved` 去重并按首次出现顺序输出。

以下是本次审计中**由代码直接推出**的行为缺陷（每条都给出触发条件，不需要运行即可判断）：

| 编号 | 触发条件 | 实际行为 | 性质 |
|---|---|---|---|
| D1 | Dockerfile 以 UTF-8 BOM 开头，且含 2 条以上 `FROM` | 首行 token 变成 `"\uFEFFFROM"`，第一条 `FROM` 被**静默跳过**（只有 1 条 `FROM` 时会返回"未找到 FROM 指令"） | 静默漏检 |
| D2 | HEREDOC 或跨行 `RUN` 的脚本体里出现以 `FROM ` 开头的文本行 | 该文本行被当成真实 `FROM` 处理：指向真实仓库时会发起多余查询（可能误判失败），指向 `scratch`/别名时会产生假的跳过记录 | 误报 |
| D3 | `${VAR+x}` / `${VAR:+x}`，`VAR` 已设为非空值 | 返回 `VAR` 的**值**，而正确语义是返回 `x`；`VAR` 未定义时又返回 `x`（正确语义是空串） | 语义倒置 |
| D4 | `${VAR-x}`，`VAR` 已定义但值为空串 | 返回 `x`，正确语义是空串 | 语义错误 |
| D5 | `${VAR}`，`VAR` 由 `ARG VAR=""` 定义为空 | 报"变量 VAR 依赖构建期参数，静态解析无法确定" | fail-closed 但原因错误 |
| D6 | `ARG BASE=${OTHER}`（嵌套引用）后 `FROM ${BASE}` | 展开结果仍含 `$` → 报"仍包含未解析的变量" | 不能处理嵌套 |
| D7 | `FROM node:latest as build`（小写 `as`）、`FROM node:latest # 说明`（行尾注释）、`FROM\tnode:latest`（Tab 分隔）、`FROM node`（省略 tag） | `ValidateDockerfile` 的 `:latest` 规则全部**绕过**（前两条因 ` AS ` / 后缀匹配不成立，第三条因前缀要求字面空格连 `FROM` 都识别不到，第四条是规则本身没覆盖隐式 `latest`） | 安全校验可绕过 |
| D8 | 引用不合法：`FROM Node:LATEST`、`FROM node:`、`FROM localhost:5000` | 不加校验直接送进元数据查询，最终表现为 Registry 侧失败而不是本地可读的引用错误 | 缺少引用校验 |
| D9 | `# escape=`` ` 指令、`# syntax=` / `# check=` 指令 | 全部当普通注释忽略；反引号转义的续行无法识别 | 语法覆盖缺口 |
| D10 | `FROM --platform=$BUILDPLATFORM …` | `--platform` 值原样保存且**从不参与**变量展开或校验（`BaseImage.Platform` 无消费方） | 形同未解析 |
| D11 | 单行超过 64 KiB | 自研实现无上限（整个文件受 4 MiB 上下文上限约束） | 与上游严格度不同 |

现有测试覆盖（只阅读，未运行）：`internal/dockerfile` **没有专属测试文件**；
`dockerfile.Parse` 在 `tests/` 里**没有任何调用点**；`tests/unit/image/image_test.go` 只覆盖 `ValidateDockerfile`
与内置模板（`TestValidateDockerfile`、`TestDefaultBaseDockerfileHasNoLatestOrSecrets`）。
也就是说：D1–D11 目前没有任何自动化守点。

## 4. 上游版本、维护状态、许可证与依赖规模

### 4.1 版本与 Go 版本门槛（关键约束）

`go.mod` 使用 `go 1.24.5`，本机工具链也是 `go1.24.5`（`GOTOOLCHAIN=auto`）。上游 `go` 指令随版本抬升：

| buildkit 版本 | `go` 指令 | 发布时间 |
|---|---|---|
| v0.24.0 | 1.23.0 | 2025-09-03 |
| v0.25.0 | 1.24.0 | 2025-09-30 |
| **v0.26.3** | **1.24.3** | **2025-12-16** |
| v0.27.0 | 1.25.0 | 2026-01-21 |
| v0.30.0 | 1.25.5 | 2026-05-13 |
| v0.33.1 | 1.26.8 | 2026-09-30 |
| v0.34.2（当前最新） | 1.26.8 | 2026-11-03 |

结论：**与本项目 `go 1.24.5` 兼容的最新版本是 `v0.26.3`**（`go 1.24.3`）。
`v0.27.0` 起要求 Go ≥ 1.25，`v0.33.1` 起要求 Go ≥ 1.26.8；在本项目当前 Go 版本下使用 `v0.34.2`
只会得到"module requires go >= 1.26.8"的失败，或在 `GOTOOLCHAIN=auto` 下**静默下载并切换工具链**——
后者违反"环境与依赖可预期"的常识，不可接受。因此任何替换方案都必须把 buildkit 固定在 `v0.26.3`。

### 4.2 维护状态与许可证

- 仓库：`github.com/moby/buildkit`（Moby 组织官方项目，Docker BuildKit 本体）。
- 发布节奏：稳定版本持续发布至 `v0.34.2`（2026-11-03），无停更迹象。
- 许可证：Apache-2.0（`LICENSE` 第 1 行即 Apache License 2.0），与本项目兼容。
- API 稳定性（实测 diff）：`frontend/dockerfile/parser` 的包级导出声明（`Node`、`Heredoc`、`Result`、
  `Warning`、`LocationError`、`Range`、`Position`、`Directive`、`DirectiveParser`、`Parse`、
  `ParseHeredoc`、`MustParseHeredoc`、`ChompHeredocContent`、`WithLocation`、`SetLocation`、
  `DetectSyntax`、`ParseDirective`、`DefaultEscapeToken`）与 `*Node` 的方法集，在 `v0.26.3` 与 `v0.34.2`
  之间**完全一致**；差异只在实现细节（`Dump` 内部改用 `strings.Builder`、heredoc 内容拼接方式）与
  `DirectiveParser.setComment` → `SetComment` 这一处**改为导出**的增量变化。
  `frontend/dockerfile/shell` 的导出符号同样一致（`Lex`、`NewLex`、`ProcessWord`、`ProcessWords`、
  `ProcessWordWithMatches`、`ProcessWordResult`、`EnvGetter`、`EnvsFromSlice`），差异在内部 `processStopOn`
  的签名（未导出）。
- 未发现 `Deprecated` 标记的导出项。

### 4.3 依赖规模（实测，`v0.26.3`，`go1.24.5`）

| 引入范围 | 模块数 | 非标准库包数 | 编译闭包总包数 |
|---|---|---|---|
| `frontend/dockerfile/parser` | 6 | 36 | 113 |
| `frontend/dockerfile/instructions` | 12 | 48 | 128 |
| `frontend/dockerfile/dockerfile2llb` | 49 | 300 | 508 |

`parser` 的 6 个模块：`moby/buildkit`、`pkg/errors`、`containerd/typeurl/v2`、`planetscale/vtprotobuf`、
`gogo/protobuf`、`google.golang.org/protobuf`。

**关键事实：protobuf 依赖无法规避。** `parser/errors.go` 无条件 `import "github.com/moby/buildkit/util/stack"`，
而 `util/stack` 的 `init()` 会 `typeurl.Register(...)`，于是 `containerd/typeurl/v2` → `protobuf` /
`gogo/protobuf` / `vtprotobuf` 整条链进入编译闭包（`go list -deps` 实测确认，且包内 `import` 列表里没有
`net`、`net/http` 或任何 Docker 客户端——**它确实只做纯语法解析**）。

供应链代价（实测）：空模块 `go mod tidy` 后 `go.mod` 增加 7 条 require（1 直接 + 6 间接），
`go.sum` 落到 224 行（其中 213 行是 buildkit 模块图内各模块的 `go.mod` 哈希），`go list -m all` 列出 **190 个模块**
（Go 1.17+ 模块图裁剪生效，这些模块不会全部进入构建，但会进入模块图与 `go.sum`）。
同机对照：只打印一行的 hello-world 可执行文件 2.30 MB，导入 `parser` 的探针可执行文件 6.1 MB。

`distribution/reference@v0.6.0` 的代价小得多：`go 1.20`，只依赖 `opencontainers/go-digest v1.0.0`，
Apache-2.0，最新版本仍是 `v0.6.0`（2024-03-20），属于"事实冻结"的稳定库。

## 5. Parser 的 API 与 AST 结构

```go
func Parse(rwc io.Reader) (*Result, error)

type Result struct {
    AST         *Node
    EscapeToken rune      // 实际生效的转义符（受 # escape= 指令影响）
    Warnings    []Warning
}

type Node struct {
    Value       string          // 指令名或参数字面量（大小写按原文保留）
    Next        *Node           // 同级下一个 token
    Children    []*Node         // 子节点（指令参数）
    Heredocs    []Heredoc       // 挂在该指令上的 heredoc 内容
    Attributes  map[string]bool // 例如 json
    Original    string          // 原始行
    Flags       []string        // 指令前的 --flag（只有顶层节点有）
    StartLine   int
    EndLine     int
    PrevComment []string        // 紧邻该指令的注释
}
```

实测要点（读源码得出，不依赖运行）：

1. **参数表达方式**：`FROM` 走 `parser.parseStringsWhitespaceDelimited`，参数按空白切分为 `Children` 链，
   **不做引号处理**；`--platform=…` 由 `splitCommand` → `extractBuilderFlags` 抽到 `Node.Flags`，值保持原样。
   `ARG` 走 `parseNameOrNameVal`（`parseWords`），**保留引号与转义符原文**，因此调用方仍要自己去引号。
2. **错误信息与位置**：错误统一包成 `*parser.LocationError{Locations [][]Range, error}`，`Range{Start,End Position{Line,Character}}`，
   可用 `errors.As` 取行号；`parser.WithLocation(err, node.Location())` 可以把调用方自己的错误补上行号。
   语法错误、未终止 heredoc、重复 parser directive、`# escape=` 取值非法都会走这条路径。
3. **指令名大小写**：`Dispatch` 用 `strings.ToLower(cmd)` 查表，`Node.Value` 保留原文；未知指令走 `parseIgnore`
   **不报错**（只留指令名与空参数节点），因此"未知指令"由调用方决定是否视为错误。
4. **不做的事**（决定替换边界的关键）：
   - **不展开变量**。`FROM ${X}` 在 AST 里就是字面量。BuildKit 自己在 `dockerfile2llb/convert.go` 里用
     `shell.Lex`（`shlex.ProcessWordWithMatches(st.BaseName, globalArgs)`）展开，`globalArgs` 由
     `defaultArgs()` 注入 `BUILDPLATFORM`、`TARGETPLATFORM` 等**预定义构建参数**，再由 `buildMetaArgs` 处理全局 `ARG`。
   - **不判断阶段别名、不识别 `scratch`**。这一步同样在 `dockerfile2llb`：`addState` 用
     `statesByName[stage.BaseName]` 判断"这是上一个阶段"，`emptyImageName = "scratch"` 也是 builder 侧常量。
   - **不规范化镜像引用**。`dockerfile2llb` 用 `github.com/distribution/reference`：
     `reference.ParseNormalizedNamed(name)` + `reference.TagNameOnly(ref).String()`。
   - **不要求存在 `FROM`**。只有"文件里没有任何指令"会报错（`file with no instructions`）；
     `RUN echo hi` 这样的文件解析成功。`FROM` 参数个数与阶段名校验在 `instructions.parseFrom`
     （`FROM requires either one or three arguments`、`validStageName`），不在 `parser` 里。
5. **明确覆盖的语法**：`# syntax=` / `# escape=` / `# check=` parser directive（`directives.go`，重复指令报错）、
   BOM 丢弃、任意换行风格（`scanLines` + `trimNewline` 处理 `\r\n`）、续行、空续行**警告**、
   HEREDOC（`ADD` / `COPY` / `RUN`，含 `ONBUILD` 内层）、`FROM --platform`、注释关联（`PrevComment`）。
   单行超过 `bufio.MaxScanTokenSize`（64 KiB）时报错。

## 6. 行为逐项对照

### 6.1 Dockerfile 语法

| 行为 | 当前自研实现 | BuildKit `parser` | 说明 |
|---|---|---|---|
| 空内容 | 报错"内容为空" | 报错 `file with no instructions` | 都报错，文案不同 |
| 只有注释/空行 | 同"内容为空" | 同 `file with no instructions` | 一致 |
| 缺少 `FROM`（如只有 `RUN`） | 报错"未找到 FROM 指令" | **不报错** | 换用后须由项目补"必须存在 FROM" |
| 注释与空行 | 丢弃（续行中的注释行也丢弃） | 丢弃，并挂到 `Node.PrevComment` | 上游多一条可用信息 |
| CRLF 输入 | `ReplaceAll("\r\n","\n")` 后 `TrimSpace` | `trimNewline` 去掉行尾 `\r\n` | 两者都能处理 |
| 行尾反斜杠续行 | 支持（`HasSuffix("\\")`，含被转义的 `\\` 也当续行） | 支持，正则排除被转义的转义符 | 上游更精确 |
| 反引号转义 `# escape=`` ` | 不支持 | 支持（`EscapeToken`） | 上游更完整 |
| parser directive | 全部当注释 | `syntax` / `escape` / `check`，重复报错 | 上游更完整 |
| 指令大小写 | `strings.ToUpper` 后比较 | `ToLower` 查表，`Value` 保原文 | 一致 |
| `FROM image` / `FROM image AS stage` | `parseFrom` 手写 | `Children` 链（1 或 3 个），`Flags` 分开 | 上游结构化更好 |
| `FROM --platform=…` | 手写抽取，**不展开变量、无消费方** | `Node.Flags`，值原样 | 二者都不展开；展开在 builder 侧 |
| 多阶段构建 | 支持 | 支持（AST 里就是多个子节点） | 一致 |
| 内部阶段别名引用 | 支持（`stageAliases` 小写比较，先登记后判断） | **parser 不管**；`dockerfile2llb` 才判断 | 换用后此逻辑仍必须自己写 |
| `FROM scratch` | 支持（`EqualFold`） | **parser 不管**；builder 侧 `emptyImageName` | 同上 |
| HEREDOC | **不支持**（脚本体被当普通行，产生 D2 类误报） | 支持（`Heredoc{Name,FileDescriptor,Expand,Chomp,Content}`） | 上游明显更完整 |
| 非法语法与错误行号 | 报"FROM 指令缺少镜像引用"等自造文案，行号为逻辑行起始物理行 | `*LocationError` + `Range.Start.Line`，逐层可加位置 | 上游位置信息更精确 |
| 单行长度上限 | 无 | 64 KiB（`bufio.MaxScanTokenSize`） | 上游更严 |

### 6.2 ARG 与变量展开

下表"BuildKit 实际"列指 `frontend/dockerfile/shell`（`Lex.processDollar`）的行为——它是 BuildKit 展开 `FROM`
与 `--platform` 时实际使用的实现，`parser` 只负责把原样字符串交出来。

| 行为 | 当前自研实现 | BuildKit `shell.Lex` | 差异 |
|---|---|---|---|
| `FROM` 之前的全局 `ARG` | 收集默认值；无默认值不写入 | 调用方用 `buildMetaArgs` 逐条处理 | 一致（都要自己做） |
| `ARG NAME`（无默认值） | 不写入 → 使用时 Unresolved | 由调用方决定（BuildKit 用 `--build-arg` 或空） | 一致（fail-closed） |
| `ARG NAME=""` | 写入空串，但 `${NAME}` 被判为"依赖构建期参数" | 展开为空串 | **当前实现误报（D5）** |
| `${VAR}` | 支持 | 支持（`unset` 与 `set-empty` 区分） | 上游语义更准 |
| `$VAR` | 支持 | 支持 | 一致 |
| `${VAR:-default}` | 支持（空值与未定义都用默认值） | 支持（`nullIsUnset` 语义） | 一致 |
| `${VAR-default}` | `VAR` 已定义但为空时错误返回默认值 | 返回空串 | **当前实现错（D4）** |
| `${VAR:+default}` | 已定义非空时返回值、未定义时返回 `default` | 已定义非空时返回 `default`、未定义时返回空 | **完全倒置（D3）** |
| `${VAR+default}` | 同上 | 同上 | **完全倒置（D3）** |
| `${VAR?msg}` / `${VAR:?msg}` | 未实现（落到"依赖构建期参数"） | 支持（转成错误） | 上游更完整 |
| `${VAR#pat}` / `${VAR%pat}` / `${VAR/a/b}` | 不支持（正则遇 `}` 截断） | 支持 | 上游更完整 |
| 未知变量 | Unresolved（fail-closed） | `SkipUnsetEnv=true` 时保留 `${VAR}` 并把变量记入 `Unmatched` | 语义接近，上游可区分"未匹配变量集合" |
| 空变量 | 见 D5 | 展开为空串，`FROM` 为空时 builder 报 `base name should not be blank` | 上游更准 |
| 多变量组合 | 逐 token 替换 | 支持（含嵌套 `${A:-${B}}`） | 上游更完整 |
| 变量形成的镜像名 | 支持（仅全局 `ARG` 默认值） | 支持（含 `BUILDPLATFORM` 等预定义参数） | 上游多一层预定义参数 |
| 嵌套 `ARG A=${B}` | 报"仍包含未解析的变量"（D6） | 处理 `ARG` 时先展开再入表 | **当前实现不能处理** |
| 构建期无法确定的参数 | 判 Unresolved 并让检查失败 | 由调用方决定（agent 应当同样 fail-closed） | 策略一致，能力不同 |
| 转义符 | 固定 `\` | 由 `EscapeToken` / `NewLex(escapeToken)` 决定 | 上游更完整 |

补充风险（跨平台）：`shell` 包在 `GOOS=windows` 下把环境变量名**大写归一**（`NormalizeEnvKey`），
`GOOS=linux` 下不做归一。若直接使用 `shell.EnvsFromSlice`，同一份 Dockerfile 在 Windows 与 Linux 上会得到
不同结果（例如 `ARG base=…` + `FROM ${BASE}`）。规避方式：不用 `EnvsFromSlice`，自己实现
`EnvGetter`（三个方法）以保持大小写敏感。这条**必须在方案 C 里写死**。

### 6.3 镜像引用解析

| 行为 | 当前自研 `ResolveReference` | `distribution/reference@v0.6.0` | 说明 |
|---|---|---|---|
| 省略 tag → `:latest` | 追加 `:latest` | `TagNameOnly(ref)`（仅当 `IsNameOnly`） | 语义一致 |
| 已指定 tag | 原样返回 | 原样返回 | 一致 |
| digest 引用 | 含 `@` 原样返回 | 原样返回 | 一致 |
| registry 端口 `reg:5000/ns/repo` | 取最后路径段判断，端口不被误判 | 由完整语法解析 | 一致，但上游更严谨 |
| 多级仓库路径 | 一致 | 一致 | — |
| 域名规范化 | 不规范化（`node` 就是 `node`） | `ParseNormalizedNamed` → `docker.io/library/node` | **行为差异**：`Resolved` 会变长；显示可用 `FamiliarString` |
| 非法引用 | 不校验，直接进查询（D8） | 报错（大小写、空 tag、非法字符、纯 64 位十六进制） | 上游更安全 |
| 隐式 `latest` 识别 | 只有显式 `:latest` 才被判为 latest（D7） | `TagNameOnly(ref)` 结果里 tag 为 `latest` 即可判定 | 上游能堵住 D7 的第四条 |
| 内部阶段别名 | 由 `stageAliases` 处理 | 不处理（属 builder 职责） | 仍须自研 |
| 镜像去重 / 首次出现顺序 | 已实现 | 不提供 | 仍须自研 |
| 平台信息 | 原样保存 `--platform`，无消费方 | 不处理 | 两者都不解决"按平台查元数据" |
| 原始引用 vs 解析后引用 | `BaseImage.Reference` 实际已是**展开后**的引用，`Resolved` 是补 tag 后的引用 | 可以额外保留"展开前原文"（`Node.Original`） | 换用后可补齐这个区分 |

结论：**`FROM` 能被解析，不等于镜像引用规范化已经解决。** 这一层与 Dockerfile 语法解析是两个独立职责，
`distribution/reference` 才是对应组件（BuildKit 自己也是这么分工的，见 `dockerfile2llb/convert.go`）。

## 7. 项目安全规则的保留方案

先按"是否属于语法解析"分类。**第三方 Parser 不能替代其中任何一条策略判断。**

| 规则 | 当前实现位置 | 归类 | 换用 Parser 后的归属 | 受影响吗 |
|---|---|---|---|---|
| 禁止写入 API Key / 密码 / Token / SSH 私钥 | `docker.ValidateContent` 的私钥头特征匹配 | 项目安全策略（文本特征） | 保持自研，与解析器无关 | 不受影响 |
| 禁止挂载 Docker Socket | `docker.ValidateContent` 匹配 `/var/run/docker.sock` | 项目安全策略 | 保持自研 | 不受影响 |
| 禁止 `--privileged` | **代码中无 Dockerfile 级实现**（`container.go` 只在创建容器时不使用 `--privileged`；`templates.go` 的注释提到但未实现） | 项目安全策略 | 需要新增；可用 AST 也可用文本匹配 | 与解析器无关，但**注释与实现不一致**，建议单独修正 |
| 禁止 `latest` 作为唯一基础镜像标签 | `ValidateDockerfile` 逐行字符串匹配 | 项目安全策略（依赖引用解析质量） | 保留自研判定；**可用 `distribution/reference` 收紧**（堵 D7） | 可增强 |
| 无法确定动态 `FROM` 时必须失败 | `dockerfile.Parse` → `Unresolved` → `checkBaseImages` 直接返回失败 | 项目安全策略（fail-closed） | 必须自研保留 | 逻辑不变，判定依据可换成 `Lex` 的 `Unmatched` |
| `scratch` 与内部阶段别名不得触发外部镜像查询 | `Parse` 内的 `EqualFold(scratch)` + `stageAliases` | 项目安全策略（语义判断） | 必须自研保留（上游在 `dockerfile2llb` 里，代价不可接受） | 逻辑不变 |
| 外部镜像检查只读、不拉取镜像层 | `docker.Runner.InspectDistribution`（`GET /distribution/{name}/json`） | 网络行为，与解析无关 | 不变 | 不受影响 |

## 8. A / B / C 三种方案比较

### 方案 A：完全保留当前自研实现

- 可删除代码：无。
- 必须保留：全部。
- 依赖与供应链：不变。
- 双实现：无。
- 错误语义：不变。
- 业务功能：**继续带着 D1–D11**。其中 D1（BOM）、D2（heredoc 误报）、D3/D4（变量语义倒置）、D7（安全校验可绕过）都是可举证的缺陷，且没有任何自动化守点。
- 长期维护成本：最低的依赖成本，但要自己继续修语法边角。

### 方案 B：仅引入 `parser` 替换语法解析

- 可删除：`logicalLines`、`splitInstruction`、`parseFrom`（含 `fromInstruction`）以及 `Parse` 里"逐行遍历 + 切字段"的骨架。
- 必须保留：`collectArg`/`unquote`、`expandVariables`、`ResolveReference`、阶段别名与 `scratch` 跳过、去重、`Result` 类型与全部策略判断。
- 解析结果结构需要重定义：`Parse` 改为"遍历 AST → 取 `Value=="from"` 的节点 → `Flags` 取 platform、
  `Children` 取 image/AS"，对外 `Result` / `BaseImage` 保持不变（`host_validation` 不受影响）。
- 依赖增加：6 个模块 / 36 个包 / 二进制约 +3.8 MB；`go` 指令锁死 buildkit ≤ `v0.26.3`。
- 双实现：不会立刻产生，但**变量展开与引用规范化仍是自研**，与"上游同族的 `shell.Lex` / `reference`"形成两套语义（同一份 Dockerfile 我们判定的和 `docker build` 判定的可能不同）。
- 错误语义变化：`FROM` 缺失不再由解析器报错（要自己加）；非法语法从"自造文案"变成 `*LocationError`（行号更准，判定更宽/更严的具体点见第 6 节）。
- 业务覆盖：修好 D1（BOM）、D2（heredoc）、D9（directive/escape）、D11（行长）；**没修** D3–D6（变量展开）、
  D7–D8、D10（引用与平台）。
- 结论：拿到的是"更准确的语法层"，代价却是 6 个模块与一个 Go 版本天花板，而收益最小的一块恰好是自研最不容易出错的一块。

### 方案 C：`parser` + 同模块 `shell.Lex` + `distribution/reference`

- 可删除（按符号）：`logicalLines`、`splitInstruction`、`parseFrom`/`fromInstruction`、`unquote`、
  `variablePattern`、`expandVariables`、`ResolveReference`。
- 必须保留（按符号）：`Parse` 的策略骨架（跳过/未解析/去重/顺序）、`Result`、`BaseImage`、`SkippedFrom`、
  `UnresolvedFrom`、`collectArg` 的"无默认值不入表"规则，以及全部 `ValidateDockerfile` 安全策略。
- 需要的新代码（规模远小于被删代码）：
  1. AST 遍历取 `FROM`（`Flags` + `Children` + `StartLine`）；
  2. 用 `shell.NewLex(result.EscapeToken)` + 自实现 `EnvGetter`（**不用 `EnvsFromSlice`**，避免 Windows 大小写归一）；
     `SkipUnsetEnv = true`，用 `ProcessWordWithMatches` 返回的 `Unmatched` 决定 fail-closed；
     另需按 `defaultArgs()` 的名单注入 `BUILDPLATFORM` 等**预定义参数**，否则合法的 `FROM --platform=$BUILDPLATFORM`
     仍会被判无法解析（这一步是"补齐语义"，不是"新增功能"）；
  3. 用 `reference.ParseNormalizedNamed` + `TagNameOnly` 规范化，用 `FamiliarString` 保持对外显示与现状一致；
  4. `FROM` 存在性、阶段别名、`scratch`、去重、`Unresolved` 判定照旧自研。
- 依赖增加：6 个模块（`parser` + `shell` 同属 buildkit，**不额外增加**）+ `distribution/reference` 与
  `opencontainers/go-digest` 2 个模块（后者又恰好在 buildkit 的模块图里，`go.sum` 已覆盖）。
- 双实现：**不产生**。语法层、变量展开、引用规范化各只有一条路径，且与 `docker build` 用的是同源实现
  （远端 Engine 的经典 builder 同样使用 `frontend/dockerfile/parser`）。
- 错误语义变化：需要一次性重定 5 类文案（空文件、无 `FROM`、未知变量的 `Unmatched`、非法引用、heredoc 未终止），
  `host_validation` 的 `ErrorCode`（`dockerfile_unparsable` / `dockerfile_unresolved` /
  `dockerfile_no_external_image`）**保持不变**即可，接口契约不受影响。
- 业务覆盖：修 D1、D2、D3、D4、D5、D6、D7（含隐式 `latest` 判定）、D8、D9、D11；D10 仍需要自己把
  `--platform` 送进 `Lex` 展开（否则保持现状的"原样保存"）。
- 抽象层风险：只要**不引入 "Dockerfile 模型层"**（即不引入 `instructions` / `dockerfile2llb`），就不会多出无价值的中间层；
  `instructions` 已经把 `docker/docker-image-spec`、`opencontainers/image-spec`、`tonistiigi/go-csvvalue` 等
  拉进闭包（12 模块 / 48 包），`dockerfile2llb` 更是 49 模块 / 300 包（含 containerd v2、gRPC、OpenTelemetry），
  与本项目"直接依赖只有 6 个、优先标准库"的现状明显不符。**这两层都不应引入。**

## 9. 可以删除的代码与必须保留的代码

**方案 C 下可删除（`internal/dockerfile/dockerfile.go`）**：
`logicalLine`、`logicalLines`、`splitInstruction`、`fromInstruction`、`parseFrom`、`unquote`、
`variablePattern`、`expandVariables`、`ResolveReference`。
`ResolveReference` 是 `internal/dockerfile` 的导出符号，全仓无外部调用方（只有 `Parse` 自己用），删除不影响 `app`。

**必须保留（不得下沉给第三方）**：
1. `Result` / `BaseImage` / `SkippedFrom` / `UnresolvedFrom` 四个类型及其字段语义——它们是
   `domain.ImageMetadataResult` 与 `domain.ValidationCheck` 的输入契约。
2. `Parse` 的四条策略：静态路径别名/`scratch` 跳过、镜像去重与首次出现顺序、动态 `FROM` 必须 fail-closed、
   行号透传到用户提示文案。
3. `collectArg` 的"无默认值不入表"规则（这是 fail-closed 的前提）。
4. `docker.ValidateDockerfile` 与 `docker.ValidateContent` 的全部安全判定（第 7 节）。
5. `docker.Runner.InspectDistribution` 只读查询路径与 `MetadataCode*` 错误分类。

## 10. 风险与未确认事项

| 风险 | 影响 | 缓解 |
|---|---|---|
| Go 版本天花板 | buildkit 只能停在 `v0.26.3`；`v0.27.0+` 要求 Go ≥ 1.25，`v0.33.1+` 要求 ≥ 1.26.8 | 固定版本并在 `go.mod` 注释写明原因；升 Go 时同步评估升级 buildkit |
| `GOTOOLCHAIN=auto` 静默换工具链 | 误用新版 buildkit 时本机构建悄悄改用另一套工具链 | CI/本机构建显式 `GOTOOLCHAIN=local`，或固定版本 |
| protobuf 依赖链 | 6 个模块、二进制约 +3.8 MB 只为 `util/stack` 的错误栈功能 | 接受（无法规避）；不引入 `instructions` / `dockerfile2llb` 以免进一步扩大 |
| Windows 大小写归一 | `shell.EnvsFromSlice` 在 Windows 下把变量名大写，与 Linux 行为不一致 | 自实现 `EnvGetter`，禁用 `EnvsFromSlice` |
| 三处语义需补齐 | 预定义构建参数（`BUILDPLATFORM` 等）不注入就会把合法 Dockerfile 判成"无法解析"；正则/`#`/`%` 替换语义；`FROM` 空引用 | 在方案实施时按 `dockerfile2llb` 的依据逐项补齐，并把"无法解析"仍作为失败 |
| 校验严格度变化 | 上游更严（64 KiB 行上限、非法引用报错、directive 重复报错）也可能更宽（无 `FROM` 不报错） | 一次性在 `Parse` 里显式补"必须存在 FROM"，其余按第 6 节逐项确认 |
| 双权威解析 | 本地库版本与远端 Engine 版本可能不同，出现"本地可解析、远端 `docker build` 失败"或反向 | 固定 buildkit 版本并在 `docs/docker-compatibility.md` 记录；远端仍以 `docker build` 结果为准 |
| `--privileged` 规则未实现 | `ValidateDockerfile` 注释声称禁止，代码里没有 | 属独立缺陷，本次不修；实施替换时一并决定归属 |
| 无自动化守点 | `dockerfile.Parse` 目前没有任何测试调用点 | 替换时按测试规范新增守点（需用户另行授权，本轮不做） |

未确认事项（不作断言）：

1. 上游 parser 是否发生过与其语法解析相关的安全公告/CVE——本轮**未**核查安全公告数据库，仅确认了依赖闭包与发布节奏。（第二轮已核查，见第 16 节。）
2. `v0.26.3` 之后到 `v0.34.2` 之间 heredoc / directive 行为是否有**语义**变化（本次只做了导出符号比较与
   diff 抽查：`parser.go`、`directives.go` 的差异看起来是重构性质，未逐版本核对全部中间版本）。
3. 本项目是否有近期升 Go 版本（≥ 1.25 / 1.26）的计划——若有，方案 C 的版本天花板会自然消失，评估结论会更强。
4. `docker build` 在远端 Engine 上对"未指定 tag"的镜像是否与 `TagNameOnly` 完全一致（未在真实 Engine 上验证，本轮不允许连接 Docker）。

## 11. 最终结论

> **已被取代**：本节的历史结论是"部分值得替换"。执行节点预检移除基础镜像元数据检查后，专用 Dockerfile
> `FROM` 解析能力已随 `internal/dockerfile` 删除，`ValidateDockerfile` 的安全性由现有的内容安全校验独立
> 承担。因此不再实施"替换 parser"或"引入 `distribution/reference`"，本结论仅作记录。

历史结论（已失效）：**部分值得替换。**

具体范围：

1. **值得替换：Dockerfile 语法层** —— 用 `frontend/dockerfile/parser` 替换 `logicalLines` / `splitInstruction` /
   `parseFrom`。理由是它的覆盖面和错误定位明显更强，且当前实现在这里有**可举证的误报与漏检**
   （D1 BOM 静默跳过、D2 heredoc 内伪 `FROM`、D9 directive/escape 缺失），这些恰恰是"字符串切分"无法可靠解决的问题。
2. **值得替换：变量展开** —— 用同一模块内的 `frontend/dockerfile/shell` 替换 `variablePattern` / `expandVariables`。
   `shell` 已经是 `parser` 的编译依赖（**零额外模块成本**），而当前实现存在**语义倒置**（D3：`${VAR+x}` / `${VAR:+x}`
   在变量非空时返回值而不是替代值、在变量未定义时返回替代值而不是空串）与嵌套不可用（D6）。
   `Unmatched` 返回值能更精确地支撑"无法确定即失败"的既有策略。
3. **值得替换：镜像引用规范化** —— 用 `github.com/distribution/reference` 替换 `ResolveReference`。
   它是 Docker 生态的事实标准，BuildKit 在**完全相同的用途**上就是用它（`ParseNormalizedNamed` + `TagNameOnly`），
   只增加 2 个模块，并能顺带堵住 D8（非法引用）与 D7 中的隐式 `latest`。
4. **不值得替换：策略层** —— 阶段别名判断、`scratch` 跳过、镜像去重与顺序、`Unresolved` fail-closed、
   `ValidateDockerfile` / `ValidateContent` 的全部安全规则必须留在本项目。上游对应能力只存在于
   `dockerfile2llb`（49 模块 / 300 个非标准库包，含 containerd v2、gRPC、OpenTelemetry），
   引入它等于用一整套构建器替换"提取 FROM"这个单一职责，与项目"优先标准库、直接依赖只有 6 个"的现状冲突。

不选择"完全保留"（方案 A）的关键理由：缺陷不是风格问题，而是**安全相关规则可被绕过**（D7）与**静默漏检**（D1），
且当前没有任何自动化守点。不选择"只换 parser"（方案 B）的关键理由：它只修好语法层，却让变量与引用继续用自研语义，
等于在同一份 Dockerfile 上并存两套判定标准，与"不得制造双实现"的原则冲突，收益与依赖成本也不匹配。

实施前提（若结论被采纳）：buildkit **必须固定 `v0.26.3`**（Go 1.24.3 ≤ 本项目 1.24.5），不引入
`instructions` / `dockerfile2llb`，不使用 `shell.EnvsFromSlice`，`Result` / `BaseImage` 的对外语义与
`ErrorCode` 保持不变。**本轮不实施。**

### 11.1 第二轮修订（2026-11-04）

本节结论不变（**部分值得替换**，范围仍是"语法层 + 变量展开 + 引用规范化"，策略层留在本项目），
但有四处需要按第二轮证据收紧或更正：

1. **`ValidateDockerfile` 必须与 `dockerfile.Parse` 共用同一份 AST 事实**，不得保留第二套 `FROM`
   字符串解析；两条路径共用"事实层"，但"策略层"分别定义（§13）。
2. **`--privileged` 不是 Dockerfile 指令**，第一轮把它记为"校验缺失"不准确：真实情况是
   `ValidateDockerfile` 的注释写了不存在的能力，容器创建层从不使用 `--privileged`（§15）。
   该项**不纳入**本次替换的实施范围。
3. **预定义构建参数一个都不能注入**：上游 v0.26.3 实际有 11 个预定义名（第一轮只列了 8 个），
   其取值全部依赖构建上下文，本项目没有任何可靠来源，注入即等于伪造事实（§14）。
4. **`v0.26.3` 的安全核查已完成**（第一轮未做）：没有任何已知漏洞影响 `parser` / `shell` /
   `util/stack`，但 0.26 版本线在 `v0.26.3` 之后**再无补丁发布**，因此"固定 v0.26.3"只在
   "不导入 `parser`/`shell` 之外的任何 buildkit 包"这一硬边界下成立（§16、§17）。

## 12. 未来重新评估的触发条件

满足任一条再重新评估，不要凭"上游更知名"引入：

1. 本项目 `go.mod` 的 Go 版本升到 ≥ 1.25（理想 ≥ 1.26.8）：buildkit 可跟随最新版本，第 4.1 节的版本天花板消失，
   方案 C 的成本进一步下降。
2. 出现真实可复现的漏检/误判：例如用户 Dockerfile 用 HEREDOC 或 BOM 导致基础镜像检查结论错误（D1/D2 从"理论缺陷"
   变成"线上事实"）。
3. 需求扩展到需要完整指令级语义：例如要求校验 `COPY --from` 引用的阶段、`RUN --mount` 目标、
   或按 `--platform` 区分元数据查询——此时语法层之上必须要有类型化模型，`instructions` 才开始具备性价比。
4. `dockerfile.Parse` 需要覆盖的调用点从 1 处增长到 3 处以上（例如构建前预检、CLI `env lint`、导入 Dockerfile 的接口），
   自研解析的维护成本才会超过 6 个模块的依赖成本。
5. 上游把 `util/stack` 从 `parser/errors.go` 中移除（protobuf 依赖链消失），或提供独立的、无 protobuf 依赖的
   Dockerfile 语法解析模块——这是当前依赖代价的主要来源。

## 13. 第二轮补充：统一 `Parse` 与 `ValidateDockerfile` 的解析依据

### 13.1 结论

三条判定线必须分别定义，但**只能有一条语法解析路径**：

1. **语法有效性**：`frontend/dockerfile/parser` 是唯一语法来源，`dockerfile.Parse` 与
   `docker.ValidateDockerfile` 共用同一份 AST。`parser` 只保证"文件能被解析"，不保证"存在 `FROM`"、
   也不保证 `FROM` 参数个数正确——这两条必须由本项目补齐。
2. **基础镜像提取**：在 AST 之上做变量展开（`shell.Lex`）与引用规范化（`distribution/reference`），
   产出 `Images` / `Skipped` / `Unresolved`，供 `checkBaseImages` 使用。
3. **项目安全策略**：`ValidateDockerfile` 与 `ValidateContent` 只消费上述事实，不再自己解析文本；
   `latest` 判定、私钥特征、Docker Socket 检测仍属本项目策略。

关键取舍：`ValidateDockerfile` 改为复用同一份事实，**不再保留第二套 `FROM` 字符串解析**；
`fail-closed` 仍然只在 `checkBaseImages` 生效，`ValidateDockerfile` 对动态引用保持"不判定、不阻断"，
否则内置语言模板 `FROM ${AGENTFLOW_BASE_IMAGE}` 会被自己的校验拒绝。

### 13.2 三条判定线的定义

| 判定线 | 输入 | 判定内容 | 失败语义 | 归属 |
|---|---|---|---|---|
| 语法有效性 | Dockerfile 字节 | 指令、续行、heredoc、parser directive、`FROM` 参数形状 | 带行号的解析错误 | `parser` + 本项目补齐的 `FROM` 形状校验 |
| 基础镜像提取 | 同一份 AST | 变量展开、引用规范化、别名 / `scratch` 跳过、去重 | `Unresolved` 非空即检查失败 | `internal/dockerfile` |
| 项目安全策略 | 同一份 AST 的事实 | 必须存在 `FROM`、禁止 `latest`、禁止凭据与 Docker Socket 文本 | 返回可读错误，调用方拒绝保存 | `internal/docker` |

**语法有效不等于满足项目要求**，三个方向都要有明确判定，不能"能解析就算通过"：

- `RUN echo hi`：语法有效，但没有 `FROM` → 项目必须拒绝（`parser` 只对"没有任何指令"报
  `file with no instructions`）。
- `FROM node:latest # 说明`：语法**无效**（3 个参数且第二个不是 `AS`）→ 解析错误。
- `FROM node`：语法有效、引用合法，但违反 `latest` 策略 → 策略拒绝。

### 13.3 目标输入的逐条判定

上游依据均取自本机模块缓存中的 `github.com/moby/buildkit@v0.26.3`，行号可直接复核。

| 输入 | `parser` 结果 | 引用规范化 | `dockerfile.Parse` | `ValidateDockerfile` | 依据 |
|---|---|---|---|---|---|
| `FROM node` | `from` 节点，`Children=[node]` | `TagNameOnly` → `docker.io/library/node:latest`，原引用 `IsNameOnly` | 查询 `docker.io/library/node:latest` | **拒绝**：隐式 `latest` | `reference/normalize.go:56-80`、`:230`、`helpers.go:6` |
| `FROM node:latest` | `Children=[node:latest]` | tag=`latest` | 查询同一引用 | **拒绝**：显式 `latest` | 同上 |
| `FROM node:22` | `Children=[node:22]` | tag=`22` | 查询 `docker.io/library/node:22` | **通过**（进入元数据检查） | 同上 |
| `FROM node:latest AS build` | `Children=[node:latest, AS, build]` | tag=`latest`，阶段名 `build` | 查询同一引用 | **拒绝** | `instructions/parse.go:419-431` |
| `FROM node:latest as build` | 同上（`as` 小写） | `EqualFold(args[1],"as")` 成立 | 同上 | **拒绝**：别名写法不影响判定 | `instructions/parse.go:421` |
| `FROM node:latest # 注释` | `Children=[node:latest, #, 注释]` | 不需要规范化（形状先失败） | 不提取，报 `FROM` 参数不合法 | **拒绝**：3 个参数且第二个不是 `AS` | `parser/parser.go:205`、`parser/parser.go:516-519`、`instructions/parse.go:426` |
| `FROM    node:latest` | 与单空格等价 | tag=`latest` | 查询同一引用 | **拒绝** | `parser/parser.go:205`（`parseStringsWhitespaceDelimited`） |
| `FROM --platform=$BUILDPLATFORM node` | `Flags=[--platform=$BUILDPLATFORM]`，`Children=[node]` | `node` → 隐式 `latest` | 查询 `docker.io/library/node:latest` | **拒绝**：隐式 `latest`；`--platform` 未展开不影响判定 | `parser/split_command.go:28`、§14.3 |

结论：D7 的四条绕过路径在新结构下全部关闭——前三条由语法层自然修好（大小写 `as`、Tab / 多空格、
行尾注释变为语法错误），第四条（隐式 `latest`）由引用规范化判定 `TagNameOnly` 后的 tag 关闭。

### 13.4 其他形态

- **`FROM scratch`**：`Children=[scratch]`（1 个参数，形状合法）；引用规范化会得到
  `docker.io/library/scratch:latest`，所以必须在规范化**之前**先按 `scratch` 跳过，否则会去查询一个
  不存在的镜像。现状已是先判断后规范化，替换后保持这个顺序（比较用 `EqualFold`，与现状一致）。
- **`FROM` 引用前序阶段别名**：`parser` 不判断（属 builder 职责）。本项目继续用 `stageAliases` 跳过，
  别名比较大小写不敏感；上游把 `AS <name>` 转小写（`instructions/parse.go:421-422`），所以 `FROM Build`
  引用 `AS Build` 是合法的。
- **动态 `FROM`**（`FROM ${AGENTFLOW_BASE_IMAGE}`，`ARG` 无默认值）：展开后仍未知 → `Unresolved` →
  `checkBaseImages` 失败。这条 fail-closed 必须原样保留；`ValidateDockerfile` 则必须继续**接受**这类
  文件（内置语言模板就是这个形态），否则模板无法保存。
- **多阶段**：逐个 `from` 节点处理；别名先登记再判断；同一 `Resolved` 只保留首次出现。
- **`FROM` 缺失**：`parser` 不报错（`RUN echo hi` 能解析成功），本项目必须自己补"必须存在 `FROM`"。
- **非法引用**：`FROM Node:LATEST`（仓库名含大写）→ `ParseNormalizedNamed` 报
  `repository name (Node) must be lowercase`；`FROM node:`（空 tag）→ `Parse` 报引用格式错误。
  两者都能在本地给出可读错误，不再把无效引用送进 Registry 查询（修 D8 的一部分）。
- **更正第一轮 D8 的一处结论**：`FROM localhost:5000` **不是**非法引用。`splitDockerDomain` 的单段
  快路径把整串当作 familiar name，最终解析为 `docker.io/library/localhost:5000`（`5000` 是 tag），
  引用合法且 tag 不是 `latest` → 通过校验并查询该引用。第一轮把它列为"上游会报错"的三例之一是错的
  （依据 `reference/normalize.go:126-137` 与 `reference/reference.go:186-231`）。
- **HEREDOC**：`RUN <<EOF` 内的 `FROM ...` 文本进入 `Node.Heredocs`，不产生 `from` 节点 → 不再误报（修 D2）。
- **BOM**：`discardBOM` 只作用于首个扫描 token（`parser/parser.go:294`、`directives.go:186-188`），修 D1。
- **`# escape=` / `# syntax=` / `# check=`**：`Result.EscapeToken` 反映实际生效的转义符，续行与引号
  解析随之变化（修 D9）；重复 directive 会报错。
- **单行长度**：`scanLines` 在超过 `bufio.MaxScanTokenSize-1` 时直接报错（`parser/parser.go:561`），
  比现状更严格。
- **边界保留：`COPY --from=<image>`**：`parser` 把它放进 `Node.Flags`，但它不是 `FROM`。现状与替换后
  都不参与基础镜像检查。需要指出这属于**策略覆盖缺口**：`COPY --from=nginx:latest` 会在构建时拉取
  latest 镜像，绕过"禁止 latest"策略。这不是解析器问题，本轮只登记，不扩大实施范围。

### 13.5 统一后的结构（事实层与策略层分离）

```text
internal/dockerfile（事实层：唯一解析路径）
  analyze(content []byte) (analysis, error)
    ├─ parser.Parse(bytes.NewReader(content))          // 语法层
    ├─ FROM 形状校验：1 或 3 个参数；3 个时第二参数 EqualFold "AS"；阶段名正则
    └─ 每个 FROM：RawImage / RawPlatform / Alias / Args / Line
  Parse(content []byte) (Result, error)                // 语义层，签名与对外语义不变
    ├─ analyze
    ├─ shell.NewLex(escapeToken)；SkipUnsetEnv = true；自实现大小写敏感 EnvGetter
    ├─ reference.ParseNormalizedNamed + TagNameOnly（显示可用 FamiliarString）
    └─ 阶段别名 / scratch / 去重 / Unresolved（策略骨架保留在本项目）

internal/docker（策略层：唯一消费者）
  ValidateDockerfile(content []byte) error             // 签名不变，3 个调用点不变
    ├─ 复用同一份 analyze 事实（不再逐行字符串匹配）
    ├─ 空内容 / 必须存在 FROM / 非法引用 / latest 判定
    └─ ValidateContent（凭据特征与 Docker Socket，独立的文本安全策略，保持不动）
```

- 依赖新增一条边：`internal/docker` → `internal/dockerfile`（单向，反向不存在，无环）。
  实施时必须在 `docs/workspace.md` 的依赖表登记这条边。
- `Result` / `BaseImage` / `SkippedFrom` / `UnresolvedFrom` 的字段语义与 `dockerfile_unparsable` /
  `dockerfile_unresolved` / `dockerfile_no_external_image` 三个 `ErrorCode` 保持不变。
- 错误文案可以重写（空文件、缺 `FROM`、`FROM` 形状、非法引用、heredoc 未终止），但必须能取到行号
  （`*parser.LocationError` + `errors.As`）。

### 13.6 必须由本项目补齐的 `FROM` 形状校验

`parser` 对 `FROM` 只做空白切分（`parser/parser.go:205`），参数个数与阶段名合法性在
`instructions` 里（`instructions/parse.go:393-431`）。既然本项目不引入 `instructions`，就必须把这几行
语义实现成自己的代码，否则 §13.3 第 6 行（行尾注释）会退化成"把 `#` 当成别名"，重新打开 D7：

1. 参数个数必须为 1 或 3（`len(args) != 1` 且不是 `len(args) == 3 && EqualFold(args[1], "as")` → 错误）；
2. 阶段名必须匹配 `^[a-z][a-z0-9-_.]*$`，且比较前转小写（`instructions/parse.go:417-431`）；
3. 展开后基础镜像为空时必须报错，语义对齐上游 `base name (%s) should not be blank`
   （`dockerfile2llb/convert.go:310-312`）；
4. 这些错误都要带 `FROM` 节点行号。

### 13.7 本轮明确不做

- 不引入 `instructions` / `dockerfile2llb`（理由见第 8 节：12 模块 / 49 模块，代价与本项目现状不匹配）。
- 不新增第二套 Dockerfile 解析器，不保留旧字符串解析作为回退。
- 不把 `ValidateContent` 的文本策略迁移到 AST（它检查的是凭据与 Socket 路径，与指令语法无关）。
- 不在本轮实现 `COPY --from` 检查、`--platform` 参与元数据查询。

## 14. 预定义 `ARG` 的取值策略

### 14.1 上游名单（`v0.26.3` 实测，11 个）

`dockerfile2llb/platform.go:39-65` 的 `defaultArgs()` 注入以下参数，且**允许被 `--build-arg` 覆盖**
（`overrides` 来自 `opt.BuildArgs`）：

`BUILDPLATFORM`、`BUILDOS`、`BUILDOSVERSION`、`BUILDARCH`、`BUILDVARIANT`、
`TARGETPLATFORM`、`TARGETOS`、`TARGETOSVERSION`、`TARGETARCH`、`TARGETVARIANT`、`TARGETSTAGE`。

第一轮只提到 8 个，遗漏了 `BUILDOSVERSION`、`TARGETOSVERSION`、`TARGETSTAGE`——这属于补充而非更正，
但结论更严：注入时必须按完整名单判断，否则会漏掉三个"看起来不像平台"的参数。

### 14.2 逐项判定

| 参数 | 含义 | 值依赖 | 本项目现有信息能否确定 | 远端 Docker 检查能否确定 | 建议 |
|---|---|---|---|---|---|
| `BUILDPLATFORM` | 构建机平台（`platforms.Format`：`os/arch[/variant]`） | buildkitd worker 平台、`--platform`、`DOCKER_DEFAULT_PLATFORM`、`--build-arg` 覆盖 | 否（未采集任何平台信息） | 否（`docker info` 只能给出 daemon 主平台，且不等于 worker 选定的 build platform） | 不注入 → 使用即 `Unresolved` |
| `BUILDOS` | 构建机 OS | 同上 | 否 | 否 | 不注入 |
| `BUILDOSVERSION` | 构建机 OS 版本 | worker 内核 / 镜像版本 | 否 | 否 | 不注入 |
| `BUILDARCH` | 构建机架构 | 同上 | 否 | 否 | 不注入 |
| `BUILDVARIANT` | 构建机 variant（如 `v8`） | 同上 | 否 | 否 | 不注入 |
| `TARGETPLATFORM` | 目标平台（`platforms.FormatAll`） | 未指定 `--platform` 时等于 build platform（`implicitTarget`） | 否 | 否 | 不注入 |
| `TARGETOS` | 目标 OS | 由 `TARGETPLATFORM` 派生 | 否 | 否 | 不注入 |
| `TARGETOSVERSION` | 目标 OS 版本 | 同上 | 否 | 否 | 不注入 |
| `TARGETARCH` | 目标架构 | 同上 | 否 | 否 | 不注入 |
| `TARGETVARIANT` | 目标 variant | 同上 | 否 | 否 | 不注入 |
| `TARGETSTAGE` | 目标阶段名（无别名时为 `default`） | 构建请求的 `--target` | 静态可知（本项目 `docker build` 不传 `--target`） | 是 | 仍不注入：与 `FROM` 判定无关，注入无收益 |

结论三条：

1. **变量名已知不等于值已知**。11 个名字里没有一个是常量，全部由构建上下文推导。
2. **不允许把名字映射成"看起来合理"的默认值**（例如 `linux/amd64`）。那会在 arm 节点、
   `DOCKER_DEFAULT_PLATFORM`、多 worker 场景下给出错误结论，本质是伪造事实。
3. 因此 `EnvGetter` 只装载"全局 `ARG` 有默认值"的项（`collectArg` 的既有规则不变），预定义参数一律缺失：
   引用中出现即被记为未匹配 → `Unresolved` → `checkBaseImages` 失败。
   **这与现状完全一致，fail-closed 未被弱化。**

### 14.3 `--platform` 是否参与变量展开

- 上游用**同一个** `Lex` 与 `globalArgs` 展开 `FROM --platform=...` 的值，再 `platforms.Parse`，
  空值报 `empty platform value from expression`（`dockerfile2llb/convert.go:325-345`）。
- 本项目的元数据检查是只读 `GET /distribution/{name}/json`，与平台无关；`BaseImage.Platform` 至今
  **没有消费方**。
- 建议：`--platform` **不参与 fail-closed 判定**，保留展开前原文（与现状一致）。理由：若把平台变量
  的未知也算作"无法解析"，`FROM --platform=$BUILDPLATFORM node:22` 这类完全合法且基础镜像确定的
  Dockerfile 会被误杀，而平台值对本检查没有任何影响。
- 若将来确实要按平台查元数据，前提是先在 `HostValidator` 采集可靠平台上下文（并明确它与 buildkitd
  worker 平台的关系），届时再把平台值注入 `Lex`。本轮不实现，也不预留半成品抽象。

### 14.4 与 `SkipUnsetEnv` 的关系（实现细节，必须写死）

- 默认 `Lex`（`SkipUnsetEnv=false`）把未定义变量**展开为空串**（`shell/lex.go:348, 371, 396, 466`）。
  直接使用默认行为会静默改变引用：`FROM ${IMAGE}` 变成空串、`FROM node${SUFFIX}` 退化成 `node`，
  与 fail-closed 冲突。
- 所以必须 `SkipUnsetEnv = true`：未定义变量原样保留在结果里并记入 `Unmatched`，由调用方判定 `Unresolved`。
- 已知代价：`FROM ${BASE:-node:22}` 在 `SkipUnsetEnv=true` 下同样会被判 `Unresolved`，而 BuildKit 能
  确定地展开成 `node:22`。这是"更严"的方向，符合 fail-closed；若要放行，需要额外实现"变量未知时
  `:-` 默认值仍可静态确定"的语义，属新增能力，本轮不建议。
- 跨平台风险（第一轮已提，本轮复核成立）：`shell.EnvsFromSlice` 在 `GOOS=windows` 下把变量名大写归一
  （`shell/equal_env_windows.go` 的 `NormalizeEnvKey` / `EqualEnvKeys`，`GOOS=linux` 下为恒等），
  同一份 Dockerfile 在两个平台上会得到不同结果。必须自实现 `EnvGetter`（`Get` / `Keys`）保持大小写敏感。

## 15. `--privileged` 与构建期提权指令

### 15.1 现状核实（三类事实分开陈述）

| 层面 | 实际实现 | 证据 |
|---|---|---|
| 容器创建参数 | **从不出现 `--privileged`**：参数由 `ContainerCreateArgs` 逐项构造（`--name` / `--workdir` / `--label` / 显式声明的 `--mount` / `--tmpfs` / `--read-only`） | `internal/docker/container.go`（`CreateContainer` / `ContainerCreateArgs`）；`tests/unit/container/docker_args_test.go`、`tests/integration/local/execenv/docker_test.go` 都在断言参数中不得包含 `--privileged` |
| Dockerfile 校验 | **没有任何 `--privileged` 判定**。`ValidateDockerfile` 只做四件事：非空、包含 `FROM ` 字样、`:latest` 后缀、转调 `ValidateContent` | `internal/docker/templates.go:305-344`；`ValidateContent` 只匹配私钥特征、`file:///`、`/var/run/docker.sock`（`internal/docker/context.go:188-216`） |
| 构建命令 | 只传 `--tag` / `--label` / `--file` / `--build-arg AGENTFLOW_BASE_IMAGE` / `--no-cache` / `--progress` / 上下文路径；**不传 `--allow`、不传 `--platform`**，也不设置 `DOCKER_BUILDKIT` / `DOCKER_DEFAULT_PLATFORM` | `internal/docker/image.go` 的 `BuildImage` |

注释层面的声称有两处，且只有一处与实现一致：

- `container.go` 的"不使用 `--privileged`"——与实现一致（守点在参数构造与测试）。
- `templates.go:311` 的"禁止 `--privileged` 与 Docker Socket 挂载"——前半句**不成立**：代码里没有这条
  检查；后半句成立，但由 `ValidateContent` 通过匹配 `/var/run/docker.sock` 完成。

### 15.2 为什么"补一条 `--privileged` 字符串检查"是错的

1. Dockerfile 语法里**不存在** `--privileged` 指令，它是 `docker create` / `docker run` 的参数。
   用户写不出"让容器变成特权容器"的 Dockerfile，因此这条"缺失的检查"没有对应的攻击面。
2. 若真的按字符串实现，必然同时命中注释与 HEREDOC 文本（模板注释、`RUN cat <<EOF` 正文），
   误伤合法文件——这正是 D2 类问题的镜像。
3. 因此正确修复是**改注释措辞**，不是新增检查：把"禁止在 Dockerfile 中写 `--privileged`"改成
   "容器创建参数不使用 `--privileged` / host 网络 / host PID / 宿主目录挂载；Dockerfile 中的提权指令
   受 BuildKit entitlement 约束，本项目不授予任何 entitlement"。

### 15.3 Dockerfile 里真实存在的构建期特权面

| 指令 | 生效条件 | 本项目现状 | 结论 |
|---|---|---|---|
| `RUN --security=insecure` | buildkitd 开启 `security.insecure` entitlement **且** 客户端传 `--allow security.insecure` | `docker build` 从不传 `--allow` | 构建**失败**，不产生提权；不是本次阻塞项 |
| `RUN --network=host` | `network.host` entitlement | 同上 | 同上 |
| `RUN --device=...` | `device` entitlement | 同上 | 同上 |
| `RUN --mount=type=bind` | 源必须在构建上下文中（`from=` 只能引用阶段或镜像） | 无宿主路径直通能力 | 不构成宿主逃逸 |
| `COPY --from=<image>` | 构建期拉取任意镜像 | 不参与基础镜像检查 | 策略覆盖缺口（§13.4），与解析器无关 |
| 经典 builder（`DOCKER_BUILDKIT=0`）下的 `RUN --security=insecure` | 该 flag 不被识别 | — | 只会在容器内当成 shell 参数，不产生提权 |

上游依据：`--security` 的取值校验与 llb 映射在 `instructions/commands_runsecurity.go` 与
`dockerfile2llb/convert_runsecurity.go`；entitlement 白名单在 `solver/llbsolver/solver.go:527`
（`entitlements.WhiteList`），即**守护进程侧**判定，客户端不申请就不可能获得。

不要把"构建期 `--security=insecure`"与"容器运行时 `--privileged`"等同：前者是 BuildKit 的构建沙箱
开关，受双重 entitlement 约束；后者是容器运行时参数，由本项目的创建参数构造决定。两者都不需要
"Dockerfile 文本检查"来兜底。

### 15.4 建议归属

- `--privileged` 的守点留在**容器创建层**（`ContainerCreateArgs` + 现有两处测试），不新增 Dockerfile
  文本检查，**不纳入**本次解析器替换的实施范围。
- 若要拦构建期提权，正确做法是读 `RUN` 节点的 `Node.Flags`（`--security=` / `--network=` / `--device=`），
  而不是匹配文本；但该需求的输入是"本次构建会授予哪些 entitlement"，而本项目当前恒为空集，
  因此应单独立项，不作为替换的附带项。
- 本轮**只记录建议**，未修改任何安全代码。第一轮的"独立缺陷"方向不变，性质更正为
  **注释与实现不一致（且该检查本身不成立）**，编号记为 D12。

## 16. BuildKit `v0.26.3` 安全核查

核查日期：**2026-11-04**。本节所有结论都标注了可追溯来源；官方资料无法确认的内容一律标为未确认。

### 16.1 检索范围与来源

| 来源 | 用法 | 结果 |
|---|---|---|
| NVD CVE API 2.0，`keywordSearch=buildkit` | 全量列举相关 CVE | 31 条，其中 7 条与 BuildKit（Moby）本体无关（见 §16.3 末段） |
| OSV.dev API（按 CVE / GHSA / GO 编号取记录） | 受影响版本区间、修复提交、Go 漏洞库的 import 级信息 | 18 条 CVE 记录与 5 条 Go 记录，其余条目以 NVD 描述为准 |
| Go 漏洞库 | `GO-2026-4858`、`GO-2026-4859`、`GO-2026-6255`、`GO-2026-6256`、`GO-2024-2492` | 其中仅 `GO-2026-6256` 带 import 级受影响包信息 |
| 模块代理版本列表（`go list -m -versions`） | 确认 0.26 版本线是否还有补丁版本 | 0.26 线止于 `v0.26.3` |
| 上游源码（本机模块缓存 `v0.26.3` / `v0.31.2` / `v0.32.0` / `v0.32.2` / `v0.33.1` / `v0.34.0` / `v0.34.2`） | `go` 指令、目录级 diff、修复文件是否存在 | 见 §16.2、§16.4 |

**本轮受限项（必须记录）**：

- `api.github.com` 的 Advisory 查询在匿名配额下返回空数组或 403（`ecosystem=go&affects=moby/buildkit`
  与 `affects=github.com/moby/buildkit` 都为空），因此**没有**取得 GitHub Security Advisories 的完整列表；
- `github.com` 与 `patch-diff.githubusercontent.com` 在本机解析到私有地址，被工具拒绝，**未能**逐条
  取得修复提交的 diff；
- 上述缺口用"NVD + OSV（GHSA/GO）+ 本地文件级比对"交叉覆盖，残余不确定项列入 §16.5。

**官方来源 URL（核查日期 2026-11-04）**：

- NVD CVE API：`https://services.nvd.nist.gov/rest/json/cves/2.0?keywordSearch=buildkit`（及按 `cveId=` 取单条）
- OSV 记录：`https://api.osv.dev/v1/vulns/<编号>`（本轮用到 `CVE-*`、`GHSA-*`、`GO-*` 三类编号）
- BuildKit 安全公告（编号索引）：`https://github.com/moby/buildkit/security/advisories`
- 本轮引用到的公告编号：`GHSA-4c29-8rgm-jvjj`、`GHSA-4vrq-3vrq-g6gg`、`GHSA-7236-3392-c5c6`、
  `GHSA-72x6-4j93-7w86`、`GHSA-g2h8-426c-7976`、`GHSA-388v-wmr2-g2v2`、`GHSA-32pv-7hq5-qhwq`、
  `GHSA-qx3x-mv6r-52p6`、`GHSA-hw3h-2gp9-cxpv`、`GHSA-f2v9-hprr-32q3`、`GHSA-4hgw-qrhw-fhg8`、
  `GHSA-9728-qjrv-2xh2`、`GHSA-fjj4-h6vf-m9hj`、`GHSA-cv6p-7w7g-xjwq`、`GHSA-mgqf-486f-49vp`、
  `GHSA-w26x-xg27-7qjc`、`GHSA-2fqp-xg6f-v7gj`、`GHSA-r456-g3gm-cvxf`
- 修复版本发布页：`https://github.com/moby/buildkit/releases/tag/v0.28.1`、`.../v0.31.1`、
  `.../v0.31.2`、`.../v0.33.1`、`.../v0.34.1`、`.../v0.34.2`
- Go 漏洞库条目（OSV 记录里给出原始地址）：`https://vuln.go.dev/ID/GO-2026-4858.json`、
  `GO-2026-4859`、`GO-2026-6255`、`GO-2026-6256`、`GO-2024-2492`
- 版本列表与 `go` 指令：`go list -m -versions github.com/moby/buildkit` 与各版本 `go.mod`
  （`https://proxy.golang.org/github.com/moby/buildkit/@v/list` 在本机不可直接访问，因此改用模块工具查询）

### 16.2 核心结论：没有任何已知漏洞落在 `parser` / `shell` / `util/stack`

文件级证据（`v0.26.3` → `v0.34.2` 目录 diff，本机模块缓存）：

- `frontend/dockerfile/parser/`：只有 3 个文件变化——`parser.go`（`Node.Dump` 改用 `strings.Builder`、
  heredoc 内容改用 `strings.Builder` 拼接）、`directives.go`（`setComment` → `SetComment` 导出）、
  `dumper/main.go`。**没有安全修复，也没有行为收紧**。
- `frontend/dockerfile/shell/`：只有 `lex.go`——`processStopOn` 增加 `preservePatternEscapes`，修正
  `${var#pat}` / `${var%pat}` / `${var/pat/rep}` 在引号内的转义保留。属正确性修复，未被任何安全公告引用。
- `util/stack/`（`parser/errors.go` 无条件引用，protobuf 依赖链的来源）：只有 `stack.pb.go`（生成代码）
  与一个测试文件变化。

因此可以回答分类问题：**"明确影响 Parser / Shell 的漏洞"为零条**，而不是"没有查到"。

### 16.3 逐条核查（BuildKit 相关部分）

| 编号 | 受影响版本 | 修复版本 | 影响组件（依据） | v0.26.3 是否受影响 | 是否影响本次引入 | 阻止引入 |
|---|---|---|---|---|---|---|
| CVE-2023-26054 | 0.10.0–0.11.3 | 0.11.4 | provenance 中的 Git 凭据泄漏（守护进程） | 否 | 否 | 否 |
| CVE-2024-23650 / 23651 / 23652 / 23653 | < 0.12.5 | 0.12.5 | 守护进程 panic、cache mount 竞态、`RUN --mount` 越界删除、insecure 容器 API | 否 | 否 | 否 |
| CVE-2026-33747（`GO-2026-4858`） | < 0.28.1 | 0.28.1 | 自定义 frontend 使文件写到 state 目录之外（solver / worker） | 是 | 否（Go 记录无 import 级信息，按整模块计） | 否 |
| CVE-2026-33748（`GO-2026-4859`） | < 0.28.1 | 0.28.1 | Git URL `subdir` 越过检出根（Git 源解析） | 是 | 否（同上） | 否 |
| CVE-2026-61711（`GO-2026-6255`） | ≤ 0.31.0 | 0.31.1 | 伪造 `SecurityMode` 绕过 Seccomp / AppArmor（`executor/oci/spec_linux.go`） | 是 | 否（同上；与 `--security=insecure` 相关，见 §15.3） | 否 |
| CVE-2026-61712（`GO-2026-6256`） | < 0.31.1 | 0.31.1 | 无上限解析 `/etc/passwd`、`/etc/group`；Go 记录明确列出 `solver/llbsolver/ops`、`executor/oci` | 是 | 否（import 级信息明确排除 parser / shell） | 否 |
| CVE-2026-75593 / CVE-2026-15789 | < 0.31.2 | 0.31.2 | 上传请求绕过目标目录校验（daemon） | 是 | 否 | 否 |
| CVE-2026-15788 | < 0.31.2 | 0.31.2 | WCOW 缓存卷目录联接越界（worker） | 是 | 否 | 否 |
| CVE-2026-15791 | 0.30.0–0.31.1 | 0.31.2 | LLB file op 删除宿主 `/tmp`（solver） | 是 | 否 | 否 |
| CVE-2026-15792 | < 0.31.2 | 0.31.2 | 构造请求导致守护进程 panic | 是 | 否 | 否 |
| CVE-2026-15793 | 0.30.0–0.31.1 | 0.31.2 | Git bundle 检出命令注入（Git 源） | 是 | 否 | 否 |
| CVE-2026-93318 | < 0.33.1 | 0.33.1 | 未校验 DiffID 导致缓存投毒（solver / worker） | 是 | 否 | 否 |
| CVE-2026-93319 | < 0.33.1 | 0.33.1 | 外部 frontend 触发数据竞争 panic（gateway） | 是 | 否 | 否 |
| CVE-2026-93320 | < 0.33.1 | 0.33.1 | 特殊文件 inode 处理（worker） | 是 | 否 | 否 |
| CVE-2026-93316（`GHSA-r456-g3gm-cvxf`） | < 0.33.1 | 0.33.1 | 以 `--cdi-disabled` 启动时，使用 CDI 设备的构建可导致守护进程 panic | 是 | 否 | 否 |
| CVE-2026-93321 / CVE-2026-93322 | < 0.33.1 | 0.33.1 | 畸形 LLB file op / MergeOp 导致 panic（solver） | 是 | 否 | 否 |
| CVE-2026-93323 | < 0.33.1 | 0.33.1 | Dockerfile 前端**读取** Dockerfile 与 `.dockerignore` 无大小上限；修复文件为 `frontend/dockerui/readfile.go`（本机比对：`v0.32.2` 无、`v0.33.1` 有） | 是 | 否（不在 parser / shell；本项目读取 Dockerfile 的上限是 `docker.MaxContextFileBytes` = 4 MiB） | 否 |
| CVE-2026-121520 | < 0.34.1 | 0.34.1 | 在 state 目录之外设置扩展属性（daemon） | 是 | 否 | 否 |
| CVE-2026-121522 | 0.26.0–0.34.1 | 0.34.2 | Git 源解析 panic（daemon） | 是 | 否 | 否 |

误命中，与 BuildKit（Moby）无关，仅登记以说明检索噪声：`CVE-2023-43116`、`CVE-2023-43741`
（Buildkite Elastic CI）、`CVE-2026-102379`、`CVE-2026-94298`、`CVE-2026-97275`（WordPress 插件
"BuildKit – Product Builder"）；`CVE-2024-24557` 属 Moby 经典 builder，`CVE-2025-0495` 属 buildx CLI，
均不在本次引入范围。

### 16.4 `v0.26.3` 是否仍适合长期固定

- **0.26 版本线事实上已停止维护**：模块代理的版本列表里 0.26 线止于 `v0.26.3`（2025-12-16），
  之后没有任何 `v0.26.4`。2026-01 之后的所有安全修复都发布在 `0.28.1` / `0.31.1` / `0.31.2` /
  `0.33.1` / `0.34.1` / `0.34.2`，**没有回补 0.26 线**。这是"可观察到的事实"，不是官方 EOL 声明。
- Go 版本门槛（从各版本 `go.mod` 的 `go` 指令读取）：`v0.26.3`=1.24.3、`v0.27.0`=1.25.0、
  `v0.28.1`=1.25.5、`v0.30.0`=1.25.5、`v0.31.2`=1.25.9、`v0.32.0`=1.26.3、`v0.33.1`=1.26.8、
  `v0.34.2`=1.26.8。本项目 `go 1.24.5`，因此**能用的最高版本就是 `v0.26.3`**。
- 综合判断：`v0.26.3` 对"只编译 `parser` + `shell`"的用法**没有已知可利用漏洞**，但它是一个"永久不会
  再收到补丁"的版本，且 Go 漏洞库会因三条模块级记录（无 import 级信息）把整个模块判为受影响。

### 16.5 未确认事项

1. 未取得修复提交 diff（§16.1 受限项）。表中"影响组件"的依据是公告文本 + `GO-2026-6256` 的 import 级
   信息 + `CVE-2026-93323` 的本地文件存在性比对；**其余条目的组件归属未经 diff 复核**。部分条目的修复版本
   也是按"同一修复批次"归入（`CVE-2026-15788` / `15791` / `15792` / `15793` 与已明确指向 `v0.31.2` 的
   `CVE-2026-15789` 共用同一修复提交）。
2. `CVE-2026-121520` / `CVE-2026-121522` 于 2026-11-03 发布，NVD 状态仍为 `Received`（未经二次分析），
   CVSS 与修复细节可能变化。
3. 未取得 GitHub Advisory 完整列表，可能存在尚未分配编号或未进入 NVD / OSV 的公告。
   **"未查到"不等于"不存在"。**
4. `CVE-2026-93323` 的 `v0.33.1` 归属由 NVD 引用与本机文件比对共同支持，但同批修复是否还改动了
   `frontend/dockerfile` 下的其它文件，未经 diff 确认。
5. 未验证"`v0.26.3` 的 `parser` 在恶意输入下的健壮性"（fuzz 级别），本轮只做公告与代码比对。

## 17. 修订后的实施方案与准入结论

### 17.1 决策表

| 问题 | 结论 |
|---|---|
| 是否引入 `frontend/dockerfile/parser` | **是**（唯一语法来源） |
| 是否引入 `frontend/dockerfile/shell` | **是**（同模块，零额外模块成本；禁用 `EnvsFromSlice`） |
| 是否引入 `distribution/reference` | **是**（连带 `opencontainers/go-digest`，共 2 个模块） |
| `ValidateDockerfile` 是否复用统一解析结果 | **是**；不得保留字符串 `FROM` 解析，必须补齐 §13.6 的形状校验 |
| `--privileged` 是否纳入实施范围 | **否**。Dockerfile 无此指令；改为修正注释，守点留在容器创建层（§15） |
| 预定义 `ARG` 注入哪些值 | **一个都不注入**；11 个名字全部保持未知 → `Unresolved`；`--platform` 不参与判定 |
| `v0.26.3` 是否可接受 | **有条件接受**，但首选"先升 Go 再引最新版"（§17.3） |

### 17.2 实施范围

- 删除（按符号）：`logicalLines`、`logicalLine`、`splitInstruction`、`parseFrom`、`fromInstruction`、
  `unquote`、`variablePattern`、`expandVariables`、`ResolveReference`。
- 新增：`analyze`（AST 事实层）、`FROM` 形状校验（1 或 3 参数 / `AS` / 阶段名正则 / 空引用）、
  自实现大小写敏感 `EnvGetter`、`reference` 规范化、`ValidateDockerfile` 改为消费事实。
- 保留：`Result` / `BaseImage` / `SkippedFrom` / `UnresolvedFrom` 的对外语义、三个 `ErrorCode`、
  `ValidateContent` 文本策略、容器创建参数构造。
- 依赖边：新增 `internal/docker` → `internal/dockerfile`，须在 `docs/workspace.md` 登记。
- 依赖规模（本轮实测，探针模块在仓库之外）：`parser`（v0.26.3）6 个模块 / 114 个包（含探针 main 包，与 §4.3 的 113 差 1）；
  `parser + shell + reference`（v0.34.2）为 2 个直接 + 5 个间接 require / 121 个包。
  附带收益：v0.34.2 不再需要 `gogo/protobuf`（已迁移到 `google.golang.org/protobuf`）。

### 17.3 两个分支与阻塞条件

**分支 A（推荐）：先升 Go，再引最新版。**

把 `go.mod` 的 `go` 指令升到 ≥ 1.26.8（核查时工具链可自动取到 `go1.26.10`），再引入 buildkit `≥ v0.34.2`。

- 收益：版本天花板消失；不再有"永久不会修的模块级安全告警"；间接依赖更少。
- 前置：Go 升级对 `modernc.org/sqlite`、CI 镜像与发布流程的影响需要单独评估（本轮不评估）。

**分支 B（不升 Go）：固定 `v0.26.3`。** 必须先接受三条硬条件：

1. 只允许导入 `frontend/dockerfile/parser` 与 `frontend/dockerfile/shell`（及其传递依赖 `util/stack`）；
   禁止导入 `instructions`、`dockerfile2llb`、`solver/*`、`executor/*`、`frontend/dockerui`。
2. 若 CI 启用 govulncheck：`GO-2026-4858`、`GO-2026-4859`、`GO-2026-6255` 会按整模块命中，
   必须留一份显式豁免记录，并注明"只有升 Go + 升 buildkit 才能消除"。
3. 在 `go.mod` 与 `docs/docker-compatibility.md` 记录固定版本的原因与上游 Go 版本门槛。

**阻塞条件**（任一触发就必须转分支 A，不得继续在 v0.26.3 上加功能）：

- 需要用到 `parser` / `shell` 之外的任何 buildkit 包；
- 出现针对 `parser` / `shell` 的新安全公告并被要求升级；
- 需要 `--platform` 参与元数据查询、或需要 `instructions` 级别的指令语义。

### 17.4 尚未确认的风险

1. Go 升级路径（分支 A）的成本未评估，可能牵动 SQLite 驱动与 CI。
2. `SkipUnsetEnv=true` 会把 `FROM ${BASE:-node:22}` 判为 `Unresolved`（偏严），用户可感知的行为变化。
3. 引用规范化后 `Resolved` 变成全限定名（`node` → `docker.io/library/node:latest`），
   `ValidationCheck.Images` 中的 `Reference` / `Resolved` 文本会变化（契约不变，展示需确认）。
4. 远端 Engine 的 `docker build` 与本项目固定的 buildkit 版本不同源，仍可能"本地通过、远端失败"。
5. `COPY --from=<image>` 不在检查范围（策略覆盖缺口，需单独立项）。
6. §13.6 的 `FROM` 形状校验如果漏实现任何一条，D7 会重新打开——这是实施方案里最容易被"简化"掉、
   也最危险的一处。

### 17.5 本轮改动说明

- **只修改了本文件**。生产代码、`go.mod` / `go.sum`、测试、Schema、Docker / SSH 基线均未改动。
- 未运行任何测试、未连接 Docker / SSH / Registry、未执行任何写入型 Git 操作。
- 报告状态仍为：**审计结论，未实施。**
