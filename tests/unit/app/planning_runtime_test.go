package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// TestPlanningRuntimeInspectReportsPresentResources 校验现场齐备时核验给出三份"已确认存在"事实。
func TestPlanningRuntimeInspectReportsPresentResources(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{
		hostBound:      true,
		containerBound: true,
		sourceCommit:   runtimeCommit,
	})
	f.remote.trackContainer(seeded.container, seeded.session.ID.String())
	f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeCommit)
	f.remote.trackPrepContainer(
		f.prepContainerName(seeded.session),
		seeded.session.ID,
		f.host.ID,
		domain.DockerStateExited,
		false,
	)

	inspection, err := f.service.InspectPlanningRuntime(f.ctx, app.InspectPlanningRuntimeRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireNoError(t, err, "现场齐备时核验不应失败")
	if inspection.Container.Presence != app.PlanningRuntimePresencePresent {
		t.Fatalf("规划容器应当确认存在，实际 %s", inspection.Container.Presence)
	}
	if !inspection.Container.Running || inspection.Container.DockerID != seeded.container.DockerID {
		t.Fatalf("规划容器现场不符合实际：%+v", inspection.Container)
	}
	if inspection.PrepContainer.Presence != app.PlanningRuntimePresencePresent {
		t.Fatalf("准备容器应当确认存在，实际 %s", inspection.PrepContainer.Presence)
	}
	if inspection.PrepContainer.Running {
		t.Fatal("准备容器现场为已退出，不得报告运行中")
	}
	if inspection.Snapshot.Presence != app.PlanningRuntimePresencePresent {
		t.Fatalf("快照卷应当确认存在，实际 %s", inspection.Snapshot.Presence)
	}
	if !inspection.Snapshot.CommitVerified || !inspection.Snapshot.CommitMatches {
		t.Fatalf("快照 commit 应当核验一致：%+v", inspection.Snapshot)
	}
	if inspection.Snapshot.Commit != runtimeCommit {
		t.Fatalf("快照实际 commit 应为 %s，实际 %s", runtimeCommit, inspection.Snapshot.Commit)
	}
	// 核验不得修改数据库，也不得执行任何破坏性远程动作。
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.SourceCommit != runtimeCommit || loaded.ContainerID != seeded.container.ID.String() {
		t.Fatalf("核验改写了运行时绑定：%+v", loaded)
	}
	assertNoOperation(t, f.log,
		runtimeCallDeleteManaged,
		runtimeCallRemovePrep,
		runtimeCallRemoveVolume,
	)
}

// TestPlanningRuntimeInspectReportsAbsentResources 校验资源确实不存在时给出"已确认不存在"。
func TestPlanningRuntimeInspectReportsAbsentResources(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true})

	inspection, err := f.service.InspectPlanningRuntime(f.ctx, app.InspectPlanningRuntimeRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireNoError(t, err, "资源不存在时核验不应失败")
	if inspection.Container.Presence != app.PlanningRuntimePresenceAbsent {
		t.Fatalf("规划容器应当确认不存在，实际 %s", inspection.Container.Presence)
	}
	if inspection.PrepContainer.Presence != app.PlanningRuntimePresenceAbsent {
		t.Fatalf("准备容器应当确认不存在，实际 %s", inspection.PrepContainer.Presence)
	}
	if inspection.Snapshot.Presence != app.PlanningRuntimePresenceAbsent {
		t.Fatalf("快照卷应当确认不存在，实际 %s", inspection.Snapshot.Presence)
	}
	// 未登记规划容器时不应读取容器状态；快照卷不存在时不应再核验 commit。
	assertNoOperation(t, f.log, runtimeCallInspectManaged, runtimeCallVerifyCommit)
}

// TestPlanningRuntimeInspectReportsUnconfirmedResources 校验远程读取失败不得被当作资源不存在。
func TestPlanningRuntimeInspectReportsUnconfirmedResources(t *testing.T) {
	cases := []struct {
		name      string
		configure func(f *runtimeFixture, seeded runtimeSeededSession)
		assert    func(t *testing.T, inspection app.PlanningRuntimeInspection)
		category  error
	}{
		{
			name: "规划容器状态不确定",
			configure: func(f *runtimeFixture, _ runtimeSeededSession) {
				f.remote.inspectContainerErr = runtimeDockerError(docker.CodeStateUncertain, "docker inspect")
			},
			assert: func(t *testing.T, inspection app.PlanningRuntimeInspection) {
				if inspection.Container.Presence != app.PlanningRuntimePresenceUnknown {
					t.Fatalf("容器状态无法确认时必须报告 unknown，实际 %s", inspection.Container.Presence)
				}
			},
			category: app.ErrPlanningRuntimeUncertain,
		},
		{
			name: "快照卷读取失败",
			configure: func(f *runtimeFixture, _ runtimeSeededSession) {
				f.remote.inspectVolumeErr = runtimeDockerError(docker.CodeSSHUnavailable, "docker volume inspect")
			},
			assert: func(t *testing.T, inspection app.PlanningRuntimeInspection) {
				if inspection.Snapshot.Presence != app.PlanningRuntimePresenceUnknown {
					t.Fatalf("卷状态无法确认时必须报告 unknown，实际 %s", inspection.Snapshot.Presence)
				}
			},
			category: app.ErrPlanningRuntimeUncertain,
		},
		{
			name: "准备容器读取失败",
			configure: func(f *runtimeFixture, _ runtimeSeededSession) {
				f.remote.inspectPrepErr = runtimeDockerError(docker.CodeCommandFailed, "docker inspect")
			},
			assert: func(t *testing.T, inspection app.PlanningRuntimeInspection) {
				if inspection.PrepContainer.Presence != app.PlanningRuntimePresenceUnknown {
					t.Fatalf("准备容器状态无法确认时必须报告 unknown，实际 %s", inspection.PrepContainer.Presence)
				}
			},
			category: app.ErrPlanningRuntimeFailed,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newRuntimeFixture(t)
			seeded := f.seedSession(t, runtimeSeed{
				hostBound:      true,
				containerBound: true,
				sourceCommit:   runtimeCommit,
			})
			f.remote.trackContainer(seeded.container, seeded.session.ID.String())
			testCase.configure(f, seeded)
			inspection, err := f.service.InspectPlanningRuntime(f.ctx, app.InspectPlanningRuntimeRequest{
				PlanningSessionID: seeded.session.ID.String(),
			})
			requireIs(t, err, testCase.category, "无法确认的现场必须按对应类别上报")
			testCase.assert(t, inspection)
			assertNoOperation(t, f.log,
				runtimeCallDeleteManaged,
				runtimeCallRemovePrep,
				runtimeCallRemoveVolume,
			)
		})
	}
}

// TestPlanningRuntimeInspectReportsOwnershipMismatch 校验标签不一致时报告 mismatch 与冲突。
func TestPlanningRuntimeInspectReportsOwnershipMismatch(t *testing.T) {
	t.Run("快照卷标签不属于本会话", func(t *testing.T) {
		f := newRuntimeFixture(t)
		seeded := f.seedSession(t, runtimeSeed{hostBound: true})
		volumeName := f.volumeName(seeded.session)
		f.remote.trackVolume(volumeName, seeded.session.ID, f.host.ID, runtimeCommit)
		f.remote.setVolumeLabels(volumeName, map[string]string{
			docker.OwnershipLabelKey:       "true",
			docker.HostLabelKey:            f.host.ID.String(),
			docker.PlanningSessionLabelKey: "另一个规划会话",
		})
		inspection, err := f.service.InspectPlanningRuntime(f.ctx, app.InspectPlanningRuntimeRequest{
			PlanningSessionID: seeded.session.ID.String(),
		})
		requireIs(t, err, app.ErrPlanningRuntimeConflict, "归属不成立必须返回冲突")
		if inspection.Snapshot.Presence != app.PlanningRuntimePresenceMismatch {
			t.Fatalf("快照卷归属不成立时必须报告 mismatch，实际 %s", inspection.Snapshot.Presence)
		}
	})
	t.Run("准备容器标签不属于本节点", func(t *testing.T) {
		f := newRuntimeFixture(t)
		seeded := f.seedSession(t, runtimeSeed{hostBound: true})
		name := f.prepContainerName(seeded.session)
		f.remote.trackPrepContainer(name, seeded.session.ID, f.host.ID, domain.DockerStateExited, false)
		f.remote.setPrepLabels(name, map[string]string{
			docker.OwnershipLabelKey:       "true",
			docker.HostLabelKey:            "另一个执行节点",
			docker.PlanningSessionLabelKey: seeded.session.ID.String(),
		})
		inspection, err := f.service.InspectPlanningRuntime(f.ctx, app.InspectPlanningRuntimeRequest{
			PlanningSessionID: seeded.session.ID.String(),
		})
		requireIs(t, err, app.ErrPlanningRuntimeConflict, "归属不成立必须返回冲突")
		if inspection.PrepContainer.Presence != app.PlanningRuntimePresenceMismatch {
			t.Fatalf("准备容器归属不成立时必须报告 mismatch，实际 %s", inspection.PrepContainer.Presence)
		}
	})
	t.Run("规划容器远端标签不一致", func(t *testing.T) {
		f := newRuntimeFixture(t)
		seeded := f.seedSession(t, runtimeSeed{
			hostBound:      true,
			containerBound: true,
			sourceCommit:   runtimeCommit,
		})
		f.remote.inspectContainerErr = runtimeDockerError(docker.CodeOwnershipMismatch, "ownership")
		inspection, err := f.service.InspectPlanningRuntime(f.ctx, app.InspectPlanningRuntimeRequest{
			PlanningSessionID: seeded.session.ID.String(),
		})
		requireIs(t, err, app.ErrPlanningRuntimeConflict, "容器归属不成立必须返回冲突")
		if inspection.Container.Presence != app.PlanningRuntimePresenceMismatch {
			t.Fatalf("容器归属不成立时必须报告 mismatch，实际 %s", inspection.Container.Presence)
		}
	})
	t.Run("容器登记记录属于其他节点", func(t *testing.T) {
		f := newRuntimeFixture(t)
		seeded := f.seedSession(t, runtimeSeed{
			hostBound:      true,
			containerBound: true,
			sourceCommit:   runtimeCommit,
			containerHost:  "another-host",
		})
		f.remote.trackContainer(seeded.container, seeded.session.ID.String())
		inspection, err := f.service.InspectPlanningRuntime(f.ctx, app.InspectPlanningRuntimeRequest{
			PlanningSessionID: seeded.session.ID.String(),
		})
		requireIs(t, err, app.ErrPlanningRuntimeConflict, "节点不一致必须返回冲突")
		if inspection.Container.Presence != app.PlanningRuntimePresenceMismatch {
			t.Fatalf("节点不一致时必须报告 mismatch，实际 %s", inspection.Container.Presence)
		}
		assertNoOperation(t, f.log, runtimeCallDeleteManaged)
	})
}

// TestPlanningRuntimeInspectReportsCommitMismatch 校验固定 commit 不一致时返回冲突。
func TestPlanningRuntimeInspectReportsCommitMismatch(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true, sourceCommit: runtimeCommit})
	f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeOtherCommit)

	inspection, err := f.service.InspectPlanningRuntime(f.ctx, app.InspectPlanningRuntimeRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireIs(t, err, app.ErrPlanningRuntimeConflict, "commit 不一致必须返回冲突")
	if inspection.Snapshot.CommitMatches {
		t.Fatal("commit 不一致时不得报告一致")
	}
	if inspection.Snapshot.Commit != runtimeOtherCommit {
		t.Fatalf("应报告远端实际 commit %s，实际 %s", runtimeOtherCommit, inspection.Snapshot.Commit)
	}
	// 冲突结论不得改写数据库中的定位信息。
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.SourceCommit != runtimeCommit {
		t.Fatalf("冲突处理不得覆盖已记录的 commit，实际 %s", loaded.SourceCommit)
	}
}

// TestPlanningRuntimeInspectWithoutBindingDoesNotTouchRemote 校验未绑定节点时不访问远程。
func TestPlanningRuntimeInspectWithoutBindingDoesNotTouchRemote(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{})

	inspection, err := f.service.InspectPlanningRuntime(f.ctx, app.InspectPlanningRuntimeRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireNoError(t, err, "未绑定节点时核验不应失败")
	if inspection.Host != nil {
		t.Fatal("未绑定执行节点时不得报告执行节点")
	}
	for name, presence := range map[string]app.PlanningRuntimeResourcePresence{
		"规划容器": inspection.Container.Presence,
		"准备容器": inspection.PrepContainer.Presence,
		"快照卷":  inspection.Snapshot.Presence,
	} {
		if presence != app.PlanningRuntimePresenceAbsent {
			t.Fatalf("%s 应当确认不存在，实际 %s", name, presence)
		}
	}
	if events := f.log.snapshot(); len(events) != 0 {
		t.Fatalf("未绑定节点时不得访问远程，实际动作 %v", events)
	}
}

// TestPlanningRuntimeCleanupPreparationRemovesStoppedContainer 校验已停止准备容器核验后删除。
func TestPlanningRuntimeCleanupPreparationRemovesStoppedContainer(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true})
	name := f.prepContainerName(seeded.session)
	f.remote.trackPrepContainer(name, seeded.session.ID, f.host.ID, domain.DockerStateExited, false)
	// 快照卷必须留在现场：清理准备容器不得顺带删除卷。
	f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeCommit)

	result, err := f.service.CleanupPlanningSnapshotPreparation(
		f.ctx,
		app.CleanupPlanningSnapshotPreparationRequest{PlanningSessionID: seeded.session.ID.String()},
	)
	requireNoError(t, err, "已停止的准备容器应当可以清理")
	if !result.Removed || result.AlreadyAbsent {
		t.Fatalf("清理结果应当报告已删除：%+v", result)
	}
	if result.ContainerName != name {
		t.Fatalf("清理结果应给出准备容器名称 %s，实际 %s", name, result.ContainerName)
	}
	if f.remote.hasPrepHolder(name) {
		t.Fatal("删除后准备容器现场必须不存在")
	}
	if !f.remote.hasVolumeHolder(f.volumeName(seeded.session)) {
		t.Fatal("清理准备容器不得删除快照卷")
	}
	assertOperation(t, f.log, runtimeCallRemovePrep)
	assertNoOperation(t, f.log, runtimeCallRemoveVolume, runtimeCallDeleteManaged)
	// 清理不改写数据库绑定。
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.HostID == nil || *loaded.HostID != f.host.ID {
		t.Fatalf("清理不得修改运行时绑定：%+v", loaded)
	}
}

// TestPlanningRuntimeCleanupPreparationIsIdempotentWhenAbsent 校验容器不存在时按已清理处理。
func TestPlanningRuntimeCleanupPreparationIsIdempotentWhenAbsent(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true})

	result, err := f.service.CleanupPlanningSnapshotPreparation(
		f.ctx,
		app.CleanupPlanningSnapshotPreparationRequest{PlanningSessionID: seeded.session.ID.String()},
	)
	requireNoError(t, err, "准备容器不存在时必须幂等成功")
	if result.Removed || !result.AlreadyAbsent {
		t.Fatalf("不存在时应报告已清理：%+v", result)
	}
	assertNoOperation(t, f.log, runtimeCallRemovePrep, runtimeCallRemoveVolume)
}

// TestPlanningRuntimeCleanupPreparationRefusesRunningContainer 校验运行中的准备容器拒绝清理。
func TestPlanningRuntimeCleanupPreparationRefusesRunningContainer(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true})
	name := f.prepContainerName(seeded.session)
	f.remote.trackPrepContainer(name, seeded.session.ID, f.host.ID, domain.DockerStateRunning, true)

	_, err := f.service.CleanupPlanningSnapshotPreparation(
		f.ctx,
		app.CleanupPlanningSnapshotPreparationRequest{PlanningSessionID: seeded.session.ID.String()},
	)
	requireIs(t, err, app.ErrPlanningRuntimeConflict, "运行中的准备容器必须拒绝清理")
	if !f.remote.hasPrepHolder(name) {
		t.Fatal("拒绝清理时不得删除准备容器")
	}
	assertNoOperation(t, f.log, runtimeCallRemovePrep)
}

// TestPlanningRuntimeCleanupPreparationRefusesUnconfirmedState 校验状态无法确认时拒绝清理。
func TestPlanningRuntimeCleanupPreparationRefusesUnconfirmedState(t *testing.T) {
	t.Run("读取失败", func(t *testing.T) {
		f := newRuntimeFixture(t)
		seeded := f.seedSession(t, runtimeSeed{hostBound: true})
		f.remote.inspectPrepErr = runtimeDockerError(docker.CodeStateUncertain, "docker inspect")
		_, err := f.service.CleanupPlanningSnapshotPreparation(
			f.ctx,
			app.CleanupPlanningSnapshotPreparationRequest{PlanningSessionID: seeded.session.ID.String()},
		)
		requireIs(t, err, app.ErrPlanningRuntimeUncertain, "读取失败必须按不确定上报")
		assertNoOperation(t, f.log, runtimeCallRemovePrep)
	})
	t.Run("未开始运行", func(t *testing.T) {
		f := newRuntimeFixture(t)
		seeded := f.seedSession(t, runtimeSeed{hostBound: true})
		name := f.prepContainerName(seeded.session)
		f.remote.trackPrepContainer(name, seeded.session.ID, f.host.ID, domain.DockerStateCreated, false)
		_, err := f.service.CleanupPlanningSnapshotPreparation(
			f.ctx,
			app.CleanupPlanningSnapshotPreparationRequest{PlanningSessionID: seeded.session.ID.String()},
		)
		requireIs(t, err, app.ErrPlanningRuntimeUncertain, "未退出也未运行的容器不得删除")
		if !f.remote.hasPrepHolder(name) {
			t.Fatal("拒绝清理时不得删除准备容器")
		}
		assertNoOperation(t, f.log, runtimeCallRemovePrep)
	})
}

// TestPlanningRuntimeCleanupPreparationRefusesForeignOwnership 校验归属不成立时拒绝删除。
func TestPlanningRuntimeCleanupPreparationRefusesForeignOwnership(t *testing.T) {
	cases := []struct {
		name   string
		labels func(f *runtimeFixture, seeded runtimeSeededSession) map[string]string
	}{
		{
			name: "其他规划会话",
			labels: func(f *runtimeFixture, _ runtimeSeededSession) map[string]string {
				return map[string]string{
					docker.OwnershipLabelKey:       "true",
					docker.HostLabelKey:            f.host.ID.String(),
					docker.PlanningSessionLabelKey: "另一个规划会话",
				}
			},
		},
		{
			name: "其他执行节点",
			labels: func(_ *runtimeFixture, seeded runtimeSeededSession) map[string]string {
				return map[string]string{
					docker.OwnershipLabelKey:       "true",
					docker.HostLabelKey:            "另一个执行节点",
					docker.PlanningSessionLabelKey: seeded.session.ID.String(),
				}
			},
		},
		{
			name: "正式受管理容器",
			labels: func(f *runtimeFixture, seeded runtimeSeededSession) map[string]string {
				return map[string]string{
					docker.OwnershipLabelKey:       "true",
					docker.HostLabelKey:            f.host.ID.String(),
					docker.PlanningSessionLabelKey: seeded.session.ID.String(),
					docker.ContainerLabelKey:       "正式容器登记标识",
				}
			},
		},
		{
			name: "缺少管理标签",
			labels: func(f *runtimeFixture, seeded runtimeSeededSession) map[string]string {
				return map[string]string{
					docker.HostLabelKey:            f.host.ID.String(),
					docker.PlanningSessionLabelKey: seeded.session.ID.String(),
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newRuntimeFixture(t)
			seeded := f.seedSession(t, runtimeSeed{hostBound: true})
			name := f.prepContainerName(seeded.session)
			f.remote.trackPrepContainer(name, seeded.session.ID, f.host.ID, domain.DockerStateExited, false)
			f.remote.setPrepLabels(name, testCase.labels(f, seeded))
			_, err := f.service.CleanupPlanningSnapshotPreparation(
				f.ctx,
				app.CleanupPlanningSnapshotPreparationRequest{PlanningSessionID: seeded.session.ID.String()},
			)
			requireIs(t, err, app.ErrPlanningRuntimeConflict, "归属不成立必须返回冲突")
			if !f.remote.hasPrepHolder(name) {
				t.Fatal("归属不成立时不得删除准备容器")
			}
			assertNoOperation(t, f.log, runtimeCallRemovePrep)
		})
	}
}

// TestPlanningRuntimeCleanupPreparationKeepsSceneOnUncertainRemoval 校验删除结果不确定时保留现场。
func TestPlanningRuntimeCleanupPreparationKeepsSceneOnUncertainRemoval(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true})
	name := f.prepContainerName(seeded.session)
	f.remote.trackPrepContainer(name, seeded.session.ID, f.host.ID, domain.DockerStateExited, false)
	f.remote.removePrepErr = runtimeDockerError(docker.CodeStateUncertain, "docker rm")

	result, err := f.service.CleanupPlanningSnapshotPreparation(
		f.ctx,
		app.CleanupPlanningSnapshotPreparationRequest{PlanningSessionID: seeded.session.ID.String()},
	)
	requireIs(t, err, app.ErrPlanningRuntimeUncertain, "删除结果不确定必须按不确定上报")
	if result.Removed {
		t.Fatal("删除结果不确定时不得报告已清理")
	}
	// 不确定时不再发起第二次删除，也不追加删除后的核验。
	if count := f.log.count(runtimeCallRemovePrep); count != 1 {
		t.Fatalf("删除命令只应执行一次，实际 %d 次：%v", count, f.log.snapshot())
	}
}

// TestPlanningRuntimeCleanupPreparationRequiresConfirmationAfterRemoval 校验删除后必须再次核验。
func TestPlanningRuntimeCleanupPreparationRequiresConfirmationAfterRemoval(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true})
	name := f.prepContainerName(seeded.session)
	f.remote.trackPrepContainer(name, seeded.session.ID, f.host.ID, domain.DockerStateExited, false)
	f.remote.removeKeepsResource = true

	result, err := f.service.CleanupPlanningSnapshotPreparation(
		f.ctx,
		app.CleanupPlanningSnapshotPreparationRequest{PlanningSessionID: seeded.session.ID.String()},
	)
	requireIs(t, err, app.ErrPlanningRuntimeFailed, "删除后仍然存在必须按失败上报")
	if result.Removed {
		t.Fatal("删除后仍然存在时不得报告已清理")
	}
}

// TestPlanningRuntimeRecoverSnapshotPersistsCommit 校验完整快照可以恢复并落库固定 commit。
func TestPlanningRuntimeRecoverSnapshotPersistsCommit(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true})
	f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeCommit)

	result, err := f.service.RecoverPlanningSnapshot(f.ctx, app.RecoverPlanningSnapshotRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireNoError(t, err, "完整快照应当可以恢复")
	if !result.Persisted || result.AlreadyRecorded {
		t.Fatalf("恢复结果应当报告已写入 commit：%+v", result)
	}
	if result.Commit != runtimeCommit {
		t.Fatalf("恢复的实际 commit 应为 %s，实际 %s", runtimeCommit, result.Commit)
	}
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.SourceCommit != runtimeCommit {
		t.Fatalf("恢复后数据库应记录固定 commit，实际 %q", loaded.SourceCommit)
	}
	if loaded.ContainerID != "" {
		t.Fatalf("恢复不得凭空登记规划容器，实际 %q", loaded.ContainerID)
	}
	if loaded.HostID == nil || *loaded.HostID != f.host.ID {
		t.Fatalf("恢复应保留执行节点绑定：%+v", loaded)
	}
	// 恢复只核验已有快照：不 Clone、不删除任何资源。
	assertNoOperation(t, f.log, runtimeCallRemoveVolume, runtimeCallDeleteManaged, runtimeCallRemovePrep)
}

// TestPlanningRuntimeRecoverSnapshotIsIdempotentWithSameCommit 校验相同 commit 幂等成功。
func TestPlanningRuntimeRecoverSnapshotIsIdempotentWithSameCommit(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true, sourceCommit: runtimeCommit})
	f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeCommit)

	result, err := f.service.RecoverPlanningSnapshot(f.ctx, app.RecoverPlanningSnapshotRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireNoError(t, err, "同一 commit 的恢复应当幂等成功")
	if result.Persisted || !result.AlreadyRecorded {
		t.Fatalf("相同 commit 不应重复写入：%+v", result)
	}
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.SourceCommit != runtimeCommit || !loaded.UpdatedAt.Equal(seeded.session.UpdatedAt) {
		t.Fatalf("幂等恢复不得改写会话记录：%+v", loaded)
	}
}

// TestPlanningRuntimeRecoverSnapshotRefusesDifferentCommit 校验不一致的 commit 拒绝覆盖。
func TestPlanningRuntimeRecoverSnapshotRefusesDifferentCommit(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true, sourceCommit: runtimeOtherCommit})
	f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeCommit)

	_, err := f.service.RecoverPlanningSnapshot(f.ctx, app.RecoverPlanningSnapshotRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireIs(t, err, app.ErrPlanningRuntimeConflict, "commit 不一致必须返回冲突")
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.SourceCommit != runtimeOtherCommit {
		t.Fatalf("冲突时不得覆盖已记录的 commit，实际 %s", loaded.SourceCommit)
	}
}

// TestPlanningRuntimeRecoverSnapshotRefusesPresentPrepContainer 校验准备容器存在时拒绝核验。
func TestPlanningRuntimeRecoverSnapshotRefusesPresentPrepContainer(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true})
	f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeCommit)
	// 即使准备容器已经退出，也必须先显式清理，不能在此处顺手删除或继续核验。
	f.remote.trackPrepContainer(
		f.prepContainerName(seeded.session),
		seeded.session.ID,
		f.host.ID,
		domain.DockerStateExited,
		false,
	)

	_, err := f.service.RecoverPlanningSnapshot(f.ctx, app.RecoverPlanningSnapshotRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireIs(t, err, app.ErrPlanningRuntimeConflict, "准备容器存在时必须拒绝恢复")
	assertNoOperation(t, f.log, runtimeCallVerifyCommit, runtimeCallRemovePrep, runtimeCallRemoveVolume)
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.SourceCommit != "" {
		t.Fatalf("拒绝恢复时不得写入 commit，实际 %s", loaded.SourceCommit)
	}
}

// TestPlanningRuntimeRecoverSnapshotRefusesIncompleteSnapshot 校验不完整快照不算恢复成功。
func TestPlanningRuntimeRecoverSnapshotRefusesIncompleteSnapshot(t *testing.T) {
	cases := []struct {
		name     string
		injected error
		category error
	}{
		{
			name:     "核验命令明确失败",
			injected: runtimeDockerError(docker.CodeCommandFailed, "docker run"),
			category: app.ErrPlanningRuntimeFailed,
		},
		{
			name:     "核验结果不确定",
			injected: runtimeDockerError(docker.CodeStateUncertain, "docker run"),
			category: app.ErrPlanningRuntimeUncertain,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newRuntimeFixture(t)
			seeded := f.seedSession(t, runtimeSeed{hostBound: true})
			f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeCommit)
			f.remote.verifyCommitErr = testCase.injected
			_, err := f.service.RecoverPlanningSnapshot(f.ctx, app.RecoverPlanningSnapshotRequest{
				PlanningSessionID: seeded.session.ID.String(),
			})
			requireIs(t, err, testCase.category, "核验失败必须按对应类别上报")
			loaded := f.loadSession(t, seeded.session.ID)
			if loaded.SourceCommit != "" {
				t.Fatalf("核验失败时不得更新绑定，实际 %s", loaded.SourceCommit)
			}
		})
	}
}

// TestPlanningRuntimeRecoverSnapshotKeepsLatestBindingOnConflict 校验并发改写绑定不被旧值覆盖。
func TestPlanningRuntimeRecoverSnapshotKeepsLatestBindingOnConflict(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true})
	f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeCommit)
	var updateErr error
	f.remote.setHook(func(operation string) {
		if operation != runtimeCallVerifyCommit {
			return
		}
		// 模拟另一个入口在核验期间写入新的运行时绑定。
		updateErr = f.db.UpdatePlanningSessionRuntimeBinding(
			f.ctx,
			seeded.session.ID,
			storage.PlanningSessionRuntimeBinding{HostID: &f.host.ID},
			storage.PlanningSessionRuntimeBinding{HostID: &f.host.ID, SourceCommit: runtimeOtherCommit},
			time.Now().UTC(),
		)
	})

	_, err := f.service.RecoverPlanningSnapshot(f.ctx, app.RecoverPlanningSnapshotRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireNoError(t, updateErr, "并发写入绑定应当成功")
	requireIs(t, err, app.ErrPlanningRuntimeConflict, "绑定被改写时必须返回冲突")
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.SourceCommit != runtimeOtherCommit {
		t.Fatalf("条件更新冲突时必须保留最新数据库值，实际 %s", loaded.SourceCommit)
	}
}

// TestPlanningRuntimeDiscardIncompleteSnapshotRemovesVolume 校验不完整快照可以显式废弃。
func TestPlanningRuntimeDiscardIncompleteSnapshotRemovesVolume(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true})
	volumeName := f.volumeName(seeded.session)
	f.remote.trackVolume(volumeName, seeded.session.ID, f.host.ID, runtimeCommit)

	result, err := f.service.DiscardIncompletePlanningSnapshot(
		f.ctx,
		app.DiscardIncompletePlanningSnapshotRequest{PlanningSessionID: seeded.session.ID.String()},
	)
	requireNoError(t, err, "未固定、未绑定且准备容器不存在的快照应当可以废弃")
	if !result.Removed || result.AlreadyAbsent {
		t.Fatalf("废弃结果应当报告已删除：%+v", result)
	}
	if f.remote.hasVolumeHolder(volumeName) {
		t.Fatal("废弃后快照卷必须不存在")
	}
	assertOperation(t, f.log, runtimeCallRemoveVolume)
	// 废弃不清空 SourceCommit、不改业务状态、不删除容器与准备容器。
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.SourceCommit != "" || loaded.ContainerID != "" {
		t.Fatalf("废弃不得改写数据库绑定之外的事实：%+v", loaded)
	}
	if loaded.HostID == nil || *loaded.HostID != f.host.ID {
		t.Fatalf("废弃不得清空执行节点绑定：%+v", loaded)
	}
	assertNoOperation(t, f.log, runtimeCallDeleteManaged, runtimeCallRemovePrep)
}

// TestPlanningRuntimeDiscardIncompleteSnapshotRefusesBoundSnapshot 校验已固定的快照拒绝废弃。
func TestPlanningRuntimeDiscardIncompleteSnapshotRefusesBoundSnapshot(t *testing.T) {
	t.Run("已记录固定 commit", func(t *testing.T) {
		f := newRuntimeFixture(t)
		seeded := f.seedSession(t, runtimeSeed{hostBound: true, sourceCommit: runtimeCommit})
		f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeCommit)
		_, err := f.service.DiscardIncompletePlanningSnapshot(
			f.ctx,
			app.DiscardIncompletePlanningSnapshotRequest{PlanningSessionID: seeded.session.ID.String()},
		)
		requireIs(t, err, app.ErrPlanningRuntimeConflict, "已固定 commit 的快照不得废弃")
		assertNoOperation(t, f.log, runtimeCallRemoveVolume)
	})
	t.Run("仍有规划容器引用", func(t *testing.T) {
		f := newRuntimeFixture(t)
		seeded := f.seedSession(t, runtimeSeed{hostBound: true, containerBound: true})
		volumeName := f.volumeName(seeded.session)
		f.remote.trackVolume(volumeName, seeded.session.ID, f.host.ID, runtimeCommit)
		_, err := f.service.DiscardIncompletePlanningSnapshot(
			f.ctx,
			app.DiscardIncompletePlanningSnapshotRequest{PlanningSessionID: seeded.session.ID.String()},
		)
		requireIs(t, err, app.ErrPlanningRuntimeConflict, "被容器引用的快照不得废弃")
		if !f.remote.hasVolumeHolder(volumeName) {
			t.Fatal("拒绝废弃时不得删除快照卷")
		}
		assertNoOperation(t, f.log, runtimeCallRemoveVolume)
	})
}

// TestPlanningRuntimeDiscardIncompleteSnapshotRefusesPrepContainer 校验准备容器存在时拒绝废弃。
func TestPlanningRuntimeDiscardIncompleteSnapshotRefusesPrepContainer(t *testing.T) {
	t.Run("准备容器仍存在", func(t *testing.T) {
		f := newRuntimeFixture(t)
		seeded := f.seedSession(t, runtimeSeed{hostBound: true})
		volumeName := f.volumeName(seeded.session)
		f.remote.trackVolume(volumeName, seeded.session.ID, f.host.ID, runtimeCommit)
		f.remote.trackPrepContainer(
			f.prepContainerName(seeded.session),
			seeded.session.ID,
			f.host.ID,
			domain.DockerStateExited,
			false,
		)
		_, err := f.service.DiscardIncompletePlanningSnapshot(
			f.ctx,
			app.DiscardIncompletePlanningSnapshotRequest{PlanningSessionID: seeded.session.ID.String()},
		)
		requireIs(t, err, app.ErrPlanningRuntimeConflict, "准备容器存在时不得废弃快照")
		if !f.remote.hasVolumeHolder(volumeName) {
			t.Fatal("拒绝废弃时不得删除快照卷")
		}
		assertNoOperation(t, f.log, runtimeCallRemoveVolume)
	})
	t.Run("准备容器状态不确定", func(t *testing.T) {
		f := newRuntimeFixture(t)
		seeded := f.seedSession(t, runtimeSeed{hostBound: true})
		volumeName := f.volumeName(seeded.session)
		f.remote.trackVolume(volumeName, seeded.session.ID, f.host.ID, runtimeCommit)
		f.remote.inspectPrepErr = runtimeDockerError(docker.CodeStateUncertain, "docker inspect")
		_, err := f.service.DiscardIncompletePlanningSnapshot(
			f.ctx,
			app.DiscardIncompletePlanningSnapshotRequest{PlanningSessionID: seeded.session.ID.String()},
		)
		requireIs(t, err, app.ErrPlanningRuntimeUncertain, "准备容器状态不确定时必须按不确定上报")
		if !f.remote.hasVolumeHolder(volumeName) {
			t.Fatal("拒绝废弃时不得删除快照卷")
		}
	})
}

// TestPlanningRuntimeDiscardIncompleteSnapshotRefusesOwnershipMismatch 校验归属不成立拒绝废弃。
func TestPlanningRuntimeDiscardIncompleteSnapshotRefusesOwnershipMismatch(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true})
	volumeName := f.volumeName(seeded.session)
	f.remote.trackVolume(volumeName, seeded.session.ID, f.host.ID, runtimeCommit)
	f.remote.setVolumeLabels(volumeName, map[string]string{
		docker.OwnershipLabelKey:       "true",
		docker.HostLabelKey:            f.host.ID.String(),
		docker.PlanningSessionLabelKey: "另一个规划会话",
	})

	_, err := f.service.DiscardIncompletePlanningSnapshot(
		f.ctx,
		app.DiscardIncompletePlanningSnapshotRequest{PlanningSessionID: seeded.session.ID.String()},
	)
	requireIs(t, err, app.ErrPlanningRuntimeConflict, "归属不成立必须拒绝废弃")
	if !f.remote.hasVolumeHolder(volumeName) {
		t.Fatal("归属不成立时不得删除快照卷")
	}
	assertNoOperation(t, f.log, runtimeCallRemoveVolume)
}

// TestPlanningRuntimeDiscardIncompleteSnapshotKeepsBindingOnUncertainRemoval 校验删除不确定时保留绑定。
func TestPlanningRuntimeDiscardIncompleteSnapshotKeepsBindingOnUncertainRemoval(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true})
	volumeName := f.volumeName(seeded.session)
	f.remote.trackVolume(volumeName, seeded.session.ID, f.host.ID, runtimeCommit)
	f.remote.removeVolumeErr = runtimeDockerError(docker.CodeStateUncertain, "docker volume rm")

	result, err := f.service.DiscardIncompletePlanningSnapshot(
		f.ctx,
		app.DiscardIncompletePlanningSnapshotRequest{PlanningSessionID: seeded.session.ID.String()},
	)
	requireIs(t, err, app.ErrPlanningRuntimeUncertain, "删除结果不确定必须按不确定上报")
	if result.Removed {
		t.Fatal("删除结果不确定时不得报告已废弃")
	}
	if count := f.log.count(runtimeCallRemoveVolume); count != 1 {
		t.Fatalf("删除命令只应执行一次，实际 %d 次：%v", count, f.log.snapshot())
	}
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.HostID == nil || *loaded.HostID != f.host.ID {
		t.Fatalf("不确定时不得修改数据库绑定：%+v", loaded)
	}
}

// TestPlanningRuntimeDiscardIncompleteSnapshotRequiresConfirmation 校验删除后必须确认卷不存在。
func TestPlanningRuntimeDiscardIncompleteSnapshotRequiresConfirmation(t *testing.T) {
	t.Run("删除后仍然存在", func(t *testing.T) {
		f := newRuntimeFixture(t)
		seeded := f.seedSession(t, runtimeSeed{hostBound: true})
		f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeCommit)
		f.remote.removeKeepsResource = true
		result, err := f.service.DiscardIncompletePlanningSnapshot(
			f.ctx,
			app.DiscardIncompletePlanningSnapshotRequest{PlanningSessionID: seeded.session.ID.String()},
		)
		requireIs(t, err, app.ErrPlanningRuntimeFailed, "删除后仍然存在必须按失败上报")
		if result.Removed {
			t.Fatal("删除后仍然存在时不得报告已废弃")
		}
	})
	t.Run("卷已不存在", func(t *testing.T) {
		f := newRuntimeFixture(t)
		seeded := f.seedSession(t, runtimeSeed{hostBound: true})
		result, err := f.service.DiscardIncompletePlanningSnapshot(
			f.ctx,
			app.DiscardIncompletePlanningSnapshotRequest{PlanningSessionID: seeded.session.ID.String()},
		)
		requireNoError(t, err, "卷不存在时废弃应当幂等成功")
		if result.Removed || !result.AlreadyAbsent {
			t.Fatalf("卷不存在时应报告已废弃：%+v", result)
		}
		assertNoOperation(t, f.log, runtimeCallRemoveVolume)
	})
}

// TestPlanningRuntimeReleaseStopsWhenContainerRemovalUncertain 校验容器删除不确定时立即停止。
func TestPlanningRuntimeReleaseStopsWhenContainerRemovalUncertain(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{
		hostBound:      true,
		containerBound: true,
		sourceCommit:   runtimeCommit,
	})
	f.remote.trackContainer(seeded.container, seeded.session.ID.String())
	f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeCommit)
	f.remote.deleteContainerErr = runtimeDockerError(docker.CodeStateUncertain, "docker rm")

	_, err := f.service.Release(f.ctx, app.ReleasePlanningRuntimeRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireIs(t, err, app.ErrPlanningRuntimeUncertain, "容器删除结果不确定必须按不确定上报")
	// 容器删除未确认前不得继续删除快照卷，也不得清空绑定。
	assertNoOperation(t, f.log, runtimeCallRemoveVolume)
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.ContainerID != seeded.container.ID.String() || loaded.SourceCommit != runtimeCommit {
		t.Fatalf("不确定时不得清空运行时绑定：%+v", loaded)
	}
}

// TestPlanningRuntimeReleaseKeepsBindingWhenSnapshotRemovalUncertain 校验卷删除不确定时保留绑定。
func TestPlanningRuntimeReleaseKeepsBindingWhenSnapshotRemovalUncertain(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{
		hostBound:      true,
		containerBound: true,
		sourceCommit:   runtimeCommit,
	})
	f.remote.trackContainer(seeded.container, seeded.session.ID.String())
	f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeCommit)
	f.remote.removeVolumeErr = runtimeDockerError(docker.CodeStateUncertain, "docker volume rm")

	result, err := f.service.Release(f.ctx, app.ReleasePlanningRuntimeRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireIs(t, err, app.ErrPlanningRuntimeUncertain, "卷删除结果不确定必须按不确定上报")
	if result.SnapshotReleased {
		t.Fatal("卷删除结果不确定时不得报告已释放")
	}
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.SourceCommit != runtimeCommit || loaded.ContainerID != seeded.container.ID.String() {
		t.Fatalf("不确定时不得清空运行时绑定：%+v", loaded)
	}
}

// TestPlanningRuntimeReleaseCompletesWhenResourcesConfirmedAbsent 校验已确认不存在按幂等完成。
func TestPlanningRuntimeReleaseCompletesWhenResourcesConfirmedAbsent(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{
		hostBound:      true,
		containerBound: true,
		sourceCommit:   runtimeCommit,
	})

	result, err := f.service.Release(f.ctx, app.ReleasePlanningRuntimeRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireNoError(t, err, "已确认不存在的资源应当可以完成释放")
	if !result.ContainerReleased || !result.SnapshotReleased {
		t.Fatalf("已确认不存在的资源应报告已释放：%+v", result)
	}
	assertNoOperation(t, f.log, runtimeCallRemoveVolume)
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.HostID != nil || loaded.ContainerID != "" || loaded.SourceCommit != "" {
		t.Fatalf("释放完成后运行时绑定必须清空：%+v", loaded)
	}
}

// TestPlanningRuntimeReleaseDeletesOwnedResources 校验归属正确的资源按顺序释放。
func TestPlanningRuntimeReleaseDeletesOwnedResources(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{
		hostBound:      true,
		containerBound: true,
		sourceCommit:   runtimeCommit,
	})
	f.remote.trackContainer(seeded.container, seeded.session.ID.String())
	volumeName := f.volumeName(seeded.session)
	f.remote.trackVolume(volumeName, seeded.session.ID, f.host.ID, runtimeCommit)

	result, err := f.service.Release(f.ctx, app.ReleasePlanningRuntimeRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireNoError(t, err, "归属正确的资源应当可以释放")
	if !result.ContainerReleased || !result.SnapshotReleased {
		t.Fatalf("释放结果应报告两项资源已释放：%+v", result)
	}
	if f.remote.hasVolumeHolder(volumeName) {
		t.Fatal("释放后快照卷必须不存在")
	}
	events := f.log.snapshot()
	// 先核验容器，再删除容器，最后处理快照卷：顺序必须与文档一致。
	order := []string{
		runtimeCallInspectManaged,
		runtimeCallDeleteManaged,
		runtimeCallRemoveVolume,
	}
	index := 0
	for _, event := range events {
		if index < len(order) && event == order[index] {
			index++
		}
	}
	if index != len(order) {
		t.Fatalf("释放顺序不符合预期，实际动作序列 %v", events)
	}
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.HostID != nil || loaded.ContainerID != "" || loaded.SourceCommit != "" {
		t.Fatalf("释放完成后运行时绑定必须清空：%+v", loaded)
	}
}

// TestPlanningRuntimeReleaseKeepsBindingOnOwnershipMismatch 校验归属不成立时不清空绑定。
func TestPlanningRuntimeReleaseKeepsBindingOnOwnershipMismatch(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{
		hostBound:      true,
		containerBound: true,
		sourceCommit:   runtimeCommit,
	})
	f.remote.trackContainer(seeded.container, seeded.session.ID.String())
	volumeName := f.volumeName(seeded.session)
	f.remote.trackVolume(volumeName, seeded.session.ID, f.host.ID, runtimeCommit)
	f.remote.setVolumeLabels(volumeName, map[string]string{
		docker.OwnershipLabelKey:       "true",
		docker.HostLabelKey:            f.host.ID.String(),
		docker.PlanningSessionLabelKey: "另一个规划会话",
	})

	result, err := f.service.Release(f.ctx, app.ReleasePlanningRuntimeRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireIs(t, err, app.ErrPlanningRuntimeConflict, "归属不成立必须返回冲突")
	if !result.ContainerReleased {
		t.Fatal("规划容器已确认删除，应当如实报告")
	}
	if result.SnapshotReleased {
		t.Fatal("快照卷归属不成立时不得报告已释放")
	}
	assertNoOperation(t, f.log, runtimeCallRemoveVolume)
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.SourceCommit != runtimeCommit {
		t.Fatalf("归属不成立时不得清空绑定，实际 %+v", loaded)
	}
}

// TestPlanningRuntimeReleaseKeepsBindingOnContainerInspectionUncertain 校验核验失败时不动手。
func TestPlanningRuntimeReleaseKeepsBindingOnContainerInspectionUncertain(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{
		hostBound:      true,
		containerBound: true,
		sourceCommit:   runtimeCommit,
	})
	f.remote.inspectContainerErr = runtimeDockerError(docker.CodeStateUncertain, "docker inspect")
	f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeCommit)

	_, err := f.service.Release(f.ctx, app.ReleasePlanningRuntimeRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireIs(t, err, app.ErrPlanningRuntimeUncertain, "核验失败必须按不确定上报")
	assertNoOperation(t, f.log, runtimeCallDeleteManaged, runtimeCallRemoveVolume)
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.ContainerID != seeded.container.ID.String() {
		t.Fatalf("核验失败时不得清空绑定：%+v", loaded)
	}
}

// TestPlanningRuntimeReleaseKeepsLatestBindingOnConflict 校验清空绑定走条件更新。
func TestPlanningRuntimeReleaseKeepsLatestBindingOnConflict(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{
		hostBound:      true,
		containerBound: true,
		sourceCommit:   runtimeCommit,
	})
	f.remote.trackContainer(seeded.container, seeded.session.ID.String())
	volumeName := f.volumeName(seeded.session)
	f.remote.trackVolume(volumeName, seeded.session.ID, f.host.ID, runtimeCommit)
	var updateErr error
	f.remote.setHook(func(operation string) {
		if operation != runtimeCallRemoveVolume {
			return
		}
		// 模拟另一个入口在删除期间写入新的运行时绑定。
		updateErr = f.db.UpdatePlanningSessionRuntimeBinding(
			f.ctx,
			seeded.session.ID,
			storage.PlanningSessionRuntimeBinding{
				HostID:       &f.host.ID,
				ContainerID:  seeded.container.ID.String(),
				SourceCommit: runtimeCommit,
			},
			storage.PlanningSessionRuntimeBinding{HostID: &f.host.ID, SourceCommit: runtimeOtherCommit},
			time.Now().UTC(),
		)
	})

	_, err := f.service.Release(f.ctx, app.ReleasePlanningRuntimeRequest{
		PlanningSessionID: seeded.session.ID.String(),
	})
	requireNoError(t, updateErr, "并发写入绑定应当成功")
	requireIs(t, err, app.ErrPlanningRuntimeConflict, "绑定被改写时必须返回冲突")
	loaded := f.loadSession(t, seeded.session.ID)
	if loaded.SourceCommit != runtimeOtherCommit {
		t.Fatalf("条件更新冲突时必须保留最新数据库值，实际 %s", loaded.SourceCommit)
	}
}

// TestPlanningRuntimeOperationsSerializeOnSameSession 校验同一会话的入口严格串行。
func TestPlanningRuntimeOperationsSerializeOnSameSession(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{hostBound: true})
	name := f.prepContainerName(seeded.session)
	f.remote.trackPrepContainer(name, seeded.session.ID, f.host.ID, domain.DockerStateExited, false)
	entered, release := f.remote.blockOn(runtimeCallInspectPrep)

	inspectDone := make(chan error, 1)
	go func() {
		_, err := f.service.InspectPlanningRuntime(f.ctx, app.InspectPlanningRuntimeRequest{
			PlanningSessionID: seeded.session.ID.String(),
		})
		inspectDone <- err
	}()
	awaitRuntimeSignal(t, entered, "核验应当进入远程核验阶段")

	cleanupDone := make(chan error, 1)
	go func() {
		_, err := f.service.CleanupPlanningSnapshotPreparation(
			f.ctx,
			app.CleanupPlanningSnapshotPreparationRequest{PlanningSessionID: seeded.session.ID.String()},
		)
		cleanupDone <- err
	}()
	// 核验持有会话锁期间，清理不得进入任何远程动作。
	assertRuntimeNotEntered(t, entered, "同一会话的清理必须等待核验释放会话锁")

	release()
	requireNoError(t, waitRuntimeDone(t, inspectDone), "核验应当成功")
	requireNoError(t, waitRuntimeDone(t, cleanupDone), "清理应当成功")
	if f.remote.hasPrepHolder(name) {
		t.Fatalf("清理应当在核验之后删除准备容器，实际动作序列 %v", f.log.snapshot())
	}
}

// TestPlanningRuntimeOperationsRunInParallelAcrossSessions 校验不同会话可以并行核验。
func TestPlanningRuntimeOperationsRunInParallelAcrossSessions(t *testing.T) {
	f := newRuntimeFixture(t)
	first := f.seedSession(t, runtimeSeed{hostBound: true})
	second := f.seedSession(t, runtimeSeed{hostBound: true})
	entered, release := f.remote.blockOn(runtimeCallInspectPrep)

	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, err := f.service.InspectPlanningRuntime(f.ctx, app.InspectPlanningRuntimeRequest{
			PlanningSessionID: first.session.ID.String(),
		})
		firstDone <- err
	}()
	go func() {
		_, err := f.service.InspectPlanningRuntime(f.ctx, app.InspectPlanningRuntimeRequest{
			PlanningSessionID: second.session.ID.String(),
		})
		secondDone <- err
	}()
	awaitRuntimeSignal(t, entered, "第一个会话的核验应当进入远程核验阶段")
	awaitRuntimeSignal(t, entered, "不同会话必须可以并行进入远程核验阶段")
	release()
	requireNoError(t, waitRuntimeDone(t, firstDone), "第一个会话核验应当成功")
	requireNoError(t, waitRuntimeDone(t, secondDone), "第二个会话核验应当成功")
}

// TestPlanningRuntimeQueuedOperationsSkipRemoteOnCancellation 校验排队期间取消不执行远程动作。
func TestPlanningRuntimeQueuedOperationsSkipRemoteOnCancellation(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{
		hostBound:      true,
		containerBound: true,
		sourceCommit:   runtimeCommit,
	})
	f.remote.trackContainer(seeded.container, seeded.session.ID.String())
	f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeCommit)
	sessionID := seeded.session.ID.String()

	operations := map[string]func(ctx context.Context) error{
		"核验": func(ctx context.Context) error {
			_, err := f.service.InspectPlanningRuntime(ctx, app.InspectPlanningRuntimeRequest{
				PlanningSessionID: sessionID,
			})
			return err
		},
		"清理": func(ctx context.Context) error {
			_, err := f.service.CleanupPlanningSnapshotPreparation(
				ctx,
				app.CleanupPlanningSnapshotPreparationRequest{PlanningSessionID: sessionID},
			)
			return err
		},
		"恢复": func(ctx context.Context) error {
			_, err := f.service.RecoverPlanningSnapshot(ctx, app.RecoverPlanningSnapshotRequest{
				PlanningSessionID: sessionID,
			})
			return err
		},
		"废弃": func(ctx context.Context) error {
			_, err := f.service.DiscardIncompletePlanningSnapshot(
				ctx,
				app.DiscardIncompletePlanningSnapshotRequest{PlanningSessionID: sessionID},
			)
			return err
		},
		"释放": func(ctx context.Context) error {
			_, err := f.service.Release(ctx, app.ReleasePlanningRuntimeRequest{PlanningSessionID: sessionID})
			return err
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			// 测试自己持有会话锁，模拟另一个入口正在执行。
			unlock := f.coordination.Lock(seeded.session.ID)
			queuedCtx, cancel := context.WithCancel(f.ctx)
			done := make(chan error, 1)
			go func() {
				done <- operation(queuedCtx)
			}()
			cancel()
			before := len(f.log.snapshot())
			unlock()
			err := waitRuntimeDone(t, done)
			requireIs(t, err, context.Canceled, "排队期间取消必须按取消返回")
			if len(f.log.snapshot()) != before {
				t.Fatalf("排队期间取消不得执行远程动作，实际动作序列 %v", f.log.snapshot())
			}
		})
	}
}

// TestPlanningRuntimeInspectSerializesWithRunRole 校验规划角色调用与现场核验共用同一把会话锁。
func TestPlanningRuntimeInspectSerializesWithRunRole(t *testing.T) {
	f := newRuntimeFixture(t)
	seeded := f.seedSession(t, runtimeSeed{
		hostBound:      true,
		containerBound: true,
		sourceCommit:   runtimeCommit,
		analyzing:      true,
	})
	f.remote.trackContainer(seeded.container, seeded.session.ID.String())
	f.remote.trackVolume(f.volumeName(seeded.session), seeded.session.ID, f.host.ID, runtimeCommit)
	agentPort := &runtimeAgentPort{
		log:     f.log,
		entered: make(chan struct{}, 4),
		detail: app.ContainerDetail{
			Container: seeded.container,
			Inspect: &docker.ContainerInspect{
				ID:      seeded.container.DockerID,
				Name:    seeded.container.Name,
				State:   domain.DockerStateRunning,
				Running: true,
				Labels: map[string]string{
					docker.OwnershipLabelKey:       "true",
					docker.HostLabelKey:            f.host.ID.String(),
					docker.ContainerLabelKey:       seeded.container.ID.String(),
					docker.PlanningSessionLabelKey: seeded.session.ID.String(),
				},
			},
		},
	}
	agent, err := app.NewPlanningAgentService(app.PlanningAgentServiceOptions{
		Store:        f.db,
		Roles:        f.roles,
		Containers:   agentPort,
		Runtime:      f.service,
		Coordination: f.coordination,
		SessionRoot:  f.sessionRoot,
	})
	if err != nil {
		t.Fatalf("构造规划 Agent 服务失败: %v", err)
	}
	if err := agent.Start(f.ctx); err != nil {
		t.Fatalf("启动规划 Agent 服务失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if stopErr := agent.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止规划 Agent 服务失败: %v", stopErr)
		}
	})
	entered, release := f.remote.blockOn(runtimeCallVerifyCommit)

	inspectDone := make(chan error, 1)
	go func() {
		_, inspectErr := f.service.InspectPlanningRuntime(f.ctx, app.InspectPlanningRuntimeRequest{
			PlanningSessionID: seeded.session.ID.String(),
		})
		inspectDone <- inspectErr
	}()
	awaitRuntimeSignal(t, entered, "核验应当进入快照 commit 核验阶段")

	roleDone := make(chan error, 1)
	go func() {
		_, roleErr := agent.RunRole(f.ctx, app.RunPlanningRoleRequest{
			PlanningSessionID: seeded.session.ID.String(),
			RoleType:          string(domain.RoleTypeRequirementAnalyst),
			Prompt:            "分析这次需求",
		})
		roleDone <- roleErr
	}()
	select {
	case <-agentPort.entered:
		t.Fatal("核验持有会话锁期间，规划角色调用不得进入容器执行")
	case <-time.After(runtimeNotEnteredProbeTimeout):
	}
	release()
	requireNoError(t, waitRuntimeDone(t, inspectDone), "核验应当成功")
	// 角色调用随后进入容器执行：替身返回固定错误，用于证明调用确实发生过。
	roleErr := waitRuntimeDone(t, roleDone)
	if roleErr == nil {
		t.Fatal("规划角色调用应当把替身的执行失败如实返回")
	}
	if f.log.count(runtimeCallRunRole) != 1 {
		t.Fatalf("规划角色调用应当执行一次，实际动作序列 %v", f.log.snapshot())
	}
}

// runtimeAgentPort 是规划角色调用的受控替身。
type runtimeAgentPort struct {
	log     *runtimeEventLog
	entered chan struct{}
	detail  app.ContainerDetail
}

// InspectContainer 返回固定的规划容器现场。
func (p *runtimeAgentPort) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	p.log.add(runtimeCallInspectAgent)
	return p.detail, nil
}

// RunAgent 记录进入容器执行的时刻，并返回受控失败。
func (p *runtimeAgentPort) RunAgent(
	ctx context.Context,
	request app.ContainerAgentRequest,
) (app.ContainerAgentResult, error) {
	p.log.add(runtimeCallRunRole)
	select {
	case p.entered <- struct{}{}:
	default:
	}
	return app.ContainerAgentResult{}, errors.New("受控替身注入的规划角色执行失败")
}

// waitRuntimeDone 等待后台调用结束并返回其错误。
func waitRuntimeDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("等待后台调用结束超时")
		return nil
	}
}
