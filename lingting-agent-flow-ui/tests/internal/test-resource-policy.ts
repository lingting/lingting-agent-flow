/**
 * 前端测试并发策略的唯一定义处。
 *
 * Playwright 与 Vitest 的配置、单元测试都从这里取值，避免两处各写一套默认值。
 * 这里只放常量与纯函数：不做 IO，不启动进程，也不涉及 CPU 配额。
 *
 * 关于资源模型，必须区分两个层次：
 * - dev server 只有一份：Playwright 的 `webServer` 在一次运行中只启动一个 Vite 进程，
 *   全部 worker 共享同一个 baseURL（这是 Playwright 的原生行为，不是本项目的实现）。
 * - 真正的 CPU 开销来自无头浏览器进程数，而浏览器进程数等于 worker 数，
 *   因此限制 worker 就是限制 CPU 峰值；没有额外的进程级配额机制。
 */

/** Playwright 默认 worker 数：每个 worker 一个无头浏览器实例。 */
export const DEFAULT_PLAYWRIGHT_WORKERS = 4;

/** Vitest 默认 worker 上限：Vitest 默认取逻辑 CPU 数，18 核机器会一次起 18 个 fork。 */
export const DEFAULT_VITEST_MAX_WORKERS = 4;

/**
 * worker 数允许的上限。
 *
 * 超过上限的输入一律视为非法并报错，不做静默截断，也不回退到更高并发。
 */
export const MAX_WORKERS = 8;

export const VITEST_WORKERS_ENV = "AGENTFLOW_TEST_VITEST_WORKERS";
export const PLAYWRIGHT_WORKERS_ENV = "AGENTFLOW_TEST_PLAYWRIGHT_WORKERS";

/**
 * 解析 worker 数环境变量。
 *
 * 只接受十进制正整数并限制在 1..MAX_WORKERS：百分比、小数、0 与负数都视为非法输入，
 * 直接抛错，不静默回退到默认值或更高并发。空值表示使用 fallback。
 */
export function parseWorkerCount(options: {
  raw: string | undefined;
  fallback: number;
  envName: string;
}): number {
  const value = (options.raw ?? "").trim();
  if (value === "") {
    return options.fallback;
  }
  if (!/^[0-9]+$/.test(value)) {
    throw new Error(
      `${options.envName} 必须是 1-${MAX_WORKERS} 的十进制整数（当前值："${value}"）；` +
        "不接受百分比与小数，以免实际并发与配置不一致。",
    );
  }
  const parsed = Number(value);
  if (parsed < 1 || parsed > MAX_WORKERS) {
    throw new Error(
      `${options.envName} 必须在 1-${MAX_WORKERS} 之间（当前值："${value}"）；` +
        "需要更高并发时必须显式调整策略上限，而不是绕过校验。",
    );
  }
  return parsed;
}

/** Playwright 生效的 worker 数。 */
export function resolvePlaywrightWorkers(env: NodeJS.ProcessEnv = process.env): number {
  return parseWorkerCount({
    raw: env[PLAYWRIGHT_WORKERS_ENV],
    fallback: DEFAULT_PLAYWRIGHT_WORKERS,
    envName: PLAYWRIGHT_WORKERS_ENV,
  });
}

/** Vitest 生效的 worker 数。 */
export function resolveVitestWorkers(env: NodeJS.ProcessEnv = process.env): number {
  return parseWorkerCount({
    raw: env[VITEST_WORKERS_ENV],
    fallback: DEFAULT_VITEST_MAX_WORKERS,
    envName: VITEST_WORKERS_ENV,
  });
}
