package planning_remote

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// breakableSSH 是测试专用的 SSH 连接包装器。
//
// 它只用真实认证建立连接（主机密钥校验与生产一致），并在测试需要时关闭已经建立的连接，
// 用来制造"规划 Pi 执行期间 SSH 连接突然中断"的真实现场。它不替换 SSH、Docker 或 Pi 的
// 实际执行：认证、握手、命令执行与文件传输全部由真实客户端完成。
type breakableSSH struct {
	// knownHosts 是参与主机密钥校验的只读快照。
	knownHosts []string

	mu     sync.Mutex
	active []*breakableClient
}

// breakableClient 是一个可被测试关闭的真实连接。
type breakableClient struct {
	sshclient.Client
	owner *breakableSSH

	closeOnce sync.Once
	closeErr  error
}

// Break 关闭底层连接，模拟链路中断。
func (c *breakableClient) Break() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.Client.Close()
	})
	return c.closeErr
}

// DialUnixSocket 把远端 Unix 套接字通道能力转发给真实连接。
//
// Engine API 版本探测需要该能力；包装器只转发调用，不改变通道语义。
func (c *breakableClient) DialUnixSocket(ctx context.Context, socketPath string) (net.Conn, error) {
	dialer, ok := c.Client.(sshclient.UnixSocketDialer)
	if !ok {
		return nil, fmt.Errorf("底层连接不支持访问远端 Unix 套接字")
	}
	return dialer.DialUnixSocket(ctx, socketPath)
}

// Close 关闭连接并从活跃集合中移除。
func (c *breakableClient) Close() error {
	err := c.Break()
	c.owner.remove(c)
	return err
}

// newBreakableSSH 构造一个真实的连接包装器。
func newBreakableSSH(knownHosts []string) *breakableSSH {
	return &breakableSSH{knownHosts: knownHosts}
}

// connector 返回给 app.SSHService 使用的连接函数。
//
// 每次调用都用生产同一套主机密钥校验与私钥认证建立真实连接，然后把客户端包装成可中断的
// 形式；包装只改变连接的生命周期入口，不改变任何命令语义。
func (b *breakableSSH) connector(
	ctx context.Context,
	host domain.ExecutionHost,
) (*app.SSHConnection, error) {
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: b.knownHosts,
	})
	if err != nil {
		return nil, fmt.Errorf("构造主机密钥库失败: %w", err)
	}
	dialer, err := sshclient.NewDialer(sshclient.DialOptions{
		HostKeys:       store,
		ConnectTimeout: 20 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("构造连接器失败: %w", err)
	}
	privateKey := strings.TrimSpace(host.PrivateKeyPath)
	if privateKey == "" {
		return nil, fmt.Errorf("执行节点 %s 缺少私钥路径", host.ID)
	}
	result, err := dialer.Dial(ctx, sshclient.Endpoint{
		Host:     host.Host,
		Port:     host.Port,
		Username: host.Username,
	}, sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: privateKey,
	})
	if err != nil {
		return nil, err
	}
	if result.HostKeyVerdict != sshclient.VerdictTrusted {
		_ = result.Client.Close()
		return nil, fmt.Errorf("主机密钥应已被信任，实际: %s", result.HostKeyVerdict)
	}
	home, err := sshclient.ResolveHome(ctx, result.Client, planningOpTimeout)
	if err != nil {
		_ = result.Client.Close()
		return nil, err
	}
	wrapped := &breakableClient{Client: result.Client, owner: b}
	b.add(wrapped)
	return &app.SSHConnection{
		Host:    host,
		Client:  wrapped,
		Home:    home,
		HostKey: result.HostKey,
	}, nil
}

// add 登记一个活跃连接。
func (b *breakableSSH) add(client *breakableClient) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.active = append(b.active, client)
}

// remove 移除一个已关闭的连接。
func (b *breakableSSH) remove(client *breakableClient) {
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := b.active[:0]
	for _, item := range b.active {
		if item != client {
			kept = append(kept, item)
		}
	}
	b.active = kept
}

// breakActive 关闭当前全部活跃连接，返回实际关闭的连接数。
//
// 调用时机固定为"已经通过独立控制连接确认 Pi 进程在运行"：此时活跃连接就是承载 Pi 执行
// 的那一次调用使用的连接。
func (b *breakableSSH) breakActive() int {
	b.mu.Lock()
	targets := append([]*breakableClient(nil), b.active...)
	b.mu.Unlock()
	for _, target := range targets {
		if err := target.Break(); err != nil {
			fmt.Fprintf(os.Stdout, "WARN 关闭测试 SSH 连接返回错误: %v\n", err)
		}
	}
	return len(targets)
}

// TestRemotePlanningSSHInterruptionTermination 验收真实 SSH 中断后的远程 Pi 终止。
//
// 覆盖：在真实 Pi 调用执行期间关闭该次调用使用的真实 SSH 连接、用独立的控制连接核验远端
// 现场、核验规划容器按设计被停止且 Docker 确认退出、本地状态与绑定真实反映结论，以及中断
// 之后仍能复用原容器继续同一上下文。
func TestRemotePlanningSSHInterruptionTermination(t *testing.T) {
	t.Parallel()
	breaker := newBreakableSSH(testkit.KnownHostsSources(t))
	f := newFixtureWith(t, fixtureOptions{
		sshOptions: app.SSHOptions{Connector: breaker.connector},
	})
	session := f.newSession(t, "SSH 中断验收：先记录一个标记，再在中断后核对。")
	detail := f.prepare(t, session)
	defer f.release(t, session)
	f.injectPiConfig(t, detail.Container.ID)

	marker := "COMET-" + f.suffix
	first := f.runRoleOK(t, session, detail, domain.RoleTypeRequirementAnalyst,
		"请记住这个唯一标记："+marker+"。只需回复 OK。")
	analystSessionID, _ := f.sessionRoleIDs(t, session)
	archiveBefore := mustReadFile(t, first.ArchivePath)

	// 第二轮在真实 Pi 执行期间断开连接。
	type outcome struct {
		result app.PlanningAgentResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := f.runRoleWithContext(f.ctx, session, domain.RoleTypeRequirementAnalyst,
			"请用三句话描述仓库结构，然后等待。")
		done <- outcome{result: result, err: err}
	}()
	f.waitForPiProcess(t, detail.Container.DockerID, 2*time.Minute)
	brokenCount := breaker.breakActive()
	if brokenCount == 0 {
		t.Fatalf("没有活跃的 SSH 连接可中断")
	}
	t.Logf("SSH_CONNECTIONS_BROKEN count=%d", brokenCount)

	var collected outcome
	select {
	case collected = <-done:
	case <-time.After(8 * time.Minute):
		t.Fatalf("SSH 中断后的规划调用未在 8 分钟内收敛")
	}
	t.Logf(
		"SSH_INTERRUPTED_CALL err=%v %s",
		collected.err,
		describeCallResult(collected.result),
	)
	if collected.err == nil {
		t.Fatalf("SSH 连接中断后规划调用不应报告成功")
	}
	if !collected.result.Result.RemoteTerminationConfirmed {
		t.Fatalf(
			"SSH 中断后没有确认远端终止: %s",
			truncate(collected.result.Result.RemoteTerminationError, 600),
		)
	}

	// 用独立控制连接核验远端现场：容器已退出，且不再有 Pi 进程。
	f.requireContainerStopped(t, detail.Container.DockerID)
	f.requirePiProcessGone(t, detail.Container.DockerID)

	// 本地记录必须与远端事实一致：已确认停止，而不是仍然 running。
	record := f.assertLocalContainerState(t, detail.Container.ID, domain.ContainerStateStopped)
	if record.DockerState != domain.DockerStateExited {
		t.Fatalf("本地记录的 Docker 状态不是 exited: %s", record.DockerState)
	}
	f.assertBindingIntact(t, session, detail)

	// 中断的调用不得覆盖最后一份完整归档。
	if current := mustReadFile(t, first.ArchivePath); string(current) != string(archiveBefore) {
		t.Fatalf("SSH 中断的调用覆盖了最后一份完整归档: %d → %d 字节",
			len(archiveBefore), len(current))
	}

	// 中断之后仍能复用同一个容器继续同一上下文。
	restarted := f.restartPlanningContainer(t, session, detail)
	f.injectPiConfig(t, restarted.Container.ID)
	second := f.runRoleOK(t, session, restarted, domain.RoleTypeRequirementAnalyst,
		"上一轮我给你的唯一标记是什么？只回复该标记本身，不要解释。")
	if !strings.Contains(second.Result.FinalText, marker) {
		t.Fatalf("SSH 中断恢复后没有恢复历史上下文：期望包含 %q，实际 %q",
			marker, truncate(second.Result.FinalText, 400))
	}
	if second.RoleSessionID != analystSessionID {
		t.Fatalf("SSH 中断恢复后角色会话标识被改变: %s → %s", analystSessionID, second.RoleSessionID)
	}
	if !second.Result.StableSession.Restored {
		t.Fatalf("SSH 中断恢复后没有从本机归档恢复历史 Session")
	}
	t.Logf(
		"SSH_INTERRUPTION_OK container=%s docker_id=%s archive=%s",
		detail.Container.ID,
		detail.Container.DockerID,
		second.ArchivePath,
	)
}
