#!/usr/bin/env bash
#
# 用途: Go 测试分组清单与核验的唯一来源。集中定义每个分组属于普通 CI 还是真实外部依赖
#       测试、对应的 Makefile 入口与包模式，并用 `go list` 得到的真实测试包清单核验分组
#       覆盖：新增测试包未分类、一个测试包落在多个分组、分组已定义但普通 CI 没有执行、
#       正常分组的 Makefile 入口与清单不一致、执行脚本硬编码包路径，都会让核验失败。
#
# 参数:
#   --check [--declared-groups LIST]  核验分组完整性。LIST 是普通 CI 实际并行执行的分组
#                                    （逗号分隔，例如 unit,local,e2e）。
#   --list normal|real                打印指定类型的分组名，每行一个。
#   --packages GROUP                  打印该分组的 go test 包模式，每行一个。
#   --make-target GROUP               打印该分组的 Makefile 入口目标。
#   --describe                        打印分组清单（Markdown 表格）。
#
# 环境变量（输入）:
#   GITHUB_OUTPUT        可选。--check 通过时写出 coverage / package_total /
#                        groups_markdown（Markdown 表格，供 CI 汇总引用）。
#   GITHUB_STEP_SUMMARY  可选。--check 与 --describe 追加摘要到该文件。
#
# 输出:
#   标准输出：分组清单、包模式或核验结论；核验失败时列出未分组与重复归属的测试包。
#   GITHUB_OUTPUT / GITHUB_STEP_SUMMARY：仅在设置时写入，内容不含任何凭据。
#
# 退出码:
#   0  查询成功或分组核验通过
#   1  分组核验失败（遗漏、重复、与声明的普通分组或 Makefile 入口不一致）
#   2  参数缺失或非法
#
# 副作用: 只读仓库文件并调用 `go list`；不修改仓库内容，不访问网络之外的测试设备。
set -euo pipefail

usage() {
    echo "用法: go-test-groups.sh --check [--declared-groups unit,local,e2e]"
    echo "      go-test-groups.sh --list normal|real"
    echo "      go-test-groups.sh --packages GROUP"
    echo "      go-test-groups.sh --make-target GROUP"
    echo "      go-test-groups.sh --describe"
}

# normal_groups 是普通 CI 并行执行的分组（不需要真实外部设备）。
normal_groups() {
    printf '%s\n' unit local e2e
}

# real_groups 是只在真实测试授权通过后执行的分组。
real_groups() {
    printf '%s\n' ssh remote
}

# all_groups 返回全部分组，用于覆盖核验。
all_groups() {
    normal_groups
    real_groups
}

# group_kind 返回分组类型（normal / real），未知分组返回非零。
group_kind() {
    case "$1" in
        unit | local | e2e) printf 'normal\n' ;;
        ssh | remote) printf 'real\n' ;;
        *) return 1 ;;
    esac
}

# group_patterns 返回分组的 go test 包模式，每行一个。
group_patterns() {
    case "$1" in
        unit) printf './tests/unit/...\n' ;;
        local) printf './tests/integration/local/...\n' ;;
        e2e) printf './tests/e2e/...\n' ;;
        ssh) printf './tests/integration/ssh/...\n' ;;
        remote) printf './tests/integration/remote/...\n' ;;
        *) return 1 ;;
    esac
}

# group_make_target 返回普通分组的 Makefile 入口；真实分组没有本机入口。
group_make_target() {
    case "$1" in
        unit) printf 'test-unit\n' ;;
        local) printf 'test-local\n' ;;
        e2e) printf 'test-e2e\n' ;;
        *) return 1 ;;
    esac
}

# pattern_base 把包模式转换成用于归属匹配的包前缀。
# ./tests/unit/... -> tests/unit
pattern_base() {
    local pattern="$1"
    pattern="${pattern#./}"
    pattern="${pattern%/...}"
    printf '%s\n' "$pattern"
}

# matches_group 判断测试包是否属于给定包模式（包本身或它的子包）。
matches_group() {
    local pkg="$1" base="$2"
    if [ "$pkg" = "$base" ]; then
        return 0
    fi
    [[ "$pkg" == "$base/"* ]]
}

# list_test_packages 列出仓库中真实存在测试文件的包（按模块内相对路径输出）。
list_test_packages() {
    local module_path pkg
    module_path="$(go list -m)"
    go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./tests/... |
        while IFS= read -r pkg; do
            [ -n "$pkg" ] || continue
            printf '%s\n' "${pkg#"$module_path"/}"
        done
}

declare -A counts=()
package_total=0
unmatched_packages=()
duplicated_packages=()

# load_test_packages 统计每个分组的测试包数量，并记录未分组与重复归属的包。
load_test_packages() {
    local pkg group pattern
    local -a matches=()
    package_total=0
    unmatched_packages=()
    duplicated_packages=()
    counts=()
    while IFS= read -r pkg; do
        [ -n "$pkg" ] || continue
        package_total=$((package_total + 1))
        matches=()
        for group in $(all_groups); do
            while IFS= read -r pattern; do
                if matches_group "$pkg" "$(pattern_base "$pattern")"; then
                    matches+=("$group")
                fi
            done < <(group_patterns "$group")
        done
        case "${#matches[@]}" in
            0) unmatched_packages+=("$pkg") ;;
            1) counts["${matches[0]}"]=$(( ${counts["${matches[0]}"]:-0} + 1 )) ;;
            *) duplicated_packages+=("$pkg（${matches[*]}）") ;;
        esac
    done < <(list_test_packages)
}

# group_count 返回分组内的测试包数量（未收录时输出 0）。
group_count() {
    printf '%s\n' "${counts["$1"]:-0}"
}

# render_table 输出分组清单表格，供核验结论与 CI 汇总共同使用。
render_table() {
    local group kind target patterns count
    echo "| 分组 | 类型 | 测试包 | Makefile 入口 | 包模式 |"
    echo "| --- | --- | --- | --- | --- |"
    for group in $(all_groups); do
        kind="$(group_kind "$group")"
        if [ "$kind" = "normal" ]; then
            kind="普通 CI"
            target="$(group_make_target "$group")"
        else
            kind="真实外部依赖"
            target="不适用"
        fi
        patterns="$(group_patterns "$group" | tr '\n' ' ' | sed 's/ *$//')"
        count="$(group_count "$group")"
        echo "| ${group} | ${kind} | ${count} | ${target} | \`${patterns}\` |"
    done
}

# emit_summary 把给定文本同时写到标准输出与 Job 摘要。
emit_summary() {
    printf '%s\n' "$1"
    if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
        printf '%s\n' "$1" >> "$GITHUB_STEP_SUMMARY"
    fi
}

# make_target_patterns 从 Makefile 的目标配方里取出包模式，用于发现清单与本地入口不一致。
make_target_patterns() {
    local target="$1"
    awk -v target="$target" '
        index($0, target ":") == 1 { inside = 1; next }
        inside && /^\t/ { print; next }
        inside { exit }
    ' Makefile | grep -o '\./tests/[^[:space:]]*' | sort
}

# check_consumer 校验执行脚本没有硬编码包路径，而是从本脚本解析清单。
check_consumer() {
    local script="$1" failures=0
    if ! grep -q 'go-test-groups\.sh' "$script"; then
        echo "错误: ${script} 必须从 go-test-groups.sh 解析包清单，不得自带分组定义" >&2
        failures=1
    fi
    if grep -q '\./tests/' "$script"; then
        echo "错误: ${script} 不得硬编码 ./tests 包路径，分组归属只能在 go-test-groups.sh 中定义" >&2
        failures=1
    fi
    return "$failures"
}

run_check() {
    local declared="$1"
    local group target expected declared_patterns kind failed=0
    local -a declared_groups=()

    load_test_packages

    if [ "${#unmatched_packages[@]}" -gt 0 ]; then
        echo "错误: 以下测试包没有分组归属（新增测试包必须补充分组清单）:" >&2
        printf '  %s\n' "${unmatched_packages[@]}" >&2
        failed=1
    fi
    if [ "${#duplicated_packages[@]}" -gt 0 ]; then
        echo "错误: 以下测试包出现在多个分组（必须只属于一个分组）:" >&2
        printf '  %s\n' "${duplicated_packages[@]}" >&2
        failed=1
    fi
    if [ "$package_total" -eq 0 ]; then
        echo "错误: 没有解析到任何测试包，分组核验无法成立" >&2
        failed=1
    fi

    # 工作流声明的普通分组必须与清单完全一致：分组已定义却没有 Job 执行，同样算遗漏。
    if [ -z "$declared" ]; then
        echo "错误: --declared-groups 不能为空" >&2
        exit 2
    fi
    IFS=',' read -r -a declared_groups <<< "$declared"
    for group in "${declared_groups[@]}"; do
        if ! group_kind "$group" >/dev/null; then
            echo "错误: 声明的分组 ${group} 不在分组清单中" >&2
            failed=1
            continue
        fi
        kind="$(group_kind "$group")"
        if [ "$kind" != "normal" ]; then
            echo "错误: 分组 ${group} 属于真实外部依赖测试，不得在普通 CI 中执行" >&2
            failed=1
        fi
    done
    local declared_sorted expected_sorted
    declared_sorted="$(printf '%s\n' "${declared_groups[@]}" | sort)"
    expected_sorted="$(normal_groups | sort)"
    if [ "$declared_sorted" != "$expected_sorted" ]; then
        echo "错误: 工作流执行的普通分组与清单不一致" >&2
        echo "  工作流声明: $(printf '%s' "$declared_sorted" | tr '\n' ' ')" >&2
        echo "  清单定义  : $(printf '%s' "$expected_sorted" | tr '\n' ' ')" >&2
        failed=1
    fi

    # 普通分组的 Makefile 入口必须与本清单的包模式一致，避免本地入口与 CI 分组漂移。
    for group in $(normal_groups); do
        target="$(group_make_target "$group")"
        expected="$(group_patterns "$group" | sort)"
        declared_patterns="$(make_target_patterns "$target" || true)"
        if [ -z "$declared_patterns" ]; then
            echo "错误: Makefile 目标 ${target} 没有声明任何 ./tests 包模式（分组 ${group}）" >&2
            failed=1
            continue
        fi
        if [ "$declared_patterns" != "$expected" ]; then
            echo "错误: Makefile 目标 ${target} 的包模式与分组 ${group} 不一致" >&2
            echo "  分组定义  : $(printf '%s' "$expected" | tr '\n' ' ')" >&2
            echo "  Makefile  : $(printf '%s' "$declared_patterns" | tr '\n' ' ')" >&2
            failed=1
        fi
    done

    # 真实分组与普通分组的执行脚本都从本清单解析，禁止各自硬编码包路径。
    check_consumer "${script_dir}/run-go-group-tests.sh" || failed=1
    check_consumer "${script_dir}/run-real-tests.sh" || failed=1

    if [ "$failed" -ne 0 ]; then
        echo "::error::Go 测试分组核验失败：分组遗漏、重复归属或与工作流 / Makefile 不一致" >&2
        exit 1
    fi

    local table summary
    table="$(render_table)"
    summary="$(
        echo "### Go 测试分组核验"
        echo
        printf '%s\n' "$table"
        echo
        echo "分组核验通过：${package_total} 个测试包全部分类，无遗漏、无重复归属。"
        echo
        echo "- 普通 CI 并行分组：$(normal_groups | tr '\n' ' ')"
        echo "- 真实外部依赖分组（仅授权后执行）：$(real_groups | tr '\n' ' ')"
    )"
    emit_summary "$summary"

    if [ -n "${GITHUB_OUTPUT:-}" ]; then
        {
            echo "coverage=passed"
            echo "package_total=${package_total}"
            echo "groups_markdown<<AGENTFLOW_GROUPS_MARKDOWN"
            printf '%s\n' "$table"
            echo "AGENTFLOW_GROUPS_MARKDOWN"
        } >> "$GITHUB_OUTPUT"
    fi
}

command_name=""
group_name=""
declared_groups=""

while [ $# -gt 0 ]; do
    case "$1" in
        --check)
            command_name="check"
            shift
            ;;
        --describe)
            command_name="describe"
            shift
            ;;
        --list)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --list 缺少取值（normal 或 real）" >&2
                exit 2
            fi
            command_name="list"
            group_name="$2"
            shift 2
            ;;
        --packages)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --packages 缺少分组名" >&2
                exit 2
            fi
            command_name="packages"
            group_name="$2"
            shift 2
            ;;
        --make-target)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --make-target 缺少分组名" >&2
                exit 2
            fi
            command_name="make-target"
            group_name="$2"
            shift 2
            ;;
        --declared-groups)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --declared-groups 缺少取值" >&2
                exit 2
            fi
            declared_groups="$2"
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

if [ -z "$command_name" ]; then
    echo "错误: 必须指定 --check / --describe / --list / --packages / --make-target 之一" >&2
    usage >&2
    exit 2
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
cd "$repo_root"

case "$command_name" in
    check)
        run_check "$declared_groups"
        ;;
    describe)
        load_test_packages
        emit_summary "$(render_table)"
        ;;
    list)
        case "$group_name" in
            normal) normal_groups ;;
            real) real_groups ;;
            *)
                echo "错误: --list 的取值只能是 normal 或 real，实际 ${group_name}" >&2
                exit 2
                ;;
        esac
        ;;
    packages)
        group_patterns "$group_name" || {
            echo "错误: 未知分组 ${group_name}" >&2
            exit 2
        }
        ;;
    make-target)
        group_make_target "$group_name" || {
            echo "错误: 分组 ${group_name} 没有普通 CI 的 Makefile 入口" >&2
            exit 2
        }
        ;;
esac
