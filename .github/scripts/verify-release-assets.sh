#!/usr/bin/env bash
#
# 用途: 发布制品核验。校验摘要、发布目录清单与（可选）远端 Release 的实际资产列表，
#       供"构建之后 / 上传之后"复用同一份核验规则。
#
# 参数:
#   --dir DIR              必填。制品目录（或远端资产的下载目录）。
#   --version V            必填。vMAJOR.MINOR.PATCH。
#   --commit SHA           必填。40 位提交号，核验制品与源码提交一致。
#   --require-provenance   可选。要求清单声明来源证明（上传后核验使用）。
#   --download-from-tag T  可选。先从 GitHub Release T 下载资产，再核验远端资产列表
#                          与下载内容（上传后核验使用）。
#   --assets-file FILE     可选。远端资产名列表文件；--download-from-tag 未指定时
#                          默认 $RUNNER_TEMP/release-assets.txt。
#
# 环境变量:
#   GITHUB_REPOSITORY     --download-from-tag 必填（owner/repo）。
#   RUNNER_TEMP           可选，临时文件目录。
#
# 输出:
#   标准输出：sha256sum -c、目录清单与各子命令的核验结论。
#   不写 GITHUB_OUTPUT / GITHUB_STEP_SUMMARY；结论由退出码表达。
#
# 退出码:
#   0  全部核验通过
#   1  核验失败（摘要不符、目录有多余文件、资产列表不一致、缺少来源信息等）
#   2  参数缺失或非法
#
# 副作用: 写 --dir 与资产列表临时文件；--download-from-tag 时通过 gh 读取远端 Release
#         资产（只读下载）。不修改仓库、不创建或删除 Release。
set -euo pipefail

usage() {
    echo "用法: verify-release-assets.sh --dir DIR --version V --commit SHA"
    echo "        [--require-provenance] [--download-from-tag TAG] [--assets-file FILE]"
    echo "  校验发布制品的摘要、目录清单与（可选的）远端资产列表。"
}

target_dir=""
version=""
commit=""
require_provenance="false"
download_tag=""
assets_file=""

while [ $# -gt 0 ]; do
    case "$1" in
        --dir)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --dir 缺少取值" >&2
                exit 2
            fi
            target_dir="$2"
            shift 2
            ;;
        --version)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --version 缺少取值" >&2
                exit 2
            fi
            version="$2"
            shift 2
            ;;
        --commit)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --commit 缺少取值" >&2
                exit 2
            fi
            commit="$2"
            shift 2
            ;;
        --require-provenance)
            require_provenance="true"
            shift
            ;;
        --download-from-tag)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --download-from-tag 缺少取值" >&2
                exit 2
            fi
            download_tag="$2"
            shift 2
            ;;
        --assets-file)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --assets-file 缺少取值" >&2
                exit 2
            fi
            assets_file="$2"
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

missing=""
[ -n "$target_dir" ] || missing="$missing --dir"
[ -n "$version" ] || missing="$missing --version"
[ -n "$commit" ] || missing="$missing --commit"
if [ -n "$missing" ]; then
    echo "错误: 缺少必需参数:$missing" >&2
    usage >&2
    exit 2
fi
if ! [[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "错误: 版本号 $version 不是 vMAJOR.MINOR.PATCH 形式" >&2
    exit 2
fi
if ! [[ "$commit" =~ ^[0-9a-f]{40}$ ]]; then
    echo "错误: 提交号 $commit 不是完整的 Git SHA" >&2
    exit 2
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
# 先切到仓库根目录，再解析相对路径：制品目录是相对仓库根目录给出的。
cd "$repo_root"

mkdir -p "$target_dir"
target_dir="$(cd "$target_dir" && pwd)"
if [ -n "$download_tag" ]; then
    if [ -z "${GITHUB_REPOSITORY:-}" ]; then
        echo "错误: --download-from-tag 需要 GITHUB_REPOSITORY（owner/repo）" >&2
        exit 2
    fi
    if [ -z "$assets_file" ]; then
        assets_file="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/release-assets.txt"
    fi
    echo "下载 Release $download_tag 的资产到 $target_dir"
    gh release download "$download_tag" --repo "$GITHUB_REPOSITORY" --dir "$target_dir" --clobber
    # 远端资产列表必须与清单声明完全一致：不能只看上传命令的返回值。
    gh release view "$download_tag" --repo "$GITHUB_REPOSITORY" \
        --json assets --jq '.assets[].name' > "$assets_file"
    if ! go run ./cmd/agentflow-release verify-assets \
        -dir "$target_dir" \
        -version "$version" \
        -assets-file "$assets_file"; then
        echo "::error::远端 Release 资产列表与发布清单不一致"
        exit 1
    fi
fi

# 独立复核：用 sha256sum 校验制品与校验文件一致，而不是只看构建工具的退出码。
if ! (cd "$target_dir" && sha256sum -c SHA256SUMS); then
    echo "::error::发布制品的 SHA-256 与 SHA256SUMS 不一致"
    exit 1
fi
ls -l "$target_dir"

verify_args=(-dir "$target_dir" -version "$version" -commit "$commit")
if [ "$require_provenance" = "true" ]; then
    verify_args+=(-require-provenance)
fi
if ! go run ./cmd/agentflow-release verify-release "${verify_args[@]}"; then
    echo "::error::发布目录核验失败：版本、提交、平台覆盖、摘要或来源信息不满足要求"
    exit 1
fi

echo "发布制品核验通过：版本 $version 提交 $commit（目录 $target_dir）。"
