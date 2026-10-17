package domain

import (
	"errors"
	"fmt"
)

// ErrEmptyID 表示实体标识为空。
var ErrEmptyID = errors.New("domain: 标识不能为空")

// ErrInvalidValue 表示实体字段取值非法。
var ErrInvalidValue = errors.New("domain: 字段取值非法")

// ErrInvalidTransition 表示状态迁移不被允许。
var ErrInvalidTransition = errors.New("domain: 状态迁移不被允许")

// ErrInvalidState 表示当前状态不允许执行该操作。
var ErrInvalidState = errors.New("domain: 当前状态不允许该操作")

// ValidationError 描述某个实体字段的校验失败。
type ValidationError struct {
	Entity string
	Field  string
	Reason string
}

// Error 实现 error 接口。
func (e *ValidationError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", e.Entity, e.Reason)
	}
	return fmt.Sprintf("%s.%s: %s", e.Entity, e.Field, e.Reason)
}

// Unwrap 使调用方可以通过 errors.Is 判断字段校验失败。
func (e *ValidationError) Unwrap() error {
	return ErrInvalidValue
}

// invalidField 构造字段校验错误。
func invalidField(entity, field, reason string) error {
	return &ValidationError{Entity: entity, Field: field, Reason: reason}
}
