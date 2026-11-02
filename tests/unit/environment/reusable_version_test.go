package environment_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// reusableVersion 构造一个指定构建状态的环境版本，用于复用判定。
func reusableVersion(
	t *testing.T,
	number int,
	status domain.BuildStatus,
	dockerfileHash string,
	contextHash string,
	parent string,
) domain.EnvironmentVersion {
	t.Helper()
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	version := domain.EnvironmentVersion{
		HostID:            "host-1",
		Ref:               ref,
		Number:            number,
		DockerfileSHA256:  dockerfileHash,
		BuildConfigSHA256: hashOf("config-" + domain.FormatVersionLabel(number)),
		ContextSHA256:     contextHash,
		ParentVersion:     parent,
		ImageRef:          "agentflow-language-go:" + domain.FormatVersionLabel(number) + "-aaaaaaaaaaaa",
		Status:            status,
		CreatedAt:         time.Now().UTC().Truncate(time.Millisecond),
	}
	if status == domain.BuildStatusSucceeded {
		version.ImageID = "sha256:" + strings.Repeat("a", 64)
		builtAt := time.Now().UTC().Truncate(time.Millisecond)
		version.BuiltAt = &builtAt
	}
	if err := version.Validate(); err != nil {
		t.Fatalf("构造的版本应当合法: %v", err)
	}
	return version
}

// TestFindReusableVersionRequiresIdenticalContent 验证版本复用条件。
//
// 重复提交相同内容时必须复用既有成功版本，不产生重复版本号；只要 Dockerfile、构建
// 上下文、父基础版本或构建状态有一项不同，就必须创建新版本。
func TestFindReusableVersionRequiresIdenticalContent(t *testing.T) {
	t.Parallel()
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	parent := domain.FormatVersionLabel(1)
	versions := []domain.EnvironmentVersion{
		reusableVersion(t, 1, domain.BuildStatusSucceeded, hashA, hashA, parent),
		reusableVersion(t, 2, domain.BuildStatusFailed, hashB, hashB, parent),
		reusableVersion(t, 3, domain.BuildStatusSucceeded, hashB, hashA, parent),
	}
	cases := []struct {
		name           string
		dockerfileHash string
		contextHash    string
		parent         string
		wantReuse      bool
		wantNumber     int
	}{
		{
			name:           "内容与父版本完全一致",
			dockerfileHash: hashA,
			contextHash:    hashA,
			parent:         parent,
			wantReuse:      true,
			wantNumber:     1,
		},
		{
			name:           "Dockerfile 不同但上下文一致",
			dockerfileHash: hashB,
			contextHash:    hashA,
			parent:         parent,
			wantReuse:      true,
			wantNumber:     3,
		},
		{
			name:           "构建上下文不同",
			dockerfileHash: hashA,
			contextHash:    hashB,
			parent:         parent,
			wantReuse:      false,
		},
		{
			name:           "父基础版本不同",
			dockerfileHash: hashA,
			contextHash:    hashA,
			parent:         domain.FormatVersionLabel(2),
			wantReuse:      false,
		},
		{
			name:           "内容一致但历史构建失败",
			dockerfileHash: hashB,
			contextHash:    hashB,
			parent:         parent,
			wantReuse:      false,
		},
	}
	for _, testCase := range cases {
		reused, ok := domain.FindReusableVersion(
			versions,
			testCase.dockerfileHash,
			testCase.contextHash,
			testCase.parent,
		)
		if ok != testCase.wantReuse {
			t.Fatalf("%s: 期望复用=%v，实际 %v", testCase.name, testCase.wantReuse, ok)
		}
		if ok && reused.Number != testCase.wantNumber {
			t.Fatalf("%s: 期望复用版本 %d，实际 %d", testCase.name, testCase.wantNumber, reused.Number)
		}
	}
}

// TestFindReusableVersionIgnoresNonSucceededRecords 验证未构建成功的版本永不复用。
func TestFindReusableVersionIgnoresNonSucceededRecords(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("c", 64)
	parent := domain.FormatVersionLabel(1)
	versions := []domain.EnvironmentVersion{
		reusableVersion(t, 1, domain.BuildStatusPending, hash, hash, parent),
		reusableVersion(t, 2, domain.BuildStatusBuilding, hash, hash, parent),
		reusableVersion(t, 3, domain.BuildStatusUncertain, hash, hash, parent),
	}
	if _, ok := domain.FindReusableVersion(versions, hash, hash, parent); ok {
		t.Fatal("pending / building / uncertain 的版本都不得被复用")
	}
}

// TestMarkUncertainKeepsVersionUnusable 验证"结果不确定"状态的语义。
//
// uncertain 与 failed 不同：它表示无法判断镜像是否已生成，因此只清除可用标记，
// 不写构建时间，并保留可诊断原因。
func TestMarkUncertainKeepsVersionUnusable(t *testing.T) {
	t.Parallel()
	version := reusableVersion(
		t,
		1,
		domain.BuildStatusBuilding,
		strings.Repeat("d", 64),
		strings.Repeat("d", 64),
		domain.FormatVersionLabel(1),
	)
	if err := version.MarkUncertain("构建被中断，结果无法确认"); err != nil {
		t.Fatalf("标记结果不确定失败: %v", err)
	}
	if version.Status != domain.BuildStatusUncertain {
		t.Fatalf("状态应为 uncertain，实际 %s", version.Status)
	}
	if version.Usable() {
		t.Fatal("uncertain 版本不得可用")
	}
	if version.BuiltAt != nil || version.ImageID != "" {
		t.Fatalf("uncertain 版本不得保留镜像信息: %+v", version)
	}
	if version.ErrorSummary == "" {
		t.Fatal("uncertain 版本必须保留可诊断原因")
	}
	// 已成功构建的版本不允许被改写成不确定：迟到结果不能覆盖已确认的终态。
	built := reusableVersion(
		t,
		2,
		domain.BuildStatusSucceeded,
		strings.Repeat("e", 64),
		strings.Repeat("e", 64),
		domain.FormatVersionLabel(1),
	)
	if err := built.MarkUncertain("迟到结果"); err == nil {
		t.Fatal("已成功构建的版本不得被标记为结果不确定")
	}
}
