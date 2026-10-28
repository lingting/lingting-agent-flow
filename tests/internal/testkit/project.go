package testkit

import (
	"context"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 测试用项目的默认取值。
//
// 任务必须关联项目，因此凡是通过 app.TaskService 创建任务的用例都需要先写入一条
// 项目记录。这些常量集中在一处，避免各测试包各写一套"看起来像真的"的项目参数。
const (
	// DefaultTestProjectID 是测试项目的默认标识。
	DefaultTestProjectID = "demo-project"
	// DefaultTestProjectName 是测试项目的默认名称。
	DefaultTestProjectName = "示例项目"
	// DefaultTestRepositoryURL 是测试项目的默认仓库地址。
	DefaultTestRepositoryURL = "ssh://git.example.com/team/repo"
	// DefaultTestBaseBranch 是测试项目的默认基础分支。
	DefaultTestBaseBranch = "main"
	// DefaultTestEnvironment 是测试项目的默认语言环境。
	DefaultTestEnvironment = "go"
)

// CreateProject 在测试存储中写入一个可用于创建任务的项目。
//
// 返回值是写入后的领域对象，调用方可以直接把 ID 传给建任务请求。写入失败即测试
// 失败：任务必须关联项目，缺少项目时后续步骤没有意义。
func CreateProject(
	t *testing.T,
	store storage.Store,
	projectID string,
	at time.Time,
) domain.Project {
	t.Helper()
	if projectID == "" {
		projectID = DefaultTestProjectID
	}
	environment, err := domain.NewEnvironmentRef(
		domain.EnvironmentTypeLanguage,
		DefaultTestEnvironment,
	)
	if err != nil {
		t.Fatalf("构造测试环境引用失败: %v", err)
	}
	project, err := domain.NewProject(domain.ProjectInput{
		ID:            projectID,
		Name:          DefaultTestProjectName,
		RepositoryURL: DefaultTestRepositoryURL,
		BaseBranch:    DefaultTestBaseBranch,
		Environment:   environment,
	}, at)
	if err != nil {
		t.Fatalf("构造测试项目失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.CreateProject(ctx, project); err != nil {
		t.Fatalf("写入测试项目失败: %v", err)
	}
	return project
}
