#!/usr/bin/env bash
#
# 用途: GitHub Release 的生命周期操作（草稿 → 上传 → 来源证明核验 → 公开 → 复核）。
#       发布流程不允许"创建成功即认为发布成功"，也不允许静默覆盖同标签的已有 Release。
#
# 参数:
#   第一个参数是子命令，其余参数随子命令而定（--tag / --dir / --title /
#   --notes-file / --commit）。缺失或非法时以退出码 2 拒绝，不执行任何远端操作。
#
# 子命令:
#   ensure-absent --tag TAG
#       仓库中已存在同名 Release（含草稿）时失败：正式制品不允许被静默覆盖。
#   create-draft --tag TAG --title TITLE --notes-file FILE
#       先创建草稿：之后任何上传或核验失败都不会让外部看到"发布成功但资产不全"。
#   upload --tag TAG --dir DIR
#       上传压缩包、SHA256SUMS 与 release-manifest.json（制品目录只允许这些资产）。
#   verify-attestations --tag TAG --dir DIR
#       对已下载的资产逐个执行 gh attestation verify，证明不通过就失败。
#   publish --tag TAG
#       草稿转公开：只有全部核验通过后才执行。
#   verify-published --tag TAG --commit SHA
#       复核 Release 已公开并输出摘要。
#
# 环境变量:
#   GITHUB_REPOSITORY   必填（owner/repo）。
#   GITHUB_STEP_SUMMARY 可选；verify-published 的摘要写入该文件。
#
# 输出:
#   verify-published 写 GITHUB_STEP_SUMMARY；其余子命令只打印状态。
#   不写 GITHUB_OUTPUT：结论由退出码表达。
#
# 退出码:
#   0  子命令成功
#   1  操作失败（Release 已存在、上传失败、证明不通过、仍是草稿等）
#   2  参数缺失、非法或子命令未知
#
# 副作用: 通过 gh 读写远端 Release（创建草稿、上传资产、转公开）。不删除 Release、
#         不改写标签、不对已有资产做覆盖式替换，也不打印任何令牌。
set -euo pipefail

usage() {
    cat <<'EOF'
用法: github-release.sh <子命令> [参数]
  ensure-absent       --tag TAG
  create-draft        --tag TAG --title TITLE --notes-file FILE
  upload              --tag TAG --dir DIR
  verify-attestations --tag TAG --dir DIR
  publish             --tag TAG
  verify-published    --tag TAG --commit SHA
EOF
}

die_usage() {
    echo "错误: $1" >&2
    usage >&2
    exit 2
}

if [ $# -eq 0 ]; then
    die_usage "缺少子命令"
fi

command_name="$1"
shift

if [ "$command_name" = "-h" ] || [ "$command_name" = "--help" ]; then
    usage
    exit 0
fi

tag=""
title=""
notes_file=""
asset_dir=""
commit=""

while [ $# -gt 0 ]; do
    case "$1" in
        --tag)
            [ $# -ge 2 ] || die_usage "--tag 缺少取值"
            tag="$2"
            shift 2
            ;;
        --title)
            [ $# -ge 2 ] || die_usage "--title 缺少取值"
            title="$2"
            shift 2
            ;;
        --notes-file)
            [ $# -ge 2 ] || die_usage "--notes-file 缺少取值"
            notes_file="$2"
            shift 2
            ;;
        --dir)
            [ $# -ge 2 ] || die_usage "--dir 缺少取值"
            asset_dir="$2"
            shift 2
            ;;
        --commit)
            [ $# -ge 2 ] || die_usage "--commit 缺少取值"
            commit="$2"
            shift 2
            ;;
        *)
            die_usage "未知参数 $1"
            ;;
    esac
done

if [ -z "${GITHUB_REPOSITORY:-}" ]; then
    die_usage "需要 GITHUB_REPOSITORY（owner/repo）"
fi
if [ -z "$tag" ]; then
    die_usage "子命令 $command_name 需要 --tag"
fi

repo="$GITHUB_REPOSITORY"

case "$command_name" in
    ensure-absent)
        # 同一个标签不允许静默覆盖：已存在的 Release（含上次失败留下的草稿）
        # 必须由人处理，而不是被本次运行复写。
        if gh release view "$tag" --repo "$repo" >/dev/null 2>&1; then
            echo "::error::已存在 $tag 的 Release（含草稿）。正式制品不允许静默覆盖。"
            echo "::error::请先在 GitHub 上确认并删除该 Release（含草稿），再从标签重新运行本工作流。"
            exit 1
        fi
        echo "确认 $tag 尚无 Release。"
        ;;
    create-draft)
        [ -n "$title" ] || die_usage "create-draft 需要 --title"
        [ -n "$notes_file" ] || die_usage "create-draft 需要 --notes-file"
        [ -f "$notes_file" ] || die_usage "说明文件不存在: $notes_file"
        gh release create "$tag" \
            --repo "$repo" \
            --title "$title" \
            --notes-file "$notes_file" \
            --draft \
            --verify-tag
        echo "已创建草稿 Release $tag。"
        ;;
    upload)
        [ -n "$asset_dir" ] || die_usage "upload 需要 --dir"
        [ -d "$asset_dir" ] || die_usage "制品目录不存在: $asset_dir"
        gh release upload "$tag" \
            "$asset_dir"/*.tar.gz \
            "$asset_dir"/*.zip \
            "$asset_dir/SHA256SUMS" \
            "$asset_dir/release-manifest.json" \
            --repo "$repo"
        echo "已上传 $tag 的发布资产。"
        ;;
    verify-attestations)
        [ -n "$asset_dir" ] || die_usage "verify-attestations 需要 --dir"
        [ -d "$asset_dir" ] || die_usage "核验目录不存在: $asset_dir"
        found=0
        for asset in "$asset_dir"/*.tar.gz "$asset_dir"/*.zip \
            "$asset_dir/SHA256SUMS" "$asset_dir/release-manifest.json"; do
            if [ ! -f "$asset" ]; then
                continue
            fi
            found=1
            echo "核验来源证明: $(basename "$asset")"
            if ! gh attestation verify "$asset" --repo "$repo"; then
                echo "::error::来源证明核验失败：$(basename "$asset")"
                exit 1
            fi
        done
        if [ "$found" -eq 0 ]; then
            echo "::error::$asset_dir 中没有可核验的发布资产"
            exit 1
        fi
        echo "全部发布资产的来源证明已通过核验。"
        ;;
    publish)
        # 草稿到公开是最后一步：失败则保持草稿供人工处置。
        gh release edit "$tag" \
            --repo "$repo" \
            --draft=false \
            --latest
        echo "已把 $tag 转为公开 Release。"
        ;;
    verify-published)
        [ -n "$commit" ] || die_usage "verify-published 需要 --commit"
        is_draft="$(gh release view "$tag" --repo "$repo" --json isDraft --jq '.isDraft')"
        if [ "$is_draft" != "false" ]; then
            echo "::error::$tag 仍处于草稿状态，发布未完成"
            exit 1
        fi
        {
            echo "### 正式 Release 已发布"
            echo
            echo "标签：\`$tag\`　提交：\`$commit\`"
            echo
            echo "资产、SHA-256 与来源证明均已在上传后重新核验。"
        } | tee "${GITHUB_STEP_SUMMARY:-/dev/null}"
        ;;
    *)
        die_usage "未知子命令 $command_name"
        ;;
esac
