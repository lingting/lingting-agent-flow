package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/workflow"
	"github.com/spf13/cobra"
)

// newTaskCommand 构建 task 命令组。
func newTaskCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "task",
		Short: "创建、执行、查询、取消、重试与恢复任务",
		Long: "任务状态、重试策略、验证规则与资源清理全部由 Go 工作流引擎控制。\n" +
			"每次 Planner、Developer、Validator 调用都使用独立的 Invocation 与独立 Session。\n" +
			"必需确定性验证命令先执行，Agent 的 passed 结论不能覆盖必需检查的失败。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.PersistentFlags().String(
		"config",
		"",
		"配置文件路径（默认 "+config.DefaultPath+"）",
	)
	command.AddCommand(newTaskCreateCommand(a))
	command.AddCommand(newTaskRunCommand(a))
	command.AddCommand(newTaskGetCommand(a))
	command.AddCommand(newTaskListCommand(a))
	command.AddCommand(newTaskCancelCommand(a))
	command.AddCommand(newTaskRetryCommand(a))
	command.AddCommand(newTaskRecoverCommand(a))
	return command
}

// taskServices 是一次任务命令执行所需的依赖。
type taskServices struct {
	tasks    *app.TaskService
	engine   *workflow.Engine
	recovery *workflow.Recovery
}

// withTaskServices 加载配置并构造任务服务与工作流引擎。
func withTaskServices(
	ctx context.Context,
	a *app.App,
	configPath string,
	flags credentialFlags,
	hostID string,
	run func(ctx context.Context, services taskServices) error,
) error {
	return withDockerServices(ctx, a, configPath, func(
		ctx context.Context,
		services dockerServices,
	) error {
		// 认证方式固定为无口令私钥，任务执行不需要额外收集凭据；
		// 节点是否可用由执行节点准入校验在执行阶段统一确认。
		sessions, err := app.NewSessionService(
			services.store,
			app.SessionRoot(services.config),
			a.Logger().Slog(),
		)
		if err != nil {
			return err
		}
		workflowService, err := app.NewWorkflowService(app.WorkflowOptions{
			Containers: services.containers,
			Sessions:   sessions,
			Store:      services.store,
			Logger:     a.Logger().Slog(),
		})
		if err != nil {
			return err
		}
		maxParallel, _ := app.WorkflowOptionsFromConfig(services.config)
		engine, err := workflow.NewEngine(workflow.Options{
			Store:       services.store,
			Ports:       workflowService.Ports(),
			Logger:      a.Logger().Slog(),
			MaxParallel: maxParallel,
		})
		if err != nil {
			return err
		}
		recovery, err := workflow.NewRecovery(workflow.RecoveryOptions{
			Store:  services.store,
			Ports:  workflowService.Ports(),
			Logger: a.Logger().Slog(),
		})
		if err != nil {
			return err
		}
		tasks, err := app.NewTaskService(app.TaskServiceOptions{
			Store:    services.store,
			Logger:   a.Logger().Slog(),
			Engine:   engine,
			Recovery: recovery,
		})
		if err != nil {
			return err
		}
		return run(ctx, taskServices{tasks: tasks, engine: engine, recovery: recovery})
	})
}

// newTaskCreateCommand 构建 task create 子命令。
func newTaskCreateCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var (
		title          string
		description    string
		goal           string
		priority       string
		hostID         string
		repositoryURL  string
		baseBranch     string
		environment    string
		environmentVer string
		projectID      string
		caches         []string
		workDir        string
		commands       []string
		criteria       []string
		maxAttempts    int
		retryTimeout   bool
		retryVerify    bool
		retryNetwork   bool
		retryWait      int
		mergeAfterPush bool
		plannerRole    string
		developerRole  string
		validatorRole  string
		maxPlanSteps   int
		agentTimeout   int
		verifyTimeout  int
		idempotencyKey string
	)
	command := &cobra.Command{
		Use:   "create",
		Short: "校验配置并创建任务",
		Long: "所有必填配置在创建任务前校验：不会先创建容器再发现配置缺失。\n" +
			"--command 以「名称=可执行文件 参数...」形式声明必需验证命令，可重复。\n" +
			"--acceptance 声明任务级验收条件，可重复。\n" +
			"相同 --idempotency-key 重复提交不会创建第二个任务。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := buildVerificationSpec(commands, criteria)
			if err != nil {
				return err
			}
			request := app.CreateTaskRequest{
				Title:                      title,
				Description:                description,
				Goal:                       goal,
				Priority:                   domain.Priority(strings.TrimSpace(priority)),
				HostID:                     hostID,
				RepositoryURL:              repositoryURL,
				BaseBranch:                 baseBranch,
				Environment:                environment,
				EnvironmentVersion:         environmentVer,
				ProjectID:                  projectID,
				Caches:                     caches,
				WorkDir:                    workDir,
				Verification:               spec,
				Retry:                      buildRetryConfig(maxAttempts, retryTimeout, retryVerify, retryNetwork, retryWait),
				MergeAfterPush:             mergeAfterPush,
				PlannerRoleID:              plannerRole,
				DeveloperRoleID:            developerRole,
				ValidatorRoleID:            validatorRole,
				MaxPlanSteps:               maxPlanSteps,
				AgentTimeoutSeconds:        agentTimeout,
				VerificationTimeoutSeconds: verifyTimeout,
				IdempotencyKey:             idempotencyKey,
			}
			var created domain.Task
			err = withTaskServices(
				cmd.Context(),
				a,
				hostConfigPath(cmd),
				flags,
				hostID,
				func(ctx context.Context, services taskServices) error {
					task, createErr := services.tasks.CreateTask(ctx, request)
					created = task
					return createErr
				},
			)
			if err != nil {
				return err
			}
			return printTaskSummary(a, created)
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().StringVar(&title, "title", "", "需求标题（必填）")
	command.Flags().StringVar(&description, "description", "", "需求描述，省略时使用标题")
	command.Flags().StringVar(&goal, "goal", "", "任务目标描述，省略时使用需求描述")
	command.Flags().StringVar(&priority, "priority", "", "需求优先级：low、normal、high")
	command.Flags().StringVar(&hostID, "host", "", "执行节点标识（必填）")
	command.Flags().StringVar(&repositoryURL, "repo", "", "仓库地址，ssh://、https:// 或 host:path（必填）")
	command.Flags().StringVar(&baseBranch, "base", "", "基础分支（必填）")
	command.Flags().StringVar(&environment, "environment", "", "语言环境名称（必填）")
	command.Flags().StringVar(&environmentVer, "environment-version", "", "锁定的环境版本标签，例如 v0001")
	command.Flags().StringVar(&projectID, "project", "", "项目标识，用于项目级缓存共享")
	command.Flags().StringSliceVar(&caches, "cache", nil, "允许挂载的缓存类型，可重复")
	command.Flags().StringVar(&workDir, "workdir", "", "容器内项目目录，省略时使用默认目录")
	command.Flags().StringArrayVar(
		&commands,
		"command",
		nil,
		"必需验证命令，形式为「名称=可执行文件 参数...」，可重复",
	)
	command.Flags().StringArrayVar(&criteria, "acceptance", nil, "任务级验收条件，可重复")
	command.Flags().IntVar(&maxAttempts, "max-attempts", 0, "尝试次数上限（1-10），省略时使用默认值")
	command.Flags().BoolVar(&retryTimeout, "retry-timeout", false, "对超时失败自动重试")
	command.Flags().BoolVar(&retryVerify, "retry-verification", false, "对必需验证失败自动重试")
	command.Flags().BoolVar(&retryNetwork, "retry-network", false, "对可核验的网络类失败自动重试")
	command.Flags().IntVar(&retryWait, "retry-wait", 0, "两次尝试之间的等待秒数")
	command.Flags().BoolVar(
		&mergeAfterPush,
		"merge-after-push",
		false,
		"推送成功后合并基础分支（默认关闭）",
	)
	command.Flags().StringVar(&plannerRole, "planner-role", "", "规划角色标识（必填）")
	command.Flags().StringVar(&developerRole, "developer-role", "", "开发角色标识（必填）")
	command.Flags().StringVar(&validatorRole, "validator-role", "", "验证角色标识，省略时不做辅助评估")
	command.Flags().IntVar(&maxPlanSteps, "max-plan-steps", 0, "计划步骤数上限")
	command.Flags().IntVar(&agentTimeout, "agent-timeout", 0, "单次 Agent 调用超时秒数")
	command.Flags().IntVar(&verifyTimeout, "verify-timeout", 0, "单条验证命令默认超时秒数")
	command.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "幂等键，重复提交不会创建第二个任务")
	for _, required := range []string{
		"title", "host", "repo", "base", "environment", "planner-role", "developer-role",
	} {
		if err := command.MarkFlagRequired(required); err != nil {
			panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
		}
	}
	return command
}

// buildVerificationSpec 把命令行声明的命令转换为验证规格。
func buildVerificationSpec(commands, criteria []string) (domain.VerificationSpec, error) {
	spec := domain.VerificationSpec{AcceptanceCriteria: criteria}
	for _, raw := range commands {
		command, err := parseVerificationCommand(raw)
		if err != nil {
			return domain.VerificationSpec{}, err
		}
		spec.Commands = append(spec.Commands, command)
	}
	if err := spec.Validate(); err != nil {
		return domain.VerificationSpec{}, fmt.Errorf("验证命令配置非法: %w", err)
	}
	return spec, nil
}

// parseVerificationCommand 解析「名称=可执行文件 参数...」形式的命令声明。
//
// 命令与参数以空白分隔并逐项传给容器执行层，不经过 shell 解释。
func parseVerificationCommand(raw string) (domain.VerificationCommand, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return domain.VerificationCommand{}, fmt.Errorf("验证命令不能为空")
	}
	name, body, ok := strings.Cut(trimmed, "=")
	if !ok {
		return domain.VerificationCommand{}, fmt.Errorf(
			"验证命令 %q 缺少名称，请使用「名称=可执行文件 参数...」形式",
			trimmed,
		)
	}
	fields := strings.Fields(body)
	if len(fields) == 0 {
		return domain.VerificationCommand{}, fmt.Errorf("验证命令 %q 缺少可执行文件", trimmed)
	}
	command := domain.VerificationCommand{
		Name:     strings.TrimSpace(name),
		Command:  fields[0],
		Args:     fields[1:],
		Required: true,
	}
	if err := command.Validate(); err != nil {
		return domain.VerificationCommand{}, err
	}
	return command, nil
}

// buildRetryConfig 构造重试策略。
func buildRetryConfig(
	maxAttempts int,
	retryTimeout, retryVerification, retryNetwork bool,
	waitSeconds int,
) domain.RetryConfig {
	return domain.RetryConfig{
		MaxAttempts:       maxAttempts,
		RetryTimeout:      retryTimeout,
		RetryVerification: retryVerification,
		RetryNetwork:      retryNetwork,
		WaitSeconds:       waitSeconds,
	}
}

// newTaskRunCommand 构建 task run 子命令。
func newTaskRunCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var hostID string
	command := &cobra.Command{
		Use:   "run <task-id>",
		Short: "开始或继续推进一个任务",
		Long: "按阶段推进任务：准备容器 → 获取代码 → 规划 → 开发 → 确定性验证 →\n" +
			"辅助评估 → 代码保存 → 成功清理。\n" +
			"已结束或正在被推进的任务不会被重复执行。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var task domain.Task
			err := withTaskServices(
				cmd.Context(),
				a,
				hostConfigPath(cmd),
				flags,
				hostID,
				func(ctx context.Context, services taskServices) error {
					runErr := services.tasks.RunTask(ctx, args[0])
					current, getErr := services.tasks.GetTask(ctx, args[0])
					if getErr != nil {
						return runErr
					}
					task = current
					return runErr
				},
			)
			if printErr := printTaskSummary(a, task); printErr != nil {
				return printErr
			}
			return err
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().StringVar(&hostID, "host", "", "执行节点标识，用于收集 SSH 凭据")
	return command
}

// newTaskGetCommand 构建 task get 子命令。
func newTaskGetCommand(a *app.App) *cobra.Command {
	var hostID string
	command := &cobra.Command{
		Use:   "get <task-id>",
		Short: "展示任务状态、步骤、尝试、验证与代码保存结果",
		Long:  "该命令只读取数据库，不连接远程机器，也不会修改容器或仓库。",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var detail app.TaskDetail
			err := withTaskServices(
				cmd.Context(),
				a,
				hostConfigPath(cmd),
				credentialFlags{},
				hostID,
				func(ctx context.Context, services taskServices) error {
					loaded, getErr := services.tasks.Detail(ctx, args[0])
					detail = loaded
					return getErr
				},
			)
			if err != nil {
				return err
			}
			return printTaskDetail(a, detail)
		},
	}
	command.Flags().StringVar(&hostID, "host", "", "执行节点标识，用于收集 SSH 凭据")
	return command
}

// newTaskListCommand 构建 task list 子命令。
func newTaskListCommand(a *app.App) *cobra.Command {
	var (
		statuses []string
		hostID   string
		limit    int
	)
	command := &cobra.Command{
		Use:   "list",
		Short: "查询任务列表",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			filter := storage.TaskFilter{Limit: limit}
			for _, raw := range statuses {
				status := domain.TaskStatus(strings.TrimSpace(raw))
				if err := status.Validate(); err != nil {
					return fmt.Errorf("任务状态过滤非法: %w", err)
				}
				filter.Statuses = append(filter.Statuses, status)
			}
			if strings.TrimSpace(hostID) != "" {
				filter.HostID = domain.ID(strings.TrimSpace(hostID))
			}
			var tasks []domain.Task
			err := withTaskServices(
				cmd.Context(),
				a,
				hostConfigPath(cmd),
				credentialFlags{},
				"",
				func(ctx context.Context, services taskServices) error {
					loaded, listErr := services.tasks.ListTasks(ctx, filter)
					tasks = loaded
					return listErr
				},
			)
			if err != nil {
				return err
			}
			return printTaskList(a, tasks)
		},
	}
	command.Flags().StringSliceVar(&statuses, "status", nil, "按任务状态过滤，可重复")
	command.Flags().StringVar(&hostID, "host", "", "按执行节点过滤")
	command.Flags().IntVar(&limit, "limit", 0, "返回条数上限，0 表示不限制")
	return command
}

// newTaskCancelCommand 构建 task cancel 子命令。
func newTaskCancelCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "cancel <task-id>",
		Short: "请求取消任务并保留现场",
		Long: "取消请求先持久化，任务进入 cancelling 后收敛为 cancelled。\n" +
			"中断默认保留容器现场，不会自动删除诊断证据。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var task domain.Task
			err := withTaskServices(
				cmd.Context(),
				a,
				hostConfigPath(cmd),
				credentialFlags{},
				"",
				func(ctx context.Context, services taskServices) error {
					cancelled, cancelErr := services.tasks.CancelTask(ctx, args[0])
					task = cancelled
					return cancelErr
				},
			)
			if err != nil {
				return err
			}
			return printTaskSummary(a, task)
		},
	}
	return command
}

// newTaskRetryCommand 构建 task retry 子命令。
func newTaskRetryCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var (
		hostID      string
		disposition string
		runAfter    bool
	)
	command := &cobra.Command{
		Use:   "retry <task-id>",
		Short: "按既定策略重新开始失败任务",
		Long: "重新开始一定使用新容器：先按 --previous 处置旧容器，再创建新容器并重新获取代码。\n" +
			"--previous delete 删除旧容器；--previous detach 保留现场并脱离 AgentFlow 管理。\n" +
			"缺少 --previous 时直接拒绝：不会在未授权的情况下删除或脱离现场。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parsed, err := domain.ParseRestartDisposition(disposition)
			if err != nil {
				return fmt.Errorf("重试任务失败: %w", err)
			}
			var task domain.Task
			err = withTaskServices(
				cmd.Context(),
				a,
				hostConfigPath(cmd),
				flags,
				hostID,
				func(ctx context.Context, services taskServices) error {
					updated, retryErr := services.tasks.RetryTask(ctx, app.RetryTaskRequest{
						TaskID:              args[0],
						PreviousDisposition: parsed,
					})
					task = updated
					if retryErr != nil {
						return retryErr
					}
					if !runAfter {
						return nil
					}
					runErr := services.tasks.RunTask(ctx, args[0])
					latest, getErr := services.tasks.GetTask(ctx, args[0])
					if getErr == nil {
						task = latest
					}
					return runErr
				},
			)
			if printErr := printTaskSummary(a, task); printErr != nil {
				return printErr
			}
			return err
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().StringVar(&hostID, "host", "", "执行节点标识，用于收集 SSH 凭据")
	command.Flags().StringVar(
		&disposition,
		"previous",
		"",
		"旧容器处置方式：delete 或 detach（必填）",
	)
	command.Flags().BoolVar(&runAfter, "run", false, "处置旧容器后立即推进新执行")
	if err := command.MarkFlagRequired("previous"); err != nil {
		panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
	}
	return command
}

// newTaskRecoverCommand 构建 task recover 子命令。
func newTaskRecoverCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var hostID string
	command := &cobra.Command{
		Use:   "recover <task-id>",
		Short: "核验不确定状态并给出恢复建议",
		Long: "逐项核验容器、步骤、尝试、Session 与代码保存结果，不会盲目重放整个工作流。\n" +
			"无法确认的状态会保留为 recovery_required 并输出诊断信息。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var report workflow.RecoveryReport
			err := withTaskServices(
				cmd.Context(),
				a,
				hostConfigPath(cmd),
				flags,
				hostID,
				func(ctx context.Context, services taskServices) error {
					loaded, recoverErr := services.tasks.RecoverTask(ctx, args[0])
					report = loaded
					return recoverErr
				},
			)
			if printErr := printRecoveryReport(a, report); printErr != nil {
				return printErr
			}
			return err
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().StringVar(&hostID, "host", "", "执行节点标识，用于收集 SSH 凭据")
	return command
}

// printTaskSummary 输出任务状态摘要。
func printTaskSummary(a *app.App, task domain.Task) error {
	if task.ID.IsEmpty() {
		return nil
	}
	lines := []string{
		fmt.Sprintf("任务: %s", task.ID),
		fmt.Sprintf("状态: %s", task.Status),
		fmt.Sprintf("目标: %s", emptyAsUnknown(task.Config.Goal)),
		fmt.Sprintf("仓库: %s（基础分支 %s）", task.RepositoryPath, task.BaseRef),
		fmt.Sprintf("环境: %s %s", task.Config.Environment.Label(), task.Config.EnvironmentVersion),
		fmt.Sprintf("容器: %s", emptyAsUnknown(task.ContainerID)),
		fmt.Sprintf("当前步骤: %s", idOrNone(task.CurrentStepID)),
		fmt.Sprintf("当前尝试: %s", idOrNone(task.CurrentAttemptID)),
		fmt.Sprintf("取消请求: %s", flagMark(task.CancelRequested)),
		fmt.Sprintf("最终结果: %s", emptyAsUnknown(string(task.FinalResult))),
		fmt.Sprintf("创建时间: %s", task.CreatedAt.Format(timeFormat)),
		fmt.Sprintf("更新时间: %s", task.UpdatedAt.Format(timeFormat)),
	}
	if task.FailureSummary != "" {
		lines = append(lines, fmt.Sprintf("摘要: %s", task.FailureSummary))
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// printTaskDetail 输出任务完整执行历史。
func printTaskDetail(a *app.App, detail app.TaskDetail) error {
	var builder strings.Builder
	builder.WriteString(taskDetailHeader(detail))
	builder.WriteString("\n\n")
	builder.WriteString(taskDetailSteps(detail))
	if len(detail.CodeSaves) > 0 {
		builder.WriteString("\n\n代码保存:\n")
		for _, record := range detail.CodeSaves {
			builder.WriteString(fmt.Sprintf(
				"- 提交 %s，推送 %s，合并 %s",
				record.CommitStatus,
				record.PushStatus,
				record.MergeStatus,
			))
			if record.CommitSHA != "" {
				builder.WriteString("，commit " + record.CommitSHA)
			}
			if record.ErrorSummary != "" {
				builder.WriteString("，错误 " + record.ErrorSummary)
			}
			builder.WriteString("\n")
		}
	}
	if len(detail.Sessions) > 0 {
		builder.WriteString("\n会话:\n")
		for _, session := range detail.Sessions {
			builder.WriteString(fmt.Sprintf(
				"- %s 角色 %s，状态 %s，完整性 %s，调用 %s\n",
				session.ID,
				session.RoleSnapshot.Type,
				session.Status,
				emptyAsUnknown(string(session.Completeness)),
				emptyAsUnknown(session.InvocationID),
			))
		}
	}
	if len(detail.Containers) > 0 {
		builder.WriteString("\n容器:\n")
		for _, container := range detail.Containers {
			builder.WriteString(fmt.Sprintf(
				"- %s（Docker %s），状态 %s，脱离管理 %s\n",
				container.ID,
				emptyAsUnknown(container.DockerID),
				container.State,
				flagMark(container.Detached),
			))
		}
	}
	_, err := fmt.Fprint(a.Stdout(), builder.String())
	return err
}

// taskDetailHeader 生成任务详情头部。
func taskDetailHeader(detail app.TaskDetail) string {
	task := detail.Task
	lines := []string{
		fmt.Sprintf("任务: %s", task.ID),
		fmt.Sprintf("状态: %s", task.Status),
		fmt.Sprintf("目标: %s", emptyAsUnknown(task.Config.Goal)),
		fmt.Sprintf("最终结果: %s", emptyAsUnknown(string(task.FinalResult))),
	}
	if task.FailureSummary != "" {
		lines = append(lines, fmt.Sprintf("摘要: %s", task.FailureSummary))
	}
	return strings.Join(lines, "\n")
}

// taskDetailSteps 生成步骤与尝试详情。
func taskDetailSteps(detail app.TaskDetail) string {
	if len(detail.Steps) == 0 {
		return "步骤: 尚未生成"
	}
	attemptsByStep := make(map[domain.ID][]domain.Attempt, len(detail.Steps))
	for _, attempt := range detail.Attempts {
		attemptsByStep[attempt.StepID] = append(attemptsByStep[attempt.StepID], attempt)
	}
	var builder strings.Builder
	builder.WriteString("步骤:\n")
	for _, step := range detail.Steps {
		builder.WriteString(fmt.Sprintf(
			"- [%d] %s（%s）状态 %s，尝试上限 %d\n",
			step.Sequence,
			step.Title,
			step.Type,
			step.Status,
			step.MaxAttempts,
		))
		for _, attempt := range attemptsByStep[step.ID] {
			builder.WriteString(fmt.Sprintf(
				"  · 尝试 %d：状态 %s，验证 %s\n",
				attempt.AttemptNumber,
				attempt.Status,
				emptyAsUnknown(string(attempt.VerificationVerdict)),
			))
			if attempt.FailureSummary != "" {
				builder.WriteString("    失败摘要: " + attempt.FailureSummary + "\n")
			}
		}
	}
	return builder.String()
}

// printTaskList 输出任务列表。
func printTaskList(a *app.App, tasks []domain.Task) error {
	if len(tasks) == 0 {
		_, err := fmt.Fprintln(a.Stdout(), "没有匹配的任务")
		return err
	}
	rows := make([]string, 0, len(tasks))
	for _, task := range tasks {
		rows = append(rows, fmt.Sprintf(
			"%s  %-18s  %s  %s",
			task.ID,
			task.Status,
			task.UpdatedAt.Format(timeFormat),
			emptyAsUnknown(task.Config.Goal),
		))
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(rows, "\n"))
	return err
}

// printRecoveryReport 输出恢复核验结论。
func printRecoveryReport(a *app.App, report workflow.RecoveryReport) error {
	if report.Task.ID.IsEmpty() {
		return nil
	}
	lines := []string{
		fmt.Sprintf("任务: %s", report.Task.ID),
		fmt.Sprintf("状态: %s", report.Task.Status),
		fmt.Sprintf("容器: %s", report.ContainerState),
		fmt.Sprintf("代码保存: %s", report.CodeSaveState),
		fmt.Sprintf("未完成步骤: %d", len(report.UnfinishedSteps)),
		fmt.Sprintf("结果未知尝试: %d", len(report.UncertainAttempts)),
		fmt.Sprintf("缺失 Session: %d", len(report.MissingSessions)),
		fmt.Sprintf("需要人工处理: %s", flagMark(report.RecoveryRequired)),
		fmt.Sprintf("建议: %s", report.Recommendation),
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// idOrNone 把可空标识转换为可读文本。
func idOrNone(id *domain.ID) string {
	if id == nil {
		return "（无）"
	}
	return id.String()
}
