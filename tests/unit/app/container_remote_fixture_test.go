package app_test

// 本文件提供容器服务单元测试使用的受控远程现场。
//
// 替身只替换 SSH 连接的建立过程（app.SSHOptions.Connector）：容器服务仍然经过真实的
// sshclient.Client 接口、真实的 docker.Runner 与真实的容器状态机，因此被测的是容器服务
// 自己的停止、复核与登记同步逻辑。远程文件通过把远程绝对路径映射到测试工作空间中的
// 本机目录来模拟，容器与命名卷的状态由替身显式记录，不连接任何真实主机。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 受控远程现场的固定值。
const (
	// remoteHome 是替身返回的远程 Home 目录。
	remoteHome = "/home/deploy"
	// remoteWorkspace 是替身执行节点的远程工作空间。
	remoteWorkspace = "/srv/agentflow"
	// containerTestHostID 是受控远程现场使用的执行节点标识。
	containerTestHostID = "container-remote-host"
	// containerTestProjectID 是受控远程现场使用的项目标识。
	containerTestProjectID = "container-remote-project"
	// containerTestImageID 是容器记录使用的镜像 ID。
	containerTestImageID = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	// containerTestBuildHash 是容器记录使用的构建配置哈希。
	containerTestBuildHash = "4444444444444444444444444444444444444444444444444444444444444444"
	// containerTestCommit 是规划快照使用的固定 commit。
	containerTestCommit = "5555555555555555555555555555555555555555"
	// containerTestVersionLabel 是环境版本标签。
	containerTestVersionLabel = "v1"
	// containerTestPiOutput 是容器内 Pi 版本探测的输出。
	containerTestPiOutput = "9.9.9"
	// containerTestSessionJSONL 是一份结构完整的最小 Session 内容。
	containerTestSessionJSONL = `{"type":"session","version":3,"id":"fake-session","cwd":"/workspace/project"}
{"type":"message","id":"a1","parentId":null,"timestamp":"2025-01-02T03:04:06.000Z","message":{"role":"user","content":"只回答 READY","timestamp":1735787046000}}
{"type":"message","id":"b2","parentId":"a1","timestamp":"2025-01-02T03:04:07.000Z","message":{"role":"assistant","content":[{"type":"text","text":"READY"}],"provider":"fake","model":"model-1","stopReason":"stop","timestamp":1735787047000}}
`
)

// fakeRemoteContainer 是替身记录的一份容器远程现场。
type fakeRemoteContainer struct {
	// ID 是 Docker 容器 ID。
	ID string
	// Name 是不带前导斜杠的容器名称。
	Name string
	// Labels 是容器标签。
	Labels map[string]string
	// State 是 Docker 侧状态。
	State domain.DockerContainerState
	// Running 表示容器是否正在运行。
	Running bool
	// ExitCode 是容器退出码。
	ExitCode int
}

// fakeRemoteHost 是受控的远程宿主机替身。
//
// 它同时提供 SSH 命令执行与远程文件系统：命令按 Docker 子命令分流，容器与命名卷状态
// 由这里的字段显式记录，因此"停止后是否真的停止"不会由替身默认给出。
type fakeRemoteHost struct {
	mu sync.Mutex
	// root 是远程文件系统映射到本机的根目录。
	root string
	// containers 按 Docker 容器 ID 记录现场。
	containers map[string]*fakeRemoteContainer
	// volumes 按卷名记录命名卷现场。
	volumes map[string]docker.VolumeInspect
	// commits 按卷名记录快照实际 commit。
	commits map[string]string
	// commands 按发生顺序记录收到的远程命令。
	commands []string
	// files 按「Docker 容器 ID|容器内路径」记录预置的容器内文件内容。
	files map[string][]byte

	// inspectFailure 让 docker inspect 返回不确定结果。
	inspectFailure bool
	// stopFailure 让 docker stop 返回不确定结果。
	stopFailure bool
	// stopApplies 为 true 时停止命令报错但容器确实已经停止。
	stopApplies bool
	// inspectAfterStopFailure 让停止之后的 docker inspect 返回不确定结果。
	inspectAfterStopFailure bool
	// stopIgnored 为 true 时停止命令报成功但容器仍在运行。
	stopIgnored bool
	// inspectIDOverride 覆盖 docker inspect 返回的容器 ID。
	inspectIDOverride string
	// piCancelled 为 true 时容器内 Pi 主命令返回"已启动但结果不确定"。
	piCancelled bool
	// piStarted 为 false 时容器内 Pi 主命令根本没有启动。
	piStarted bool
	// stopCalls 统计进入 docker stop 的次数。
	stopCalls int
	// inspectCalls 统计进入 docker inspect 的次数。
	inspectCalls int
	// printSessionPresent 按顺序记录每次 Pi 主调用启动时容器内是否已有 Session 文件。
	//
	// 它用于验证"下一次调用先恢复历史 Session，再执行模型调用"这一顺序。
	printSessionPresent []bool
}

// newFakeRemoteHost 构造受控远程宿主机。
func newFakeRemoteHost(t *testing.T, root string) *fakeRemoteHost {
	t.Helper()
	// 远程工作空间必须真实存在：容器服务在建立会话时会检查它的可访问性与写权限。
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(remoteWorkspace)), 0o755); err != nil {
		t.Fatalf("创建受控远程工作空间失败: %v", err)
	}
	return &fakeRemoteHost{
		root:       root,
		containers: make(map[string]*fakeRemoteContainer),
		volumes:    make(map[string]docker.VolumeInspect),
		commits:    make(map[string]string),
		piStarted:  true,
	}
}

// trackContainer 登记一份容器远程现场。
func (h *fakeRemoteHost) trackContainer(container domain.Container, planningSessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	labels := map[string]string{
		docker.OwnershipLabelKey: "true",
		docker.HostLabelKey:      container.HostID.String(),
		docker.ContainerLabelKey: container.ID.String(),
	}
	if strings.TrimSpace(planningSessionID) != "" {
		labels[docker.PlanningSessionLabelKey] = planningSessionID
	}
	h.containers[container.DockerID] = &fakeRemoteContainer{
		ID:      container.DockerID,
		Name:    container.Name,
		Labels:  labels,
		State:   domain.DockerStateRunning,
		Running: true,
	}
}

// setContainerLabels 覆盖容器标签，用于构造标签不一致的现场。
func (h *fakeRemoteHost) setContainerLabels(dockerID string, labels map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if container, ok := h.containers[dockerID]; ok {
		container.Labels = labels
	}
}

// setContainerID 覆盖远程容器 ID，用于构造本地记录与远程不一致的现场。
func (h *fakeRemoteHost) setContainerID(dockerID, actual string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	container, ok := h.containers[dockerID]
	if !ok {
		return
	}
	delete(h.containers, dockerID)
	container.ID = actual
	h.containers[actual] = container
}

// isRunning 判断指定容器当前是否仍在运行。
func (h *fakeRemoteHost) isRunning(dockerID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	container, ok := h.containers[dockerID]
	return ok && container.Running
}

// setRunning 直接设置容器的远程运行状态，用于构造"容器已经停止"的现场。
func (h *fakeRemoteHost) setRunning(dockerID string, running bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	container, ok := h.containers[dockerID]
	if !ok {
		return
	}
	container.Running = running
	if running {
		container.State = domain.DockerStateRunning
		return
	}
	container.State = domain.DockerStateExited
}

// commandsCopy 返回收到的远程命令副本。
func (h *fakeRemoteHost) commandsCopy() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.commands...)
}

// printSessionStates 返回每次 Pi 主调用启动时容器内是否已有 Session 文件的记录。
func (h *fakeRemoteHost) printSessionStates() []bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]bool(nil), h.printSessionPresent...)
}

// countCommands 统计包含指定片段的命令数量。
func (h *fakeRemoteHost) countCommands(fragment string) int {
	total := 0
	for _, command := range h.commandsCopy() {
		if strings.Contains(command, fragment) {
			total++
		}
	}
	return total
}

// trackVolume 登记一份命名卷现场与它的实际 commit。
func (h *fakeRemoteHost) trackVolume(name string, sessionID, hostID domain.ID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.volumes[name] = docker.VolumeInspect{
		Name: name,
		Labels: map[string]string{
			docker.OwnershipLabelKey:       "true",
			docker.HostLabelKey:            hostID.String(),
			docker.PlanningSessionLabelKey: sessionID.String(),
		},
	}
	h.commits[name] = containerTestCommit
}

// remotePath 把远程绝对路径映射到本机路径。
func (h *fakeRemoteHost) remotePath(remotePath string) (string, error) {
	normalized, err := sshclient.NormalizeRemotePath(remotePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(h.root, filepath.FromSlash(normalized)), nil
}

// run 处理一条远程命令。
func (h *fakeRemoteHost) run(command string) (sshclient.CommandResult, error) {
	h.mu.Lock()
	h.commands = append(h.commands, command)
	h.mu.Unlock()
	switch {
	case command == `printf %s "$HOME"`:
		return succeededRemoteCommand(remoteHome), nil
	case strings.HasPrefix(command, "test -w "):
		return succeededRemoteCommand(""), nil
	case strings.Contains(command, " docker inspect "):
		return h.inspect(command)
	case strings.Contains(command, " docker stop "):
		return h.stop(command)
	case strings.Contains(command, " docker start "):
		return h.start(command)
	case strings.Contains(command, " docker volume inspect "):
		return h.inspectVolume(command)
	case strings.Contains(command, " docker exec "):
		return h.exec(command)
	case strings.Contains(command, " docker run "):
		return h.runOneShot(command)
	case strings.Contains(command, " docker cp "):
		return h.copy(command)
	default:
		return succeededRemoteCommand(""), nil
	}
}

// inspect 处理 docker inspect。
func (h *fakeRemoteHost) inspect(command string) (sshclient.CommandResult, error) {
	h.mu.Lock()
	h.inspectCalls++
	stopped := h.stopCalls > 0
	failure := h.inspectFailure || (stopped && h.inspectAfterStopFailure)
	target := commandTarget(command)
	container, ok := h.containers[target]
	var snapshot fakeRemoteContainer
	if ok {
		snapshot = *container
		snapshot.Labels = map[string]string{}
		for key, value := range container.Labels {
			snapshot.Labels[key] = value
		}
		if strings.TrimSpace(h.inspectIDOverride) != "" {
			snapshot.ID = h.inspectIDOverride
		}
	}
	h.mu.Unlock()
	if failure {
		return uncertainRemoteCommand(command, "模拟 docker inspect 结果不确定")
	}
	if !ok {
		return failedRemoteCommand(command, "Error: No such object: "+target), nil
	}
	payload := map[string]any{
		"Id":    snapshot.ID,
		"Name":  "/" + snapshot.Name,
		"Image": containerTestImageID,
		"Config": map[string]any{
			"Labels": snapshot.Labels,
		},
		"State": map[string]any{
			"Status":   snapshot.State.String(),
			"Running":  snapshot.Running,
			"ExitCode": snapshot.ExitCode,
		},
	}
	content, err := json.Marshal([]any{payload})
	if err != nil {
		return sshclient.CommandResult{}, err
	}
	return succeededRemoteCommand(string(content)), nil
}

// stop 处理 docker stop。
func (h *fakeRemoteHost) stop(command string) (sshclient.CommandResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopCalls++
	target := commandTarget(command)
	container, ok := h.containers[target]
	if h.stopFailure {
		if ok && h.stopApplies {
			container.Running = false
			container.State = domain.DockerStateExited
		}
		return uncertainRemoteCommand(command, "模拟 docker stop 结果不确定")
	}
	if h.stopIgnored {
		// 停止命令返回成功但容器仍在运行：只有后续实际检查才能发现。
		return succeededRemoteCommand(""), nil
	}
	if ok {
		container.Running = false
		container.State = domain.DockerStateExited
	}
	return succeededRemoteCommand(""), nil
}

// start 处理 docker start。
func (h *fakeRemoteHost) start(command string) (sshclient.CommandResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	target := commandTarget(command)
	if container, ok := h.containers[target]; ok {
		container.Running = true
		container.State = domain.DockerStateRunning
	}
	return succeededRemoteCommand(""), nil
}

// exec 处理 docker exec。
func (h *fakeRemoteHost) exec(command string) (sshclient.CommandResult, error) {
	dockerID := containerToken(command)
	switch {
	case strings.Contains(command, "--version"):
		return succeededRemoteCommand(containerTestPiOutput + "\n"), nil
	case strings.Contains(command, "--export"):
		// 导出 HTML：在容器内写入目标文件，使后续 docker cp 有内容可复制。
		h.writeContainerFile(dockerID, commandTarget(command), []byte("<html>session</html>"))
		return succeededRemoteCommand("Exported\n"), nil
	case strings.Contains(command, "--print"):
		sessionPath := sessionPathOf(commandArgs(command))
		h.mu.Lock()
		started, cancelled := h.piStarted, h.piCancelled
		_, restored := h.files[dockerID+"|"+sessionPath]
		h.printSessionPresent = append(h.printSessionPresent, restored)
		h.mu.Unlock()
		if !started {
			return sshclient.CommandResult{
				Command:  command,
				Status:   sshclient.CommandStatusConnectionError,
				ExitCode: -1,
			}, errors.New("模拟容器内命令没有启动")
		}
		if cancelled {
			return uncertainRemoteCommand(command, "模拟容器内 Pi 结果不确定")
		}
		// 正常完成时写入容器内的 Session 文件，后续 docker cp 才能取到内容。
		h.writeContainerFile(dockerID, sessionPath, []byte(containerTestSessionJSONL))
		return succeededRemoteCommand("READY\n"), nil
	default:
		return succeededRemoteCommand(""), nil
	}
}

// containerToken 返回 docker exec 命令中的容器 ID。
//
// 受控现场的容器 ID 统一以 docker- 开头，因此可以在参数中唯一定位；替身只用于测试，
// 不做与真实 Docker CLI 等价的参数解析。
func containerToken(command string) string {
	for _, arg := range commandArgs(command) {
		if strings.HasPrefix(arg, "docker-") {
			return arg
		}
	}
	return ""
}

// sessionPathOf 返回 docker exec 命令中 --session 的取值。
func sessionPathOf(command []string) string {
	for index, arg := range command {
		if arg == "--session" && index+1 < len(command) {
			return command[index+1]
		}
	}
	return ""
}

// inspectVolume 处理 docker volume inspect。
func (h *fakeRemoteHost) inspectVolume(command string) (sshclient.CommandResult, error) {
	h.mu.Lock()
	volume, ok := h.volumes[commandTarget(command)]
	h.mu.Unlock()
	if !ok {
		return failedRemoteCommand(command, "Error: no such volume"), nil
	}
	payload := map[string]any{"Name": volume.Name, "Labels": volume.Labels}
	content, err := json.Marshal([]any{payload})
	if err != nil {
		return sshclient.CommandResult{}, err
	}
	return succeededRemoteCommand(string(content)), nil
}

// runOneShot 处理 docker run：运行契约预检与只读 git 核验都走这里。
func (h *fakeRemoteHost) runOneShot(command string) (sshclient.CommandResult, error) {
	switch {
	case strings.Contains(command, "command -v sleep"):
		return succeededRemoteCommand("/usr/bin/sleep\n"), nil
	case strings.Contains(command, "--abbrev-ref"):
		return succeededRemoteCommand("HEAD\n"), nil
	case strings.Contains(command, "rev-parse"):
		h.mu.Lock()
		commit := containerTestCommit
		for _, candidate := range h.commits {
			commit = candidate
			break
		}
		h.mu.Unlock()
		return succeededRemoteCommand(commit + "\n"), nil
	default:
		return succeededRemoteCommand(""), nil
	}
}

// copy 处理 docker cp 的两个方向。
//
// 容器 → 宿主机：把容器内文件写入映射后的宿主机路径；宿主机 → 容器：把映射后的宿主机
// 文件写入容器内路径。两个方向都经过与真实命令相同的参数形式。
func (h *fakeRemoteHost) copy(command string) (sshclient.CommandResult, error) {
	args := commandArgs(command)
	if len(args) < 4 {
		return failedRemoteCommand(command, "docker cp: 参数不足"), nil
	}
	source := args[len(args)-2]
	target := args[len(args)-1]
	if strings.Contains(target, ":") {
		return h.copyIntoContainer(command, source, target)
	}
	separator := strings.Index(source, ":")
	if separator <= 0 {
		return failedRemoteCommand(command, "docker cp: 源路径非法"), nil
	}
	containerPath := source[separator+1:]
	container := h.containerByNameOrID(source[:separator])
	if container == nil {
		return failedRemoteCommand(command, "Error: No such container: "+source[:separator]), nil
	}
	h.mu.Lock()
	content, ok := h.containerFile(container, containerPath)
	h.mu.Unlock()
	if !ok {
		return failedRemoteCommand(command, "Error: No such file or directory"), nil
	}
	localTarget, err := h.remotePath(target)
	if err != nil {
		return sshclient.CommandResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(localTarget), 0o755); err != nil {
		return sshclient.CommandResult{}, err
	}
	if err := os.WriteFile(localTarget, content, 0o600); err != nil {
		return sshclient.CommandResult{}, err
	}
	return succeededRemoteCommand(""), nil
}

// copyIntoContainer 处理宿主机 → 容器方向的 docker cp。
func (h *fakeRemoteHost) copyIntoContainer(
	command, hostPath, target string,
) (sshclient.CommandResult, error) {
	separator := strings.Index(target, ":")
	if separator <= 0 {
		return failedRemoteCommand(command, "docker cp: 目标路径非法"), nil
	}
	containerPath := target[separator+1:]
	container := h.containerByNameOrID(target[:separator])
	if container == nil {
		return failedRemoteCommand(command, "Error: No such container: "+target[:separator]), nil
	}
	localSource, err := h.remotePath(hostPath)
	if err != nil {
		return sshclient.CommandResult{}, err
	}
	content, err := os.ReadFile(localSource)
	if err != nil {
		return failedRemoteCommand(command, "Error: No such file or directory"), nil
	}
	h.writeContainerFile(container.ID, containerPath, content)
	return succeededRemoteCommand(""), nil
}

// containerByNameOrID 按名称或 ID 定位容器。
func (h *fakeRemoteHost) containerByNameOrID(target string) *fakeRemoteContainer {
	h.mu.Lock()
	defer h.mu.Unlock()
	if container, ok := h.containers[target]; ok {
		return container
	}
	for _, container := range h.containers {
		if container.Name == target {
			return container
		}
	}
	return nil
}

// containerFile 返回容器内文件内容；替身只支持通过 files 字段预置的路径。
func (h *fakeRemoteHost) containerFile(container *fakeRemoteContainer, containerPath string) ([]byte, bool) {
	if h.files == nil {
		return nil, false
	}
	content, ok := h.files[container.ID+"|"+containerPath]
	return content, ok
}

// writeContainerFile 预置容器内文件内容。
func (h *fakeRemoteHost) writeContainerFile(dockerID, containerPath string, content []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.files == nil {
		h.files = make(map[string][]byte)
	}
	h.files[dockerID+"|"+containerPath] = content
}

// succeededRemoteCommand 构造成功的远程命令结果。
func succeededRemoteCommand(stdout string) sshclient.CommandResult {
	return sshclient.CommandResult{
		Status:               sshclient.CommandStatusSucceeded,
		ExitCode:             0,
		Stdout:               stdout,
		Started:              true,
		TerminationConfirmed: true,
	}
}

// failedRemoteCommand 构造正常结束但退出码非零的远程命令结果。
func failedRemoteCommand(command, stderr string) sshclient.CommandResult {
	return sshclient.CommandResult{
		Command:              command,
		Status:               sshclient.CommandStatusExitError,
		ExitCode:             1,
		Stderr:               stderr,
		Started:              true,
		TerminationConfirmed: true,
	}
}

// uncertainRemoteCommand 构造"已启动但结果无法确认"的远程命令结果。
func uncertainRemoteCommand(command, detail string) (sshclient.CommandResult, error) {
	return sshclient.CommandResult{
		Command:  command,
		Status:   sshclient.CommandStatusCancelled,
		ExitCode: -1,
		Started:  true,
	}, errors.New(detail)
}

// commandArgs 把远程命令拆成参数，去掉环境变量前缀与单引号。
func commandArgs(command string) []string {
	parts := strings.Split(command, " ")
	args := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.Trim(part, "'")
		if trimmed == "" {
			continue
		}
		args = append(args, trimmed)
	}
	return args
}

// commandTarget 返回 docker 子命令的最后一个参数，即目标容器或卷。
func commandTarget(command string) string {
	args := commandArgs(command)
	if len(args) == 0 {
		return ""
	}
	return args[len(args)-1]
}

// fakeRemoteClient 是 sshclient.Client 的受控替身。
type fakeRemoteClient struct {
	host     *fakeRemoteHost
	transfer *fakeRemoteTransfer
}

// Endpoint 返回固定目标。
func (c *fakeRemoteClient) Endpoint() sshclient.Endpoint {
	return sshclient.Endpoint{Host: "127.0.0.1", Port: 22, Username: "deploy"}
}

// Run 把命令交给远程宿主机替身。
func (c *fakeRemoteClient) Run(
	ctx context.Context,
	command string,
	options sshclient.RunOptions,
) (sshclient.CommandResult, error) {
	return c.host.run(command)
}

// RunWithInput 读完标准输入后转发到 Run。
func (c *fakeRemoteClient) RunWithInput(
	ctx context.Context,
	command string,
	input io.Reader,
	options sshclient.RunOptions,
) (sshclient.CommandResult, error) {
	if input != nil {
		if _, err := io.Copy(io.Discard, input); err != nil {
			return sshclient.CommandResult{}, err
		}
	}
	return c.host.run(command)
}

// SFTP 返回同一个受控远程文件系统。
func (c *fakeRemoteClient) SFTP() (sshclient.FileTransfer, error) {
	return c.transfer, nil
}

// Close 表示连接关闭。
func (c *fakeRemoteClient) Close() error {
	return nil
}

// fakeRemoteTransfer 把远程路径映射到本机目录，从而实现真实的文件读写。
type fakeRemoteTransfer struct {
	host *fakeRemoteHost
}

// localPath 把远程路径映射到本机路径。
func (t *fakeRemoteTransfer) localPath(remotePath string) (string, error) {
	return t.host.remotePath(remotePath)
}

// Upload 把本机文件复制到映射后的本机路径。
func (t *fakeRemoteTransfer) Upload(
	ctx context.Context,
	localPath, remotePath string,
	options sshclient.UploadOptions,
) error {
	content, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	return t.WriteFile(ctx, remotePath, content, sshclient.WriteOptions{
		Overwrite:    options.Overwrite,
		Mode:         options.Mode,
		CreateParent: options.CreateParent,
	})
}

// Download 把映射后的本机文件复制到目标路径。
func (t *fakeRemoteTransfer) Download(
	ctx context.Context,
	remotePath, localPath string,
	options sshclient.DownloadOptions,
) error {
	source, err := t.localPath(remotePath)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if options.MaxBytes > 0 && int64(len(content)) > options.MaxBytes {
		return fmt.Errorf("%w: 文件超过上限", sshclient.ErrRemoteSizeExceeded)
	}
	if options.CreateParent {
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(localPath, content, 0o600)
}

// MkdirAll 递归创建映射后的本机目录。
func (t *fakeRemoteTransfer) MkdirAll(ctx context.Context, remotePath string) error {
	target, err := t.localPath(remotePath)
	if err != nil {
		return err
	}
	return os.MkdirAll(target, 0o755)
}

// Stat 返回映射后的本机路径属性。
func (t *fakeRemoteTransfer) Stat(ctx context.Context, remotePath string) (sshclient.FileInfo, error) {
	return t.stat(remotePath)
}

// Lstat 与 Stat 一致：替身不模拟符号链接。
func (t *fakeRemoteTransfer) Lstat(ctx context.Context, remotePath string) (sshclient.FileInfo, error) {
	return t.stat(remotePath)
}

// stat 读取本机属性并转换为远程文件属性。
func (t *fakeRemoteTransfer) stat(remotePath string) (sshclient.FileInfo, error) {
	target, err := t.localPath(remotePath)
	if err != nil {
		return sshclient.FileInfo{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return sshclient.FileInfo{}, err
	}
	normalized, err := sshclient.NormalizeRemotePath(remotePath)
	if err != nil {
		return sshclient.FileInfo{}, err
	}
	kind := sshclient.FileKindOther
	switch {
	case info.IsDir():
		kind = sshclient.FileKindDirectory
	case info.Mode().IsRegular():
		kind = sshclient.FileKindRegular
	}
	return sshclient.FileInfo{
		Path:      normalized,
		Name:      info.Name(),
		Kind:      kind,
		Size:      info.Size(),
		Mode:      info.Mode(),
		ModTime:   info.ModTime(),
		IsDir:     info.IsDir(),
		IsRegular: info.Mode().IsRegular(),
	}, nil
}

// ReadDir 列出映射后的本机目录内容。
func (t *fakeRemoteTransfer) ReadDir(ctx context.Context, remotePath string) ([]sshclient.FileInfo, error) {
	target, err := t.localPath(remotePath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	infos := make([]sshclient.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, statErr := t.stat(strings.TrimSuffix(remotePath, "/") + "/" + entry.Name())
		if statErr != nil {
			return nil, statErr
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// Rename 重命名映射后的本机路径。
func (t *fakeRemoteTransfer) Rename(ctx context.Context, oldPath, newPath string) error {
	source, err := t.localPath(oldPath)
	if err != nil {
		return err
	}
	target, err := t.localPath(newPath)
	if err != nil {
		return err
	}
	return os.Rename(source, target)
}

// Remove 删除映射后的本机文件。
func (t *fakeRemoteTransfer) Remove(ctx context.Context, remotePath string) error {
	target, err := t.localPath(remotePath)
	if err != nil {
		return err
	}
	return os.Remove(target)
}

// ReadFile 读取映射后的本机文件内容。
func (t *fakeRemoteTransfer) ReadFile(
	ctx context.Context,
	remotePath string,
	maxBytes int64,
) ([]byte, error) {
	target, err := t.localPath(remotePath)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	if maxBytes > 0 && int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("%w: 文件超过上限", sshclient.ErrRemoteSizeExceeded)
	}
	return content, nil
}

// WriteFile 以原子方式写入映射后的本机文件。
func (t *fakeRemoteTransfer) WriteFile(
	ctx context.Context,
	remotePath string,
	content []byte,
	options sshclient.WriteOptions,
) error {
	target, err := t.localPath(remotePath)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(target); statErr == nil && !options.Overwrite {
		return fmt.Errorf("%w: %s", sshclient.ErrRemotePathExists, remotePath)
	}
	if options.CreateParent {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
	}
	mode := options.Mode
	if mode == 0 {
		mode = 0o644
	}
	return os.WriteFile(target, content, mode)
}

// RemoveAll 递归删除映射后的本机路径。
func (t *fakeRemoteTransfer) RemoveAll(ctx context.Context, remotePath string) error {
	target, err := t.localPath(remotePath)
	if err != nil {
		return err
	}
	return os.RemoveAll(target)
}

// Close 表示通道关闭。
func (t *fakeRemoteTransfer) Close() error {
	return nil
}

// remoteContainerFixture 是一套容器服务受控测试现场。
type remoteContainerFixture struct {
	t *testing.T
	// db 是本次现场使用的测试数据库。
	db *sqlite.DB
	// containers 是接入受控远程现场的容器服务。
	containers *app.ContainerService
	// host 是记录远程命令与容器状态的替身。
	host *fakeRemoteHost
	// ctx 是现场使用的上下文。
	ctx context.Context
	// workspace 是测试工作空间。
	workspace *testkit.Workspace
	// node 是写入数据库的执行节点。
	node domain.ExecutionHost
	// project 是写入数据库的项目。
	project domain.Project
	// version 是写入数据库的环境版本。
	version domain.EnvironmentVersion
}

// newRemoteContainerFixture 构造容器服务受控测试现场。
func newRemoteContainerFixture(t *testing.T) *remoteContainerFixture {
	t.Helper()
	workspace := testkit.New(t)
	workspace.Report()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	remoteRoot := workspace.Dir("remote")
	host := newFakeRemoteHost(t, remoteRoot)
	client := &fakeRemoteClient{host: host, transfer: &fakeRemoteTransfer{host: host}}
	sshService, err := app.NewSSHService(db, logger, app.SSHOptions{
		KnownHostsPath: filepath.Join(workspace.Dir("ssh"), "known_hosts"),
		Connector: func(
			ctx context.Context,
			node domain.ExecutionHost,
		) (*app.SSHConnection, error) {
			return &app.SSHConnection{
				Host:   node,
				Client: client,
				Home:   remoteHome,
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("构造 SSH 服务失败: %v", err)
	}
	containers, err := app.NewContainerService(app.DockerServiceOptions{
		SSH:    sshService,
		Store:  db,
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("构造容器服务失败: %v", err)
	}
	now := time.Now().UTC()
	environment := domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"}
	project, err := domain.NewProject(domain.ProjectInput{
		ID:                 containerTestProjectID,
		Name:               "容器受控测试项目",
		RepositoryURL:      "ssh://git.example.com/team/repo",
		BaseBranch:         "main",
		Environment:        environment,
		EnvironmentVersion: containerTestVersionLabel,
	}, now)
	if err != nil {
		t.Fatalf("构造项目失败: %v", err)
	}
	if err := db.CreateProject(ctx, project); err != nil {
		t.Fatalf("写入项目失败: %v", err)
	}
	node, err := domain.NewExecutionHost(
		containerTestHostID,
		"容器受控测试节点",
		"10.0.0.31",
		22,
		"deploy",
		remoteWorkspace,
		now,
		domain.WithPrivateKey(testkit.PrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	node.ValidationStatus = domain.ValidationStatusNormal
	if err := db.CreateExecutionHost(ctx, node); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	version := domain.EnvironmentVersion{
		HostID:            node.ID,
		Ref:               environment,
		Number:            1,
		DockerfileSHA256:  containerTestBuildHash,
		BuildConfigSHA256: containerTestBuildHash,
		ContextSHA256:     containerTestBuildHash,
		ParentVersion:     containerTestVersionLabel,
		ImageRef:          "agentflow-language-go:v0001-444444444444",
		ImageID:           containerTestImageID,
		Status:            domain.BuildStatusSucceeded,
		CreatedAt:         now,
	}
	builtAt := now
	version.BuiltAt = &builtAt
	if err := db.CreateEnvironmentVersion(ctx, version); err != nil {
		t.Fatalf("写入环境版本失败: %v", err)
	}
	return &remoteContainerFixture{
		t:          t,
		db:         db,
		containers: containers,
		host:       host,
		ctx:        ctx,
		workspace:  workspace,
		node:       node,
		project:    project,
		version:    version,
	}
}

// seedContainer 写入一条锚定真实远程现场的容器记录。
//
// 容器在替身中登记为运行中，标签按规划会话标识写入；调用方随后可以按用例需要覆盖
// 标签、远程 ID 或运行状态。
func (f *remoteContainerFixture) seedContainer(
	planningSessionID domain.ID,
	state domain.ContainerState,
) domain.Container {
	f.t.Helper()
	container, err := domain.NewContainer(
		"",
		f.node.ID,
		"agentflow-remote-"+domain.NewID().String(),
		f.project.Environment,
		containerTestVersionLabel,
		containerTestBuildHash,
		containerTestImageID,
		time.Now().UTC(),
	)
	if err != nil {
		f.t.Fatalf("构造容器记录失败: %v", err)
	}
	container.DockerID = "docker-" + container.ID.String()
	container.State = state
	container.DockerState = domain.DockerStateRunning
	if err := f.db.CreateContainer(f.ctx, container); err != nil {
		f.t.Fatalf("写入容器记录失败: %v", err)
	}
	f.host.trackContainer(container, planningSessionID.String())
	return container
}

// seedPlanningSession 写入一个绑定容器与固定的 commit 的规划会话。
func (f *remoteContainerFixture) seedPlanningSession(
	container domain.Container,
	sourceCommit string,
) domain.PlanningSession {
	f.t.Helper()
	now := time.Now().UTC()
	session, err := domain.NewPlanningSession(f.project.ID, now)
	if err != nil {
		f.t.Fatalf("构造规划会话失败: %v", err)
	}
	if err := session.BeginAnalysis(now); err != nil {
		f.t.Fatalf("规划会话进入 analyzing 失败: %v", err)
	}
	hostID := f.node.ID
	if err := session.BindRuntime(&hostID, container.ID.String(), sourceCommit, now); err != nil {
		f.t.Fatalf("绑定规划运行环境失败: %v", err)
	}
	if err := f.db.CreatePlanningSession(f.ctx, session); err != nil {
		f.t.Fatalf("写入规划会话失败: %v", err)
	}
	return session
}

// loadContainer 重新读取容器记录的持久化事实。
func (f *remoteContainerFixture) loadContainer(containerID domain.ID) domain.Container {
	f.t.Helper()
	container, err := f.db.GetContainer(f.ctx, containerID)
	if err != nil {
		f.t.Fatalf("读取容器记录失败: %v", err)
	}
	return *container
}

// loadSession 重新读取规划会话的持久化事实。
func (f *remoteContainerFixture) loadSession(sessionID domain.ID) domain.PlanningSession {
	f.t.Helper()
	session, err := f.db.GetPlanningSession(f.ctx, sessionID)
	if err != nil {
		f.t.Fatalf("读取规划会话失败: %v", err)
	}
	return *session
}

// registryContent 返回远程容器登记清单的内容。
func (f *remoteContainerFixture) registryContent() string {
	f.t.Helper()
	layout, err := docker.NewLayout(remoteWorkspace)
	if err != nil {
		f.t.Fatalf("构造远程布局失败: %v", err)
	}
	target, err := f.host.remotePath(layout.ContainersRegistryPath())
	if err != nil {
		f.t.Fatalf("解析远程清单路径失败: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		f.t.Fatalf("读取远程容器清单失败: %v", err)
	}
	return string(content)
}

// markContainerStopped 把容器记录置为已停止并同步远程现场。
func (f *remoteContainerFixture) markContainerStopped(container domain.Container) error {
	updated := container
	if err := updated.MarkStopped(time.Now().UTC()); err != nil {
		return err
	}
	if err := f.db.UpdateContainer(f.ctx, updated); err != nil {
		return err
	}
	f.host.setRunning(container.DockerID, false)
	return nil
}

// agentRequest 构造一次容器内 Agent 调用请求。
func (f *remoteContainerFixture) agentRequest(
	container domain.Container,
	planningSessionID string,
	stopOnUncertain bool,
) app.ContainerAgentRequest {
	sessionID := strings.TrimSpace(planningSessionID)
	if sessionID == "" {
		sessionID = domain.NewID().String()
	}
	return app.ContainerAgentRequest{
		ContainerID:              container.ID.String(),
		RoleType:                 string(domain.RoleTypeRequirementAnalyst),
		Model:                    "fake/model-1",
		Prompt:                   "分析这次需求",
		SessionRoot:              f.workspace.SessionRoot(),
		SessionID:                sessionID,
		WorkDir:                  domain.DefaultContainerWorkDir,
		PlanningSessionID:        planningSessionID,
		StopContainerOnUncertain: stopOnUncertain,
	}
}
