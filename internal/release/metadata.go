package release

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// MetadataFileName 是压缩包内版本元数据文件名。
const MetadataFileName = "version.json"

// Metadata 是每个制品附带的版本元数据（压缩包内的 version.json）。
//
// 字段与 CLI 的 version 输出、HTTP 的 /api/v1/system/info 一一对应：
// version、commit、build_date 来自构建注入，go_version 与 os/arch 来自实际
// 编译工具链与目标平台，不允许由人工填写成"期望值"。
type Metadata struct {
	// Version 是版本号：正式版本与 Git 标签一致，本地构建为 dev。
	Version string `json:"version"`
	// Commit 是实际参与构建的提交 SHA。
	Commit string `json:"commit"`
	// BuildDate 是 UTC 构建时间（RFC 3339）。
	BuildDate string `json:"build_date"`
	// GoVersion 是实际使用的 Go 工具链版本。
	GoVersion string `json:"go_version"`
	// OS 是制品目标操作系统。
	OS string `json:"os"`
	// Arch 是制品目标架构。
	Arch string `json:"arch"`
}

// Validate 校验版本、提交、构建时间与 Go 工具链字段。
//
// 目标平台字段不在这里校验：构建开始时还不知道具体平台，平台的合法性由
// Manifest 逐个制品校验。
func (m Metadata) Validate() error {
	if err := ValidateVersion(m.Version); err != nil {
		return err
	}
	if !ValidCommit(m.Commit) {
		return fmt.Errorf("提交号 %q 无效：必须是完整的 Git 提交 SHA", m.Commit)
	}
	if _, err := time.Parse(time.RFC3339, m.BuildDate); err != nil {
		return fmt.Errorf("构建时间 %q 无效：必须是 RFC 3339 时间", m.BuildDate)
	}
	if !strings.HasPrefix(m.GoVersion, "go") || len(m.GoVersion) <= len("go") {
		return fmt.Errorf("Go 工具链版本 %q 无效", m.GoVersion)
	}
	return nil
}

// JSON 返回缩进后的 JSON 内容，末尾带换行。
func (m Metadata) JSON() ([]byte, error) {
	return marshalIndent(m)
}

// marshalIndent 以统一的缩进格式序列化结构，供 version.json 与清单共用。
func marshalIndent(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
