#!/usr/bin/env bash
#
# 用途: 执行一个普通 Go 测试分组（unit / local / e2e）。分组归属、Makefile 入口与包模式
#       全部来自 go-test-groups.sh，本脚本不重复定义测试范围；真实外部依赖分组在这里被
#       明确拒绝，避免普通 CI 无意执行真实设备测试。
#
# 参数:
#   --group NAME        必填。普通分组名（unit / local / e2e），由 go-test-groups.sh 定义。
#   --artifact-dir DIR  必填。测试产物目录；同时作为 TMPDIR，使 testkit 生成的随机工作空间
#                       与 test.log 落在可上传的位置。
#   --parallel N        可选，默认 4。作为 Makefile 的 PARALLEL（go test 的 -p / -parallel）。
#
# 环境变量（输入）:
#   GITHUB_STEP_SUMMARY  可选。追加本分组的耗时、包数量、用例结论与产物位置摘要。
#
# 输出:
#   DIR/<group>-output.log  该分组的完整控制台输出。
#   DIR/<group>-summary.txt 机器可读的执行记录（分组、时间、耗时、退出码、包数量、用例数）。
#   不写 GITHUB_OUTPUT：步骤结论由退出码表达。
#
# 退出码:
#   0  该分组的测试全部通过
#   1  测试失败或该分组不是普通分组
#   2  参数缺失或非法
#
# 副作用: 创建并写入 --artifact-dir 下的文件；不修改仓库内容，不访问真实外部设备。
set -euo pipefail

usage() {
    echo "用法: run-go-group-tests.sh --group NAME --artifact-dir DIR [--parallel N]"
    echo "  执行 go-test-groups.sh 中定义的普通测试分组，并把日志与摘要写入 --artifact-dir。"
}

artifact_dir=""
group=""
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
        --group)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --group 缺少取值" >&2
                exit 2
            fi
            group="$2"
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

if [ -z "$group" ]; then
    echo "错误: 必须提供 --group" >&2
    usage >&2
    exit 2
fi
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
groups_script="$script_dir/go-test-groups.sh"

# 分组归属只从清单解析：这里既不列举目录，也不认识任何具体测试包。
normal_groups="$(bash "$groups_script" --list normal)"
real_groups="$(bash "$groups_script" --list real)"
if ! grep -qx "$group" <<< "$real_groups"; then
    if ! grep -qx "$group" <<< "$normal_groups"; then
        echo "错误: 未知分组 ${group}，普通分组只有: $(tr '\n' ' ' <<< "$normal_groups")" >&2
        exit 2
    fi
else
    echo "错误: 分组 ${group} 属于真实外部依赖测试，必须由 run-real-tests.sh 在授权后执行" >&2
    exit 1
fi

make_target="$(bash "$groups_script" --make-target "$group")"
mapfile -t patterns < <(bash "$groups_script" --packages "$group")
if [ "${#patterns[@]}" -eq 0 ]; then
    echo "错误: 分组 ${group} 没有解析到任何包模式" >&2
    exit 1
fi


mkdir -p "$artifact_dir"
artifact_dir="$(cd "$artifact_dir" && pwd)"
log_file="$artifact_dir/${group}-output.log"
record_file="$artifact_dir/${group}-summary.txt"

# 测试工作空间落在 TMPDIR 之下：指向工作区内固定目录以便上传失败产物。
export TMPDIR="$artifact_dir"

echo "分组: ${group}"
echo "仓库根目录: $repo_root"
echo "测试工作空间: $TMPDIR"
echo "测试日志: $log_file"
echo "测试包模式: ${patterns[*]}"
echo "并行度: ${parallel}"

cd "$repo_root"

# 包数量按清单里的模式统计（只计真正带测试文件的包），用于耗时记录与摘要。
package_count="$(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' \
    "${patterns[@]}" | sed '/^$/d' | wc -l | tr -d ' ')"
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
started_seconds=$SECONDS
status=0
PARALLEL="$parallel" make "$make_target" 2>&1 | tee "$log_file" || status=$?
duration=$((SECONDS - started_seconds))
finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# 用例数量取自 go test -v 的顶层结论行（子用例带缩进，不计入）；包结论单独统计。
count_matches() {
    local pattern="$1"
    local value
    value="$(grep -c "$pattern" "$log_file" || true)"
    printf '%s\n' "${value:-0}"
}
passed_tests="$(count_matches '^--- PASS:')"
failed_tests="$(count_matches '^--- FAIL:')"
skipped_tests="$(count_matches '^--- SKIP:')"
passed_packages="$(count_matches '^ok[[:space:]]')"
failed_packages="$(count_matches '^FAIL[[:space:]]')"

{
    echo "group=${group}"
    echo "make_target=${make_target}"
    echo "parallel=${parallel}"
    echo "packages=${package_count}"
    echo "passed_tests=${passed_tests}"
    echo "failed_tests=${failed_tests}"
    echo "skipped_tests=${skipped_tests}"
    echo "passed_packages=${passed_packages}"
    echo "failed_packages=${failed_packages}"
    echo "started_at=${started_at}"
    echo "finished_at=${finished_at}"
    echo "duration_seconds=${duration}"
    echo "exit_code=${status}"
    echo "log=${log_file}"
} > "$record_file"

summary="$(
    echo "### Go 测试组 ${group}"
    echo
    echo "| 项目 | 值 |"
    echo "| --- | --- |"
    echo "| 分组 / 入口 | ${group} / make ${make_target} |"
    echo "| 包模式 | $(printf '%s ' "${patterns[@]}")|"
    echo "| 并行度 | -p ${parallel} / -parallel ${parallel} |"
    echo "| 测试包 | ${package_count} |"
    echo "| 顶层用例 | 通过 ${passed_tests} / 失败 ${failed_tests} / 跳过 ${skipped_tests} |"
    echo "| 包结论 | 通过 ${passed_packages} / 失败 ${failed_packages} |"
    echo "| 开始 / 结束（UTC） | ${started_at} / ${finished_at} |"
    echo "| 耗时 | ${duration} 秒 |"
    echo "| 退出码 | ${status} |"
    echo "| 测试日志 | $(basename "$log_file") |"
)"
printf '%s\n' "$summary"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf '%s\n' "$summary" >> "$GITHUB_STEP_SUMMARY"
fi

if [ "$status" -ne 0 ]; then
    echo "::error::Go 测试分组 ${group} 失败（退出码 $status），测试日志: $log_file"
    exit "$status"
fi

echo "Go 测试分组 ${group} 通过：${package_count} 个测试包，耗时 ${duration} 秒。"
