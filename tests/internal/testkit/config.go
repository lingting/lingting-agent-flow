package testkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ConfigDirEnv 是唯一允许影响测试配置读取的环境变量，用于覆盖配置目录位置。
//
// 未设置时使用仓库内的 tests/config；该变量只用于把测试指向另一份配置目录，
// 具体配置项本身一律来自配置文件，不再通过环境变量逐项读取。
const ConfigDirEnv = "AGENTFLOW_TEST_CONFIG_DIR"

// 测试配置文件名与示例文件前缀。
//
// 真实配置文件不入库，仓库只提交同名的示例文件；示例文件统一使用 ExamplePrefix
// 前缀，并由 .gitignore 按文件名逐个放行。
const (
	SSHConfigFile    = "ssh.yml"
	PiConfigFile     = "pi.yml"
	DockerConfigFile = "docker.yml"
	GitConfigFile    = "git.yml"
	ExamplePrefix    = "example."
)

// DefaultSSHPort 是 ssh.yml 未指定端口时使用的端口。
const DefaultSSHPort = 22

// DefaultPiModel 是 pi.yml 未指定模型时使用的免费模型。
//
// 该标识特意选用非标准名称，避免测试意外消耗付费模型额度；
// 不得改为任何常见的付费模型标识。
const DefaultPiModel = "group/free"

// 容器内 Pi 覆盖配置的默认值。
const (
	// DefaultContainerPiProvider 是写入容器内 models.json 的默认 provider 名称。
	DefaultContainerPiProvider = "agentflow-test"
	// DefaultContainerPiAPI 是默认接口协议：多数自建网关提供 OpenAI 兼容接口。
	DefaultContainerPiAPI = "openai-completions"
	// ContainerPiConfigDir 是容器内 Pi 配置目录相对容器内 Home 的位置。
	//
	// 真实远程测试覆盖容器内 Pi 配置时写入该目录，即容器内 $HOME/.pi/agent，
	// 与 Pi 默认的 agent 目录一致；该目录只存在于容器内部文件系统。
	ContainerPiConfigDir = ".pi/agent"
)

// SSHConfig 是 tests/config/ssh.yml 的内容。
type SSHConfig struct {
	// Host 是 SSH 服务器地址。
	Host string `yaml:"host"`
	// User 是登录用户名。
	User string `yaml:"user"`
	// Port 是 SSH 端口，未填写时为 DefaultSSHPort。
	Port int `yaml:"port"`
	// PrivateKeyPath 是测试使用的私钥路径。
	PrivateKeyPath string `yaml:"private_key_path"`
	// KnownHosts 是参与主机密钥校验的只读 known_hosts 文件列表。
	//
	// 为空时复用本机用户 OpenSSH 的 ~/.ssh/known_hosts 与 known_hosts2。
	KnownHosts []string `yaml:"known_hosts"`
}

// PiConfig 是 tests/config/pi.yml 的内容。
type PiConfig struct {
	// Model 是真实调用使用的模型标识，未填写时为 DefaultPiModel。
	Model string `yaml:"model"`
	// Executable 是 Pi 可执行文件路径，为空时使用 PATH 中的 pi。
	Executable string `yaml:"executable"`
}

// DockerConfig 是 tests/config/docker.yml 的内容。
type DockerConfig struct {
	// RemoteEnabled 决定是否允许测试操作真实远程 Docker。
	RemoteEnabled bool `yaml:"remote_enabled"`
	// AgentImage 是远程 Agent 测试使用的镜像引用。
	AgentImage string `yaml:"agent_image"`
	// GitImage 是远程代码保存测试使用的任务镜像引用。
	GitImage string `yaml:"git_image"`
	// Pi 是可选的容器内 Pi 覆盖配置。
	Pi ContainerPiConfig `yaml:"pi"`
}

// ContainerPiConfig 描述容器内 Pi 使用的模型接入配置。
//
// 配置齐全时，真实远程测试会覆盖容器内 Pi 自己的配置目录（容器内 $HOME/.pi/agent）
// 下的 models.json 与 settings.json，使容器内 Pi 使用这里给出的接入点、密钥、模型
// 与思考等级；任一必填项缺失时该配置视为未提供，相关用例按缺少配置跳过。
type ContainerPiConfig struct {
	// BaseURL 是模型接入点的 OpenAI 兼容地址，必填。
	BaseURL string `yaml:"base_url"`
	// APIKey 是接入点密钥，必填；只写入容器内 Pi 的配置文件，不写入日志。
	APIKey string `yaml:"api_key"`
	// Model 是容器内 Pi 使用的模型标识，必填。
	Model string `yaml:"model"`
	// ThinkingLevel 是容器内 Pi 的思考等级，留空时不显式指定。
	ThinkingLevel string `yaml:"thinking_level"`
	// API 是 provider 使用的接口协议，留空时使用 DefaultContainerPiAPI。
	API string `yaml:"api"`
	// Provider 是写入 models.json 的 provider 名称，留空时使用
	// DefaultContainerPiProvider。
	Provider string `yaml:"provider"`
}

// Enabled 判断容器内 Pi 覆盖配置是否完整可用。
func (c ContainerPiConfig) Enabled() bool {
	return strings.TrimSpace(c.BaseURL) != "" &&
		strings.TrimSpace(c.APIKey) != "" &&
		strings.TrimSpace(c.Model) != ""
}

// Missing 返回容器内 Pi 覆盖配置缺少的必填项，供跳过原因使用。
//
// 三项必填配置都为空时视为完全未提供，返回 nil。
func (c ContainerPiConfig) Missing() []string {
	if strings.TrimSpace(c.BaseURL) == "" &&
		strings.TrimSpace(c.APIKey) == "" &&
		strings.TrimSpace(c.Model) == "" {
		return nil
	}
	missing := make([]string, 0, 3)
	if strings.TrimSpace(c.BaseURL) == "" {
		missing = append(missing, "docker.pi.base_url")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		missing = append(missing, "docker.pi.api_key")
	}
	if strings.TrimSpace(c.Model) == "" {
		missing = append(missing, "docker.pi.model")
	}
	return missing
}

// ResolvedProvider 返回实际使用的 provider 名称。
func (c ContainerPiConfig) ResolvedProvider() string {
	if provider := strings.TrimSpace(c.Provider); provider != "" {
		return provider
	}
	return DefaultContainerPiProvider
}

// ResolvedAPI 返回实际使用的接口协议。
func (c ContainerPiConfig) ResolvedAPI() string {
	if api := strings.TrimSpace(c.API); api != "" {
		return api
	}
	return DefaultContainerPiAPI
}

// GitConfig 是 tests/config/git.yml 的内容。
type GitConfig struct {
	// Repository 是远程机器可访问的 Git 仓库地址。
	Repository string `yaml:"repository"`
}

// sshDocument 对应 ssh.yml 的顶层结构。
type sshDocument struct {
	SSH SSHConfig `yaml:"ssh"`
}

// piDocument 对应 pi.yml 的顶层结构。
type piDocument struct {
	Pi PiConfig `yaml:"pi"`
}

// dockerDocument 对应 docker.yml 的顶层结构。
type dockerDocument struct {
	Docker DockerConfig `yaml:"docker"`
}

// gitDocument 对应 git.yml 的顶层结构。
type gitDocument struct {
	Git GitConfig `yaml:"git"`
}

// ContainerPiModelsFile 返回写入容器内 Pi 配置目录的 models.json 内容。
//
// 该文件只声明本次测试使用的接入点、密钥与模型，写入容器内 Pi 自己的 agent 目录，
// 不写入宿主机或远程项目配置目录，也不会出现在日志里。
func ContainerPiModelsFile(config ContainerPiConfig) []byte {
	type modelEntry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type providerEntry struct {
		BaseURL string       `json:"baseUrl"`
		API     string       `json:"api"`
		APIKey  string       `json:"apiKey"`
		Models  []modelEntry `json:"models"`
	}
	model := strings.TrimSpace(config.Model)
	document := map[string]any{
		"providers": map[string]any{
			config.ResolvedProvider(): providerEntry{
				BaseURL: strings.TrimSpace(config.BaseURL),
				API:     config.ResolvedAPI(),
				APIKey:  strings.TrimSpace(config.APIKey),
				Models:  []modelEntry{{ID: model, Name: model}},
			},
		},
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		// 该结构只包含字符串与切片，序列化不会失败；保留兜底避免返回 nil。
		return []byte("{}\n")
	}
	return append(data, '\n')
}

// ContainerPiSettingsFile 返回写入容器内 Pi 配置目录的 settings.json 内容。
//
// 它只声明默认 provider、默认模型与默认思考等级，使容器内 Pi 的非交互调用直接
// 使用 docker.pi 给出的模型；思考等级为空时不写该字段，沿用 Pi 的默认值。
func ContainerPiSettingsFile(config ContainerPiConfig) []byte {
	document := map[string]any{
		"defaultProvider": config.ResolvedProvider(),
		"defaultModel":    strings.TrimSpace(config.Model),
	}
	if level := strings.TrimSpace(config.ThinkingLevel); level != "" {
		document["defaultThinkingLevel"] = level
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return []byte("{}\n")
	}
	return append(data, '\n')
}

// ConfigDir 返回测试配置目录的绝对路径。
//
// 该目录不存在是正常情况（仓库只提交示例文件），调用方按"缺少配置"处理。
func ConfigDir(t *testing.T) string {
	t.Helper()
	if configured := strings.TrimSpace(os.Getenv(ConfigDirEnv)); configured != "" {
		return configured
	}
	return filepath.Join(repoRoot(t), "tests", "config")
}

// ConfigPath 返回指定配置文件在测试配置目录中的绝对路径。
func ConfigPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(ConfigDir(t), name)
}

// ConfigHint 返回"复制示例文件"的使用提示，供跳过原因与失败信息引用。
func ConfigHint(t *testing.T, name string) string {
	t.Helper()
	return fmt.Sprintf(
		"请把 %s 复制为 %s 后填写内容",
		ConfigPath(t, ExamplePrefix+name),
		ConfigPath(t, name),
	)
}

// SkipReason 生成缺少测试配置时的跳过原因。
//
// missing 列出缺失的配置项名称；提示中包含配置文件与示例文件的绝对路径，
// 便于区分"没有配置"与"配置写错"。
func SkipReason(t *testing.T, name string, missing []string) string {
	t.Helper()
	return fmt.Sprintf(
		"WARN [SKIP] 缺少测试配置 %s 中的 %s；%s",
		ConfigPath(t, name),
		strings.Join(missing, ", "),
		ConfigHint(t, name),
	)
}

// LoadSSHConfig 读取 tests/config/ssh.yml。
//
// 文件不存在时返回零值配置与 false；文件存在但内容非法时让测试失败，
// 避免配置错误被当成"缺少环境"跳过。
func LoadSSHConfig(t *testing.T) (SSHConfig, bool) {
	t.Helper()
	var document sshDocument
	if !loadDocument(t, SSHConfigFile, &document) {
		return SSHConfig{}, false
	}
	config := document.SSH
	if config.Port == 0 {
		config.Port = DefaultSSHPort
	}
	return config, true
}

// LoadPiConfig 读取 tests/config/pi.yml。
func LoadPiConfig(t *testing.T) (PiConfig, bool) {
	t.Helper()
	var document piDocument
	if !loadDocument(t, PiConfigFile, &document) {
		return PiConfig{}, false
	}
	config := document.Pi
	if strings.TrimSpace(config.Model) == "" {
		config.Model = DefaultPiModel
	}
	return config, true
}

// LoadDockerConfig 读取 tests/config/docker.yml。
func LoadDockerConfig(t *testing.T) (DockerConfig, bool) {
	t.Helper()
	var document dockerDocument
	if !loadDocument(t, DockerConfigFile, &document) {
		return DockerConfig{}, false
	}
	return document.Docker, true
}

// LoadGitConfig 读取 tests/config/git.yml。
func LoadGitConfig(t *testing.T) (GitConfig, bool) {
	t.Helper()
	var document gitDocument
	if !loadDocument(t, GitConfigFile, &document) {
		return GitConfig{}, false
	}
	return document.Git, true
}

// loadDocument 读取并解析一个配置文件，返回文件是否存在。
//
// 解析使用严格模式：未知字段与多余文档都会被拒绝，避免拼写错误被静默忽略；
// 解析成功后统一展开 ${NAME} 占位符。
func loadDocument(t *testing.T, name string, target any) bool {
	t.Helper()
	path := ConfigPath(t, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false
		}
		t.Fatalf("读取测试配置失败: %s: %v", path, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			t.Fatalf("测试配置为空: %s", path)
		}
		t.Fatalf("解析测试配置失败: %s: %v", path, err)
	}
	var trailing yaml.Node
	switch err := decoder.Decode(&trailing); {
	case err == nil:
		t.Fatalf("测试配置只允许一个 YAML 文档: %s", path)
	case errors.Is(err, io.EOF):
	default:
		t.Fatalf("解析测试配置失败: %s: %v", path, err)
	}
	expandValue(reflect.ValueOf(target))
	return true
}

// envPlaceholder 匹配 ${NAME} 形式的占位符。
var envPlaceholder = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandEnv 把字符串中的 ${NAME} 替换为同名环境变量的值。
//
// 环境变量不存在时保留原样 ${NAME}，使未解析的占位符在路径与日志中直接可见；
// 环境变量存在但为空时替换为空字符串。
func ExpandEnv(value string) string {
	return envPlaceholder.ReplaceAllStringFunc(value, func(match string) string {
		name := envPlaceholder.FindStringSubmatch(match)[1]
		if resolved, ok := os.LookupEnv(name); ok {
			return resolved
		}
		return match
	})
}

// expandValue 就地展开配置结构体中的 ${NAME} 占位符。
//
// 展开在 YAML 解析之后进行，因此占位符替换出的反斜杠等内容不会被当作
// YAML 转义序列处理。
func expandValue(value reflect.Value) {
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !value.IsNil() {
			expandValue(value.Elem())
		}
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			expandValue(value.Field(index))
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			expandValue(value.Index(index))
		}
	case reflect.String:
		if value.CanSet() {
			value.SetString(ExpandEnv(value.String()))
		}
	}
}

// repoRoot 通过编译期源码路径定位仓库根目录。
//
// 该路径与工作目录无关，因此在任意包目录下执行 go test 都能找到 tests/config。
func repoRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("无法定位测试辅助代码路径")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(currentFile))))
}
