#!/usr/bin/env bash
#
# 用途: 解析并校验正式发布的版本号与提交号，写出构建与发布共用的输出。
#       publish.yml 与 release.yml 共用同一实现，版本格式只在 internal/release 与
#       本脚本中各校验一次，不在两份 YAML 里重复。
#
# 参数:
#   --require-tag        可选。要求当前 ref 必须是版本标签（publish.yml 使用：
#                        分支上的手动运行不算正式发布）。
#   --input-version V    可选。手动输入的版本（release.yml 的 workflow_dispatch 使用），
#                        与标签推导出的版本必须一致。
#
# 环境变量（输入，来自 GitHub 事件上下文）:
#   REF_TYPE       github.ref_type（tag / branch）
#   REF_NAME       github.ref_name
#   COMMIT_SHA     github.sha
#   GITHUB_OUTPUT  可选；写入 version / commit / artifact_name。
#   GITHUB_STEP_SUMMARY  可选；写入版本摘要。
#
# 输出:
#   GITHUB_OUTPUT: version（vMAJOR.MINOR.PATCH）、commit（40 位 SHA）、
#                  artifact_name（agentflow-release-<version>）。
#   GITHUB_STEP_SUMMARY: 版本与提交的摘要表。
#   标准输出：解析结论。
#
# 退出码:
#   0  解析成功
#   1  ref 不是标签、版本格式错误、输入与标签不一致、提交号不是完整 SHA
#   2  参数错误
#
# 副作用: 只读事件上下文；只写 GITHUB_OUTPUT / GITHUB_STEP_SUMMARY，不改动仓库。
set -euo pipefail

usage() {
    echo "用法: resolve-release-version.sh [--require-tag] [--input-version V]"
    echo "  校验版本标签与提交号，输出 version / commit / artifact_name。"
}

require_tag="false"
input_version=""

while [ $# -gt 0 ]; do
    case "$1" in
        --require-tag)
            require_tag="true"
            shift
            ;;
        --input-version)
            if [ $# -lt 2 ]; then
                echo "错误: --input-version 缺少取值" >&2
                exit 2
            fi
            input_version="$2"
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

ref_type="${REF_TYPE:-}"
ref_name="${REF_NAME:-}"
commit_sha="${COMMIT_SHA:-}"

version=""
if [ "$ref_type" = "tag" ]; then
    # 标签推送：版本号必须与标签完全一致，不接受任何改写。
    version="$ref_name"
elif [ "$require_tag" = "true" ]; then
    echo "::error::正式发布只能由版本标签触发：当前 ref 是 ${ref_type:-（空）}（${ref_name:-（空）}）"
    exit 1
fi

if [ -n "$input_version" ]; then
    if [ -n "$version" ] && [ "$input_version" != "$version" ]; then
        echo "::error::手动输入的版本 $input_version 与标签 $version 不一致"
        exit 1
    fi
    version="$input_version"
fi

if [ -z "$version" ]; then
    echo "::error::无法确定版本号：请推送 vMAJOR.MINOR.PATCH 标签，或手动指定 version"
    exit 1
fi

# 与 internal/release.ValidateVersion 保持同一格式：拒绝 dev、分支名与构建号，
# 在构建开始前就失败，避免把无效版本写进制品文件名。
if ! [[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "::error::版本号 $version 不是 vMAJOR.MINOR.PATCH 形式"
    exit 1
fi
if ! [[ "$commit_sha" =~ ^[0-9a-f]{40}$ ]]; then
    echo "::error::提交号 ${commit_sha:-（空）} 不是完整的 Git SHA"
    exit 1
fi

# 制品名只在这里生成一次：构建上传与发布下载都用同一个取值，避免两处命名漂移。
artifact_name="agentflow-release-$version"

echo "版本: $version"
echo "提交: $commit_sha"
echo "制品名: $artifact_name"

if [ -n "${GITHUB_OUTPUT:-}" ]; then
    {
        echo "version=$version"
        echo "commit=$commit_sha"
        echo "artifact_name=$artifact_name"
    } >> "$GITHUB_OUTPUT"
fi

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    {
        echo "### 发布版本"
        echo
        echo "| 字段 | 取值 |"
        echo "| --- | --- |"
        echo "| 版本 | \`$version\` |"
        echo "| 提交 | \`$commit_sha\` |"
    } >> "$GITHUB_STEP_SUMMARY"
fi
