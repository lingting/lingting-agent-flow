package caseinventory

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// goSource 是一个已跟踪 Go 文件的解析结果；测试文件与测试辅助文件都按它表示。
type goSource struct {
	RelPath     string
	Dir         string
	PackageName string
	IsTest      bool
	HasTestMain bool
	Imports     []string
	Symbols     map[string]bool
	Funcs       []goFunc
	Signals     signals
	Markers     fileMarkers
	Source      string
}

// goFunc 是一个 Go 测试函数；Subtests 是其中的 t.Run 字符串字面量子测试名。
type goFunc struct {
	Name     string
	Subtests []string
}

// signals 是逐文件统计的测试调用点数量。
type signals struct {
	Parallel int
	Cleanup  int
	Helper   int
	TempDir  int
	Fatal    int
	Error    int
	Skip     int
	Run      int
}

// add 把另一个文件的统计累加进当前统计。
func (s *signals) add(other signals) {
	s.Parallel += other.Parallel
	s.Cleanup += other.Cleanup
	s.Helper += other.Helper
	s.TempDir += other.TempDir
	s.Fatal += other.Fatal
	s.Error += other.Error
	s.Skip += other.Skip
	s.Run += other.Run
}

// fileMarkers 是资源推断与并行风险筛查使用的包级源码线索。
type fileMarkers struct {
	SyncOnce       bool
	NetListen      bool
	HTTPTestServer bool
	ExecCommand    bool
}

// merge 合并另一个文件的线索。
func (m *fileMarkers) merge(other fileMarkers) {
	m.SyncOnce = m.SyncOnce || other.SyncOnce
	m.NetListen = m.NetListen || other.NetListen
	m.HTTPTestServer = m.HTTPTestServer || other.HTTPTestServer
	m.ExecCommand = m.ExecCommand || other.ExecCommand
}

// scanGoSource 解析一个 Go 文件。
func scanGoSource(root, relPath string) (goSource, error) {
	absPath := filepath.Join(root, filepath.FromSlash(relPath))
	content, err := os.ReadFile(absPath)
	if err != nil {
		return goSource{}, fmt.Errorf("读取 %s 失败: %w", relPath, err)
	}
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, absPath, content, parser.ParseComments)
	if err != nil {
		return goSource{}, fmt.Errorf("解析 %s 失败: %w", relPath, err)
	}
	source := goSource{
		RelPath:     relPath,
		Dir:         path.Dir(relPath),
		PackageName: parsed.Name.Name,
		IsTest:      strings.HasSuffix(relPath, "_test.go"),
		Symbols:     map[string]bool{},
		Source:      string(content),
	}
	for _, spec := range parsed.Imports {
		if value, unquoteErr := strconv.Unquote(spec.Path.Value); unquoteErr == nil {
			source.Imports = append(source.Imports, value)
		}
	}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if fn.Name.Name == "TestMain" && hasSingleParam(fn) {
			source.HasTestMain = true
			continue
		}
		if !isGoTestFunc(fn) {
			continue
		}
		source.Funcs = append(source.Funcs, goFunc{
			Name:     fn.Name.Name,
			Subtests: subtestNames(fn),
		})
	}
	source.Symbols, source.Signals, source.Markers = inspectFile(parsed)
	sort.Slice(source.Funcs, func(i, j int) bool { return source.Funcs[i].Name < source.Funcs[j].Name })
	return source, nil
}

// inspectFile 统计文件级标记：testkit 符号、调用点数量与资源线索。
func inspectFile(parsed *ast.File) (map[string]bool, signals, fileMarkers) {
	symbols := map[string]bool{}
	var markers fileMarkers
	ast.Inspect(parsed, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch ident.Name {
		case "testkit":
			symbols[selector.Sel.Name] = true
		case "sync":
			if selector.Sel.Name == "Once" || selector.Sel.Name == "OnceFunc" {
				markers.SyncOnce = true
			}
		case "net":
			if selector.Sel.Name == "Listen" {
				markers.NetListen = true
			}
		case "httptest":
			if strings.HasPrefix(selector.Sel.Name, "New") {
				markers.HTTPTestServer = true
			}
		case "exec":
			if strings.HasPrefix(selector.Sel.Name, "Command") {
				markers.ExecCommand = true
			}
		}
		return true
	})
	var counts signals
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !isTestReceiver(selector.X) {
			return true
		}
		switch selector.Sel.Name {
		case "Parallel":
			counts.Parallel++
		case "Cleanup":
			counts.Cleanup++
		case "Helper":
			counts.Helper++
		case "TempDir":
			counts.TempDir++
		case "Fatal", "Fatalf", "FailNow":
			counts.Fatal++
		case "Error", "Errorf":
			counts.Error++
		case "Skip", "Skipf", "SkipNow":
			counts.Skip++
		case "Run":
			counts.Run++
		}
		return true
	})
	return symbols, counts, markers
}

// subtestNames 提取用例内 t.Run 的字符串字面量子测试名。
func subtestNames(fn *ast.FuncDecl) []string {
	var names []string
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Run" || !isTestReceiver(selector.X) {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		if value, err := strconv.Unquote(literal.Value); err == nil {
			names = append(names, value)
		}
		return true
	})
	return names
}

// testReceiverNames 是 Go 测试函数参数的惯用名。
//
// 调用点口径只统计这些接收者，避免把 err.Error()、http.Error 之类的普通调用算成
// 测试断言，从而让"迁移没有静默丢失"的分母可核对。
var testReceiverNames = map[string]bool{"t": true, "b": true, "f": true, "tb": true, "c": true}

// isTestReceiver 判断调用接收者是否是测试上下文参数。
func isTestReceiver(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && testReceiverNames[ident.Name]
}

// isGoTestFunc 判断是否为 Go 认可的可执行测试函数（func TestXxx(t *testing.T)）。
func isGoTestFunc(fn *ast.FuncDecl) bool {
	const prefix = "Test"
	name := fn.Name.Name
	if !strings.HasPrefix(name, prefix) || len(name) == len(prefix) {
		return false
	}
	if unicode.IsLower(rune(name[len(prefix)])) {
		return false
	}
	if !hasSingleParam(fn) {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	selector, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "testing" && selector.Sel.Name == "T"
}

// hasSingleParam 判断函数是否恰好有一个具名参数。
func hasSingleParam(fn *ast.FuncDecl) bool {
	params := fn.Type.Params
	return params != nil && len(params.List) == 1 && len(params.List[0].Names) == 1
}
