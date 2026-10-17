package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// ID 是 AgentFlow 内部实体的唯一标识。
type ID string

// idByteLength 是随机标识的字节长度，十六进制编码后为 32 个字符。
const idByteLength = 16

// NewID 生成一个新的随机标识。
func NewID() ID {
	buffer := make([]byte, idByteLength)
	if _, err := rand.Read(buffer); err != nil {
		panic(fmt.Errorf("domain: 生成标识失败: %w", err))
	}
	return ID(hex.EncodeToString(buffer))
}

// ParseID 解析持久化层返回的标识字符串。
func ParseID(raw string) (ID, error) {
	id := ID(strings.TrimSpace(raw))
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

// String 返回标识的字符串形式。
func (id ID) String() string {
	return string(id)
}

// IsEmpty 判断标识是否为空。
func (id ID) IsEmpty() bool {
	return strings.TrimSpace(string(id)) == ""
}

// Validate 校验标识非空。
func (id ID) Validate() error {
	if id.IsEmpty() {
		return ErrEmptyID
	}
	return nil
}
