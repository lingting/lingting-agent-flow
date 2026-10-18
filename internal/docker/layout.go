package docker

import (
	"fmt"
	"path"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
)

// 远程工作空间内的固定子目录名。
const (
	// DirectoryEnvironments 保存环境 Dockerfile 与版本清单。
	DirectoryEnvironments = "environments"
	// DirectoryProjects 保存项目配置，不保存项目源代码。
	DirectoryProjects = "projects"
	// DirectoryImages 保存镜像元数据清单。
	DirectoryImages = "images"
	// DirectoryContainers 保存容器登记清单。
	DirectoryContainers = "containers"
	// DirectoryBuilds 保存构建日志。
	DirectoryBuilds = "builds"
)

// VersionDirectoryName 是保存历史版本的子目录名。
const VersionDirectoryName = "versions"

// 环境清单文件名。
const (
	// DockerfileName 是构建使用的 Dockerfile 文件名。
	DockerfileName = "Dockerfile"
	// ManifestFileName 是版本清单文件名。
	ManifestFileName = "manifest.json"
	// LanguageManifestFileName 是语言环境定义文件名。
	LanguageManifestFileName = "language.json"
)

// Layout 是远程 AgentFlow 工作空间的目录布局。
//
// 所有路径都是远程绝对路径，由远程 Home 解析后拼接得到，不使用本机路径规则。
type Layout struct {
	// Root 是工作空间根目录绝对路径。
	Root string
}

// NewLayout 校验并构造目录布局。
func NewLayout(root string) (Layout, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return Layout{}, fmt.Errorf("docker: 远程工作空间不能为空")
	}
	if !strings.HasPrefix(trimmed, "/") {
		return Layout{}, fmt.Errorf("docker: 远程工作空间 %q 必须是绝对路径", root)
	}
	if strings.ContainsRune(trimmed, '\\') {
		return Layout{}, fmt.Errorf("docker: 远程工作空间 %q 必须使用 POSIX 分隔符 /", root)
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == ".." {
			return Layout{}, fmt.Errorf("docker: 远程工作空间 %q 不能包含上级目录引用 ..", root)
		}
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "/" {
		return Layout{}, fmt.Errorf("docker: 远程工作空间不能是根目录")
	}
	return Layout{Root: cleaned}, nil
}

// Path 返回工作空间内的子路径。
//
// 参数可以是相对片段，也可以是已经位于工作空间内的绝对路径；后者直接作为新的
// 基准，避免把工作空间根目录重复拼接。
func (l Layout) Path(elements ...string) string {
	joined := l.Root
	for _, element := range elements {
		trimmed := strings.TrimSpace(element)
		if trimmed == "" {
			continue
		}
		if trimmed == l.Root || strings.HasPrefix(trimmed, l.Root+"/") {
			joined = path.Clean(trimmed)
			continue
		}
		joined = path.Join(joined, strings.TrimPrefix(trimmed, "/"))
	}
	return joined
}

// EnvironmentsRoot 返回环境目录。
func (l Layout) EnvironmentsRoot() string {
	return l.Path(DirectoryEnvironments)
}

// EnvironmentDir 返回指定环境的目录。
//
// 基础环境为 environments/base；语言环境为 environments/languages/<name>。
func (l Layout) EnvironmentDir(ref domain.EnvironmentRef) string {
	if ref.IsBase() {
		return l.Path(DirectoryEnvironments, ref.Name)
	}
	return l.Path(DirectoryEnvironments, "languages", ref.Name)
}

// VersionsRoot 返回指定环境的历史版本根目录。
func (l Layout) VersionsRoot(ref domain.EnvironmentRef) string {
	return l.Path(l.EnvironmentDir(ref), VersionDirectoryName)
}

// VersionDir 返回指定环境版本的目录。
func (l Layout) VersionDir(ref domain.EnvironmentRef, number int) string {
	return l.Path(l.VersionsRoot(ref), domain.FormatVersionLabel(number))
}

// DockerfilePath 返回指定版本目录下的 Dockerfile 路径。
func (l Layout) DockerfilePath(ref domain.EnvironmentRef, number int) string {
	return l.Path(l.VersionDir(ref, number), DockerfileName)
}

// ManifestPath 返回指定版本目录下的清单路径。
func (l Layout) ManifestPath(ref domain.EnvironmentRef, number int) string {
	return l.Path(l.VersionDir(ref, number), ManifestFileName)
}

// LanguageManifestPath 返回语言环境定义文件路径。
func (l Layout) LanguageManifestPath(ref domain.EnvironmentRef) string {
	return l.Path(l.EnvironmentDir(ref), LanguageManifestFileName)
}

// ProjectsRoot 返回项目配置目录。
func (l Layout) ProjectsRoot() string {
	return l.Path(DirectoryProjects)
}

// ProjectDir 返回指定项目的配置目录。
//
// 该目录只保存非机密元数据，项目源代码必须留在任务容器内部。
func (l Layout) ProjectDir(projectID string) string {
	return l.Path(DirectoryProjects, projectID)
}

// ProjectConfigPath 返回项目配置文件路径。
func (l Layout) ProjectConfigPath(projectID string) string {
	return l.Path(l.ProjectDir(projectID), "project.json")
}

// ImagesManifestPath 返回镜像元数据清单路径。
func (l Layout) ImagesManifestPath() string {
	return l.Path(DirectoryImages, "manifest.json")
}

// ContainersRegistryPath 返回容器登记清单路径。
func (l Layout) ContainersRegistryPath() string {
	return l.Path(DirectoryContainers, "registry.json")
}

// BuildDir 返回指定构建的日志目录。
func (l Layout) BuildDir(buildID string) string {
	return l.Path(DirectoryBuilds, buildID)
}

// BuildLogPath 返回指定构建的日志文件路径。
func (l Layout) BuildLogPath(buildID string) string {
	return l.Path(l.BuildDir(buildID), "build.log")
}

// InitialDirectories 返回初始化工作空间时需要创建的目录。
//
// 只为已实现的能力创建目录，不提前生成没有内容的空文件。
func (l Layout) InitialDirectories() []string {
	return []string{
		l.EnvironmentsRoot(),
		l.ProjectsRoot(),
		l.Path(DirectoryImages),
		l.Path(DirectoryContainers),
		l.Path(DirectoryBuilds),
	}
}
