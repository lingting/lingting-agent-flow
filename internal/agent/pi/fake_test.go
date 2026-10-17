package pi_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
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

// fakeCommand 是假 Pi 解析到的 RPC 命令。
type fakeCommand struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

// fakeReport 记录子进程实际收到的启动信息。
type fakeReport struct {
	Args []string `json:"args"`
	Cwd  string   `json:"cwd"`
}

// runFakePi 模拟 Pi 的 RPC 模式，按场景输出可控的 JSONL 数据。
func runFakePi() int {
	scenario := os.Getenv(fakePiEnv)
	writeReport()
	if handled, code := handleInspectionArgs(); handled {
		return code
	}
	if scenario == "startup_failure" {
		fmt.Fprintln(os.Stderr, "fake pi: 启动失败")
		return 7
	}
	if scenario == "invalid_json" {
		fmt.Fprintln(os.Stdout, "not-json")
		return 1
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var cmd fakeCommand
		if err := json.Unmarshal([]byte(line), &cmd); err != nil {
			return 1
		}
		switch cmd.Type {
		case "get_state":
			writeRecord(map[string]any{
				"id":      cmd.ID,
				"type":    "response",
				"command": "get_state",
				"success": true,
				"data":    map[string]any{"sessionId": sessionID()},
			}, "\n")
		case "prompt":
			handlePrompt(scenario, cmd)
		case "abort":
			writeRecord(map[string]any{
				"id":      cmd.ID,
				"type":    "response",
				"command": "abort",
				"success": true,
			}, "\n")
			if scenario == "hang" {
				time.Sleep(30 * time.Second)
			}
			return 0
		}
	}
	return 0
}

// handlePrompt 输出 prompt 响应与场景事件。
func handlePrompt(scenario string, cmd fakeCommand) {
	disposition := "started"
	if scenario == "handled" {
		disposition = "handled"
	}
	response := map[string]any{
		"id":      cmd.ID,
		"type":    "response",
		"command": "prompt",
		"success": true,
		"data":    map[string]any{"disposition": disposition},
	}
	if scenario == "instant" {
		// 响应与完成事件在同一次写入中到达，验证监听已提前就绪。
		writeRecord(response, "\n")
		writeSequence(scenario)
		return
	}
	writeRecord(response, "\n")
	switch scenario {
	case "handled":
		return
	case "exit_nonzero":
		os.Exit(3)
	case "hang":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "fragmented":
		// 最后一条记录不带换行符，进程退出时由读取方按流结束处理。
		writeFragmented()
		os.Exit(0)
	default:
		writeSequence(scenario)
	}
}

// writeSequence 输出一个完整的运行事件序列。
func writeSequence(scenario string) {
	switch scenario {
	case "provider_error":
		writeRecord(map[string]any{
			"type": "message_update",
			"assistantMessageEvent": map[string]any{
				"type":   "error",
				"reason": "provider exploded",
			},
		}, "\n")
		writeRecord(map[string]any{
			"type": "message_end",
			"message": map[string]any{
				"role":         "assistant",
				"content":      []any{},
				"stopReason":   "error",
				"errorMessage": "provider exploded",
			},
		}, "\n")
		writeRecord(map[string]any{"type": "agent_settled", "aborted": false}, "\n")
		return
	case "long":
		writeRecord(map[string]any{
			"type": "message_update",
			"assistantMessageEvent": map[string]any{
				"type":         "text_delta",
				"contentIndex": 0,
				"delta":        strings.Repeat("长", 100000),
			},
		}, "\n")
		writeRecord(map[string]any{
			"type": "message_end",
			"message": map[string]any{
				"role":       "assistant",
				"content":    []any{map[string]any{"type": "text", "text": "READY"}},
				"stopReason": "stop",
			},
		}, "\n")
		writeRecord(map[string]any{"type": "agent_settled", "aborted": false}, "\n")
		return
	}

	writeRecord(map[string]any{"type": "agent_start"}, "\n")
	writeRecord(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type":         "text_delta",
			"contentIndex": 0,
			"delta":        "READY",
		},
	}, "\n")
	if scenario == "unknown" {
		writeRecord(map[string]any{"type": "compaction_start", "reason": "manual"}, "\n")
		writeRecord(map[string]any{"type": "extension_error", "error": "ignored"}, "\n")
	}
	writeRecord(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":       "assistant",
			"content":    []any{map[string]any{"type": "text", "text": "READY"}},
			"stopReason": "stop",
		},
	}, "\n")
	if scenario == "agent_end" {
		// agent_end 只结束一轮底层运行，后续仍可能继续工作。
		writeRecord(map[string]any{
			"type":      "agent_end",
			"messages":  []any{},
			"willRetry": false,
		}, "\n")
		writeRecord(map[string]any{
			"type": "message_update",
			"assistantMessageEvent": map[string]any{
				"type":         "text_delta",
				"contentIndex": 0,
				"delta":        " 补充",
			},
		}, "\n")
	}
	if scenario == "aborted" {
		writeRecord(map[string]any{"type": "agent_settled", "aborted": true}, "\n")
		return
	}
	writeRecord(map[string]any{"type": "agent_settled", "aborted": false}, "\n")
}

// writeFragmented 以 CRLF 分片输出，最后一条记录不带换行符。
func writeFragmented() {
	records := []string{
		`{"type":"agent_start"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"READY"}}`,
		`{"type":"message_end","message":{"role":"assistant","content":"READY","stopReason":"stop"}}`,
		`{"type":"agent_settled","aborted":false}`,
	}
	for index, rec := range records {
		terminator := "\r\n"
		if index == len(records)-1 {
			terminator = ""
		}
		payload := rec + terminator
		for offset := 0; offset < len(payload); offset += 7 {
			end := offset + 7
			if end > len(payload) {
				end = len(payload)
			}
			_, _ = os.Stdout.WriteString(payload[offset:end])
		}
	}
}

// handleInspectionArgs 响应版本与帮助查询，用于检查命令的测试。
func handleInspectionArgs() (bool, int) {
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--version":
			fmt.Fprintln(os.Stdout, "9.9.9-fake")
			return true, 0
		case "--help":
			fmt.Fprintln(os.Stdout, "Options:\n  --mode <mode>  Output mode: text (default), json, or rpc")
			return true, 0
		}
	}
	return false, 0
}

// writeRecord 编码并写出一条 JSONL 记录。
func writeRecord(payload map[string]any, terminator string) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = os.Stdout.WriteString(string(encoded) + terminator)
}

// writeReport 把启动参数与工作目录写入报告文件。
func writeReport() {
	path := strings.TrimSpace(os.Getenv(fakePiReportEnv))
	if path == "" {
		return
	}
	cwd, _ := os.Getwd()
	report := fakeReport{Args: os.Args[1:], Cwd: cwd}
	encoded, err := json.Marshal(report)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, encoded, 0o600)
}

// sessionID 生成随进程变化的假 Pi 会话标识。
func sessionID() string {
	return fmt.Sprintf("pi-session-%d", os.Getpid())
}
