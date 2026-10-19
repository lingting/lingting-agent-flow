package dockerfile_test

import (
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/dockerfile"
)

// TestParseSingleStage 验证单阶段 Dockerfile 的外部镜像被正确解析。
func TestParseSingleStage(t *testing.T) {
	t.Parallel()
	result, err := dockerfile.Parse([]byte(docker.DefaultBaseDockerfile))
	if err != nil {
		t.Fatalf("解析默认基础 Dockerfile 失败: %v", err)
	}
	if len(result.Images) != 1 {
		t.Fatalf("外部镜像数量不符: %+v", result.Images)
	}
	image := result.Images[0]
	if image.Reference != docker.DefaultBaseImage {
		t.Fatalf("镜像引用不符: %s", image.Reference)
	}
	// 默认基础镜像已带 tag，ResolveReference 不应追加 latest。
	if image.Resolved != docker.DefaultBaseImage {
		t.Fatalf("已带 tag 的引用不应被改写: %s", image.Resolved)
	}
	if len(result.Unresolved) != 0 {
		t.Fatalf("不应存在无法解析的引用: %+v", result.Unresolved)
	}
}

// TestParseMultiStageSkipsInternalStages 验证多阶段构建中的内部阶段别名不产生网络查询。
func TestParseMultiStageSkipsInternalStages(t *testing.T) {
	t.Parallel()
	content := `
# 多阶段构建示例
FROM golang:1.24 AS builder
WORKDIR /src
RUN go build -o /out/app ./cmd/app

FROM alpine:3.20 AS runtime
COPY --from=builder /out/app /usr/local/bin/app
CMD ["/usr/local/bin/app"]
`
	result, err := dockerfile.Parse([]byte(content))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(result.Images) != 2 {
		t.Fatalf("外部镜像数量应为 2，实际 %+v", result.Images)
	}
	if result.Images[0].Resolved != "golang:1.24" {
		t.Fatalf("第一个镜像不符: %s", result.Images[0].Resolved)
	}
	if result.Images[1].Resolved != "alpine:3.20" {
		t.Fatalf("第二个镜像不符: %s", result.Images[1].Resolved)
	}
	if result.Images[0].Stage != "builder" || result.Images[1].Stage != "runtime" {
		t.Fatalf("阶段别名不符: %+v", result.Images)
	}
	// 第三个阶段引用内部别名时不应产生额外镜像。
	more := content + "\nFROM builder AS extra\n"
	extra, err := dockerfile.Parse([]byte(more))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(extra.Images) != 2 {
		t.Fatalf("引用内部阶段别名不应新增外部镜像: %+v", extra.Images)
	}
	if len(extra.Skipped) != 1 || !strings.Contains(extra.Skipped[0].Reason, "内部构建阶段") {
		t.Fatalf("内部阶段别名应被记录为跳过: %+v", extra.Skipped)
	}
}

// TestParseScratchIsSkipped 验证 scratch 不产生网络查询。
func TestParseScratchIsSkipped(t *testing.T) {
	t.Parallel()
	result, err := dockerfile.Parse([]byte("FROM scratch\nCOPY app /app\n"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(result.Images) != 0 {
		t.Fatalf("scratch 不应产生外部镜像: %+v", result.Images)
	}
	if len(result.Skipped) != 1 {
		t.Fatalf("scratch 应被记录为跳过: %+v", result.Skipped)
	}
}

// TestParseResolvesGlobalArg 验证可解析的全局 ARG 会被替换。
func TestParseResolvesGlobalArg(t *testing.T) {
	t.Parallel()
	content := `
ARG BASE=node:22.19.0-bookworm-slim
FROM ${BASE}
CMD ["sh"]
`
	result, err := dockerfile.Parse([]byte(content))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(result.Images) != 1 {
		t.Fatalf("外部镜像数量不符: %+v", result.Images)
	}
	if result.Images[0].Reference != "node:22.19.0-bookworm-slim" {
		t.Fatalf("ARG 未被替换: %s", result.Images[0].Reference)
	}
}

// TestParseUsesArgDefaultValue 验证 ${VAR:-default} 形式使用默认值。
func TestParseUsesArgDefaultValue(t *testing.T) {
	t.Parallel()
	result, err := dockerfile.Parse([]byte("FROM ${BASE:-alpine:3.20}\n"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(result.Images) != 1 || result.Images[0].Reference != "alpine:3.20" {
		t.Fatalf("默认值未被使用: %+v", result.Images)
	}
}

// TestParseReportsUnresolvedArg 验证无法解析的变量不会被当成成功。
func TestParseReportsUnresolvedArg(t *testing.T) {
	t.Parallel()
	// 语言环境模板使用没有默认值的 AGENTFLOW_BASE_IMAGE，静态解析无法确定。
	template, err := docker.LanguageTemplateFor("node")
	if err != nil {
		t.Fatalf("读取语言模板失败: %v", err)
	}
	result, err := dockerfile.Parse([]byte(template.Dockerfile))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(result.Images) != 0 {
		t.Fatalf("无法解析的引用不应产生镜像: %+v", result.Images)
	}
	if len(result.Unresolved) != 1 {
		t.Fatalf("应记录无法解析的 FROM: %+v", result.Unresolved)
	}
	if !strings.Contains(result.Unresolved[0].Reason, "AGENTFLOW_BASE_IMAGE") {
		t.Fatalf("无法解析原因应指明变量: %s", result.Unresolved[0].Reason)
	}
}

// TestParseHandlesPlatformAndContinuation 验证 --platform 与续行处理。
func TestParseHandlesPlatformAndContinuation(t *testing.T) {
	t.Parallel()
	content := "FROM --platform=linux/amd64 \\\n    alpine:3.20 AS base\n"
	result, err := dockerfile.Parse([]byte(content))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(result.Images) != 1 {
		t.Fatalf("外部镜像数量不符: %+v", result.Images)
	}
	image := result.Images[0]
	if image.Platform != "linux/amd64" {
		t.Fatalf("--platform 未解析: %q", image.Platform)
	}
	if image.Resolved != "alpine:3.20" {
		t.Fatalf("镜像引用不符: %s", image.Resolved)
	}
	if image.Line != 1 {
		t.Fatalf("行号应为逻辑行起始行: %d", image.Line)
	}
}

// TestParseRejectsDockerfileWithoutFrom 验证缺少 FROM 时返回错误。
func TestParseRejectsDockerfileWithoutFrom(t *testing.T) {
	t.Parallel()
	if _, err := dockerfile.Parse([]byte("RUN echo hello\n")); err == nil {
		t.Fatal("缺少 FROM 应当返回错误")
	}
	if _, err := dockerfile.Parse(nil); err == nil {
		t.Fatal("空内容应当返回错误")
	}
}

// TestResolveReferenceAddsLatest 验证省略 tag 时补齐 latest。
func TestResolveReferenceAddsLatest(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"alpine":                           "alpine:latest",
		"alpine:3.20":                      "alpine:3.20",
		"registry.example.com:5000/app":    "registry.example.com:5000/app:latest",
		"registry.example.com:5000/app:v1": "registry.example.com:5000/app:v1",
		"alpine@sha256:abc":                "alpine@sha256:abc",
	}
	for input, expected := range cases {
		if actual := dockerfile.ResolveReference(input); actual != expected {
			t.Fatalf("ResolveReference(%q) = %q，期望 %q", input, actual, expected)
		}
	}
}

// TestParseDeduplicatesSameImage 验证同一镜像只保留首次出现。
func TestParseDeduplicatesSameImage(t *testing.T) {
	t.Parallel()
	content := "FROM alpine:3.20 AS a\nFROM alpine:3.20 AS b\n"
	result, err := dockerfile.Parse([]byte(content))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(result.Images) != 1 {
		t.Fatalf("同一镜像应去重: %+v", result.Images)
	}
}
