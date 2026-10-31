package release

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"
)

// checksumPattern 是 SHA-256 十六进制摘要的格式。
var checksumPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SHA256Hex 返回内容的 SHA-256 小写十六进制摘要。
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SHA256File 返回文件的 SHA-256 小写十六进制摘要与文件字节数。
//
// 摘要与大小必须来自同一次读取：先取大小再单独算摘要会让"文件在两次操作之间
// 被替换"的情况无法发现。
func SHA256File(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() {
		_ = file.Close()
	}()
	digest := sha256.New()
	size, err := io.Copy(digest, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(digest.Sum(nil)), size, nil
}

// ValidChecksum 判断取值是否为合法的 SHA-256 小写十六进制摘要。
func ValidChecksum(value string) bool {
	return checksumPattern.MatchString(value)
}

// VerifyChecksum 校验文件内容与给定摘要一致，不一致时返回错误。
func VerifyChecksum(path string, expected string) error {
	actual, _, err := SHA256File(path)
	if err != nil {
		return fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	if actual != expected {
		return fmt.Errorf("%s 的 SHA-256 不一致：期望 %s，实际 %s", path, expected, actual)
	}
	return nil
}
