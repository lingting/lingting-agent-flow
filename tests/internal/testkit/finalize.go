// Package testkit 提供 AgentFlow 测试统一使用的工作空间与环境辅助组件。
//
// 该文件提供统一的包级最终核验入口：测试包的 TestMain 通过 RegisterFinalizer
// 注册核验函数，Main 在 m.Run 结束之后、日志刷新之前执行它们，使核验结论与
// 清理结果写进同一份 test.log，并让核验失败真实改变测试进程退出码。
package testkit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/sshclient"
)

// finalizeTimeout 是全部包级最终核验的总超时。
const finalizeTimeout = 10 * time.Minute

// remoteFinalizeDialTimeout 是最终核验建立 SSH 连接与解析远程 Home 的超时。
const remoteFinalizeDialTimeout = 30 * time.Second

// Finalizer 是包级最终核验函数。
//
// 返回 skipped=true 表示本次核验未执行（例如真实远程测试未启用）；这种情况必须
// 如实报告为 SKIP，不得当作通过。
type Finalizer func(ctx context.Context) (skipped bool, err error)

// finalizerEntry 是一个已注册的最终核验。
type finalizerEntry struct {
	name string
	fn   Finalizer
}

// finalizerRegistry 保存本次测试进程注册的最终核验。
var finalizerRegistry struct {
	mu    sync.Mutex
	items []finalizerEntry
}

// RegisterFinalizer 注册一个在全部测试结束后、日志关闭之前执行的最终核验。
//
// 必须在 testkit.Main 之前调用（即在 TestMain 内），否则不会被执行。
func RegisterFinalizer(name string, fn Finalizer) {
	if fn == nil {
		return
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		trimmed = "unnamed"
	}
	finalizerRegistry.mu.Lock()
	defer finalizerRegistry.mu.Unlock()
	finalizerRegistry.items = append(finalizerRegistry.items, finalizerEntry{name: trimmed, fn: fn})
}

// snapshotFinalizers 返回已注册的最终核验列表。
func snapshotFinalizers() []finalizerEntry {
	finalizerRegistry.mu.Lock()
	defer finalizerRegistry.mu.Unlock()
	items := make([]finalizerEntry, len(finalizerRegistry.items))
	copy(items, finalizerRegistry.items)
	return items
}

// RunFinalizers 依次执行给定的最终核验并返回是否存在失败。
//
// 该导出形式与统一入口使用同一套语义（失败改变退出码、跳过不等于通过、panic 按
// 失败处理），便于自定义测试入口或测试验证这套语义本身。
func RunFinalizers(ctx context.Context, finalizers ...Finalizer) bool {
	items := make([]finalizerEntry, 0, len(finalizers))
	for index, finalizer := range finalizers {
		items = append(items, finalizerEntry{
			name: fmt.Sprintf("finalizer-%d", index),
			fn:   finalizer,
		})
	}
	return runFinalizers(ctx, items)
}

// runFinalizers 依次执行最终核验，返回是否存在失败。
//
// 核验输出写到当前标准输出：此时它仍指向运行日志，因此结论与测试结论出现在同一份
// test.log 中。核验函数 panic 不会让进程崩溃，而是按失败处理。
func runFinalizers(ctx context.Context, items []finalizerEntry) (failed bool) {
	for _, item := range items {
		skipped, err := runFinalizer(ctx, item)
		switch {
		case err != nil:
			failed = true
			fmt.Fprintf(os.Stdout, "FINALIZER name=%s status=FAIL error=%v\n", item.name, err)
		case skipped:
			fmt.Fprintf(os.Stdout, "FINALIZER name=%s status=SKIP\n", item.name)
		default:
			fmt.Fprintf(os.Stdout, "FINALIZER name=%s status=PASS\n", item.name)
		}
	}
	return failed
}

// runFinalizer 执行单个最终核验，并把它自身的 panic 转换为错误。
func runFinalizer(ctx context.Context, item finalizerEntry) (skipped bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			skipped = false
			err = fmt.Errorf("最终核验 panic: %v", recovered)
		}
	}()
	return item.fn(ctx)
}

// runRegisteredFinalizers 执行本次进程注册的最终核验。
func runRegisteredFinalizers() bool {
	items := snapshotFinalizers()
	if len(items) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()
	return runFinalizers(ctx, items)
}

// RemoteDockerSession 是包级最终核验使用的远程 Docker 连接。
type RemoteDockerSession struct {
	// Runner 是真实远程 Docker 执行器。
	Runner *docker.Runner
	// Close 关闭底层 SSH 连接。
	Close func() error
	// SkipReason 非空表示本次不执行核验（远程测试未启用或缺少目标配置）。
	SkipReason string
}

// OpenRemoteDockerForFinalize 建立用于容器零残留核验的远程连接。
//
// 配置来源与各测试包一致（tests/config/ssh.yml 与 docker.yml），主机密钥校验不可
// 关闭；远程测试未启用或目标缺失时返回 SkipReason，而不是虚构核验通过。
func OpenRemoteDockerForFinalize(ctx context.Context) (RemoteDockerSession, error) {
	dir, err := configDirPath()
	if err != nil {
		return RemoteDockerSession{}, err
	}
	dockerPath := filepath.Join(dir, DockerConfigFile)
	var dockerDoc dockerDocument
	if _, decodeErr := decodeDocument(dockerPath, &dockerDoc); decodeErr != nil {
		return RemoteDockerSession{}, decodeErr
	}
	if !dockerDoc.Docker.RemoteEnabled {
		return RemoteDockerSession{
			SkipReason: fmt.Sprintf("请把 %s 中的 docker.remote_enabled 设为 true", dockerPath),
		}, nil
	}
	sshPath := filepath.Join(dir, SSHConfigFile)
	var sshDoc sshDocument
	if _, decodeErr := decodeDocument(sshPath, &sshDoc); decodeErr != nil {
		return RemoteDockerSession{}, decodeErr
	}
	sshConfig := sshDoc.SSH
	if !sshConfig.Enabled {
		return RemoteDockerSession{
			SkipReason: fmt.Sprintf("请把 %s 中的 ssh.enable 设为 true", sshPath),
		}, nil
	}
	host := strings.TrimSpace(sshConfig.Host)
	user := strings.TrimSpace(sshConfig.User)
	if host == "" || user == "" {
		return RemoteDockerSession{
			SkipReason: fmt.Sprintf("缺少 %s 中的 ssh.host / ssh.user", sshPath),
		}, nil
	}
	keyPath := strings.TrimSpace(sshConfig.PrivateKeyPath)
	if keyPath == "" {
		return RemoteDockerSession{}, fmt.Errorf(
			"真实远程测试必须设置私钥路径（本版本只支持私钥认证）: %s",
			sshPath,
		)
	}
	port := sshConfig.Port
	if port <= 0 {
		port = DefaultSSHPort
	}
	if port > 65535 {
		return RemoteDockerSession{}, fmt.Errorf("ssh.port 不是合法端口: %d", port)
	}
	snapshotRoot, err := os.MkdirTemp(runRootInternal(), "finalize-")
	if err != nil {
		return RemoteDockerSession{}, fmt.Errorf("创建最终核验工作目录失败: %w", err)
	}
	sources, _, err := resolveKnownHostsSources()
	if err != nil {
		return RemoteDockerSession{}, err
	}
	snapshots, err := snapshotKnownHosts(snapshotRoot, sources)
	if err != nil {
		return RemoteDockerSession{}, err
	}
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{TrustedPaths: snapshots})
	if err != nil {
		return RemoteDockerSession{}, fmt.Errorf("构造主机密钥库失败: %w", err)
	}
	dialer, err := sshclient.NewDialer(sshclient.DialOptions{
		HostKeys:       store,
		ConnectTimeout: remoteFinalizeDialTimeout,
	})
	if err != nil {
		return RemoteDockerSession{}, fmt.Errorf("构造连接器失败: %w", err)
	}
	dialCtx, cancel := context.WithTimeout(ctx, remoteFinalizeDialTimeout)
	defer cancel()
	result, err := dialer.Dial(dialCtx, sshclient.Endpoint{
		Host: host, Port: port, Username: user,
	}, sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: keyPath,
	})
	if err != nil {
		return RemoteDockerSession{}, fmt.Errorf("最终核验连接真实远程机器失败: %w", err)
	}
	if result.HostKeyVerdict != sshclient.VerdictTrusted {
		_ = result.Client.Close()
		return RemoteDockerSession{}, fmt.Errorf("主机密钥应已被信任，实际: %s", result.HostKeyVerdict)
	}
	runner, err := docker.NewRunner(result.Client, docker.RunnerOptions{})
	if err != nil {
		_ = result.Client.Close()
		return RemoteDockerSession{}, fmt.Errorf("构造 Docker 执行器失败: %w", err)
	}
	return RemoteDockerSession{
		Runner: runner,
		Close:  result.Client.Close,
	}, nil
}

// RegisterContainerFinalizer 注册"测试容器零残留"的包级最终核验。
//
// 该核验在所有测试（含 t.Parallel 用例）结束后执行，使用真实远程 docker ps -a
// 清单确认本次运行登记的容器已全部消失；核验失败会让测试进程以非零码退出。
func RegisterContainerFinalizer(run *ContainerRun) {
	if run == nil {
		return
	}
	name := "remote-container-cleanup"
	if label := run.Label(); label != "" {
		name += "-" + label
	}
	RegisterFinalizer(name, func(ctx context.Context) (bool, error) {
		session, err := OpenRemoteDockerForFinalize(ctx)
		if err != nil {
			return false, err
		}
		if session.SkipReason != "" {
			fmt.Fprintf(
				os.Stdout,
				"WARN [SKIP] 测试容器最终核验未执行（运行 %s）: %s\n",
				run.Label(),
				session.SkipReason,
			)
			return true, nil
		}
		defer func() {
			if session.Close == nil {
				return
			}
			if closeErr := session.Close(); closeErr != nil {
				fmt.Fprintf(os.Stdout, "WARN 关闭最终核验连接失败: %v\n", closeErr)
			}
		}()
		finalizer := ContainerFinalizer{Run: run, Runtime: session.Runner}
		return false, finalizer.Finalize(ctx)
	})
}
