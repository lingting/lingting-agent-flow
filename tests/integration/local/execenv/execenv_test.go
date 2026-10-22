// Package execenv_local 提供统一执行环境在容器生命周期与工作区隔离方面的本机测试。
//
// 用例使用受控的镜像与容器替身：不访问真实 Docker Engine、SSH 或远程机器，
// 因此可以在任意开发机上完整验证
//   - 准备流程的四个阶段与失败定位；
//   - 容器创建、启动、核验与释放的资源处置；
//   - 容器内项目目录与远程管理工作区的边界；
//   - 命令执行的工作目录、参数与环境变量传递；
//   - 超时、取消与执行层错误不会产生虚假的成功结果。
package execenv_local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/execenv"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 测试使用的固定标识。
const (
	testHostID      = "host-1"
	testProjectID   = "demo-project"
	testContainerID = "container-1"
	testDockerID    = "docker-1"
	testImageRef    = "agentflow-language-go:v0001-0123456789ab"
	testImageID     = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testWorkspace   = "/home/deploy/.lingting/agentflow"
)

// fakeImages 是镜像能力的受控替身。
type fakeImages struct {
	mu sync.Mutex
	// preflightErr 使远程 Docker 预检失败。
	preflightErr error
	// workspaceRoot 覆盖预检返回的工作区路径。
	workspaceRoot string
	// buildErr 使镜像准备失败。
	buildErr error
	// version 覆盖构建返回的版本。
	version domain.EnvironmentVersion
	// cached 表示复用已有构建。
	cached bool
	// resolveErr 使版本解析失败。
	resolveErr error
	// builds 记录收到的构建请求。
	builds []app.BuildRequest
}

// Preflight 返回受控的预检结果。
func (f *fakeImages) Preflight(
	ctx context.Context,
	options app.InitOptions,
) (app.InitResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.preflightErr != nil {
		return app.InitResult{}, f.preflightErr
	}
	root := f.workspaceRoot
	if root == "" {
		root = testWorkspace
	}
	return app.InitResult{
		Host:          domain.ExecutionHost{ID: domain.ID(options.MachineID)},
		WorkspaceRoot: root,
		Preflight: docker.Preflight{
			SSHOK:             true,
			CLIPresent:        true,
			CLIVersion:        docker.MinimumCLIVersion,
			EngineReachable:   true,
			EngineVersion:     docker.MinimumEngineVersion,
			APIVersion:        docker.EngineAPIBaseVersion,
			MinAPIVersion:     "1.24",
			APIVersionCheckOK: true,
			PermissionOK:      true,
			WorkspaceWritable: true,
			ErrorCode:         docker.CodeOK,
		},
	}, nil
}

// Build 返回受控的镜像准备结果。
func (f *fakeImages) Build(
	ctx context.Context,
	request app.BuildRequest,
) (app.BuildResult, error) {
	f.mu.Lock()
	f.builds = append(f.builds, request)
	buildErr := f.buildErr
	version := f.version
	cached := f.cached
	f.mu.Unlock()
	if buildErr != nil {
		return app.BuildResult{}, buildErr
	}
	if version.Ref.IsZero() {
		version = mustVersion(request.Ref)
	}
	return app.BuildResult{Version: version, Cached: cached}, nil
}

// ResolveVersion 返回受控的版本解析结果。
func (f *fakeImages) ResolveVersion(
	ctx context.Context,
	hostID domain.ID,
	ref domain.EnvironmentRef,
	number int,
) (domain.EnvironmentVersion, error) {
	if f.resolveErr != nil {
		return domain.EnvironmentVersion{}, f.resolveErr
	}
	return mustVersion(ref), nil
}

// mustVersion 构造一个可用于创建容器的环境版本。
func mustVersion(ref domain.EnvironmentRef) domain.EnvironmentVersion {
	if ref.IsZero() {
		language, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
		if err != nil {
			panic("测试固定环境名必须合法: " + err.Error())
		}
		ref = language
	}
	builtAt := time.Now().UTC()
	return domain.EnvironmentVersion{
		HostID:            testHostID,
		Ref:               ref,
		Number:            1,
		DockerfileSHA256:  strings.Repeat("a", 64),
		BuildConfigSHA256: strings.Repeat("b", 64),
		ContextSHA256:     strings.Repeat("c", 64),
		ParentVersion:     "v0001",
		ImageRef:          testImageRef,
		ImageID:           testImageID,
		Status:            domain.BuildStatusSucceeded,
		CreatedAt:         builtAt,
		BuiltAt:           &builtAt,
	}
}

// fakeContainers 是容器能力的受控替身。
type fakeContainers struct {
	mu sync.Mutex
	// createErr 使容器创建失败。
	createErr error
	// running 表示核验结果中的容器是否在运行。
	running bool
	// detached 表示容器已脱离管理。
	detached bool
	// removed 表示容器已被删除。
	removed bool
	// inspectErr 使容器核验失败。
	inspectErr error
	// deleteErr 使删除失败。
	deleteErr error
	// detachErr 使脱离管理失败。
	detachErr error
	// execReply 是命令执行的受控结果。
	execReply docker.CommandResult
	// execErr 使命令执行返回错误。
	execErr error
	// created 记录收到的创建请求。
	created []app.CreateContainerRequest
	// execs 记录收到的执行请求。
	execs []app.ExecRequest
	// deleted 记录删除请求。
	deleted []app.DeleteContainerRequest
	// detachedRequests 记录脱离管理请求。
	detachedRequests []app.DetachContainerRequest
	// container 是当前容器记录。
	container domain.Container
}

// CreateContainer 创建并返回受控容器。
func (f *fakeContainers) CreateContainer(
	ctx context.Context,
	request app.CreateContainerRequest,
) (app.ContainerDetail, error) {
	f.mu.Lock()
	f.created = append(f.created, request)
	createErr := f.createErr
	f.mu.Unlock()
	if createErr != nil {
		return app.ContainerDetail{}, createErr
	}
	container, err := newContainerRecord(request)
	if err != nil {
		return app.ContainerDetail{}, err
	}
	f.mu.Lock()
	f.container = container
	f.running = true
	f.mu.Unlock()
	return app.ContainerDetail{
		Container: container,
		Inspect:   &docker.ContainerInspect{ID: testDockerID, Running: true},
	}, nil
}

// newContainerRecord 构造与创建请求一致的容器记录。
func newContainerRecord(request app.CreateContainerRequest) (domain.Container, error) {
	container, err := domain.NewContainer(
		testContainerID,
		testHostID,
		"agentflow-abcdef123456",
		request.Ref,
		domain.FormatVersionLabel(maxInt(request.VersionNumber, 1)),
		strings.Repeat("b", 64),
		testImageID,
		time.Now(),
	)
	if err != nil {
		return domain.Container{}, err
	}
	container.TaskID = request.TaskID
	container.AttemptID = request.AttemptID
	container.DockerID = testDockerID
	container.State = domain.ContainerStateRunning
	container.DockerState = domain.DockerStateRunning
	return container, nil
}

// maxInt 返回两个整数中的较大值。
func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// InspectContainer 返回受控的容器状态。
func (f *fakeContainers) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspectErr != nil {
		return app.ContainerDetail{}, f.inspectErr
	}
	container := f.container
	if container.ID.IsEmpty() {
		container = domain.Container{
			ID:       domain.ID(request.ContainerID),
			DockerID: testDockerID,
			Name:     "agentflow-abcdef123456",
		}
	}
	container.Detached = f.detached
	if f.removed {
		container.State = domain.ContainerStateRemoved
	}
	return app.ContainerDetail{
		Container: container,
		Inspect: &docker.ContainerInspect{
			ID:      testDockerID,
			Running: f.running && !f.removed,
		},
	}, nil
}

// Exec 返回受控的命令执行结果。
func (f *fakeContainers) Exec(
	ctx context.Context,
	request app.ExecRequest,
) (app.ExecResult, error) {
	f.mu.Lock()
	f.execs = append(f.execs, request)
	reply := f.execReply
	execErr := f.execErr
	f.mu.Unlock()
	if execErr != nil {
		// 执行层不可用时命令是否执行过无法确认：结果必须标记为不确定，
		// 调用方不得据此判定成功。
		return app.ExecResult{Result: docker.CommandResult{
			ExitCode: -1,
			Status:   sshclient.CommandStatusConnectionError,
			Started:  false,
			Certain:  false,
		}}, execErr
	}
	return app.ExecResult{Result: reply}, nil
}

// DeleteContainer 记录删除请求并返回已删除记录。
func (f *fakeContainers) DeleteContainer(
	ctx context.Context,
	request app.DeleteContainerRequest,
) (app.ContainerDetail, error) {
	f.mu.Lock()
	f.deleted = append(f.deleted, request)
	deleteErr := f.deleteErr
	container := f.container
	f.mu.Unlock()
	if deleteErr != nil {
		return app.ContainerDetail{Container: container}, deleteErr
	}
	f.mu.Lock()
	f.removed = true
	f.container = container
	f.mu.Unlock()
	container.State = domain.ContainerStateRemoved
	container.Detached = false
	return app.ContainerDetail{Container: container}, nil
}

// DetachContainer 记录脱离管理请求。
func (f *fakeContainers) DetachContainer(
	ctx context.Context,
	request app.DetachContainerRequest,
) (app.ContainerDetail, error) {
	f.mu.Lock()
	f.detachedRequests = append(f.detachedRequests, request)
	detachErr := f.detachErr
	container := f.container
	f.mu.Unlock()
	if detachErr != nil {
		return app.ContainerDetail{Container: container}, detachErr
	}
	container.Detached = true
	container.State = domain.ContainerStateDetached
	f.mu.Lock()
	f.detached = true
	f.container = container
	f.mu.Unlock()
	return app.ContainerDetail{Container: container}, nil
}

// LoadContainer 返回当前容器记录。
func (f *fakeContainers) LoadContainer(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.container.ID.IsEmpty() {
		return domain.Container{}, errors.New("容器记录不存在")
	}
	return f.container, nil
}

// LoadHost 返回受控的执行节点。
func (f *fakeContainers) LoadHost(
	ctx context.Context,
	hostID string,
) (domain.ExecutionHost, error) {
	return domain.ExecutionHost{
		ID:               domain.ID(hostID),
		Name:             "测试节点",
		Host:             "127.0.0.1",
		Port:             22,
		Username:         "deploy",
		WorkspaceRoot:    "~/.lingting/agentflow",
		Enabled:          true,
		ValidationStatus: domain.ValidationStatusNormal,
	}, nil
}

// fixture 是一套可直接发起准备与执行的测试数据。
type fixture struct {
	images     *fakeImages
	containers *fakeContainers
	service    *app.ExecutionEnvironmentService
	workspace  *testkit.Workspace
}

// newFixture 构造执行环境服务与受控替身。
func newFixture(t *testing.T) *fixture {
	t.Helper()
	workspace := testkit.New(t)
	images := &fakeImages{}
	// 默认核验命令成功：准备流程会探测工作目录与 git / pi 入口。
	containers := &fakeContainers{execReply: successfulExec()}
	service, err := app.NewExecutionEnvironmentService(app.ExecutionEnvironmentOptions{
		Environments:  &app.EnvironmentService{},
		Containers:    &app.ContainerService{},
		Images:        images,
		ContainerPort: containers,
		Tasks:         &fakeTaskStore{},
	})
	if err != nil {
		t.Fatalf("构造执行环境服务失败: %v", err)
	}
	return &fixture{
		images:     images,
		containers: containers,
		service:    service,
		workspace:  workspace,
	}
}

// successfulExec 返回一条"命令成功结束"的受控结果。
func successfulExec() docker.CommandResult {
	return docker.CommandResult{
		ExitCode: 0,
		Status:   sshclient.CommandStatusSucceeded,
		Started:  true,
		Certain:  true,
	}
}

// fakeTaskStore 是任务记录的受控替身。
type fakeTaskStore struct {
	mu sync.Mutex
	// task 是当前任务记录。
	task domain.Task
	// attempt 是当前尝试记录。
	attempt domain.Attempt
	// updateErr 使任务写入失败。
	updateErr error
	// appendErr 使事件写入失败。
	appendErr error
	// updated 记录写入的任务。
	updated []domain.Task
	// events 记录写入的事件。
	events []domain.Event
}

// GetTask 返回当前任务记录。
func (f *fakeTaskStore) GetTask(ctx context.Context, id domain.ID) (*domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.task.ID.IsEmpty() {
		return nil, errors.New("任务不存在")
	}
	task := f.task
	return &task, nil
}

// GetAttempt 返回当前尝试记录。
func (f *fakeTaskStore) GetAttempt(
	ctx context.Context,
	id domain.ID,
) (*domain.Attempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attempt.ID.IsEmpty() || f.attempt.ID != id {
		return nil, errors.New("尝试不存在")
	}
	attempt := f.attempt
	return &attempt, nil
}

// UpdateTaskIfStatus 记录任务写入。
func (f *fakeTaskStore) UpdateTaskIfStatus(
	ctx context.Context,
	task domain.Task,
	expected []domain.TaskStatus,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return f.updateErr
	}
	f.task = task
	f.updated = append(f.updated, task)
	return nil
}

// AppendEvent 记录写入的事件。
//
// 执行环境用它记录"容器已创建但未写回任务"的现场，因此替身必须能收到该事件，
// 使"是否留下可追溯记录"可以直接断言。
func (f *fakeTaskStore) AppendEvent(
	ctx context.Context,
	event domain.Event,
) (domain.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appendErr != nil {
		return domain.Event{}, f.appendErr
	}
	f.events = append(f.events, event)
	return event, nil
}

// prepareSpec 返回一个合法的准备请求。
func prepareSpec(t *testing.T) execenv.PrepareSpec {
	t.Helper()
	language, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境失败: %v", err)
	}
	return execenv.PrepareSpec{
		HostID:      testHostID,
		Environment: language,
		ProjectID:   testProjectID,
	}
}

func TestPrepareSucceedsAndRecordsStages(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	prepared, err := fixture.service.Prepare(context.Background(), prepareSpec(t))
	if err != nil {
		t.Fatalf("准备执行环境失败: %v", err)
	}
	if !prepared.Succeeded() {
		t.Fatal("准备成功时必须报告成功")
	}
	steps := make([]string, 0, len(prepared.Steps))
	for _, step := range prepared.Steps {
		if !step.Succeeded() {
			t.Fatalf("阶段 %s 不应失败: %v", step.Step, step.Err)
		}
		steps = append(steps, step.Step.String())
	}
	want := []string{
		execenv.StepValidate.String(),
		execenv.StepImage.String(),
		execenv.StepCreate.String(),
		execenv.StepStart.String(),
		execenv.StepVerify.String(),
	}
	if strings.Join(steps, ",") != strings.Join(want, ",") {
		t.Fatalf("准备阶段顺序不符: 期望 %v，实际 %v", want, steps)
	}
	environment, err := prepared.Require()
	if err != nil {
		t.Fatalf("准备成功后应返回可用环境: %v", err)
	}
	if environment.WorkDir() != execenv.DefaultProjectDir {
		t.Fatalf("默认项目目录应为 %s，实际 %s", execenv.DefaultProjectDir, environment.WorkDir())
	}
	// 管理工作区位于远程宿主机，且与容器内项目目录不同。
	if prepared.Management.Root != testWorkspace {
		t.Fatalf("管理工作区应为 %s，实际 %s", testWorkspace, prepared.Management.Root)
	}
	if !strings.HasSuffix(prepared.Management.Project, "/projects/"+testProjectID) {
		t.Fatalf("项目管理工作目录不符: %s", prepared.Management.Project)
	}
	if prepared.Management.ContainsSource() {
		t.Fatal("管理工作区不得包含项目源代码")
	}
	if len(fixture.containers.created) != 1 {
		t.Fatalf("准备流程必须只创建一个容器，实际 %d", len(fixture.containers.created))
	}
	created := fixture.containers.created[0]
	if created.WorkDir != execenv.DefaultProjectDir {
		t.Fatalf("容器内工作目录应为 %s，实际 %s", execenv.DefaultProjectDir, created.WorkDir)
	}
	if created.ProjectID != testProjectID {
		t.Fatalf("项目标识未传递到容器创建: %s", created.ProjectID)
	}
	// 可用性核验必须在容器内执行，而不是在服务进程目录执行。
	if len(fixture.containers.execs) == 0 {
		t.Fatal("准备流程必须核验容器内执行入口")
	}
	for _, request := range fixture.containers.execs {
		if request.WorkDir != execenv.DefaultProjectDir {
			t.Fatalf("核验命令工作目录应为 %s，实际 %s", execenv.DefaultProjectDir, request.WorkDir)
		}
	}
}

func TestPrepareFailsWhenImagePreparationFails(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	fixture.images.buildErr = errors.New("镜像构建失败: 网络不可达")
	prepared, err := fixture.service.Prepare(context.Background(), prepareSpec(t))
	if err == nil {
		t.Fatal("镜像准备失败时必须返回错误")
	}
	if prepared.Succeeded() {
		t.Fatal("镜像准备失败时不得报告成功")
	}
	if prepared.FailedStep() != execenv.StepImage {
		t.Fatalf("失败阶段应为 image，实际 %s", prepared.FailedStep())
	}
	if len(fixture.containers.created) != 0 {
		t.Fatal("镜像准备失败时不得创建容器")
	}
	if !strings.Contains(err.Error(), "镜像构建失败") {
		t.Fatalf("错误信息应保留真实构建原因: %v", err)
	}
}

func TestPrepareFailsWhenContainerCreationFails(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	fixture.containers.createErr = errors.New("docker create 失败")
	prepared, err := fixture.service.Prepare(context.Background(), prepareSpec(t))
	if err == nil {
		t.Fatal("容器创建失败时必须返回错误")
	}
	if prepared.FailedStep() != execenv.StepCreate {
		t.Fatalf("失败阶段应为 create，实际 %s", prepared.FailedStep())
	}
	if _, requireErr := prepared.Require(); requireErr == nil {
		t.Fatal("容器创建失败时不得返回可用环境")
	}
}

func TestPrepareFailsWhenContainerNotRunning(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	// 容器创建后立刻被判定为未运行：准备流程必须拒绝把这种环境当作可用。
	fixture.containers.createErr = nil
	service := fixture.service
	prepared, err := service.Prepare(context.Background(), prepareSpec(t))
	if err != nil {
		t.Fatalf("准备执行环境失败: %v", err)
	}
	// 手动把容器置为未运行后重新核验状态。
	fixture.containers.mu.Lock()
	fixture.containers.running = false
	fixture.containers.mu.Unlock()
	status, statusErr := service.Status(context.Background(), prepared.Environment)
	if statusErr != nil {
		t.Fatalf("查询状态失败: %v", statusErr)
	}
	if status != execenv.StatusNotRunning {
		t.Fatalf("容器未运行时状态应为 not_running，实际 %s", status)
	}
	if status.Usable() {
		t.Fatal("未运行的容器不得被当作可执行状态")
	}
}

func TestStatusDistinguishesReleasedAndDetached(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	prepared, err := fixture.service.Prepare(context.Background(), prepareSpec(t))
	if err != nil {
		t.Fatalf("准备执行环境失败: %v", err)
	}
	fixture.containers.mu.Lock()
	fixture.containers.removed = true
	fixture.containers.mu.Unlock()
	status, err := fixture.service.Status(context.Background(), prepared.Environment)
	if err != nil {
		t.Fatalf("查询状态失败: %v", err)
	}
	if status != execenv.StatusReleased {
		t.Fatalf("已删除容器状态应为 released，实际 %s", status)
	}
	fixture.containers.mu.Lock()
	fixture.containers.removed = false
	fixture.containers.detached = true
	fixture.containers.mu.Unlock()
	status, err = fixture.service.Status(context.Background(), prepared.Environment)
	if err != nil {
		t.Fatalf("查询状态失败: %v", err)
	}
	if status != execenv.StatusNotRunning {
		t.Fatalf("已脱离管理的容器不得被视为可执行，实际 %s", status)
	}
}

func TestStatusReportsUnknownOnInspectFailure(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	prepared, err := fixture.service.Prepare(context.Background(), prepareSpec(t))
	if err != nil {
		t.Fatalf("准备执行环境失败: %v", err)
	}
	fixture.containers.mu.Lock()
	fixture.containers.inspectErr = errors.New("SSH 连接中断")
	fixture.containers.mu.Unlock()
	status, err := fixture.service.Status(context.Background(), prepared.Environment)
	if err == nil {
		t.Fatal("核验失败时必须返回错误")
	}
	if status != execenv.StatusUnknown {
		t.Fatalf("核验失败时状态应为 unknown，实际 %s", status)
	}
}

func TestExecPassesWorkDirArgsAndEnv(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	prepared, err := fixture.service.Prepare(context.Background(), prepareSpec(t))
	if err != nil {
		t.Fatalf("准备执行环境失败: %v", err)
	}
	fixture.containers.mu.Lock()
	fixture.containers.execReply = docker.CommandResult{
		ExitCode: 0,
		Stdout:   "ok\n",
		Status:   sshclient.CommandStatusSucceeded,
		Started:  true,
		Certain:  true,
	}
	fixture.containers.mu.Unlock()
	result, err := fixture.service.Exec(context.Background(), prepared.Environment, execenv.ExecSpec{
		Command: "go",
		Args:    []string{"test", "./..."},
		WorkDir: "/workspace/project/sub",
		Env:     []string{"CI=1"},
		Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("执行命令失败: %v", err)
	}
	if !result.Succeeded() {
		t.Fatalf("退出码 0 且结果可确认时应判定成功: %+v", result)
	}
	if result.Stdout != "ok\n" {
		t.Fatalf("标准输出未透传: %q", result.Stdout)
	}
	request := fixture.containers.execs[len(fixture.containers.execs)-1]
	if got := strings.Join(request.Command, " "); got != "go test ./..." {
		t.Fatalf("参数未逐项传递: %s", got)
	}
	if request.WorkDir != "/workspace/project/sub" {
		t.Fatalf("工作目录未透传: %s", request.WorkDir)
	}
	if len(request.Env) != 1 || request.Env[0] != "CI=1" {
		t.Fatalf("环境变量未透传: %v", request.Env)
	}
}

func TestExecRejectsWorkDirOutsideWorkspace(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	prepared, err := fixture.service.Prepare(context.Background(), prepareSpec(t))
	if err != nil {
		t.Fatalf("准备执行环境失败: %v", err)
	}
	before := len(fixture.containers.execs)
	if _, err := fixture.service.Exec(context.Background(), prepared.Environment, execenv.ExecSpec{
		Command: "ls",
		WorkDir: "/etc",
	}); err == nil {
		t.Fatal("容器外工作目录必须被拒绝")
	}
	if len(fixture.containers.execs) != before {
		t.Fatal("工作目录非法时不得发出任何容器命令")
	}
}

func TestExecPropagatesTimeoutAndCancellation(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	prepared, err := fixture.service.Prepare(context.Background(), prepareSpec(t))
	if err != nil {
		t.Fatalf("准备执行环境失败: %v", err)
	}
	fixture.containers.mu.Lock()
	fixture.containers.execReply = docker.CommandResult{
		ExitCode: -1,
		Status:   sshclient.CommandStatusTimeout,
		Started:  true,
		Certain:  false,
	}
	fixture.containers.mu.Unlock()
	result, _ := fixture.service.Exec(context.Background(), prepared.Environment, execenv.ExecSpec{
		Command: "sleep",
		Args:    []string{"600"},
	})
	if !result.TimedOut() {
		t.Fatalf("超时必须被识别，实际 %s", result.Status)
	}
	if result.Succeeded() {
		t.Fatal("超时不得判定成功")
	}
	fixture.containers.mu.Lock()
	fixture.containers.execReply = docker.CommandResult{
		ExitCode: -1,
		Status:   sshclient.CommandStatusCancelled,
		Started:  true,
		Certain:  false,
	}
	fixture.containers.mu.Unlock()
	result, _ = fixture.service.Exec(context.Background(), prepared.Environment, execenv.ExecSpec{
		Command: "sleep",
		Args:    []string{"600"},
	})
	if !result.Cancelled() {
		t.Fatalf("取消必须被识别，实际 %s", result.Status)
	}
	if result.Succeeded() {
		t.Fatal("取消不得判定成功")
	}
}

func TestExecReportsExecutionLayerError(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	prepared, err := fixture.service.Prepare(context.Background(), prepareSpec(t))
	if err != nil {
		t.Fatalf("准备执行环境失败: %v", err)
	}
	fixture.containers.mu.Lock()
	fixture.containers.execErr = errors.New("SSH 连接不可用")
	fixture.containers.mu.Unlock()
	result, err := fixture.service.Exec(context.Background(), prepared.Environment, execenv.ExecSpec{
		Command: "ls",
	})
	if err == nil {
		t.Fatal("执行层错误必须返回")
	}
	if result.Succeeded() {
		t.Fatal("执行层错误时不得产生虚假的成功结果")
	}
}

func TestReleaseDeleteRemovesContainer(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	prepared, err := fixture.service.Prepare(context.Background(), prepareSpec(t))
	if err != nil {
		t.Fatalf("准备执行环境失败: %v", err)
	}
	result, err := fixture.service.Release(
		context.Background(),
		prepared.Environment,
		execenv.ReleaseRequest{Outcome: domain.ContainerOutcomeSucceeded},
	)
	if err != nil {
		t.Fatalf("释放执行环境失败: %v", err)
	}
	if result.Retention != execenv.RetentionDelete {
		t.Fatalf("任务成功时应删除容器，实际 %s", result.Retention)
	}
	if !result.ContainerRemoved {
		t.Fatal("删除后必须报告容器已删除")
	}
	if len(fixture.containers.deleted) != 1 {
		t.Fatalf("必须只删除一次，实际 %d", len(fixture.containers.deleted))
	}
	if fixture.containers.deleted[0].Reason != domain.ContainerEndReasonTaskSucceeded {
		t.Fatalf("结束原因应为 task_succeeded，实际 %s", fixture.containers.deleted[0].Reason)
	}
}

func TestReleaseKeepsContainerOnFailure(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	prepared, err := fixture.service.Prepare(context.Background(), prepareSpec(t))
	if err != nil {
		t.Fatalf("准备执行环境失败: %v", err)
	}
	result, err := fixture.service.Release(
		context.Background(),
		prepared.Environment,
		execenv.ReleaseRequest{Outcome: domain.ContainerOutcomeFailed},
	)
	if err != nil {
		t.Fatalf("释放执行环境失败: %v", err)
	}
	if result.Retention != execenv.RetentionKeep {
		t.Fatalf("任务失败时应保留容器，实际 %s", result.Retention)
	}
	if !result.ContainerRetained {
		t.Fatal("保留时必须报告容器已保留")
	}
	if len(fixture.containers.deleted) != 0 {
		t.Fatal("任务失败时不得删除容器")
	}
	if len(fixture.containers.detachedRequests) != 0 {
		t.Fatal("任务失败时不得脱离管理")
	}
}

func TestReleaseDetachOnInterruptedKeepsControl(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	prepared, err := fixture.service.Prepare(context.Background(), prepareSpec(t))
	if err != nil {
		t.Fatalf("准备执行环境失败: %v", err)
	}
	result, err := fixture.service.Release(context.Background(), prepared.Environment, execenv.ReleaseRequest{
		Retention: execenv.RetentionDetach,
		Outcome:   domain.ContainerOutcomeInterrupted,
	})
	if err != nil {
		t.Fatalf("脱离管理失败: %v", err)
	}
	if !result.ContainerRetained {
		t.Fatal("脱离管理必须保留容器")
	}
	if result.ContainerRemoved {
		t.Fatal("脱离管理不得删除容器")
	}
	// 已脱离管理的容器不得再被自动删除。
	if _, err := fixture.service.Release(context.Background(), prepared.Environment, execenv.ReleaseRequest{
		Retention: execenv.RetentionDelete,
		Outcome:   domain.ContainerOutcomeSucceeded,
	}); err == nil {
		t.Fatal("已脱离管理的容器不得被自动删除")
	}
}

func TestReleaseDeleteIsIdempotentWhenContainerMissing(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	prepared, err := fixture.service.Prepare(context.Background(), prepareSpec(t))
	if err != nil {
		t.Fatalf("准备执行环境失败: %v", err)
	}
	fixture.containers.mu.Lock()
	fixture.containers.deleteErr = fmt.Errorf(
		"%w: 容器不存在",
		&docker.Error{Code: docker.CodeContainerNotFound, Op: "docker rm"},
	)
	fixture.containers.mu.Unlock()
	result, err := fixture.service.Release(context.Background(), prepared.Environment, execenv.ReleaseRequest{
		Retention: execenv.RetentionDelete,
	})
	if err != nil {
		t.Fatalf("容器不存在时应按删除成功处理: %v", err)
	}
	if !result.ContainerRemoved {
		t.Fatal("容器不存在时必须报告删除完成")
	}
}

func TestPrepareForTaskCreatesFreshContainerPerAttempt(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	language, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境失败: %v", err)
	}
	requirement := domain.NewID()
	task, err := domain.NewTask(requirement, "git@example.com:demo/repo.git", "main", "", time.Now())
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.Status = domain.TaskStatusPreparing
	task.HostID = ptrID(domain.ID(testHostID))
	task.Config = domain.TaskConfig{
		Goal:            "实现功能",
		Environment:     language,
		ProjectID:       testProjectID,
		PlannerRoleID:   domain.NewID(),
		DeveloperRoleID: domain.NewID(),
	}
	if err := task.Validate(); err != nil {
		t.Fatalf("任务记录应当合法: %v", err)
	}
	store := &fakeTaskStore{task: task}
	service, err := app.NewExecutionEnvironmentService(app.ExecutionEnvironmentOptions{
		Environments:  &app.EnvironmentService{},
		Containers:    &app.ContainerService{},
		Images:        fixture.images,
		ContainerPort: fixture.containers,
		Tasks:         store,
	})
	if err != nil {
		t.Fatalf("构造执行环境服务失败: %v", err)
	}
	prepared, err := service.PrepareForTask(context.Background(), app.TaskEnvironmentRequest{
		TaskID: task.ID.String(),
	})
	if err != nil {
		t.Fatalf("依据任务准备执行环境失败: %v", err)
	}
	if !prepared.Succeeded() {
		t.Fatal("准备成功时必须报告成功")
	}
	if len(store.updated) != 1 {
		t.Fatalf("必须把容器关联写回任务，实际写入 %d 次", len(store.updated))
	}
	if store.updated[0].ContainerID != testContainerID {
		t.Fatalf("任务容器关联不符: %s", store.updated[0].ContainerID)
	}
	// 再次准备必须创建新的容器记录，而不是复用旧容器。
	before := len(fixture.containers.created)
	if _, err := service.PrepareForTask(context.Background(), app.TaskEnvironmentRequest{
		TaskID: task.ID.String(),
	}); err != nil {
		t.Fatalf("第二次准备执行环境失败: %v", err)
	}
	if len(fixture.containers.created) != before+1 {
		t.Fatal("重新准备必须创建新容器，不得复用旧容器")
	}
}

func TestPrepareForTaskRejectsUnknownAttempt(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	language, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境失败: %v", err)
	}
	task, err := domain.NewTask(
		domain.NewID(),
		"git@example.com:demo/repo.git",
		"main",
		"",
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = ptrID(domain.ID(testHostID))
	task.Config = domain.TaskConfig{
		Goal:            "实现功能",
		Environment:     language,
		PlannerRoleID:   domain.NewID(),
		DeveloperRoleID: domain.NewID(),
	}
	store := &fakeTaskStore{task: task}
	service, err := app.NewExecutionEnvironmentService(app.ExecutionEnvironmentOptions{
		Environments:  &app.EnvironmentService{},
		Containers:    &app.ContainerService{},
		Images:        fixture.images,
		ContainerPort: fixture.containers,
		Tasks:         store,
	})
	if err != nil {
		t.Fatalf("构造执行环境服务失败: %v", err)
	}
	if _, err := service.PrepareForTask(context.Background(), app.TaskEnvironmentRequest{
		TaskID:    task.ID.String(),
		AttemptID: domain.NewID().String(),
	}); err == nil {
		t.Fatal("不存在的尝试必须被拒绝")
	}
	if len(fixture.containers.created) != 0 {
		t.Fatal("尝试校验失败时不得创建容器")
	}
}

// ptrID 返回标识的指针。
func ptrID(id domain.ID) *domain.ID {
	return &id
}

func TestPrepareRejectsUnvalidatedHost(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	service, err := app.NewExecutionEnvironmentService(app.ExecutionEnvironmentOptions{
		Environments:  &app.EnvironmentService{},
		Containers:    &app.ContainerService{},
		Images:        fixture.images,
		ContainerPort: &unvalidatedHostPort{},
		Tasks:         &fakeTaskStore{},
	})
	if err != nil {
		t.Fatalf("构造执行环境服务失败: %v", err)
	}
	_, err = service.Prepare(context.Background(), prepareSpec(t))
	if err == nil {
		t.Fatal("未通过环境预检的节点必须被拒绝")
	}
	if !strings.Contains(err.Error(), "环境预检") && !strings.Contains(err.Error(), "checking") {
		t.Fatalf("错误信息应说明节点状态: %v", err)
	}
	if len(fixture.containers.created) != 0 {
		t.Fatal("节点不可用时不得创建容器")
	}
}

// unvalidatedHostPort 返回处于 checking 状态的执行节点。
type unvalidatedHostPort struct {
	fakeContainers
}

// LoadHost 返回尚未通过预检的节点。
func (p *unvalidatedHostPort) LoadHost(
	ctx context.Context,
	hostID string,
) (domain.ExecutionHost, error) {
	return domain.ExecutionHost{
		ID:               domain.ID(hostID),
		Name:             "未校验节点",
		Host:             "127.0.0.1",
		Port:             22,
		Username:         "deploy",
		WorkspaceRoot:    "~/.lingting/agentflow",
		Enabled:          true,
		ValidationStatus: domain.ValidationStatusChecking,
	}, nil
}

func TestWorkspaceIsolationFromManagementArea(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	prepared, err := fixture.service.Prepare(context.Background(), prepareSpec(t))
	if err != nil {
		t.Fatalf("准备执行环境失败: %v", err)
	}
	// 容器内项目目录与远程管理工作区必须是两个不同的位置。
	if prepared.Environment.WorkDir() == prepared.Management.Project {
		t.Fatal("容器内项目目录不得与管理工作区重合")
	}
	if !strings.HasPrefix(prepared.Environment.WorkDir(), "/workspace/") {
		t.Fatalf("容器内项目目录必须位于 /workspace 之下: %s", prepared.Environment.WorkDir())
	}
	if !strings.HasPrefix(prepared.Management.Project, prepared.Management.Root+"/") {
		t.Fatalf("项目管理工作目录必须位于管理工作区之内: %s", prepared.Management.Project)
	}
	// 缓存目录不得落在项目代码目录内，否则项目代码会被共享缓存覆盖。
	mount := domain.CacheMount{
		Type:          "go-mod",
		ContainerPath: "/var/cache/agentflow/go/pkg/mod",
		Scope:         domain.CacheScopeMachine,
		ABI:           "go124",
	}
	if _, err := execenv.NewWorkspace(execenv.DefaultProjectDir, "", []domain.CacheMount{mount}); err != nil {
		t.Fatalf("合法缓存挂载不应失败: %v", err)
	}
	badMount := mount
	badMount.ContainerPath = "/workspace/project/cache"
	if _, err := execenv.NewWorkspace(execenv.DefaultProjectDir, "", []domain.CacheMount{badMount}); err == nil {
		t.Fatal("缓存目录落在项目代码目录内时必须被拒绝")
	}
}

func TestSessionFilesSurviveContainerRelease(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	prepared, err := fixture.service.Prepare(context.Background(), prepareSpec(t))
	if err != nil {
		t.Fatalf("准备执行环境失败: %v", err)
	}
	// 本机 Session 与执行记录不依赖容器：删除容器后本机文件仍然可读。
	localFile := fixture.workspace.Path("sessions", "session.jsonl")
	if err := writeFile(localFile, "{\"role\":\"user\"}\n"); err != nil {
		t.Fatalf("写入本机 Session 文件失败: %v", err)
	}
	if _, err := fixture.service.Release(context.Background(), prepared.Environment, execenv.ReleaseRequest{
		Outcome: domain.ContainerOutcomeSucceeded,
	}); err != nil {
		t.Fatalf("释放执行环境失败: %v", err)
	}
	content, err := readFile(localFile)
	if err != nil {
		t.Fatalf("容器删除后本机 Session 必须仍可读: %v", err)
	}
	if !strings.Contains(content, "user") {
		t.Fatalf("Session 内容不符: %s", content)
	}
}

// writeFile 把内容写入工作空间内的文件，父目录不存在时创建。
func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// readFile 读取工作空间内的文件内容。
func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// checkContainer 是验证引擎所需的最小容器端口替身。
//
// 它记录检查命令的工作目录，用于断言验证命令确实在 Attempt 的容器工作区执行，
// 而不是在服务进程的当前目录执行。
type checkContainer struct {
	mu sync.Mutex
	// running 表示容器是否在运行。
	running bool
	// execs 记录收到的执行请求。
	execs []app.ExecRequest
}

// InspectContainer 返回受控的容器状态。
func (c *checkContainer) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return app.ContainerDetail{
		Container: domain.Container{
			ID:       domain.ID(request.ContainerID),
			HostID:   testHostID,
			Name:     "agentflow-abcdef123456",
			DockerID: testDockerID,
		},
		Inspect: &docker.ContainerInspect{ID: testDockerID, Running: c.running},
	}, nil
}

// Exec 返回受控的成功结果并记录请求。
func (c *checkContainer) Exec(
	ctx context.Context,
	request app.ExecRequest,
) (app.ExecResult, error) {
	c.mu.Lock()
	c.execs = append(c.execs, request)
	c.mu.Unlock()
	return app.ExecResult{Result: successfulExec()}, nil
}

func TestCheckEnvironmentRunsInGivenWorkspace(t *testing.T) {
	t.Parallel()
	container := &checkContainer{running: true}
	executor, err := app.NewCheckEnvironmentForTest(
		container,
		execenv.ContainerRef{
			ContainerID: testContainerID,
			DockerID:    testDockerID,
			HostID:      testHostID,
			Name:        "agentflow-abcdef123456",
		},
		"/workspace/project/sub",
	)
	if err != nil {
		t.Fatalf("构造检查执行环境失败: %v", err)
	}
	if executor.Name() != "remote_container" {
		t.Fatalf("执行环境名称应为 remote_container，实际 %s", executor.Name())
	}
	result, err := executor.Exec(context.Background(), app.CheckExecutionRequest{
		CheckID: "build",
		Command: "go",
		Args:    []string{"build", "./..."},
		Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("执行检查失败: %v", err)
	}
	if !result.Certain || result.ExitCode != 0 {
		t.Fatalf("检查结果不符: %+v", result)
	}
	container.mu.Lock()
	defer container.mu.Unlock()
	if len(container.execs) != 1 {
		t.Fatalf("必须只执行一条检查命令，实际 %d", len(container.execs))
	}
	if container.execs[0].WorkDir != "/workspace/project/sub" {
		t.Fatalf("检查命令工作目录必须来自执行环境: %s", container.execs[0].WorkDir)
	}
	if got := strings.Join(container.execs[0].Command, " "); got != "go build ./..." {
		t.Fatalf("检查命令参数不符: %s", got)
	}
}

func TestPrepareForTaskReportsConflictWhenTaskAdvanced(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	language, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境失败: %v", err)
	}
	task, err := domain.NewTask(
		domain.NewID(),
		"git@example.com:demo/repo.git",
		"main",
		"",
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = ptrID(domain.ID(testHostID))
	task.Config = domain.TaskConfig{
		Goal:            "实现功能",
		Environment:     language,
		PlannerRoleID:   domain.NewID(),
		DeveloperRoleID: domain.NewID(),
	}
	store := &fakeTaskStore{task: task, updateErr: storage.ErrConflict}
	service, err := app.NewExecutionEnvironmentService(app.ExecutionEnvironmentOptions{
		Environments:  &app.EnvironmentService{},
		Containers:    &app.ContainerService{},
		Images:        fixture.images,
		ContainerPort: fixture.containers,
		Tasks:         store,
	})
	if err != nil {
		t.Fatalf("构造执行环境服务失败: %v", err)
	}
	prepared, err := service.PrepareForTask(context.Background(), app.TaskEnvironmentRequest{
		TaskID: task.ID.String(),
	})
	if err == nil {
		t.Fatal("任务状态变更时必须返回错误")
	}
	if !errors.Is(err, execenv.ErrStateUncertain) {
		t.Fatalf("错误类别应为状态不确定，实际 %v", err)
	}
	// 容器本身已经创建并登记：失败的是任务关联写入，不是容器创建。
	if !prepared.Succeeded() {
		t.Fatal("容器准备本身已成功，不应被报告为失败")
	}
}
