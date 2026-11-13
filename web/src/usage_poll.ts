// 控制台里「按时间排队的轮询」的调度判定：额度面板（usage_poll）与 Provider
// 列表的排序都收在这里，让 web/src/lib.test.mjs 能直接测到真实代码（前端没有
// 测试运行器，见该文件说明）。

// ── Provider 额度面板轮询 ─────────────────────────────────────────────────

/** 一次请求该不该发。 */
export type UsagePollDecision =
  /** 现在就发。 */
  | 'fetch'
  /** 有弹窗压在上面（用户在编辑 Provider）：没人看的卡片不再打厂商。 */
  | 'skip-modal'
  /** 文档不在前台。 */
  | 'skip-hidden'
  /** 这个账号已经有一次在飞。 */
  | 'skip-pending'
  /** 距上次请求还不到间隔。 */
  | 'skip-not-due'

/**
 * shouldPollUsage 决定一次 tick 时某个账号要不要发额度请求。
 * 所有输入由调用方现算，函数没有副作用。
 *
 * 没有数据的账号不等间隔，下一次 tick 立刻取；但它仍然让位于弹窗与可见性——
 * 早先弹窗开着时背后的卡片照打厂商，就是「没有数据就立刻取」这一条没有闸。
 */
export function shouldPollUsage(input: {
  /** 当前请求键对应的数据已经取到（刚换了「今日」之类的查询时视为没取到）。 */
  fetched: boolean
  /** 上一次请求结案的时刻（毫秒时间戳）。 */
  lastFinishedAt: number | null
  now: number
  active: boolean
  modalOpen: boolean
  pending: boolean
  intervalMs: number
}): UsagePollDecision {
  if (input.modalOpen) return 'skip-modal';
  if (input.pending) return 'skip-pending';
  if (!input.fetched) return 'fetch';
  if (!input.active) return 'skip-hidden';
  if (input.lastFinishedAt != null && input.now - input.lastFinishedAt < input.intervalMs) return 'skip-not-due';
  return 'fetch';
}

/**
 * usagePollDelay 给下一个定时器排期：按「上次结案时刻 + 间隔」算。弹窗打开期间
 * 定时器照常跳、什么都不发，重排后也不会因为「刚跳过一次」再白等一个整间隔。
 */
export function usagePollDelay(now: number, lastFinishedAt: number | null, intervalMs: number): number {
  if (lastFinishedAt == null) return 0;
  const due = lastFinishedAt + intervalMs;
  return Math.max(0, due - now);
}

/** 额度请求的完整地址（路径 + 附加查询，「今日」等另取的数据就挂在这条请求上）。 */
export function usageRequestKey(path: string, query?: Record<string, string | undefined>): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query ?? {})) {
    if (value != null && value !== '') params.set(key, value);
  }
  const suffix = params.toString();
  if (!suffix) return path;
  return `${path}${path.includes('?') ? '&' : '?'}${suffix}`;
}

// ── 输入 Provider 列表的排序 ───────────────────────────────────────────────

/**
 * providerRankOrder 决定「今日 Token 用量」排序的一把快照：只在进入这一页的
 * 首次绘制、或用户点「刷新/获取模型」时按最新用量重排；后台轮询只更新数字、
 * 不动顺序。Token 用量每分钟都可能交叉，跟着轮询重排会让卡片在光标底下换位，
 * 点 ▸ 编辑 落到另一张卡上。
 */
export function providerRankOrder(input: {
  /** 上一次排好的顺序（这一页还没排过时为 null）。 */
  previous: string[] | null
  /** 这一页是否已经有可用的计数（统计请求回来之前不排）。 */
  ready: boolean
  /** 用户显式要求按最新计数重排。 */
  force: boolean
}): 'rank' | 'keep' | 'wait' {
  if (!input.ready) return 'wait';
  if (input.previous == null || input.force) return 'rank';
  return 'keep';
}

/**
 * Provider 排序使用的今日 Token：优先父网关下发的上游全量，父端尚未下发时
 * 回退到本机已经观测到的转发量。不能把“上游未知”当成 0，否则有本机用量的
 * peer Provider 会和完全没用的 Provider 同分，最后被名称顺序排到后面。
 *
 * 上游全量通常已经包含本机转发量；用 max 而不是相加，既避免重复计数，也能
 * 在父端快照暂时落后时继续使用较新的本机观测值。
 */
export function providerRankTokenTotal(localTotal: number, upstreamTotal?: number): number {
  const local = Number.isFinite(localTotal) ? Math.max(0, localTotal) : 0;
  const upstream = typeof upstreamTotal === 'number' && Number.isFinite(upstreamTotal)
    ? Math.max(0, upstreamTotal)
    : 0;
  return Math.max(local, upstream);
}

/** 按计数把当前 Provider 排成新顺序；再按这把顺序摆上一个快照里的已知 id。 */
export function rankProviderIDs(
  ids: string[],
  countOf: (id: string) => number,
  tieBreak: (id: string) => string,
): string[] {
  return [...ids].sort((a, b) => countOf(b) - countOf(a) || tieBreak(a).localeCompare(tieBreak(b)));
}

/** 把 known 快照按新鲜的顺序重排；快照里没有的（新加的 Provider）接在后面。 */
export function applyProviderOrder(known: string[], fresh: string[]): string[] {
  const position = new Map(fresh.map((id, index) => [id, index]));
  const extra = 0x7fffffff;
  return [...known].sort((a, b) => (position.get(a) ?? extra) - (position.get(b) ?? extra));
}
