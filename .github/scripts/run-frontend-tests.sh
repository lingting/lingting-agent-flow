#!/usr/bin/env bash
#
# 用途: 执行前端统一测试入口 `pnpm test:all:safe`（vitest → Playwright → vp fmt /
#       vp lint --fix / tsc --noEmit → 生产构建），并把日志与测试产物落到工作区内
#       固定目录，供失败时上传。
#
# 参数:
#   --artifact-dir DIR  必填。测试产物目录，同时作为 TMPDIR（Playwright 报告与
#                       截图都经由 TMPDIR 落在该目录下）。
#   --project DIR       可选，默认仓库内 lingting-agent-flow-ui。
#
# 环境变量: 无必需项。
#
# 输出:
#   DIR/ui-test-output.log  完整控制台输出。
#   不写 GITHUB_OUTPUT；步骤结论由退出码表达。
#
# 退出码:
#   0  test:all:safe 通过
#   1  test:all:safe 失败（保留其真实退出码）
#   2  参数缺失、非法或前端目录不存在
#
# 副作用: 创建并写入 --artifact-dir 下的文件；test:all:safe 会执行 vp fmt / vp lint --fix
#         并可能修改前端源码，是否有待修复改动由 check-frontend-clean.sh 单独判定。
set -euo pipefail

usage() {
    echo "用法: run-frontend-tests.sh --artifact-dir DIR [--project DIR]"
    echo "  执行 pnpm test:all:safe，并把日志与测试产物写入 --artifact-dir。"
}

artifact_dir=""
project_dir=""

while [ $# -gt 0 ]; do
    case "$1" in
        --artifact-dir)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --artifact-dir 缺少取值" >&2
                exit 2
            fi
            artifact_dir="$2"
            shift 2
            ;;
        --project)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --project 缺少取值" >&2
                exit 2
            fi
            project_dir="$2"
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

if [ -z "$artifact_dir" ]; then
    echo "错误: 必须提供 --artifact-dir" >&2
    usage >&2
    exit 2
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
if [ -z "$project_dir" ]; then
    project_dir="$repo_root/lingting-agent-flow-ui"
fi
if [ ! -f "$project_dir/package.json" ]; then
    echo "::error::前端目录缺少 package.json: $project_dir"
    exit 2
fi

mkdir -p "$artifact_dir"
artifact_dir="$(cd "$artifact_dir" && pwd)"
log_file="$artifact_dir/ui-test-output.log"

export TMPDIR="$artifact_dir"

echo "前端目录: $project_dir"
echo "测试工作空间: $TMPDIR"
echo "测试日志: $log_file"

cd "$project_dir"

# test:all:safe 内部固定顺序为：vitest（--maxWorkers 4）→ Playwright（--workers=4）→
# vp fmt / vp lint --fix / tsc --noEmit → 生产构建。并发度保持项目既有设置，不提高。
status=0
pnpm test:all:safe 2>&1 | tee "$log_file" || status=$?
if [ "$status" -ne 0 ]; then
    echo "::error::前端测试与构建失败（退出码 $status），测试日志: $log_file"
    exit "$status"
fi

echo "前端单元测试、E2E、格式、lint、类型检查与生产构建全部通过。"
