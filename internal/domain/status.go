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
