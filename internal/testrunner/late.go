package testrunner

import "fmt"

// 迟到结果防护（V3 R3 §4.5）。
//
// worker 进程树被终止后仍可能有进程写出 result.json。编排进程只在用例结论未定型时
// 接受结果文件；结论一旦确定（timeout / error），之后写出的文件只能作为差异留痕，
// 不得改写结论（AGENTS 红线：迟到结果不得覆盖已确认终态）。
//
// 写报告之前做一次复核：结论已定型、而磁盘上的结果文件声称另一种状态时，把差异记进
// 用例警告。自检注入会主动写出这样一份文件，使这条路径可以稳定复现。

// detectLateResults 复核已定型结论与磁盘结果文件是否一致。
func detectLateResults(results []CaseResult, inject bool) {
	for index := range results {
		result := &results[index]
		if result.Artifacts.Result == "" {
			continue
		}
		if result.Status != StatusTimeout && result.Status != StatusError {
			continue
		}
		if inject {
			if err := writeLateResultFile(result.Artifacts.Result, result.ID); err != nil {
				result.Warnings = append(
					result.Warnings,
					fmt.Sprintf("自检注入迟到结果失败: %v", err),
				)
			}
		}
		late, present, err := readWorkerResult(result.Artifacts.Result)
		if err != nil {
			result.Warnings = append(
				result.Warnings,
				fmt.Sprintf("复核迟到结果失败: %v", err),
			)
			continue
		}
		if !present || late.Status == result.Status {
			continue
		}
		result.Warnings = append(
			result.Warnings,
			fmt.Sprintf(
				"结论已定型为 %s，忽略迟到的结果文件（声称 %s）",
				result.Status,
				late.Status,
			),
		)
	}
}

// writeLateResultFile 写出自检用的迟到结果文件：它声称用例通过，用来验证结论不被改写。
func writeLateResultFile(path, caseID string) error {
	forged := CaseResult{
		ID:      caseID,
		Status:  StatusPassed,
		Message: "自检注入的迟到结果：该结论不得被采用",
	}
	return writeJSONFile(path, forged)
}
