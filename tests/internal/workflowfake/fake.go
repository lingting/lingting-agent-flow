// Package workflowfake 提供工作流引擎的受控替身。
//
// 替身只模拟下层组件的协议行为，不复制工作流决策：重试、判定与资源处置仍然由
// 真实工作流引擎执行，因此测试核验的是真实的协调顺序与持久化结果。
package workflowfake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/workflow"
)

// Ports 是一组共享调用日志的受控替身。
type Ports struct {
	Containers *Containers
	Source     *Source
	Agents     *Agents
	Commands   *Commands
	CodeSaver  *CodeSaver
	// Calls 按发生顺序记录跨组件调用。
	Calls *CallLog
}

// New 构造一组互相共享调用日志的替身。
func New() *Ports {
	log := &CallLog{}
	return &Ports{
		Containers: &Containers{Log: log},
		Source:     &Source{Log: log},
		Agents:     &Agents{Log: log},
		Commands:   &Commands{Log: log},
		CodeSaver:  &CodeSaver{Log: log},
		Calls:      log,
	}
}

// Ports 返回工作流引擎所需的依赖集合。
func (p *Ports) Ports() workflow.Ports {
	return workflow.Ports{
		Containers: p.Containers,
		Source:     p.Source,
		Agents:     p.Agents,
		Commands:   p.Commands,
		CodeSaver:  p.CodeSaver,
	}
}

// CallLog 是线程安全的调用顺序记录。
type CallLog struct {
	mu      sync.Mutex
	entries []string
}

// Add 追加一条调用记录。
func (l *CallLog) Add(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

// Snapshot 返回当前调用顺序的副本。
func (l *CallLog) Snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	copied := make([]string, len(l.entries))
	copy(copied, l.entries)
	return copied
}

// Contains 判断调用顺序中是否出现过指定条目。
func (l *CallLog) Contains(entry string) bool {
	return l.Index(entry) >= 0
}

// Index 返回指定条目的位置，不存在时返回 -1。
func (l *CallLog) Index(entry string) int {
	for position, item := range l.Snapshot() {
		if item == entry {
			return position
		}
	}
	return -1
}

// Containers 是容器生命周期的替身。
type Containers struct {
	Log *CallLog
	// CreateErr 使创建容器失败。
	CreateErr error
	// InspectErr 使核验容器失败。
	InspectErr error
	// DisposeErr 使删除容器失败。
	DisposeErr error
	// States 覆盖指定容器的核验结果。
	States map[string]workflow.ContainerInfo
	// Created 是按顺序创建的容器记录。
	Created []workflow.ContainerInfo
	// Disposed 是已处置的容器与处置方式。
	Disposed []DisposeRecord
	// NextID 是下一个容器登记标识序号。
	NextID int
}

// DisposeRecord 是一次容器处置记录。
type DisposeRecord struct {
	ContainerID string
	Disposition domain.RestartDisposition
}

// Create 创建并记录一个任务容器。
func (c *Containers) Create(
	ctx context.Context,
	spec workflow.ContainerSpec,
) (workflow.ContainerInfo, error) {
	if err := ctx.Err(); err != nil {
		return workflow.ContainerInfo{}, err
	}
	c.Log.Add("container.create")
	if c.CreateErr != nil {
		return workflow.ContainerInfo{}, c.CreateErr
	}
	c.NextID++
	info := workflow.ContainerInfo{
		ID:       fmt.Sprintf("container-%d", c.NextID),
		DockerID: fmt.Sprintf("docker-%d", c.NextID),
		HostID:   domain.ID(spec.HostID),
		Running:  true,
	}
	c.Created = append(c.Created, info)
	return info, nil
}

// Inspect 返回容器的核验结果。
func (c *Containers) Inspect(
	ctx context.Context,
	containerID string,
) (workflow.ContainerInfo, error) {
	if err := ctx.Err(); err != nil {
		return workflow.ContainerInfo{}, err
	}
	c.Log.Add("container.inspect:" + containerID)
	if c.InspectErr != nil {
		return workflow.ContainerInfo{}, c.InspectErr
	}
	if state, ok := c.States[containerID]; ok {
		return state, nil
	}
	return workflow.ContainerInfo{ID: containerID, Running: true}, nil
}

// Dispose 记录一次容器处置。
func (c *Containers) Dispose(
	ctx context.Context,
	containerID string,
	disposition domain.RestartDisposition,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.Log.Add("container.dispose:" + string(disposition))
	if c.DisposeErr != nil {
		return c.DisposeErr
	}
	c.Disposed = append(c.Disposed, DisposeRecord{
		ContainerID: containerID,
		Disposition: disposition,
	})
	return nil
}

// Source 是代码获取的替身。
type Source struct {
	Log *CallLog
	// Err 使代码获取失败。
	Err error
	// Result 是返回的代码获取结果。
	Result workflow.SourceInfo
	// Specs 记录代码获取请求。
	Specs []workflow.SourceSpec
}

// Fetch 返回受控的代码获取结果。
func (s *Source) Fetch(
	ctx context.Context,
	spec workflow.SourceSpec,
) (workflow.SourceInfo, error) {
	if err := ctx.Err(); err != nil {
		return workflow.SourceInfo{}, err
	}
	s.Log.Add("source.fetch")
	s.Specs = append(s.Specs, spec)
	if s.Err != nil {
		return workflow.SourceInfo{}, s.Err
	}
	result := s.Result
	if result.WorkDir == "" {
		result.WorkDir = spec.WorkDir
	}
	if result.BaseBranch == "" {
		result.BaseBranch = spec.BaseBranch
	}
	if result.TaskBranch == "" {
		result.TaskBranch = "agent/" + spec.TaskID
	}
	if result.Commit == "" {
		result.Commit = "0123456789012345678901234567890123456789"
	}
	return result, nil
}

// AgentReply 是一次 Agent 调用的预设回复。
type AgentReply struct {
	// FinalText 是最终文本。
	FinalText string
	// Status 是运行状态，零值表示 completed。
	Status agent.RunStatus
	// Failure 是失败摘要。
	Failure string
	// ExitCode 是退出码。
	ExitCode int
	// Err 是返回给调用方的错误。
	Err error
}

// Agents 是 Agent 调用的替身。
type Agents struct {
	Log *CallLog
	// Replies 按角色类型给出预设回复队列。
	Replies map[domain.RoleType][]AgentReply
	// Invocations 记录每次调用的输入。
	Invocations []workflow.AgentInvocation
	mu          sync.Mutex
	// SessionSeq 是会话标识序列。
	SessionSeq int
	// FailSessionRecord 使会话记录缺失，用于模拟会话不可用。
	FailSessionRecord bool
	// Store 非空时把会话元数据写入数据库，模拟真实 AgentPort 的持久化行为。
	Store storage.SessionStore
}

// Run 执行一次受控的 Agent 调用。
func (a *Agents) Run(
	ctx context.Context,
	invocation workflow.AgentInvocation,
) (workflow.AgentOutcome, error) {
	if err := ctx.Err(); err != nil {
		return workflow.AgentOutcome{}, err
	}
	a.mu.Lock()
	a.Invocations = append(a.Invocations, invocation)
	reply := a.nextReply(invocation.Role.Type)
	a.SessionSeq++
	sessionSeq := a.SessionSeq
	a.mu.Unlock()

	a.Log.Add("agent.run:" + invocation.Role.Type.String())
	status := reply.Status
	if status == "" {
		status = agent.RunCompleted
	}
	startedAt := time.Now().UTC()
	result := agent.RunResult{
		SessionID:    domain.NewID().String(),
		InvocationID: fmt.Sprintf("invocation-%d", sessionSeq),
		Status:       status,
		FinalText:    reply.FinalText,
		Failure:      reply.Failure,
		ExitCode:     reply.ExitCode,
		StartedAt:    startedAt,
		FinishedAt:   startedAt.Add(time.Second),
		Session: agent.SessionFiles{
			JSONLPath:    fmt.Sprintf("sessions/invocation-%d/session.jsonl", sessionSeq),
			Completeness: agent.SessionComplete,
			ExportStatus: agent.ExportNotAttempted,
		},
	}
	if status != agent.RunCompleted {
		result.Session.Completeness = agent.SessionPartial
	}
	session := domain.Session{
		ID:           domain.ID(result.SessionID),
		TaskID:       invocation.Scope.TaskID,
		StepID:       invocation.Scope.StepID,
		AttemptID:    invocation.Scope.AttemptID,
		RoleID:       invocation.Role.ID,
		RoleSnapshot: invocation.Role.Snapshot(),
		Provider:     "provider",
		Model:        invocation.Role.Model,
		Status:       domain.SessionStatusCompleted,
		InvocationID: result.InvocationID,
		JSONLPath:    result.Session.JSONLPath,
		Completeness: domain.SessionCompletenessComplete,
		ExportStatus: domain.SessionExportNotAttempted,
		RunStatus:    domain.SessionRunCompleted,
		StartedAt:    startedAt,
		CreatedAt:    startedAt,
	}
	if a.FailSessionRecord {
		// 模拟"Session 无法提取"：记录保留但缺失本机文件。
		session.JSONLPath = ""
		session.Completeness = domain.SessionCompletenessMissing
		session.ExportStatus = domain.SessionExportNotAttempted
	}
	if status != agent.RunCompleted {
		session.Status = domain.SessionStatusFailed
		session.RunStatus = domain.SessionRunFailed
		session.Completeness = domain.SessionCompletenessPartial
	}
	if a.Store != nil {
		if err := a.Store.CreateSession(ctx, session); err != nil {
			return workflow.AgentOutcome{Result: result}, fmt.Errorf(
				"写入测试会话失败: %w",
				err,
			)
		}
	}
	return workflow.AgentOutcome{Result: result, Session: session}, reply.Err
}

// nextReply 取出一条预设回复，用尽后返回空回复。
func (a *Agents) nextReply(roleType domain.RoleType) AgentReply {
	queue := a.Replies[roleType]
	if len(queue) == 0 {
		return AgentReply{}
	}
	reply := queue[0]
	a.Replies[roleType] = queue[1:]
	return reply
}

// SetReplies 设置某个角色的预设回复队列。
func (a *Agents) SetReplies(roleType domain.RoleType, replies ...AgentReply) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Replies == nil {
		a.Replies = make(map[domain.RoleType][]AgentReply)
	}
	a.Replies[roleType] = replies
}

// Recorded 返回已记录的调用输入副本。
func (a *Agents) Recorded() []workflow.AgentInvocation {
	a.mu.Lock()
	defer a.mu.Unlock()
	copied := make([]workflow.AgentInvocation, len(a.Invocations))
	copy(copied, a.Invocations)
	return copied
}

// Commands 是确定性验证命令的替身。
type Commands struct {
	Log *CallLog
	// Results 按命令名称给出预设结果。
	Results map[string]domain.CommandResult
	// Errors 按命令名称给出执行错误。
	Errors map[string]error
	// Runs 记录已执行的命令。
	Runs []workflow.CommandRun
	mu   sync.Mutex
}

// Exec 执行一条受控的验证命令。
func (c *Commands) Exec(
	ctx context.Context,
	containerID string,
	run workflow.CommandRun,
) (domain.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.CommandResult{}, err
	}
	c.mu.Lock()
	c.Runs = append(c.Runs, run)
	c.mu.Unlock()
	c.Log.Add("command.exec:" + run.Command.Name)
	result, ok := c.Results[run.Command.Name]
	if !ok {
		result = domain.CommandResult{ExitCode: 0, Certain: true}
	}
	result.Name = run.Command.Name
	result.Command = run.Command.Command
	result.Required = run.Command.IsRequired()
	result.AllowTimeoutPass = run.Command.AllowTimeoutPass
	result.AllowedExitCodes = run.Command.AllowedExitCodes
	return result, c.Errors[run.Command.Name]
}

// Recorded 返回已执行的命令副本。
func (c *Commands) Recorded() []workflow.CommandRun {
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := make([]workflow.CommandRun, len(c.Runs))
	copy(copied, c.Runs)
	return copied
}

// CodeSaver 是代码保存的替身。
type CodeSaver struct {
	Log *CallLog
	// Record 是返回的保存记录；为零值时构造"提交并推送成功"的记录。
	Record domain.CodeSaveRecord
	// Err 是返回给调用方的错误。
	Err error
	// Specs 记录保存请求。
	Specs []workflow.CodeSaveSpec
}

// Save 返回受控的代码保存结果。
func (s *CodeSaver) Save(
	ctx context.Context,
	spec workflow.CodeSaveSpec,
) (workflow.CodeSaveOutcome, error) {
	if err := ctx.Err(); err != nil {
		return workflow.CodeSaveOutcome{}, err
	}
	s.Log.Add("codesaver.save")
	s.Specs = append(s.Specs, spec)
	record := s.Record
	if record.ID.IsEmpty() {
		record = DefaultRecord(spec)
	}
	return workflow.CodeSaveOutcome{Record: record}, s.Err
}

// DefaultRecord 构造一条"提交并推送成功"的保存记录。
func DefaultRecord(spec workflow.CodeSaveSpec) domain.CodeSaveRecord {
	now := time.Now().UTC()
	mergeStatus := domain.MergeStatusNotRequested
	if spec.MergeAfterPush {
		mergeStatus = domain.MergeStatusSucceeded
	}
	record := domain.CodeSaveRecord{
		ID:              domain.NewID(),
		TaskID:          spec.TaskID,
		ContainerID:     spec.ContainerID,
		AttemptID:       spec.AttemptID,
		RemoteName:      "origin",
		Repository:      "ssh://git.example.com/team/repo",
		TaskBranch:      "agent/" + spec.TaskID,
		BaseBranch:      spec.BaseBranch,
		CommitStatus:    domain.CommitStatusSucceeded,
		CommitSHA:       "0123456789012345678901234567890123456789",
		PushStatus:      domain.PushStatusSucceeded,
		RemoteCommitSHA: "0123456789012345678901234567890123456789",
		MergeEnabled:    spec.MergeAfterPush,
		MergeStatus:     mergeStatus,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if spec.MergeAfterPush {
		record.MergeSHA = "9876543210987654321098765432109876543210"
	}
	return record
}

// PlanJSON 构造一份合法的 Planner 结构化输出。
func PlanJSON(summary string, steps ...PlanStep) string {
	body := ""
	for index, step := range steps {
		if index > 0 {
			body += ","
		}
		body += fmt.Sprintf(
			`{"id":"step-%d","title":%q,"objective":%q,"instructions":%q,"acceptance_criteria":[%q]}`,
			index+1,
			step.Title,
			step.Objective,
			step.Instructions,
			step.Criteria,
		)
	}
	return fmt.Sprintf(`{"summary":%q,"steps":[%s]}`, summary, body)
}

// PlanStep 是构造计划 JSON 时的单步描述。
type PlanStep struct {
	Title        string
	Objective    string
	Instructions string
	Criteria     string
}

// AssessmentJSON 构造一份合法的 Validator 结构化输出。
func AssessmentJSON(passed bool, summary string, findings ...string) string {
	body := ""
	for index, finding := range findings {
		if index > 0 {
			body += ","
		}
		body += fmt.Sprintf("%q", finding)
	}
	return fmt.Sprintf(
		`{"passed":%t,"summary":%q,"findings":[%s],"evidence":[],"suggest_fix":false,"needs_human_review":false}`,
		passed,
		summary,
		body,
	)
}
