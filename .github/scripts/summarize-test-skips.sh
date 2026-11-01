#!/usr/bin/env bash
#
# 用途: 汇总测试日志中的外部依赖跳过原因（WARN [SKIP]），写入 Job 摘要。
#       只做展示，不参与结论判定：跳过与未执行都不计为通过，但也不会让 Job 失败。
#
# 参数:
#   --base DIR     必填。testkit 生成的测试工作空间根目录
#                  （<artifact-dir>/lingting-agent-flow-tests）。
#   --title TEXT   必填。摘要小节标题。
#
# 环境变量:
#   GITHUB_STEP_SUMMARY  可选；未设置时摘要只写到标准输出（不写本地文件）。
#
# 输出:
#   GITHUB_STEP_SUMMARY: 去重后的跳过记录；没有记录时输出"没有产生跳过记录"。
#   标准输出：同样的摘要内容。
#   不写 GITHUB_OUTPUT。
#
# 退出码:
#   0  汇总完成（无论是否存在跳过记录）
#   2  参数缺失或非法
#
# 副作用: 只读 test.log；只追加 Job 摘要，不修改任何测试产物。
set -euo pipefail

usage() {
    echo "用法: summarize-test-skips.sh --base DIR --title TEXT"
    echo "  汇总 DIR 下 test.log 中的 WARN [SKIP] 记录并写入 Job 摘要。"
}

base_dir=""
title=""

while [ $# -gt 0 ]; do
    case "$1" in
        --base)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --base 缺少取值" >&2
                exit 2
            fi
            base_dir="$2"
            shift 2
            ;;
        --title)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --title 缺少取值" >&2
                exit 2
            fi
            title="$2"
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
    echo "错误: 必须提供 --base" >&2
    usage >&2
    exit 2
fi
if [ -z "$title" ]; then
    echo "错误: 必须提供 --title" >&2
    usage >&2
    exit 2
fi

# 只读取 test.log：逐用例结论、跳过原因与应用日志都在其中。
skipped=""
if [ -d "$base_dir" ]; then
    skipped="$(grep -rh 'WARN \[SKIP\]' --include=test.log "$base_dir" 2>/dev/null | sort -u || true)"
fi

render_summary() {
    echo "### $title"
    echo
    if [ -n "$skipped" ]; then
        echo '```text'
        echo "$skipped"
        echo '```'
        echo
        echo "以上用例因 CI 未提供真实 Pi / SSH / 远程 Docker 配置而跳过，不计为通过。"
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
