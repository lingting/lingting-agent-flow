package release

import (
	"fmt"
	"sort"
	"strings"
)

// Notes 返回正式 Release 的版本说明（Markdown）。
//
// 说明内容只由清单派生：版本元数据、制品表与两种核验方式。SHA-256 只证明"文件与
// 校验值一致"，来源证明才回答"这份文件是不是由该仓库的该工作流在该提交上构建"，
// 两种能力在说明里必须分开写，不能让用户误以为校验和等于来源认证。
func (m Manifest) Notes() string {
	builder := &strings.Builder{}
	fmt.Fprintf(builder, "# AgentFlow %s\n\n", m.Version)
	builder.WriteString("| 字段 | 取值 |\n| --- | --- |\n")
	fmt.Fprintf(builder, "| 版本 | `%s` |\n", m.Version)
	fmt.Fprintf(builder, "| 提交 | `%s` |\n", m.Commit)
	fmt.Fprintf(builder, "| 构建时间 | %s |\n", m.BuildDate)
	fmt.Fprintf(builder, "| Go | `%s` |\n", m.GoVersion)
	if m.NodeVersion != "" {
		fmt.Fprintf(builder, "| Node.js | `%s` |\n", m.NodeVersion)
	}
	if m.PnpmVersion != "" {
		fmt.Fprintf(builder, "| pnpm | `%s` |\n", m.PnpmVersion)
	}
	if m.SourceWorkflow != "" {
		fmt.Fprintf(builder, "| 构建工作流 | `%s` |\n", m.SourceWorkflow)
	}
	if m.SourceRunID != "" {
		fmt.Fprintf(builder, "| 运行标识 | `%s` |\n", m.SourceRunID)
	}

	builder.WriteString("\n## 制品\n\n")
	builder.WriteString("| 平台 | 制品 | 大小（字节） | SHA-256 |\n| --- | --- | --- | --- |\n")
	artifacts := make([]Artifact, len(m.Artifacts))
	copy(artifacts, m.Artifacts)
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].File < artifacts[j].File })
	for _, artifact := range artifacts {
		fmt.Fprintf(builder, "| %s/%s | `%s` | %d | `%s` |\n",
			artifact.OS, artifact.Arch, artifact.File, artifact.Size, artifact.SHA256)
	}

	builder.WriteString("\n## 校验文件一致性\n\n")
	builder.WriteString("SHA-256 只回答\"下载到的文件有没有损坏或被替换\"，无法说明来源。\n\n")
	builder.WriteString("```bash\n")
	builder.WriteString("# Linux\nsha256sum -c " + ChecksumFileName + "\n\n")
	builder.WriteString("# macOS\nshasum -a 256 -c " + ChecksumFileName + "\n\n")
	builder.WriteString("# Windows（PowerShell）\n")
	builder.WriteString(fmt.Sprintf("Get-FileHash %s -Algorithm SHA256\n", exampleArtifactNames(artifacts).windows))
	builder.WriteString("```\n")

	if m.AttestationProvider != "" && m.SourceRepository != "" {
		builder.WriteString("\n## 来源证明\n\n")
		builder.WriteString("每个制品都附带 GitHub Artifact Attestation（Actions OIDC 无密钥证明）。\n")
		builder.WriteString("它把文件摘要与本次工作流运行、仓库和提交绑定在一起，需要用 GitHub CLI 核验：\n\n")
		builder.WriteString("```bash\n")
		fmt.Fprintf(builder, "gh attestation verify %s --repo %s\n",
			exampleArtifactNames(artifacts).posix, m.SourceRepository)
		builder.WriteString("```\n\n")
		builder.WriteString("核验通过只说明\"该文件由这个仓库的构建工作流在对应提交上产出\"，")
		builder.WriteString("不代表代码经过了安全审计或功能验收。\n")
	}
	return builder.String()
}

// 示例命令使用的制品名占位取值。
const examplePlaceholder = "<制品文件名>"

// exampleArtifactNames 返回核验示例使用的制品名。
//
// POSIX 示例取排序后的第一个制品，PowerShell 示例优先取 Windows 制品：说明里的命令
// 必须是用户能直接照抄的，而不是让他先改名。
func exampleArtifactNames(artifacts []Artifact) struct{ posix, windows string } {
	names := struct{ posix, windows string }{posix: examplePlaceholder, windows: examplePlaceholder}
	for _, artifact := range artifacts {
		if names.posix == examplePlaceholder {
			names.posix = artifact.File
		}
		if artifact.OS == "windows" {
			names.windows = artifact.File
		}
	}
	return names
}
