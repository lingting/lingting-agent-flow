package pi

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
)

// sessionHeaderType 是 Session 文件首行的记录类型。
const sessionHeaderType = "session"

// sessionHeader 是 Session 文件的头部元数据。
//
// 取值以官方 session-format.md 为准：头部记录没有 id/parentId，只描述版本与工作目录。
type sessionHeader struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
	ID      string `json:"id"`
	Cwd     string `json:"cwd"`
}

// validateSessionFile 校验 Session 文件是否可读且具备基本结构。
//
// 只做"能否使用"的最低限度校验：文件非空、首行是 Session 头部、每行都是合法 JSON。
// 不校验业务语义，也不解析完整会话树。
func validateSessionFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开 Session 文件失败: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	lines := 0
	headerFound := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lines++
		if !json.Valid([]byte(line)) {
			return fmt.Errorf("Session 文件第 %d 行不是合法 JSON", lines)
		}
		if lines == 1 {
			var header sessionHeader
			if err := json.Unmarshal([]byte(line), &header); err != nil {
				return fmt.Errorf("Session 头部无法解析: %w", err)
			}
			if header.Type != sessionHeaderType {
				return fmt.Errorf("Session 首行不是会话头部，实际类型 %q", header.Type)
			}
			headerFound = true
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("读取 Session 文件失败: %w", err)
	}
	if !headerFound {
		return errors.New("Session 文件为空或缺少会话头部")
	}
	return nil
}

// emitEvent 编码并发出内部事件，回调为空或编码失败时忽略。
func emitEvent(
	sessionID string,
	emit func(agent.Event),
	eventType agent.EventType,
	payload any,
) {
	if emit == nil {
		return
	}
	event, err := agent.NewEvent(sessionID, eventType, payload, time.Now())
	if err != nil {
		return
	}
	emit(event)
}
