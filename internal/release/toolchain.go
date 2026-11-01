package release

import (
	"fmt"
	"regexp"
	"strings"
)

// toolVersionPattern 是前端工具链版本号格式：MAJOR.MINOR.PATCH。
//
// Node.js 与 pnpm 的版本都按这个格式记录。不接受 "latest"、分支名、CI 运行编号或
// 带前缀的写法：制品必须能对应到实际用于构建前端产物的那套工具链版本。
var toolVersionPattern = regexp.MustCompile(`^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$`)

// NormalizeToolVersion 去掉版本号前后空白与 v 前缀。
//
// `node --version` 输出 `v22.22.3`，`pnpm --version` 输出 `11.24.0`；统一成不带前缀的
// 形式才能与清单里的版本字段直接比较。
func NormalizeToolVersion(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

// ValidToolVersion 判断取值是否为 MAJOR.MINOR.PATCH 形式的前端工具链版本。
func ValidToolVersion(value string) bool {
	return toolVersionPattern.MatchString(value)
}

// requireToolVersion 校验前端工具链版本取值。
//
// 正式版本必须记录真实版本；dev 构建允许留空（本地核对打包流程时可能没有安装前端
// 工具链），但一旦填写就必须合法。
func requireToolVersion(name string, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s 版本缺失：正式版本必须记录实际使用的 %s 版本", name, name)
		}
		return nil
	}
	if !ValidToolVersion(value) {
		return fmt.Errorf("%s 版本 %q 无效：必须为 MAJOR.MINOR.PATCH", name, value)
	}
	return nil
}
