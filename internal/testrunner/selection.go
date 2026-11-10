package testrunner

import (
	"fmt"
	"strings"
)

// Selector 是一条用例选择条件。
//
// R1 的选择语法只有三种写法：分组名、精确用例 ID、以 "..." 结尾的前缀。R2 冻结完整
// 选择语义时替换这里，不再同时保留多种模糊写法。
type Selector struct {
	raw        string
	prefix     string
	prefixMode bool
}

// ParseSelector 解析一条选择条件。
func ParseSelector(arg string) (Selector, error) {
	raw := strings.TrimSpace(arg)
	if raw == "" {
		return Selector{}, fmt.Errorf("选择条件不能为空")
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return Selector{}, fmt.Errorf("选择条件 %q 不能包含空白字符", raw)
	}
	if strings.HasSuffix(raw, "...") {
		prefix := strings.TrimSuffix(raw, "...")
		if prefix == "" {
			return Selector{}, fmt.Errorf("选择条件 %q 缺少前缀", raw)
		}
		return Selector{raw: raw, prefix: prefix, prefixMode: true}, nil
	}
	return Selector{raw: raw}, nil
}

// ParseSelectors 解析全部选择条件；空参数表示不限制。
func ParseSelectors(args []string) ([]Selector, error) {
	selectors := make([]Selector, 0, len(args))
	for _, arg := range args {
		selector, err := ParseSelector(arg)
		if err != nil {
			return nil, err
		}
		selectors = append(selectors, selector)
	}
	return selectors, nil
}

// Match 报告用例是否命中该选择条件。
func (s Selector) Match(c Case) bool {
	if s.prefixMode {
		return strings.HasPrefix(c.ID, s.prefix)
	}
	return c.ID == s.raw || c.Group == s.raw
}

// String 返回原始写法，用于报告记录本次选择。
func (s Selector) String() string {
	return s.raw
}

// Select 按选择条件过滤用例；没有条件时返回全部用例。
func Select(cases []Case, selectors []Selector) []Case {
	if len(selectors) == 0 {
		return append([]Case(nil), cases...)
	}
	selected := make([]Case, 0, len(cases))
	for _, c := range cases {
		for _, selector := range selectors {
			if selector.Match(c) {
				selected = append(selected, c)
				break
			}
		}
	}
	return selected
}
