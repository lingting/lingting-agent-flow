package sqlite_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// hashOf 返回测试内容的 SHA-256 摘要。
func hashOf(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

// mustEnvironmentVersion 构造并写入一个环境版本。
func mustEnvironmentVersion(
	t *testing.T,
	db *sqlite.DB,
	hostID domain.ID,
	ref domain.EnvironmentRef,
	number int,
) domain.EnvironmentVersion {
	t.Helper()
	version := domain.EnvironmentVersion{
		HostID:            hostID,
		Ref:               ref,
		Number:            number,
		DockerfileSHA256:  hashOf("dockerfile-" + domain.FormatVersionLabel(number)),
		BuildConfigSHA256: hashOf("config-" + domain.FormatVersionLabel(number)),
		ContextSHA256:     hashOf("context"),
		Status:            domain.BuildStatusPending,
		ImageRef:          "agentflow-" + ref.Name + ":" + domain.FormatVersionLabel(number) + "-aaaaaaaaaaaa",
		CreatedAt:         time.Now().UTC().Truncate(time.Millisecond),
	}
	if !ref.IsBase() {
		version.ParentVersion = domain.FormatVersionLabel(1)
	}
	if err := db.CreateEnvironmentVersion(testContext(t), version); err != nil {
		t.Fatalf("写入环境版本失败: %v", err)
	}
	return version
}

// mustBaseRef 返回基础环境引用。
func mustBaseRef(t *testing.T) domain.EnvironmentRef {
	t.Helper()
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	return ref
}

func TestEnvironmentVersionIsImmutable(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	host := mustExecutionHost(t, db, "节点", "10.0.0.20", false)
	ref := mustBaseRef(t)
	created := mustEnvironmentVersion(t, db, host.ID, ref, 1)

	// 重复写入同一版本号必须失败，历史版本不会被覆盖。
	duplicate := created
	duplicate.DockerfileSHA256 = hashOf("different")
	err := db.CreateEnvironmentVersion(ctx, duplicate)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("重复版本号应返回唯一约束冲突，实际: %v", err)
	}
	loaded, err := db.GetEnvironmentVersion(ctx, host.ID, ref, 1)
	if err != nil {
		t.Fatalf("读取环境版本失败: %v", err)
	}
	if loaded.DockerfileSHA256 != created.DockerfileSHA256 {
		t.Fatalf("历史版本内容不得被覆盖: %s", loaded.DockerfileSHA256)
	}

	// 更新只允许改变构建结果字段。
	updated := *loaded
	updated.Status = domain.BuildStatusSucceeded
	imageID := "sha256:" + strings.Repeat("a", 64)
	builtAt := time.Now().UTC().Truncate(time.Millisecond)
	updated.ImageID = imageID
	updated.BuiltAt = &builtAt
	if err := db.UpdateEnvironmentVersion(ctx, updated); err != nil {
		t.Fatalf("更新环境版本失败: %v", err)
	}
	reloaded, err := db.GetEnvironmentVersion(ctx, host.ID, ref, 1)
	if err != nil {
		t.Fatalf("读取环境版本失败: %v", err)
	}
	if reloaded.Status != domain.BuildStatusSucceeded || reloaded.ImageID != imageID {
		t.Fatalf("构建结果未保存: %+v", reloaded)
	}
	if reloaded.DockerfileSHA256 != created.DockerfileSHA256 {
		t.Fatalf("更新不得修改 Dockerfile 哈希: %s", reloaded.DockerfileSHA256)
	}
}

func TestNextEnvironmentVersionNumberIsMonotonic(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	host := mustExecutionHost(t, db, "节点", "10.0.0.21", false)
	ref := mustBaseRef(t)

	next, err := db.NextEnvironmentVersionNumber(ctx, host.ID, ref)
	if err != nil {
		t.Fatalf("读取下一个版本号失败: %v", err)
	}
	if next != 1 {
		t.Fatalf("首个版本号应为 1，实际 %d", next)
	}
	mustEnvironmentVersion(t, db, host.ID, ref, 1)
	next, err = db.NextEnvironmentVersionNumber(ctx, host.ID, ref)
	if err != nil {
		t.Fatalf("读取下一个版本号失败: %v", err)
	}
	if next != 2 {
		t.Fatalf("版本号应递增，实际 %d", next)
	}
}

func TestListEnvironmentVersionsFiltersByEnvironment(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	host := mustExecutionHost(t, db, "节点", "10.0.0.22", false)
	baseRef := mustBaseRef(t)
	languageRef, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	mustEnvironmentVersion(t, db, host.ID, baseRef, 1)
	mustEnvironmentVersion(t, db, host.ID, languageRef, 1)

	versions, err := db.ListEnvironmentVersions(ctx, storage.EnvironmentVersionFilter{
		HostID: host.ID,
		Ref:    &baseRef,
	})
	if err != nil {
		t.Fatalf("查询环境版本失败: %v", err)
	}
	if len(versions) != 1 || versions[0].Ref.Name != domain.BaseEnvironmentName {
		t.Fatalf("按环境过滤结果不符: %+v", versions)
	}
	all, err := db.ListEnvironmentVersions(ctx, storage.EnvironmentVersionFilter{HostID: host.ID})
	if err != nil {
		t.Fatalf("查询环境版本失败: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("应按版本号升序返回全部版本，实际 %d", len(all))
	}
}

func TestEnvironmentVersionRejectsInvalidHashes(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	host := mustExecutionHost(t, db, "节点", "10.0.0.23", false)
	ref := mustBaseRef(t)
	invalid := domain.EnvironmentVersion{
		HostID:            host.ID,
		Ref:               ref,
		Number:            1,
		DockerfileSHA256:  "short",
		BuildConfigSHA256: hashOf("config"),
		ContextSHA256:     hashOf("context"),
		Status:            domain.BuildStatusPending,
		ImageRef:          "agentflow-base:v0001-aaaaaaaaaaaa",
		CreatedAt:         time.Now(),
	}
	if err := db.CreateEnvironmentVersion(ctx, invalid); err == nil {
		t.Fatal("非法哈希应当被拒绝")
	}
}

// mustContainer 构造并写入一个容器记录。
func mustContainer(
	t *testing.T,
	db *sqlite.DB,
	hostID domain.ID,
	name string,
) domain.Container {
	t.Helper()
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	container, err := domain.NewContainer(
		domain.NewID(),
		hostID,
		name,
		ref,
		"v0001",
		hashOf("config"),
		"sha256:"+strings.Repeat("b", 64),
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	if err := db.CreateContainer(testContext(t), container); err != nil {
		t.Fatalf("写入容器记录失败: %v", err)
	}
	return container
}

func TestContainerNameIsUniquePerHost(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	host := mustExecutionHost(t, db, "节点", "10.0.0.30", false)
	other := mustExecutionHost(t, db, "节点二", "10.0.0.31", false)
	created := mustContainer(t, db, host.ID, "agentflow-abcdef123456")

	// 同一节点内重复名称必须失败。
	duplicate := created
	duplicate.ID = domain.NewID()
	if err := db.CreateContainer(ctx, duplicate); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("同一节点内重复容器名称应返回唯一约束冲突，实际: %v", err)
	}
	// 不同节点允许同名容器。
	second := duplicate
	second.HostID = other.ID
	if err := db.CreateContainer(ctx, second); err != nil {
		t.Fatalf("不同节点允许同名容器: %v", err)
	}
}

func TestContainerLifecyclePersistsDetachedState(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	host := mustExecutionHost(t, db, "节点", "10.0.0.32", false)
	container := mustContainer(t, db, host.ID, "agentflow-111111111111")

	if err := container.MarkCreated(strings.Repeat("c", 64), domain.DockerStateCreated, time.Now()); err != nil {
		t.Fatalf("标记创建完成失败: %v", err)
	}
	if err := container.MarkRunning(time.Now()); err != nil {
		t.Fatalf("标记运行中失败: %v", err)
	}
	if err := db.UpdateContainer(ctx, container); err != nil {
		t.Fatalf("更新容器记录失败: %v", err)
	}
	if err := container.Detach(time.Now()); err != nil {
		t.Fatalf("脱离管理失败: %v", err)
	}
	if err := db.UpdateContainer(ctx, container); err != nil {
		t.Fatalf("更新容器记录失败: %v", err)
	}
	reloaded, err := db.GetContainer(ctx, container.ID)
	if err != nil {
		t.Fatalf("读取容器记录失败: %v", err)
	}
	if !reloaded.Detached || reloaded.State != domain.ContainerStateDetached {
		t.Fatalf("脱离管理状态未持久化: %+v", reloaded)
	}
	if reloaded.DetachedAt == nil {
		t.Fatal("脱离管理时间未持久化")
	}
	// 默认列表不包含已脱离管理的容器。
	visible, err := db.ListContainers(ctx, domain.ContainerFilter{HostID: host.ID})
	if err != nil {
		t.Fatalf("查询容器记录失败: %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("默认列表不应包含已脱离管理的容器，实际 %d", len(visible))
	}
	all, err := db.ListContainers(ctx, domain.ContainerFilter{
		HostID:          host.ID,
		IncludeDetached: true,
	})
	if err != nil {
		t.Fatalf("查询容器记录失败: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("显式包含时应返回已脱离管理的容器，实际 %d", len(all))
	}
}

func TestGetContainerByName(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	host := mustExecutionHost(t, db, "节点", "10.0.0.33", false)
	container := mustContainer(t, db, host.ID, "agentflow-222222222222")

	found, err := db.GetContainerByName(ctx, host.ID, container.Name)
	if err != nil {
		t.Fatalf("按名称查询容器失败: %v", err)
	}
	if found.ID != container.ID {
		t.Fatalf("查询结果不符: %s", found.ID)
	}
	if _, err := db.GetContainerByName(ctx, host.ID, "agentflow-missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("不存在的名称应返回 ErrNotFound，实际: %v", err)
	}
}

func TestConcurrentEnvironmentVersionCreationDoesNotCollide(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	host := mustExecutionHost(t, db, "节点", "10.0.0.34", false)
	ref := mustBaseRef(t)

	// 并发写入同一版本号时只能有一个成功，另一个返回唯一约束冲突。
	version := domain.EnvironmentVersion{
		HostID:            host.ID,
		Ref:               ref,
		Number:            1,
		DockerfileSHA256:  hashOf("dockerfile"),
		BuildConfigSHA256: hashOf("config"),
		ContextSHA256:     hashOf("context"),
		Status:            domain.BuildStatusPending,
		ImageRef:          "agentflow-base:v0001-aaaaaaaaaaaa",
		CreatedAt:         time.Now().UTC().Truncate(time.Millisecond),
	}
	const workers = 4
	var group sync.WaitGroup
	results := make(chan error, workers)
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- db.CreateEnvironmentVersion(ctx, version)
		}()
	}
	group.Wait()
	close(results)
	succeeded := 0
	conflicted := 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, storage.ErrConflict):
			conflicted++
		default:
			t.Fatalf("并发写入出现意外错误: %v", err)
		}
	}
	if succeeded != 1 || conflicted != workers-1 {
		t.Fatalf("并发写入结果不符: 成功 %d，冲突 %d", succeeded, conflicted)
	}
}
