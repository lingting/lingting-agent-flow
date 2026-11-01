#!/usr/bin/env bash
#
# 用途: 汇总真实外部依赖测试 Job 的结论：授权模式、配置是否就绪、测试步骤结论、
#       判定原因，以及按套件配置跳过（WARN [SKIP]）的用例清单。
#       只做展示，不参与结论判定：Job 结论完全由执行步骤的退出码决定。
#
# 参数:
#   --skip-base DIR  必填。testkit 生成的测试工作空间根目录。
#
# 环境变量（输入，全部来自上一步的结论与授权判断 Job 的输出）:
#   GATE_MODE        授权模式（self / collaborator / all，或空）。
#   GATE_REASON      授权判定原因。
#   CONFIGURED       真实测试配置是否生成（true / false）。
#   TESTS_OUTCOME    执行步骤结论（success / failure / skipped / 空）。
#   GITHUB_STEP_SUMMARY  可选；未设置时摘要只写到标准输出。
#
# 输出:
#   GITHUB_STEP_SUMMARY 与标准输出：结论表与跳过清单。不写 GITHUB_OUTPUT。
#
# 退出码:
#   0  汇总完成（本脚本从不因结论而失败）
#   2  参数缺失或非法
#
# 副作用: 只读 test.log；只追加 Job 摘要，不修改测试产物，不输出凭据。
set -euo pipefail

usage() {
    echo "用法: summarize-real-tests.sh --skip-base DIR"
    echo "  读取授权与测试结论环境变量，生成真实测试摘要。"
}

base_dir=""

while [ $# -gt 0 ]; do
    case "$1" in
        --skip-base)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --skip-base 缺少取值" >&2
                exit 2
            fi
            base_dir="$2"
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

if [ -z "$base_dir" ]; then
    echo "错误: 必须提供 --skip-base" >&2
    usage >&2
    exit 2
fi

gate_mode="${GATE_MODE:-}"
gate_reason="${GATE_REASON:-}"
configured="${CONFIGURED:-}"
tests_outcome="${TESTS_OUTCOME:-}"

skipped=""
if [ -d "$base_dir" ]; then
    skipped="$(grep -rh 'WARN \[SKIP\]' --include=test.log "$base_dir" 2>/dev/null | sort -u || true)"
fi

render_summary() {
    echo "### 真实外部依赖测试"
    echo
    echo "| 项目 | 值 |"
    echo "| --- | --- |"
    echo "| 授权模式 | ${gate_mode} |"
    echo "| 测试配置 | ${configured} |"
    echo "| 测试步骤结论 | ${tests_outcome} |"
    echo "| 授权原因 | ${gate_reason} |"
    echo
    if [ "$configured" != "true" ]; then
        echo "本次运行没有执行真实测试：缺少真实测试配置或凭据，未执行的用例不计为通过。"
    elif [ -n "$skipped" ]; then
        echo '```text'
        echo "$skipped"
        echo '```'
        echo
        echo "以上用例按套件配置跳过（目标或镜像未启用、本机/容器内缺少 CLI），不计为通过。"
    else
        echo "本次运行没有产生跳过记录。"
    fi
}

# 先渲染成字符串：同一次输出既打印到控制台，也追加到 Job 摘要，避免两处内容漂移。
summary="$(render_summary)"
printf '%s\n' "$summary"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf '%s\n' "$summary" >> "$GITHUB_STEP_SUMMARY"
fi
