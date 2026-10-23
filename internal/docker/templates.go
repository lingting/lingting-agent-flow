package docker

import (
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
)

// 默认基础镜像与 Pi CLI 安装配置。
//
// 版本固定到具体发行版号，不使用 latest：镜像更新必须显式修改本文件并生成新版本，
// 使历史镜像始终可追溯。安装方式采用 Pi 官方文档给出的 npm 方案，
// 因此镜像内必须同时提供 Node.js 与 npm。
const (
	// DefaultBaseImage 是默认基础镜像引用。
	//
	// 基础镜像固定到具体的发行版版本标签，不使用 latest：基础环境更新时必须显式
	// 修改本文件并生成新版本，使历史镜像始终可追溯。
	DefaultBaseImage = "node:22.19.0-bookworm-slim"
	// DefaultPiCLIPackage 是 Pi CLI 的 npm 包名。
	DefaultPiCLIPackage = "@earendil-works/pi-coding-agent"
	// DefaultPiCLIVersion 是默认安装的 Pi CLI 版本。
	DefaultPiCLIVersion = "1.3.0"
)

// DefaultBaseDockerfile 是默认基础环境的 Dockerfile 内容。
//
// 内容约定：
//   - 安装后续 Agent 执行所需的通用工具与固定版本的 Pi CLI；
//   - 不写入任何凭据、Token 或私钥；
//   - 不挂载或引用宿主机 Docker Socket；
//   - 不预装任何需要人工交互的 Pi 扩展或凭据配置；
//   - 提供可验证的常驻运行契约（sleep 可用）；
//   - 内置 GitHub 的 SSH 客户端配置，使 github.com 走 ssh.github.com:443。
//
// 关于 SSH 客户端配置：该配置在"22 端口可达"与"22 端口不可达"两类环境下都成立，
// 因为 ssh.github.com:443 是 GitHub 官方支持的等价接入点，直连 22 可用的环境使用它
// 同样正常。把它作为基础镜像的默认值，可以避免容器内 Git 在受限网络中因为
// `kex_exchange_identification: Connection closed by remote host` 而无法 Clone/Push。
// 它只包含主机与端口映射，不包含任何凭据或身份信息。
const DefaultBaseDockerfile = `# AgentFlow 基础环境
#
# 说明：
#   - 该文件允许用户自行修改，每次保存都会生成新的不可覆盖版本；
#   - 禁止写入 API Key、密码、Token、SSH 私钥等凭据；
#   - 禁止引用宿主机 Docker Socket 或用户 Home 目录；
#   - 镜像必须提供 sh 与 sleep，以满足容器的常驻运行契约。
#
# 基础镜像固定到具体发行版版本，不使用 latest。
FROM ` + DefaultBaseImage + `

LABEL agentflow.environment="base" \
      agentflow.description="AgentFlow 基础运行环境"

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

# 通用工具：版本控制、网络诊断、构建辅助与文本处理。
# 其中 git 是 SourceProvider 在容器内执行 Clone 的必要条件，
# procps 提供 ps，便于诊断容器内进程状态。
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        openssh-client \
        procps \
        tar \
        unzip \
        xz-utils \
        zip \
    && rm -rf /var/lib/apt/lists/*

# Pi CLI 固定到具体版本，安装方式与官方文档一致（npm 全局安装，不执行依赖脚本）。
# 不在此处写入任何凭据：模型凭据由运行期环境变量或容器内登录状态提供，
# 也不预装任何需要人工交互的扩展。
RUN npm install -g --ignore-scripts \
        ` + DefaultPiCLIPackage + `@` + DefaultPiCLIVersion + ` \
    && npm cache clean --force \
    && pi --version

# GitHub 的 SSH 客户端配置：把 github.com 映射到官方支持的 443 端口接入点。
#
# 该配置在能直连 22 的环境同样可用，因此作为基础镜像默认值；它只声明主机与端口，
# 不包含任何凭据、私钥或身份信息，凭据仍由运行期按需注入。
RUN mkdir -p /root/.ssh \
    && chmod 700 /root/.ssh \
    && printf '%s\n' \
        'Host github.com' \
        '    Hostname ssh.github.com' \
        '    Port 443' \
        '    User git' \
        > /root/.ssh/config \
    && chmod 600 /root/.ssh/config

# 任务项目目录只存在于容器内部可写层，不绑定挂载宿主机目录。
WORKDIR /workspace/project

# 容器常驻方式：等待 docker exec 执行具体任务。
CMD ["sh", "-c", "sleep infinity"]
`

// LanguageTemplate 是一个语言环境的默认定义。
type LanguageTemplate struct {
	// Name 是语言环境名称。
	Name string
	// Description 是环境说明。
	Description string
	// Dockerfile 是该语言环境的默认 Dockerfile 内容。
	Dockerfile string
	// Caches 是该语言环境提供的依赖缓存定义。
	Caches []domain.CacheMount
}

// DefaultLanguageTemplates 返回内置的语言环境模板。
//
// 语言环境镜像通过 AGENTFLOW_BASE_IMAGE 构建参数显式依赖基础环境版本，
// 因此基础环境更新不会悄悄改变语言环境的构建结果。
func DefaultLanguageTemplates() []LanguageTemplate {
	return []LanguageTemplate{
		nodeTemplate(),
		pythonTemplate(),
		goTemplate(),
	}
}

// LanguageTemplateFor 按名称查找内置语言模板。
func LanguageTemplateFor(name string) (LanguageTemplate, error) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	for _, template := range DefaultLanguageTemplates() {
		if template.Name == normalized {
			return template, nil
		}
	}
	return LanguageTemplate{}, fmt.Errorf(
		"docker: 未知的语言环境模板 %q，可用模板: %s",
		name,
		strings.Join(availableLanguageNames(), ", "),
	)
}

// availableLanguageNames 返回全部内置语言模板名称。
func availableLanguageNames() []string {
	templates := DefaultLanguageTemplates()
	names := make([]string, 0, len(templates))
	for _, template := range templates {
		names = append(names, template.Name)
	}
	return names
}

// nodeTemplate 返回 Node.js 语言环境模板。
func nodeTemplate() LanguageTemplate {
	return LanguageTemplate{
		Name:        "node",
		Description: "Node.js 22 运行与依赖缓存",
		Dockerfile: languageDockerfile(
			"node",
			"Node.js 22",
			`RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# npm 与 pnpm 缓存指向容器内的挂载点，由 AgentFlow 用命名卷挂载。
ENV NPM_CONFIG_CACHE=/var/cache/agentflow/npm \
    PNPM_HOME=/var/cache/agentflow/pnpm`,
		),
		Caches: []domain.CacheMount{
			{
				Type:          "node-npm",
				ContainerPath: "/var/cache/agentflow/npm",
				Scope:         domain.CacheScopeMachine,
				ABI:           "node22",
			},
			{
				Type:          "node-pnpm",
				ContainerPath: "/var/cache/agentflow/pnpm",
				Scope:         domain.CacheScopeMachine,
				ABI:           "node22",
			},
		},
	}
}

// pythonTemplate 返回 Python 语言环境模板。
func pythonTemplate() LanguageTemplate {
	return LanguageTemplate{
		Name:        "python",
		Description: "Python 3.12 运行与依赖缓存",
		Dockerfile: languageDockerfile(
			"python",
			"Python 3.12",
			`RUN apt-get update \
    && apt-get install -y --no-install-recommends python3 python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/*

# pip 缓存指向容器内的挂载点，由 AgentFlow 用命名卷挂载。
ENV PIP_CACHE_DIR=/var/cache/agentflow/pip`,
		),
		Caches: []domain.CacheMount{
			{
				Type:          "python-pip",
				ContainerPath: "/var/cache/agentflow/pip",
				Scope:         domain.CacheScopeMachine,
				ABI:           "python312",
			},
		},
	}
}

// goTemplate 返回 Go 语言环境模板。
func goTemplate() LanguageTemplate {
	return LanguageTemplate{
		Name:        "go",
		Description: "Go 1.24 工具链与模块缓存",
		Dockerfile: languageDockerfile(
			"go",
			"Go 1.24",
			`RUN curl -fsSL https://go.dev/dl/go1.24.5.linux-amd64.tar.gz -o /tmp/go.tar.gz \
    && tar -C /usr/local -xzf /tmp/go.tar.gz \
    && rm -f /tmp/go.tar.gz \
    && ln -s /usr/local/go/bin/go /usr/local/bin/go \
    && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt

# Go 模块与构建缓存指向容器内的挂载点，由 AgentFlow 用命名卷挂载。
ENV GOPATH=/var/cache/agentflow/go \
    GOMODCACHE=/var/cache/agentflow/go/pkg/mod \
    GOCACHE=/var/cache/agentflow/go-build`,
		),
		Caches: []domain.CacheMount{
			{
				Type:          "go-mod",
				ContainerPath: "/var/cache/agentflow/go/pkg/mod",
				Scope:         domain.CacheScopeMachine,
				ABI:           "go124",
			},
			{
				Type:          "go-build",
				ContainerPath: "/var/cache/agentflow/go-build",
				Scope:         domain.CacheScopeProject,
				ABI:           "go124",
			},
		},
	}
}

// languageDockerfile 生成语言环境 Dockerfile 的公共骨架。
//
// 通过 AGENTFLOW_BASE_IMAGE 构建参数显式依赖基础环境版本，使构建结果可追溯；
// 参数没有默认值，缺失时 docker build 直接失败，而不是悄悄回退到浮动标签。
func languageDockerfile(name, runtimeLabel, body string) string {
	return fmt.Sprintf(`# AgentFlow 语言环境: %s
#
# 说明：
#   - 该文件允许用户自行修改，每次保存都会生成新的不可覆盖版本；
#   - 必须通过 AGENTFLOW_BASE_IMAGE 构建参数依赖明确的基础环境版本；
#   - 禁止写入 API Key、密码、Token、SSH 私钥等凭据。
ARG AGENTFLOW_BASE_IMAGE
FROM ${AGENTFLOW_BASE_IMAGE}

LABEL agentflow.environment="language/%s" \
      agentflow.description="%s"

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

%s

WORKDIR /workspace/project

CMD ["sh", "-c", "sleep infinity"]
`, name, name, runtimeLabel, body)
}

// DefaultLanguageManifest 是语言环境的定义清单内容。
//
// 清单只记录非机密元数据：语言名称、依赖的基础环境版本、缓存定义与镜像标签来源。
type DefaultLanguageManifest struct {
	// Name 是语言环境名称。
	Name string `json:"name"`
	// Description 是环境说明。
	Description string `json:"description"`
	// Caches 是缓存定义。
	Caches []domain.CacheMount `json:"caches"`
	// CreatedAt 是清单创建时间。
	CreatedAt string `json:"created_at"`
}

// NewLanguageManifest 依据模板构造语言环境清单。
func NewLanguageManifest(template LanguageTemplate, createdAt string) DefaultLanguageManifest {
	return DefaultLanguageManifest{
		Name:        template.Name,
		Description: template.Description,
		Caches:      template.Caches,
		CreatedAt:   createdAt,
	}
}

// BuildArgBaseImage 是语言环境 Dockerfile 依赖基础镜像的构建参数名。
const BuildArgBaseImage = "AGENTFLOW_BASE_IMAGE"

// ValidateDockerfile 对用户编辑后的 Dockerfile 做静态校验。
//
// 校验项：
//   - 内容不能为空；
//   - 必须包含 FROM 指令；
//   - 禁止 latest 作为唯一基础镜像标签；
//   - 禁止 --privileged 与 Docker Socket 挂载；
//   - 禁止写入凭据材料。
func ValidateDockerfile(content []byte) error {
	text := string(content)
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("docker: Dockerfile 内容不能为空")
	}
	if err := ValidateContent(DockerfileName, content); err != nil {
		return err
	}
	if !strings.Contains(strings.ToUpper(text), "FROM ") {
		return fmt.Errorf("docker: Dockerfile 必须包含 FROM 指令")
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(trimmed), "FROM ") {
			continue
		}
		image := strings.TrimSpace(trimmed[len("FROM "):])
		if index := strings.Index(image, " AS "); index > 0 {
			image = strings.TrimSpace(image[:index])
		}
		if image == "scratch" || strings.Contains(image, "${") {
			continue
		}
		if strings.HasSuffix(image, ":latest") {
			return fmt.Errorf(
				"docker: 基础镜像 %s 使用了 latest 标签，请固定到具体版本",
				image,
			)
		}
	}
	return nil
}
