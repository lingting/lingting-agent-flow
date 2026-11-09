package testkit_test

import (
	"testing"

	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestPiConfigResolvedModel 守住宿主 Pi 模型标识的组成规则。
//
// Pi 的模型清单按 provider 与模型两列列出，`--model` 使用 provider/model 形式：
// 配置里只写模型名时可能因为 provider 不同名而无法被识别，因此允许显式给出 provider。
// 未给出 provider 时必须原样返回模型，保持旧配置与旧判定的行为不变。
func TestPiConfigResolvedModel(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
		want     string
	}{
		{
			name:  "未配置 provider 时保持原样",
			model: "group/free",
			want:  "group/free",
		},
		{
			name:     "配置 provider 时补全前缀",
			provider: "magpie",
			model:    "group/free",
			want:     "magpie/group/free",
		},
		{
			name:     "模型已带同一 provider 前缀时不重复拼接",
			provider: "magpie",
			model:    "magpie/group/free",
			want:     "magpie/group/free",
		},
		{
			name:     "provider 两侧空白被忽略",
			provider: "  magpie  ",
			model:    "  group/free  ",
			want:     "magpie/group/free",
		},
		{
			name:  "模型留空时使用默认免费模型",
			want:  testkit.DefaultPiModel,
			model: "",
		},
		{
			name:     "模型留空时仍然补全 provider",
			provider: "magpie",
			model:    "",
			want:     "magpie/" + testkit.DefaultPiModel,
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			config := testkit.PiConfig{Provider: testCase.provider, Model: testCase.model}
			if got := config.ResolvedModel(); got != testCase.want {
				t.Fatalf("模型标识应为 %q，实际 %q", testCase.want, got)
			}
		})
	}
}
