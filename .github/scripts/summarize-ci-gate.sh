#!/usr/bin/env bash
#
# 用途: CI 门禁汇总。结论只来自 Job 与步骤的结构化结果（needs.*.result 与
#       steps.*.outcome），不对测试日志做字符串搜索冒充判定。
#
# 参数:
#   --report-path FILE  可选。Markdown 结论文件路径，默认 $RUNNER_TEMP/ci-summary.md
#                       （未设置 RUNNER_TEMP 时退到系统临时目录）。
#
# 环境变量（输入，全部是 GitHub Actions 的结构化结论）:
#   GO_STATIC_RESULT / GO_STATIC_STEP     go-static 的 Job 结论与检查步骤结论
#   GO_TEST_RESULT / GO_TEST_PREPARE / GO_TEST_STEP
#   GO_RACE_RESULT / GO_RACE_STEP
#   UI_TEST_RESULT / UI_TEST_PREPARE / UI_TEST_STEP
#   REAL_GATE_RESULT / REAL_GATE_MODE / REAL_GATE_ENABLED / REAL_GATE_REASON
#   REAL_TESTS_RESULT                     真实测试 Job 结论
#   GITHUB_STEP_SUMMARY                   可选；未设置时只写到报告文件与控制台
#
# 输出:
#   报告文件（Markdown）与控制台：逐 Job 结论表、真实测试授权说明。
#   GITHUB_STEP_SUMMARY: 同一份报告。
#   不写 GITHUB_OUTPUT：结论由退出码表达。
#
# 退出码:
#   0  全部必需 Job 成功（真实测试未执行时标注不计为通过，但不算失败）
#   1  存在非成功的必需 Job，或真实测试执行后失败
#   2  参数错误
#
# 副作用: 写报告文件（默认在 RUNNER_TEMP 下）并追加 Job 摘要；不做任何网络或设备访问。
set -euo pipefail

usage() {
    echo "用法: summarize-ci-gate.sh [--report-path FILE]"
    echo "  读取 Job 与步骤结论环境变量，生成 CI 门禁结论并据此决定退出码。"
}

report_path=""

while [ $# -gt 0 ]; do
    case "$1" in
        --report-path)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --report-path 缺少取值" >&2
                exit 2
            fi
            report_path="$2"
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

if [ -z "$report_path" ]; then
    report_path="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/ci-summary.md"
fi

{
    echo "## CI 门禁结论"
    echo
    echo "| Job | Job 结论 | 汇总判定 |"
    echo "| --- | --- | --- |"
} > "$report_path"

failed=0

# 逐行处理必需 Job：每行格式为 Job 名|Job 结论|测试步骤结论|环境准备步骤结论。
# 这里用数组而不是 here-document：YAML 块标量的内容自带缩进，缩进的结束标记
# 不会被 bash 识别为 heredoc 终止行。
for row in \
    "go-static|${GO_STATIC_RESULT:-}|${GO_STATIC_STEP:-}|-" \
    "go-test|${GO_TEST_RESULT:-}|${GO_TEST_STEP:-}|${GO_TEST_PREPARE:-}" \
    "go-race|${GO_RACE_RESULT:-}|${GO_RACE_STEP:-}|-" \
    "ui-test|${UI_TEST_RESULT:-}|${UI_TEST_STEP:-}|${UI_TEST_PREPARE:-}"; do
    IFS='|' read -r job result tests prepare <<< "$row"
    if [ -z "$job" ]; then
        continue
    fi
    case "$result" in
        success)
            verdict="成功"
            ;;
        failure)
            if [ "$tests" = "failure" ]; then
                verdict="失败：测试步骤失败"
            elif [ "$prepare" = "failure" ]; then
                verdict="失败：环境准备失败，测试未执行"
            elif [ "$tests" = "skipped" ] || [ -z "$tests" ]; then
                verdict="失败：测试步骤未执行"
            else
                verdict="失败：测试之外的步骤失败"
            fi
            failed=1
            ;;
        cancelled)
            verdict="未完成：运行被取消"
            failed=1
            ;;
        skipped)
            verdict="未执行：Job 被跳过，不计为通过"
            failed=1
            ;;
        *)
            verdict="未执行：缺少 Job 结论，不计为通过"
            failed=1
            ;;
    esac
    echo "| ${job} | ${result:-（空）} | ${verdict} |" >> "$report_path"
done

# 授权判断 Job 必须成功：它失败说明判定本身没跑完，不能当成"未授权"放行。
case "${REAL_GATE_RESULT:-}" in
    success)
        gate_verdict="成功（判定已完成）"
        ;;
    *)
        gate_verdict="失败：授权判定未完成"
        failed=1
        ;;
esac
echo "| real-tests-gate | ${REAL_GATE_RESULT:-（空）} | ${gate_verdict} |" >> "$report_path"

# 真实测试 Job：未获授权时被跳过是预期状态，不计为通过，但不算失败；
# 实际执行后失败必须让门禁失败，不得把真实测试失败报告为通过。
case "${REAL_TESTS_RESULT:-}" in
    success)
        real_verdict="已执行：结论见该 Job 摘要"
        ;;
    skipped)
        real_verdict="未执行：未获授权或本次事件不参与判定，不计为通过"
        ;;
    failure)
        real_verdict="失败：真实外部依赖测试未通过"
        failed=1
        ;;
    cancelled)
        real_verdict="未完成：运行被取消"
        failed=1
        ;;
    *)
        real_verdict="未执行：缺少 Job 结论，不计为通过"
        failed=1
        ;;
esac
echo "| real-tests | ${REAL_TESTS_RESULT:-（空）} | ${real_verdict} |" >> "$report_path"

{
    echo
    echo "真实测试授权：模式 \`${REAL_GATE_MODE:-（空）}\`，结论 enabled=${REAL_GATE_ENABLED:-false}。"
    echo "判定原因：${REAL_GATE_REASON:-（空）}。"
    echo
    echo "外部依赖用例（真实 Pi / SSH / 远程 Docker）在未获授权时输出 WARN \[SKIP\] 并跳过；"
    echo "跳过与未执行都不计为通过，跳过原因见对应 Job 产物中的 test.log。"
} >> "$report_path"

cat "$report_path"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    cat "$report_path" >> "$GITHUB_STEP_SUMMARY"
fi

if [ "$failed" -ne 0 ]; then
    echo "::error::CI 门禁未通过：存在非成功的必需 Job 或真实测试执行失败"
    exit 1
fi

echo "全部必需 Job 通过；真实测试未执行时已标注不计为通过。"
