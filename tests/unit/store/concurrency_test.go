package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestParallelDatabasesDoNotShareData 验证并发测试各自的数据库互不干扰。
func TestParallelDatabasesDoNotShareData(t *testing.T) {
	t.Parallel()
	const total = 4
	var group sync.WaitGroup
	failures := make(chan string, total)
	for index := 0; index < total; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			db := newTestDB(t)
			ctx := testContext(t)
			requirement := mustRequirement(t, db)
			if _, err := db.GetRequirement(ctx, requirement.ID); err != nil {
				failures <- "本库应当能读到自己的需求: " + err.Error()
				return
			}
			// 其他并发用例的数据库是不同文件，本库只应包含自己写入的数据。
			list, err := db.ListRequirements(ctx, storage.RequirementFilter{})
			if err != nil {
				failures <- "查询需求列表失败: " + err.Error()
				return
			}
			if len(list) != 1 {
				failures <- "并发数据库出现数据串扰"
			}
		}()
	}
	group.Wait()
	close(failures)
	for message := range failures {
		t.Fatal(message)
	}
}

// TestParallelDatabasesUseDistinctFiles 验证并发用例的数据库文件路径互不相同。
func TestParallelDatabasesUseDistinctFiles(t *testing.T) {
	t.Parallel()
	const total = 6
	var group sync.WaitGroup
	paths := make(chan string, total)
	for index := 0; index < total; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			db := newTestDB(t)
			paths <- db.Path()
		}()
	}
	group.Wait()
	close(paths)
	seen := make(map[string]struct{}, total)
	for path := range paths {
		if !filepath.IsAbs(path) {
			t.Fatalf("数据库路径应当是绝对路径: %s", path)
		}
		if _, duplicated := seen[path]; duplicated {
			t.Fatalf("并发用例复用了数据库文件: %s", path)
		}
		seen[path] = struct{}{}
	}
	if len(seen) != total {
		t.Fatalf("数据库文件数量不符: %d", len(seen))
	}
}

// TestConcurrentWritesToSharedDatabaseStayConsistent 验证同一数据库内的并发写入结果一致。
//
// 该用例有意让多个 goroutine 共享同一个数据库，用于验证并发访问正确性。
func TestConcurrentWritesToSharedDatabaseStayConsistent(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	const total = 8
	var group sync.WaitGroup
	failures := make(chan string, total)
	for index := 0; index < total; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			role, err := domain.NewRole(
				"并发角色",
				domain.RoleTypePlanner,
				"provider/model",
				"提示词",
				domain.DefaultToolsPolicy(domain.RoleTypePlanner),
				time.Now().UTC(),
			)
			if err != nil {
				failures <- "构造角色失败: " + err.Error()
				return
			}
			if err := db.CreateRole(ctx, role); err != nil {
				failures <- "并发写入角色失败: " + err.Error()
			}
		}()
	}
	group.Wait()
	close(failures)
	for message := range failures {
		t.Fatal(message)
	}
	roles, err := db.ListRoles(ctx)
	if err != nil {
		t.Fatalf("查询角色失败: %v", err)
	}
	if len(roles) != total {
		t.Fatalf("并发写入角色数量不符: %d", len(roles))
	}
}

// TestConnectionIsReusableAfterTimeout 验证超时后连接仍可继续使用。
func TestConnectionIsReusableAfterTimeout(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	requirement := mustRequirement(t, db)
	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-expired.Done()
	if _, err := db.GetRequirement(expired, requirement.ID); err == nil {
		t.Fatal("已过期的上下文应当导致查询失败")
	}
	if _, err := db.GetRequirement(testContext(t), requirement.ID); err != nil {
		t.Fatalf("超时后连接应当仍可用: %v", err)
	}
}

// TestClosedDatabaseRejectsFurtherUse 验证关闭数据库后所有操作都返回错误。
func TestClosedDatabaseRejectsFurtherUse(t *testing.T) {
	t.Parallel()
	path := filepath.Join(testkit.Dir(t), "closed.db")
	db, err := sqlite.Open(testContext(t), sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
	ctx := testContext(t)
	if err := db.CreateRole(ctx, mustRoleEntity(t)); err == nil {
		t.Fatal("关闭后写入应当失败")
	}
	if _, err := db.ListRoles(ctx); err == nil {
		t.Fatal("关闭后查询应当失败")
	}
	if err := db.WithTx(ctx, func(context.Context, storage.Tx) error { return nil }); err == nil {
		t.Fatal("关闭后开启事务应当失败")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("重复关闭应当返回 nil: %v", err)
	}
	if _, err := db.GetRole(ctx, domain.NewID()); !errors.Is(err, storage.ErrNotFound) && err == nil {
		t.Fatal("关闭后读取应当返回错误")
	}
}

// mustRoleEntity 构造一个用于失败路径的角色实体。
func mustRoleEntity(t *testing.T) domain.Role {
	t.Helper()
	role, err := domain.NewRole(
		"角色",
		domain.RoleTypeDeveloper,
		"provider/model",
		"提示词",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	return role
}
