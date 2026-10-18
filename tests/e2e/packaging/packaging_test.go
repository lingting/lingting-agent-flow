package packaging_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// assetPattern 从入口页面中提取脚本与样式的引用路径。
var assetPattern = regexp.MustCompile(`(?:src|href)="(/[^"]+)"`)

// repoRoot 返回仓库根目录：从当前测试包的目录逐级向上找到 go.mod。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取当前目录失败: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("未能定位仓库根目录（未找到 go.mod）: %s", dir)
		}
		dir = parent
	}
}

// minimalConfig 是打包测试使用的最小配置，路径全部落在测试工作空间内。
func minimalConfig(workspace string) string {
	database := filepath.ToSlash(filepath.Join(workspace, "agentflow.db"))
	worktree := filepath.ToSlash(filepath.Join(workspace, "worktrees"))
	return fmt.Sprintf(`app:
  name: agentflow
  log_level: info
server:
  host: 127.0.0.1
  port: 0
storage:
  database_path: "%s"
workspace:
  worktree_root: "%s"
scheduler:
  max_concurrent_tasks: 1
  max_retries_per_step: 0
roles:
  - id: planner
    name: 架构规划师
    type: planner
    runner: pi
    model: "provider/model-id"
    system_prompt: 生成开发计划
    tools_policy:
      mode: read_only
`, database, worktree)
}

// runCommand 在指定目录执行命令，返回标准输出与标准错误。
func runCommand(t *testing.T, dir string, name string, args ...string) (string, string) {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("执行 %s %s 失败: %v\n标准输出: %s\n标准错误: %s",
			name, strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

// binaryName 返回当前平台的 Go 可执行文件名。
func binaryName() string {
	if runtime.GOOS == "windows" {
		return "agentflow.exe"
	}
	return "agentflow"
}

// startServer 从独立目录启动打包后的可执行文件，返回服务地址与停止函数。
//
// 服务监听随机端口，实际地址从启动输出中读取，因此不依赖固定端口。
func startServer(t *testing.T, runDir, configPath string) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	binary := filepath.Join(runDir, binaryName())
	command := exec.CommandContext(ctx, binary, "server", "--config", configPath, "--address", "127.0.0.1:0")
	// 工作目录设为独立目录：程序不得依赖仓库目录或前端 dist 目录才能提供页面。
	command.Dir = runDir
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatalf("获取子进程标准输出失败: %v", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		cancel()
		t.Fatalf("启动打包后的可执行文件失败: %v", err)
	}
	address := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			index := strings.Index(line, "http://")
			if index < 0 {
				continue
			}
			select {
			case address <- strings.TrimSpace(line[index:]):
			default:
			}
		}
	}()
	var stopOnce sync.Once
	stop := func() {
		// 停止必须是幂等的：用例失败时的 Cleanup 与正常结束都会调用它。
		stopOnce.Do(func() {
			cancel()
			_ = command.Wait()
		})
	}
	select {
	case url := <-address:
		return url, stop
	case <-time.After(30 * time.Second):
		stop()
		t.Fatalf("等待服务启动超时\n标准错误: %s", stderr.String())
		return "", func() {}
	}
}

// stopServer 在限定时间内停止服务进程，确认进程确实退出。
func stopServer(t *testing.T, stop func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("停止服务超时，进程可能仍在运行")
	}
}

// fetch 请求指定路径并返回状态码、响应头与响应体。
func fetch(t *testing.T, url string) (int, http.Header, string) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", url, err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取 %s 响应体失败: %v", url, err)
	}
	return response.StatusCode, response.Header, string(body)
}

// TestSingleBinaryPackaging 校验单文件交付：前端构建产物进入可执行文件，
// 复制到独立目录后不依赖前端源码目录、工作目录或开发服务器即可提供页面与 API。
func TestSingleBinaryPackaging(t *testing.T) {
	root := repoRoot(t)
	uiDir := filepath.Join(root, "lingting-agent-flow-ui")

	// 缺少前端构建工具链时跳过：该入口属于打包测试，不让全部 Go 测试依赖 Node 环境。
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("WARN [SKIP] 本机没有 pnpm，跳过单文件打包测试")
	}
	if _, err := os.Stat(filepath.Join(uiDir, "node_modules")); err != nil {
		t.Skip("WARN [SKIP] 前端依赖未安装，跳过单文件打包测试")
	}

	workspace := testkit.New(t)
	buildDir := workspace.Dir("build")
	runDir := workspace.Dir("run")
	configPath := filepath.Join(workspace.Root(), "agentflow.yaml")
	if err := os.WriteFile(configPath, []byte(minimalConfig(workspace.Root())), 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}

	// 1. 执行前端生产构建，产物写入 Go 的嵌入包目录。
	runCommand(t, uiDir, "pnpm", "build")
	distIndex := filepath.Join(root, "internal", "webui", "dist", "index.html")
	if _, err := os.Stat(distIndex); err != nil {
		t.Fatalf("前端构建产物缺少入口文件: %s: %v", distIndex, err)
	}

	// 2. 编译 Go 主程序（含前端静态资源）。
	runCommand(t, root, "go", "build",
		"-ldflags", "-X main.version=packaging-test -X main.commit=test -X main.buildDate=test",
		"-o", filepath.Join(buildDir, binaryName()),
		"./cmd/agentflow",
	)

	// 3. 复制到独立目录，并从该目录启动。
	source, err := os.Open(filepath.Join(buildDir, binaryName()))
	if err != nil {
		t.Fatalf("打开编译产物失败: %v", err)
	}
	defer func() {
		_ = source.Close()
	}()
	target, err := os.Create(filepath.Join(runDir, binaryName()))
	if err != nil {
		t.Fatalf("创建独立目录中的可执行文件失败: %v", err)
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		t.Fatalf("复制可执行文件失败: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("关闭可执行文件失败: %v", err)
	}

	baseURL, stop := startServer(t, runDir, configPath)
	t.Cleanup(stop)

	// 4. 页面入口可访问。
	status, header, body := fetch(t, baseURL+"/")
	if status != http.StatusOK {
		t.Fatalf("页面入口应返回 200，实际 %d", status)
	}
	if !strings.HasPrefix(header.Get("Content-Type"), "text/html") {
		t.Fatalf("页面入口应为 HTML，实际 %q", header.Get("Content-Type"))
	}
	if !strings.Contains(body, `id="root"`) {
		t.Fatalf("页面入口内容不完整: %s", body)
	}

	// 5. 入口引用的脚本与样式必须真实存在，避免路径错误导致资源静默丢失。
	assets := assetPattern.FindAllStringSubmatch(body, -1)
	if len(assets) == 0 {
		t.Fatalf("入口页面没有引用任何脚本或样式: %s", body)
	}
	var scriptCount, styleCount int
	for _, match := range assets {
		path := match[1]
		assetStatus, assetHeader, assetBody := fetch(t, baseURL+path)
		if assetStatus != http.StatusOK {
			t.Fatalf("静态资源 %s 应返回 200，实际 %d", path, assetStatus)
		}
		if assetBody == "" {
			t.Fatalf("静态资源 %s 响应体为空", path)
		}
		switch {
		case strings.HasSuffix(path, ".js"):
			scriptCount++
			if !strings.HasPrefix(assetHeader.Get("Content-Type"), "text/javascript") {
				t.Fatalf("脚本 %s 内容类型错误: %q", path, assetHeader.Get("Content-Type"))
			}
		case strings.HasSuffix(path, ".css"):
			styleCount++
			if !strings.HasPrefix(assetHeader.Get("Content-Type"), "text/css") {
				t.Fatalf("样式 %s 内容类型错误: %q", path, assetHeader.Get("Content-Type"))
			}
		}
	}
	if scriptCount == 0 {
		t.Fatalf("入口页面没有引用脚本资源: %s", body)
	}
	if styleCount == 0 {
		t.Fatalf("入口页面没有引用样式资源: %s", body)
	}

	// 6. 同源 API 可用。
	status, _, body = fetch(t, baseURL+httpapi.PathHealth)
	if status != http.StatusOK {
		t.Fatalf("健康检查应返回 200，实际 %d，响应: %s", status, body)
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(body), &health); err != nil {
		t.Fatalf("解析健康检查响应失败: %v", err)
	}
	if health.Status != "ok" {
		t.Fatalf("健康检查状态应为 ok，实际 %q", health.Status)
	}

	// 7. 未知 API 路径不得返回前端页面。
	status, header, body = fetch(t, baseURL+httpapi.APIPrefix+"/unknown")
	if status != http.StatusNotFound {
		t.Fatalf("未知 API 路径应返回 404，实际 %d", status)
	}
	if !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
		t.Fatalf("未知 API 路径应返回 JSON，实际 %q", header.Get("Content-Type"))
	}
	if strings.Contains(body, `id="root"`) {
		t.Fatal("未知 API 路径不应返回前端入口页面")
	}

	// 8. API 根路径同样属于保留路径空间，不返回页面。
	status, _, body = fetch(t, baseURL+httpapi.APIRoot)
	if status != http.StatusNotFound {
		t.Fatalf("API 根路径应返回 404，实际 %d", status)
	}
	if strings.Contains(body, `id="root"`) {
		t.Fatal("API 根路径不应返回前端入口页面")
	}

	// 9. 未知前端页面路径仍能加载前端入口，由前端路由展示 404。
	status, _, body = fetch(t, baseURL+"/not-a-real-page")
	if status != http.StatusOK {
		t.Fatalf("未知页面应回退到入口页面，实际 %d", status)
	}
	if !strings.Contains(body, `id="root"`) {
		t.Fatal("未知页面应返回前端入口页面")
	}

	// 10. 相似前缀不是 API，按页面路径处理。
	status, _, body = fetch(t, baseURL+"/apiary")
	if status != http.StatusOK {
		t.Fatalf("/apiary 应按页面路径处理，实际 %d", status)
	}
	if !strings.Contains(body, `id="root"`) {
		t.Fatal("/apiary 应返回前端入口页面")
	}

	// 11. 缺失的静态资源必须返回 404，不能伪装成 HTML。
	status, _, body = fetch(t, baseURL+"/assets/missing-file.js")
	if status != http.StatusNotFound {
		t.Fatalf("缺失静态资源应返回 404，实际 %d", status)
	}
	if strings.Contains(body, `id="root"`) {
		t.Fatal("缺失静态资源不应返回前端入口页面")
	}

	// 12. 停止服务：进程必须在限定时间内退出，且端口不再监听。
	stopServer(t, stop)
	address := strings.TrimPrefix(baseURL, "http://")
	if connection, err := net.DialTimeout("tcp", address, 500*time.Millisecond); err == nil {
		_ = connection.Close()
		t.Fatal("服务停止后监听端口仍然可用")
	}

	t.Logf("打包结果: 可执行文件=%s，运行目录=%s", filepath.Join(runDir, binaryName()), runDir)
}
