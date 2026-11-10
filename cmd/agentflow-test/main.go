// Command agentflow-test 是 AgentFlow 统一测试执行器的命令行入口。
//
// 普通调用是编排进程：选择用例、分配资源、启动 worker 并写出统一报告。--exec-case 是
// worker 内部入口，由编排进程启动，不供人工日常使用。
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"

	// 空导入聚合用例包：新增用例包只需修改 registry 一处。
	_ "github.com/lingting/agentflow/tests/runner/registry"
)

// exitUsage 表示命令行用法错误；与用例失败（1）区分开，便于 CI 判定。
const exitUsage = 2

func main() {
	os.Exit(run(os.Args[1:]))
}

// run 解析命令行并分派到编排、列用例或 worker 三种模式。
func run(args []string) int {
	flags := flag.NewFlagSet("agentflow-test", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	list := flags.Bool("list", false, "列出已注册用例后退出")
	artifactDir := flags.String("artifact-dir", ".test-artifacts", "产物根目录")
	timeout := flags.Duration("timeout", testrunner.DefaultCaseTimeout, "用例默认执行上限")
	grace := flags.Duration("grace", testrunner.DefaultWorkerGrace, "超时或取消后等待 worker 自行收尾的宽限时间")
	parallel := flags.Int("parallel", testrunner.DefaultParallel, "同时在跑的 worker 上限")
	realTests := flags.String("real-tests", testrunner.RealTestsAuto, "真实依赖授权：auto/on/off")
	execCase := flags.String("exec-case", "", "worker 内部参数：执行单个用例")
	resultFile := flags.String("result-file", "", "worker 内部参数：结果文件路径")
	handleFile := flags.String("handles", "", "worker 内部参数：资源句柄文件")
	realTestsGrant := flags.String("real-tests-grant", "", "worker 内部参数：allowed/denied")
	realTestsReason := flags.String("real-tests-reason", "", "worker 内部参数：授权结论原因")
	hold := flags.Bool("hold", false, "worker 内部参数：常驻子进程模式（资源自检使用）")
	serve := flags.String("serve", "", "worker 内部参数：自检用 HTTP 服务监听地址（资源提供者填写 {port}）")
	failRelease := flags.String(
		"selfcheck-fail-release",
		"",
		"仅自检用：让该资源类型的释放与自证必定失败，用于验证零残留终检",
	)
	cancelOn := flags.String(
		"selfcheck-cancel-on",
		"",
		"仅自检用：派发该用例后立即取消整次运行，用于验证取消路径",
	)
	lateResult := flags.Bool(
		"selfcheck-late-result",
		false,
		"仅自检用：为已定型的 timeout / error 用例写出伪造的迟到结果文件",
	)
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if *hold {
		return runHold()
	}
	if *serve != "" {
		return runSelfCheckService(*serve)
	}
	if *execCase != "" {
		return runWorker(*execCase, *resultFile, *artifactDir, *handleFile, *timeout, *realTestsGrant, *realTestsReason)
	}

	cases, err := testrunner.Cases()
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return testrunner.ExitFailure
	}
	if *list {
		printCases(cases)
		return testrunner.ExitOK
	}
	decision, err := testrunner.DecideRealTests(
		*realTests,
		os.Getenv(testrunner.CIMarkerEnv),
		os.Getenv(testrunner.RealTestsGrantEnv),
		os.Getenv(testrunner.RealTestsGrantReasonEnv),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return exitUsage
	}
	if *parallel < 1 {
		fmt.Fprintf(os.Stderr, "错误: --parallel 必须大于等于 1，当前为 %d\n", *parallel)
		return exitUsage
	}
	cancelCtx, stopSignals := testrunner.SignalContext()
	defer stopSignals()
	report, err := testrunner.Run(cancelCtx, testrunner.RunOptions{
		Cases:          cases,
		Selectors:      flags.Args(),
		DefaultTimeout: *timeout,
		Grace:          *grace,
		Parallel:       *parallel,
		ArtifactRoot:   *artifactDir,
		RealTests:      decision,
		FaultInjection: testrunner.FaultInjection{
			FailReleaseType: *failRelease,
			CancelOnCase:    *cancelOn,
			LateResult:      *lateResult,
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return testrunner.ExitFailure
	}
	return report.ExitCode
}

// runWorker 以 worker 模式执行单个用例，退出码由用例结论决定。
func runWorker(
	caseID string,
	resultPath string,
	artifactDir string,
	handlePath string,
	timeout time.Duration,
	grant string,
	reason string,
) int {
	if resultPath == "" {
		fmt.Fprintln(os.Stderr, "错误: worker 模式缺少 --result-file")
		return exitUsage
	}
	allowed, err := parseGrant(grant)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return exitUsage
	}
	result, err := testrunner.ExecuteWorker(testrunner.WorkerRequest{
		CaseID:          caseID,
		ResultPath:      resultPath,
		ArtifactDir:     artifactDir,
		HandlePath:      handlePath,
		Timeout:         timeout,
		RealTests:       allowed,
		RealTestsReason: reason,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return testrunner.ExitFailure
	}
	return result.ExitCode
}

// parseGrant 解析编排进程下发的授权结论；未识别的取值一律按拒绝处理。
func parseGrant(grant string) (bool, error) {
	switch grant {
	case "allowed":
		return true, nil
	case "denied":
		return false, nil
	default:
		return false, fmt.Errorf("未知的授权结论 %q，只接受 allowed/denied", grant)
	}
}

// printCases 以表格列出用例声明，供人工核对注册结果。
func printCases(cases []testrunner.Case) {
	fmt.Printf("%-28s %-18s %-9s %-10s %s\n", "ID", "GROUP", "PARALLEL", "TIMEOUT", "RESOURCES")
	for _, c := range cases {
		timeout := c.Timeout.String()
		if c.Timeout == 0 {
			timeout = "default"
		}
		fmt.Printf(
			"%-28s %-18s %-9t %-10s %s\n",
			c.ID,
			c.Group,
			c.Parallel,
			timeout,
			describeResources(c.Resources),
		)
	}
}

// describeResources 把资源声明压成 "name:type:scope[spec]" 列表。
func describeResources(decls []testrunner.ResourceDecl) string {
	if len(decls) == 0 {
		return "-"
	}
	values := make([]string, 0, len(decls))
	for _, decl := range decls {
		value := decl.Name + ":" + decl.Type + ":" + decl.Scope
		if len(decl.Spec) > 0 {
			keys := make([]string, 0, len(decl.Spec))
			for key := range decl.Spec {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			pairs := make([]string, 0, len(keys))
			for _, key := range keys {
				pairs = append(pairs, key+"="+decl.Spec[key])
			}
			value += "[" + strings.Join(pairs, ";") + "]"
		}
		values = append(values, value)
	}
	return strings.Join(values, ",")
}

// runHold 是资源自检用的常驻子进程入口：一直等到被父进程终止。
//
// 它只被 child-process 的默认实例使用；收到 SIGINT 时正常退出，taskkill / SIGKILL 直接结束
// 进程，两种情况都不会留下子进程。
func runHold() int {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	<-signals
	return testrunner.ExitOK
}

// runSelfCheckService 是资源自检用的 HTTP 服务入口：监听给定地址并一直提供服务。
//
// 它只被 service-process 的默认自检实例使用（编排进程以 127.0.0.1:{port} 启动），不作为
// 人工日常命令；监听失败如实返回非零退出码。
func runSelfCheckService(address string) int {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 自检服务无法监听 %s: %v\n", address, err)
		return testrunner.ExitFailure
	}
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("agentflow-test-selfcheck\n"))
	})}
	fmt.Printf("自检服务监听 %s\n", listener.Addr().String())
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "错误: 自检服务异常结束: %v\n", err)
		return testrunner.ExitFailure
	}
	return testrunner.ExitOK
}
