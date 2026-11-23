// 本文件登记 tests/unit/testkit 的 1 条用例，对应源文件 fileabsence_test.go。
//
// 源用例用 t.Run 表达四组断言，迁移后保留为 Case 内的顺序子测试（t.Run）：整体结论仍属于
// 这一条 Case，失败定位依旧指向具体子测试。
package testkit

var (
	_ = register("classify-file-absence", runClassifyFileAbsence)
)
