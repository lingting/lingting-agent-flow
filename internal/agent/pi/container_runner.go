package pi

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
)

// 容器内执行相关的默认值。
const (
	// DefaultContainerSessionDir 是容器内临时 Session 目录。
	//
	// 该目录位于容器内部文件系统，不属于项目代码目录，也不属于远程项目配置目录；
	// 容器删除后随之消失，因此必须在下一次调用前把 Session 复制到本机。
	DefaultContainerSessionDir = "/tmp/agentflow/sessions"
	// containerProbeTimeout 是容器内轻量探测命令的超时时间。
	containerProbeTimeout = 60 * time.Second
	// maxSessionBytes 是单个 Session 文件允许下载的字节上限。
	//
	// 该上限只用于拒绝异常巨大的文件，正常 Session 远小于此值。
	maxSessionBytes = 256 << 20
	// containerSessionFileName 是容器内 Session 文件名。
	containerSessionFileName = "session.jsonl"
	// containerHTMLFileName 是容器内导出的 HTML 文件名。
	containerHTMLFileName = "session.html"
)

// ContainerRunner 在任务容器内部启动 Pi 并把 Session 持久化到本机。
//
// 它复用与本机 Runner 完全相同的参数构造、Session 校验与导出流程，
// 只是把"启动子进程"替换为"在容器内执行命令"，并把 Session 文件通过
// 受控的远程临时导出路径复制到本机。
//
// 本类型不承担容器生命周期：容器的创建、停止与删除仍由容器管理层负责，
// 它也不会因为执行失败而删除容器。
type ContainerRunner struct {
	options  Options
	executor agent.ContainerExecutor
	guard    agent.ContainerGuard
}

// NewContainerRunner 构造容器内执行的 Pi Runner。
func NewContainerRunner(
	options Options,
	executor agent.ContainerExecutor,
	guard agent.ContainerGuard,
) (*ContainerRunner, error) {
	if executor == nil {
		return nil, fmt.Errorf("pi: 容器执行器不能为空")
	}
	if guard == nil {
		return nil, fmt.Errorf("pi: 容器核验器不能为空")
	}
	return &ContainerRunner{
		options:  resolveRunnerOptions(options),
		executor: executor,
		guard:    guard,
	}, nil
}

// resolveOptions 补齐默认配置。
func resolveRunnerOptions(options Options) Options {
	options.Executable = strings.TrimSpace(options.Executable)
	if options.Timeout <= 0 {
		options.Timeout = DefaultTimeout
	}
	if options.ExportTimeout <= 0 {
		options.ExportTimeout = DefaultExportTimeout
	}
	if options.GraceTimeout <= 0 {
		options.GraceTimeout = DefaultGraceTimeout
	}
	if options.OutputLimit <= 0 {
		options.OutputLimit = DefaultOutputLimit
	}
	if options.StderrLimit <= 0 {
		options.StderrLimit = DefaultStderrLimit
	}
	if options.LogLimit <= 0 {
		options.LogLimit = DefaultLogLimit
	}
	return options
}

// Run 在任务容器内执行一次完整的 Agent 调用。
//
// 执行顺序固定为：核验容器 → 检查容器内 Pi → 生成独立 Session 路径 → 启动 Pi 并
// 写入提示词 → 收集退出状态 → 导出 HTML → 复制 Session 到本机 → 清理远程临时文件。
//
// 无论进程如何结束，都会尽力把已写入的 Session 复制到本机：容器内的 Session
// 会随容器删除而消失，本机副本是唯一可追溯的记录。
func (r *ContainerRunner) Run(
	ctx context.Context,
	request agent.RunRequest,
	emit func(agent.Event),
) (agent.RunResult, error) {
	startedAt := time.Now()
	invocationID := strings.TrimSpace(request.InvocationID)
	if invocationID == "" {
		invocationID = agent.NewInvocationID()
		// 回填到请求中：本机 Session 目录同样按调用标识分层，
		// 否则同一会话内的多次调用会共用同一个文件。
		request.InvocationID = invocationID
	}
	result := agent.RunResult{
		SessionID:    request.SessionID,
		InvocationID: invocationID,
		Model:        request.Model,
		ExitCode:     -1,
		StartedAt:    startedAt,
		Session: agent.SessionFiles{
			Completeness: agent.SessionMissing,
			ExportStatus: agent.ExportNotAttempted,
		},
	}
	if request.Container == nil {
		return r.fail(request, result, emit, "container_missing",
			fmt.Errorf("pi: 容器内执行必须给出容器目标"), agent.RunFailed)
	}
	if err := request.Validate(); err != nil {
		return r.fail(request, result, emit, "invalid_request", err, agent.RunFailed)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		result.Status = agent.RunCancelled
		result.FinishedAt = time.Now()
		emitEvent(result.SessionID, emit, agent.EventRunCancelled, agent.RunCancelledData{
			Reason: "调用前已取消",
		})
		return result, err
	}
	container, err := r.guard.EnsureExecutable(ctx, request.Container.ContainerID)
	if err != nil {
		return r.fail(request, result, emit, "container_unavailable", err, agent.RunFailed)
	}
	if container.Detached {
		return r.fail(request, result, emit, "container_detached",
			fmt.Errorf("pi: 容器 %s 已脱离 AgentFlow 管理，拒绝自动执行", container.ID),
			agent.RunFailed)
	}
	if strings.TrimSpace(container.DockerID) != request.Container.DockerID {
		return r.fail(request, result, emit, "container_mismatch",
			fmt.Errorf(
				"pi: 容器记录中的 Docker 容器 ID 与请求不一致: 记录 %s，请求 %s",
				container.DockerID,
				request.Container.DockerID,
			),
			agent.RunFailed)
	}
	result.ContainerID = container.ID.String()
	result.DockerID = container.DockerID
	paths, err := r.sessionPaths(request)
	if err != nil {
		return r.fail(request, result, emit, "session_path_invalid", err, agent.RunFailed)
	}
	if err := paths.EnsureDir(); err != nil {
		return r.fail(request, result, emit, "session_dir_failed", err, agent.RunFailed)
	}
	if err := r.checkPi(ctx, request.Container); err != nil {
		return r.fail(request, result, emit, "pi_unavailable", err, agent.RunFailed)
	}
	remote := r.containerPaths(request.Container, invocationID)
	runCtx, cancel := context.WithTimeout(ctx, r.timeoutOf(request))
	defer cancel()
	emitEvent(result.SessionID, emit, agent.EventRunStarted, agent.RunStartedData{
		Model:      request.Model,
		RoleType:   request.RoleType,
		WorkingDir: request.Container.WorkDir,
		SessionDir: remote.Dir,
	})
	executed, execErr := r.execute(runCtx, request, remote)
	// 容器内的两个输出流在本机分别落盘：容器删除后原始输出仍然可读，
	// 且不会因为容器内临时目录消失而丢失诊断信息。
	logs := writeContainerLogs(paths.Dir, r.options.LogLimit, executed)
	result.StdoutPath = logs.stdoutPath()
	result.StderrPath = logs.stderrPath()
	result.ExitCode = executed.ExitCode
	result.Stdout = agent.TruncateText(strings.TrimSpace(executed.Stdout), agent.MaxFinalTextLength)
	result.StderrSummary = agent.TruncateText(
		strings.TrimSpace(executed.Stderr),
		agent.MaxFinalTextLength,
	)
	result.FinalText = result.Stdout
	// 截断标记以日志文件为准：容器返回的输出本身可能已被上限裁剪。
	result.StdoutTruncated = executed.StdoutTruncated || logs.stdoutTruncated()
	result.StderrTruncated = executed.StderrTruncated || logs.stderrTruncated()
	result.Status, result.Failure = classifyContainerOutcome(executed, execErr, request)
	// Session 处理在进程结束之后：Pi 在退出前写完 Session 文件。
	result.Session = r.persistSession(ctx, request, remote, paths, executed, result.Status, emit)
	if result.Session.Completeness == agent.SessionMissing && result.Status == agent.RunCompleted {
		// 进程正常结束却没有可用 Session，属于提取失败，不能报告为成功。
		result.Status = agent.RunFailed
		if result.Failure == "" {
			result.Failure = "未能取得 Session 文件"
		}
	}
	emitContainerOutcome(result, emit)
	result.FinishedAt = time.Now()
	return result, containerRunError(result)
}

// fail 构造失败结果并发出事件。
func (r *ContainerRunner) fail(
	request agent.RunRequest,
	result agent.RunResult,
	emit func(agent.Event),
	reason string,
	cause error,
	status agent.RunStatus,
) (agent.RunResult, error) {
	result.Status = status
	result.Failure = cause.Error()
	result.FinishedAt = time.Now()
	emitEvent(result.SessionID, emit, agent.EventRunFailed, agent.RunFailedData{
		Reason:  reason,
		Message: cause.Error(),
	})
	return result, cause
}

// timeoutOf 返回本次调用的超时时间。
func (r *ContainerRunner) timeoutOf(request agent.RunRequest) time.Duration {
	if request.Timeout > 0 {
		return request.Timeout
	}
	return r.options.Timeout
}

// containerPaths 是一次容器内调用的路径集合。
type containerSessionPaths struct {
	// Dir 是容器内本次调用的 Session 目录。
	Dir string
	// JSONL 是容器内原始 Session 文件路径。
	JSONL string
	// HTML 是容器内导出的 HTML 路径。
	HTML string
}

// containerPaths 生成容器内本次调用的 Session 路径。
//
// 路径按调用标识分层，保证同一容器内的多次调用不会共用同一个 Session 文件。
func (r *ContainerRunner) containerPaths(
	target *agent.ContainerTarget,
	invocationID string,
) containerSessionPaths {
	dir := strings.TrimSpace(target.SessionDir)
	if dir == "" {
		dir = DefaultContainerSessionDir
	}
	dir = path.Join(dir, sanitizeSegment(invocationID))
	return containerSessionPaths{
		Dir:   dir,
		JSONL: path.Join(dir, containerSessionFileName),
		HTML:  path.Join(dir, containerHTMLFileName),
	}
}

// sessionPaths 解析本次调用在本机的 Session 文件路径。
func (r *ContainerRunner) sessionPaths(request agent.RunRequest) (SessionPaths, error) {
	return SessionLayout{
		TaskID:       request.TaskID,
		StepID:       request.StepID,
		AttemptID:    request.AttemptID,
		SessionID:    request.SessionID,
		InvocationID: request.InvocationID,
	}.Paths(request.SessionRoot)
}

// checkPi 确认容器内存在可用的 Pi 可执行文件。
//
// 该检查在容器内执行，本机是否安装 Pi 与本次调用无关。
func (r *ContainerRunner) checkPi(ctx context.Context, target *agent.ContainerTarget) error {
	executable := strings.TrimSpace(target.Executable)
	if executable == "" {
		executable = executableName
	}
	command := []string{executable, argVersion}
	probeCtx, cancel := context.WithTimeout(ctx, containerProbeTimeout)
	defer cancel()
	executed, err := r.executor.Exec(probeCtx, agent.ContainerExecRequest{
		DockerID:       target.DockerID,
		Command:        command,
		Timeout:        containerProbeTimeout,
		MaxOutputBytes: MaxOutputEventLength,
	})
	if err != nil {
		return fmt.Errorf("pi: 检查容器内 Pi 失败: %w", err)
	}
	if !executed.Succeeded() {
		return fmt.Errorf(
			"pi: 容器内没有可用的 Pi 可执行文件 %q（退出码 %d）: %s",
			executable,
			executed.ExitCode,
			agent.TruncateText(strings.TrimSpace(executed.Stderr), 512),
		)
	}
	if strings.TrimSpace(executed.Stdout) == "" {
		return fmt.Errorf("pi: 容器内 Pi 版本查询没有输出")
	}
	return nil
}

// execute 在容器内启动 Pi 并写入提示词。
func (r *ContainerRunner) execute(
	ctx context.Context,
	request agent.RunRequest,
	remote containerSessionPaths,
) (agent.ContainerExecResult, error) {
	executable := strings.TrimSpace(request.Container.Executable)
	if executable == "" {
		executable = executableName
	}
	args := []string{executable, argPrint, argSession, remote.JSONL, argModel, request.Model}
	if prompt := strings.TrimSpace(request.SystemPrompt); prompt != "" {
		args = append(args, argAppendSystemPrompt, prompt)
	}
	return r.executor.Exec(ctx, agent.ContainerExecRequest{
		DockerID:       request.Container.DockerID,
		Command:        args,
		Stdin:          promptPayload(request.Prompt),
		Timeout:        r.timeoutOf(request),
		MaxOutputBytes: r.options.OutputLimit,
	})
}

// promptPayload 返回写入 Pi 标准输入的提示词内容。
//
// 末尾补一个换行，使以行为单位读取输入的 CLI 也能正确处理最后一行。
func promptPayload(prompt string) []byte {
	if strings.HasSuffix(prompt, "\n") {
		return []byte(prompt)
	}
	return []byte(prompt + "\n")
}

// classifyContainerOutcome 把容器内执行结果转换为运行状态与失败说明。
func classifyContainerOutcome(
	executed agent.ContainerExecResult,
	execErr error,
	request agent.RunRequest,
) (agent.RunStatus, string) {
	if executed.TimedOut || errors.Is(execErr, context.DeadlineExceeded) {
		return agent.RunTimedOut, fmt.Sprintf(
			"执行超时（上限 %s）",
			request.Timeout,
		)
	}
	if executed.Cancelled || errors.Is(execErr, context.Canceled) {
		return agent.RunCancelled, "执行被调用方取消"
	}
	if execErr != nil {
		return agent.RunFailed, execErr.Error()
	}
	if !executed.Certain {
		return agent.RunFailed, "容器内进程是否结束无法确认，请先核验容器内实际状态"
	}
	if executed.ExitCode != 0 {
		detail := agent.TruncateText(strings.TrimSpace(executed.Stderr), maxFailureDetail)
		if detail == "" {
			detail = agent.TruncateText(strings.TrimSpace(executed.Stdout), maxFailureDetail)
		}
		if detail != "" {
			return agent.RunFailed, fmt.Sprintf(
				"Pi 异常退出（退出码 %d）: %s",
				executed.ExitCode,
				detail,
			)
		}
		return agent.RunFailed, fmt.Sprintf("Pi 异常退出（退出码 %d）", executed.ExitCode)
	}
	return agent.RunCompleted, ""
}

// persistSession 把容器内的 Session 复制到本机并尝试导出 HTML。
//
// 顺序固定：先取得原始 JSONL 并核验落盘，再导出 HTML。导出失败只降级导出状态，
// 不会删除或覆盖已经保存的原始文件；提取失败会保留可能得到的不完整内容。
func (r *ContainerRunner) persistSession(
	ctx context.Context,
	request agent.RunRequest,
	remote containerSessionPaths,
	paths SessionPaths,
	executed agent.ContainerExecResult,
	status agent.RunStatus,
	emit func(agent.Event),
) agent.SessionFiles {
	files := agent.SessionFiles{
		Completeness: agent.SessionMissing,
		ExportStatus: agent.ExportNotAttempted,
	}
	if ctx.Err() != nil {
		// 调用已取消或超时：仍尽力提取已经写入的内容，但不再启动导出。
		ctx = context.WithoutCancel(ctx)
	}
	exportErr := r.exportHTML(ctx, request, remote)
	if exportErr != nil {
		files.ExportStatus = agent.ExportFailed
		files.Error = exportErr.Error()
		emitEvent(request.SessionID, emit, agent.EventSessionExported, agent.SessionExportedData{
			Status: files.ExportStatus,
			Error:  exportErr.Error(),
		})
	} else {
		files.ExportStatus = agent.ExportSucceeded
	}
	written, copyErr := r.copySession(ctx, request, remote, paths)
	if copyErr != nil {
		files.Error = joinError(files.Error, copyErr.Error())
		return files
	}
	if written == 0 {
		files.Error = joinError(files.Error, "容器内没有可用的 Session 文件")
		return files
	}
	files.Bytes = written
	if err := validateSessionFile(paths.JSONL); err != nil {
		// 文件存在但内容不完整时仍然保留，供人工排查。
		files.Completeness = agent.SessionPartial
		files.JSONLPath = paths.JSONL
		files.Error = joinError(files.Error, err.Error())
		emitEvent(request.SessionID, emit, agent.EventSessionSaved, agent.SessionSavedData{
			Path:         paths.JSONL,
			Bytes:        files.Bytes,
			Completeness: files.Completeness,
		})
		return files
	}
	files.JSONLPath = paths.JSONL
	switch {
	case status != agent.RunCompleted:
		files.Completeness = agent.SessionPartial
	case files.ExportStatus == agent.ExportFailed:
		files.Completeness = agent.SessionExportFailed
	default:
		files.Completeness = agent.SessionComplete
	}
	emitEvent(request.SessionID, emit, agent.EventSessionSaved, agent.SessionSavedData{
		Path:         paths.JSONL,
		Bytes:        files.Bytes,
		Completeness: files.Completeness,
	})
	if files.ExportStatus == agent.ExportSucceeded {
		files.HTMLPath = paths.HTML
		emitEvent(request.SessionID, emit, agent.EventSessionExported, agent.SessionExportedData{
			Path:   paths.HTML,
			Status: files.ExportStatus,
		})
	}
	return files
}

// copySession 把容器内 Session 复制到本机。
//
// 传输路径为：容器内文件 → 远程宿主机受控临时目录 → 本机临时文件 → 原子重命名。
// 只有本机文件成功落盘并校验字节数后，才清理远程临时副本。
func (r *ContainerRunner) copySession(
	ctx context.Context,
	request agent.RunRequest,
	remote containerSessionPaths,
	paths SessionPaths,
) (int64, error) {
	hostDir := hostExportDir(request.Container.ContainerID, request.InvocationID)
	hostPath := path.Join(hostDir, containerSessionFileName)
	if err := r.executor.CopyFromContainer(ctx, request.Container.DockerID, remote.JSONL, hostPath); err != nil {
		return 0, fmt.Errorf("从容器导出 Session 失败: %w", err)
	}
	written, err := r.executor.DownloadToLocal(ctx, hostPath, paths.JSONL, maxSessionBytes)
	if err != nil {
		// 下载失败时保留远程临时副本，便于人工排查或重新提取。
		return 0, fmt.Errorf("下载 Session 到本机失败: %w", err)
	}
	if written == 0 {
		return 0, fmt.Errorf("下载的 Session 文件为空")
	}
	// 只有本机文件成功落盘后才清理远程临时副本。
	r.cleanupHostFile(ctx, hostPath)
	return written, nil
}

// cleanupHostFile 在尽力清理远程临时文件，失败不影响已经完成的本机持久化。
func (r *ContainerRunner) cleanupHostFile(ctx context.Context, hostPath string) {
	if err := r.executor.RemoveHostFile(context.WithoutCancel(ctx), hostPath); err != nil {
		// 清理失败只影响临时文件残留，不改变 Session 的持久化结果。
		return
	}
}

// hostExportDir 返回远程宿主机上受控的 Session 导出目录。
//
// 该目录位于 AgentFlow 运行临时目录下，按容器与调用标识分层，
// 不使用远程项目配置目录，也不与项目代码目录重叠。
func hostExportDir(containerID, invocationID string) string {
	return path.Join(
		"/tmp",
		"agentflow",
		"sessions",
		sanitizeSegment(containerID),
		sanitizeSegment(invocationID),
	)
}

// exportHTML 在容器内用 Pi 官方导出能力生成 HTML。
//
// 导出只读取原始 JSONL，不会启动新的模型调用；导出失败不影响原始文件。
func (r *ContainerRunner) exportHTML(
	ctx context.Context,
	request agent.RunRequest,
	remote containerSessionPaths,
) error {
	if r.options.DisableExport {
		return nil
	}
	executable := strings.TrimSpace(request.Container.Executable)
	if executable == "" {
		executable = executableName
	}
	exportCtx, cancel := context.WithTimeout(ctx, r.options.ExportTimeout)
	defer cancel()
	executed, err := r.executor.Exec(exportCtx, agent.ContainerExecRequest{
		DockerID: request.Container.DockerID,
		Command: append(
			[]string{executable},
			exportArguments(remote.JSONL, remote.HTML)...,
		),
		Timeout:        r.options.ExportTimeout,
		MaxOutputBytes: MaxOutputEventLength,
	})
	if err != nil {
		return fmt.Errorf("导出 HTML 失败: %w", err)
	}
	if !executed.Succeeded() {
		return fmt.Errorf(
			"导出 HTML 失败（退出码 %d）: %s",
			executed.ExitCode,
			agent.TruncateText(strings.TrimSpace(executed.Stderr), 512),
		)
	}
	// 导出成功后把 HTML 也复制到本机：容器内文件会随容器删除而消失。
	hostDir := hostExportDir(request.Container.ContainerID, request.InvocationID)
	hostPath := path.Join(hostDir, containerHTMLFileName)
	if copyErr := r.executor.CopyFromContainer(
		ctx,
		request.Container.DockerID,
		remote.HTML,
		hostPath,
	); copyErr != nil {
		return fmt.Errorf("从容器导出 HTML 失败: %w", copyErr)
	}
	localPath, ok := r.localHTMLPath(request)
	if !ok {
		return fmt.Errorf("无法确定本机 HTML 路径")
	}
	if _, err := r.executor.DownloadToLocal(ctx, hostPath, localPath, maxSessionBytes); err != nil {
		return fmt.Errorf("下载 HTML 到本机失败: %w", err)
	}
	r.cleanupHostFile(ctx, hostPath)
	return nil
}

// localHTMLPath 返回本次调用在本机的 HTML 路径。
func (r *ContainerRunner) localHTMLPath(request agent.RunRequest) (string, bool) {
	paths, err := r.sessionPaths(request)
	if err != nil {
		return "", false
	}
	return paths.HTML, true
}

// emitContainerOutcome 根据执行结果发出终态事件。
func emitContainerOutcome(
	result agent.RunResult,
	emit func(agent.Event),
) {
	switch result.Status {
	case agent.RunCancelled:
		emitEvent(result.SessionID, emit, agent.EventRunCancelled, agent.RunCancelledData{
			Reason: result.Failure,
		})
	case agent.RunTimedOut:
		emitEvent(result.SessionID, emit, agent.EventRunTimedOut, agent.RunTimedOutData{
			Timeout:  result.Failure,
			ExitCode: result.ExitCode,
		})
	case agent.RunFailed:
		emitEvent(result.SessionID, emit, agent.EventRunFailed, agent.RunFailedData{
			Reason:   "container_run_failed",
			Message:  agent.TruncateText(result.Failure, 512),
			ExitCode: result.ExitCode,
		})
	default:
		emitEvent(result.SessionID, emit, agent.EventRunSettled, agent.RunSettledData{
			ExitCode: result.ExitCode,
		})
	}
}

// containerRunError 把执行结果转换为错误。
func containerRunError(result agent.RunResult) error {
	switch result.Status {
	case agent.RunCancelled:
		return fmt.Errorf("pi: 容器内执行被取消: %w", context.Canceled)
	case agent.RunTimedOut:
		return fmt.Errorf("pi: 容器内执行超时: %w", context.DeadlineExceeded)
	case agent.RunFailed:
		if result.Failure != "" {
			return fmt.Errorf("pi: 容器内执行失败（退出码 %d）: %s", result.ExitCode, result.Failure)
		}
		return fmt.Errorf("pi: 容器内执行失败（退出码 %d）", result.ExitCode)
	default:
		return nil
	}
}

// joinError 合并两个错误说明，忽略空值。
func joinError(existing, addition string) string {
	switch {
	case strings.TrimSpace(existing) == "":
		return addition
	case strings.TrimSpace(addition) == "":
		return existing
	default:
		return existing + "；" + addition
	}
}

// writeContainerLogs 把容器内执行的两个输出流写入本机日志文件。
//
// 容器执行层返回的是已经按上限裁剪后的输出，无法再流式追加；这里把它分别
// 落盘，使容器删除后仍有可读的诊断记录。日志写入失败只降级诊断，不影响执行结果。
func writeContainerLogs(dir string, limit int64, executed agent.ContainerExecResult) *outputLogs {
	logs := newOutputLogs(dir, limit)
	if logs.stdout != nil {
		logs.stdout.Write(strings.TrimRight(executed.Stdout, "\r\n"))
	}
	if logs.stderr != nil {
		logs.stderr.Write(strings.TrimRight(executed.Stderr, "\r\n"))
	}
	_ = logs.close()
	return logs
}
