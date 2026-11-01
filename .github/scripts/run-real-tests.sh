#!/usr/bin/env bash
#
# 用途: 真实外部依赖测试的执行入口。只运行需要真实设备的测试包（真实 Pi CLI、
#       真实 SSH 机器、真实远程 Docker / Agent / 代码保存 / 工作流），不重复执行
#       本机模拟测试（它们属于普通 Go CI 的 run-go-tests.sh）。
#
# 参数:
#   --artifact-dir DIR  必填。测试产物目录，同时作为 TMPDIR。
#   --parallel N        可选，默认 4。同时作为 go test 的 -p 与 -parallel。
#
# 环境变量（输入）:
#   AGENTFLOW_REAL_TESTS_GRANTED  必须是 true。它由授权判定 Job 产生，不是第二个
#                                 用户开关；不是 true 时本脚本拒绝执行并失败，
#                                 避免"没跑任何真实测试"被当成成功。
#
# 输出:
#   DIR/real-tests-output.log  完整控制台输出。
#   不写 GITHUB_OUTPUT；结论由退出码表达。
#
# 退出码:
#   0  真实测试包全部通过（用例自身按套件配置跳过时不计为通过，见日志中的 WARN [SKIP]）
#   1  任意测试包失败，或未获授权
#   2  参数缺失或非法
#
# 副作用: 创建并写入 --artifact-dir 下的文件；会连接真实测试设备（SSH、远程 Docker、
#         容器内 Pi）。只在获得授权后由 real-tests Job 调用。
set -euo pipefail

usage() {
    echo "用法: run-real-tests.sh --artifact-dir DIR [--parallel N]"
    echo "  执行真实外部依赖测试包，并把日志与测试工作空间写入 --artifact-dir。"
}

artifact_dir=""
parallel="4"

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

# 授权标记由工作流的授权判断 Job 产生；缺失或非 true 时拒绝执行，不得默默跳过。
if [ "${AGENTFLOW_REAL_TESTS_GRANTED:-}" != "true" ]; then
    echo "::error::未获真实测试授权（AGENTFLOW_REAL_TESTS_GRANTED 不是 true），拒绝执行真实外部依赖测试"
    echo "::error::真实测试只由授权判定通过的 Job 触发；普通 CI 不读取任何真实设备凭据。"
    exit 1
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"

mkdir -p "$artifact_dir"
artifact_dir="$(cd "$artifact_dir" && pwd)"
log_file="$artifact_dir/real-tests-output.log"

export TMPDIR="$artifact_dir"

echo "仓库根目录: $repo_root"
echo "测试工作空间: $TMPDIR"
echo "测试日志: $log_file"

cd "$repo_root"

# 只执行需要真实外部依赖的测试包：真实 Pi CLI、真实 SSH、真实远程 Docker /
# Agent / 代码保存 / 工作流。本机模拟测试仍由普通 CI 的 go-test 覆盖。
packages=(
    ./tests/integration/pi/...
    ./tests/integration/ssh/...
    ./tests/integration/remote/...
)

status=0
go test -count=1 -v -p "$parallel" -parallel "$parallel" "${packages[@]}" 2>&1 | tee "$log_file" || status=$?
if [ "$status" -ne 0 ]; then
    echo "::error::真实外部依赖测试失败（退出码 $status），测试日志: $log_file"
    exit "$status"
fi

echo "真实外部依赖测试执行完成；跳过情况见 $log_file 中的 WARN [SKIP]（跳过不计为通过）。"
