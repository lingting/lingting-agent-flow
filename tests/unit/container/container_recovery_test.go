// 容器残留恢复判定的单元测试。
//
// 这些用例固定的是"什么观测结论可以推动管理状态、什么必须停下来等人"这一组语义：
// 它们不读数据库、不连接远程 Docker，因此可以在任意环境完整执行。
package container_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// unknownContainer 构造一个状态不明的容器记录。
func unknownContainer(t *testing.T) domain.Container {
	t.Helper()
	container := newContainerRecord(t)
	if err := container.MarkUnknown("远程状态无法确认", time.Now().UTC()); err != nil {
		t.Fatalf("标记状态不明失败: %v", err)
	}
	return container
}

// TestDecideContainerRecoveryConvergesConfirmedStates 验证已确认的观测推动管理状态。
func TestDecideContainerRecoveryConvergesConfirmedStates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		observation domain.ContainerObservationClass
		dockerID    string
		want        domain.ContainerState
	}{
		{
			name:        "确认运行中收敛为 running",
			observation: domain.ContainerObservedRunning,
			want:        domain.ContainerStateRunning,
		},
		{
			name:        "确认已停止收敛为 stopped",
			observation: domain.ContainerObservedStopped,
			want:        domain.ContainerStateStopped,
		},
		{
			name:        "确认不存在收敛为 removed",
			observation: domain.ContainerObservedAbsent,
			want:        domain.ContainerStateRemoved,
		},
		{
			name:        "缺 ID 但按名称核验成功时补写并收敛",
			observation: domain.ContainerObservedRunning,
			dockerID:    "docker-recovered",
			want:        domain.ContainerStateRunning,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			container := unknownContainer(t)
			if testCase.dockerID != "" {
				container.DockerID = ""
			}
			decision := domain.DecideContainerRecovery(domain.ContainerRecoveryInput{
				Container:        container,
				Observation:      testCase.observation,
				ObservedDockerID: testCase.dockerID,
			})
			if !decision.Needed {
				t.Fatalf("已确认的观测必须推动收敛: %+v", decision)
			}
			if decision.Next != testCase.want {
				t.Fatalf("收敛目标不正确: 期望 %s，实际 %s", testCase.want, decision.Next)
			}
			updated, err := container.ApplyContainerRecovery(decision, time.Now().UTC())
			if err != nil {
				t.Fatalf("写回收敛结论失败: %v", err)
			}
			if updated.State != testCase.want {
				t.Fatalf("写回后状态不正确: %s", updated.State)
			}
			if testCase.dockerID != "" && updated.DockerID != testCase.dockerID {
				t.Fatalf("应补写观测到的 Docker ID，实际 %q", updated.DockerID)
			}
			if updated.ErrorSummary != "" {
				t.Fatalf("收敛成功后应清除状态不明摘要，实际 %q", updated.ErrorSummary)
			}
		})
	}
}

// TestDecideContainerRecoveryRequiresManualForRiskyObservations 验证高风险观测必须人工。
func TestDecideContainerRecoveryRequiresManualForRiskyObservations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		observation domain.ContainerObservationClass
		errorClass  string
	}{
		{
			name:        "所有权不匹配不得自动处置",
			observation: domain.ContainerObservedOwnershipMismatch,
			errorClass:  "container_ownership_mismatch",
		},
		{
			name:        "无法访问必须保留不确定",
			observation: domain.ContainerObservedUnreachable,
			errorClass:  "container_unreachable",
		},
		{
			name:        "记录不完整必须人工确认",
			observation: domain.ContainerObservedIncomplete,
			errorClass:  "container_record_incomplete",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			container := unknownContainer(t)
			decision := domain.DecideContainerRecovery(domain.ContainerRecoveryInput{
				Container:   container,
				Observation: testCase.observation,
			})
			if decision.Needed {
				t.Fatalf("高风险观测不得自动收敛: %+v", decision)
			}
			if !decision.NeedsManual {
				t.Fatal("高风险观测必须标记需要人工介入")
			}
			if decision.ErrorClass != testCase.errorClass {
				t.Fatalf("错误分类不正确: %s", decision.ErrorClass)
			}
			if !strings.Contains(domain.ContainerRecoverySummary(decision), "人工") {
				t.Fatalf("摘要必须说明需要人工介入: %s", domain.ContainerRecoverySummary(decision))
			}
		})
	}
}

// TestDecideContainerRecoveryKeepsDetachedAndRemoved 验证终态记录不被触碰。
func TestDecideContainerRecoveryKeepsDetachedAndRemoved(t *testing.T) {
	t.Parallel()
	detached := newContainerRecord(t)
	if err := detached.Detach(time.Now().UTC()); err != nil {
		t.Fatalf("脱离管理失败: %v", err)
	}
	removed := newContainerRecord(t)
	if err := removed.MarkRemoved(domain.ContainerEndReasonTaskSucceeded, time.Now().UTC()); err != nil {
		t.Fatalf("标记删除失败: %v", err)
	}
	for name, container := range map[string]domain.Container{
		"detached": detached,
		"removed":  removed,
	} {
		decision := domain.DecideContainerRecovery(domain.ContainerRecoveryInput{
			Container:   container,
			Observation: domain.ContainerObservedRunning,
		})
		if decision.Needed {
			t.Fatalf("%s 容器不得被恢复改写: %+v", name, decision)
		}
	}
}

// TestDecideContainerRecoveryRejectsUnknownObservation 验证未知观测值被拒绝。
func TestDecideContainerRecoveryRejectsUnknownObservation(t *testing.T) {
	t.Parallel()
	decision := domain.DecideContainerRecovery(domain.ContainerRecoveryInput{
		Container:   newContainerRecord(t),
		Observation: domain.ContainerObservationClass("bogus"),
	})
	if decision.Needed || !decision.NeedsManual {
		t.Fatalf("未知观测值必须被拒绝并要求人工介入: %+v", decision)
	}
}

// TestApplyContainerRecoveryRejectsIllegalTarget 验证非法收敛目标被拒绝。
func TestApplyContainerRecoveryRejectsIllegalTarget(t *testing.T) {
	t.Parallel()
	container := unknownContainer(t)
	_, err := container.ApplyContainerRecovery(domain.ContainerRecoveryDecision{
		Needed: true,
		Next:   domain.ContainerStateCreating,
	}, time.Now().UTC())
	if err == nil {
		t.Fatal("收敛目标为 creating 时必须被拒绝")
	}
}
