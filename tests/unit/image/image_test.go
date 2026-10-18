package image_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// hashA 与 hashB 是测试用的确定性哈希值。
const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// newBaseVersion 构造一个基础环境版本。
func newBaseVersion(t *testing.T) domain.EnvironmentVersion {
	t.Helper()
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	return domain.EnvironmentVersion{
		HostID:            "host-1",
		Ref:               ref,
		Number:            1,
		DockerfileSHA256:  hashA,
		BuildConfigSHA256: hashB,
		ContextSHA256:     hashA,
		Status:            domain.BuildStatusPending,
		ImageRef:          "agentflow-base:v0001-aaaaaaaaaaaa",
		CreatedAt:         time.Now().UTC().Truncate(time.Millisecond),
	}
}

func TestContextHashIsDeterministic(t *testing.T) {
	t.Parallel()
	files := []docker.ContextFile{
		{RelativePath: "Dockerfile", Content: []byte("FROM debian:12.7-slim\n"), Mode: 0o644},
		{RelativePath: "scripts/setup.sh", Content: []byte("echo setup\n"), Mode: 0o755},
	}
	first, err := docker.NewContext(files)
	if err != nil {
		t.Fatalf("构造上下文失败: %v", err)
	}
	// 相同内容、不同顺序必须得到相同哈希。
	reordered, err := docker.NewContext([]docker.ContextFile{files[1], files[0]})
	if err != nil {
		t.Fatalf("构造上下文失败: %v", err)
	}
	if first.Hash() != reordered.Hash() {
		t.Fatalf("上下文哈希应与文件顺序无关: %s != %s", first.Hash(), reordered.Hash())
	}
	// 内容变化必须改变哈希。
	changed, err := docker.NewContext([]docker.ContextFile{
		{RelativePath: "Dockerfile", Content: []byte("FROM debian:12.8-slim\n"), Mode: 0o644},
		{RelativePath: "scripts/setup.sh", Content: []byte("echo setup\n"), Mode: 0o755},
	})
	if err != nil {
		t.Fatalf("构造上下文失败: %v", err)
	}
	if first.Hash() == changed.Hash() {
		t.Fatal("内容变化必须改变上下文哈希")
	}
	// 权限变化也必须改变哈希。
	changedMode, err := docker.NewContext([]docker.ContextFile{
		{RelativePath: "Dockerfile", Content: []byte("FROM debian:12.7-slim\n"), Mode: 0o600},
		{RelativePath: "scripts/setup.sh", Content: []byte("echo setup\n"), Mode: 0o755},
	})
	if err != nil {
		t.Fatalf("构造上下文失败: %v", err)
	}
	if first.Hash() == changedMode.Hash() {
		t.Fatal("权限变化必须改变上下文哈希")
	}
}

func TestContextRejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	for _, invalid := range []string{
		"",
		"/etc/passwd",
		"../outside.txt",
		"scripts/../../outside.txt",
		"scripts\\setup.sh",
		"scripts/./setup.sh",
	} {
		if _, err := docker.NewContext([]docker.ContextFile{
			{RelativePath: invalid, Content: []byte("x")},
		}); err == nil {
			t.Fatalf("非法上下文路径 %q 应当被拒绝", invalid)
		}
	}
	if _, err := docker.NewContext(nil); err == nil {
		t.Fatal("空上下文应当被拒绝")
	}
	if _, err := docker.NewContext([]docker.ContextFile{
		{RelativePath: "a.txt", Content: []byte("1")},
		{RelativePath: "a.txt", Content: []byte("2")},
	}); err == nil {
		t.Fatal("重复路径应当被拒绝")
	}
}

func TestContextRejectsSensitiveContent(t *testing.T) {
	t.Parallel()
	if err := docker.ValidateContent("Dockerfile", []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n")); err == nil {
		t.Fatal("包含私钥的上下文应当被拒绝")
	}
	if err := docker.ValidateContent("Dockerfile", []byte("FROM debian:12.7-slim\n# /var/run/docker.sock\n")); err == nil {
		t.Fatal("引用宿主机 Docker Socket 的上下文应当被拒绝")
	}
	if err := docker.ValidateContent("Dockerfile", []byte("COPY file:///home/user/app /app\n")); err == nil {
		t.Fatal("包含本机路径引用的上下文应当被拒绝")
	}
	if err := docker.ValidateContent("Dockerfile", []byte("FROM debian:12.7-slim\n")); err != nil {
		t.Fatalf("普通内容不应被拒绝: %v", err)
	}
}

func TestReadContextDirectorySkipsTemporaryFiles(t *testing.T) {
	t.Parallel()
	root := testkit.Dir(t)
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM debian:12.7-slim\n"), 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.tmp"), []byte("temporary"), 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("创建子目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "config.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	context, err := docker.ReadContextDirectory(root)
	if err != nil {
		t.Fatalf("读取上下文目录失败: %v", err)
	}
	if len(context.Files) != 2 {
		t.Fatalf("临时文件应被排除，实际文件数 %d", len(context.Files))
	}
	for _, file := range context.Files {
		if strings.HasSuffix(file.RelativePath, ".tmp") {
			t.Fatalf("临时文件不应进入上下文: %s", file.RelativePath)
		}
	}
}

func TestBuildConfigHashCoversAllInputs(t *testing.T) {
	t.Parallel()
	version := newBaseVersion(t)
	base := docker.BuildConfigHash(version, "")
	if base != docker.BuildConfigHash(version, "") {
		t.Fatal("相同输入的构建配置哈希必须一致")
	}
	if base == docker.BuildConfigHash(version, "debian:12.7-slim") {
		t.Fatal("父镜像变化必须改变构建配置哈希")
	}
	changedContext := version
	changedContext.ContextSHA256 = hashB
	if base == docker.BuildConfigHash(changedContext, "") {
		t.Fatal("构建上下文变化必须改变构建配置哈希")
	}
	changed := version
	changed.DockerfileSHA256 = hashB
	if base == docker.BuildConfigHash(changed, "") {
		t.Fatal("Dockerfile 变化必须改变构建配置哈希")
	}
	// Dockerfile 哈希与构建配置哈希是两个独立字段。
	if version.DockerfileSHA256 == version.BuildConfigSHA256 {
		t.Fatal("Dockerfile 哈希与构建配置哈希应独立记录")
	}
}

func TestImageRefForIsTraceable(t *testing.T) {
	t.Parallel()
	version := newBaseVersion(t)
	ref, err := docker.ImageRefFor(version)
	if err != nil {
		t.Fatalf("生成镜像引用失败: %v", err)
	}
	if strings.Contains(ref, ":latest") {
		t.Fatalf("镜像引用不得使用 latest: %s", ref)
	}
	if !strings.Contains(ref, version.Label()) {
		t.Fatalf("镜像引用应包含版本标签: %s", ref)
	}
	if !strings.Contains(ref, version.BuildConfigSHA256[:12]) {
		t.Fatalf("镜像引用应包含构建配置哈希前缀: %s", ref)
	}
	if err := domain.ValidateImageRef(ref); err != nil {
		t.Fatalf("生成的镜像引用应当合法: %v", err)
	}
}

func TestValidateDockerfile(t *testing.T) {
	t.Parallel()
	if err := docker.ValidateDockerfile([]byte(docker.DefaultBaseDockerfile)); err != nil {
		t.Fatalf("默认基础 Dockerfile 应当通过校验: %v", err)
	}
	for _, template := range docker.DefaultLanguageTemplates() {
		if err := docker.ValidateDockerfile([]byte(template.Dockerfile)); err != nil {
			t.Fatalf("语言环境模板 %s 应当通过校验: %v", template.Name, err)
		}
		if !strings.Contains(template.Dockerfile, docker.BuildArgBaseImage) {
			t.Fatalf("语言环境 %s 必须依赖明确的基础镜像参数", template.Name)
		}
	}
	if err := docker.ValidateDockerfile([]byte("   ")); err == nil {
		t.Fatal("空 Dockerfile 应当被拒绝")
	}
	if err := docker.ValidateDockerfile([]byte("RUN echo hi\n")); err == nil {
		t.Fatal("缺少 FROM 的 Dockerfile 应当被拒绝")
	}
	if err := docker.ValidateDockerfile([]byte("FROM debian:latest\n")); err == nil {
		t.Fatal("使用 latest 的基础镜像应当被拒绝")
	}
	if err := docker.ValidateDockerfile([]byte("FROM debian:12.7-slim\n# /var/run/docker.sock\n")); err == nil {
		t.Fatal("引用 Docker Socket 的 Dockerfile 应当被拒绝")
	}
}

func TestDefaultBaseDockerfileHasNoLatestOrSecrets(t *testing.T) {
	t.Parallel()
	content := docker.DefaultBaseDockerfile
	if strings.Contains(content, ":latest") {
		t.Fatal("默认基础 Dockerfile 不得使用 latest 标签")
	}
	for _, forbidden := range []string{"PRIVATE KEY", "API_KEY", "docker.sock", "privileged"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("默认基础 Dockerfile 不得包含 %q", forbidden)
		}
	}
	if !strings.Contains(content, domain.DefaultContainerWorkDir) {
		t.Fatalf("默认基础 Dockerfile 应设置容器内项目目录 %s", domain.DefaultContainerWorkDir)
	}
}

func TestLanguageTemplateLookup(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"node", "python", "go"} {
		template, err := docker.LanguageTemplateFor(name)
		if err != nil {
			t.Fatalf("查找语言模板 %s 失败: %v", name, err)
		}
		if template.Name != name {
			t.Fatalf("模板名称不符: %s", template.Name)
		}
		for _, cache := range template.Caches {
			if err := cache.Validate(); err != nil {
				t.Fatalf("模板 %s 的缓存定义非法: %v", name, err)
			}
		}
	}
	if _, err := docker.LanguageTemplateFor("rust"); err == nil {
		t.Fatal("未知语言模板应当失败")
	}
}
