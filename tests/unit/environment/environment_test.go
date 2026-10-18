package environment_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// hashOf 返回测试用内容的 SHA-256 摘要。
func hashOf(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

// newVersion 构造一个合法的环境版本，供多个用例复用。
func newVersion(t *testing.T, ref domain.EnvironmentRef, number int) domain.EnvironmentVersion {
	t.Helper()
	version := domain.EnvironmentVersion{
		HostID:           "host-1",
		Ref:              ref,
		Number:           number,
		DockerfileSHA256: hashOf("FROM debian:12.7-slim"),
		ContextSHA256:    hashOf("context"),
		ParentVersion:    "",
		ImageRef:         "agentflow-base:v0001-0123456789ab",
		Status:           domain.BuildStatusPending,
		CreatedAt:        time.Now().UTC().Truncate(time.Millisecond),
	}
	version.BuildConfigSHA256 = hashOf("config-" + version.Label())
	if !ref.IsBase() {
		version.ParentVersion = domain.FormatVersionLabel(1)
	}
	if err := version.Validate(); err != nil {
		t.Fatalf("构造的版本应当合法: %v", err)
	}
	return version
}

func TestNewEnvironmentRef(t *testing.T) {
	t.Parallel()
	base, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("基础环境引用应当合法: %v", err)
	}
	if base.Name != domain.BaseEnvironmentName || !base.IsBase() {
		t.Fatalf("基础环境名称应为 %s，实际 %+v", domain.BaseEnvironmentName, base)
	}
	if base.Label() != "base/base" {
		t.Fatalf("基础环境标签不符: %s", base.Label())
	}
	language, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "Node")
	if err != nil {
		t.Fatalf("语言环境引用应当合法: %v", err)
	}
	if language.Name != "node" {
		t.Fatalf("语言环境名称应被规范化为小写: %s", language.Name)
	}
	if language.Label() != "language/node" {
		t.Fatalf("语言环境标签不符: %s", language.Label())
	}
	if _, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "other"); err == nil {
		t.Fatal("基础环境使用其他名称时应当失败")
	}
	if _, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "Bad Name"); err == nil {
		t.Fatal("非法语言环境名称应当失败")
	}
	if _, err := domain.ParseEnvironmentType("unknown"); err == nil {
		t.Fatal("未知环境类型应当失败")
	}
}

func TestVersionLabelFormatting(t *testing.T) {
	t.Parallel()
	if got := domain.FormatVersionLabel(1); got != "v0001" {
		t.Fatalf("版本标签格式不符: %s", got)
	}
	if got := domain.FormatVersionLabel(42); got != "v0042" {
		t.Fatalf("版本标签格式不符: %s", got)
	}
	number, err := domain.ParseVersionLabel("v0042")
	if err != nil {
		t.Fatalf("解析版本标签失败: %v", err)
	}
	if number != 42 {
		t.Fatalf("版本号不符: %d", number)
	}
	for _, invalid := range []string{"42", "v", "v0", "vabcd", "V0001", "v10000"} {
		if _, err := domain.ParseVersionLabel(invalid); err == nil {
			t.Fatalf("版本标签 %q 应当被拒绝", invalid)
		}
	}
}

func TestNextVersionNumberIsMonotonic(t *testing.T) {
	t.Parallel()
	next, err := domain.NextVersionNumber(nil)
	if err != nil {
		t.Fatalf("空历史应当返回第一个版本号: %v", err)
	}
	if next != 1 {
		t.Fatalf("第一个版本号应为 1，实际 %d", next)
	}
	next, err = domain.NextVersionNumber([]int{1, 2, 5})
	if err != nil {
		t.Fatalf("计算下一个版本号失败: %v", err)
	}
	if next != 6 {
		t.Fatalf("版本号应基于最大值递增，实际 %d", next)
	}
	// 即使历史中存在失败版本，其编号也不会被复用。
	next, err = domain.NextVersionNumber([]int{1, 2, 3})
	if err != nil {
		t.Fatalf("计算下一个版本号失败: %v", err)
	}
	if next != 4 {
		t.Fatalf("版本号不得复用，实际 %d", next)
	}
	if _, err := domain.NextVersionNumber([]int{domain.MaxEnvironmentVersionNumber}); err == nil {
		t.Fatal("版本号用尽时应当失败")
	}
}

func TestEnvironmentVersionValidation(t *testing.T) {
	t.Parallel()
	baseRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	version := newVersion(t, baseRef, 1)
	if err := version.Validate(); err != nil {
		t.Fatalf("合法版本校验失败: %v", err)
	}
	invalid := version
	invalid.DockerfileSHA256 = "not-a-hash"
	if err := invalid.Validate(); err == nil {
		t.Fatal("非法 Dockerfile 哈希应当被拒绝")
	}
	if !errors.Is(invalid.Validate(), domain.ErrInvalidValue) {
		t.Fatalf("字段校验失败应可用 ErrInvalidValue 判断: %v", invalid.Validate())
	}
	withParent := version
	withParent.ParentVersion = "v0001"
	if err := withParent.Validate(); err == nil {
		t.Fatal("基础环境不应允许父版本")
	}
	languageRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	language := newVersion(t, languageRef, 1)
	if language.ParentVersion == "" {
		t.Fatal("语言环境必须记录父版本")
	}
	withoutParent := language
	withoutParent.ParentVersion = ""
	if err := withoutParent.Validate(); err == nil {
		t.Fatal("语言环境缺少父版本时应当被拒绝")
	}
}

func TestVersionBuildTransitions(t *testing.T) {
	t.Parallel()
	baseRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	version := newVersion(t, baseRef, 1)
	if version.Usable() {
		t.Fatal("未构建的版本不应可用")
	}
	if err := version.MarkBuilding(); err != nil {
		t.Fatalf("标记构建中失败: %v", err)
	}
	if version.Status != domain.BuildStatusBuilding {
		t.Fatalf("构建状态不符: %s", version.Status)
	}
	imageID := "sha256:" + strings.Repeat("a", 64)
	if err := version.MarkBuilt(imageID, time.Now()); err != nil {
		t.Fatalf("标记构建成功失败: %v", err)
	}
	if !version.Usable() {
		t.Fatal("构建成功后版本应当可用")
	}
	if version.ImageID != imageID {
		t.Fatalf("镜像 ID 不符: %s", version.ImageID)
	}
	// 已成功的版本不允许被重新标记为失败或构建中。
	if err := version.MarkFailed("later failure"); err == nil {
		t.Fatal("已成功构建的版本不应允许标记为失败")
	}
	if err := version.MarkBuilding(); err == nil {
		t.Fatal("已成功构建的版本不应允许重新标记为构建中")
	}
}

func TestVersionFailureKeepsVersionUnusable(t *testing.T) {
	t.Parallel()
	baseRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	version := newVersion(t, baseRef, 2)
	if err := version.MarkBuilding(); err != nil {
		t.Fatalf("标记构建中失败: %v", err)
	}
	if err := version.MarkFailed("build failed: exit 1"); err != nil {
		t.Fatalf("标记构建失败失败: %v", err)
	}
	if version.Status != domain.BuildStatusFailed {
		t.Fatalf("构建状态不符: %s", version.Status)
	}
	if version.Usable() {
		t.Fatal("失败的版本不得可用")
	}
	if version.ImageID != "" {
		t.Fatalf("失败的版本不得保留镜像 ID: %s", version.ImageID)
	}
	if version.ErrorSummary == "" {
		t.Fatal("失败的版本应记录可诊断摘要")
	}
}

func TestValidateImageRefRejectsLatest(t *testing.T) {
	t.Parallel()
	for _, valid := range []string{
		"agentflow-base:v0001-0123456789ab",
		"agentflow-language-go:v0002-abcdefabcdef",
		"registry.example.com:5000/team/base:v1-0123456789ab",
	} {
		if err := domain.ValidateImageRef(valid); err != nil {
			t.Fatalf("镜像引用 %q 应当合法: %v", valid, err)
		}
	}
	for _, invalid := range []string{
		"",
		"agentflow-base",
		"agentflow-base:latest",
		"AgentFlow-Base:v1",
		"agentflow base:v1",
		"agentflow-base:v1;rm -rf /",
	} {
		if err := domain.ValidateImageRef(invalid); err == nil {
			t.Fatalf("镜像引用 %q 应当被拒绝", invalid)
		}
	}
}

func TestValidateImageID(t *testing.T) {
	t.Parallel()
	if err := domain.ValidateImageID("sha256:" + strings.Repeat("f", 64)); err != nil {
		t.Fatalf("合法镜像 ID 校验失败: %v", err)
	}
	for _, invalid := range []string{
		"",
		"sha256:abc",
		strings.Repeat("f", 64),
		"sha256:" + strings.Repeat("F", 64),
	} {
		if err := domain.ValidateImageID(invalid); err == nil {
			t.Fatalf("镜像 ID %q 应当被拒绝", invalid)
		}
	}
}
