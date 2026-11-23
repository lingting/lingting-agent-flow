package testkit

import (
	"strings"

	"github.com/lingting/agentflow/internal/testrunner"
	// 共享辅助在 tests/testkit；本包是 unit 用例包，两者同名不同职责（R6 §2），
	// 因此正文一律用 sharedkit 前缀指共享辅助，不空导入、不合并。
	sharedkit "github.com/lingting/agentflow/tests/testkit"
)

// 原 TestClassifyFileAbsence 守住容器内 `test -e` 探针的退出码语义。
//
// 文件不存在只能由"命令确认执行、确认结束且退出码为 1"证明：退出码 0 表示文件存在，
// 2 / 127 / -1 与执行结果不确定都不能被解释成不存在。
func runClassifyFileAbsence(t *testrunner.Context) {
	confirmed := sharedkit.FileAbsenceProbe{
		ExitCode:   1,
		Started:    true,
		Certain:    true,
		Determined: true,
	}

	t.Run("退出码 1 确认不存在", func(t *testrunner.Context) {
		verdict, err := sharedkit.ClassifyFileAbsence(confirmed)
		if err != nil {
			t.Fatalf("退出码 1 应被接受为不存在: %v", err)
		}
		if verdict != sharedkit.FileConfirmedAbsent {
			t.Fatalf("退出码 1 的结论应为不存在，实际 %q", verdict)
		}
	})

	t.Run("退出码 0 表示文件存在", func(t *testrunner.Context) {
		probe := confirmed
		probe.ExitCode = 0
		verdict, err := sharedkit.ClassifyFileAbsence(probe)
		if err != nil {
			t.Fatalf("退出码 0 应给出确定结论: %v", err)
		}
		if verdict != sharedkit.FileConfirmedPresent {
			t.Fatalf("退出码 0 的结论应为存在，实际 %q", verdict)
		}
	})

	t.Run("其他退出码都不是确定结论", func(t *testrunner.Context) {
		for _, exitCode := range []int{2, 127, -1, 3, 255} {
			probe := confirmed
			probe.ExitCode = exitCode
			verdict, err := sharedkit.ClassifyFileAbsence(probe)
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

	t.Run("执行结果不确定时不得报告不存在", func(t *testrunner.Context) {
		cases := map[string]sharedkit.FileAbsenceProbe{
			"未确认已在容器内执行": {ExitCode: 1, Certain: true, Determined: true},
			"未确认命令结束": {
				ExitCode:   1,
				Started:    true,
				Determined: true,
			},
			"不是按退出码结束": {ExitCode: 1, Started: true, Certain: true},
		}
		for reason, probe := range cases {
			verdict, err := sharedkit.ClassifyFileAbsence(probe)
			if err == nil {
				t.Fatalf("%s 时不应给出结论，实际 %q", reason, verdict)
			}
			if verdict != "" {
				t.Fatalf("%s 失败时不应返回结论，实际 %q", reason, verdict)
			}
		}
	})
}
