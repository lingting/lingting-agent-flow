#!/usr/bin/env bash
#
# 用途: 前端格式与 lint 门禁。vp fmt 与 vp lint --fix 会自动修改文件且不返回失败，
#       因此必须在它们运行之后单独核验工作区是否仍有待修复改动。
#
# 参数:
#   --path REL  可选，默认 lingting-agent-flow-ui。相对仓库根目录的受检路径。
#
# 环境变量: 无必需项。
#
# 输出:
#   status --porcelain 的改动清单与 diff；失败时输出 ::error:: 注解。
#   不写 GITHUB_OUTPUT；结论由退出码表达。
#
# 退出码:
#   0  该路径没有待修复改动
#   1  仍有待修复改动，或当前目录不是 Git 仓库
#   2  参数错误
#
# 副作用: 只读；不执行 git add / commit / checkout / stash 等写操作。
set -euo pipefail

usage() {
    echo "用法: check-frontend-clean.sh [--path REL]"
    echo "  核验前端路径在格式与 lint 自动修复后没有残留改动。"
}

check_path="lingting-agent-flow-ui"

while [ $# -gt 0 ]; do
    case "$1" in
        --path)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --path 缺少取值" >&2
                exit 2
            fi
            check_path="$2"
            shift 2
            ;;
        -h | --help)
            usage
            exit 0
            ;;
        *)
            echo "错误: 未知参数 $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"

cd "$repo_root"

if ! git rev-parse --git-dir >/dev/null 2>&1; then
    echo "::error::$repo_root 不是 Git 工作区，无法核验待修复改动"
    exit 1
fi

dirty="$(git status --porcelain -- "$check_path")"
if [ -n "$dirty" ]; then
    echo "::error::$check_path 仍有 vp fmt / vp lint --fix 会自动修改的文件"
    echo "$dirty"
    git --no-pager diff -- "$check_path" || true
    exit 1
fi

echo "$check_path 没有待修复改动。"
