package execenv

import (
	"context"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// PrepareSpec 是一次执行环境准备的输入。
//
// 所有标识都来自已经校验过的数据库记录：调用方不能指定容器名称、镜像引用、
// 远程路径或 Docker 参数，这些都由服务端按项目与环境配置决定。
type PrepareSpec struct {
	// HostID 是目标执行节点。
	HostID string
	// Environment 是任务使用的语言环境。
	Environment domain.EnvironmentRef
	// VersionNumber 是锁定的环境版本号，0 表示当前可用版本。
	VersionNumber int
	// ProjectID 是可选的项目标识，用于缓存共享范围与管理目录命名。
	ProjectID string
	// Caches 是允许挂载的缓存类型；空值表示不挂载缓存。
	Caches []string
	// WorkDir 是容器内项目目录，空值使用 DefaultProjectDir。
	WorkDir string
	// SessionDir 是容器内临时 Session 目录，空值使用 DefaultSessionDir。
	SessionDir string
	// TaskID 是本次执行所属任务，可为空。
	TaskID *domain.ID
	// AttemptID 是本次执行所属尝试，可为空。
	AttemptID *domain.ID
	// SkipRunContractCheck 跳过镜像常驻运行契约预检。
	//
	// 只允许在已确认镜像可用时使用，例如复用刚刚核验过的镜像。
	SkipRunContractCheck bool
}

// Validate 校验准备请求。
func (s PrepareSpec) Validate() error {
	if err := domain.ID(strings.TrimSpace(s.HostID)).Validate(); err != nil {
		return wrap(KindRemoteContainer, "validate", "执行节点标识不合法: "+err.Error(), err)
	}
	if err := s.Environment.Validate(); err != nil {
		return wrap(KindRemoteContainer, "validate", err.Error(), err)
	}
	if s.Environment.IsBase() {
		return wrap(
			KindRemoteContainer,
			"validate",
			"任务容器必须使用语言环境，基础环境只作为语言环境的构建基础",
			ErrInvalidValue,
		)
	}
	if s.VersionNumber < 0 {
		return wrap(KindRemoteContainer, "validate", "环境版本号不能为负数", ErrInvalidValue)
	}
	if s.TaskID != nil {
		if err := s.TaskID.Validate(); err != nil {
			return wrap(KindRemoteContainer, "validate", "任务标识不合法: "+err.Error(), err)
		}
	}
	if s.AttemptID != nil {
		if err := s.AttemptID.Validate(); err != nil {
			return wrap(KindRemoteContainer, "validate", "尝试标识不合法: "+err.Error(), err)
		}
		if s.TaskID == nil {
			return wrap(
				KindRemoteContainer,
				"validate",
				"关联尝试时必须同时指定任务",
				ErrInvalidValue,
			)
		}
	}
	if _, err := NewWorkspace(s.WorkDir, s.SessionDir, nil); err != nil {
		return wrap(KindRemoteContainer, "validate", err.Error(), err)
	}
	for _, cacheType := range s.Caches {
		if !domain.EnvironmentNamePattern.MatchString(strings.TrimSpace(cacheType)) {
			return wrap(
				KindRemoteContainer,
				"validate",
				"缓存类型书写非法: "+cacheType,
				ErrInvalidValue,
			)
		}
	}
	return nil
}

// Step 是准备流程的阶段名。
//
// 阶段划分是诊断契约的一部分：失败信息必须说明停在哪一步，使"镜像没构建成功"
// 与"容器没启动起来"可以被区分。
type Step string

// 准备阶段取值。
const (
	// StepValidate 校验机器与项目配置。
	StepValidate Step = "validate"
	// StepImage 准备基础镜像。
	StepImage Step = "image"
	// StepCreate 创建任务容器。
	StepCreate Step = "create"
	// StepStart 启动任务容器。
	StepStart Step = "start"
	// StepVerify 验证容器可用性。
	StepVerify Step = "verify"
)

// String 返回阶段名的字符串形式。
func (s Step) String() string {
	return string(s)
}

// StepResult 是单个准备阶段的执行结果。
type StepResult struct {
	// Step 是阶段名。
	Step Step
	// Started 是阶段开始时间。
	Started time.Time
	// FinishedAt 是阶段结束时间。
	FinishedAt time.Time
	// Detail 是该阶段的关键事实，例如镜像 ID、容器 ID、工作目录。
	Detail string
	// Err 是失败原因，成功时为 nil。
	Err error
}

// Succeeded 判断该阶段是否成功。
func (r StepResult) Succeeded() bool {
	return r.Err == nil
}

// Duration 返回阶段耗时。
func (r StepResult) Duration() time.Duration {
	if r.FinishedAt.Before(r.Started) {
		return 0
	}
	return r.FinishedAt.Sub(r.Started)
}

// Prepared 是一次执行环境准备的完整结果。
//
// 环境与阶段记录分离：即使准备失败也会返回已经执行过的阶段，使失败原因可追溯；
// Environment 只在全部阶段成功时才可用。
type Prepared struct {
	// Environment 是准备好的执行环境，失败时为空值。
	Environment Environment
	// Management 是远程管理工作区描述，失败时可能为空值。
	Management ManagementWorkspace
	// Steps 是按执行顺序排列的阶段结果。
	Steps []StepResult
	// ImageRef 是使用的镜像引用。
	ImageRef string
	// ImageID 是实际镜像 ID。
	ImageID string
	// ImageCached 表示复用了已存在的镜像。
	ImageCached bool
	// VersionLabel 是镜像对应的环境版本标签，例如 v0001。
	VersionLabel string
	// BuildLogPath 是远程构建日志路径，未构建时为空。
	BuildLogPath string
	// StartedAt 是准备开始时间。
	StartedAt time.Time
	// FinishedAt 是准备结束时间。
	FinishedAt time.Time
	// Err 是失败原因，成功时为 nil。
	Err error
}

// Succeeded 判断准备是否成功。
func (p Prepared) Succeeded() bool {
	return p.Err == nil && p.Environment.Container().ContainerID != ""
}

// Duration 返回准备总耗时。
func (p Prepared) Duration() time.Duration {
	if p.FinishedAt.Before(p.StartedAt) {
		return 0
	}
	return p.FinishedAt.Sub(p.StartedAt)
}

// FailedStep 返回第一个失败阶段，全部成功时返回空字符串。
func (p Prepared) FailedStep() Step {
	for _, step := range p.Steps {
		if !step.Succeeded() {
			return step.Step
		}
	}
	return ""
}

// Prepared 返回执行环境本身。
//
// 该方法在准备失败时返回错误：调用方不得使用不完整的环境执行命令。
func (p Prepared) Require() (Environment, error) {
	if p.Err != nil {
		return Environment{}, p.Err
	}
	if err := p.Environment.Validate(); err != nil {
		return Environment{}, wrap(KindRemoteContainer, "prepare", "执行环境不完整: "+err.Error(), err)
	}
	return p.Environment, nil
}

// Provider 是统一执行环境提供者。
//
// 实现必须：
//   - 只在受管理的任务容器内执行命令，不得回退到服务进程的当前目录；
//   - 把超时与取消传播到实际远程命令；
//   - 分别返回标准输出、标准错误与退出码；
//   - 在释放资源时区分"删除容器"与"保留现场"，并说明释放了哪些资源。
//
// 具体实现位于 app 层；本接口是 Pi Runner、验证引擎与工作流唯一可见的执行能力。
type Provider interface {
	// Kind 返回该提供者实现的执行环境类型。
	Kind() Kind
	// Prepare 准备执行环境：校验配置、准备镜像、创建并核验容器。
	Prepare(ctx context.Context, spec PrepareSpec) (Prepared, error)
	// Status 查询执行环境当前状态，不修改任何资源。
	Status(ctx context.Context, environment Environment) (Status, error)
	// Exec 在环境内执行一条命令。
	Exec(ctx context.Context, environment Environment, spec ExecSpec) (ExecResult, error)
	// Release 按给定处置方式释放执行环境拥有的资源。
	Release(ctx context.Context, environment Environment, request ReleaseRequest) (ReleaseResult, error)
}
