package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/process"
)

// 后台启动的等待与转发参数。
const (
	// DefaultBackgroundWaitTimeout 是等待后台服务健康检查通过的最长时间。
	DefaultBackgroundWaitTimeout = 60 * time.Second
	// BackgroundLogSuffix 是后台服务日志文件的路径后缀，前缀是数据库文件路径。
	//
	// 日志与数据库放在一起：同一个数据目录既保存运行事实，也保存本次进程的输出，
	// 不新增第二处用户需要知道的位置。
	BackgroundLogSuffix = ".server.log"
	// backgroundPollInterval 是转发输出与健康检查的轮询间隔。
	backgroundPollInterval = 200 * time.Millisecond
	// backgroundHealthTimeout 是单次健康检查请求的超时时间。
	backgroundHealthTimeout = 2 * time.Second
	// backgroundTerminateGrace 是终止后台进程的宽限时间。
	backgroundTerminateGrace = 15 * time.Second
)

// BackgroundOptions 是以后台进程方式启动服务端的输入。
type BackgroundOptions struct {
	// Config 是已加载并校验的配置，必填：它决定监听地址与日志文件位置。
	Config *config.Config
	// Address 覆盖配置文件中的监听地址，为空时使用配置。
	Address string
	// Executable 是当前程序的可执行文件路径，必填。
	Executable string
	// Args 是后台进程的参数，不含可执行文件名。
	Args []string
	// Stdout 用于转发后台进程的输出，必填。
	Stdout io.Writer
	// WaitTimeout 是等待健康检查通过的最长时间，零值使用 DefaultBackgroundWaitTimeout。
	WaitTimeout time.Duration
}

// Background 是一个已启动并通过健康检查的后台服务。
type Background struct {
	pid     int
	address string
	logPath string
}

// PID 返回后台服务进程 ID。
func (b *Background) PID() int {
	return b.pid
}

// Address 返回后台服务实际监听地址。
func (b *Background) Address() string {
	return b.address
}

// LogPath 返回后台服务输出追加写入的日志文件路径。
func (b *Background) LogPath() string {
	return b.logPath
}

// StartBackground 以独立后台进程启动服务端，并转发其输出直到健康检查通过。
//
// 健康检查通过后返回后台服务信息，调用方可以退出：子进程在独立会话中继续运行，
// 输出继续追加写入日志文件。等待超时、子进程提前退出或调用方取消时，后台进程会被
// 终止并回收，不留下半个服务端。
func StartBackground(ctx context.Context, options BackgroundOptions) (*Background, error) {
	if options.Config == nil {
		return nil, fmt.Errorf("后台启动失败: 缺少配置")
	}
	if strings.TrimSpace(options.Executable) == "" {
		return nil, fmt.Errorf("后台启动失败: 缺少可执行文件路径")
	}
	if options.Stdout == nil {
		return nil, fmt.Errorf("后台启动失败: 缺少输出写入器")
	}
	address := resolveAddress(options.Config, options.Address)
	healthURL, err := backgroundHealthURL(address)
	if err != nil {
		return nil, err
	}
	logPath := backgroundLogPath(options.Config)
	if mkdirErr := os.MkdirAll(filepath.Dir(logPath), 0o755); mkdirErr != nil {
		return nil, fmt.Errorf("后台启动失败: 创建日志目录失败: %w", mkdirErr)
	}
	offset, err := backgroundLogOffset(logPath)
	if err != nil {
		return nil, err
	}
	child, err := process.StartBackground(process.BackgroundOptions{
		Command:    process.Command{Path: options.Executable, Args: options.Args},
		OutputPath: logPath,
	})
	if err != nil {
		return nil, fmt.Errorf("后台启动失败: %w", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, backgroundWaitTimeout(options.WaitTimeout))
	defer cancel()
	if waitErr := waitBackgroundReady(waitCtx, child, healthURL, logPath, offset, options.Stdout); waitErr != nil {
		terminateErr := child.Terminate(backgroundTerminateGrace)
		return nil, errors.Join(waitErr, terminateErr)
	}
	return &Background{pid: child.PID(), address: address, logPath: logPath}, nil
}

// backgroundLogPath 返回后台服务日志文件路径。
func backgroundLogPath(cfg *config.Config) string {
	return cfg.Storage.DatabasePath + BackgroundLogSuffix
}

// backgroundWaitTimeout 返回等待健康检查通过的最长时间。
func backgroundWaitTimeout(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	return DefaultBackgroundWaitTimeout
}

// backgroundHealthURL 把监听地址转换为健康检查地址。
//
// 后台模式的父进程只能按地址轮询健康检查，因此系统分配端口（端口 0）无法确定探测
// 目标，启动前直接失败，而不是盲目等待一个不存在的地址。
func backgroundHealthURL(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("后台启动失败: 监听地址无效: %s: %w", address, err)
	}
	if port == "" || port == "0" {
		return "", fmt.Errorf("后台启动失败: 监听地址必须使用固定端口: %s", address)
	}
	return "http://" + net.JoinHostPort(loopbackHost(host), port) + httpapi.PathHealth, nil
}

// loopbackHost 把未指定监听地址换成本机回环地址。
//
// 服务端默认监听回环地址；配置成 0.0.0.0 或 :: 时健康检查必须换成对应的回环地址，
// 否则轮询目标不是一个可连接的对端地址。
func loopbackHost(host string) string {
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsUnspecified() {
		return host
	}
	if ip.To4() != nil {
		return "127.0.0.1"
	}
	return "::1"
}

// backgroundLogOffset 返回日志文件当前长度，只转发本次启动产生的新输出。
func backgroundLogOffset(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("后台启动失败: 读取日志文件状态失败: %s: %w", path, err)
	}
	return info.Size(), nil
}

// waitBackgroundReady 转发后台服务输出，直到健康检查通过、进程退出或上下文结束。
func waitBackgroundReady(
	ctx context.Context,
	child *process.Background,
	healthURL, logPath string,
	offset int64,
	output io.Writer,
) error {
	follower := &logFollower{output: output, path: logPath, offset: offset}
	defer follower.close()
	client := &http.Client{Timeout: backgroundHealthTimeout}
	ticker := time.NewTicker(backgroundPollInterval)
	defer ticker.Stop()
	for {
		follower.copyNew()
		if healthReady(ctx, client, healthURL) {
			// 就绪后再转发一次：启动日志与健康检查几乎同时发生，退出前要把最后一段
			// 输出交给用户，避免后台服务的启动信息只留在日志文件里。
			time.Sleep(backgroundPollInterval)
			follower.copyNew()
			return nil
		}
		select {
		case <-ctx.Done():
			follower.copyNew()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("等待后台服务健康检查超时: %s（日志: %s）", healthURL, logPath)
			}
			return fmt.Errorf("等待后台服务健康检查被取消: %w", ctx.Err())
		case <-child.Exited():
			follower.copyNew()
			if exitErr := child.ExitErr(); exitErr != nil {
				return fmt.Errorf("后台服务进程提前退出: %w（日志: %s）", exitErr, logPath)
			}
			return fmt.Errorf("后台服务进程提前退出（日志: %s）", logPath)
		case <-ticker.C:
		}
	}
}

// healthReady 请求一次健康检查，只有整体状态为 ok 才算就绪。
func healthReady(ctx context.Context, client *http.Client, healthURL string) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return false
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer func() {
		_ = response.Body.Close()
	}()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode == http.StatusOK
}

// logFollower 从指定偏移开始转发后台服务的日志文件。
//
// 后台进程写文件而不是写管道，因此父进程退出后它仍能继续写；父进程只在等待健康检查
// 期间读取文件并转发到自己的输出。
type logFollower struct {
	output io.Writer
	path   string
	offset int64
	file   *os.File
}

// copyNew 把日志文件新增的内容复制到输出。
func (f *logFollower) copyNew() {
	if f.output == nil {
		return
	}
	if f.file == nil {
		file, err := os.Open(f.path)
		if err != nil {
			return
		}
		if _, err := file.Seek(f.offset, io.SeekStart); err != nil {
			_ = file.Close()
			return
		}
		f.file = file
	}
	written, err := io.Copy(f.output, f.file)
	f.offset += written
	if err != nil {
		f.close()
	}
}

// close 关闭日志文件句柄。
func (f *logFollower) close() {
	if f.file == nil {
		return
	}
	_ = f.file.Close()
	f.file = nil
}
