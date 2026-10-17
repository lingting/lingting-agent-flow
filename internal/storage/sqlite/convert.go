package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// nowMillis 返回当前的 UTC Unix 毫秒时间戳。
func nowMillis() int64 {
	return time.Now().UTC().UnixMilli()
}

// toMillis 把时间转换为 UTC Unix 毫秒时间戳。
func toMillis(at time.Time) int64 {
	return at.UTC().UnixMilli()
}

// fromMillis 把毫秒时间戳还原为 UTC 时间。
func fromMillis(millis int64) time.Time {
	return time.UnixMilli(millis).UTC()
}

// toMillisPtr 把可空时间转换为可写入 NULL 的参数。
func toMillisPtr(at *time.Time) any {
	if at == nil {
		return nil
	}
	return toMillis(*at)
}

// fromMillisPtr 把可空列还原为可空时间。
func fromMillisPtr(millis sql.NullInt64) *time.Time {
	if !millis.Valid {
		return nil
	}
	value := fromMillis(millis.Int64)
	return &value
}

// toIDParam 把标识转换为可写入的参数。
func toIDParam(id domain.ID) string {
	return id.String()
}

// toIDPtrParam 把可空标识转换为可写入 NULL 的参数。
func toIDPtrParam(id *domain.ID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

// fromIDPtr 把可空列还原为可空标识。
func fromIDPtr(raw sql.NullString) *domain.ID {
	if !raw.Valid || raw.String == "" {
		return nil
	}
	value := domain.ID(raw.String)
	return &value
}

// fromIntPtr 把可空整数列还原为可空整数。
func fromIntPtr(raw sql.NullInt64) *int {
	if !raw.Valid {
		return nil
	}
	value := int(raw.Int64)
	return &value
}

// toBoolInt 把布尔值转换为数据库整数。
func toBoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// fromBoolInt 把数据库整数还原为布尔值。
func fromBoolInt(value int) bool {
	return value != 0
}

// marshalJSON 把值编码为 UTF-8 JSON 文本。
//
// 空切片会编码为 [] 而不是 null，便于读取时区分"未设置"与"空列表"。
func marshalJSON(entity string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("sqlite: 编码 %s JSON 失败: %w", entity, err)
	}
	return string(data), nil
}

// unmarshalJSON 把 JSON 文本解码到目标值。
func unmarshalJSON(entity, raw string, target any) error {
	if raw == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return fmt.Errorf("sqlite: 解码 %s JSON 失败: %w", entity, err)
	}
	return nil
}

// marshalSlice 编码切片，nil 与空切片都编码为 []。
func marshalSlice[T any](entity string, values []T) (string, error) {
	if values == nil {
		values = []T{}
	}
	return marshalJSON(entity, values)
}
