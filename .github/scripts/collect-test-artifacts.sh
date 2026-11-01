#!/usr/bin/env bash
#
# 用途: 把一次 Job 的测试产物收敛到统一的暂存目录，供 upload-artifact 直接上传。
#       只收集白名单文件（控制台输出、各测试包的 test.log 与 latest-run.txt），
#       不收集测试工作空间里的数据库、Session、临时私钥、依赖目录或构建缓存，
#       因此 YAML 里不必再写查找与归档逻辑，也不会把无关文件带走。
#
# 参数:
#   --source-dir DIR    必填。该 Job 的测试产物根目录（通常是 $TEST_ARTIFACT_ROOT/<group>）。
#   --job NAME          必填。Job 标识（如 go-unit），用于组成制品名与暂存子目录。
#   --staging-dir DIR   可选。暂存目录，默认 $source-dir/upload-staging。
#
# 环境变量（输入）:
#   GITHUB_RUN_ID       可选。制品名中的运行标识，缺失时使用 local。
#   GITHUB_RUN_ATTEMPT  可选。制品名中的重跑标识，缺失时使用 1。
#   GITHUB_OUTPUT       可选。写出 staging-dir / artifact-name / file-count。
#   GITHUB_STEP_SUMMARY 可选。追加"测试产物"小节（只列文件名，不含内容）。
#
# 输出:
#   暂存目录下按原始相对路径保留白名单文件；标准输出打印制品名与文件清单。
#
# 退出码:
#   0  收集完成（包括没有可收集文件的情形）
#   1  暂存目录不可创建或复制失败
#   2  参数缺失或非法
#
# 副作用: 只读取 --source-dir 下的白名单文件，写入 --staging-dir；不修改测试产物本身。
set -euo pipefail

usage() {
    echo "用法: collect-test-artifacts.sh --source-dir DIR --job NAME [--staging-dir DIR]"
    echo "  把白名单测试产物收敛到暂存目录，供 upload-artifact 上传。"
}

source_dir=""
job_name=""
staging_dir=""

while [ $# -gt 0 ]; do
    case "$1" in
        --source-dir)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --source-dir 缺少取值" >&2
                exit 2
            fi
            source_dir="$2"
            shift 2
            ;;
        --job)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --job 缺少取值" >&2
                exit 2
            fi
            job_name="$2"
            shift 2
            ;;
        --staging-dir)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --staging-dir 缺少取值" >&2
                exit 2
            fi
            staging_dir="$2"
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

if [ -z "$source_dir" ]; then
    echo "错误: 必须提供 --source-dir" >&2
    usage >&2
    exit 2
fi
if [ -z "$job_name" ]; then
    echo "错误: 必须提供 --job" >&2
    usage >&2
    exit 2
fi
if ! [[ "$job_name" =~ ^[A-Za-z0-9._-]+$ ]]; then
    echo "错误: --job 只允许字母、数字、点、下划线与短横线，实际 $job_name" >&2
    exit 2
fi

if [ -z "$staging_dir" ]; then
    staging_dir="$source_dir/upload-staging"
fi

run_id="${GITHUB_RUN_ID:-local}"
run_attempt="${GITHUB_RUN_ATTEMPT:-1}"
artifact_name="${job_name}-${run_id}-${run_attempt}"

if ! mkdir -p "$staging_dir"; then
    echo "错误: 无法创建暂存目录 $staging_dir" >&2
    exit 1
fi

base_dir="$source_dir/lingting-agent-flow-tests"
collected=0

# copy_one 按原始相对路径把白名单文件复制进暂存目录。
copy_one() {
    local file="$1" relative target
    relative="${file#"$source_dir"/}"
    target="$staging_dir/$relative"
    mkdir -p "$(dirname "$target")"
    if ! cp "$file" "$target"; then
        echo "错误: 复制 $file 失败" >&2
        return 1
    fi
    collected=$((collected + 1))
    printf '%s\n' "$relative"
    return 0
}

manifest="$staging_dir/.collected-files.txt"
: > "$manifest"

if [ -d "$source_dir" ]; then
    while IFS= read -r -d '' file; do
        copy_one "$file" >> "$manifest" || exit 1
    done < <(find "$source_dir" -maxdepth 1 -type f -name '*-output.log' -print0)
fi

if [ -d "$base_dir" ]; then
    while IFS= read -r -d '' file; do
        copy_one "$file" >> "$manifest" || exit 1
    done < <(find "$base_dir" -type f \( -name 'test.log' -o -name 'latest-run.txt' \) -print0)
fi

echo "制品名称: $artifact_name"
echo "暂存目录: $staging_dir"
if [ "$collected" -eq 0 ]; then
    echo "::warning::没有找到可收集的测试产物（$source_dir），跳过上传内容"
else
    echo "收集文件数: $collected"
    sort "$manifest" | sed 's/^/  /'
fi

if [ -n "${GITHUB_OUTPUT:-}" ]; then
    {
        echo "artifact-name=${artifact_name}"
        echo "staging-dir=${staging_dir}"
        echo "file-count=${collected}"
    } >> "$GITHUB_OUTPUT"
fi

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    {
        echo "### 测试产物（${job_name}）"
        echo
        echo "- 制品名称：\`${artifact_name}\`"
        echo "- 收集文件数：${collected}"
        echo "- 只收集控制台输出、\`test.log\` 与 \`latest-run.txt\`；测试工作空间（数据库、会话、临时私钥）不上传。"
    } >> "$GITHUB_STEP_SUMMARY"
fi

exit 0
