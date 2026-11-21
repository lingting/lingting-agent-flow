package testrunner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// 本文件是真实外部依赖测试的配置准备入口（V3 R8）。
//
// CI 的 real-tests 环境把连接目标与凭据放在 Environment Secrets 里：执行器在运行前把它们
// 渲染成 tests/config/*.yml 与临时凭据文件，供真实分组的用例读取。R8 之前这一步由
// .github/scripts 下的 Python 脚本完成，现在收回到执行器，配置准备与执行只有一条入口。
//
// 约束（与授权判定一致）：
//   - 缺少必需凭据时不做任何猜测，直接给出"未配置"结论，由工作流把本次运行标记为跳过；
//   - 配置内容与凭据值不打印，调用方只输出"哪些文件已生成"，并对凭据值调用 add-mask；
//   - 不写 tests/config 之外的项目路径，也不上传任何配置文件。

// 测试配置文件名与默认值。
const (
	// RealConfigDir 是测试配置的写入目录，相对仓库根，已被 .gitignore 忽略。
	RealConfigDir = "tests/config"
	// realConfigWorkDirName 是临时凭据目录名，位于 RUNNER_TEMP 之下。
	realConfigWorkDirName = "agentflow-real-tests"
	// realConfigSecretMode 是私钥与 known_hosts 的文件权限。
	realConfigSecretMode = 0o600
	// defaultRealTestImage 是真实测试容器的默认镜像，与 tests/config/example.*.yml 一致。
	defaultRealTestImage = "agentflow-test-agent:1.0.0"
	// defaultRealPIModel 是默认模型：使用免费模型，避免真实运行产生费用。
	defaultRealPIModel = "group/free"
)

// 真实测试配置文件名。
const (
	realConfigSSHFile    = "ssh.yml"
	realConfigPIFile     = "pi.yml"
	realConfigDockerFile = "docker.yml"
	realConfigGitFile    = "git.yml"
)

// RealConfigInput 是配置准备的全部输入，取值来自 real-tests 环境的 Environment Secrets。
type RealConfigInput struct {
	// SSHHost / SSHUser / SSHPort 是真实 SSH 设备的连接目标；端口留空时使用 22。
	SSHHost string
	SSHUser string
	SSHPort string
	// SSHPrivateKey 与 SSHKnownHosts 是必需凭据，会被写入临时文件后以路径形式进配置。
	SSHPrivateKey string
	SSHKnownHosts string
	// DockerAgentImage / DockerGitImage 是真实测试容器镜像，留空时使用默认镜像。
	DockerAgentImage string
	DockerGitImage   string
	// DockerPI* 是容器内 Pi 的覆盖配置：三项齐全时才写入 docker.yml。
	DockerPIBaseURL       string
	DockerPIAPIKey        string
	DockerPIModel         string
	DockerPIThinkingLevel string
	// GitRepository 与 GitSSHKey 是真实远程 Git 的仓库地址与私钥；私钥留空时不写文件。
	GitRepository string
	GitSSHKey     string
	// PIModel 是本机真实 Pi 的模型标识，留空时使用免费模型。
	PIModel string
	// WorkRoot 是临时凭据目录的父目录（CI 传 RUNNER_TEMP），留空时退回系统临时目录。
	WorkRoot string
}

// RealConfigResult 是配置准备的结论。
type RealConfigResult struct {
	// Configured 表示必需凭据齐全并已生成配置；false 表示本次运行应跳过真实测试。
	Configured bool
	// Written 是已生成的测试配置文件名。
	Written []string
	// MissingSecrets 是缺失的必需 Secret 名；仅 Configured 为 false 时非空。
	MissingSecrets []string
	// ContainerPI 表示容器内 Pi 的覆盖配置是否齐全。
	ContainerPI bool
	// Summary 是写入 Job 摘要的 Markdown 结论，不含任何凭据值。
	Summary string
}

// realConfigFile 是一个待写出的测试配置文件。
type realConfigFile struct {
	name  string
	lines []string
}

// PrepareRealConfig 按输入生成测试配置，返回结论；缺少必需凭据不是错误。
func PrepareRealConfig(repoRoot string, input RealConfigInput) (RealConfigResult, error) {
	result := RealConfigResult{}
	if missing := input.missingSecrets(); len(missing) > 0 {
		result.MissingSecrets = missing
		result.Summary = realConfigSkipSummary(missing)
		return result, nil
	}
	port, err := realConfigPort(input.SSHPort)
	if err != nil {
		return result, err
	}
	sshKeyPath, err := writeRealSecretFile(input.WorkRoot, "ssh_key", input.SSHPrivateKey)
	if err != nil {
		return result, err
	}
	knownHostsPath, err := writeRealSecretFile(input.WorkRoot, "known_hosts", input.SSHKnownHosts)
	if err != nil {
		return result, err
	}
	gitKeyPath := ""
	if strings.TrimSpace(input.GitSSHKey) != "" {
		gitKeyPath, err = writeRealSecretFile(input.WorkRoot, "git_key", input.GitSSHKey)
		if err != nil {
			return result, err
		}
	}

	configDir := filepath.Join(repoRoot, RealConfigDir)
	dockerLines, containerPI := realConfigDockerLines(input)
	files := []realConfigFile{
		{name: realConfigSSHFile, lines: realConfigSSHLines(input, sshKeyPath, knownHostsPath, port)},
		{name: realConfigPIFile, lines: realConfigPILines(input)},
		{name: realConfigDockerFile, lines: dockerLines},
		{name: realConfigGitFile, lines: realConfigGitLines(input, gitKeyPath)},
	}
	written := make([]string, 0, len(files))
	for _, file := range files {
		if err := writeRealConfigFile(configDir, file.name, file.lines); err != nil {
			return result, err
		}
		written = append(written, file.name)
	}

	result.Configured = true
	result.Written = written
	result.ContainerPI = containerPI
	result.Summary = realConfigSummary(written, containerPI)
	return result, nil
}

// MaskedSecretLines 把凭据拆成需要 add-mask 的行。
//
// 工作流的命令按行解析，多行值整体传给 add-mask 会把后续行当成普通日志输出，反而泄露
// 凭据内容，因此逐行处理；过短的行不参与掩码，避免把常见字符串整体掩掉。
func MaskedSecretLines(values ...string) []string {
	lines := make([]string, 0, len(values))
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			trimmed := strings.TrimSpace(line)
			if len(trimmed) >= 8 {
				lines = append(lines, trimmed)
			}
		}
	}
	return lines
}

// missingSecrets 返回缺失的必需 Secret 名（按名称排序，便于稳定输出）。
func (i RealConfigInput) missingSecrets() []string {
	required := map[string]string{
		"REAL_SSH_HOST":        i.SSHHost,
		"REAL_SSH_USER":        i.SSHUser,
		"REAL_SSH_PRIVATE_KEY": i.SSHPrivateKey,
		"REAL_SSH_KNOWN_HOSTS": i.SSHKnownHosts,
	}
	names := make([]string, 0, len(required))
	for name := range required {
		names = append(names, name)
	}
	sort.Strings(names)
	missing := make([]string, 0, len(names))
	for _, name := range names {
		if strings.TrimSpace(required[name]) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

// realConfigPort 解析 SSH 端口；留空时使用 22，非法值直接报错。
func realConfigPort(raw string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 22, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("REAL_SSH_PORT 不是合法端口: %q", value)
	}
	return port, nil
}

// realConfigSSHLines 生成 ssh.yml 的内容。
func realConfigSSHLines(input RealConfigInput, keyPath, knownHostsPath string, port int) []string {
	return []string{
		"ssh:",
		"  enable: true",
		"  host: " + realConfigQuote(strings.TrimSpace(input.SSHHost)),
		"  user: " + realConfigQuote(strings.TrimSpace(input.SSHUser)),
		"  port: " + strconv.Itoa(port),
		"  private_key_path: " + realConfigQuote(keyPath),
		"  known_hosts:",
		"    - " + realConfigQuote(knownHostsPath),
	}
}

// realConfigPILines 生成 pi.yml 的内容。
func realConfigPILines(input RealConfigInput) []string {
	model := strings.TrimSpace(input.PIModel)
	if model == "" {
		model = defaultRealPIModel
	}
	return []string{
		"pi:",
		"  enable: true",
		"  model: " + realConfigQuote(model),
	}
}

// realConfigDockerLines 生成 docker.yml 的内容，并返回容器内 Pi 配置是否齐全。
func realConfigDockerLines(input RealConfigInput) ([]string, bool) {
	agentImage := strings.TrimSpace(input.DockerAgentImage)
	if agentImage == "" {
		agentImage = defaultRealTestImage
	}
	gitImage := strings.TrimSpace(input.DockerGitImage)
	if gitImage == "" {
		gitImage = defaultRealTestImage
	}
	lines := []string{
		"docker:",
		"  remote_enabled: true",
		"  agent_image: " + realConfigQuote(agentImage),
		"  git_image: " + realConfigQuote(gitImage),
	}
	baseURL := strings.TrimSpace(input.DockerPIBaseURL)
	apiKey := strings.TrimSpace(input.DockerPIAPIKey)
	model := strings.TrimSpace(input.DockerPIModel)
	if baseURL == "" || apiKey == "" || model == "" {
		return lines, false
	}
	lines = append(lines,
		"  pi:",
		"    base_url: "+realConfigQuote(baseURL),
		"    api_key: "+realConfigQuote(apiKey),
		"    model: "+realConfigQuote(model),
	)
	if thinkingLevel := strings.TrimSpace(input.DockerPIThinkingLevel); thinkingLevel != "" {
		lines = append(lines, "    thinking_level: "+realConfigQuote(thinkingLevel))
	}
	return lines, true
}

// realConfigGitLines 生成 git.yml 的内容：只支持私钥方式，token 与账号密码一律留空。
func realConfigGitLines(input RealConfigInput, keyPath string) []string {
	return []string{
		"git:",
		"  repository: " + realConfigQuote(strings.TrimSpace(input.GitRepository)),
		"  ssh_key_path: " + realConfigQuote(keyPath),
		`  token: ""`,
		`  username: ""`,
		`  password: ""`,
	}
}

// realConfigQuote 把字符串渲染为 YAML 双引号标量。
//
// JSON 双引号字符串是合法的 YAML 双引号标量，因此借 encoding/json 保证引号、反斜杠与
// 换行不会破坏配置结构。
func realConfigQuote(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

// writeRealSecretFile 把凭据写入临时目录并返回绝对路径。
func writeRealSecretFile(workRoot, name, content string) (string, error) {
	root := strings.TrimSpace(workRoot)
	if root == "" {
		root = os.TempDir()
	}
	directory := filepath.Join(root, realConfigWorkDirName)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", fmt.Errorf("创建临时凭据目录失败: %w", err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), realConfigSecretMode); err != nil {
		return "", fmt.Errorf("写入临时凭据文件 %s 失败: %w", name, err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("解析临时凭据文件路径失败: %w", err)
	}
	return absolute, nil
}

// writeRealConfigFile 写出一个测试配置文件，内容统一使用 LF 行尾。
func writeRealConfigFile(configDir, name string, lines []string) error {
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		return fmt.Errorf("创建测试配置目录失败: %w", err)
	}
	content := strings.Join(lines, "\n") + "\n"
	path := filepath.Join(configDir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("写入测试配置 %s 失败: %w", name, err)
	}
	return nil
}

// realConfigSkipSummary 是缺少必需凭据时的结论：跳过真实测试，不猜测配置。
func realConfigSkipSummary(missing []string) string {
	var builder strings.Builder
	builder.WriteString("### 真实测试配置\n\n")
	builder.WriteString("SKIP：real-tests 环境缺少必需 Secret（")
	builder.WriteString(strings.Join(missing, "、"))
	builder.WriteString("），本次没有执行任何真实测试。\n\n")
	builder.WriteString("fork PR 默认拿不到环境 Secret；同仓库分支请在 Environment 上补齐配置。\n")
	builder.WriteString("未执行的用例不计为通过。\n")
	return builder.String()
}

// realConfigSummary 是配置就绪时的结论：只列已生成的文件，不含任何凭据值。
func realConfigSummary(written []string, containerPI bool) string {
	container := "未提供（相关用例按缺少配置跳过）"
	if containerPI {
		container = "已提供"
	}
	return "### 真实测试配置\n\n已生成的测试配置：" + strings.Join(written, "、") + "。\n\n" +
		"配置内容与凭据值不写入日志；容器内 Pi 覆盖配置" + container + "。\n"
}
