package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/spf13/cobra"
)

// newTaskCommand 构建 task 命令组。
func newTaskCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "task",
		Short: "创建、执行、查询与取消任务",
		Long: "任务执行由 agentflow server 内的任务级工作流编排器负责，CLI 只做管理与查询。\n" +
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
	command.AddCommand(newTaskStartCommand(a))
	command.AddCommand(newTaskPlanCommand(a))
	command.AddCommand(newTaskRunCommand(a))
	command.AddCommand(newTaskGetCommand(a))
	command.AddCommand(newTaskListCommand(a))
	command.AddCommand(newTaskCancelCommand(a))
	command.AddCommand(newTaskReopenStepCommand(a))
	command.AddCommand(newTaskResumeStatusCommand(a))
	return command
}

// taskServices 是一次任务命令执行所需的依赖。
//
// 命令层只持有任务管理服务：任务执行由 agentflow server 内的任务级工作流编排器
// 负责，CLI 不装配 SSH、容器、Pi 与代码保存能力。
type taskServices struct {
	tasks      *app.TaskService
	containers *app.ContainerService
	store      storage.Store
}

// withTaskServices 加载配置并构造任务管理服务。
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
		tasks, err := app.NewTaskService(app.TaskServiceOptions{
			Store:  services.store,
			Logger: a.Logger().Slog(),
		})
		if err != nil {
			return err
		}
		return run(ctx, taskServices{
			tasks:      tasks,
			containers: services.containers,
			store:      services.store,
		})
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
//
// 任务执行由 agentflow server 内的任务级工作流编排器负责：本命令只发起一次执行
// 请求，并按需轮询真实进度，不再自行装配 SSH、容器、Pi 与代码保存能力。
func newTaskRunCommand(a *app.App) *cobra.Command {
	var (
		address       string
		prompt        string
		timeout       string
		verifyTimeout string
		wait          bool
		interval      time.Duration
	)
	command := &cobra.Command{
		Use:   "run <task-id>",
		Short: "通过本地服务启动一次任务级工作流执行",
		Long: "向本地 API 发起 POST /api/v1/tasks/{taskID}/execute-workflow，由服务内的\n" +
			"任务级工作流编排器按已持久化的计划顺序执行开发与验证步骤。\n" +
			"该命令不生成计划、不自动重试、不自动删除容器；失败或阻塞时停止并保留现场。\n" +
			"服务地址默认取配置文件的 server 段，可用 --address 覆盖。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.ValidateConfig(cmd.Context(), hostConfigPath(cmd))
			if err != nil {
				return err
			}
			target := strings.TrimSpace(address)
			if target == "" {
				target = cfg.Server.Address()
			}
			devTimeout, err := parseOptionalDuration(timeout)
			if err != nil {
				return err
			}
			validationTimeout, err := parseOptionalDuration(verifyTimeout)
			if err != nil {
				return err
			}
			if interval <= 0 {
				return fmt.Errorf("轮询间隔必须大于 0: %s", interval)
			}
			client := newWorkflowAPIClient(target, a.Logger().Slog())
			ctx := cmd.Context()
			run, err := client.start(ctx, args[0], workflowAPIStartRequest{
				Prompt:                   strings.TrimSpace(prompt),
				TimeoutSeconds:           int(devTimeout.Seconds()),
				ValidationTimeoutSeconds: int(validationTimeout.Seconds()),
			})
			if err != nil {
				return err
			}
			if err := printWorkflowRun(a, run); err != nil {
				return err
			}
			if !wait || workflowRunStopped(run) {
				return nil
			}
			final, waitErr := client.wait(ctx, args[0], interval, a)
			if final.RunID == "" {
				return waitErr
			}
			if printErr := printWorkflowRun(a, final); printErr != nil {
				return printErr
			}
			return waitErr
		},
	}
	command.Flags().StringVar(&address, "address", "", "服务地址，省略时使用配置文件的 server 段")
	command.Flags().StringVar(&prompt, "prompt", "", "追加在每一步骤提示词之后的补充说明")
	command.Flags().StringVar(&timeout, "timeout", "", "单个步骤开发阶段的超时时间，例如 30m")
	command.Flags().StringVar(&verifyTimeout, "verify-timeout", "", "单个步骤验证阶段的超时时间，例如 10m")
	command.Flags().BoolVar(&wait, "wait", true, "轮询真实进度直到执行收敛")
	command.Flags().DurationVar(&interval, "interval", 2*time.Second, "轮询间隔")
	return command
}

// newTaskStartCommand 构建 task start 子命令。
//
// 它是任务执行的推荐入口：服务端会依据数据库事实决定是否建容器、是否生成计划，
// 然后启动工作流。命令本身只是 HTTP 客户端，不装配 SSH、容器、Pi 或代码保存能力。
func newTaskStartCommand(a *app.App) *cobra.Command {
	var (
		address       string
		prompt        string
		timeout       string
		verifyTimeout string
		planTimeout   string
		wait          bool
		interval      time.Duration
	)
	command := &cobra.Command{
		Use:   "start <task-id>",
		Short: "通过本地服务完成一次任务启动（准备环境 → 初始化工作区 → 生成计划 → 启动工作流）",
		Long: "向本地 API 发起 POST /api/v1/tasks/{taskID}/start，由服务端按数据库事实决定：\n" +
			"  1. 任务已有受管理且运行中的容器时不会重复创建；\n" +
			"  2. 任务容器内的工作区尚未就绪时先完成代码获取与 agent/<task-id> 分支准备；\n" +
			"  3. 任务还没有持久化计划时在容器内调用 Planner 并一次写入数据库；\n" +
			"  4. 计划就绪后启动任务级工作流执行。\n" +
			"任一阶段失败立即停止，不自动重试、不删除容器、不重放外部操作；\n" +
			"工作区初始化结果无法确认时返回状态冲突，必须先核验容器内实际状态；\n" +
			"失败与中断一律保留容器现场。服务地址默认取配置文件的 server 段。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.ValidateConfig(cmd.Context(), hostConfigPath(cmd))
			if err != nil {
				return err
			}
			target := strings.TrimSpace(address)
			if target == "" {
				target = cfg.Server.Address()
			}
			devTimeout, err := parseOptionalDuration(timeout)
			if err != nil {
				return err
			}
			validationTimeout, err := parseOptionalDuration(verifyTimeout)
			if err != nil {
				return err
			}
			planCallTimeout, err := parseOptionalDuration(planTimeout)
			if err != nil {
				return err
			}
			if interval <= 0 {
				return fmt.Errorf("轮询间隔必须大于 0: %s", interval)
			}
			client := newWorkflowAPIClient(target, a.Logger().Slog())
			ctx := cmd.Context()
			result, err := client.startTask(ctx, args[0], workflowAPIStartTaskRequest{
				Prompt:                   strings.TrimSpace(prompt),
				TimeoutSeconds:           int(devTimeout.Seconds()),
				ValidationTimeoutSeconds: int(validationTimeout.Seconds()),
				PlanTimeoutSeconds:       int(planCallTimeout.Seconds()),
			})
			if err != nil {
				return err
			}
			if err := printStartTaskResult(a, result); err != nil {
				return err
			}
			if !wait || result.Run == nil || workflowRunStopped(*result.Run) {
				return nil
			}
			final, waitErr := client.wait(ctx, args[0], interval, a)
			if final.RunID == "" {
				return waitErr
			}
			if printErr := printWorkflowRun(a, final); printErr != nil {
				return printErr
			}
			return waitErr
		},
	}
	command.Flags().StringVar(&address, "address", "", "服务地址，省略时使用配置文件的 server 段")
	command.Flags().StringVar(&prompt, "prompt", "", "追加在规划与每一步骤提示词之后的补充说明")
	command.Flags().StringVar(&timeout, "timeout", "", "单个步骤开发阶段的超时时间，例如 30m")
	command.Flags().StringVar(&verifyTimeout, "verify-timeout", "", "单个步骤验证阶段的超时时间，例如 10m")
	command.Flags().StringVar(&planTimeout, "plan-timeout", "", "本次规划调用的超时时间，例如 10m")
	command.Flags().BoolVar(&wait, "wait", true, "启动后轮询真实进度直到执行收敛")
	command.Flags().DurationVar(&interval, "interval", 2*time.Second, "轮询间隔")
	return command
}

// newTaskPlanCommand 构建 task plan 子命令。
//
// 计划生成由 agentflow server 内的任务级编排器负责：本命令只发起一次请求并报告
// 真实结果，不装配 SSH、容器、Pi，也不自行拼接提示词。
func newTaskPlanCommand(a *app.App) *cobra.Command {
	var (
		address string
		prompt  string
		timeout string
	)
	command := &cobra.Command{
		Use:   "plan <task-id>",
		Short: "通过本地服务在任务容器内生成并持久化计划",
		Long: "向本地 API 发起 POST /api/v1/tasks/{taskID}/plan，由服务内的任务级编排器\n" +
			"调用 Planner 并把输出校验为结构化计划后一次写入数据库。\n" +
			"该命令不创建容器、不自动重试、不覆盖已有计划；任务缺少受管理容器或已有计划时\n" +
			"直接返回错误。服务地址默认取配置文件的 server 段，可用 --address 覆盖。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.ValidateConfig(cmd.Context(), hostConfigPath(cmd))
			if err != nil {
				return err
			}
			target := strings.TrimSpace(address)
			if target == "" {
				target = cfg.Server.Address()
			}
			planTimeout, err := parseOptionalDuration(timeout)
			if err != nil {
				return err
			}
			client := newWorkflowAPIClient(target, a.Logger().Slog())
			plan, err := client.generatePlan(cmd.Context(), args[0], workflowAPIPlanRequest{
				Prompt:         strings.TrimSpace(prompt),
				TimeoutSeconds: int(planTimeout.Seconds()),
			})
			if err != nil {
				return err
			}
			return printPlanResult(a, plan)
		},
	}
	command.Flags().StringVar(&address, "address", "", "服务地址，省略时使用配置文件的 server 段")
	command.Flags().StringVar(&prompt, "prompt", "", "追加在规划提示词之后的补充说明")
	command.Flags().StringVar(&timeout, "timeout", "", "本次规划调用的超时时间，例如 10m")
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

// idOrNone 把可空标识转换为可读文本。
func idOrNone(id *domain.ID) string {
	if id == nil {
		return "（无）"
	}
	return id.String()
}

// newTaskReopenStepCommand 构建 task reopen-step 子命令。
//
// 它是"用户显式开启新的执行周期"的正式入口：只有上一周期已安全收敛且预算已用尽
// （或重试策略不再允许修复）时才允许开启，开启后该步骤获得一份新的预算。
// 命令本身不执行任何 Agent 调用，执行仍需走 execute-and-validate / execute-workflow。
func newTaskReopenStepCommand(a *app.App) *cobra.Command {
	var address string
	command := &cobra.Command{
		Use:   "reopen-step <task-id> <step-id>",
		Short: "为步骤显式开启新的执行周期（重置修复预算）",
		Long: "向本地 API 发起 POST /api/v1/tasks/{taskID}/steps/{stepID}/open-repair-cycle。\n" +
			"只有上一执行周期已安全收敛、且预算已用尽或重试策略不再允许修复时才允许开启；\n" +
			"上一周期仍有剩余预算、存在结果不确定的执行尝试或周期归属不明时会被拒绝。\n" +
			"该命令不执行任何 Agent 调用，也不代表验证会通过。",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.ValidateConfig(cmd.Context(), hostConfigPath(cmd))
			if err != nil {
				return err
			}
			target := strings.TrimSpace(address)
			if target == "" {
				target = cfg.Server.Address()
			}
			client := newWorkflowAPIClient(target, a.Logger().Slog())
			result, err := client.openRepairCycle(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			return printOpenRepairCycle(a, result)
		},
	}
	command.Flags().StringVar(&address, "address", "", "服务地址，省略时使用配置文件的 server 段")
	return command
}

// printOpenRepairCycle 输出周期开启结果。
//
// 输出只包含周期登记与预算事实：命令不会把它描述成"执行已完成"或"验证已通过"。
func printOpenRepairCycle(a *app.App, result workflowAPIOpenRepairCycle) error {
	lines := []string{
		"已开启新的执行周期",
		"任务: " + result.TaskID,
		"步骤: " + result.StepID,
		"周期: " + result.CycleID + "（第 " + strconv.Itoa(result.CycleSequence) + " 个周期）",
		"尝试上限: " + strconv.Itoa(result.MaxAttempts),
		"上一周期已消耗: " + strconv.Itoa(result.PreviousConsumed),
		"开启时间: " + result.OpenedAt.UTC().Format(time.RFC3339),
		"注意: 该操作只重置修复预算，不执行开发调用，也不代表验证会通过",
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, newlineSeparator))
	return err
}

// newTaskResumeStatusCommand 构建 task resume-status 子命令。
//
// 它回答"这个任务现在能不能继续、为什么不能、下一步该做什么"。此前这些信息分散在
// resume-workflow 的错误信息与 code status 的输出里，用户必须自己拼接。
func newTaskResumeStatusCommand(a *app.App) *cobra.Command {
	var hostID string
	command := &cobra.Command{
		Use:   "resume-status <task-id>",
		Short: "查询任务能否继续推进，以及不能继续时的下一步操作",
		Long: "只读查询：不推进工作流、不执行代码保存核验、不修改任何记录。\n" +
			"结论复用工作流与代码保存的既有判定规则，输出阻塞分类与可直接执行的下一步命令。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withTaskServices(
				cmd.Context(),
				a,
				hostConfigPath(cmd),
				credentialFlags{},
				hostID,
				func(ctx context.Context, services taskServices) error {
					eligibility, newErr := app.NewResumeEligibilityService(
						app.ResumeEligibilityOptions{
							Store:      services.store,
							Containers: services.containers,
						},
					)
					if newErr != nil {
						return newErr
					}
					result, getErr := eligibility.Get(ctx, args[0])
					if printErr := printResumeEligibility(a, result); printErr != nil {
						return printErr
					}
					return getErr
				},
			)
		},
	}
	command.Flags().StringVar(&hostID, "host", "", "执行节点标识，用于收集 SSH 凭据")
	return command
}

// printResumeEligibility 输出续跑资格结论。
func printResumeEligibility(a *app.App, result app.ResumeEligibility) error {
	if result.TaskID.IsEmpty() {
		return nil
	}
	lines := []string{
		"任务: " + result.TaskID.String(),
		"任务状态: " + string(result.TaskStatus),
	}
	if result.Run != nil {
		lines = append(lines,
			"执行记录: "+result.Run.RunID.String()+" ("+string(result.Run.Status)+")",
		)
		if result.Run.ErrorClass != "" {
			lines = append(lines, "停止分类: "+result.Run.ErrorClass)
		}
	} else {
		lines = append(lines, "执行记录: 无")
	}
	if result.CanResume {
		lines = append(lines, "能否继续: 可以")
	} else {
		lines = append(lines, "能否继续: 不可以")
		lines = append(lines, "阻塞项: "+strings.Join(result.Blocking, ", "))
	}
	lines = append(lines, "代码保存结论: "+string(result.CodeSave.Action)+
		"（"+result.CodeSave.Summary+"）")
	if len(result.CodeSaveRecords) > 0 {
		lines = append(lines, "需要处理的代码保存记录:")
		for _, record := range result.CodeSaveRecords {
			lines = append(lines, "  - "+record.ID.String()+
				" 提交="+record.CommitStatus.String()+
				" 推送="+record.PushStatus.String()+
				" 合并="+record.MergeStatus.String()+
				" 记录可信="+record.PersistStatus.Normalized().String()+
				" 核验="+record.RecoveryStatus.String())
		}
	}
	if len(result.NextActions) > 0 {
		lines = append(lines, "下一步:")
		for _, action := range result.NextActions {
			lines = append(lines, "  - "+action)
		}
	}
	lines = append(lines, "说明: "+result.Summary)
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}
