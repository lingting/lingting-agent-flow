#!/usr/bin/env bash
#
# 用途: 执行仓库唯一入口 `make test`（tests/ 全量），并把测试工作空间与日志落到
#       工作区内固定目录，供失败时上传。
#
# 参数:
#   --artifact-dir DIR  必填。测试产物目录；同时作为 TMPDIR，使 testkit 生成的
#                       随机工作空间与 test.log 落在可上传的位置。
#   --parallel N        可选，默认 8。传给 make 的 PARALLEL（-p / -parallel）。
#
# 环境变量: 无必需项。GITHUB_WORKSPACE 只用于诊断输出。
#
# 输出:
#   DIR/make-test-output.log  完整控制台输出（测试日志由 testkit 另写入 TMPDIR）。
#   不写 GITHUB_OUTPUT；步骤结论由退出码表达（步骤 outcome 供 ci-summary 汇总）。
#
# 退出码:
#   0  make test 通过
#   1  make test 失败（使用其真实退出码）
#   2  参数缺失或非法
#
# 副作用: 创建并写入 --artifact-dir 下的文件；不修改仓库其他路径。
set -euo pipefail

usage() {
    echo "用法: run-go-tests.sh --artifact-dir DIR [--parallel N]"
    echo "  执行 make test，并把日志与测试工作空间写入 --artifact-dir。"
}

artifact_dir=""
parallel="8"

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
        --parallel)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --parallel 缺少取值" >&2
                exit 2
            fi
            parallel="$2"
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
if ! [[ "$parallel" =~ ^[1-9][0-9]*$ ]]; then
    echo "错误: --parallel 必须是正整数，实际 $parallel" >&2
    exit 2
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"

mkdir -p "$artifact_dir"
artifact_dir="$(cd "$artifact_dir" && pwd)"
log_file="$artifact_dir/make-test-output.log"

# 测试工作空间落在 TMPDIR 之下：指向工作区内固定目录以便上传失败产物。
export TMPDIR="$artifact_dir"

echo "仓库根目录: $repo_root"
echo "测试工作空间: $TMPDIR"
echo "测试日志: $log_file"

cd "$repo_root"

# 管道里 tee 会吃掉 make 的退出码，这里显式保留（pipefail 保证取到非零的真实结论）。
status=0
PARALLEL="$parallel" make test 2>&1 | tee "$log_file" || status=$?
if [ "$status" -ne 0 ]; then
    echo "::error::make test 失败（退出码 $status），测试日志: $log_file"
    exit "$status"
fi

echo "全量 Go 测试通过。"
