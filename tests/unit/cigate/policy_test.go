package cigate_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/lingting/agentflow/internal/cigate"
)

// fakeChecker 是可控的仓库权限检查器替身。
type fakeChecker struct {
	// permissions 按账号给出是否有写入权限，缺失的账号按无权限处理。
	permissions map[string]bool
	// err 非空时让查询直接失败。
	err error
	// mutex 保护 calls，使并行用例共享替身时不会出现竞态。
	mutex sync.Mutex
	// calls 记录被查询过的账号。
	calls []string
}

// HasWritePermission 返回替身结果。
func (f *fakeChecker) HasWritePermission(_ context.Context, login string) (bool, error) {
	f.mutex.Lock()
	f.calls = append(f.calls, login)
	f.mutex.Unlock()
	if f.err != nil {
		return false, f.err
	}
	return f.permissions[login], nil
}

// calledLogins 返回被查询过的账号列表。
func (f *fakeChecker) calledLogins() []string {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return append([]string(nil), f.calls...)
}

// writeChecker 返回把 owner 之外的指定账号视为有写入权限的替身。
func writeChecker(writable ...string) *fakeChecker {
	permissions := make(map[string]bool, len(writable))
	for _, login := range writable {
		permissions[login] = true
	}
	return &fakeChecker{permissions: permissions}
}

// TestParseMode 校验授权模式解析只接受约定的小写取值。
func TestParseMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want cigate.Mode
	}{
		{name: "未设置", raw: "", want: cigate.ModeDisabled},
		{name: "只空白", raw: "   ", want: cigate.ModeDisabled},
		{name: "self", raw: "self", want: cigate.ModeSelf},
		{name: "collaborator", raw: "collaborator", want: cigate.ModeCollaborator},
		{name: "all", raw: "all", want: cigate.ModeAll},
		{name: "首尾空白", raw: " collaborator ", want: cigate.ModeCollaborator},
		{name: "大写 SELF", raw: "SELF", want: cigate.ModeDisabled},
		{name: "驼峰 Collaborator", raw: "Collaborator", want: cigate.ModeDisabled},
		{name: "大写 ALL", raw: "ALL", want: cigate.ModeDisabled},
		{name: "布尔 true", raw: "true", want: cigate.ModeDisabled},
		{name: "未知取值", raw: "owner", want: cigate.ModeDisabled},
		{name: "多余字符", raw: "self ", want: cigate.ModeSelf},
		{name: "内部空白", raw: "sel f", want: cigate.ModeDisabled},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := cigate.ParseMode(testCase.raw); got != testCase.want {
				t.Fatalf("ParseMode(%q) = %q，期望 %q", testCase.raw, got, testCase.want)
			}
		})
	}
}

// TestParseEvent 校验只识别明确支持的事件类型。
func TestParseEvent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		want cigate.Event
	}{
		{raw: "push", want: cigate.EventPush},
		{raw: "pull_request", want: cigate.EventPullRequest},
		{raw: " pull_request ", want: cigate.EventPullRequest},
		{raw: "workflow_call", want: cigate.EventUnsupported},
		{raw: "workflow_dispatch", want: cigate.EventUnsupported},
		{raw: "pull_request_target", want: cigate.EventUnsupported},
		{raw: "", want: cigate.EventUnsupported},
	}
	for _, testCase := range cases {
		if got := cigate.ParseEvent(testCase.raw); got != testCase.want {
			t.Fatalf("ParseEvent(%q) = %q，期望 %q", testCase.raw, got, testCase.want)
		}
	}
}

// TestDecidePush 覆盖 push 事件的身份判定矩阵。
func TestDecidePush(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		mode  string
		actor string
		want  bool
	}{
		{
			name: "空值拒绝任意 Push",
			mode: "", actor: "owner",
		},
		{
			name: "未知值拒绝任意 Push",
			mode: "OWNER", actor: "owner",
		},
		{
			name: "self 允许所有者 Push",
			mode: "self", actor: "owner", want: true,
		},
		{
			name: "self 拒绝其他用户 Push",
			mode: "self", actor: "someone",
		},
		{
			name: "self 大小写不同仍视为所有者",
			mode: "self", actor: "Owner", want: true,
		},
		{
			name: "collaborator 允许所有者 Push",
			mode: "collaborator", actor: "owner", want: true,
		},
		{
			name: "all 允许任意有效 Push",
			mode: "all", actor: "someone", want: true,
		},
		{
			name: "all 允许大小写任意的账号",
			mode: "all", actor: "Someone", want: true,
		},
		{
			name: "bot 账号在 self 下被拒绝",
			mode: "self", actor: "dependabot[bot]",
		},
		{
			name: "bot 账号在 all 下被允许",
			mode: "all", actor: "dependabot[bot]", want: true,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			checker := writeChecker("someone")
			decision := cigate.Decide(context.Background(), cigate.Input{
				Mode:            testCase.mode,
				Event:           "push",
				Actor:           testCase.actor,
				RepositoryOwner: "owner",
			}, checker)
			if decision.Enabled != testCase.want {
				t.Fatalf("结论错误: enabled=%v（原因 %s），期望 %v",
					decision.Enabled, decision.Reason, testCase.want)
			}
			if decision.Reason == "" {
				t.Fatal("判定原因不能为空")
			}
			for _, login := range checker.calledLogins() {
				if login == "owner" && testCase.mode != "collaborator" {
					t.Fatalf("所有者身份判定的模式不应查询仓库权限: %q", login)
				}
			}
		})
	}
}

// TestDecidePushCollaboratorUsesPermissionCheck 校验 collaborator 模式的 Push 判定。
func TestDecidePushCollaboratorUsesPermissionCheck(t *testing.T) {
	t.Parallel()
	t.Run("有写权限的协作者被允许", func(t *testing.T) {
		t.Parallel()
		checker := writeChecker("writer")
		decision := cigate.Decide(context.Background(), cigate.Input{
			Mode:            "collaborator",
			Event:           "push",
			Actor:           "writer",
			RepositoryOwner: "owner",
		}, checker)
		if !decision.Enabled {
			t.Fatalf("协作者 Push 应被允许: %s", decision.Reason)
		}
		if logins := checker.calledLogins(); len(logins) != 1 || logins[0] != "writer" {
			t.Fatalf("应只查询推送者权限，实际 %v", logins)
		}
	})
	t.Run("无写权限的用户被拒绝", func(t *testing.T) {
		t.Parallel()
		checker := writeChecker()
		decision := cigate.Decide(context.Background(), cigate.Input{
			Mode:            "collaborator",
			Event:           "push",
			Actor:           "reader",
			RepositoryOwner: "owner",
		}, checker)
		if decision.Enabled {
			t.Fatalf("只读用户 Push 不应被允许: %s", decision.Reason)
		}
	})
	t.Run("权限查询失败按拒绝处理", func(t *testing.T) {
		t.Parallel()
		checker := &fakeChecker{err: errors.New("接口不可用")}
		decision := cigate.Decide(context.Background(), cigate.Input{
			Mode:            "collaborator",
			Event:           "push",
			Actor:           "writer",
			RepositoryOwner: "owner",
		}, checker)
		if decision.Enabled {
			t.Fatalf("权限查询失败时不得放行: %s", decision.Reason)
		}
		if !strings.Contains(decision.Reason, "接口不可用") {
			t.Fatalf("拒绝原因应说明查询失败: %s", decision.Reason)
		}
	})
	t.Run("缺少权限检查器按拒绝处理", func(t *testing.T) {
		t.Parallel()
		decision := cigate.Decide(context.Background(), cigate.Input{
			Mode:            "collaborator",
			Event:           "push",
			Actor:           "writer",
			RepositoryOwner: "owner",
		}, nil)
		if decision.Enabled {
			t.Fatalf("无法确认权限时不得放行: %s", decision.Reason)
		}
	})
}

// TestDecidePullRequest 覆盖 PR 作者关系的判定矩阵。
func TestDecidePullRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		mode        string
		association string
		want        bool
	}{
		{name: "空值拒绝任意 PR", mode: "", association: "OWNER"},
		{name: "未知值拒绝任意 PR", mode: "everyone", association: "OWNER"},
		{name: "self 允许 OWNER", mode: "self", association: "OWNER", want: true},
		{name: "self 允许小写 owner 关系", mode: "self", association: "owner", want: true},
		{name: "self 拒绝 MEMBER", mode: "self", association: "MEMBER"},
		{name: "self 拒绝 COLLABORATOR", mode: "self", association: "COLLABORATOR"},
		{name: "self 拒绝 CONTRIBUTOR", mode: "self", association: "CONTRIBUTOR"},
		{name: "collaborator 允许 OWNER", mode: "collaborator", association: "OWNER", want: true},
		{name: "collaborator 允许 MEMBER", mode: "collaborator", association: "MEMBER", want: true},
		{
			name: "collaborator 允许 COLLABORATOR",
			mode: "collaborator", association: "COLLABORATOR", want: true,
		},
		{name: "collaborator 拒绝 CONTRIBUTOR", mode: "collaborator", association: "CONTRIBUTOR"},
		{
			name: "collaborator 拒绝 FIRST_TIME_CONTRIBUTOR",
			mode: "collaborator", association: "FIRST_TIME_CONTRIBUTOR",
		},
		{name: "collaborator 拒绝 FIRST_TIMER", mode: "collaborator", association: "FIRST_TIMER"},
		{name: "collaborator 拒绝 MANNEQUIN", mode: "collaborator", association: "MANNEQUIN"},
		{name: "collaborator 拒绝 NONE", mode: "collaborator", association: "NONE"},
		{
			name: "collaborator 拒绝无法识别的关系",
			mode: "collaborator", association: "SOMETHING_NEW",
		},
		{name: "all 允许 OWNER", mode: "all", association: "OWNER", want: true},
		{name: "all 允许 CONTRIBUTOR", mode: "all", association: "CONTRIBUTOR", want: true},
		{name: "all 允许 NONE", mode: "all", association: "NONE", want: true},
		{name: "all 允许无法识别的关系", mode: "all", association: "SOMETHING_NEW", want: true},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			checker := writeChecker("actor")
			decision := cigate.Decide(context.Background(), cigate.Input{
				Mode:              testCase.mode,
				Event:             "pull_request",
				Actor:             "actor",
				RepositoryOwner:   "owner",
				PullRequestAuthor: "author",
				AuthorAssociation: testCase.association,
			}, checker)
			if decision.Enabled != testCase.want {
				t.Fatalf("结论错误: enabled=%v（原因 %s），期望 %v",
					decision.Enabled, decision.Reason, testCase.want)
			}
			if logins := checker.calledLogins(); len(logins) != 0 {
				t.Fatalf("PR 判定不应查询仓库权限，实际查询 %v", logins)
			}
		})
	}
}

// TestDecidePullRequestIgnoresActor 校验 PR 判定只看作者关系，不看事件触发者。
func TestDecidePullRequestIgnoresActor(t *testing.T) {
	t.Parallel()
	t.Run("非所有者触发但作者是所有者", func(t *testing.T) {
		t.Parallel()
		decision := cigate.Decide(context.Background(), cigate.Input{
			Mode:              "self",
			Event:             "pull_request",
			Actor:             "outsider",
			RepositoryOwner:   "owner",
			PullRequestAuthor: "owner",
			AuthorAssociation: "OWNER",
		}, nil)
		if !decision.Enabled {
			t.Fatalf("PR 判定不应取决于触发者: %s", decision.Reason)
		}
	})
	t.Run("所有者触发但作者是历史贡献者", func(t *testing.T) {
		t.Parallel()
		decision := cigate.Decide(context.Background(), cigate.Input{
			Mode:              "self",
			Event:             "pull_request",
			Actor:             "owner",
			RepositoryOwner:   "owner",
			PullRequestAuthor: "outsider",
			AuthorAssociation: "CONTRIBUTOR",
		}, nil)
		if decision.Enabled {
			t.Fatalf("PR 判定不得因为触发者是所有者而放行: %s", decision.Reason)
		}
	})
}

// TestDecideIdentityMissing 校验任何模式下身份字段缺失都被拒绝。
func TestDecideIdentityMissing(t *testing.T) {
	t.Parallel()
	modes := []string{"self", "collaborator", "all"}
	for _, mode := range modes {
		mode := mode
		t.Run(mode+" 模式 Push 缺少发起者", func(t *testing.T) {
			t.Parallel()
			decision := cigate.Decide(context.Background(), cigate.Input{
				Mode:            mode,
				Event:           "push",
				Actor:           "   ",
				RepositoryOwner: "owner",
			}, writeChecker("owner"))
			if decision.Enabled {
				t.Fatalf("缺少 Push 发起者时不得放行: %s", decision.Reason)
			}
		})
		t.Run(mode+" 模式 Push 缺少仓库所有者", func(t *testing.T) {
			t.Parallel()
			decision := cigate.Decide(context.Background(), cigate.Input{
				Mode:  mode,
				Event: "push",
				Actor: "someone",
			}, writeChecker("someone"))
			if mode == "all" {
				return
			}
			if decision.Enabled {
				t.Fatalf("缺少仓库所有者时不得放行: %s", decision.Reason)
			}
		})
		t.Run(mode+" 模式 PR 缺少作者关系", func(t *testing.T) {
			t.Parallel()
			decision := cigate.Decide(context.Background(), cigate.Input{
				Mode:              mode,
				Event:             "pull_request",
				Actor:             "someone",
				RepositoryOwner:   "owner",
				PullRequestAuthor: "author",
			}, nil)
			if decision.Enabled {
				t.Fatalf("缺少 author_association 时不得放行: %s", decision.Reason)
			}
		})
	}
}

// TestDecideUnsupportedEvents 校验不受支持的事件类型不获得授权。
func TestDecideUnsupportedEvents(t *testing.T) {
	t.Parallel()
	events := []string{
		"workflow_call",
		"workflow_dispatch",
		"pull_request_target",
		"schedule",
		"",
	}
	for _, event := range events {
		event := event
		t.Run(fmt.Sprintf("事件 %q", event), func(t *testing.T) {
			t.Parallel()
			decision := cigate.Decide(context.Background(), cigate.Input{
				Mode:              "all",
				Event:             event,
				Actor:             "owner",
				RepositoryOwner:   "owner",
				PullRequestAuthor: "owner",
				AuthorAssociation: "OWNER",
			}, writeChecker("owner"))
			if decision.Enabled {
				t.Fatalf("事件 %q 不应获得真实测试授权: %s", event, decision.Reason)
			}
		})
	}
}

// TestDecisionReasonIsSingleLine 校验判定原因可直接写入 CI 输出文件。
func TestDecisionReasonIsSingleLine(t *testing.T) {
	t.Parallel()
	inputs := []cigate.Input{
		{Mode: "", Event: "push", Actor: "a", RepositoryOwner: "b"},
		{Mode: "self", Event: "push", Actor: "a", RepositoryOwner: "b"},
		{Mode: "collaborator", Event: "pull_request", AuthorAssociation: "CONTRIBUTOR"},
		{Mode: "all", Event: "pull_request", AuthorAssociation: "NONE"},
		{Mode: "all", Event: "nonsense"},
	}
	for index, input := range inputs {
		decision := cigate.Decide(context.Background(), input, writeChecker("a"))
		if decision.Reason == "" {
			t.Fatalf("第 %d 组输入没有判定原因", index)
		}
		if strings.ContainsAny(decision.Reason, "\r\n") {
			t.Fatalf("判定原因必须单行: %q", decision.Reason)
		}
	}
}
