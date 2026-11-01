#!/usr/bin/env bash
#
# 用途: Go 格式门禁与静态检查（gofmt -l 有输出即失败、go vet ./...、go build ./...）。
#
# 参数: 无。检查对象固定为 cmd / internal / tests，不接受外部传入，避免只检查一部分文件。
#
# 环境变量:
#   GITHUB_WORKSPACE  可选，仅用于诊断输出；仓库根目录始终按脚本自身位置解析。
#
# 输出:
#   标准输出：未格式化文件清单与阶段结论；失败时输出 GitHub Actions 的 ::error:: 注解。
#   不写 GITHUB_OUTPUT / GITHUB_STEP_SUMMARY，结论只由退出码表达。
#
# 退出码:
#   0  格式检查、静态检查与编译全部通过
#   1  存在未格式化文件、go vet 或 go build 失败
#   2  参数用法错误
#
# 副作用: 只读源码；go vet / go build 会写入 Go 构建缓存，不修改工作区文件。
set -euo pipefail

usage() {
    echo "用法: check-go-static.sh"
    echo "  执行 gofmt 门禁、go vet ./... 与 go build ./...。"
}

for argument in "$@"; do
    case "$argument" in
        -h | --help)
            usage
            exit 0
            ;;
        *)
            echo "错误: 未知参数 $argument" >&2
            usage >&2
            exit 2
            ;;
    esac
done

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"

if [ ! -f "$repo_root/go.mod" ]; then
    echo "::error::仓库根目录缺少 go.mod: $repo_root"
    exit 1
fi

cd "$repo_root"

# make fmt-check 只打印未格式化文件且退出码为 0，这里必须以非零退出码作为门禁。
targets=(cmd internal tests)
unformatted="$(gofmt -l "${targets[@]}")"
if [ -n "$unformatted" ]; then
    echo "::error::以下文件未按 gofmt 格式化，请执行 make fmt 后重新提交"
    echo "$unformatted"
    exit 1
fi

go vet ./...
go build ./...

echo "Go 格式检查、静态检查与编译全部通过。"
