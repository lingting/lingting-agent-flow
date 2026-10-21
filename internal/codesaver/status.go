package codesaver

import (
	"strings"
)

// statusHeaderPrefix 是 `git status --porcelain` 分支行的前缀。
const statusHeaderPrefix = "## "

// ParseStatus 解析 `git status --porcelain=v1 -z --branch` 的结构化输出。
//
// 使用 -z 形式：条目之间以 NUL 分隔，重命名条目后面紧跟原始路径，因此路径中的
// 空格、引号与换行都不会破坏解析。
func ParseStatus(raw string) (WorktreeState, error) {
	state := WorktreeState{}
	entries := strings.Split(raw, "\x00")
	for index := 0; index < len(entries); index++ {
		entry := entries[index]
		if entry == "" {
			continue
		}
		if strings.HasPrefix(entry, statusHeaderPrefix) {
			applyBranchHeader(&state, strings.TrimPrefix(entry, statusHeaderPrefix))
			continue
		}
		if len(entry) < 3 {
			return WorktreeState{}, newError(
				CodeWorktreeInvalid,
				"verify",
				"无法解析的 Git 状态条目",
				nil,
			)
		}
		indexColumn := entry[0]
		workColumn := entry[1]
		classifyStatusEntry(&state, indexColumn, workColumn)
		if indexColumn == 'R' || indexColumn == 'C' ||
			workColumn == 'R' || workColumn == 'C' {
			// 重命名与复制条目后紧跟原始路径，跳过它以保持解析位置正确。
			index++
		}
	}
	state.Clean = state.Modified == 0 &&
		state.Added == 0 &&
		state.Deleted == 0 &&
		state.Renamed == 0 &&
		state.Staged == 0 &&
		state.Conflicts == 0
	return state, nil
}

// applyBranchHeader 解析分支头，填充分支名与分离头指针标记。
func applyBranchHeader(state *WorktreeState, header string) {
	trimmed := strings.TrimSpace(header)
	if strings.HasPrefix(trimmed, "HEAD (no branch)") {
		state.Detached = true
		state.Branch = ""
		return
	}
	if trimmed == "No commits yet" {
		return
	}
	// 形如 main...origin/main [ahead 1]；只保留本地分支名。
	name := trimmed
	if index := strings.Index(name, "..."); index >= 0 {
		name = name[:index]
	}
	if index := strings.Index(name, " "); index >= 0 {
		name = name[:index]
	}
	state.Branch = strings.TrimSpace(name)
	if state.Branch == "HEAD" {
		state.Detached = true
		state.Branch = ""
	}
}

// classifyStatusEntry 按 porcelain 状态字符归类一个条目。
func classifyStatusEntry(state *WorktreeState, indexColumn, workColumn byte) {
	if isConflictPair(indexColumn, workColumn) {
		state.Conflicts++
		return
	}
	if indexColumn == '?' && workColumn == '?' {
		state.Added++
		return
	}
	if indexColumn == '!' && workColumn == '!' {
		// 被忽略的文件不计入变更。
		return
	}
	if indexColumn != ' ' {
		state.Staged++
	}
	for _, column := range []byte{indexColumn, workColumn} {
		switch column {
		case 'M', 'T':
			state.Modified++
		case 'A':
			state.Added++
		case 'D':
			state.Deleted++
		case 'R', 'C':
			state.Renamed++
		}
	}
}

// isConflictPair 判断状态字符对是否表示未解决的冲突。
func isConflictPair(indexColumn, workColumn byte) bool {
	conflicts := map[string]struct{}{
		"DD": {}, "AU": {}, "UD": {}, "UA": {},
		"DU": {}, "AA": {}, "UU": {},
	}
	_, ok := conflicts[string([]byte{indexColumn, workColumn})]
	return ok
}

// hasConflicts 判断工作区是否存在未解决的冲突。
func hasConflicts(state WorktreeState) bool {
	return state.Conflicts > 0
}
