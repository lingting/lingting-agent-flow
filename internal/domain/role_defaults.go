package domain

// DefaultRoleModel 是内置角色的默认模型标识。
//
// 模型值是占位符，不代表已验证的模型名称：真实模型由本机 Pi 配置，用户需要在设置页改成可用模型。
// 内置定义只提供默认值，运行期以数据库中保存的模型为准。
const DefaultRoleModel = "provider/model-id"

// DefaultRole 是内置固定角色的默认定义。
//
// 这里只描述默认值：角色运行配置以数据库为准，用户修改后重启不会回退到这些默认值。
type DefaultRole struct {
	ID           ID
	Name         string
	Type         RoleType
	Runner       string
	Model        string
	SystemPrompt string
}

// DefaultRoles 返回四个固定角色的内置默认定义。
//
// 顺序固定为需求分析师、方案规划师、开发者、验收者：方案规划师同时承担 V1 任务内部计划，
// 需求分析师是独立角色，不与方案规划师合并。
func DefaultRoles() []DefaultRole {
	return []DefaultRole{
		{
			ID:     RoleIDRequirementAnalyst,
			Name:   "需求分析师",
			Type:   RoleTypeRequirementAnalyst,
			Runner: RoleRunnerPi,
			Model:  DefaultRoleModel,
			SystemPrompt: "你是一名需求分析师。阅读用户需求与项目现状（代码、文档与配置），产出结构化的需求分析：" +
				"目标、范围、约束、验收标准与未知项。你只读取项目内容，不修改任何文件，也不执行写入类命令。",
		},
		{
			ID:     RoleIDPlanner,
			Name:   "方案规划师",
			Type:   RoleTypePlanner,
			Runner: RoleRunnerPi,
			Model:  DefaultRoleModel,
			SystemPrompt: "你是一名方案规划师。基于需求分析产出实施方案与任务拆分：明确步骤顺序、涉及文件、" +
				"风险与验收方式，并按既有计划输出格式给出结构化结果。你只读取项目内容，不修改任何文件。",
		},
		{
			ID:     RoleIDDeveloper,
			Name:   "开发者",
			Type:   RoleTypeDeveloper,
			Runner: RoleRunnerPi,
			Model:  DefaultRoleModel,
			SystemPrompt: "你是一名开发者。按指定的开发步骤在任务工作区内实现代码：保持改动范围最小，" +
				"遵循现有代码风格，不修改需求与验收标准，并在完成后说明改了什么以及还缺少什么。",
		},
		{
			ID:     RoleIDValidator,
			Name:   "验收者",
			Type:   RoleTypeValidator,
			Runner: RoleRunnerPi,
			Model:  DefaultRoleModel,
			SystemPrompt: "你是一名验收者。根据验收标准与确定性检查结果验证代码：只依据真实执行的证据收敛结论，" +
				"证据不足时如实给出未通过或不确定，不得声称未验证的内容已经通过。",
		},
	}
}
