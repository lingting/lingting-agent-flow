#!/usr/bin/env bash
#
# 用途: 测试分组声明的唯一来源（V3 R8）。声明两类分组：
#       - Go 测试分组：仍由 `go test` 执行的包集合，R8 之后只有 unit（tests/unit/**）；
#       - Case 分组：由 `agentflow-test` 执行的已注册 Case，按 kind 归类：
#           local / e2e / selfcheck / frontend 属于普通门禁，
#           real 是需要真实设备并单独授权的分组，
#           fault 是按设计注入失败、必定以非零退出码结束的自检分组。
#       --check 用真实清单核验：Go 测试包无遗漏无重复、Makefile 入口与清单一致、
#       已注册 Case 的每个分组恰好被一条声明覆盖、声明不落空、工作流声明的普通 kind 与
#       清单一致、消费脚本不硬编码测试范围。
#
# 参数:
#   --check [--declared-groups LIST] [--declared-case-kinds LIST]
#        核验分组完整性。LIST 以逗号分隔：--declared-groups 是工作流实际执行的 Go 测试
#        分组（unit），--declared-case-kinds 是工作流实际执行的普通 Case kind。
#   --list-groups          打印 Go 测试分组名，每行一个。
#   --packages GROUP       打印该 Go 测试分组的包模式，每行一个。
#   --make-target GROUP    打印该 Go 测试分组的 Makefile 入口。
#   --case-selectors KIND  打印该 kind 的用例选择条件，每行一个（normal 展开为全部普通 kind）。
#   --describe             打印两张分组清单（Markdown）。
#
# 环境变量（输入）:
#   GITHUB_OUTPUT        可选。--check 通过时写出 package_total / case_total /
#                        groups_markdown（Markdown 表格，供 CI 汇总引用）。
#   GITHUB_STEP_SUMMARY  可选。--check 与 --describe 追加摘要到该文件。
#
# 输出:
#   标准输出：分组清单、选择条件或核验结论；核验失败时列出未分组、重复归属、落空声明。
#   GITHUB_OUTPUT / GITHUB_STEP_SUMMARY：仅在设置时写入，内容不含任何凭据。
#
# 退出码:
#   0  查询成功或分组核验通过
#   1  分组核验失败（遗漏、重复、落空声明、与工作流 / Makefile / 消费脚本不一致）
#   2  参数缺失或非法
#
# 副作用: 只读仓库文件、调用 `go list` 与 `go run ./cmd/agentflow-test --list`（只列用例，
#         不执行任何用例）；不修改仓库内容，不访问真实设备。
set -euo pipefail

usage() {
    echo "用法: go-test-groups.sh --check [--declared-groups unit] [--declared-case-kinds local,e2e,selfcheck,frontend]"
    echo "      go-test-groups.sh --list-groups"
    echo "      go-test-groups.sh --packages GROUP"
    echo "      go-test-groups.sh --make-target GROUP"
    echo "      go-test-groups.sh --case-selectors KIND"
    echo "      go-test-groups.sh --describe"
}

# go_test_groups 是仍由 go test 执行的分组；R8 之后只有 unit。
go_test_groups() {
    printf 'unit\n'
}

# group_packages 返回 Go 测试分组的包模式，每行一个。
group_packages() {
    case "$1" in
        unit) printf './tests/unit/...\n' ;;
        *) return 1 ;;
    esac
}

# group_make_target 返回 Go 测试分组的 Makefile 入口。
group_make_target() {
    case "$1" in
        unit) printf 'test-unit\n' ;;
        *) return 1 ;;
    esac
}

# normal_case_kinds 是普通门禁执行的 Case kind（不需要真实外部设备）。
normal_case_kinds() {
    printf '%s\n' local e2e selfcheck frontend
}

# real_case_kinds 是只在真实测试授权通过后执行的 Case kind。
real_case_kinds() {
    printf 'real\n'
}

# fault_case_kinds 是按设计注入失败、必定非零退出的自检 Case kind。
fault_case_kinds() {
    printf 'fault\n'
}

# all_case_kinds 返回全部 Case kind，用于核验。
all_case_kinds() {
    normal_case_kinds
    real_case_kinds
    fault_case_kinds
}

# case_declarations 打印全部 Case 分组声明，每行 "<kind>|<selector>"。
# selector 是执行器的选择条件：精确分组名，或以 "..." 结尾的 ID 前缀。
case_declarations() {
    cat <<'EOF'
local|integration-local-...
e2e|e2e-cli
selfcheck|selfcheck
selfcheck|resourcecheck
selfcheck|concurrencycheck
selfcheck|pilot
frontend|frontend
real|integration-ssh
real|integration-remote-...
fault|selfcheck-fault
fault|resourcecheck-fault
fault|concurrencycheck-fault
fault|pilot-fault
EOF
}

# kind_list 把 KIND 展开为 kind 列表（normal 展开为全部普通 kind）。
kind_list() {
    local kind="$1"
    if [ "$kind" = "normal" ]; then
        normal_case_kinds
        return 0
    fi
    if all_case_kinds | grep -qx "$kind"; then
        printf '%s\n' "$kind"
        return 0
    fi
    return 1
}

# case_selectors_for_kind 打印指定 kind 的用例选择条件，每行一个。
case_selectors_for_kind() {
    local kind="$1"
    local declaration_kind selector wanted wanted_kinds_text
    local -a wanted_kinds=()
    if ! wanted_kinds_text="$(kind_list "$kind")"; then
        return 1
    fi
    while IFS= read -r wanted; do
        [ -n "$wanted" ] && wanted_kinds+=("$wanted")
    done <<< "$wanted_kinds_text"
    while IFS='|' read -r declaration_kind selector; do
        for wanted in "${wanted_kinds[@]}"; do
            if [ "$declaration_kind" = "$wanted" ]; then
                printf '%s\n' "$selector"
            fi
        done
    done < <(case_declarations)
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

# list_case_groups 列出注册表里的全部 Case 分组（只列用例，不执行用例）。
list_case_groups() {
    go run ./cmd/agentflow-test --list |
        awk 'NR > 1 && NF > 0 { print $1 }' |
        cut -d/ -f1 |
        sort -u
}

# group_matches_selector 判断已注册的 Case 分组是否命中一条声明。
group_matches_selector() {
    local group="$1" selector="$2"
    if [[ "$selector" == *... ]]; then
        [[ "$group" == "${selector%...}"* ]]
        return
    fi
    [ "$group" = "$selector" ]
}

declare -A package_counts=()
package_total=0
unmatched_packages=()
duplicated_packages=()

# load_test_packages 统计每个 Go 测试分组的测试包数量，并记录未分组与重复归属的包。
load_test_packages() {
    local pkg group pattern
    local -a matches=()
    package_total=0
    unmatched_packages=()
    duplicated_packages=()
    package_counts=()
    while IFS= read -r pkg; do
        [ -n "$pkg" ] || continue
        package_total=$((package_total + 1))
        matches=()
        for group in $(go_test_groups); do
            while IFS= read -r pattern; do
                if matches_group "$pkg" "$(pattern_base "$pattern")"; then
                    matches+=("$group")
                fi
            done < <(group_packages "$group")
        done
        case "${#matches[@]}" in
            0) unmatched_packages+=("$pkg") ;;
            1) package_counts["${matches[0]}"]=$(( ${package_counts["${matches[0]}"]:-0} + 1 )) ;;
            *) duplicated_packages+=("$pkg（${matches[*]}）") ;;
        esac
    done < <(list_test_packages)
}

declare -A case_counts=()
case_total=0
unmatched_case_groups=()
duplicated_case_groups=()
stale_declarations=()
declare -a registered_case_groups=()

# load_case_groups 统计每个 kind 覆盖的 Case 分组数量，并记录未归类、重复归类与落空声明。
load_case_groups() {
    local group declaration_kind selector hit
    local -a matches=()
    case_total=0
    unmatched_case_groups=()
    duplicated_case_groups=()
    stale_declarations=()
    case_counts=()
    # 注册表只读一次：--list 会编译并运行执行器（只列用例），逐条声明重复调用没有必要。
    mapfile -t registered_case_groups < <(list_case_groups)
    for group in "${registered_case_groups[@]}"; do
        [ -n "$group" ] || continue
        case_total=$((case_total + 1))
        matches=()
        while IFS='|' read -r declaration_kind selector; do
            if group_matches_selector "$group" "$selector"; then
                matches+=("$declaration_kind")
            fi
        done < <(case_declarations)
        case "${#matches[@]}" in
            0) unmatched_case_groups+=("$group") ;;
            1) case_counts["${matches[0]}"]=$(( ${case_counts["${matches[0]}"]:-0} + 1 )) ;;
            *) duplicated_case_groups+=("$group（${matches[*]}）") ;;
        esac
    done

    # 声明必须命中至少一个已注册分组：分组改名或删除后遗留的声明同样算漂移。
    while IFS='|' read -r declaration_kind selector; do
        hit=0
        for group in "${registered_case_groups[@]}"; do
            if group_matches_selector "$group" "$selector"; then
                hit=1
                break
            fi
        done
        if [ "$hit" -eq 0 ]; then
            stale_declarations+=("${declaration_kind}|${selector}")
        fi
    done < <(case_declarations)
}

# render_table 输出分组清单表格，供核验结论与 CI 汇总共同使用。
render_table() {
    local group target patterns count kind case_total_label
    echo "| Go 测试分组 | 测试包 | Makefile 入口 | 包模式 |"
    echo "| --- | --- | --- | --- |"
    for group in $(go_test_groups); do
        patterns="$(group_packages "$group" | tr '\n' ' ' | sed 's/ *$//')"
        count="${package_counts["$group"]:-0}"
        target="$(group_make_target "$group")"
        echo "| ${group} | ${count} | ${target} | \`${patterns}\` |"
    done
    echo
    echo "| Case kind | 门禁归属 | 分组数 | 选择条件 |"
    echo "| --- | --- | --- | --- |"
    for kind in $(all_case_kinds); do
        if [ "$kind" = "real" ]; then
            case_total_label="真实外部依赖（需授权）"
        elif [ "$kind" = "fault" ]; then
            case_total_label="故障注入（必定非零退出）"
        else
            case_total_label="普通门禁"
        fi
        echo "| ${kind} | ${case_total_label} | ${case_counts["$kind"]:-0} | $(case_selectors_for_kind "$kind" | tr '\n' ' ' | sed 's/ *$//') |"
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

# make_target_body 输出 Makefile 目标配方的原文，用于检查入口调用的是脚本而不是分组名。
make_target_body() {
    local target="$1"
    awk -v target="$target" '
        index($0, target ":") == 1 { inside = 1; next }
        inside && /^\t/ { print; next }
        inside { exit }
    ' Makefile
}

# check_consumer 校验执行脚本没有硬编码测试范围，而是从本脚本解析清单。
check_consumer() {
    local script="$1" failures=0
    if ! grep -q 'go-test-groups\.sh' "$script"; then
        echo "错误: ${script} 必须从 go-test-groups.sh 解析分组清单，不得自带分组定义" >&2
        failures=1
    fi
    if grep -q '\./tests/' "$script"; then
        echo "错误: ${script} 不得硬编码 ./tests 包路径，分组归属只能在 go-test-groups.sh 中定义" >&2
        failures=1
    fi
    if grep -qE 'integration-(local|remote|ssh)|e2e-cli|resourcecheck|concurrencycheck|pilot/' "$script"; then
        echo "错误: ${script} 不得硬编码 Case 分组名，选择条件只能在 go-test-groups.sh 中定义" >&2
        failures=1
    fi
    return "$failures"
}

run_check() {
    local declared_groups="$1" declared_kinds="$2"
    local group target expected declared_patterns kind failed=0 body
    local -a declared_group_list=() declared_kind_list=()

    load_test_packages
    load_case_groups

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
    if [ "${#unmatched_case_groups[@]}" -gt 0 ]; then
        echo "错误: 以下已注册 Case 分组没有归类（新增用例分组必须补充分组声明）:" >&2
        printf '  %s\n' "${unmatched_case_groups[@]}" >&2
        failed=1
    fi
    if [ "${#duplicated_case_groups[@]}" -gt 0 ]; then
        echo "错误: 以下 Case 分组命中多条分组声明（必须只属于一个 kind）:" >&2
        printf '  %s\n' "${duplicated_case_groups[@]}" >&2
        failed=1
    fi
    if [ "${#stale_declarations[@]}" -gt 0 ]; then
        echo "错误: 以下分组声明没有匹配任何已注册 Case 分组（分组改名或删除后必须同步声明）:" >&2
        printf '  %s\n' "${stale_declarations[@]}" >&2
        failed=1
    fi
    if [ "$case_total" -eq 0 ]; then
        echo "错误: 注册表没有解析到任何 Case 分组，分组核验无法成立" >&2
        failed=1
    fi

    # 工作流声明的 Go 测试分组必须与清单完全一致。
    if [ -z "$declared_groups" ]; then
        echo "错误: --declared-groups 不能为空" >&2
        exit 2
    fi
    IFS=',' read -r -a declared_group_list <<< "$declared_groups"
    for group in "${declared_group_list[@]}"; do
        if ! group_packages "$group" >/dev/null; then
            echo "错误: 声明的 Go 测试分组 ${group} 不在分组清单中" >&2
            failed=1
        fi
    done
    if [ "$(printf '%s\n' "${declared_group_list[@]}" | sort)" != "$(go_test_groups | sort)" ]; then
        echo "错误: 工作流执行的 Go 测试分组与清单不一致" >&2
        echo "  工作流声明: $(printf '%s' "${declared_group_list[*]}")" >&2
        echo "  清单定义  : $(go_test_groups | tr '\n' ' ')" >&2
        failed=1
    fi

    # 工作流声明的普通 Case kind 必须与清单完全一致：分组已定义却没有 Job 执行同样算遗漏。
    if [ -z "$declared_kinds" ]; then
        echo "错误: --declared-case-kinds 不能为空" >&2
        exit 2
    fi
    IFS=',' read -r -a declared_kind_list <<< "$declared_kinds"
    for kind in "${declared_kind_list[@]}"; do
        case "$kind" in
            real | fault)
                echo "错误: Case kind ${kind} 不属于普通门禁，不得在普通 CI 中执行" >&2
                failed=1
                ;;
            *)
                if ! all_case_kinds | grep -qx "$kind"; then
                    echo "错误: 声明的 Case kind ${kind} 不在分组清单中" >&2
                    failed=1
                fi
                ;;
        esac
    done
    if [ "$(printf '%s\n' "${declared_kind_list[@]}" | sort)" != "$(normal_case_kinds | sort)" ]; then
        echo "错误: 工作流执行的普通 Case kind 与清单不一致" >&2
        echo "  工作流声明: $(printf '%s' "${declared_kind_list[*]}")" >&2
        echo "  清单定义  : $(normal_case_kinds | tr '\n' ' ')" >&2
        failed=1
    fi

    # Go 测试分组的 Makefile 入口必须与本清单的包模式一致，避免本地入口与 CI 分组漂移。
    for group in $(go_test_groups); do
        target="$(group_make_target "$group")"
        expected="$(group_packages "$group" | sort)"
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

    # Case 入口必须经由 run-cases.sh 解析选择条件，不得在 Makefile 里另写分组名。
    for target in test-cases test-real; do
        body="$(make_target_body "$target" || true)"
        if ! grep -q 'run-cases\.sh' <<< "$body"; then
            echo "错误: Makefile 目标 ${target} 必须调用 .github/scripts/run-cases.sh 解析选择条件" >&2
            failed=1
        fi
        if grep -qE 'integration-(local|remote|ssh)|e2e-cli|resourcecheck|concurrencycheck|pilot/' <<< "$body"; then
            echo "错误: Makefile 目标 ${target} 不得硬编码 Case 分组名" >&2
            failed=1
        fi
    done

    # 执行脚本都从本清单解析范围，禁止各自硬编码。
    check_consumer "${script_dir}/run-go-group-tests.sh" || failed=1
    check_consumer "${script_dir}/run-cases.sh" || failed=1

    if [ "$failed" -ne 0 ]; then
        echo "::error::测试分组核验失败：分组遗漏、重复归属、落空声明或与工作流 / Makefile / 脚本不一致" >&2
        exit 1
    fi

    local table summary
    table="$(render_table)"
    summary="$(
        echo "### 测试分组核验"
        echo
        printf '%s\n' "$table"
        echo
        echo "分组核验通过：${package_total} 个 go test 测试包、${case_total} 个 Case 分组全部分类，无遗漏、无重复归属、无落空声明。"
        echo
        echo "- 普通门禁 kind：$(normal_case_kinds | tr '\n' ' ')"
        echo "- 真实外部依赖 kind（仅授权后执行）：$(real_case_kinds | tr '\n' ' ')"
        echo "- 故障注入 kind（按设计非零退出，不进绿色门禁）：$(fault_case_kinds | tr '\n' ' ')"
    )"
    emit_summary "$summary"

    if [ -n "${GITHUB_OUTPUT:-}" ]; then
        {
            echo "package_total=${package_total}"
            echo "case_total=${case_total}"
            echo "groups_markdown<<AGENTFLOW_GROUPS_MARKDOWN"
            printf '%s\n' "$table"
            echo "AGENTFLOW_GROUPS_MARKDOWN"
        } >> "$GITHUB_OUTPUT"
    fi
}

command_name=""
group_name=""
kind_name=""
declared_groups=""
declared_kinds=""

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
        --list-groups)
            command_name="list-groups"
            shift
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
        --case-selectors)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --case-selectors 缺少 kind" >&2
                exit 2
            fi
            command_name="case-selectors"
            kind_name="$2"
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
        --declared-case-kinds)
            if [ $# -lt 2 ] || [ -z "$2" ]; then
                echo "错误: --declared-case-kinds 缺少取值" >&2
                exit 2
            fi
            declared_kinds="$2"
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
    echo "错误: 必须指定 --check / --describe / --list-groups / --packages / --make-target / --case-selectors 之一" >&2
    usage >&2
    exit 2
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
cd "$repo_root"

case "$command_name" in
    check)
        run_check "$declared_groups" "$declared_kinds"
        ;;
    describe)
        load_test_packages
        load_case_groups
        emit_summary "$(render_table)"
        ;;
    list-groups)
        go_test_groups
        ;;
    packages)
        group_packages "$group_name" || {
            echo "错误: 未知 Go 测试分组 ${group_name}" >&2
            exit 2
        }
        ;;
    make-target)
        group_make_target "$group_name" || {
            echo "错误: Go 测试分组 ${group_name} 没有 Makefile 入口" >&2
            exit 2
        }
        ;;
    case-selectors)
        case_selectors_for_kind "$kind_name" || {
            echo "错误: 未知 Case kind ${kind_name}" >&2
            exit 2
        }
        ;;
esac
