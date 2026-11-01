#!/usr/bin/env bash
#
# 用途: 构建发布制品。复用仓库唯一发布入口 `make release-dist`（内部调用
#       cmd/agentflow-release），不在脚本里另拼一套编译命令。
#
# 参数:
#   --version V               必填。vMAJOR.MINOR.PATCH。
#   --dir DIR                 必填。制品输出目录（默认约定 dist/release）。
#   --platforms LIST          可选。逗号分隔的目标平台，留空表示使用 internal/release
#                             的发布矩阵。
#   --attestation-provider P  必填。写入清单的来源证明提供方标识。
#
# 环境变量（输入，来自 GitHub 事件上下文）:
#   SOURCE_REPOSITORY     github.repository
#   SOURCE_RUN_ID         github.run_id
#   SOURCE_WORKFLOW_REF   github.workflow_ref（owner/repo/.github/workflows/x.yml@ref）
#
# 输出:
#   制品目录：压缩包、SHA256SUMS、release-manifest.json。
#   不写 GITHUB_OUTPUT；结论由退出码表达。
#
# 退出码:
#   0  构建、打包与自检全部通过
#   1  make release-dist 失败（含前端产物缺失、版本无效、来源信息缺失）
#   2  参数缺失、非法或缺少构建工具链
#
# 副作用: 执行前端生产构建（写入 internal/webui/dist）、交叉编译并写入制品目录；
#         不创建 Release、不推送镜像、不部署环境。
set -euo pipefail

usage() {
    echo "用法: build-release-dist.sh --version V --dir DIR --attestation-provider P"
    echo "        [--platforms LIST]"
    echo "  调用 make release-dist 生成跨平台制品、SHA256SUMS 与 release-manifest.json。"
}

version=""
release_dir=""
platforms=""
attestation_provider=""

while [ $# -gt 0 ]; do
    case "$1" in
        --version)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --version 缺少取值" >&2
                exit 2
            fi
            version="$2"
            shift 2
            ;;
        --dir)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --dir 缺少取值" >&2
                exit 2
            fi
            release_dir="$2"
            shift 2
            ;;
        --platforms)
            if [ $# -lt 2 ]; then
                echo "错误: --platforms 缺少取值" >&2
                exit 2
            fi
            platforms="$2"
            shift 2
            ;;
        --attestation-provider)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --attestation-provider 缺少取值" >&2
                exit 2
            fi
            attestation_provider="$2"
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

if [ -z "$version" ] || [ -z "$release_dir" ] || [ -z "$attestation_provider" ]; then
    echo "错误: --version、--dir 与 --attestation-provider 都是必需参数" >&2
    usage >&2
    exit 2
fi

source_repository="${SOURCE_REPOSITORY:-}"
source_run_id="${SOURCE_RUN_ID:-}"
source_workflow_ref="${SOURCE_WORKFLOW_REF:-}"
if [ -z "$source_repository" ] || [ -z "$source_run_id" ] || [ -z "$source_workflow_ref" ]; then
    echo "错误: 正式版本的构建必须提供 SOURCE_REPOSITORY / SOURCE_RUN_ID / SOURCE_WORKFLOW_REF" >&2
    echo "      缺少来源信息时会让制品无法溯源，因此直接失败而不是产出无来源元数据的制品。" >&2
    exit 2
fi

# 从 owner/repo/.github/workflows/x.yml@ref 里取出工作流文件路径。
workflow_file="${source_workflow_ref#*/}"
workflow_file="${workflow_file#*/}"
workflow_file="${workflow_file%%@*}"
if ! [[ "$workflow_file" =~ ^\.github/workflows/[A-Za-z0-9_.-]+\.ya?ml$ ]]; then
    echo "错误: 无法从 SOURCE_WORKFLOW_REF=$source_workflow_ref 解析工作流路径" >&2
    exit 2
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"

for tool in go node pnpm; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "错误: 构建发布制品需要 $tool，但当前环境找不到它" >&2
        exit 2
    fi
done

cd "$repo_root"

echo "版本: $version"
echo "制品目录: $release_dir"
echo "来源: $source_repository $workflow_file run=$source_run_id"

# make release-dist 的顺序是：前端生产构建 → 校验入口 HTML/JS/CSS → 逐平台交叉编译
# → 打包 → 写 SHA256SUMS 与 release-manifest.json → 回读自检。顺序由 Makefile 保证，
# 不允许先编译 Go 再构建前端。这里同时把实际用于构建的工具链版本写进制品元数据。
make release-dist \
    VERSION="$version" \
    RELEASE_DIR="$release_dir" \
    RELEASE_PLATFORMS="$platforms" \
    NODE_VERSION="$(node --version)" \
    PNPM_VERSION="$(pnpm --version)" \
    SOURCE_REPOSITORY="$source_repository" \
    SOURCE_WORKFLOW="$workflow_file" \
    SOURCE_RUN_ID="$source_run_id" \
    ATTESTATION_PROVIDER="$attestation_provider"

echo "发布制品构建与自检完成：$release_dir"
