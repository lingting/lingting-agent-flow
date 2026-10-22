package domain

import "fmt"

// statusMachine 描述某一状态类型允许的迁移集合与终态集合。
type statusMachine struct {
	entity      string
	transitions map[string][]string
	terminal    []string
}

// validate 校验一次状态迁移是否被允许。
func (m statusMachine) validate(current, next string) error {
	for _, candidate := range m.transitions[current] {
		if candidate == next {
			return nil
		}
	}
	return fmt.Errorf("%w: %s 不允许从 %q 迁移到 %q", ErrInvalidTransition, m.entity, current, next)
}

// allows 判断状态机是否允许从 current 迁移到 next。
//
// 它只表达"状态机是否允许"，不做任何额外业务判断：调用方据此定义准入集合，
// 从而避免在多处复制同一份状态白名单。
func (m statusMachine) allows(current, next string) bool {
	for _, candidate := range m.transitions[current] {
		if candidate == next {
			return true
		}
	}
	return false
}

// isKnown 判断状态是否属于该状态机。
func (m statusMachine) isKnown(status string) bool {
	if _, ok := m.transitions[status]; ok {
		return true
	}
	for _, candidate := range m.terminal {
		if candidate == status {
			return true
		}
	}
	return false
}

// isTerminal 判断状态是否为终态。
func (m statusMachine) isTerminal(status string) bool {
	for _, candidate := range m.terminal {
		if candidate == status {
			return true
		}
	}
	return false
}
