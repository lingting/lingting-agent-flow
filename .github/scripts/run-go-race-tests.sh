#!/usr/bin/env bash
#
# 用途: 竞态检测。执行 `make test-race` 已覆盖的范围，再补齐 test-race 未包含的
#       数据库迁移与任务级工作流编排两个范围。
#
# 参数:
#   --artifact-dir DIR  必填。测试产物目录，同时作为 TMPDIR。
#
# 环境变量: 无必需项。
#
# 输出:
#   DIR/race-output.log  三组命令的完整控制台输出。
#   不写 GITHUB_OUTPUT；步骤结论由退出码表达。
#
# 退出码:
#   0  三组竞态检测全部通过
#   1  任意一组失败（保留其真实退出码）
#   2  参数缺失或非法
#
# 副作用: 创建并写入 --artifact-dir 下的文件；不修改仓库其他路径。
set -euo pipefail

usage() {
    echo "用法: run-go-race-tests.sh --artifact-dir DIR"
    echo "  执行 make test-race 与两个补充范围的 -race 检测。"
}

artifact_dir=""

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

mkdir -p "$artifact_dir"
artifact_dir="$(cd "$artifact_dir" && pwd)"
log_file="$artifact_dir/race-output.log"

export TMPDIR="$artifact_dir"

echo "仓库根目录: $repo_root"
echo "测试工作空间: $TMPDIR"
echo "测试日志: $log_file"

cd "$repo_root"

# 三组命令都要跑完：结论在最后一并核对，而不是遇到第一个失败就停下，
# 这样一次运行就能看到全部竞态问题。set +e 只作用于这个子 shell。
set +e
{
    # make test-race 已经覆盖 ./tests/unit/...（含 unit/store、unit/app、unit/httpapi、
    # unit/source/codesaver）与 ./tests/integration/local/ssh/...，因此这些范围不再
    # 重复执行；下面只补充 test-race 未包含的两个范围，实际覆盖是要求的超集。
    make test-race
    status=$?
    # 数据库迁移与并发访问
    go test -race -count=1 ./tests/integration/local/database/ || status=$?
    # 任务级工作流编排与并发隔离
    go test -race -count=1 ./tests/integration/local/workflow/ || status=$?
    exit "$status"
} 2>&1 | tee "$log_file"
status=${PIPESTATUS[0]}
set -e

if [ "$status" -ne 0 ]; then
    echo "::error::竞态检测失败（退出码 $status），测试日志: $log_file"
    exit "$status"
fi

echo "竞态检测全部通过。"
