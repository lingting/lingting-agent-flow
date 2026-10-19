// Package dockerfile 解析 Dockerfile 中的外部基础镜像引用。
//
// 该包只做静态解析：它不访问网络、不调用 Docker，也不执行构建。解析结果用于
// 预检阶段确定"哪些外部镜像需要查询元数据"，因此必须正确处理多阶段构建、
// 阶段别名、全局 ARG 与 --platform 等写法，不能用简单的字符串匹配代替。
package dockerfile

import (
	"fmt"
	"regexp"
	"strings"
)

// BaseImage 是一个需要查询元数据的外部基础镜像引用。
type BaseImage struct {
	// Reference 是 Dockerfile 中写出的原始镜像引用（已完成变量替换）。
	Reference string
	// Resolved 是补齐默认 tag 后的镜像引用，用于元数据查询。
	Resolved string
	// Stage 是使用该镜像的阶段名（`AS <name>`），匿名阶段为空。
	Stage string
	// Platform 是 `--platform` 指定的平台，未指定时为空。
	Platform string
	// Line 是该 FROM 指令在 Dockerfile 中的行号，便于定位问题。
	Line int
}

// Result 是一次解析的结果。
type Result struct {
	// Images 是去重后的外部基础镜像列表，按首次出现顺序排列。
	Images []BaseImage
	// Stages 是全部阶段别名，按出现顺序排列。
	Stages []string
	// Skipped 是被跳过的 FROM 及其原因，例如 scratch 或内部阶段别名。
	Skipped []SkippedFrom
	// Unresolved 是无法解析的 FROM 指令及其原因。
	//
	// 存在无法解析的引用时调用方不得把检查判为通过：未解析的动态引用意味着
	// 无法确认需要查询哪些镜像。
	Unresolved []UnresolvedFrom
}

// SkippedFrom 是一个被跳过、不需要网络查询的 FROM 指令。
type SkippedFrom struct {
	// Reference 是原始引用。
	Reference string
	// Reason 是跳过原因，面向用户展示。
	Reason string
	// Line 是行号。
	Line int
}

// UnresolvedFrom 是一个无法解析的 FROM 指令。
type UnresolvedFrom struct {
	// Reference 是原始引用。
	Reference string
	// Reason 是无法解析的原因。
	Reason string
	// Line 是行号。
	Line int
}

// scratchImage 是空镜像，不需要网络查询。
const scratchImage = "scratch"

// Parse 解析 Dockerfile 内容中的外部基础镜像引用。
//
// 解析规则：
//   - 处理行注释与续行（行尾反斜杠）；
//   - 收集 FROM 之前的全局 ARG，并在 FROM 中做变量替换；
//   - `FROM <stage>` 形式引用内部阶段别名时不产生网络查询；
//   - `FROM scratch` 不产生网络查询；
//   - 同一镜像只保留首次出现，避免重复查询。
func Parse(content []byte) (Result, error) {
	lines := logicalLines(string(content))
	if len(lines) == 0 {
		return Result{}, fmt.Errorf("dockerfile: 内容为空，无法解析基础镜像")
	}
	result := Result{}
	globalArgs := make(map[string]string)
	stageAliases := make(map[string]struct{})
	seenImage := make(map[string]struct{})
	sawFrom := false

	for _, line := range lines {
		fields := splitInstruction(line.Text)
		if len(fields) == 0 {
			continue
		}
		instruction := strings.ToUpper(fields[0])
		if instruction == "ARG" && !sawFrom {
			collectArg(fields[1:], globalArgs)
			continue
		}
		if instruction != "FROM" {
			continue
		}
		sawFrom = true
		parsed, ok := parseFrom(fields[1:])
		if !ok {
			result.Unresolved = append(result.Unresolved, UnresolvedFrom{
				Reference: strings.Join(fields[1:], " "),
				Reason:    "FROM 指令缺少镜像引用",
				Line:      line.Number,
			})
			continue
		}
		resolved, unresolvedReason := expandVariables(parsed.image, globalArgs)
		if unresolvedReason != "" {
			result.Unresolved = append(result.Unresolved, UnresolvedFrom{
				Reference: parsed.image,
				Reason:    unresolvedReason,
				Line:      line.Number,
			})
			if parsed.alias != "" {
				stageAliases[strings.ToLower(parsed.alias)] = struct{}{}
				result.Stages = append(result.Stages, parsed.alias)
			}
			continue
		}
		if parsed.alias != "" {
			stageAliases[strings.ToLower(parsed.alias)] = struct{}{}
			result.Stages = append(result.Stages, parsed.alias)
		}
		if _, ok := stageAliases[strings.ToLower(resolved)]; ok {
			result.Skipped = append(result.Skipped, SkippedFrom{
				Reference: resolved,
				Reason:    "内部构建阶段别名，不是外部镜像",
				Line:      line.Number,
			})
			continue
		}
		if strings.EqualFold(resolved, scratchImage) {
			result.Skipped = append(result.Skipped, SkippedFrom{
				Reference: resolved,
				Reason:    "scratch 是空镜像，不需要查询",
				Line:      line.Number,
			})
			continue
		}
		if _, ok := seenImage[resolved]; ok {
			continue
		}
		seenImage[resolved] = struct{}{}
		result.Images = append(result.Images, BaseImage{
			Reference: resolved,
			Resolved:  ResolveReference(resolved),
			Stage:     parsed.alias,
			Platform:  parsed.platform,
			Line:      line.Number,
		})
	}
	if !sawFrom {
		return Result{}, fmt.Errorf("dockerfile: 未找到 FROM 指令，无法确定基础镜像")
	}
	return result, nil
}

// logicalLine 是一个逻辑行（已合并续行）。
type logicalLine struct {
	// Number 是逻辑行起始的物理行号，从 1 开始。
	Number int
	// Text 是合并续行后的内容，不含注释与行尾反斜杠。
	Text string
}

// logicalLines 把 Dockerfile 拆分为逻辑行。
//
// 合并规则与 Docker 一致：行尾反斜杠表示续行；整行注释与空行被丢弃。
func logicalLines(content string) []logicalLine {
	physical := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	lines := make([]logicalLine, 0, len(physical))
	var (
		builder   strings.Builder
		startAt   int
		continued bool
	)
	for index, raw := range physical {
		number := index + 1
		trimmed := strings.TrimSpace(raw)
		if !continued {
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			startAt = number
			builder.Reset()
		}
		body := trimmed
		if continued {
			if body == "" || strings.HasPrefix(body, "#") {
				continue
			}
		}
		if strings.HasSuffix(body, "\\") {
			builder.WriteString(strings.TrimSuffix(body, "\\"))
			builder.WriteString(" ")
			continued = true
			continue
		}
		builder.WriteString(body)
		continued = false
		lines = append(lines, logicalLine{Number: startAt, Text: builder.String()})
	}
	if continued && builder.Len() > 0 {
		lines = append(lines, logicalLine{Number: startAt, Text: builder.String()})
	}
	return lines
}

// splitInstruction 把逻辑行拆为指令与参数。
func splitInstruction(text string) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	return strings.Fields(trimmed)
}

// collectArg 收集全局 ARG 的默认值。
//
// 没有默认值的 ARG 不写入映射：它的值来自构建期参数，静态解析无法确定，
// 使用时必须报告为无法解析，而不是猜测一个值。
func collectArg(fields []string, target map[string]string) {
	for _, field := range fields {
		name, value, ok := strings.Cut(field, "=")
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !ok {
			continue
		}
		target[name] = unquote(value)
	}
}

// fromInstruction 是一条 FROM 指令的解析结果。
type fromInstruction struct {
	// image 是镜像引用，可能仍包含变量。
	image string
	// alias 是阶段别名。
	alias string
	// platform 是 --platform 的值。
	platform string
}

// parseFrom 解析 FROM 指令的参数。
//
// 支持 `FROM [--platform=...] <image> [AS <name>]` 形式。
func parseFrom(fields []string) (fromInstruction, bool) {
	parsed := fromInstruction{}
	index := 0
	for index < len(fields) {
		field := fields[index]
		if !strings.HasPrefix(field, "--") {
			break
		}
		name, value, ok := strings.Cut(strings.TrimPrefix(field, "--"), "=")
		if !ok {
			index++
			continue
		}
		if strings.EqualFold(name, "platform") {
			parsed.platform = value
		}
		index++
	}
	if index >= len(fields) {
		return fromInstruction{}, false
	}
	parsed.image = fields[index]
	index++
	if index < len(fields) && strings.EqualFold(fields[index], "AS") {
		if index+1 < len(fields) {
			parsed.alias = fields[index+1]
		}
	}
	if parsed.image == "" {
		return fromInstruction{}, false
	}
	return parsed, true
}

// unquote 去掉 ARG 值两侧的引号。
func unquote(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) >= 2 {
		if (trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"') ||
			(trimmed[0] == '\'' && trimmed[len(trimmed)-1] == '\'') {
			return trimmed[1 : len(trimmed)-1]
		}
	}
	return trimmed
}

// variablePattern 匹配 ${VAR}、${VAR:-default}、$VAR 三种形式。
var variablePattern = regexp.MustCompile(
	`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::([-+?])([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`,
)

// expandVariables 用已知的全局 ARG 替换引用中的变量。
//
// 返回替换结果与无法解析的原因：存在无法确定的变量时结果为空，调用方必须把该
// FROM 记录为无法解析，不能跳过并判为成功。
func expandVariables(reference string, args map[string]string) (string, string) {
	var unresolved string
	expanded := variablePattern.ReplaceAllStringFunc(reference, func(match string) string {
		groups := variablePattern.FindStringSubmatch(match)
		name := groups[1]
		if name == "" {
			name = groups[4]
		}
		modifier := groups[2]
		fallback := groups[3]
		value, known := args[name]
		switch {
		case known && value != "":
			return value
		case modifier == "-" || modifier == ":-":
			if fallback != "" {
				return fallback
			}
			if known {
				return value
			}
			unresolved = "变量 " + name + " 没有可用的默认值"
			return ""
		case modifier == "+" || modifier == ":+":
			return fallback
		default:
			unresolved = "变量 " + name + " 依赖构建期参数，静态解析无法确定"
			return ""
		}
	})
	if unresolved != "" {
		return "", unresolved
	}
	if strings.Contains(expanded, "$") {
		return "", "镜像引用中仍包含未解析的变量: " + expanded
	}
	return expanded, ""
}

// ResolveReference 补齐镜像引用中省略的默认 tag。
//
// Docker 在省略 tag 时使用 latest，因此 `node` 与 `node:latest` 指向同一镜像。
// 引用中已含 tag 或 digest 时原样返回。
func ResolveReference(reference string) string {
	trimmed := strings.TrimSpace(reference)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "@") {
		return trimmed
	}
	// tag 只出现在最后一个路径段中：registry:5000/ns/repo 的冒号属于端口。
	lastSlash := strings.LastIndex(trimmed, "/")
	lastSegment := trimmed
	if lastSlash >= 0 {
		lastSegment = trimmed[lastSlash+1:]
	}
	if strings.Contains(lastSegment, ":") {
		return trimmed
	}
	return trimmed + ":latest"
}
