package domain

import (
	"fmt"
	"strings"
	"time"
)

// 本文件定义任务租约的持久化身份与 fencing 语义。
//
// 任务租约是任务的**唯一所有权令牌**：领取成功即取得任务的执行权，工作流执行记录
// （WorkflowRun）的租约只是它在执行阶段的续期视图。两者必须由同一个 owner 与同一
// 个领取代次持有，否则"任务领取"与"工作流推进"会退化成两套互不相干的所有权。
//
// 为什么需要代次（epoch）：owner 只是一个字符串，服务重启后可以重复使用同一个
// owner（例如进程级标识），仅凭 owner 无法区分"同一执行者的上一次领取"与"这一次
// 领取"。代次由数据库在每次领取时原子递增，因此 (owner, epoch) 唯一确定一次领取；
// 旧代次的写入、续租与释放都必须被拒绝。
//
// 该代次是**内部栅栏**，不是用户可控参数：它只由存储层的原子领取产生，HTTP 与 CLI
// 接口不得接受调用方传入的 epoch。

// TaskLeaseHandle 是一次任务领取的持久化身份。
//
// 它只在"存储层确认领取成功"之后由存储层返回，不能由调用方凭空构造后当作所有权
// 证明：任何使用它的写入都会附带 owner 与 epoch 两个数据库条件。
type TaskLeaseHandle struct {
	// TaskID 是被领取的任务。
	TaskID ID
	// Owner 是领取者标识，由领取方生成且每次领取唯一。
	Owner string
	// Epoch 是数据库在本次领取时分配的领取代次，从 1 开始单调递增。
	Epoch int64
}

// Validate 校验租约身份是否完整。
func (h TaskLeaseHandle) Validate() error {
	if err := h.TaskID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(h.Owner) == "" {
		return invalidField("TaskLease", "owner", "执行者标识不能为空")
	}
	if h.Epoch < 1 {
		return invalidField("TaskLease", "epoch", "领取代次必须大于 0")
	}
	return nil
}

// Matches 判断任务记录是否仍由该租约身份持有（不判断是否过期）。
//
// 它用于区分"执行权已转移到新代次"与"执行权仍在但已过期"两种不同的失效原因。
func (h TaskLeaseHandle) Matches(task Task) bool {
	if strings.TrimSpace(h.Owner) == "" || h.Epoch < 1 {
		return false
	}
	return task.LeaseOwner == h.Owner && task.LeaseEpoch == h.Epoch
}

// TaskHoldsLease 判断指定租约在给定时刻是否仍持有任务执行权。
//
// 三个条件同时成立才算持有：owner 匹配、代次匹配、租约未过期。任何一项不成立，
// 原执行者都必须立即停止推进，而不是继续写入或继续执行副作用。
//
// 与 TaskHoldsExecution 的区别：后者只比较 owner，无法区分同一 owner 的不同代次，
// 因此只适用于"owner 每次领取都唯一"的调用方；需要严格栅栏的路径必须使用本函数。
func TaskHoldsLease(task Task, lease TaskLeaseHandle, at time.Time) bool {
	return lease.Matches(task) && task.LeaseActive(at)
}

// TaskLeaseLossReason 是执行权失效原因的稳定分类。
type TaskLeaseLossReason string

// 执行权失效原因取值。
const (
	// TaskLeaseLossNone 表示仍然持有执行权。
	TaskLeaseLossNone TaskLeaseLossReason = ""
	// TaskLeaseLossSuperseded 表示任务已被另一个代次的执行者领取。
	TaskLeaseLossSuperseded TaskLeaseLossReason = "lease_superseded"
	// TaskLeaseLossExpired 表示租约已过期，原持有者没有及时续租。
	TaskLeaseLossExpired TaskLeaseLossReason = "lease_expired"
	// TaskLeaseLossReleased 表示租约已被释放，任务当前没有持有者。
	TaskLeaseLossReleased TaskLeaseLossReason = "lease_released"
)

// String 返回原因的字符串形式。
func (r TaskLeaseLossReason) String() string {
	return string(r)
}

// DecideTaskLeaseLoss 判断租约在给定时刻的失效原因。
//
// 规则顺序固定：
//  1. 任务记录的 owner 与代次都不匹配 → 已被新代次接管（superseded）；
//  2. 匹配但租约已失效 → 已过期或已释放（按是否仍有 owner 区分）。
//
// 结论只描述"为什么不能继续推进"，不描述"应该怎么修"：接管、过期与释放对应完全
// 不同的恢复动作，调用方不得把它们压缩成一个布尔值。
func DecideTaskLeaseLoss(task Task, lease TaskLeaseHandle, at time.Time) TaskLeaseLossReason {
	if lease.Matches(task) {
		if task.LeaseActive(at) {
			return TaskLeaseLossNone
		}
		if strings.TrimSpace(task.LeaseOwner) == "" {
			return TaskLeaseLossReleased
		}
		return TaskLeaseLossExpired
	}
	if strings.TrimSpace(task.LeaseOwner) == "" {
		return TaskLeaseLossReleased
	}
	return TaskLeaseLossSuperseded
}

// TaskLeaseRenewal 是"能否延长租约"的判定结论。
type TaskLeaseRenewal struct {
	// Allowed 表示允许续期。
	Allowed bool
	// Reason 是拒绝原因，允许时为空。
	Reason TaskLeaseLossReason
	// Detail 是可读说明，允许时为空。
	Detail string
}

// DecideTaskLeaseRenewal 判断当前持有者能否延长租约。
//
// 只有"仍是同一 owner、仍是同一代次、租约仍未过期"三者同时成立才允许续期：
//   - 代次不匹配说明任务已被接管，旧持有者不得通过续期把执行权抢回来；
//   - 租约已过期说明原持有者已经失去执行权，迟到续期同样不得恢复所有权。
//
// 该判定是纯函数，真正的原子性由存储层的条件更新保证：本函数只给出可读结论，
// 不能替代带 owner + epoch 条件的写入。
func DecideTaskLeaseRenewal(
	task Task,
	lease TaskLeaseHandle,
	at time.Time,
) TaskLeaseRenewal {
	switch DecideTaskLeaseLoss(task, lease, at) {
	case TaskLeaseLossNone:
		return TaskLeaseRenewal{Allowed: true}
	case TaskLeaseLossSuperseded:
		return TaskLeaseRenewal{
			Reason: TaskLeaseLossSuperseded,
			Detail: fmt.Sprintf(
				"任务 %s 的执行权已转移到代次 %d，当前代次 %d 不得续期",
				task.ID,
				task.LeaseEpoch,
				lease.Epoch,
			),
		}
	case TaskLeaseLossExpired:
		return TaskLeaseRenewal{
			Reason: TaskLeaseLossExpired,
			Detail: fmt.Sprintf("任务 %s 的租约已过期，迟到续期不得恢复执行权", task.ID),
		}
	default:
		return TaskLeaseRenewal{
			Reason: TaskLeaseLossReleased,
			Detail: fmt.Sprintf("任务 %s 的租约已被释放，不得续期", task.ID),
		}
	}
}

// TaskLeaseReleaseOutcome 是一次释放尝试的结论。
type TaskLeaseReleaseOutcome string

// 释放结论取值。
const (
	// TaskLeaseReleased 表示本次调用真的清除了自己的租约。
	TaskLeaseReleased TaskLeaseReleaseOutcome = "released"
	// TaskLeaseReleaseConflict 表示没有释放任何记录：租约已不属于本次领取。
	//
	// 它不是"释放成功"：调用方不得据此认为名额已经空出。
	TaskLeaseReleaseConflict TaskLeaseReleaseOutcome = "conflict"
)

// CheckTaskLeaseRelease 判断释放是否真的命中了本次领取的租约。
//
// 存储层必须返回受影响行数，本函数把"0 行"翻译成可识别的冲突而不是成功：租约被
// 接管、已过期释放、代次变化都意味着本次执行者已无权释放，名额归属必须以数据库
// 事实为准。
func CheckTaskLeaseRelease(affected int64) (TaskLeaseReleaseOutcome, error) {
	if affected > 0 {
		return TaskLeaseReleased, nil
	}
	return TaskLeaseReleaseConflict, fmt.Errorf(
		"%w: 租约已不属于本次领取（owner 或代次不匹配），没有任何记录被释放",
		ErrInvalidState,
	)
}
