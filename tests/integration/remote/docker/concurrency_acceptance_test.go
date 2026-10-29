// P3-M5 真实远程 Docker 多任务并发与资源回收验收。
//
// 本文件在真实远程 Linux + 真实 Docker Engine 上主动注入 P3-M5 §6 要求的现场：
//
//  1. 创建后无法确认状态的容器记录（unknown 现场）；
//  2. 数据库里有记录、远程容器不存在的现场（absent 现场）；
//  3. 远程存在但无法确认归属的孤儿容器；
//  4. 两个任务同时运行并分别回收资源；
//  5. 远程节点短暂不可达后重新核验；
//  6. 回收过程中发生错误后保留的现场。
//
// 安全约束（与仓库测试规范一致）：
//   - 只有 tests/config/docker.yml 的 docker.remote_enabled 为 true 且 ssh.enable 为 true
//     时才执行；否则输出 WARN [SKIP] 并跳过；
//   - 只操作带随机后缀、属于本次测试的容器与远程目录；
//   - 不执行全局 prune，不删除共享镜像或缓存卷；
//   - 主机密钥校验不可关闭，测试不写入用户真实 known_hosts；
//   - 不在远程现场不确定时启动可能重复产生副作用的任务。
//
// 运行方式：
//
//	cp tests/config/example.ssh.yml tests/config/ssh.yml
//	cp tests/config/example.docker.yml tests/config/docker.yml   # remote_enabled: true
//	go test -count=1 -v ./tests/integration/remote/docker/...
package docker_remote

import (
	"context"
	"errors"
	"path"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestRemoteDockerUnknownAndOrphanScenes 在真实远程环境注入 unknown 与孤儿容器现场。
//
// 断言的对象是"真实远程状态"与"真实数据库记录"两边的对应关系：
//   - 远程真实存在但数据库记录为 unknown 的容器，可被真实观测确认为运行中；
//   - 远程真实不存在但数据库仍有记录的容器，可被确认为不存在（收敛为已删除）；
//   - 归属标签不匹配的容器不被自动处置。
func TestRemoteDockerUnknownAndOrphanScenes(t *testing.T) {
	t.Parallel()
	tg := requireEnabled(t)
	ws := newWorkspace(t)
	client := dial(t, ws, tg)

	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	defer cancel()
	suffix := randomSuffix(t)
	testRoot := remoteTestRoot(t, ctx, client, suffix)

	runner, err := docker.NewRunner(client, docker.RunnerOptions{})
	if err != nil {
		t.Fatalf("构造 Docker 执行器失败: %v", err)
	}
	preflight := runner.Preflight(ctx, docker.PreflightOptions{
		WorkspaceRoot:   testRoot,
		CreateWorkspace: true,
	})
	if !preflight.Ready() {
		t.Fatalf("真实远程 Docker 预检未通过(%s): %s", preflight.ErrorCode, preflight.Detail)
	}
	imageRef := "agentflow-test-lifecycle:" + suffix
	imageID := buildTestImage(t, ctx, client, runner, testRoot, imageRef)

	// 现场 1：远程真实存在、数据库记录为 unknown。
	running := newRemoteTestContainer(t, imageRef, imageID, suffix+"-run")
	runningDockerID := createAndStartTestContainer(t, ctx, runner, running, imageRef)
	// 容器清理责任已在创建前登记，用例结束时会复核远程状态。
	inspect, err := runner.InspectContainer(ctx, runningDockerID)
	if err != nil {
		t.Fatalf("核验真实容器失败: %v", err)
	}
	if !inspect.Running {
		t.Fatalf("测试容器应在运行中: %+v", inspect)
	}
	if verifyErr := docker.VerifyOwnership(inspect, running); verifyErr != nil {
		t.Fatalf("测试容器所有权核验失败: %v", verifyErr)
	}
	// 用真实观测结果驱动领域收敛：unknown + 确认真实运行中 → running。
	decision := domain.DecideContainerRecovery(domain.ContainerRecoveryInput{
		Container: domain.Container{
			ID:       running.ID,
			HostID:   running.HostID,
			Name:     running.Name,
			DockerID: "",
			State:    domain.ContainerStateUnknown,
		},
		Observation:      domain.ContainerObservedRunning,
		ObservedDockerID: inspect.ID,
	})
	if !decision.Needed || decision.Next != domain.ContainerStateRunning {
		t.Fatalf("真实运行中的容器应收敛为 running，实际 %+v", decision)
	}
	if decision.DockerID != inspect.ID {
		t.Fatalf("收敛必须补写真实 Docker ID: %q", decision.DockerID)
	}

	// 现场 2：数据库有记录、远程容器不存在（absent）。
	absent := newRemoteTestContainer(t, imageRef, imageID, suffix+"-gone")
	absentDecision := domain.DecideContainerRecovery(domain.ContainerRecoveryInput{
		Container:   absent,
		Observation: domain.ContainerObservedAbsent,
	})
	if !absentDecision.Needed || absentDecision.Next != domain.ContainerStateRemoved {
		t.Fatalf("确认真实不存在的容器应收敛为已删除，实际 %+v", absentDecision)
	}
	// 真实核验：该容器确实不存在（用例从未创建它）。
	if _, inspectErr := runner.InspectContainer(ctx, absent.Name); docker.ErrorCodeOf(inspectErr) != docker.CodeContainerNotFound {
		t.Fatalf("未创建的容器必须被确认为不存在，实际 %v", inspectErr)
	}

	// 现场 3：远程存在但归属标签不匹配的容器不得被自动处置。
	impostor := newRemoteTestContainer(t, imageRef, imageID, suffix+"-impostor")
	impostorID := createAndStartTestContainer(t, ctx, runner, impostor, imageRef)
	// 容器清理责任已在创建前登记，用例结束时会复核远程状态。
	// 用"另一个容器登记记录"去核验真实容器：标签必然不匹配。
	mismatched := impostor
	mismatched.ID = domain.NewID()
	mismatched.Name = "agentflow-test-mismatched-" + suffix
	realInspect, err := runner.InspectContainer(ctx, impostorID)
	if err != nil {
		t.Fatalf("核验真实容器失败: %v", err)
	}
	if ownershipErr := docker.VerifyOwnership(realInspect, mismatched); ownershipErr == nil {
		t.Fatal("登记记录与真实标签不一致时所有权核验必须失败")
	} else if docker.ErrorCodeOf(ownershipErr) != docker.CodeOwnershipMismatch {
		t.Fatalf("应返回所有权不匹配错误，实际 %v", ownershipErr)
	}
	mismatchDecision := domain.DecideContainerRecovery(domain.ContainerRecoveryInput{
		Container:   impostor,
		Observation: domain.ContainerObservedOwnershipMismatch,
	})
	if mismatchDecision.Needed {
		t.Fatal("所有权不匹配的容器不得被自动处置")
	}
	if !mismatchDecision.NeedsManual {
		t.Fatal("所有权不匹配的容器必须要求人工核验")
	}
	// 该容器必须仍然真实存在且运行中（没有被自动删除）。
	stillThere, err := runner.InspectContainer(ctx, impostorID)
	if err != nil {
		t.Fatalf("所有权不匹配的容器不得被自动删除: %v", err)
	}
	if !stillThere.Running {
		t.Fatalf("所有权不匹配的容器应仍在运行: %+v", stillThere)
	}
	t.Logf("REMOTE_UNKNOWN_SCENE container=%s docker_id=%s", running.ID, runningDockerID)
	t.Logf("REMOTE_ABSENT_SCENE container=%s name=%s", absent.ID, absent.Name)
	t.Logf("REMOTE_ORPHAN_SCENE container=%s docker_id=%s", impostor.ID, impostorID)
}

// TestRemoteDockerTwoTasksConcurrentAndReclaim 验证两个任务同时运行并分别回收资源。
//
// 断言：两个任务各自持有独立的真实容器；回收任务 A 的容器不改变任务 B 的容器；
// 两个容器都真实创建、真实运行、真实删除。
func TestRemoteDockerTwoTasksConcurrentAndReclaim(t *testing.T) {
	t.Parallel()
	tg := requireEnabled(t)
	ws := newWorkspace(t)
	client := dial(t, ws, tg)

	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	defer cancel()
	suffix := randomSuffix(t)
	testRoot := remoteTestRoot(t, ctx, client, suffix)

	runner, err := docker.NewRunner(client, docker.RunnerOptions{})
	if err != nil {
		t.Fatalf("构造 Docker 执行器失败: %v", err)
	}
	preflight := runner.Preflight(ctx, docker.PreflightOptions{
		WorkspaceRoot:   testRoot,
		CreateWorkspace: true,
	})
	if !preflight.Ready() {
		t.Fatalf("真实远程 Docker 预检未通过(%s): %s", preflight.ErrorCode, preflight.Detail)
	}
	imageRef := "agentflow-test-lifecycle:" + suffix
	imageID := buildTestImage(t, ctx, client, runner, testRoot, imageRef)

	// 两个任务各自创建一个真实容器。
	taskA := newRemoteTestContainer(t, imageRef, imageID, suffix+"-a")
	taskB := newRemoteTestContainer(t, imageRef, imageID, suffix+"-b")
	dockerA := createAndStartTestContainer(t, ctx, runner, taskA, imageRef)
	dockerB := createAndStartTestContainer(t, ctx, runner, taskB, imageRef)
	// 两个容器的清理责任已在创建前登记，用例结束时会逐个复核远程状态。
	if dockerA == dockerB {
		t.Fatal("两个任务必须使用不同的 Docker 容器")
	}
	// 两个容器都在运行，且各自归属成立。
	for _, item := range []struct {
		container domain.Container
		dockerID  string
	}{
		{container: taskA, dockerID: dockerA},
		{container: taskB, dockerID: dockerB},
	} {
		inspect, inspectErr := runner.InspectContainer(ctx, item.dockerID)
		if inspectErr != nil {
			t.Fatalf("核验容器失败: %v", inspectErr)
		}
		if !inspect.Running {
			t.Fatalf("容器 %s 应在运行中", item.dockerID)
		}
		if ownershipErr := docker.VerifyOwnership(inspect, item.container); ownershipErr != nil {
			t.Fatalf("容器 %s 归属核验失败: %v", item.dockerID, ownershipErr)
		}
	}
	// 回收任务 A 的容器。
	removed, err := runner.RemoveContainer(ctx, dockerA)
	if err != nil {
		t.Fatalf("删除任务 A 的容器失败: %v", err)
	}
	if !removed.Removed {
		t.Fatalf("任务 A 的容器应被删除: %+v", removed)
	}
	// 任务 B 的容器必须原样存在且运行中。
	survivor, err := runner.InspectContainer(ctx, dockerB)
	if err != nil {
		t.Fatalf("回收任务 A 影响了任务 B 的容器: %v", err)
	}
	if !survivor.Running {
		t.Fatalf("任务 B 的容器必须仍在运行: %+v", survivor)
	}
	if ownershipErr := docker.VerifyOwnership(survivor, taskB); ownershipErr != nil {
		t.Fatalf("任务 B 的容器归属被改变: %v", ownershipErr)
	}
	// 任务 A 的容器必须确实已不存在。
	if _, inspectErr := runner.InspectContainer(ctx, dockerA); docker.ErrorCodeOf(inspectErr) != docker.CodeContainerNotFound {
		t.Fatalf("任务 A 的容器应已不存在，实际 %v", inspectErr)
	}
	// 再删除任务 B：只删除本次测试创建的资源。
	if _, err := runner.RemoveContainer(ctx, dockerB); err != nil {
		t.Fatalf("删除任务 B 的容器失败: %v", err)
	}
	t.Logf("REMOTE_TASK_A container=%s docker_id=%s removed=true", taskA.ID, dockerA)
	t.Logf("REMOTE_TASK_B container=%s docker_id=%s removed=true", taskB.ID, dockerB)
}

// TestRemoteDockerRecoveryKeepsSceneAfterTransientFailure 验证短暂不可达后重新核验与失败现场保留。
//
// 该用例用一个可注入失败的连接包装真实连接：在"不可达"窗口内，恢复服务必须保留
// unknown 记录并留下证据；连接恢复后重新核验必须收敛为真实状态。
//
// 包装只影响本用例进程内的错误注入，不修改远程主机、不关闭主机密钥校验。
func TestRemoteDockerRecoveryKeepsSceneAfterTransientFailure(t *testing.T) {
	t.Parallel()
	tg := requireEnabled(t)
	ws := newWorkspace(t)
	client := dial(t, ws, tg)

	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	defer cancel()
	suffix := randomSuffix(t)
	testRoot := remoteTestRoot(t, ctx, client, suffix)

	realRunner, err := docker.NewRunner(client, docker.RunnerOptions{})
	if err != nil {
		t.Fatalf("构造 Docker 执行器失败: %v", err)
	}
	preflight := realRunner.Preflight(ctx, docker.PreflightOptions{
		WorkspaceRoot:   testRoot,
		CreateWorkspace: true,
	})
	if !preflight.Ready() {
		t.Fatalf("真实远程 Docker 预检未通过(%s): %s", preflight.ErrorCode, preflight.Detail)
	}
	imageRef := "agentflow-test-lifecycle:" + suffix
	imageID := buildTestImage(t, ctx, client, realRunner, testRoot, imageRef)

	// 先写入执行节点：容器标签里必须带真实节点标识，否则归属核验必然失败，
	// 用例就退化成"验证一个构造错误的现场"。
	db, err := sqlite.Open(ctx, sqlite.Options{Path: ws.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	now := time.Now().UTC()
	host, err := domain.NewExecutionHost(
		"",
		"真实远程回收节点",
		tg.host,
		tg.port,
		tg.user,
		path.Join(testRoot, "agentflow"),
		now,
		domain.WithPrivateKey(tg.keyPath),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}

	container := newRemoteTestContainer(t, imageRef, imageID, suffix+"-flaky")
	container.HostID = host.ID
	dockerID := createAndStartTestContainer(t, ctx, realRunner, container, imageRef)
	// 容器清理责任已在创建前登记，用例结束时会复核远程状态。

	// 用真实数据库记录容器，并把记录推进为 unknown。
	record := container
	record.DockerID = dockerID
	if err := db.CreateContainer(ctx, record); err != nil {
		t.Fatalf("写入容器登记记录失败: %v", err)
	}
	unknown := record
	if err := unknown.MarkUnknown("真实远程观测暂时不可达", now); err != nil {
		t.Fatalf("标记 unknown 失败: %v", err)
	}
	if err := db.UpdateContainer(ctx, unknown); err != nil {
		t.Fatalf("写入 unknown 记录失败: %v", err)
	}

	// 阶段 1：注入"不可达"。恢复服务必须保留 unknown 并留下证据。
	failing := &unreachableContainerPort{}
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	service, err := app.NewContainerRecoveryService(app.ContainerRecoveryOptions{
		Store:      db,
		Containers: failing,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造容器残留收敛服务失败: %v", err)
	}
	if startErr := service.Start(ctx); startErr != nil {
		t.Fatalf("不可达时收敛不得让启动失败: %v", startErr)
	}
	stored, err := db.GetContainer(ctx, record.ID)
	if err != nil {
		t.Fatalf("读取容器记录失败: %v", err)
	}
	if stored.State != domain.ContainerStateUnknown {
		t.Fatalf("不可达时必须保持 unknown，实际 %s", stored.State)
	}
	// 真实容器必须仍在运行：不可达不得触发任何破坏性操作。
	inspect, err := realRunner.InspectContainer(ctx, dockerID)
	if err != nil {
		t.Fatalf("不可达窗口内真实容器不应被删除: %v", err)
	}
	if !inspect.Running {
		t.Fatalf("不可达窗口内真实容器应仍在运行: %+v", inspect)
	}

	// 阶段 2：连接恢复，重新核验必须收敛为真实状态。
	healthy, err := app.NewContainerRecoveryService(app.ContainerRecoveryOptions{
		Store:      db,
		Containers: &realContainerPort{store: db, runner: realRunner},
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造第二个收敛服务失败: %v", err)
	}
	if startErr := healthy.Start(ctx); startErr != nil {
		t.Fatalf("连接恢复后收敛失败: %v", startErr)
	}
	recovered, err := db.GetContainer(ctx, record.ID)
	if err != nil {
		t.Fatalf("读取容器记录失败: %v", err)
	}
	if recovered.State != domain.ContainerStateRunning {
		t.Fatalf("连接恢复后应收敛为运行中，实际 %s", recovered.State)
	}
	t.Logf("REMOTE_FLAKY_SCENE container=%s docker_id=%s final_state=%s",
		record.ID, dockerID, recovered.State)
}

// unreachableContainerPort 让每次容器观测都返回"连接不可用"。
//
// 它只影响本进程内的观测调用，不修改远程主机，也不关闭主机密钥校验。
type unreachableContainerPort struct{}

// InspectContainer 返回受控的"连接不可用"错误。
func (p *unreachableContainerPort) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	return app.ContainerDetail{}, &docker.Error{
		Code:   docker.CodeSSHUnavailable,
		Op:     "docker inspect",
		Detail: "注入的连接不可用",
		Cause:  errors.New("注入的连接不可用"),
	}
}

// realContainerPort 用真实 Docker 执行器观测容器。
//
// 它按容器登记记录 ID 读取数据库里的记录，再用真实 Docker 观测并核验归属，
// 因此"连接恢复后重新核验"走的是与生产代码一致的判定路径。
type realContainerPort struct {
	store  realContainerStore
	runner *docker.Runner
}

// realContainerStore 是本端口读取容器登记记录所需的最小能力。
type realContainerStore interface {
	GetContainer(ctx context.Context, id domain.ID) (*domain.Container, error)
}

// InspectContainer 用真实 Docker 观测容器并核验归属。
func (p *realContainerPort) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	container, err := p.store.GetContainer(ctx, domain.ID(request.ContainerID))
	if err != nil {
		return app.ContainerDetail{}, err
	}
	inspect, inspectErr := p.runner.InspectContainer(ctx, container.DockerID)
	if inspectErr != nil {
		return app.ContainerDetail{Container: *container}, inspectErr
	}
	if ownershipErr := docker.VerifyOwnership(inspect, *container); ownershipErr != nil {
		return app.ContainerDetail{Container: *container}, ownershipErr
	}
	return app.ContainerDetail{Container: *container, Inspect: &inspect}, nil
}
