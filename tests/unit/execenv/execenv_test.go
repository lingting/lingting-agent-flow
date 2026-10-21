// Package execenv_test 提供统一执行环境契约的单元测试。
//
// 用例只依赖纯类型与受控替身：不访问 Docker、SSH、数据库或任何外部进程，
// 因此默认测试路径完全在测试工作空间内完成。
package execenv_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/execenv"
)

// newEnvironment 构造一个合法的执行环境。
func newEnvironment(t *testing.T, workDir, sessionDir string) execenv.Environment {
	t.Helper()
	workspace, err := execenv.NewWorkspace(workDir, sessionDir, nil)
	if err != nil {
		t.Fatalf("构造容器内工作区失败: %v", err)
	}
	ref, err := execenv.NewContainerRef(
		"container-1",
		"docker-1",
		"host-1",
		"agentflow-abcdef123456",
	)
	if err != nil {
		t.Fatalf("构造容器引用失败: %v", err)
	}
	environment, err := execenv.NewEnvironment(
		execenv.KindRemoteContainer,
		workspace,
		ref,
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造执行环境失败: %v", err)
	}
	return environment
}

func TestEnvironmentKindMustBeSupported(t *testing.T) {
	t.Parallel()
	if err := execenv.KindRemoteContainer.RequireSupported(); err != nil {
		t.Fatalf("远程容器执行环境应当受支持: %v", err)
	}
	for _, kind := range []execenv.Kind{
		execenv.KindLocalLinux,
		execenv.KindLocalDarwin,
		execenv.KindLocalWindows,
	} {
		err := kind.RequireSupported()
		if !errors.Is(err, execenv.ErrUnsupportedKind) {
			t.Fatalf("%s 应当返回未实现错误，实际 %v", kind, err)
		}
	}
	if err := execenv.Kind("unknown").RequireSupported(); !errors.Is(err, execenv.ErrInvalidValue) {
		t.Fatalf("未知类型应当返回参数错误，实际 %v", err)
	}
}

func TestWorkspaceRejectsSourceInSessionDir(t *testing.T) {
	t.Parallel()
	if _, err := execenv.NewWorkspace("/workspace/project", "/workspace/project/tmp", nil); err == nil {
		t.Fatal("Session 目录位于项目代码目录内时必须被拒绝")
	}
	workspace, err := execenv.NewWorkspace("", "", nil)
	if err != nil {
		t.Fatalf("默认工作区应当合法: %v", err)
	}
	if workspace.Project != execenv.DefaultProjectDir {
		t.Fatalf("默认项目目录应为 %s，实际 %s", execenv.DefaultProjectDir, workspace.Project)
	}
	if !workspace.SourceInContainer() {
		t.Fatal("本版本的源代码必须位于容器内")
	}
}

func TestManagementWorkspaceDoesNotContainSource(t *testing.T) {
	t.Parallel()
	management, err := execenv.NewManagementWorkspace(
		"/home/deploy/.lingting/agentflow",
		"/home/deploy/.lingting/agentflow/projects/demo",
	)
	if err != nil {
		t.Fatalf("构造管理工作区失败: %v", err)
	}
	if management.ContainsSource() {
		t.Fatal("管理工作区不得被当作项目代码工作区")
	}
	if _, err := execenv.NewManagementWorkspace(
		"/home/deploy/.lingting/agentflow",
		"/tmp/elsewhere",
	); err == nil {
		t.Fatal("项目管理工作目录必须位于管理工作区之内")
	}
}

func TestEnsureWorkDirRejectsEscapingPaths(t *testing.T) {
	t.Parallel()
	environment := newEnvironment(t, "", "")
	workDir, err := environment.EnsureWorkDir("")
	if err != nil {
		t.Fatalf("空工作目录应回退到项目目录: %v", err)
	}
	if workDir != execenv.DefaultProjectDir {
		t.Fatalf("默认工作目录应为 %s，实际 %s", execenv.DefaultProjectDir, workDir)
	}
	for _, candidate := range []string{"/etc", "/workspace", "/root", "/workspace/project/../etc"} {
		if _, err := environment.EnsureWorkDir(candidate); err == nil {
			t.Fatalf("工作目录 %s 必须被拒绝", candidate)
		}
	}
	if _, err := environment.EnsureWorkDir("/workspace/project/sub"); err != nil {
		t.Fatalf("项目目录内的子目录应当合法: %v", err)
	}
}

func TestExecSpecResolveDefaults(t *testing.T) {
	t.Parallel()
	environment := newEnvironment(t, "", "")
	resolved, err := execenv.ExecSpec{
		Command: "go",
		Args:    []string{"test", "./..."},
		Env:     []string{"CI=1", "GOFLAGS=-mod=mod"},
	}.Resolve(environment)
	if err != nil {
		t.Fatalf("解析命令失败: %v", err)
	}
	if resolved.WorkDir != execenv.DefaultProjectDir {
		t.Fatalf("默认工作目录应为 %s，实际 %s", execenv.DefaultProjectDir, resolved.WorkDir)
	}
	if resolved.Timeout != execenv.DefaultTimeout {
		t.Fatalf("默认超时应为 %s，实际 %s", execenv.DefaultTimeout, resolved.Timeout)
	}
	if resolved.MaxOutputBytes != execenv.DefaultMaxOutputBytes {
		t.Fatalf("默认输出上限应为 %d，实际 %d", execenv.DefaultMaxOutputBytes, resolved.MaxOutputBytes)
	}
	if got := strings.Join(resolved.Argv(), " "); got != "go test ./..." {
		t.Fatalf("参数列表不符: %s", got)
	}
	if len(resolved.Env) != 2 {
		t.Fatalf("环境变量应当全部保留，实际 %v", resolved.Env)
	}
}

func TestExecSpecRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	environment := newEnvironment(t, "", "")
	cases := []struct {
		name string
		spec execenv.ExecSpec
	}{
		{name: "空命令", spec: execenv.ExecSpec{}},
		{name: "工作目录越界", spec: execenv.ExecSpec{Command: "ls", WorkDir: "/etc"}},
		{name: "环境变量缺少等号", spec: execenv.ExecSpec{Command: "ls", Env: []string{"CI"}}},
		{name: "环境变量名非法", spec: execenv.ExecSpec{Command: "ls", Env: []string{"1CI=1"}}},
		{name: "环境变量重复", spec: execenv.ExecSpec{Command: "ls", Env: []string{"CI=1", "CI=2"}}},
	}
	for _, testCase := range cases {
		if _, err := testCase.spec.Resolve(environment); err == nil {
			t.Fatalf("用例 %s 必须被拒绝", testCase.name)
		} else if !errors.Is(err, execenv.ErrInvalidValue) {
			t.Fatalf("用例 %s 应返回参数错误，实际 %v", testCase.name, err)
		}
	}
}

func TestExecResultDistinguishesOutcomes(t *testing.T) {
	t.Parallel()
	succeeded := execenv.ExecResult{
		ExitCode: 0,
		Status:   execenv.ExecStatusSucceeded,
		Started:  true,
		Certain:  true,
	}
	if !succeeded.Succeeded() {
		t.Fatal("退出码 0 且结果可确认时应判定成功")
	}
	exitError := execenv.ExecResult{
		ExitCode: 2,
		Status:   execenv.ExecStatusExitError,
		Started:  true,
		Certain:  true,
	}
	if exitError.Succeeded() {
		t.Fatal("非零退出码不得判定成功")
	}
	timeout := execenv.ExecResult{ExitCode: -1, Status: execenv.ExecStatusTimeout}
	if !timeout.TimedOut() || timeout.Succeeded() {
		t.Fatal("超时必须被识别且不得判定成功")
	}
	cancelled := execenv.ExecResult{ExitCode: -1, Status: execenv.ExecStatusCancelled}
	if !cancelled.Cancelled() || cancelled.Succeeded() {
		t.Fatal("取消必须被识别且不得判定成功")
	}
	uncertain := execenv.ExecResult{
		ExitCode: 0,
		Status:   execenv.ExecStatusSucceeded,
		Started:  true,
		Certain:  false,
	}
	if !uncertain.Uncertain() || uncertain.Succeeded() {
		t.Fatal("结果无法确认时不得判定成功")
	}
	if summary := exitError.Summary(); summary == "" {
		t.Fatal("失败结果必须给出可读摘要")
	}
}

func TestDecideRetentionFollowsLifecyclePolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		outcome         domain.ContainerOutcome
		keepInterrupted bool
		want            execenv.Retention
	}{
		{
			outcome: domain.ContainerOutcomeSucceeded,
			want:    execenv.RetentionDelete,
		},
		{
			outcome: domain.ContainerOutcomeFailed,
			want:    execenv.RetentionKeep,
		},
		{
			outcome:         domain.ContainerOutcomeInterrupted,
			keepInterrupted: true,
			want:            execenv.RetentionKeep,
		},
		{
			outcome:         domain.ContainerOutcomeInterrupted,
			keepInterrupted: false,
			want:            execenv.RetentionDelete,
		},
	}
	for _, testCase := range cases {
		got, err := execenv.DecideRetention(testCase.outcome, testCase.keepInterrupted)
		if err != nil {
			t.Fatalf("计算处置方式失败: %v", err)
		}
		if got != testCase.want {
			t.Fatalf(
				"任务结果 %s（保留中断=%v）应得到 %s，实际 %s",
				testCase.outcome,
				testCase.keepInterrupted,
				testCase.want,
				got,
			)
		}
	}
	if _, err := execenv.DecideRetention("unknown", false); err == nil {
		t.Fatal("未知任务结果必须返回错误")
	}
}

func TestPreparedRequiresCompleteEnvironment(t *testing.T) {
	t.Parallel()
	failed := execenv.Prepared{Err: errors.New("镜像准备失败")}
	if failed.Succeeded() {
		t.Fatal("准备失败时不得报告成功")
	}
	if _, err := failed.Require(); err == nil {
		t.Fatal("准备失败时不得返回可用的执行环境")
	}
	empty := execenv.Prepared{}
	if empty.Succeeded() {
		t.Fatal("缺少容器标识时不得报告成功")
	}
	environment := newEnvironment(t, "", "")
	ok := execenv.Prepared{Environment: environment}
	if !ok.Succeeded() {
		t.Fatal("完整环境应当报告成功")
	}
	if _, err := ok.Require(); err != nil {
		t.Fatalf("完整环境应当可用: %v", err)
	}
}

func TestProviderRejectsUnsupportedKind(t *testing.T) {
	t.Parallel()
	workspace, err := execenv.NewWorkspace("", "", nil)
	if err != nil {
		t.Fatalf("构造工作区失败: %v", err)
	}
	ref, err := execenv.NewContainerRef("container-1", "docker-1", "host-1", "agentflow-1")
	if err != nil {
		t.Fatalf("构造容器引用失败: %v", err)
	}
	if _, err := execenv.NewEnvironment(execenv.KindLocalWindows, workspace, ref, time.Time{}); !errors.Is(
		err,
		execenv.ErrUnsupportedKind,
	) {
		t.Fatalf("未实现的执行环境必须被拒绝，实际 %v", err)
	}
}

func TestPrepareSpecValidation(t *testing.T) {
	t.Parallel()
	language, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境失败: %v", err)
	}
	base, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境失败: %v", err)
	}
	taskID := domain.NewID()
	attemptID := domain.NewID()
	valid := execenv.PrepareSpec{
		HostID:      "host-1",
		Environment: language,
		TaskID:      &taskID,
		AttemptID:   &attemptID,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("合法准备请求不应失败: %v", err)
	}
	baseSpec := valid
	baseSpec.Environment = base
	if err := baseSpec.Validate(); err == nil {
		t.Fatal("基础环境不能用于准备任务容器")
	}
	orphan := valid
	orphan.TaskID = nil
	if err := orphan.Validate(); err == nil {
		t.Fatal("关联尝试时必须同时指定任务")
	}
	badCache := valid
	badCache.Caches = []string{"Go Mod"}
	if err := badCache.Validate(); err == nil {
		t.Fatal("非法缓存类型必须被拒绝")
	}
}

// fakeExecPort 是 execenv.ExecPort 的受控替身。
type fakeExecPort struct {
	// result 是固定返回的执行结果。
	result execenv.ExecResult
	// err 是固定返回的错误。
	err error
	// specs 记录收到的命令。
	specs []execenv.ExecSpec
	// environments 记录收到的执行环境。
	environments []execenv.Environment
}

// Exec 返回受控结果并记录请求。
func (f *fakeExecPort) Exec(
	ctx context.Context,
	environment execenv.Environment,
	spec execenv.ExecSpec,
) (execenv.ExecResult, error) {
	f.specs = append(f.specs, spec)
	f.environments = append(f.environments, environment)
	return f.result, f.err
}

func TestProviderContractIsImplementable(t *testing.T) {
	t.Parallel()
	// 用编译期断言固定契约形态：实现方必须提供这四个方法。
	var port execenv.ExecPort = &fakeExecPort{}
	environment := newEnvironment(t, "", "")
	result, err := port.Exec(context.Background(), environment, execenv.ExecSpec{Command: "ls"})
	if err != nil {
		t.Fatalf("受控执行不应失败: %v", err)
	}
	if result.Succeeded() {
		t.Fatal("零值结果不得被判定为成功")
	}
	var _ execenv.Provider = (execenv.Provider)(nil)
}

func TestEnvironmentLabelAndAccessors(t *testing.T) {
	t.Parallel()
	environment := newEnvironment(t, "/workspace/project/app", "/tmp/agentflow/sessions")
	if environment.Kind() != execenv.KindRemoteContainer {
		t.Fatalf("环境类型不符: %s", environment.Kind())
	}
	if environment.SessionDir() != "/tmp/agentflow/sessions" {
		t.Fatalf("Session 目录不符: %s", environment.SessionDir())
	}
	// 标签只包含执行环境类型与容器登记记录 ID，不包含任何凭据或本机路径。
	label := environment.Label()
	if !strings.Contains(label, execenv.KindRemoteContainer.String()) {
		t.Fatalf("标签应包含环境类型: %s", label)
	}
	if !strings.Contains(label, "container-1") {
		t.Fatalf("标签应包含容器登记记录 ID: %s", label)
	}
	if strings.Contains(label, "docker-1") {
		t.Fatalf("标签不应暴露 Docker 容器 ID: %s", label)
	}
	if environment.Workspace().Project != "/workspace/project/app" {
		t.Fatalf("工作区项目目录不符: %s", environment.Workspace().Project)
	}
	if environment.PreparedAt().IsZero() {
		t.Fatal("准备时间必须被记录")
	}
}

func TestContainerRefValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		container     string
		docker        string
		host          string
		containerName string
	}{
		{name: "缺少容器记录 ID", docker: "docker-1", host: "host-1", containerName: "agentflow-1"},
		{name: "缺少 Docker ID", container: "container-1", host: "host-1", containerName: "agentflow-1"},
		{name: "缺少节点 ID", container: "container-1", docker: "docker-1", containerName: "agentflow-1"},
		{name: "容器名称非法", container: "container-1", docker: "docker-1", host: "host-1", containerName: "-bad"},
	}
	for _, testCase := range cases {
		if _, err := execenv.NewContainerRef(
			testCase.container,
			testCase.docker,
			testCase.host,
			testCase.containerName,
		); err == nil {
			t.Fatalf("用例 %s 必须被拒绝", testCase.name)
		} else if !errors.Is(err, execenv.ErrInvalidValue) {
			t.Fatalf("用例 %s 应返回参数错误，实际 %v", testCase.name, err)
		}
	}
}

func TestPreparedFailedStep(t *testing.T) {
	t.Parallel()
	ok := execenv.StepResult{Step: execenv.StepValidate}
	failed := execenv.StepResult{
		Step: execenv.StepImage,
		Err:  errors.New("构建失败"),
	}
	prepared := execenv.Prepared{Steps: []execenv.StepResult{ok, failed}}
	if prepared.FailedStep() != execenv.StepImage {
		t.Fatalf("失败阶段应为 image，实际 %s", prepared.FailedStep())
	}
	if prepared.Duration() != 0 {
		t.Fatal("未设置结束时间时耗时应为 0")
	}
	allOK := execenv.Prepared{Steps: []execenv.StepResult{ok}}
	if allOK.FailedStep() != "" {
		t.Fatalf("全部成功时不应返回失败阶段: %s", allOK.FailedStep())
	}
}
