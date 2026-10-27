/**
 * 任务快照的请求票据。
 *
 * 任务详情、工作流进度、手动刷新与自动刷新会并发发起请求，Promise 的完成顺序并不等于
 * 发起顺序。这里用"序号 + 任务标识"给每次请求打票据：
 *   - 只有最后一次发起、并且仍然属于当前详情任务的响应才会被采用；
 *   - 切换任务、关闭详情或请求被取消时，迟到的响应一律丢弃，不会覆盖更新的数据。
 *
 * 票据与判定都是纯数据 / 纯函数：可以被直接断言，不依赖 React 或浏览器环境。
 */

/** 某一类请求的票据计数器 */
export type RequestGate = { requestID: number };

/** 一次请求的票据：记录发起时的序号与归属任务 */
export type RequestTicket = { requestID: number; taskID: string };

/** 新建一个票据计数器 */
export function createRequestGate(): RequestGate {
  return { requestID: 0 };
}

/** 发起一次请求：序号递增，返回本次票据 */
export function beginRequest(gate: RequestGate, taskID: string): RequestTicket {
  gate.requestID += 1;
  return { requestID: gate.requestID, taskID };
}

/**
 * 判断一次响应是否仍然有效。
 *
 * 三个条件都必须成立：请求没有被人为取消、它仍是最后一次发起的请求、它描述的仍是
 * 当前打开的详情任务。任一不成立都说明响应已过期，必须丢弃。
 */
export function acceptResponse(
  gate: RequestGate,
  ticket: RequestTicket,
  activeTaskID: string | undefined,
  aborted: boolean,
): boolean {
  if (aborted) return false;
  if (gate.requestID !== ticket.requestID) return false;
  return (activeTaskID ?? "") === ticket.taskID;
}

/**
 * 刷新失败提示。
 *
 * 文案只说明"这次刷新失败、展示的是上一次快照"，不把刷新失败表达成任务失败或工作流终止，
 * 也不省略仍然有效的业务事实。
 */
export function describeRefreshFailures(failures: string[]): string | undefined {
  if (failures.length === 0) return undefined;
  return `刷新失败：${failures.join("、")}。下方展示的是上一次成功获取的状态，可能不是最新。`;
}
