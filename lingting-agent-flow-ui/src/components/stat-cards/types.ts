/**
 * 概览值卡片的展示数据。
 *
 * 这里的类型只描述"要显示什么"，不代表任何业务结论：服务端返回的取值原样传进来，
 * 前端不补造状态、不把未知取值改名成已知状态。
 */

/** 值卡片的语义色，只影响展示，不改变服务端返回的结论 */
export type StatCardTone = "default" | "success" | "warning" | "error";

/** 一张值卡片 */
export type StatCardItem = {
  /** 唯一键，用于渲染列表 */
  key: string;
  /** 指标名称 */
  label: string;
  /** 指标值；数字与文本都可以（例如"正常"或时间） */
  value: number | string;
  /** 值的语义色，缺省为中性色 */
  tone?: StatCardTone;
  /** 值的附加说明，显示在值下方 */
  hint?: string;
};

/** 分组的失败提示：标题说明结论，说明补充原因 */
export type StatCardsError = {
  /** 失败结论，例如"无法连接 AgentFlow 服务" */
  title: string;
  /** 具体原因，例如服务端返回的业务提示 */
  description?: string;
};
