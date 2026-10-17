package domain

import "time"

// normalizeTime 统一时间戳的时区，零值表示使用当前时间。
func normalizeTime(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now().UTC()
	}
	return at.UTC()
}
