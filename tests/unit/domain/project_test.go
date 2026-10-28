package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// languageRef 返回一个合法的语言环境引用。
func languageRef(t *testing.T) domain.EnvironmentRef {
	t.Helper()
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境失败: %v", err)
	}
	return ref
}

// projectInput 返回一份可用的项目输入。
func projectInput(t *testing.T) domain.ProjectInput {
	t.Helper()
	return domain.ProjectInput{
		ID:            "demo-service",
		Name:          "示例服务",
		RepositoryURL: "ssh://git.example.com/team/repo",
		BaseBranch:    "main",
		Environment:   languageRef(t),
	}
}

// TestNewProjectNormalizesInput 校验构造项目时归一化标识、分支与工作目录。
func TestNewProjectNormalizesInput(t *testing.T) {
	t.Parallel()
	input := projectInput(t)
	input.ID = "  Demo-Service  "
	input.BaseBranch = "origin/main"
	input.Caches = []string{"go-mod", "go-mod", " "}
	project, err := domain.NewProject(input, time.Now())
	if err != nil {
		t.Fatalf("构造项目失败: %v", err)
	}
	if project.ID != "demo-service" {
		t.Fatalf("项目标识应归一化为小写: %s", project.ID)
	}
	if project.BaseBranch != "main" {
		t.Fatalf("基础分支应去掉 origin/ 前缀: %s", project.BaseBranch)
	}
	if project.NormalizedBaseBranch() != "main" {
		t.Fatalf("规范化后的基础分支不符: %s", project.NormalizedBaseBranch())
	}
	if len(project.Caches) != 1 || project.Caches[0] != "go-mod" {
		t.Fatalf("缓存类型应去空白并去重: %+v", project.Caches)
	}
	if project.EffectiveWorkDir() != domain.DefaultContainerWorkDir {
		t.Fatalf("未指定工作目录时应使用默认目录: %s", project.EffectiveWorkDir())
	}
	if project.Deleted() {
		t.Fatal("新建项目不应处于删除状态")
	}
}

// TestNewProjectRejectsInvalidInput 校验项目校验拒绝非法取值。
func TestNewProjectRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*domain.ProjectInput){
		"标识含大写与空格": func(p *domain.ProjectInput) { p.ID = "Demo Project" },
		"标识为空":     func(p *domain.ProjectInput) { p.ID = "  " },
		"名称为空":     func(p *domain.ProjectInput) { p.Name = "  " },
		"仓库地址为空":   func(p *domain.ProjectInput) { p.RepositoryURL = " " },
		"仓库地址含凭据":  func(p *domain.ProjectInput) { p.RepositoryURL = "https://user:token@git.example.com/r.git" },
		"仓库协议不支持":  func(p *domain.ProjectInput) { p.RepositoryURL = "ftp://git.example.com/r.git" },
		"基础分支为空":   func(p *domain.ProjectInput) { p.BaseBranch = " " },
		"基础分支非法":   func(p *domain.ProjectInput) { p.BaseBranch = "bad..branch" },
		"工作目录越界":   func(p *domain.ProjectInput) { p.WorkDir = "/etc/project" },
		"工作目录为根":   func(p *domain.ProjectInput) { p.WorkDir = domain.WorkspaceContainerRoot },
		"版本标签非法":   func(p *domain.ProjectInput) { p.EnvironmentVersion = "1" },
		"缓存类型书写非法": func(p *domain.ProjectInput) { p.Caches = []string{"Go Mod"} },
	}
	for name, mutate := range cases {
		input := projectInput(t)
		mutate(&input)
		if _, err := domain.NewProject(input, time.Now()); err == nil {
			t.Fatalf("%s: 应当被拒绝", name)
		} else if !errors.Is(err, domain.ErrInvalidValue) {
			t.Fatalf("%s: 错误类别应为校验错误，实际: %v", name, err)
		}
	}
}

// TestNewProjectRequiresLanguageEnvironment 校验项目必须使用语言环境。
func TestNewProjectRequiresLanguageEnvironment(t *testing.T) {
	t.Parallel()
	input := projectInput(t)
	base, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, domain.BaseEnvironmentName)
	if err != nil {
		t.Fatalf("构造基础环境失败: %v", err)
	}
	input.Environment = base
	if _, err := domain.NewProject(input, time.Now()); err == nil {
		t.Fatal("基础环境不能作为项目环境")
	}
}

// TestProjectAllowsEmptyEnvironmentVersion 校验环境版本可空。
//
// 空值表示使用当前可用版本，与任务配置一致；给出时必须满足版本标签格式。
func TestProjectAllowsEmptyEnvironmentVersion(t *testing.T) {
	t.Parallel()
	input := projectInput(t)
	input.EnvironmentVersion = ""
	project, err := domain.NewProject(input, time.Now())
	if err != nil {
		t.Fatalf("空环境版本应当合法: %v", err)
	}
	if project.EnvironmentVersion != "" {
		t.Fatalf("空环境版本不应被改写: %q", project.EnvironmentVersion)
	}

	input.EnvironmentVersion = "v0003"
	if _, err := domain.NewProject(input, time.Now()); err != nil {
		t.Fatalf("合法版本标签应当被接受: %v", err)
	}
}

// TestProjectMarkDeletedIsOneWay 校验逻辑删除只写一次时间。
func TestProjectMarkDeletedIsOneWay(t *testing.T) {
	t.Parallel()
	project, err := domain.NewProject(projectInput(t), time.Now())
	if err != nil {
		t.Fatalf("构造项目失败: %v", err)
	}
	first := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	if err := project.MarkDeleted(first); err != nil {
		t.Fatalf("标记删除失败: %v", err)
	}
	if !project.Deleted() {
		t.Fatal("标记删除后 Deleted 应为 true")
	}
	if project.DeletedAt == nil || !project.DeletedAt.Equal(first) {
		t.Fatalf("删除时间不符: %v", project.DeletedAt)
	}
	second := first.Add(time.Hour)
	if err := project.MarkDeleted(second); err == nil {
		t.Fatal("重复删除应当返回错误")
	}
	if project.DeletedAt == nil || !project.DeletedAt.Equal(first) {
		t.Fatalf("重复删除不得改写首次删除时间: %v", project.DeletedAt)
	}
}

// TestRequireProjectID 校验任务必须关联项目的规则。
func TestRequireProjectID(t *testing.T) {
	t.Parallel()
	if err := domain.RequireProjectID("  "); err == nil {
		t.Fatal("空项目标识应当被拒绝")
	} else if !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatalf("错误类别应为校验错误，实际: %v", err)
	}
	if err := domain.RequireProjectID("Demo Project"); err == nil {
		t.Fatal("非法项目标识应当被拒绝")
	}
	if err := domain.RequireProjectID("demo-service"); err != nil {
		t.Fatalf("合法项目标识不应被拒绝: %v", err)
	}
	// 书写形式校验本身允许空值：是否需要关联项目由上层按业务规则决定。
	if err := domain.ValidateProjectID(""); err != nil {
		t.Fatalf("书写形式校验不应拒绝空值: %v", err)
	}
}

// TestProjectIDPatternLimits 校验项目标识长度上限。
func TestProjectIDPatternLimits(t *testing.T) {
	t.Parallel()
	if err := domain.ValidateProjectID(strings.Repeat("a", 32)); err != nil {
		t.Fatalf("32 字符标识应当合法: %v", err)
	}
	if err := domain.ValidateProjectID(strings.Repeat("a", 33)); err == nil {
		t.Fatal("超过 32 字符的标识应当被拒绝")
	}
}
