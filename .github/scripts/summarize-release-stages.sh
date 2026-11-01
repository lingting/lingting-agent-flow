#!/usr/bin/env bash
#
# 用途: 汇总多阶段流水线的结构化结论（publish.yml 的发布阶段、release.yml 的构建
#       阶段）。任一阶段不是 success 就失败：被跳过与未执行都不计为通过。
#
# 参数:
#   --title TEXT            必填。摘要标题，例如 "发布结论"。
#   --stage "名称=结论"     至少一个，可重复。结论取值来自 needs.<job>.result。
#   --version VERSION       可选。写入摘要的版本号。
#   --commit SHA            可选。写入摘要的提交号。
#   --note TEXT             可选，可重复。追加的说明行。
#   --report-path FILE      可选。报告文件路径，默认 $RUNNER_TEMP/<标题>.md。
#
# 环境变量:
#   GITHUB_STEP_SUMMARY  可选；未设置时只写到报告文件与控制台。
#
# 输出:
#   报告文件（Markdown）、控制台与 GITHUB_STEP_SUMMARY：阶段结论表与说明。
#   不写 GITHUB_OUTPUT：结论由退出码表达。
#
# 退出码:
#   0  全部阶段均为 success
#   1  存在非 success 阶段（跳过与失败都不计为通过）
#   2  参数缺失或非法
#
# 副作用: 写报告文件（默认在 RUNNER_TEMP 下）并追加 Job 摘要；不做任何网络访问，
#         不创建 Release、不上传或修改任何制品。
set -euo pipefail

usage() {
    echo "用法: summarize-release-stages.sh --title TEXT --stage \"名称=结论\" [--stage ...]"
    echo "        [--version V] [--commit SHA] [--note TEXT] [--report-path FILE]"
    echo "  汇总阶段结论；任一阶段不是 success 时以非零退出码结束。"
}

title=""
version=""
commit=""
report_path=""
stages=()
notes=()

while [ $# -gt 0 ]; do
    case "$1" in
        --title)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --title 缺少取值" >&2
                exit 2
            fi
            title="$2"
            shift 2
            ;;
        --stage)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --stage 缺少取值" >&2
                exit 2
            fi
            stages+=("$2")
            shift 2
            ;;
        --version)
            if [ $# -lt 2 ]; then
                echo "错误: --version 缺少取值" >&2
                exit 2
            fi
            version="$2"
            shift 2
            ;;
        --commit)
            if [ $# -lt 2 ]; then
                echo "错误: --commit 缺少取值" >&2
                exit 2
            fi
            commit="$2"
            shift 2
            ;;
        --note)
            if [ $# -lt 2 ]; then
                echo "错误: --note 缺少取值" >&2
                exit 2
            fi
            notes+=("$2")
            shift 2
            ;;
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

if [ -z "$title" ]; then
    echo "错误: 必须提供 --title" >&2
    usage >&2
    exit 2
fi
if [ "${#stages[@]}" -eq 0 ]; then
    echo "错误: 至少需要一个 --stage \"名称=结论\"" >&2
    usage >&2
    exit 2
fi

if [ -z "$report_path" ]; then
    report_path="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/${title}.md"
fi

failed=0

{
    echo "## ${title}"
    echo
    echo "| 阶段 | 结论 |"
    echo "| --- | --- |"
} > "$report_path"

for stage in "${stages[@]}"; do
    name="${stage%%=*}"
    result="${stage#*=}"
    if [ "$name" = "$stage" ]; then
        echo "::error::--stage 取值必须是 \"名称=结论\" 形式，实际: $stage"
        exit 2
    fi
    echo "| ${name} | ${result:-（空）} |" >> "$report_path"
    if [ "$result" != "success" ]; then
        failed=1
    fi
done

if [ -n "$version" ] || [ -n "$commit" ]; then
    {
        echo
        echo "版本：\`${version:-（未解析）}\`　提交：\`${commit:-（未解析）}\`"
    } >> "$report_path"
fi

for note in "${notes[@]}"; do
    echo "$note" >> "$report_path"
done

cat "$report_path"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    cat "$report_path" >> "$GITHUB_STEP_SUMMARY"
fi

if [ "$failed" -ne 0 ]; then
    echo "::error::${title}未达成：存在非成功的阶段（跳过与失败都不计为通过）"
    exit 1
fi

echo "${title}：全部阶段成功。"
