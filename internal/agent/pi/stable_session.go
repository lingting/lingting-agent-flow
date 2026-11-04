package pi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
)

// 稳定 Session 的固定值。
const (
	// stableSessionDirMode 是本机归档目录权限：只允许当前用户进入。
	stableSessionDirMode os.FileMode = 0o700
	// stableSessionFileMode 是本机归档文件权限：只允许当前用户读取。
	stableSessionFileMode os.FileMode = 0o600
	// stableRestoreDirName 是远程宿主机上恢复中转目录的名称。
	stableRestoreDirName = "restore"
	// containerMkdirProgram 是容器内创建 Session 目录使用的可执行文件。
	//
	// 目录来自服务端生成的标识，且以参数切片形式传递，不经过 shell。
	containerMkdirProgram = "mkdir"
	// stableRestoreTimeout 是恢复历史 Session 的传输超时。
	stableRestoreTimeout = 5 * time.Minute
)

// stableSessionPaths 是稳定 Session 在容器内与本机归档中的位置。
type stableSessionPaths struct {
	// ContainerJSONL 是容器内固定的 Session 文件路径。
	ContainerJSONL string
	// ArchiveDir 是本机持久归档目录。
	ArchiveDir string
	// ArchiveJSONL 是本机可恢复的最新完整 Session 快照。
	ArchiveJSONL string
}

// enabled 判断本次调用是否使用稳定 Session。
func (p stableSessionPaths) enabled() bool {
	return strings.TrimSpace(p.ContainerJSONL) != ""
}

// resolveStableSession 校验稳定 Session 的两个路径。
//
// 容器内路径必须落在容器 Session 根目录之内，本机归档目录必须落在本机 Session 根目录
// 之内：这是"规划 Session 不写入项目代码目录、也不接受任意宿主机路径"的落点。
func (r *ContainerRunner) resolveStableSession(
	request agent.RunRequest,
) (stableSessionPaths, error) {
	if request.StableSession == nil {
		return stableSessionPaths{}, nil
	}
	if err := request.StableSession.Validate(); err != nil {
		return stableSessionPaths{}, err
	}
	containerPath, err := stableContainerSessionPath(
		r.containerSessionRoot(request.Container),
		request.StableSession.ContainerPath,
	)
	if err != nil {
		return stableSessionPaths{}, err
	}
	archiveDir, err := stableSessionArchiveDir(
		request.SessionRoot,
		request.StableSession.ArchiveDir,
	)
	if err != nil {
		return stableSessionPaths{}, err
	}
	return stableSessionPaths{
		ContainerJSONL: containerPath,
		ArchiveDir:     archiveDir,
		ArchiveJSONL:   filepath.Join(archiveDir, sessionFileName),
	}, nil
}

// containerSessionRoot 返回容器内的 Session 根目录。
func (r *ContainerRunner) containerSessionRoot(target *agent.ContainerTarget) string {
	root := strings.TrimSpace(target.SessionDir)
	if root == "" {
		root = DefaultContainerSessionDir
	}
	return path.Clean(root)
}

// stableContainerSessionPath 校验稳定 Session 路径位于容器 Session 根目录之内。
//
// 规则固定：绝对 POSIX 路径、已规范化（不含 "." / ".." / 重复分隔符）、位于根目录之下
// 至少两层（规划会话标识与角色会话标识）、文件名固定为 session.jsonl。
func stableContainerSessionPath(root, candidate string) (string, error) {
	trimmed := strings.TrimSpace(candidate)
	if !path.IsAbs(trimmed) {
		return "", fmt.Errorf("pi: 稳定 Session 容器内路径 %q 必须是绝对路径", trimmed)
	}
	cleaned := path.Clean(trimmed)
	if cleaned != trimmed {
		return "", fmt.Errorf("pi: 稳定 Session 容器内路径 %q 必须是规范化路径", trimmed)
	}
	if cleaned == root || !strings.HasPrefix(cleaned, root+"/") {
		return "", fmt.Errorf(
			"pi: 稳定 Session 容器内路径 %s 必须位于容器 Session 根目录 %s 之内",
			cleaned,
			root,
		)
	}
	relative := strings.TrimPrefix(cleaned, root+"/")
	segments := strings.Split(relative, "/")
	if len(segments) < 3 {
		return "", fmt.Errorf(
			"pi: 稳定 Session 容器内路径必须由 <规划会话标识>/<角色会话标识>/%s 组成，实际 %q",
			sessionFileName,
			relative,
		)
	}
	for _, segment := range segments[:len(segments)-1] {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("pi: 稳定 Session 容器内路径含有非法片段 %q", segment)
		}
	}
	if segments[len(segments)-1] != sessionFileName {
		return "", fmt.Errorf(
			"pi: 稳定 Session 容器内文件名必须是 %s，实际 %q",
			sessionFileName,
			segments[len(segments)-1],
		)
	}
	return cleaned, nil
}

// stableSessionArchiveDir 校验本机归档目录位于本机 Session 根目录之内。
func stableSessionArchiveDir(sessionRoot, candidate string) (string, error) {
	root, err := filepath.Abs(strings.TrimSpace(sessionRoot))
	if err != nil {
		return "", fmt.Errorf("pi: 解析 Session 根目录失败: %w", err)
	}
	absolute, err := filepath.Abs(strings.TrimSpace(candidate))
	if err != nil {
		return "", fmt.Errorf("pi: 解析本机 Session 归档目录失败: %w", err)
	}
	if absolute == root || !pathWithin(root, absolute) {
		return "", fmt.Errorf(
			"pi: 本机 Session 归档目录 %s 必须位于 Session 根目录 %s 之内",
			absolute,
			root,
		)
	}
	return absolute, nil
}

// restoreStableSession 在启动 Pi 之前把本机归档恢复到容器内的固定 Session 路径。
//
// 归档不存在表示首轮调用：允许 Pi 创建新的 Session。归档存在时先做基本结构校验，再经
// "本机 → 远程宿主机临时目录 → 容器内路径"三段传输写入；校验或传输失败都返回错误，
// 绝不静默丢弃历史上下文后创建新的空 Session。
func (r *ContainerRunner) restoreStableSession(
	ctx context.Context,
	request agent.RunRequest,
	stable stableSessionPaths,
) (bool, error) {
	info, err := os.Stat(stable.ArchiveJSONL)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("读取本机 Session 归档失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("本机 Session 归档 %s 不是普通文件", stable.ArchiveJSONL)
	}
	if info.Size() > maxSessionBytes {
		return false, fmt.Errorf(
			"本机 Session 归档 %s 超过上限 %d 字节",
			stable.ArchiveJSONL,
			int64(maxSessionBytes),
		)
	}
	if err := validateSessionFile(stable.ArchiveJSONL); err != nil {
		return false, fmt.Errorf("本机 Session 归档不可用: %w", err)
	}
	writer, ok := r.executor.(agent.ContainerFileWriter)
	if !ok {
		return false, fmt.Errorf("当前容器执行器不支持把 Session 写入容器，无法恢复历史上下文")
	}
	restoreCtx, cancel := context.WithTimeout(ctx, stableRestoreTimeout)
	defer cancel()
	hostPath := path.Join(
		hostExportDir(request.Container.ContainerID, request.InvocationID),
		stableRestoreDirName,
		containerSessionFileName,
	)
	if err := writer.UploadFromLocal(restoreCtx, stable.ArchiveJSONL, hostPath); err != nil {
		return false, fmt.Errorf("上传 Session 归档到远程宿主机失败: %w", err)
	}
	if err := r.ensureContainerDir(restoreCtx, request.Container, path.Dir(stable.ContainerJSONL)); err != nil {
		return false, err
	}
	if err := writer.CopyToContainer(
		restoreCtx,
		request.Container.DockerID,
		hostPath,
		stable.ContainerJSONL,
	); err != nil {
		// 写入容器失败时保留远程中转副本，便于人工排查或重新提取；本机归档不受影响。
		return false, fmt.Errorf("把 Session 归档写入容器失败: %w", err)
	}
	// 本机归档是权威副本且已校验通过：容器内写入成功后即可清理远程中转副本。
	r.cleanupHostFile(restoreCtx, hostPath)
	return true, nil
}

// ensureContainerDir 在容器内创建 Session 目录。
//
// docker cp 不会为目标文件创建父目录，因此恢复前先建立目录；命令以参数切片形式
// 传递，不经过 shell。
func (r *ContainerRunner) ensureContainerDir(
	ctx context.Context,
	target *agent.ContainerTarget,
	dir string,
) error {
	executed, err := r.executor.Exec(ctx, agent.ContainerExecRequest{
		DockerID:       target.DockerID,
		Command:        []string{containerMkdirProgram, "-p", dir},
		Timeout:        containerProbeTimeout,
		MaxOutputBytes: MaxOutputEventLength,
	})
	if err != nil {
		return fmt.Errorf("创建容器内 Session 目录失败: %w", err)
	}
	if !executed.Succeeded() {
		return fmt.Errorf(
			"创建容器内 Session 目录失败（退出码 %d）: %s",
			executed.ExitCode,
			agent.TruncateText(strings.TrimSpace(executed.Stderr), 512),
		)
	}
	return nil
}

// promoteStableSession 把本轮 Session 归档为可恢复的最新完整快照。
//
// 只有"本轮执行正常完成 + 本机单轮归档通过基本结构校验"时才替换最新快照：失败、超时、
// 取消或残缺的记录只保留在按调用标识分层的单轮归档中，不会覆盖最后一份可恢复的完整
// 历史。替换通过同目录临时文件加原子重命名完成，写入失败不会留下半份快照。
func (r *ContainerRunner) promoteStableSession(
	request agent.RunRequest,
	stable stableSessionPaths,
	files agent.SessionFiles,
	status agent.RunStatus,
	previous agent.StableSessionResult,
) (agent.StableSessionResult, error) {
	// 从恢复阶段的结果继续：本轮是否恢复了历史 Session 与归档结果无关，两者都要保留。
	result := previous
	result.ContainerPath = stable.ContainerJSONL
	result.ArchivePath = stable.ArchiveJSONL
	if strings.TrimSpace(files.JSONLPath) == "" {
		result.Error = "本轮没有可用的 Session 文件，上一次可恢复快照保持不变"
		return result, nil
	}
	if status != agent.RunCompleted {
		result.Error = "本轮执行未正常完成，只保留单轮归档，上一次可恢复快照保持不变"
		return result, nil
	}
	if err := validateSessionFile(files.JSONLPath); err != nil {
		result.Error = "本轮 Session 未通过结构校验，上一次可恢复快照保持不变: " + err.Error()
		return result, nil
	}
	if err := os.MkdirAll(stable.ArchiveDir, stableSessionDirMode); err != nil {
		result.Error = "创建本机 Session 归档目录失败: " + err.Error()
		return result, fmt.Errorf("%w: %s", agent.ErrSessionArchive, result.Error)
	}
	written, err := writeArchiveAtomically(
		files.JSONLPath,
		stable.ArchiveJSONL,
		request.InvocationID,
	)
	if err != nil {
		result.Error = err.Error()
		return result, fmt.Errorf("%w: %w", agent.ErrSessionArchive, err)
	}
	// 归档文件重新校验一次：只有确认本机已取得并校验文件，才认为本轮归档成功。
	if err := validateSessionFile(stable.ArchiveJSONL); err != nil {
		result.Error = "本机 Session 归档校验失败: " + err.Error()
		return result, fmt.Errorf("%w: %s", agent.ErrSessionArchive, result.Error)
	}
	result.Archived = true
	result.Bytes = written
	return result, nil
}

// writeArchiveAtomically 把单轮归档复制为最新快照。
//
// 先写同目录临时文件并校验字节数，再原子重命名到目标路径：读取方要么看到旧快照，
// 要么看到完整的新快照。临时文件权限保守，失败时删除，不留下半份文件。
func writeArchiveAtomically(source, target, invocationID string) (int64, error) {
	dir := filepath.Dir(target)
	temp := filepath.Join(dir, "."+sanitizeSegment(invocationID)+".tmp")
	if err := os.Remove(temp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("清理本机 Session 归档临时文件失败: %w", err)
	}
	input, err := os.Open(source)
	if err != nil {
		return 0, fmt.Errorf("读取单轮 Session 归档失败: %w", err)
	}
	defer func() {
		_ = input.Close()
	}()
	output, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, stableSessionFileMode)
	if err != nil {
		return 0, fmt.Errorf("创建本机 Session 归档临时文件失败: %w", err)
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, maxSessionBytes+1))
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(temp)
		return 0, fmt.Errorf("写入本机 Session 归档失败: %w", errors.Join(copyErr, closeErr))
	}
	if written > maxSessionBytes {
		_ = os.Remove(temp)
		return 0, fmt.Errorf("本机 Session 归档超过上限 %d 字节", int64(maxSessionBytes))
	}
	if err := os.Rename(temp, target); err != nil {
		_ = os.Remove(temp)
		return 0, fmt.Errorf("提交本机 Session 归档失败: %w", err)
	}
	return written, nil
}
