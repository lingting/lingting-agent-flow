package container_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// newLanguageContainer 构造一个语言环境容器记录。
func newLanguageContainer(t *testing.T) domain.Container {
	t.Helper()
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	container, err := domain.NewContainer(
		"container-1",
		"host-1",
		"agentflow-abcdef123456",
		ref,
		"v0001",
		strings.Repeat("a", 64),
		"sha256:"+strings.Repeat("b", 64),
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	return container
}

func TestCacheVolumeNameIsScoped(t *testing.T) {
	t.Parallel()
	machine := domain.CacheMount{
		Type:          "go-mod",
		ContainerPath: "/var/cache/agentflow/go/pkg/mod",
		Scope:         domain.CacheScopeMachine,
		ABI:           "go124",
	}
	first, err := docker.CacheVolumeName("host-1", machine, "")
	if err != nil {
		t.Fatalf("生成缓存卷名失败: %v", err)
	}
	if !strings.Contains(first, "go-mod") || !strings.Contains(first, "machine") {
		t.Fatalf("缓存卷名应包含类型与共享范围: %s", first)
	}
	// 不同 ABI 必须得到不同的卷名。
	otherABI := machine
	otherABI.ABI = "go125"
	second, err := docker.CacheVolumeName("host-1", otherABI, "")
	if err != nil {
		t.Fatalf("生成缓存卷名失败: %v", err)
	}
	if first == second {
		t.Fatal("ABI 变化必须改变缓存卷名，避免误共享不兼容的缓存")
	}
	// 不同节点必须得到不同的卷名。
	third, err := docker.CacheVolumeName("host-2", machine, "")
	if err != nil {
		t.Fatalf("生成缓存卷名失败: %v", err)
	}
	if first == third {
		t.Fatal("不同节点不应共享同一缓存卷")
	}
	// 项目级缓存的共享范围必须体现在卷名中。
	project := machine
	project.Scope = domain.CacheScopeProject
	projectA, err := docker.CacheVolumeName("host-1", project, "proj-a")
	if err != nil {
		t.Fatalf("生成缓存卷名失败: %v", err)
	}
	projectB, err := docker.CacheVolumeName("host-1", project, "proj-b")
	if err != nil {
		t.Fatalf("生成缓存卷名失败: %v", err)
	}
	if projectA == projectB {
		t.Fatal("项目级缓存必须按项目隔离")
	}
	if _, err := docker.CacheVolumeName("", machine, ""); err == nil {
		t.Fatal("缺少节点标识时应当失败")
	}
}

func TestContainerNameIsSafeAndUnique(t *testing.T) {
	t.Parallel()
	first, err := docker.ContainerNameFor("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("生成容器名称失败: %v", err)
	}
	if err := domain.ValidateContainerName(first); err != nil {
		t.Fatalf("生成的容器名称应当合法: %v", err)
	}
	if !strings.HasPrefix(first, "agentflow-") {
		t.Fatalf("容器名称应带固定前缀: %s", first)
	}
	second, err := docker.ContainerNameFor("fedcba9876543210fedcba9876543210")
	if err != nil {
		t.Fatalf("生成容器名称失败: %v", err)
	}
	if first == second {
		t.Fatal("不同登记记录必须得到不同的容器名称")
	}
	if _, err := docker.ContainerNameFor(""); err == nil {
		t.Fatal("空登记标识应当失败")
	}
}

func TestVerifyOwnershipRequiresMatchingLabels(t *testing.T) {
	t.Parallel()
	container := newLanguageContainer(t)
	labels := map[string]string{
		docker.OwnershipLabelKey: "true",
		docker.HostLabelKey:      container.HostID.String(),
		docker.ContainerLabelKey: container.ID.String(),
	}
	inspect := docker.ContainerInspect{ID: "docker-1", Labels: labels}
	if err := docker.VerifyOwnership(inspect, container); err != nil {
		t.Fatalf("标签一致时所有权核验应通过: %v", err)
	}

	missingFlag := inspect
	missingFlag.Labels = map[string]string{
		docker.HostLabelKey:      container.HostID.String(),
		docker.ContainerLabelKey: container.ID.String(),
	}
	if err := docker.VerifyOwnership(missingFlag, container); docker.ErrorCodeOf(err) != docker.CodeOwnershipMismatch {
		t.Fatalf("缺少管理标签时应返回所有权不一致: %v", err)
	}

	wrongHost := inspect
	wrongHost.Labels = map[string]string{
		docker.OwnershipLabelKey: "true",
		docker.HostLabelKey:      "host-other",
		docker.ContainerLabelKey: container.ID.String(),
	}
	if err := docker.VerifyOwnership(wrongHost, container); docker.ErrorCodeOf(err) != docker.CodeOwnershipMismatch {
		t.Fatalf("节点标签不一致时应返回所有权不一致: %v", err)
	}

	wrongRecord := inspect
	wrongRecord.Labels = map[string]string{
		docker.OwnershipLabelKey: "true",
		docker.HostLabelKey:      container.HostID.String(),
		docker.ContainerLabelKey: "other-container",
	}
	if err := docker.VerifyOwnership(wrongRecord, container); docker.ErrorCodeOf(err) != docker.CodeOwnershipMismatch {
		t.Fatalf("登记记录标签不一致时应返回所有权不一致: %v", err)
	}
}

func TestLayoutPaths(t *testing.T) {
	t.Parallel()
	layout, err := docker.NewLayout("/home/deploy/.lingting/agentflow")
	if err != nil {
		t.Fatalf("构造布局失败: %v", err)
	}
	baseRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	if got := layout.VersionDir(baseRef, 1); got != "/home/deploy/.lingting/agentflow/environments/base/versions/v0001" {
		t.Fatalf("基础环境版本目录不符: %s", got)
	}
	if got := layout.DockerfilePath(baseRef, 2); got != "/home/deploy/.lingting/agentflow/environments/base/versions/v0002/Dockerfile" {
		t.Fatalf("Dockerfile 路径不符: %s", got)
	}
	languageRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	if got := layout.VersionDir(languageRef, 3); got != "/home/deploy/.lingting/agentflow/environments/languages/go/versions/v0003" {
		t.Fatalf("语言环境版本目录不符: %s", got)
	}
	if got := layout.ProjectConfigPath("proj"); got != "/home/deploy/.lingting/agentflow/projects/proj/project.json" {
		t.Fatalf("项目配置路径不符: %s", got)
	}
	if got := layout.BuildLogPath("base/v0001-0123456789ab"); got != "/home/deploy/.lingting/agentflow/builds/base/v0001-0123456789ab/build.log" {
		t.Fatalf("构建日志路径不符: %s", got)
	}
	for _, invalid := range []string{"", "relative/path", "/", "/home/../etc"} {
		if _, err := docker.NewLayout(invalid); err == nil {
			t.Fatalf("非法工作空间路径 %q 应当被拒绝", invalid)
		}
	}
}

func TestParseDfAvailableKilobytes(t *testing.T) {
	t.Parallel()
	output := "Filesystem     1024-blocks    Used Available Capacity Mounted on\n" +
		"/dev/sda1         10240000 5120000   5120000      50% /\n"
	if got := docker.ParseDfAvailableKilobytes(output); got != 5120000 {
		t.Fatalf("可用块数解析不符: %d", got)
	}
	if got := docker.ParseDfAvailableKilobytes(""); got != 0 {
		t.Fatalf("空输出应返回 0，实际 %d", got)
	}
	if got := docker.ParseDfAvailableKilobytes("only one line"); got != 0 {
		t.Fatalf("单行输出应返回 0，实际 %d", got)
	}
}

func TestDockerVersionParsing(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"29.0.0":  true,
		"29.0.1":  true,
		"29.1.0":  true,
		"30.0.0":  true,
		"29.10.0": true,
		"28.5.2":  false,
		"19.03.8": false,
		"":        false,
		"abc":     false,
	}
	for raw, expected := range cases {
		err := docker.CheckVersionSupport(raw)
		if (err == nil) != expected {
			t.Fatalf("版本 %q 支持判定不符: 期望 %t，实际错误 %v", raw, expected, err)
		}
	}
}

func TestClassifyDockerMessage(t *testing.T) {
	t.Parallel()
	cases := map[string]docker.ErrorCode{
		"permission denied while trying to connect to the Docker daemon socket": docker.CodePermissionDenied,
		"bash: docker: command not found":                                       docker.CodeCLIMissing,
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock":    docker.CodeDaemonUnavailable,
		"Error: No such container: abc":                                         docker.CodeContainerNotFound,
		"something completely unexpected":                                       docker.CodeCommandFailed,
	}
	for message, expected := range cases {
		if got := docker.ClassifyDockerMessage(message); got != expected {
			t.Fatalf("消息分类不符: %q: 期望 %s，实际 %s", message, expected, got)
		}
	}
}

func TestContainerCreateArgsEnforceSafetyBoundary(t *testing.T) {
	t.Parallel()
	container := newLanguageContainer(t)
	args, err := docker.ContainerCreateArgs(docker.CreateContainerSpec{
		Container: container,
		ImageRef:  "agentflow-language-go:v0001-aaaaaaaaaaaa",
		WorkDir:   domain.DefaultContainerWorkDir,
		Caches: []docker.CacheVolumeSpec{
			{VolumeName: "agentflow-host1-go-mod-machine-go124", ContainerPath: "/var/cache/agentflow/go/pkg/mod"},
		},
	})
	if err != nil {
		t.Fatalf("构造容器创建参数失败: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, forbidden := range []string{
		"--privileged",
		"--pid=host",
		"--network=host",
		"--cap-add",
		"docker.sock",
		"type=bind",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("容器创建参数不得包含 %q: %s", forbidden, joined)
		}
	}
	for _, required := range []string{
		"--name",
		"--workdir",
		docker.OwnershipLabelKey + "=true",
		docker.ContainerLabelKey + "=" + container.ID.String(),
		"type=volume,source=agentflow-host1-go-mod-machine-go124",
		domain.DefaultContainerWorkDir,
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("容器创建参数缺少 %q: %s", required, joined)
		}
	}
	// 项目代码目录只能通过 --workdir 出现在容器内部，不能出现 bind 挂载。
	if strings.Contains(joined, "target="+domain.DefaultContainerWorkDir) {
		t.Fatalf("项目工作目录不得被挂载为卷: %s", joined)
	}
}

func TestContainerCreateArgsRejectInvalidInput(t *testing.T) {
	t.Parallel()
	container := newLanguageContainer(t)
	if _, err := docker.ContainerCreateArgs(docker.CreateContainerSpec{
		Container: container,
		ImageRef:  "latest",
	}); err == nil {
		t.Fatal("非法镜像引用应当被拒绝")
	}
	if _, err := docker.ContainerCreateArgs(docker.CreateContainerSpec{
		Container: container,
		ImageRef:  "agentflow-language-go:v0001-aaaaaaaaaaaa",
		Caches:    []docker.CacheVolumeSpec{{VolumeName: "", ContainerPath: "/var/cache/x"}},
	}); err == nil {
		t.Fatal("空缓存卷名应当被拒绝")
	}
	invalid := container
	invalid.Name = "bad name"
	if _, err := docker.ContainerCreateArgs(docker.CreateContainerSpec{
		Container: invalid,
		ImageRef:  "agentflow-language-go:v0001-aaaaaaaaaaaa",
	}); err == nil {
		t.Fatal("非法容器名称应当被拒绝")
	}
}

func TestBuildLogCommandQuotesPaths(t *testing.T) {
	t.Parallel()
	command, err := docker.BuildLogCommand(
		"docker build --tag agentflow-base:v0001-aaaaaaaaaaaa .",
		"/home/deploy/.lingting/agentflow/builds/base/v0001-aaaaaaaaaaaa/build.log",
	)
	if err != nil {
		t.Fatalf("构造构建日志命令失败: %v", err)
	}
	if !strings.Contains(command, "tee") {
		t.Fatalf("构建日志命令应写入远程日志: %s", command)
	}
	if !strings.Contains(command, "PIPESTATUS") {
		t.Fatalf("构建日志命令应透传构建退出码: %s", command)
	}
	if _, err := docker.BuildLogCommand("docker build .", ""); err == nil {
		t.Fatal("缺少日志路径时应当失败")
	}
	// 日志路径中的空格与引号必须被转义，不改变命令语义。
	quoted, err := docker.BuildLogCommand("docker build .", "/tmp/a b'c/build.log")
	if err != nil {
		t.Fatalf("构造构建日志命令失败: %v", err)
	}
	if !strings.Contains(quoted, `'/tmp/a b'\''c/build.log'`) {
		t.Fatalf("日志路径应被 POSIX 转义: %s", quoted)
	}
}

func TestParseImageInspect(t *testing.T) {
	t.Parallel()
	output := `[{"Id":"sha256:` + strings.Repeat("c", 64) + `",` +
		`"RepoTags":["agentflow-base:v0001-aaaaaaaaaaaa"],` +
		`"Created":"2025-01-01T00:00:00Z","Size":1234,` +
		`"Config":{"Labels":{"agentflow.build_config_sha256":"` + strings.Repeat("a", 64) + `"}}}]`
	inspect, err := docker.ParseImageInspect(output)
	if err != nil {
		t.Fatalf("解析镜像检查结果失败: %v", err)
	}
	if inspect.ID != "sha256:"+strings.Repeat("c", 64) {
		t.Fatalf("镜像 ID 不符: %s", inspect.ID)
	}
	if len(inspect.RepoTags) != 1 || inspect.RepoTags[0] != "agentflow-base:v0001-aaaaaaaaaaaa" {
		t.Fatalf("镜像标签不符: %+v", inspect.RepoTags)
	}
	if inspect.Labels[docker.BuildConfigLabelKey] != strings.Repeat("a", 64) {
		t.Fatalf("镜像标签未解析: %+v", inspect.Labels)
	}
	if _, err := docker.ParseImageInspect(""); err == nil {
		t.Fatal("空输出应当失败")
	}
	if _, err := docker.ParseImageInspect("not json"); err == nil {
		t.Fatal("非法 JSON 应当失败")
	}
	if _, err := docker.ParseImageInspect("[]"); err == nil {
		t.Fatal("空数组应当失败")
	}
}

func TestParseContainerInspect(t *testing.T) {
	t.Parallel()
	output := `[{"Id":"abc123","Name":"/agentflow-abcdef123456",` +
		`"Image":"sha256:` + strings.Repeat("b", 64) + `",` +
		`"Config":{"Labels":{"agentflow.managed":"true"}},` +
		`"State":{"Status":"running","Running":true,"ExitCode":0,"Error":""}}]`
	inspect, err := docker.ParseContainerInspect(output)
	if err != nil {
		t.Fatalf("解析容器检查结果失败: %v", err)
	}
	if inspect.ID != "abc123" {
		t.Fatalf("容器 ID 不符: %s", inspect.ID)
	}
	if inspect.Name != "agentflow-abcdef123456" {
		t.Fatalf("容器名称应去掉前导斜杠: %s", inspect.Name)
	}
	if inspect.State != domain.DockerStateRunning || !inspect.Running {
		t.Fatalf("容器状态不符: %s running=%t", inspect.State, inspect.Running)
	}
	if inspect.Labels[docker.OwnershipLabelKey] != "true" {
		t.Fatalf("容器标签未解析: %+v", inspect.Labels)
	}
}

func TestContainerWorkDirMustBeInsideWorkspace(t *testing.T) {
	t.Parallel()
	container := newLanguageContainer(t)
	for _, workDir := range []string{"/root", "/etc/app", "/workspace", "/home/deploy/project", "relative/path"} {
		if _, err := docker.ContainerCreateArgs(docker.CreateContainerSpec{
			Container: container,
			ImageRef:  "agentflow-language-go:v0001-aaaaaaaaaaaa",
			WorkDir:   workDir,
		}); err == nil {
			t.Fatalf("工作目录 %q 应当被拒绝", workDir)
		}
	}
	for _, workDir := range []string{
		domain.DefaultContainerWorkDir,
		"/workspace/project/sub",
	} {
		if _, err := docker.ContainerCreateArgs(docker.CreateContainerSpec{
			Container: container,
			ImageRef:  "agentflow-language-go:v0001-aaaaaaaaaaaa",
			WorkDir:   workDir,
		}); err != nil {
			t.Fatalf("工作目录 %q 应当被接受: %v", workDir, err)
		}
	}
}
