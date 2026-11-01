#!/usr/bin/env bash
#
# 用途: 真实测试授权判定的执行入口。把 GitHub 事件上下文原样交给
#       `cmd/agentflow-ci real-tests-gate`，判定规则只在 internal/cigate 实现一次，
#       本脚本不重新实现任何身份判断。
#
# 参数: 无。全部输入来自环境变量，避免把不可信文本拼进命令行。
#
# 环境变量（输入）:
#   AGENTFLOW_REAL_TESTS   唯一的用户配置变量：self / collaborator / all；
#                          未设置、为空或取值未知时关闭。大小写敏感。
#   REPOSITORY_OWNER       仓库所有者账号（github.repository_owner）。
#   PR_AUTHOR              PR 作者账号（github.event.pull_request.user.login），
#                          非 PR 事件为空。判定使用 PR 作者，不是触发者。
#   AUTHOR_ASSOCIATION     PR 作者的 author_association，非 PR 事件为空。
#   GITHUB_EVENT_NAME      事件名；其他事件不参与身份判定，一律不授权。
#   GITHUB_ACTOR           触发本次运行的账号（push 事件的身份来源）。
#   GITHUB_REPOSITORY      owner/repo，仅 collaborator + push 查询权限时使用。
#   GITHUB_API_URL         GitHub API 根地址。
#   GITHUB_TOKEN           只读令牌，仅用于查询推送者的仓库权限；查不出权限按拒绝处理。
#   GITHUB_OUTPUT          可选；判定结论写入 real_tests_enabled / mode / reason。
#   GITHUB_STEP_SUMMARY    可选；判定摘要写入该文件。
#
# 输出:
#   GITHUB_OUTPUT: real_tests_enabled=true|false、mode、reason。
#   GITHUB_STEP_SUMMARY: 判定结论表（不含任何凭据）。
#   标准输出：判定结论与原因（不含任何凭据）。
#
# 退出码:
#   0  判定正常完成：授权与拒绝都属于正常结论（拒绝时 real_tests_enabled=false）
#   1  写出输出文件或摘要失败
#   2  参数错误（本脚本不接受参数）
#
# 副作用: 读取仓库变量与事件上下文；collaborator + push 时发起一次只读权限查询。
#         不建立真实设备连接，不打印任何凭据，不修改工作区。
set -euo pipefail

usage() {
    echo "用法: check-real-test-authorization.sh"
    echo "  读取环境变量并执行真实测试授权判定，结论写入 GITHUB_OUTPUT / GITHUB_STEP_SUMMARY。"
}

for argument in "$@"; do
    case "$argument" in
        -h | --help)
            usage
            exit 0
            ;;
        *)
            echo "错误: 本脚本不接受参数，全部输入来自环境变量；收到 $argument" >&2
            usage >&2
            exit 2
            ;;
    esac
done

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"

if [ ! -d "$repo_root/cmd/agentflow-ci" ]; then
    echo "::error::未找到 $repo_root/cmd/agentflow-ci，无法执行授权判定"
    exit 1
fi

cd "$repo_root"

# 每个取值都作为独立 argv 传入，不拼接 shell 字符串：PR 作者名、事件名等外部
# 输入因此无法被解释成命令。缺失的身份字段留空，由 internal/cigate 按拒绝处理。
exec go run ./cmd/agentflow-ci real-tests-gate \
    -mode "${AGENTFLOW_REAL_TESTS:-}" \
    -event "${GITHUB_EVENT_NAME:-}" \
    -actor "${GITHUB_ACTOR:-}" \
    -repository-owner "${REPOSITORY_OWNER:-}" \
    -pr-author "${PR_AUTHOR:-}" \
    -author-association "${AUTHOR_ASSOCIATION:-}" \
    -repository "${GITHUB_REPOSITORY:-}" \
    -api-url "${GITHUB_API_URL:-https://api.github.com}"
