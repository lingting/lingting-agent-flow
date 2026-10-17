package pi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 测试替身使用的环境变量。
const (
	// fakePiEnv 选择假 Pi 的行为场景。
	fakePiEnv = "AGENTFLOW_PI_FAKE"
	// fakePiReportEnv 指定写入启动信息的报告文件路径。
	fakePiReportEnv = "AGENTFLOW_PI_FAKE_REPORT"
)

// fakeReport 记录子进程实际收到的启动信息。
type fakeReport struct {
	Args []string `json:"args"`
	Cwd  string   `json:"cwd"`
}

// runFakePi 以测试二进制充当假 Pi，按场景产生可控行为。
//
// 测试通过重新执行测试二进制本身得到可控的子进程，不依赖本机是否安装 Pi，
// 因此默认测试不访问任何外部模型服务。
func runFakePi() int {
	scenario := strings.TrimSpace(os.Getenv(fakePiEnv))
	writeReport()
	if handled, code := handleInspectionArgs(scenario); handled {
		return code
	}
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake pi: 读取提示词失败")
		return 9
	}
	switch scenario {
	case "startup_failure":
		fmt.Fprintln(os.Stderr, "fake pi: 启动失败")
		return 7
	case "hang":
		// 忽略 stdin 关闭，只有强制终止才能结束该进程。
		time.Sleep(time.Hour)
		return 0
	case "missing_session":
		fmt.Fprintln(os.Stdout, "READY")
		return 0
	case "invalid_session":
		writeSessionFile(scenario, sessionModeInvalid)
		fmt.Fprintln(os.Stdout, "READY")
		return 0
	case "partial_session":
		writeSessionFile(scenario, sessionModeHeaderOnly)
		fmt.Fprintln(os.Stdout, "READY")
		return 3
	case "exit_nonzero":
		fmt.Fprintln(os.Stderr, "fake pi: 模型不可用")
		return 3
	case "huge_output":
		writeHugeOutput()
	case "stderr_output":
		fmt.Fprintln(os.Stderr, "fake pi: 诊断信息")
		fmt.Fprintln(os.Stderr, "fake pi: 第二行诊断")
	case "no_session_on_success":
		writeSessionFile(scenario, sessionModeComplete)
		fmt.Fprintln(os.Stdout, "READY")
		return 0
	}
	if len(strings.TrimSpace(string(prompt))) == 0 {
		fmt.Fprintln(os.Stderr, "fake pi: 没有收到提示词")
		return 9
	}
	writeSessionFile(scenario, sessionModeComplete)
	fmt.Fprintln(os.Stdout, "READY")
	return 0
}

// handleInspectionArgs 响应版本、帮助、模型列表、凭据检查与导出。
func handleInspectionArgs(scenario string) (bool, int) {
	for index, arg := range os.Args {
		switch arg {
		case "--version":
			fmt.Fprintln(os.Stdout, "9.9.9-fake")
			return true, 0
		case "--help":
			if scenario == "old_version" {
				fmt.Fprintln(os.Stdout, "Options:\n  --mode <mode>  Output mode: text, json, or rpc")
				return true, 0
			}
			fmt.Fprintln(os.Stdout, "Options:\n"+
				"  --mode <mode>                  Output mode: text (default), json, or rpc\n"+
				"  --print, -p                    Non-interactive mode: process prompt and exit\n"+
				"  --session <path|id>            Use specific session file or partial UUID\n"+
				"  --export <file>                Export session file to HTML and exit")
			return true, 0
		case "--list-models":
			fmt.Fprintln(os.Stdout, "provider   model                context  max-out")
			fmt.Fprintln(os.Stdout, "fake       model-1              128K     16.4K")
			return true, 0
		case "--export":
			return true, handleExport(scenario, os.Args[index+1:])
		case "auth":
			return true, handleAuth(scenario)
		}
	}
	return false, 0
}

// handleAuth 输出凭据检查结果。
func handleAuth(scenario string) int {
	status := "ready"
	if scenario == "no_auth" {
		status = "not_ready"
	}
	payload, err := json.Marshal(map[string]string{"status": status, "provider": "fake"})
	if err != nil {
		return 1
	}
	fmt.Fprintln(os.Stdout, string(payload))
	return 0
}

// handleExport 模拟 HTML 导出。
func handleExport(scenario string, args []string) int {
	if scenario == "export_failure" {
		fmt.Fprintln(os.Stderr, "fake pi: 导出失败")
		return 5
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "fake pi: 缺少导出目标")
		return 6
	}
	if err := os.WriteFile(args[1], []byte("<html>session</html>"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "fake pi: 写入导出文件失败")
		return 6
	}
	fmt.Fprintf(os.Stdout, "Exported to: %s\n", args[1])
	return 0
}

// writeReport 把启动参数与工作目录写入报告文件。
//
// 导出子进程也会继承同一个报告路径，因此导出调用不写报告，避免覆盖主调用的参数。
func writeReport() {
	path := strings.TrimSpace(os.Getenv(fakePiReportEnv))
	if path == "" {
		return
	}
	for _, arg := range os.Args[1:] {
		if arg == "--export" {
			return
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	encoded, err := json.Marshal(fakeReport{Args: os.Args[1:], Cwd: cwd})
	if err != nil {
		return
	}
	_ = os.WriteFile(path, encoded, 0o600)
}

// writeHugeOutput 输出远超摘要上限的内容，验证输出缓存有界。
func writeHugeOutput() {
	block := strings.Repeat("长", 2048)
	for index := 0; index < 512; index++ {
		fmt.Fprintln(os.Stdout, block)
	}
}

// sessionMode 描述假 Session 文件的内容完整度。
type sessionMode int

// 假 Session 文件的写入方式。
const (
	// sessionModeComplete 写入头部与一轮完整对话。
	sessionModeComplete sessionMode = iota
	// sessionModeHeaderOnly 只写入头部，模拟异常退出后的残缺文件。
	sessionModeHeaderOnly
	// sessionModeInvalid 写入非法 JSON，模拟文件损坏。
	sessionModeInvalid
)

// writeSessionFile 按模式写出 Session 文件。
func writeSessionFile(scenario string, mode sessionMode) {
	path := sessionPathFromArgs()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	header := fmt.Sprintf(
		`{"type":"session","version":3,"id":"fake-session-%s",`+
			`"timestamp":"2025-01-02T03:04:05.000Z","cwd":"/tmp"}`,
		scenario,
	)
	lines := []string{header}
	switch mode {
	case sessionModeInvalid:
		lines = append(lines, "not-json")
	case sessionModeComplete:
		lines = append(
			lines,
			`{"type":"message","id":"a1b2c3d4","parentId":null,`+
				`"timestamp":"2025-01-02T03:04:06.000Z",`+
				`"message":{"role":"user","content":"只回答 READY","timestamp":1735787046000}}`,
			`{"type":"message","id":"b2c3d4e5","parentId":"a1b2c3d4",`+
				`"timestamp":"2025-01-02T03:04:07.000Z",`+
				`"message":{"role":"assistant","content":[{"type":"text","text":"READY"}],`+
				`"provider":"fake","model":"model-1","stopReason":"stop","timestamp":1735787047000}}`,
		)
	}
	content := strings.Join(lines, "\n") + "\n"
	_ = os.WriteFile(path, []byte(content), 0o600)
}

// sessionPathFromArgs 从启动参数中提取 --session 的取值。
func sessionPathFromArgs() string {
	for index, arg := range os.Args {
		if arg == "--session" && index+1 < len(os.Args) {
			return os.Args[index+1]
		}
	}
	return ""
}
