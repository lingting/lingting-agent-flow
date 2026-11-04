package domain

import (
	"strings"
)

// 本文件定义规划会话执行租约的持久化身份与操作分类。
//
// 规划调用（容器准备、角色会话绑定、Pi 执行与 Session 归档）必须由同一个执行者独占。
// 进程内互斥只在单个 Server 内有效，因此"谁在占用这个规划会话"必须成为数据库事实：
// 存储层用一条原子条件写入决定归属，并用领取代次（epoch）作为内部栅栏。
//
// 为什么需要 epoch：owner 只是一个字符串，服务重启或同一进程重复调用都可能复用相同
// 形态的标识，仅凭 owner 无法区分"同一执行者的上一次领取"与"这一次领取"。代次由存储层
// 在每次成功领取时原子递增，因此 (owner, epoch) 唯一确定一次领取；旧代次的续期与释放
// 都必须被拒绝。
//
// 该代次是**内部栅栏**，不是用户可控参数：它只由存储层的原子领取产生，HTTP、CLI 与
// 应用层接口都不得接受调用方传入的 epoch。

// PlanningSessionLeaseHandle 是一次规划会话租约领取的持久化身份。
//
// 它只在"存储层确认领取成功"之后由存储层返回，不能由调用方凭空构造后当作执行权证明：
// 任何使用它的写入都会附带 owner 与 epoch 两个数据库条件。
type PlanningSessionLeaseHandle struct {
	// PlanningSessionID 是被领取的规划会话。
	PlanningSessionID ID
	// Owner 是领取者标识，由服务端生成且每次领取唯一。
	Owner string
	// Epoch 是数据库在本次领取时分配的领取代次，从 1 开始单调递增。
	Epoch int64
}

// Validate 校验租约身份是否完整。
func (h PlanningSessionLeaseHandle) Validate() error {
	if err := h.PlanningSessionID.Validate(); err != nil {
		return invalidField("PlanningSessionLease", "planning_session_id", "规划会话标识不能为空")
	}
	if strings.TrimSpace(h.Owner) == "" {
		return invalidField("PlanningSessionLease", "owner", "执行者标识不能为空")
	}
	if h.Epoch < 1 {
		return invalidField("PlanningSessionLease", "epoch", "领取代次必须大于 0")
	}
	return nil
}

// PlanningLeaseOperation 是规划会话租约当前承载的操作类型。
//
// 它只是诊断与审计用的分类，不是数据库枚举：Schema 不为该列设取值白名单，新增取值必须
// 能在不修改 Schema 的情况下写入。租约的有效性只由 owner + epoch + 到期时间决定，与操作
// 类型无关，因此不同操作之间互相排斥的依据是"同一规划会话只有一份租约"。
type PlanningLeaseOperation string

// 规划会话租约的操作类型取值。
const (
	// PlanningLeaseOperationRunRole 表示执行一轮规划角色调用，覆盖 Pi 执行与 Session 归档。
	PlanningLeaseOperationRunRole PlanningLeaseOperation = "run_role"
	// PlanningLeaseOperationPrepare 表示准备或复用规划运行环境。
	PlanningLeaseOperationPrepare PlanningLeaseOperation = "prepare"
	// PlanningLeaseOperationRelease 表示释放规划容器与快照卷。
	PlanningLeaseOperationRelease PlanningLeaseOperation = "release"
)

// String 返回操作类型的字符串形式。
func (o PlanningLeaseOperation) String() string {
	return string(o)
}
