/**
 * 创建任务时"项目默认执行配置"的收起摘要。
 *
 * 仓库地址、基础分支与容器内工作目录都能从关联项目继承，因此表单默认收起这三个字段，
 * 只展示一行摘要。摘要必须给出**表单当前有效取值**，而不是项目记录里的原始取值：
 * 用户覆盖过仓库地址时显示项目地址等于把一份不会被提交的配置说成任务配置。
 *
 * 摘要只表达表单交互事实（是否由项目默认值写入、是否被用户改过），不推断服务端最终
 * 执行配置——服务端仍按 `firstNonBlank` 依次取"请求体非空值 → 项目配置"。
 */
import type { Project } from "@/lib/api";
import {
  PROJECT_TEXT_FIELDS,
  projectFieldSourceLabel,
  type ProjectPrefillField,
} from "@/pages/tasks/projectPrefill";

/** 必填字段没有取值时的展示文案 */
export const UNSET_FIELD_LABEL = "未填写，需要补充";

/** 可选字段没有取值时的展示文案：空值表示使用默认目录，不是"缺失" */
export const DEFAULT_WORK_DIR_LABEL = "未指定（使用默认目录）";

/** 尚未选择关联项目时的提示 */
export const NO_PROJECT_HINT =
  "尚未选择关联项目：仓库地址与基础分支需要手工填写，容器内工作目录留空时使用默认目录。";

/** 收起摘要中的一行 */
export type ProjectExecutionSummaryRow = {
  /** 对应的表单字段 */
  field: ProjectPrefillField;
  /** 字段名，与展开后的表单项标签保持一致 */
  label: string;
  /** 表单当前有效取值的展示文本 */
  value: string;
  /** 必填字段没有取值：界面要提示用户展开补充，而不是显示一个看似有效的继承配置 */
  missing: boolean;
  /** 取值来源说明；没有取值或没有项目时为空 */
  source?: string;
};

/** 字段的中文名 */
const FIELD_LABEL: Record<ProjectPrefillField, string> = {
  base_branch: "基础分支",
  environment: "语言环境",
  environment_version: "环境版本",
  repository_url: "仓库地址",
  work_dir: "容器内工作目录",
};

/** 生成收起状态下的执行配置摘要，行顺序固定为 `PROJECT_TEXT_FIELDS` */
export function projectExecutionSummary(input: {
  /** 表单当前取值，取值来源是表单本身 */
  current: Partial<Record<ProjectPrefillField, string | undefined>>;
  /** 关联项目；未选择时为 undefined，此时不给出任何"来自项目"的说明 */
  project?: Project;
  /** 当前取值由项目默认值写入的字段 */
  applied: readonly ProjectPrefillField[];
  /** 用户显式设置过取值的字段 */
  overridden: readonly ProjectPrefillField[];
}): ProjectExecutionSummaryRow[] {
  return PROJECT_TEXT_FIELDS.map((field) => {
    const value = input.current[field]?.trim();
    // 工作目录是可选项：空值表示使用服务端默认目录，不属于"缺失"。
    const missing = !value && field !== "work_dir";
    const fallback = field === "work_dir" ? DEFAULT_WORK_DIR_LABEL : UNSET_FIELD_LABEL;
    return {
      field,
      label: FIELD_LABEL[field],
      missing,
      source: value
        ? projectFieldSourceLabel({
            applied: input.applied.includes(field),
            overridden: input.overridden.includes(field),
            project: input.project,
          })
        : undefined,
      // 空字符串（清空输入框）与 undefined 一样表示"表单里没有值"。
      value: value && value.length > 0 ? value : fallback,
    };
  });
}
