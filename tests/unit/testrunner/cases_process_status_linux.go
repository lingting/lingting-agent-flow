//go:build linux

package testrunner

import (
	"fmt"
	"os"
	"syscall"

	"github.com/lingting/agentflow/internal/testrunner"
)

// runProcessGoneError 验证 /proc 读取期间进程退出时，两种内核错误都按进程已消失处理。
func runProcessGoneError(t *testrunner.Context) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "ENOENT", err: os.ErrNotExist, want: true},
		{name: "wrapped ENOENT", err: fmt.Errorf("read /proc/pid/stat: %w", os.ErrNotExist), want: true},
		{name: "ESRCH", err: syscall.ESRCH, want: true},
		{name: "wrapped ESRCH", err: fmt.Errorf("read /proc/pid/stat: %w", syscall.ESRCH), want: true},
		{name: "permission denied", err: syscall.EPERM, want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testrunner.Context) {
			if got := testrunner.IsProcessGoneError(test.err); got != test.want {
				t.Fatalf("IsProcessGoneError(%v) = %t，期望 %t", test.err, got, test.want)
			}
		})
	}
}
