package domain

import "time"

// normalizeTime 统一时间戳的时区与精度，零值表示使用当前时间。
//
// 持久化层以 UTC Unix 毫秒时间戳保存时间，这里统一截断到毫秒，
// 保证实体在写入并读回后与写入前的值完全相等。
func normalizeTime(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now().UTC().Truncate(time.Millisecond)
	}
	return at.UTC().Truncate(time.Millisecond)
}
