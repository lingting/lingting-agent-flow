# 第三方声明与许可证

AgentFlow 发布制品是单个 Go 可执行文件，其中静态链接了下列第三方模块（含间接依赖）。
清单来源为 `go.mod` / `go.sum`，与制品 `version.json` 中记录的 Go 工具链版本共同确定
实际参与编译的依赖集合。

## 直接依赖

| 模块 | 版本 | 许可证 |
| --- | --- | --- |
| github.com/pkg/sftp | v1.13.9 | BSD-2-Clause |
| github.com/spf13/cobra | v1.10.2 | Apache-2.0 |
| golang.org/x/crypto | v0.41.0 | BSD-3-Clause |
| golang.org/x/term | v0.34.0 | BSD-3-Clause |
| gopkg.in/yaml.v3 | v3.0.1 | MIT 与 Apache-2.0 |
| modernc.org/sqlite | v1.45.0 | BSD-3-Clause |

## 间接依赖

| 模块 | 版本 | 许可证 |
| --- | --- | --- |
| github.com/dustin/go-humanize | v1.0.1 | MIT |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause |
| github.com/inconshreveable/mousetrap | v1.1.0 | Apache-2.0 |
| github.com/kr/fs | v0.1.0 | BSD-3-Clause |
| github.com/mattn/go-isatty | v0.0.20 | MIT |
| github.com/ncruces/go-strftime | v1.0.0 | MIT |
| github.com/remyoudompheng/bigfft | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause |
| github.com/spf13/pflag | v1.0.9 | BSD-3-Clause |
| golang.org/x/exp | v0.0.0-20251023183803-a4bb9ffd2546 | BSD-3-Clause |
| golang.org/x/sys | v0.37.0 | BSD-3-Clause |
| modernc.org/libc | v1.67.6 | BSD-3-Clause |
| modernc.org/mathutil | v1.7.1 | BSD-3-Clause |
| modernc.org/memory | v1.11.0 | BSD-2-Clause |

## 说明

- Go 标准库随 Go 工具链分发，许可证为 BSD-3-Clause，随 Go 发行版附带。
- SQLite 通过 `modernc.org/sqlite`（纯 Go 实现）使用，不链接系统 SQLite 库；SQLite 自身
  为公有领域（Public Domain）。
- 前端依赖不进入 Go 可执行文件的模块清单，但前端构建产物（JavaScript 与 CSS）被打包进
  可执行文件。其许可证与版本记录在仓库的 `lingting-agent-flow-ui/pnpm-lock.yaml`。
- 本文件随发布制品分发，内容必须与 `go.mod` 实际依赖保持一致；升级依赖后需同步更新。
- AgentFlow 自身未声明开源许可证，本文件不改变这一点，也不构成对 AgentFlow 本体的授权。
