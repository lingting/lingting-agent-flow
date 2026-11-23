// 本文件提供迁移后用例所需的本机测试服务，对应源文件 tests/e2e/cli/host_test.go 里的
// startProbeServer 与 startSSHHandshakeServer。
//
// 两个服务都只监听 127.0.0.1 的临时端口，由操作系统分配，随用例结束关闭。
package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
	"golang.org/x/crypto/ssh"
)

// startProbeServer 启动一个返回固定协议标识的本地 TCP 服务。
func startProbeServer(t *testrunner.Context, banner string) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动测试服务失败: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() {
					_ = conn.Close()
				}()
				if _, err := conn.Write([]byte(banner)); err != nil {
					return
				}
				<-time.After(200 * time.Millisecond)
			}(conn)
		}
	}()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址类型不符: %T", listener.Addr())
	}
	return address.IP.String(), address.Port
}

// startSSHHandshakeServer 启动一个只完成 SSH 握手的最小服务端。
//
// 它用于验证主机密钥探测与信任流程：服务端提供真实的主机密钥，但不接受任何
// 身份认证，因此客户端只会取得主机密钥指纹而不会建立已认证连接。
func startSSHHandshakeServer(t *testrunner.Context) (string, int) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成主机密钥失败: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("构造主机密钥签名器失败: %v", err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			return nil, errors.New("拒绝认证")
		},
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动测试 SSH 服务失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := listener.Close(); closeErr != nil {
			t.Logf("关闭测试 SSH 监听失败: %v", closeErr)
		}
	})
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() {
					_ = conn.Close()
				}()
				_, _, _, handshakeErr := ssh.NewServerConn(conn, config)
				if handshakeErr != nil {
					return
				}
			}(conn)
		}
	}()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址类型不符: %T", listener.Addr())
	}
	return address.IP.String(), address.Port
}

// startUnreachableAddress 申请一个临时端口后立即关闭监听，返回不可达的回环地址。
//
// host probe 与 host test 用它构造"连接必然失败"的节点，不依赖外部网络。
func startUnreachableAddress(t *testrunner.Context) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动测试监听失败: %v", err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址类型不符: %T", listener.Addr())
	}
	port := address.Port
	if closeErr := listener.Close(); closeErr != nil {
		t.Fatalf("关闭测试监听失败: %v", closeErr)
	}
	return address.IP.String(), port
}
