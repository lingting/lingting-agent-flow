package testkit_test

import (
	"strings"
	"testing"

	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestClassifyFileAbsence 守住容器内 `test -e` 探针的退出码语义。
//
// 文件不存在只能由"命令确认执行、确认结束且退出码为 1"证明：退出码 0 表示文件存在，
// 2 / 127 / -1 与执行结果不确定都不能被解释成不存在。
func TestClassifyFileAbsence(t *testing.T) {
	confirmed := testkit.FileAbsenceProbe{
		ExitCode:   1,
		Started:    true,
		Certain:    true,
		Determined: true,
	}

	t.Run("退出码 1 确认不存在", func(t *testing.T) {
		verdict, err := testkit.ClassifyFileAbsence(confirmed)
		if err != nil {
			t.Fatalf("退出码 1 应被接受为不存在: %v", err)
		}
		if verdict != testkit.FileConfirmedAbsent {
			t.Fatalf("退出码 1 的结论应为不存在，实际 %q", verdict)
		}
	})

	t.Run("退出码 0 表示文件存在", func(t *testing.T) {
		probe := confirmed
		probe.ExitCode = 0
		verdict, err := testkit.ClassifyFileAbsence(probe)
		if err != nil {
			t.Fatalf("退出码 0 应给出确定结论: %v", err)
		}
		if verdict != testkit.FileConfirmedPresent {
			t.Fatalf("退出码 0 的结论应为存在，实际 %q", verdict)
		}
	})

	t.Run("其他退出码都不是确定结论", func(t *testing.T) {
		for _, exitCode := range []int{2, 127, -1, 3, 255} {
			probe := confirmed
			probe.ExitCode = exitCode
			verdict, err := testkit.ClassifyFileAbsence(probe)
			if err == nil {
				t.Fatalf("退出码 %d 不应给出结论，实际 %q", exitCode, verdict)
			}
			if verdict != "" {
				t.Fatalf("退出码 %d 失败时不应返回结论，实际 %q", exitCode, verdict)
			}
			if !strings.Contains(err.Error(), "退出码") {
				t.Fatalf("错误信息应包含退出码，实际 %q", err.Error())
			}
		}
	})

	t.Run("执行结果不确定时不得报告不存在", func(t *testing.T) {
		cases := map[string]testkit.FileAbsenceProbe{
			"未确认已在容器内执行": {ExitCode: 1, Certain: true, Determined: true},
			"未确认命令结束": {
				ExitCode:   1,
				Started:    true,
				Determined: true,
			},
			"不是按退出码结束": {ExitCode: 1, Started: true, Certain: true},
		}
		for reason, probe := range cases {
			verdict, err := testkit.ClassifyFileAbsence(probe)
			if err == nil {
				t.Fatalf("%s 时不应给出结论，实际 %q", reason, verdict)
			}
			if verdict != "" {
				t.Fatalf("%s 失败时不应返回结论，实际 %q", reason, verdict)
			}
		}
	})
}
