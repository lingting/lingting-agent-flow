// Package cigate 提供 GitHub Actions 真实测试授权的判定规则。
//
// 需要真实外部依赖的测试（真实 SSH 设备、真实远程 Docker、容器内真实 Pi、真实远程
// Git 仓库）只有通过这里的信任策略才允许执行：单一用户变量 AGENTFLOW_REAL_TESTS
// 给出授权范围，事件类型与 GitHub 事件里的真实身份字段共同决定本次提交是否放行。
//
// 两条硬性约束：
//   - 身份只取 GitHub 事件字段（github.actor、github.repository_owner、
//     author_association），不使用 Git commit 的 author 姓名或邮箱：这些字段由提交者
//     自行填写，不具备权限证明能力；
//   - 任何无法确认的输入（变量取值未知、身份字段缺失、权限查询失败）一律按拒绝处理
//     （fail closed），不得把"判断不出来"当作授权。
package cigate

import (
	"context"
	"fmt"
	"strings"
)

// ModeVariable 是唯一允许影响真实测试授权范围的用户配置变量。
//
// 它在 GitHub 上以 Repository Variable（Settings → Secrets and variables → Actions →
// Variables）的形式维护，不是 Secret，不得为它再建第二套凭据管理机制。
const ModeVariable = "AGENTFLOW_REAL_TESTS"

// Mode 是真实测试的授权模式。
type Mode string

// 支持的授权模式。
//
// 空值表示关闭：变量未设置、设置为空或设置成约定之外的值都落到 ModeDisabled。
const (
	// ModeDisabled 表示未授权：真实测试一律跳过。
	ModeDisabled Mode = ""
	// ModeSelf 表示只有仓库所有者发起的提交与 PR 可以执行真实测试。
	ModeSelf Mode = "self"
	// ModeCollaborator 表示仓库所有者、组织成员与仓库协作者可以执行真实测试。
	ModeCollaborator Mode = "collaborator"
	// ModeAll 表示不区分作者身份，所有提交与 PR 都进入真实测试授权范围。
	//
	// 这是明确的信任策略放宽：启用后任意 PR 作者的代码都可能进入真实测试执行环境，
	// 接触真实测试设备与测试凭据，因此它不等于安全隔离。
	ModeAll Mode = "all"
)

// Display 返回用于报告展示的模式名，关闭状态显示为 disabled。
func (m Mode) Display() string {
	if m == ModeDisabled {
		return "disabled"
	}
	return string(m)
}

// ParseMode 解析 AGENTFLOW_REAL_TESTS 的取值。
//
// 只接受约定的小写值：SELF、Collaborator 之类的错误拼写、带空白的取值以及任何未知
// 取值都视为关闭。这里刻意不做大小写归一，避免把拼写错误当成有效授权。
func ParseMode(raw string) Mode {
	switch strings.TrimSpace(raw) {
	case string(ModeSelf):
		return ModeSelf
	case string(ModeCollaborator):
		return ModeCollaborator
	case string(ModeAll):
		return ModeAll
	default:
		return ModeDisabled
	}
}

// Event 是触发本次运行的 GitHub 事件类型。
type Event string

// 本项目真实测试需要处理的事件类型。
//
// 其他事件（workflow_call、schedule、workflow_dispatch 之外的自定义事件等）不自动
// 获得真实测试授权，统一按 EventUnsupported 处理。
const (
	// EventPush 是 push 事件，按实际 Push 发起者判断身份。
	EventPush Event = "push"
	// EventPullRequest 是按 PR 作者判断身份的 pull_request 事件。
	EventPullRequest Event = "pull_request"
	// EventUnsupported 表示未获得真实测试授权的事件类型。
	EventUnsupported Event = ""
)

// ParseEvent 解析事件名，只接受明确支持的事件。
func ParseEvent(name string) Event {
	switch strings.TrimSpace(name) {
	case string(EventPush):
		return EventPush
	case string(EventPullRequest):
		return EventPullRequest
	default:
		return EventUnsupported
	}
}

// PR 作者与仓库的关系取值，来自 github.event.pull_request.author_association。
const (
	// AssociationOwner 表示 PR 作者是仓库所有者。
	AssociationOwner = "OWNER"
	// AssociationMember 表示 PR 作者是拥有仓库的组织成员。
	AssociationMember = "MEMBER"
	// AssociationCollaborator 表示 PR 作者是仓库协作者。
	AssociationCollaborator = "COLLABORATOR"
	// AssociationContributor 表示 PR 作者是历史贡献者，但不是当前协作者。
	AssociationContributor = "CONTRIBUTOR"
	// AssociationFirstTimeContributor 表示 PR 作者是首次贡献者。
	AssociationFirstTimeContributor = "FIRST_TIME_CONTRIBUTOR"
	// AssociationFirstTimer 表示 PR 作者是首次参与 GitHub 协作的账号。
	AssociationFirstTimer = "FIRST_TIMER"
	// AssociationMannequin 表示 PR 作者是尚未认领的占位账号。
	AssociationMannequin = "MANNEQUIN"
	// AssociationNone 表示 PR 作者与仓库没有关系。
	AssociationNone = "NONE"
)

// collaboratorAssociations 是 collaborator 模式放行的 PR 作者关系集合。
//
// CONTRIBUTOR 只是历史贡献者，不等同于当前仓库协作者，因此不在其中。
var collaboratorAssociations = map[string]bool{
	AssociationOwner:        true,
	AssociationMember:       true,
	AssociationCollaborator: true,
}

// knownAssociations 是 GitHub 当前定义的全部 author_association 取值。
var knownAssociations = map[string]bool{
	AssociationOwner:                true,
	AssociationMember:               true,
	AssociationCollaborator:         true,
	AssociationContributor:          true,
	AssociationFirstTimeContributor: true,
	AssociationFirstTimer:           true,
	AssociationMannequin:            true,
	AssociationNone:                 true,
}

// Input 是一次授权判定的输入。
//
// 字段全部来自可信的 GitHub Actions 事件上下文，不接受提交内容里的自述身份。
type Input struct {
	// Mode 是 AGENTFLOW_REAL_TESTS 的原始取值，未设置时为空。
	Mode string
	// Event 是触发事件名，例如 push、pull_request、workflow_call。
	Event string
	// Actor 是触发本次运行的 GitHub 账号（github.actor）。
	//
	// push 事件使用它判断 Push 发起者；不得用 commit author 代替。
	Actor string
	// RepositoryOwner 是仓库所有者账号（github.repository_owner）。
	RepositoryOwner string
	// PullRequestAuthor 是 PR 作者账号（github.event.pull_request.user.login）。
	//
	// 它只用于报告：授权判定依据是 PR 作者关系，不是触发工作流的 actor。
	PullRequestAuthor string
	// AuthorAssociation 是 PR 作者与仓库的关系。
	AuthorAssociation string
}

// Decision 是授权判定结论。
type Decision struct {
	// Enabled 表示本次运行是否允许执行真实外部依赖测试。
	Enabled bool
	// Mode 是解析后的授权模式。
	Mode Mode
	// Reason 是中文判定原因，单行，可直接写入 CI 摘要与测试跳过原因。
	Reason string
}

// PermissionChecker 报告指定账号对仓库是否具备写入权限。
//
// 只有 push 事件的 collaborator 模式需要它：GitHub 事件里没有"推送者是否协作者"
// 字段，只能查询仓库权限。查询失败一律按拒绝处理。
type PermissionChecker interface {
	HasWritePermission(ctx context.Context, login string) (bool, error)
}

// Decide 依据模式、事件与身份字段给出真实测试授权结论。
//
// checker 只用于 push 事件的 collaborator 模式；其他情况可以传 nil。
func Decide(ctx context.Context, input Input, checker PermissionChecker) Decision {
	mode := ParseMode(input.Mode)
	if mode == ModeDisabled {
		return deny(mode, disabledReason(input.Mode))
	}
	event := ParseEvent(input.Event)
	switch event {
	case EventPush:
		return decidePush(ctx, mode, input, checker)
	case EventPullRequest:
		return decidePullRequest(mode, input)
	default:
		return deny(mode, unsupportedEventReason(input.Event))
	}
}

// disabledReason 生成变量未生效时的拒绝原因。
func disabledReason(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return fmt.Sprintf(
			"未配置 %s：真实测试默认关闭，只有显式设置 self / collaborator / all 才会授权",
			ModeVariable,
		)
	}
	return fmt.Sprintf(
		"%s 的取值 %q 不是受支持的取值（只接受 self / collaborator / all），按关闭处理",
		ModeVariable,
		strings.TrimSpace(raw),
	)
}

// unsupportedEventReason 生成事件类型不受支持时的拒绝原因。
func unsupportedEventReason(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "事件类型缺失：无法确认本次提交的真实身份，拒绝真实测试"
	}
	return fmt.Sprintf(
		"事件类型 %q 不获得真实测试授权：只有 push 与 pull_request 参与身份判定",
		trimmed,
	)
}

// decidePush 处理 push 事件：依据实际 Push 发起者判断身份。
func decidePush(
	ctx context.Context,
	mode Mode,
	input Input,
	checker PermissionChecker,
) Decision {
	actor := strings.TrimSpace(input.Actor)
	if actor == "" {
		return deny(mode, "身份信息缺失：没有得到 Push 发起者账号，拒绝真实测试")
	}
	if mode == ModeAll {
		return allow(mode, "模式 all 不区分 Push 发起者身份")
	}
	owner := strings.TrimSpace(input.RepositoryOwner)
	if owner == "" {
		return deny(mode, "身份信息缺失：没有得到仓库所有者账号，拒绝真实测试")
	}
	if strings.EqualFold(actor, owner) {
		return allow(mode, fmt.Sprintf("Push 发起者 %s 是仓库所有者", actor))
	}
	if mode == ModeSelf {
		return deny(mode, fmt.Sprintf(
			"模式 self 只允许仓库所有者，本次 Push 发起者为 %s",
			actor,
		))
	}
	// 余下情况是 collaborator 模式：需要确认该账号具备仓库写入权限。
	if checker == nil {
		return deny(mode, fmt.Sprintf(
			"模式 collaborator 无法确认 Push 发起者 %s 的仓库权限，按拒绝处理",
			actor,
		))
	}
	hasWrite, err := checker.HasWritePermission(ctx, actor)
	if err != nil {
		return deny(mode, fmt.Sprintf(
			"查询 Push 发起者 %s 的仓库权限失败，按拒绝处理: %v",
			actor,
			err,
		))
	}
	if !hasWrite {
		return deny(mode, fmt.Sprintf(
			"模式 collaborator 要求仓库协作者身份，Push 发起者 %s 不具备写入权限",
			actor,
		))
	}
	return allow(mode, fmt.Sprintf(
		"Push 发起者 %s 具备仓库写入权限（collaborator）",
		actor,
	))
}

// decidePullRequest 处理 pull_request 事件：依据 PR 作者的仓库关系判断身份。
func decidePullRequest(mode Mode, input Input) Decision {
	association := strings.ToUpper(strings.TrimSpace(input.AuthorAssociation))
	if association == "" {
		return deny(mode, "身份信息缺失：没有得到 PR 作者的 author_association，拒绝真实测试")
	}
	if mode == ModeAll {
		return allow(mode, fmt.Sprintf(
			"模式 all 不区分 PR 作者身份（author_association=%s）",
			association,
		))
	}
	if !knownAssociations[association] {
		return deny(mode, fmt.Sprintf(
			"PR 作者的 author_association 取值 %q 无法识别，按拒绝处理",
			association,
		))
	}
	switch mode {
	case ModeSelf:
		if association == AssociationOwner {
			return allow(mode, "PR 作者是仓库所有者（OWNER）")
		}
		return deny(mode, fmt.Sprintf(
			"模式 self 只允许仓库所有者，本次 PR 作者的 author_association 为 %s",
			association,
		))
	case ModeCollaborator:
		if collaboratorAssociations[association] {
			return allow(mode, fmt.Sprintf(
				"PR 作者具备仓库成员或协作者身份（author_association=%s）",
				association,
			))
		}
		return deny(mode, fmt.Sprintf(
			"模式 collaborator 要求仓库成员或协作者身份，本次 PR 作者的 author_association 为 %s",
			association,
		))
	default:
		return deny(mode, "授权模式不受支持，拒绝真实测试")
	}
}

// allow 构造授权通过的结论。
func allow(mode Mode, reason string) Decision {
	return Decision{Enabled: true, Mode: mode, Reason: oneLine(reason)}
}

// deny 构造拒绝授权的结论。
func deny(mode Mode, reason string) Decision {
	return Decision{Enabled: false, Mode: mode, Reason: oneLine(reason)}
}

// oneLine 把原因压成单行，保证它能直接写入 GitHub Actions 输出文件与摘要表格。
func oneLine(reason string) string {
	return strings.Join(strings.Fields(reason), " ")
}
