// Package release 定义 AgentFlow 正式发布制品的版本元数据、目标平台矩阵、
// 制品命名规范、压缩包内容与校验文件格式。
//
// 本包只做纯计算与纯文件读写：不执行 git、go、docker 等外部命令，也不访问工作区
// 之外的路径，因此命名规则、清单格式与校验结果都可以被单元测试直接覆盖。
// 实际驱动工具链构建制品、汇总清单与自校验的流程位于 cmd/agentflow-release。
package release

import (
	"fmt"
	"strings"
)

// 压缩包格式取值。
const (
	// FormatTarGz 是 Linux 与 macOS 制品的压缩格式。
	FormatTarGz = "tar.gz"
	// FormatZip 是 Windows 制品的压缩格式。
	FormatZip = "zip"
)

// Platform 是发布制品的目标操作系统与架构。
type Platform struct {
	// OS 是目标操作系统，取值与 GOOS 一致。
	OS string
	// Arch 是目标架构，取值与 GOARCH 一致。
	Arch string
}

// String 返回 "os/arch" 形式的目标平台标识。
func (p Platform) String() string {
	return p.OS + "/" + p.Arch
}

// SupportedPlatforms 是正式发布矩阵，只有经过核实的目标平台才会出现在这里。
//
// 核实依据：
//   - 持久化使用 modernc.org/sqlite（纯 Go 实现），不需要 CGO、系统 SQLite 库或
//     平台专用构建标签；cobra、sftp、x/crypto、x/term、yaml.v3 同样为纯 Go 依赖。
//   - 代码中没有 //go:build 平台分文件、没有 cgo 调用；进程信号（SIGTERM）、终端
//     交互与文件路径处理都只使用标准库与 x/term 的跨平台实现。
//   - 前端产物通过 //go:embed 编译进二进制，与目标平台无关。
//   - 五个目标都已用本仓库的构建工具实际交叉编译成功（CGO_ENABLED=0）。
//
// 该矩阵只表达构建矩阵，"是否完成真实启动冒烟"按平台分别记录在 docs/release.md，
// 不得用交叉编译成功代替运行验收。
var SupportedPlatforms = []Platform{
	{OS: "linux", Arch: "amd64"},
	{OS: "linux", Arch: "arm64"},
	{OS: "darwin", Arch: "amd64"},
	{OS: "darwin", Arch: "arm64"},
	{OS: "windows", Arch: "amd64"},
}

// IsSupported 判断目标平台是否在发布矩阵内。
func IsSupported(platform Platform) bool {
	for _, supported := range SupportedPlatforms {
		if supported == platform {
			return true
		}
	}
	return false
}

// ParsePlatform 解析 "os/arch" 形式的目标平台标识。
//
// 只做格式解析，是否属于发布矩阵由 IsSupported 单独判断：解析失败与
// "解析成功但平台未经验证"是两种不同的错误，不能合并。
func ParsePlatform(value string) (Platform, error) {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Platform{}, fmt.Errorf("目标平台 %q 格式无效：必须为 os/arch", value)
	}
	return Platform{OS: parts[0], Arch: parts[1]}, nil
}

// BinaryName 返回目标平台的可执行文件名。
func BinaryName(goos string) string {
	if goos == "windows" {
		return "agentflow.exe"
	}
	return "agentflow"
}

// ArchiveFormat 返回目标平台的压缩包格式。
func ArchiveFormat(goos string) string {
	if goos == "windows" {
		return FormatZip
	}
	return FormatTarGz
}

// ArtifactName 返回制品文件名，形如 agentflow_v1.2.3_linux_amd64.tar.gz。
//
// 命名中固定包含版本、目标 OS 与目标架构，保证同一个版本的每个制品都能从文件名
// 直接对应到平台，不需要额外的映射表。
func ArtifactName(version string, platform Platform) string {
	return fmt.Sprintf(
		"agentflow_%s_%s_%s.%s",
		version,
		platform.OS,
		platform.Arch,
		ArchiveFormat(platform.OS),
	)
}
