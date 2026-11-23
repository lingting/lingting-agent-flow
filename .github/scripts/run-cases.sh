#!/usr/bin/env bash
#
# 用途: Case 执行的唯一入口（Makefile 与 CI 共用）。选择条件只从 go-test-groups.sh 解析，
#       本脚本不认识任何具体分组名；执行器二进制由本脚本构建，执行日志与统一报告都落在
#       --artifact-dir 下，跳过统计与零残留结论只读 report.json，不解析日志文本。
#
# 参数:
#   --kind KIND         必填。normal / local / e2e / selfcheck / frontend / unit / real。
#                       normal 展开为全部普通 kind。
#   --artifact-dir DIR  必填。执行器产物根目录，本次运行落在 DIR/<run-id>/。
#   --group GROUP       可选。只执行该 kind 下的一个已声明分组（CI matrix 分片使用）。
#   --parallel N        可选，默认 4。作为执行器的 --parallel。
#   --timeout DURATION  可选。作为执行器的默认用例上限，单条 Case 声明的 Timeout 优先。
#   --bin PATH          可选。执行器二进制路径，默认 <仓库>/bin/agentflow-test。
#
# 环境变量（输入）:
#   AGENTFLOW_REAL_TESTS_GRANTED      kind=real 时必须为 true：它由授权判定 Job 产生，
#                                     不是第二个用户开关；缺失时本脚本直接失败，
#                                     不把"没跑任何真实用例"当成成功，也不回退到本机替身。
#   AGENTFLOW_REAL_TESTS_GRANT_REASON 可选。授权原因，随判定结论写入报告。
#   PACKAGE_LIMIT                    可选。运行级包级并发上限，透传为 --package-limit；
#                                     留空表示按用例声明。
#   POOLS                            可选。命名并发池容量，形如 "name=limit,name=limit"，
#                                     逐条透传为 --pool；本脚本不解释池的含义。
#   GITHUB_STEP_SUMMARY              可选。追加结论摘要。
#
# 输出:
#   DIR/<run-id>/report.json       统一报告（用例结论、跳过原因、零残留、退出码）。
#   DIR/cases-<kind>-output.log    本次执行的控制台输出。
#   DIR/cases-<kind>-summary.txt   机器可读的结论记录。
#   不写 GITHUB_OUTPUT：步骤结论由退出码表达。
#
# 退出码:
#   0  选定 Case 全部通过，且零残留终检未失败
#   1  Case 失败、零残留失败、执行器无法运行，或 kind=real 未获授权
#   2  参数缺失或非法
#
# 副作用: 执行选定的用例（kind=real 会连接真实设备），写 --artifact-dir；不修改仓库内容。
set -euo pipefail

usage() {
    echo "用法: run-cases.sh --kind KIND --artifact-dir DIR [--group GROUP] [--parallel N] [--timeout D] [--bin PATH]"
    echo "  KIND 取 normal / local / e2e / selfcheck / frontend / unit / real；选择条件由 go-test-groups.sh 定义。"
}

artifact_dir=""
kind=""
parallel="4"
timeout=""
bin_path=""
group=""

while [ $# -gt 0 ]; do
    case "$1" in
        --kind | --artifact-dir | --group | --parallel | --timeout | --bin)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: $1 缺少取值" >&2
                exit 2
            fi
            case "$1" in
                --kind) kind="$2" ;;
                --artifact-dir) artifact_dir="$2" ;;
                --group) group="$2" ;;
                --parallel) parallel="$2" ;;
                --timeout) timeout="$2" ;;
                --bin) bin_path="$2" ;;
            esac
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

if [ -z "$kind" ]; then
    echo "错误: 必须提供 --kind" >&2
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

selector_args=(--case-selectors "$kind")
if [ -n "$group" ]; then
    selector_args+=("$group")
fi
selector_output="$(bash "$groups_script" "${selector_args[@]}")"
selectors=()
while IFS= read -r selector; do
    [ -n "$selector" ] && selectors+=("$selector")
done <<< "$selector_output"
if [ "${#selectors[@]}" -eq 0 ]; then
    if [ -n "$group" ]; then
        echo "错误: kind ${kind} 没有分组 ${group}（选择条件只能来自 go-test-groups.sh）" >&2
    else
        echo "错误: kind ${kind} 没有解析到任何选择条件（kind 只取 normal / local / e2e / selfcheck / frontend / unit / real）" >&2
    fi
    exit 2
fi

# 真实依赖授权只由环境变量表达：未获授权时立即失败，不执行任何真实用例。
real_mode="off"
if [ "$kind" = "real" ]; then
    if [ "${AGENTFLOW_REAL_TESTS_GRANTED:-}" != "true" ]; then
        echo "::error::未获真实测试授权（AGENTFLOW_REAL_TESTS_GRANTED 不是 true），拒绝执行真实外部依赖分组"
        echo "::error::真实分组只由授权判定通过的 Job 触发；普通门禁不读取任何真实设备凭据。"
        exit 1
    fi
    real_mode="auto"
fi

if [ -z "$bin_path" ]; then
    bin_path="$repo_root/bin/agentflow-test"
fi
mkdir -p "$(dirname "$bin_path")"
if ! (cd "$repo_root" && go build -o "$bin_path" ./cmd/agentflow-test); then
    echo "::error::构建统一测试执行器失败: $bin_path"
    exit 1
fi

mkdir -p "$artifact_dir"
artifact_dir="$(cd "$artifact_dir" && pwd)"
log_file="$artifact_dir/cases-${kind}-output.log"
record_file="$artifact_dir/cases-${kind}-summary.txt"

echo "仓库根目录: $repo_root"
echo "执行器: $bin_path"
echo "分组类型: ${kind}"
echo "指定分组: ${group:-（全部）}"
echo "选择条件: ${selectors[*]}"
echo "产物根目录: $artifact_dir"
echo "真实依赖授权: ${real_mode}"
echo "执行日志: $log_file"

cd "$repo_root"

arguments=(
    --parallel "$parallel"
    --artifact-dir "$artifact_dir"
    --real-tests "$real_mode"
)
# 并发额度只做透传：脚本不认识任何具体包或池，额度语义全在执行器里。
if [ -n "${PACKAGE_LIMIT:-}" ]; then
    arguments+=(--package-limit "$PACKAGE_LIMIT")
fi
if [ -n "${POOLS:-}" ]; then
    IFS=', ' read -r -a pool_specs <<< "$POOLS"
    for pool_spec in "${pool_specs[@]}"; do
        if [ -n "$pool_spec" ]; then
            arguments+=(--pool "$pool_spec")
        fi
    done
fi
if [ -n "$timeout" ]; then
    arguments+=(--timeout "$timeout")
fi
arguments+=("${selectors[@]}")

status=0
"$bin_path" "${arguments[@]}" 2>&1 | tee "$log_file" || status=$?

# 本次运行的产物目录是最新创建的那个：报告的路径与 run-id 都以它为准。
latest_run=""
if [ -d "$artifact_dir" ]; then
    latest_run="$(find "$artifact_dir" -mindepth 1 -maxdepth 1 -type d -name '*z-*' -print 2>/dev/null | sort | tail -n 1)"
fi
report_path=""
if [ -n "$latest_run" ] && [ -f "$latest_run/report.json" ]; then
    report_path="$latest_run/report.json"
fi

emit_summary() {
    local text="$1"
    printf '%s\n' "$text"
    if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
        printf '%s\n' "$text" >> "$GITHUB_STEP_SUMMARY"
    fi
}

# 结论、跳过原因与零残留只从 report.json 读取（D4）：没有报告就不做文本猜测。
if [ -z "$report_path" ]; then
    emit_summary "### Case 执行结论（${kind}）

没有找到 report.json（产物根目录 ${artifact_dir}），本次运行无法给出结构化结论。
执行器退出码：${status}。"
    echo "::error::Case 执行没有产生统一报告，按异常处理（退出码 ${status}）" >&2
    if [ "$status" -eq 0 ]; then
        status=1
    fi
    exit "$status"
fi

# 摘要只读 report.json：CI 用 python3，本地 Windows 环境一般只有 python。
python_bin=""
for candidate in python3 python; do
    if command -v "$candidate" >/dev/null 2>&1; then
        python_bin="$candidate"
        break
    fi
done

if [ -n "$python_bin" ]; then
    # 摘要含中文：显式要求 UTF-8，避免 Windows 默认编码把 Markdown 产物写成乱码。
    PYTHONUTF8=1 PYTHONIOENCODING=utf-8 \
        REPORT_PATH="$report_path" RECORD_PATH="$record_file" CASE_KIND="$kind" \
        "$python_bin" - > "$record_file.markdown" <<'PY'
import json
import os

with open(os.environ["REPORT_PATH"], "r", encoding="utf-8") as handle:
    report = json.load(handle)

totals = report.get("totals", {})
residual = report.get("residual_check", {})
real = report.get("real_tests", {})
concurrency = report.get("concurrency", {})

lines = []
lines.append("### Case 执行结论（{0}）".format(os.environ["CASE_KIND"]))
lines.append("")
lines.append("| 项目 | 值 |")
lines.append("| --- | --- |")
lines.append("| 运行 ID | `{0}` |".format(report.get("run_id", "")))
lines.append("| 选择条件 | `{0}` |".format(" ".join(report.get("selection", []))))
lines.append("| 用例 | 通过 {0} / 失败 {1} / 跳过 {2} / panic {3} / 超时 {4} / 异常 {5} |".format(
    totals.get("passed", 0),
    totals.get("failed", 0),
    totals.get("skipped", 0),
    totals.get("panics", 0),
    totals.get("timeouts", 0),
    totals.get("errors", 0),
))
lines.append("| 并发 | 上限 {0} / 峰值 {1} |".format(
    concurrency.get("parallel_limit", 0), concurrency.get("max_in_flight", 0)))
lines.append("| 零残留 | {0}{1} |".format(
    residual.get("status", ""),
    "（{0}）".format(residual.get("reason")) if residual.get("reason") else "",
))
lines.append("| 真实依赖授权 | allowed={0} / {1} |".format(
    real.get("allowed", False), real.get("reason", "")))
lines.append("| 报告 | `{0}` |".format(os.environ["REPORT_PATH"]))

skipped = [case for case in report.get("cases", []) if case.get("status") == "skipped"]
lines.append("")
if skipped:
    lines.append("跳过的用例（不计为通过）：")
    lines.append("")
    for case in skipped:
        lines.append("- `{0}`：{1}".format(case.get("id", ""), case.get("skip_reason", "未给出原因")))
else:
    lines.append("本次运行没有跳过的用例。")

failed = [case for case in report.get("cases", []) if case.get("status") not in ("passed", "skipped")]
if failed:
    lines.append("")
    lines.append("未通过的用例：")
    lines.append("")
    for case in failed:
        message = (case.get("message") or "").splitlines()
        lines.append("- `{0}`（{1}）{2}".format(
            case.get("id", ""), case.get("status", ""), message[0] if message else ""))

with open(os.environ["RECORD_PATH"], "w", encoding="utf-8", newline="\n") as handle:
    for key, value in (
        ("run_id", report.get("run_id", "")),
        ("selection", " ".join(report.get("selection", []))),
        ("cases", totals.get("cases", 0)),
        ("passed", totals.get("passed", 0)),
        ("failed", totals.get("failed", 0)),
        ("skipped", totals.get("skipped", 0)),
        ("panics", totals.get("panics", 0)),
        ("timeouts", totals.get("timeouts", 0)),
        ("errors", totals.get("errors", 0)),
        ("residual_check", residual.get("status", "")),
        ("exit_code", report.get("exit_code", 0)),
        ("report", os.environ["REPORT_PATH"]),
    ):
        handle.write("{0}={1}\n".format(key, value))
    for case in skipped:
        handle.write("skip_reason[{0}]={1}\n".format(case.get("id", ""), case.get("skip_reason", "")))

print("\n".join(lines))
PY
    if [ -f "$record_file.markdown" ]; then
        emit_summary "$(cat "$record_file.markdown")"
        rm -f "$record_file.markdown"
    fi
else
    emit_summary "### Case 执行结论（${kind}）

本机没有 python3 / python，跳过 report.json 摘要；结构化结论见 \`${report_path}\`。"
fi

if [ "$status" -ne 0 ]; then
    echo "::error::Case 执行未通过（退出码 $status），执行日志: $log_file，报告: $report_path"
    exit "$status"
fi

echo "Case 执行通过：$(basename "$artifact_dir")，报告: $report_path。"
