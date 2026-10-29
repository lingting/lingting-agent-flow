package testkit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestRunFinalizersReportsFailure 验证最终核验失败会让测试进程判定失败。
//
// "清理失败只打印 WARN 仍然通过"是被明确禁止的行为，因此这里的失败标记必须为真。
func TestRunFinalizersReportsFailure(t *testing.T) {
	t.Parallel()
	failed := testkit.RunFinalizers(
		context.Background(),
		func(ctx context.Context) (bool, error) { return false, nil },
		func(ctx context.Context) (bool, error) { return false, errors.New("容器残留") },
	)
	if !failed {
		t.Fatal("存在失败的最终核验时必须以失败结束")
	}
}

// TestRunFinalizersDistinguishesSkip 验证跳过与通过被区分，且跳过不改变退出码。
func TestRunFinalizersDistinguishesSkip(t *testing.T) {
	t.Parallel()
	failed := testkit.RunFinalizers(
		context.Background(),
		func(ctx context.Context) (bool, error) { return true, nil },
		func(ctx context.Context) (bool, error) { return false, nil },
	)
	if failed {
		t.Fatal("跳过与通过都不应让测试进程失败")
	}
}

// TestRunFinalizersRecoversPanic 验证核验函数 panic 不会被当成通过。
func TestRunFinalizersRecoversPanic(t *testing.T) {
	t.Parallel()
	failed := testkit.RunFinalizers(
		context.Background(),
		func(ctx context.Context) (bool, error) {
			panic("核验实现出错")
		},
	)
	if !failed {
		t.Fatal("核验函数 panic 必须按失败处理")
	}
}

// TestRunFinalizersWithoutItems 验证没有核验项时不影响退出码。
func TestRunFinalizersWithoutItems(t *testing.T) {
	t.Parallel()
	if failed := testkit.RunFinalizers(context.Background()); failed {
		t.Fatal("没有最终核验时不应判定失败")
	}
}
