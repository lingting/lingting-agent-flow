package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// 本文件定义项目级执行权（Project Execution Lease）的业务语义。
//
// 它只包含纯类型与纯判定函数：不读数据库、不取当前时间、不启动 goroutine，也不涉及
// HTTP 与后台服务生命周期。真正的原子取得、条件续租与条件释放由存储层用带条件的
// SQL 完成，应用层只按这里的结论处理冲突、过期与 fencing。
//
// 与任务执行权的关系：任务执行权由进程内的任务协调器登记（同一进程内同一任务只有
// 一个执行者），项目执行权是跨请求、跨重启仍需识别的事实，因此它单独持久化。两者
// 并存、互不替代：拿到项目执行权不等于取得任务执行权，反之亦然。

// 项目执行权判定的稳定错误，调用方通过 errors.Is 判断类别。
var (
	// ErrProjectLeaseHeld 表示项目执行权仍被其他持有者占用，且尚未到期。
	ErrProjectLeaseHeld = errors.New("domain: 项目执行权被其他持有者占用")
	// ErrProjectLeaseLost 表示本次调用不再持有项目执行权（已释放或被新 epoch 取代）。
	ErrProjectLeaseLost = errors.New("domain: 项目执行权已不属于当前持有者")
	// ErrProjectLeaseExpired 表示项目执行权已经过期，持有者不得继续推进。
	ErrProjectLeaseExpired = errors.New("domain: 项目执行权已过期")
)

// maxProjectExecutionOwnerLength 是项目执行权持有者标识的长度上限。
//
// 持有者标识是进程内部生成的不透明字符串，不写入任何用户可见内容，长度上限只用于
// 拒绝异常输入，不表达业务含义。
const maxProjectExecutionOwnerLength = 128

// ProjectExecutionState 是项目执行意图的持久化记录。
//
// 它不是第二套状态机：接口返回的项目状态由 DecideProjectExecution 依据任务真实状态、
// 执行权与停止意图计算；这里的取值只是"最后一次决策写下的记录"，用于跨重启保留
// 用户的停止意图与排查现场。枚举式取值不受数据库白名单限制，未知取值必须原样保留。
type ProjectExecutionState string

// 项目执行意图取值。
const (
	// ProjectExecutionStateIdle 表示项目没有在执行，也没有待领取任务。
	ProjectExecutionStateIdle ProjectExecutionState = "idle"
	// ProjectExecutionStateRunning 表示项目正在执行协调中。
	ProjectExecutionStateRunning ProjectExecutionState = "running"
	// ProjectExecutionStateBlocked 表示项目被未收敛或失败的前序任务阻塞。
	ProjectExecutionStateBlocked ProjectExecutionState = "blocked"
	// ProjectExecutionStatePaused 表示用户显式停止，且当前没有任务在执行。
	ProjectExecutionStatePaused ProjectExecutionState = "paused"
	// ProjectExecutionStateStopping 表示用户已请求停止，当前任务仍在收敛。
	ProjectExecutionStateStopping ProjectExecutionState = "stopping"
)

// Validate 校验项目执行意图取值是否属于已知取值。
func (s ProjectExecutionState) Validate() error {
	switch s {
	case ProjectExecutionStateIdle, ProjectExecutionStateRunning, ProjectExecutionStateBlocked,
		ProjectExecutionStatePaused, ProjectExecutionStateStopping:
		return nil
	default:
		return invalidField("ProjectExecution", "state", "未知取值 "+string(s))
	}
}

// ProjectExecutionLease 是一个项目的执行权登记。
//
// 不变量：
//   - 一个项目最多一份执行权（存储层以 project_id 为主键保证）；
//   - epoch 单调递增，取得新执行权时递增，释放不重置；
//   - 未到期的执行权不能被其他持有者抢占；
//   - 只有 owner + epoch 同时匹配的调用者可以续租或释放。
type ProjectExecutionLease struct {
	// ProjectID 是执行权所属项目。
	ProjectID ID `json:"project_id"`
	// Owner 是持有者标识，由服务进程在启动时生成，用于区分不同进程实例。
	Owner string `json:"owner"`
	// Epoch 是单调递增的执行权代数，用于 fencing 迟到的旧持有者。
	Epoch int64 `json:"epoch"`
	// ExpiresAt 是执行权到期时间；到期后其他持有者可以重新取得。
	ExpiresAt time.Time `json:"expires_at"`
	// AcquiredAt 是本次执行权被取得的时间。
	AcquiredAt time.Time `json:"acquired_at"`
	// UpdatedAt 是执行权最后一次写入时间。
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate 校验执行权记录的字段完整性。
func (l ProjectExecutionLease) Validate() error {
	if err := l.ProjectID.Validate(); err != nil {
		return err
	}
	if _, err := NormalizeProjectExecutionOwner(l.Owner); err != nil {
		return err
	}
	if l.Epoch <= 0 {
		return invalidField("ProjectExecutionLease", "epoch", "执行权代数必须大于 0")
	}
	if l.ExpiresAt.IsZero() {
		return invalidField("ProjectExecutionLease", "expires_at", "到期时间不能为空")
	}
	if l.AcquiredAt.IsZero() {
		return invalidField("ProjectExecutionLease", "acquired_at", "取得时间不能为空")
	}
	if l.UpdatedAt.IsZero() {
		return invalidField("ProjectExecutionLease", "updated_at", "更新时间不能为空")
	}
	return nil
}

// ActiveAt 判断执行权在给定时刻是否仍然有效。
func (l ProjectExecutionLease) ActiveAt(now time.Time) bool {
	return l.Epoch > 0 && l.ExpiresAt.After(now)
}

// HeldBy 判断执行权是否由指定持有者的指定代数持有。
func (l ProjectExecutionLease) HeldBy(owner string, epoch int64) bool {
	if epoch <= 0 || l.Epoch != epoch {
		return false
	}
	return strings.TrimSpace(l.Owner) == strings.TrimSpace(owner) && strings.TrimSpace(owner) != ""
}

// HeldByOwner 判断执行权是否由指定持有者持有（不区分代数）。
//
// 它只用于"重复开始是否幂等"这一判定：持有者相同且未到期时，重复开始不产生第二份
// 执行权，也不递增 epoch。
func (l ProjectExecutionLease) HeldByOwner(owner string) bool {
	normalized := strings.TrimSpace(owner)
	return normalized != "" && strings.TrimSpace(l.Owner) == normalized
}

// NormalizeProjectExecutionOwner 规范化并校验持有者标识。
func NormalizeProjectExecutionOwner(owner string) (string, error) {
	trimmed := strings.TrimSpace(owner)
	if trimmed == "" {
		return "", fmt.Errorf("%w: 项目执行权持有者不能为空", ErrInvalidValue)
	}
	if len(trimmed) > maxProjectExecutionOwnerLength {
		return "", fmt.Errorf("%w: 项目执行权持有者过长", ErrInvalidValue)
	}
	return trimmed, nil
}

// DecideProjectLeaseAcquire 判定本次调用能否取得项目执行权，并给出取得后的 epoch。
//
// 规则固定为：
//  1. 没有登记（existing 为空）→ 首次取得，epoch = 1；
//  2. 已有登记但已到期 → 允许重新取得，epoch = 原 epoch + 1（旧持有者的迟到写入被
//     epoch 条件拒绝）；
//  3. 已到期之外的情况一律不允许抢占：返回 ErrProjectLeaseHeld。
//
// 重复开始不在这里判定：持有者相同且未到期时由调用方按"幂等"处理，不递增 epoch。
// 该函数是纯函数，不会因为调用时间不同而给出不同结论之外的变化。
func DecideProjectLeaseAcquire(
	existing *ProjectExecutionLease,
	owner string,
	now time.Time,
	expiresAt time.Time,
) (int64, error) {
	if _, err := NormalizeProjectExecutionOwner(owner); err != nil {
		return 0, err
	}
	if !expiresAt.After(now) {
		return 0, fmt.Errorf(
			"%w: 项目执行权到期时间 %s 必须晚于当前时间 %s",
			ErrInvalidValue,
			expiresAt.Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano),
		)
	}
	if existing == nil {
		return 1, nil
	}
	if err := existing.ProjectID.Validate(); err != nil {
		return 0, err
	}
	if existing.Epoch <= 0 {
		return 0, invalidField("ProjectExecutionLease", "epoch", "执行权代数必须大于 0")
	}
	if existing.ActiveAt(now) {
		return 0, fmt.Errorf(
			"%w: 项目 %s 的执行权由 %s 持有且在 %s 之前不会到期",
			ErrProjectLeaseHeld,
			existing.ProjectID,
			existing.Owner,
			existing.ExpiresAt.Format(time.RFC3339Nano),
		)
	}
	return existing.Epoch + 1, nil
}

// DecideProjectLeaseRenew 判定本次调用能否续租项目执行权。
//
// 只有 owner + epoch 同时匹配且未到期的持有者可以续租：epoch 不匹配说明执行权已换人，
// 到期说明可能在续租与抢占之间被其他持有者取得，两种情况都必须停止继续领取任务。
func DecideProjectLeaseRenew(
	lease *ProjectExecutionLease,
	owner string,
	epoch int64,
	now time.Time,
	expiresAt time.Time,
) error {
	if lease == nil {
		return fmt.Errorf("%w: 项目 %s 没有执行权登记", ErrProjectLeaseLost, owner)
	}
	if !lease.HeldBy(owner, epoch) {
		return fmt.Errorf(
			"%w: 项目 %s 的执行权已不是持有者 %s 的 epoch %d",
			ErrProjectLeaseLost,
			lease.ProjectID,
			owner,
			epoch,
		)
	}
	if !lease.ActiveAt(now) {
		return fmt.Errorf(
			"%w: 项目 %s 的执行权已于 %s 过期",
			ErrProjectLeaseExpired,
			lease.ProjectID,
			lease.ExpiresAt.Format(time.RFC3339Nano),
		)
	}
	if !expiresAt.After(now) {
		return fmt.Errorf("%w: 续租后的到期时间必须晚于当前时间", ErrInvalidValue)
	}
	return nil
}

// DecideProjectLeaseRelease 判定本次调用能否释放项目执行权。
//
// 释放只清空有效期（不删除行、不重置 epoch）；带 owner + epoch 条件是为了让旧持有者
// 迟到的释放被拒绝，不会把新持有者刚取得的执行权放掉。
func DecideProjectLeaseRelease(
	lease *ProjectExecutionLease,
	owner string,
	epoch int64,
) error {
	if lease == nil {
		return fmt.Errorf("%w: 项目 %s 没有执行权登记", ErrProjectLeaseLost, owner)
	}
	if !lease.HeldBy(owner, epoch) {
		return fmt.Errorf(
			"%w: 项目 %s 的执行权已不是持有者 %s 的 epoch %d",
			ErrProjectLeaseLost,
			lease.ProjectID,
			owner,
			epoch,
		)
	}
	return nil
}
