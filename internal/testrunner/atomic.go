package testrunner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// writeJSONFile 以原子方式写出 JSON 文件：先写临时文件再改名，避免中途失败留下半个文件。
func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 %s 失败: %w", filepath.Base(path), err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("创建 %s 的目录失败: %w", filepath.Base(path), err)
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", filepath.Base(path), err)
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("落盘 %s 失败: %w", filepath.Base(path), err)
	}
	return nil
}
