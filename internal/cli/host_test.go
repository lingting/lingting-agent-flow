package cli_test

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hostConfig 生成一份指向临时数据库的执行节点测试配置。
func hostConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	content := strings.ReplaceAll(
		minimalConfig,
		".agentflow/agentflow.db",
		filepath.ToSlash(filepath.Join(dir, "agentflow.db")),
	)
	return writeConfig(t, content)
}

// TestHostHelp 校验 host 命令组暴露全部子命令。
func TestHostHelp(t *testing.T) {
	code, stdout, stderr := run(t, "host", "--help")
	if code != 0 {
		t.Fatalf("host --help 应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"add", "list", "show", "set-default", "probe", "remove"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("帮助输出缺少 %s，实际: %s", expected, stdout)
		}
	}
}

// TestHostAddRequiresFlags 校验新增节点要求显式指定主机与用户名。
func TestHostAddRequiresFlags(t *testing.T) {
	configPath := hostConfig(t)
	code, _, stderr := run(t, "host", "add", "--config", configPath)
	if code == 0 {
		t.Fatal("缺少必需参数时应返回非零退出码")
	}
	for _, flag := range []string{"host", "user"} {
		if !strings.Contains(stderr, flag) {
			t.Fatalf("错误信息应指出缺少 %s，实际: %s", flag, stderr)
		}
	}
}

// TestHostLifecycle 校验新增、列出、查看、设默认与删除的完整流程。
func TestHostLifecycle(t *testing.T) {
	configPath := hostConfig(t)
	code, stdout, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-a",
		"--name", "构建节点",
		"--host", "10.0.0.11",
		"--user", "deploy",
	)
	if code != 0 {
		t.Fatalf("新增节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"node-a", "构建节点", "10.0.0.11:22"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("新增输出缺少 %s，实际: %s", expected, stdout)
		}
	}
	if !strings.Contains(stdout, "~/.lingting/agentflow") {
		t.Fatalf("未指定 --workspace 时应使用默认路径，实际: %s", stdout)
	}

	code, stdout, stderr = run(t, "host", "list", "--config", configPath)
	if code != 0 {
		t.Fatalf("列出节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"node-a", "构建节点", "10.0.0.11:22", "deploy"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("列表输出缺少 %s，实际: %s", expected, stdout)
		}
	}

	code, stdout, stderr = run(t, "host", "show", "node-a", "--config", configPath)
	if code != 0 {
		t.Fatalf("查看节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "10.0.0.11") || !strings.Contains(stdout, "~/.lingting/agentflow") {
		t.Fatalf("详情输出不符: %s", stdout)
	}

	code, stdout, stderr = run(t, "host", "set-default", "node-a", "--config", configPath)
	if code != 0 {
		t.Fatalf("设置默认节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "默认执行节点已设置") {
		t.Fatalf("输出不符: %s", stdout)
	}

	code, stdout, stderr = run(t, "host", "remove", "node-a", "--config", configPath)
	if code != 0 {
		t.Fatalf("删除节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "执行节点已删除") {
		t.Fatalf("输出不符: %s", stdout)
	}

	code, _, stderr = run(t, "host", "show", "node-a", "--config", configPath)
	if code == 0 {
		t.Fatal("删除后查看应返回非零退出码")
	}
	if !strings.Contains(stderr, "记录不存在") {
		t.Fatalf("错误信息应说明记录不存在，实际: %s", stderr)
	}
}

// TestHostAddRejectsInvalidInput 校验非法参数不会写入数据库。
func TestHostAddRejectsInvalidInput(t *testing.T) {
	configPath := hostConfig(t)
	cases := map[string][]string{
		"端口超出范围":   {"--host", "10.0.0.11", "--user", "deploy", "--port", "70000"},
		"工作空间相对路径": {"--host", "10.0.0.11", "--user", "deploy", "--workspace", "agentflow"},
		"主机地址非法":   {"--host", "10.0.0.11:22", "--user", "deploy"},
	}
	for name, extra := range cases {
		args := append([]string{"host", "add", "--config", configPath}, extra...)
		if code, _, _ := run(t, args...); code == 0 {
			t.Fatalf("%s 应返回非零退出码", name)
		}
	}
	code, stdout, stderr := run(t, "host", "list", "--config", configPath)
	if code != 0 {
		t.Fatalf("列出节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "尚未配置执行节点") {
		t.Fatalf("非法输入不应写入节点，实际: %s", stdout)
	}
}

// TestHostSetDefaultRejectsUnknownID 校验设置默认节点时节点不存在。
func TestHostSetDefaultRejectsUnknownID(t *testing.T) {
	configPath := hostConfig(t)
	code, _, stderr := run(t, "host", "set-default", "missing", "--config", configPath)
	if code == 0 {
		t.Fatal("不存在的节点应返回非零退出码")
	}
	if !strings.Contains(stderr, "记录不存在") {
		t.Fatalf("错误信息应说明记录不存在，实际: %s", stderr)
	}
}

// startProbeServer 启动一个返回固定协议标识的本地 TCP 服务。
func startProbeServer(t *testing.T, banner string) (string, int) {
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

// TestHostProbeDetectsSSH 校验探测成功时明确说明尚未认证。
func TestHostProbeDetectsSSH(t *testing.T) {
	configPath := hostConfig(t)
	host, port := startProbeServer(t, "SSH-2.0-OpenSSH_9.6\r\n")
	addArgs := []string{
		"host", "add",
		"--config", configPath,
		"--id", "node-probe",
		"--host", host,
		"--port", fmt.Sprint(port),
		"--user", "deploy",
	}
	if code, _, stderr := run(t, addArgs...); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	code, stdout, stderr := run(t, "host", "probe", "node-probe", "--config", configPath)
	if code != 0 {
		t.Fatalf("探测应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"尚未执行身份认证", "ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("探测输出缺少 %s，实际: %s", expected, stdout)
		}
	}
	if strings.Contains(stdout, "登录成功") {
		t.Fatalf("探测输出不得声称登录成功: %s", stdout)
	}
}

// TestHostProbeNotSSH 校验非 SSH 服务返回非零退出码与明确说明。
func TestHostProbeNotSSH(t *testing.T) {
	configPath := hostConfig(t)
	host, port := startProbeServer(t, "HTTP/1.1 400 Bad Request\r\n\r\n")
	addArgs := []string{
		"host", "add",
		"--config", configPath,
		"--id", "node-http",
		"--host", host,
		"--port", fmt.Sprint(port),
		"--user", "deploy",
	}
	if code, _, stderr := run(t, addArgs...); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	code, stdout, stderr := run(t, "host", "probe", "node-http", "--config", configPath)
	if code == 0 {
		t.Fatal("非 SSH 服务应返回非零退出码")
	}
	if !strings.Contains(stdout, "not_ssh") {
		t.Fatalf("输出应标明结果类别，实际: %s", stdout)
	}
	if !strings.Contains(stderr, "未识别到有效的 SSH 服务") {
		t.Fatalf("错误信息应说明未识别到 SSH 服务，实际: %s", stderr)
	}
}

// TestHostProbeUnreachable 校验不可达地址的探测结果。
func TestHostProbeUnreachable(t *testing.T) {
	configPath := hostConfig(t)
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
	addArgs := []string{
		"host", "add",
		"--config", configPath,
		"--id", "node-down",
		"--host", address.IP.String(),
		"--port", fmt.Sprint(port),
		"--user", "deploy",
	}
	if code, _, stderr := run(t, addArgs...); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	code, stdout, _ := run(t, "host", "probe", "node-down", "--config", configPath)
	if code == 0 {
		t.Fatal("不可达地址应返回非零退出码")
	}
	if !strings.Contains(stdout, "unreachable") {
		t.Fatalf("输出应标明不可达，实际: %s", stdout)
	}
}
