package release

import (
	"fmt"
	"regexp"
)

// DevVersion 表示没有注入正式版本号的本地开发构建。
//
// 它是唯一的"非正式版本"取值：缺省构建变量、`make build` 与本地
// `make release-dist` 都输出它，而不是一个看起来像正式版本的号码。
const DevVersion = "dev"

// releaseVersionPattern 是正式版本号格式：vMAJOR.MINOR.PATCH。
//
// 与仓库的版本标签约定一致；不接受前缀、后缀、元数据与 CI 运行编号，
// 保证制品文件名中的版本可以直接对应到 Git 标签。
var releaseVersionPattern = regexp.MustCompile(`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$`)

// ValidateVersion 校验版本号。
//
// 只允许 dev（本地开发构建）与 vMAJOR.MINOR.PATCH（正式发布标签）。用 CI 运行编号、
// 分支名或空值冒充正式版本会让制品无法与源码对应，因此一律拒绝。
func ValidateVersion(version string) error {
	if version == DevVersion {
		return nil
	}
	if releaseVersionPattern.MatchString(version) {
		return nil
	}
	return fmt.Errorf("版本号 %q 无效：必须为 %q 或 vMAJOR.MINOR.PATCH", version, DevVersion)
}

// IsReleaseVersion 判断版本号是否为正式发布版本。
func IsReleaseVersion(version string) bool {
	return version != DevVersion && releaseVersionPattern.MatchString(version)
}
