package domain

// DefaultMaxAttempts 是未显式配置时的步骤尝试次数上限。
const DefaultMaxAttempts = 3

// RetryPolicy 约束某个步骤允许的最大尝试次数。
//
// 上限判断属于业务规则，重试的编排与执行由 workflow 层负责。
type RetryPolicy struct {
	MaxAttempts int `json:"max_attempts"`
}

// NewRetryPolicy 创建重试策略，未配置时使用默认上限。
func NewRetryPolicy(maxAttempts int) (RetryPolicy, error) {
	policy := RetryPolicy{MaxAttempts: maxAttempts}
	if policy.MaxAttempts == 0 {
		policy.MaxAttempts = DefaultMaxAttempts
	}
	if err := policy.Validate(); err != nil {
		return RetryPolicy{}, err
	}
	return policy, nil
}

// Validate 校验重试策略。
func (p RetryPolicy) Validate() error {
	if p.MaxAttempts < 1 {
		return invalidField("RetryPolicy", "max_attempts", "尝试次数上限必须大于 0")
	}
	return nil
}

// CanRetry 判断在已完成 attemptNumber 次尝试后是否仍可重试。
func (p RetryPolicy) CanRetry(attemptNumber int) bool {
	limit := p.MaxAttempts
	if limit < 1 {
		limit = DefaultMaxAttempts
	}
	return attemptNumber < limit
}

// NextAttemptNumber 返回下一次尝试应使用的序号。
func (p RetryPolicy) NextAttemptNumber(attemptNumber int) int {
	return attemptNumber + 1
}
