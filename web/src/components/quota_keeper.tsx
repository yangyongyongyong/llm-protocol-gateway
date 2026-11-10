import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { API_BASE, protocolLabel } from '../lib';
import type { BadgeTone, Provider, QuotaPreviewFire, ScheduledTask, ScheduledTaskRun, ScheduleMode } from '../types';
import type { MonitorQuota } from '../monitoring/types';
import { quotaWindowLabel } from '../monitoring/format';
import { MultiSelectFilter } from './selects';
import { ProviderLabel, ProviderRefLabel, providerIcon } from './provider_label';
import { Badge, CheckboxField, Field, Modal, MoreMenu } from './ui';
import { confirmDialog } from '../dialogs';

/**
 * 额度保活面板。
 *
 * 为什么要这个东西：Claude 这类订阅的 5 小时窗口只在「真的有请求进来」时才起算。
 * 一天没聊，窗口就不走；第二天第一次请求还落在昨天的窗口里，一天的额度要在一个
 * 下午烧完。定时发一发极短的对话，把窗口钉在用户挑的小时上，之后的重置时间就
 * 能推出来（09:00 → 14:00 → 19:00）。
 */

const WINDOW_HOURS = 5;
const POLL_TICKS = 15;

/** 浏览器自己的 IANA 时区，取不到时退回 UTC 偏移写法。 */
function browserZone(): string {
  try {
    const name = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (name) return name;
  } catch {
    // 老浏览器没有 resolvedOptions，交给下面的偏移兜底。
  }
  return `UTC${-new Date().getTimezoneOffset() / 60 >= 0 ? '+' : '-'}${Math.abs(-new Date().getTimezoneOffset() / 60)}`;
}

const LOCAL_ZONE = browserZone();

/** 常用时区兜底列出；列表里没有的值也不会被 select 吞掉（见 zoneOptions）。 */
const COMMON_ZONES = ['Asia/Shanghai', 'Asia/Singapore', 'Asia/Tokyo', 'Europe/London', 'America/New_York', 'America/Los_Angeles'];

/** 按指定时区格式化时刻；时区名不认识时退回浏览器本地，绝不整页报错。 */
function formatInZone(stamp: string | undefined, zone: string | undefined, options: Intl.DateTimeFormatOptions) {
  if (!stamp) return '';
  const date = new Date(stamp);
  if (Number.isNaN(date.getTime())) return '';
  try {
    return new Intl.DateTimeFormat('zh-CN', { ...options, timeZone: zone || undefined, hourCycle: 'h23' }).format(date);
  } catch {
    return new Intl.DateTimeFormat('zh-CN', options).format(date);
  }
}

type EditorSession = { draft: ScheduledTask };

const MODE_LABELS: Record<ScheduleMode, string> = {
  daily_times: '每天定点',
  every_hours: '每 N 小时滚动',
  every_minutes: '每 N 分钟滚动',
};

const MODE_HINTS: Record<ScheduleMode, string> = {
  daily_times: '在你列的每个时刻各触发一次，天天重复。',
  every_hours: `从锚点开始每 N 小时一次，正好复刻上游 ${WINDOW_HOURS} 小时的滚动窗口。`,
  every_minutes: '从锚点开始每 N 分钟一次，适合临时盯额度或做压测。',
};

const QUIET_OPTIONS: Array<{ label: string; value: number }> = [
  { label: '不跳过（到点就发）', value: -1 },
  { label: '15 分钟', value: 15 },
  { label: '30 分钟', value: 30 },
  { label: '60 分钟', value: 60 },
  { label: '120 分钟', value: 120 },
  { label: '180 分钟', value: 180 },
  { label: '360 分钟', value: 360 },
];

function emptyDraft(): ScheduledTask {
  return {
    id: '',
    name: '',
    enabled: true,
    mode: 'daily_times',
    timeZone: LOCAL_ZONE,
    times: ['09:00'],
    interval: 5,
    providerIds: [],
    modelOverrides: {},
    quietMinutes: 60,
    probePrompt: 'ping',
  };
}

/** 时区名可能为空（= 服务器当地），也可能浏览器 Intl 不认；两种情况都退回本地。 */
function clockIn(stamp: string | undefined, zone?: string) {
  return formatInZone(stamp, zone, { hour: '2-digit', minute: '2-digit' });
}

/** 今天/明天的相对说法要按"那个时区的今天"算，不能按浏览器的今天。 */
function dayClockIn(stamp: string | undefined, zone?: string) {
  if (!stamp) return '';
  const clock = clockIn(stamp, zone);
  if (!clock) return '';
  const day = formatInZone(stamp, zone, { month: '2-digit', day: '2-digit' });
  const today = formatInZone(new Date().toISOString(), zone, { month: '2-digit', day: '2-digit' });
  const tomorrow = formatInZone(new Date(Date.now() + 86400000).toISOString(), zone, { month: '2-digit', day: '2-digit' });
  if (day === today) return `今天 ${clock}`;
  if (day === tomorrow) return `明天 ${clock}`;
  return `${day} ${clock}`;
}

function countdown(stamp: string | undefined, now: number) {
  if (!stamp) return '';
  const target = new Date(stamp).getTime();
  if (Number.isNaN(target)) return '';
  const delta = target - now;
  if (delta <= 0) return '即将触发';
  const totalMinutes = Math.floor(delta / 60000);
  const seconds = Math.floor((delta % 60000) / 1000);
  if (totalMinutes >= 60) return `${Math.floor(totalMinutes / 60)} 小时 ${totalMinutes % 60} 分后`;
  if (totalMinutes >= 1) return `${totalMinutes} 分 ${seconds} 秒后`;
  return `${seconds} 秒后`;
}

/** "09:00" + 5 小时 → "14:00"；只报时刻，日期由同一行的日期字段给出。 */
function shiftClock(clock: string, hours: number) {
  const [rawHour, rawMinute] = clock.split(':');
  const hour = Number(rawHour);
  const minute = Number(rawMinute);
  if (Number.isNaN(hour) || Number.isNaN(minute)) return '';
  const total = (hour * 60 + minute + hours * 60) % 1440;
  return `${String(Math.floor(total / 60)).padStart(2, '0')}:${String(total % 60).padStart(2, '0')}`;
}

function normalizeClock(raw: string): string {
  const match = /^(\d{1,2}):(\d{1,2})$/.exec(raw.trim());
  if (!match) return '';
  const hour = Number(match[1]);
  const minute = Number(match[2]);
  if (hour > 23 || minute > 59) return '';
  return `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`;
}

/** 从最早的时间点起按窗口长度铺满一天，省得用户自己算 09→14→19→00→05。 */
function windowChain(times: string[]): string[] {
  const first = normalizeClock([...(times || [])].sort()[0] || '') || '09:00';
  const [hour, minute] = first.split(':').map(Number);
  const out: string[] = [];
  for (let step = 0; step < Math.ceil(24 / WINDOW_HOURS); step += 1) {
    const total = ((hour + step * WINDOW_HOURS) % 24) * 60 + minute;
    out.push(`${String(Math.floor(total / 60)).padStart(2, '0')}:${String(total % 60).padStart(2, '0')}`);
  }
  return out.sort();
}

/** 探测可选的模型：菜单里的模型，外加 provider 自己的默认模型。 */
function modelsForProbe(provider: Provider): string[] {
  const ids = (provider.models || []).filter((model) => model.inMenu).map((model) => model.id);
  const fallback = provider.defaultModel;
  if (fallback && !ids.includes(fallback)) ids.push(fallback);
  return ids;
}

function summarize(runs: ScheduledTaskRun[]) {
  let fired = 0;
  let skipped = 0;
  let failed = 0;
  runs.forEach((run) => {
    if (run.outcome === 'fired') fired += 1;
    else if (run.outcome === 'skipped') skipped += 1;
    else failed += 1;
  });
  return { fired, skipped, failed };
}

function outcomeTone(outcome: ScheduledTaskRun['outcome']): BadgeTone {
  if (outcome === 'fired') return 'green';
  if (outcome === 'skipped') return 'slate';
  return 'red';
}

function outcomeLabel(outcome: ScheduledTaskRun['outcome']) {
  if (outcome === 'fired') return '已触发';
  if (outcome === 'skipped') return '已跳过';
  return '失败';
}

async function readError(response: Response) {
  const payload = (await response.json().catch(() => null)) as { error?: { message?: string } | string } | null;
  if (typeof payload === 'string') return payload;
  const nested = payload && typeof payload.error === 'object' ? payload.error.message : '';
  if (typeof payload?.error === 'string') return payload.error;
  return nested || `HTTP ${response.status}`;
}

export function QuotaKeeperPanel({ providers, showOwner, canManage }: { providers: Provider[]; showOwner: boolean; canManage: boolean }) {
  const [tasks, setTasks] = useState<ScheduledTask[]>([]);
  const [recentRuns, setRecentRuns] = useState<ScheduledTaskRun[]>([]);
  const [providerQuota, setProviderQuota] = useState<Record<string, MonitorQuota>>({});
  const [ownerNames, setOwnerNames] = useState<Record<string, string>>({});
  const [serverZone, setServerZone] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [editor, setEditor] = useState<EditorSession | null>(null);
  const [busyId, setBusyId] = useState('');
  const [expanded, setExpanded] = useState('');
  const [now, setNow] = useState(() => Date.now());

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const response = await fetch(`${API_BASE}/__quota-tasks`);
      if (!response.ok) throw new Error(await readError(response));
      const payload = (await response.json()) as { tasks?: ScheduledTask[]; recentRuns?: ScheduledTaskRun[]; providerQuota?: Record<string, MonitorQuota>; ownerNames?: Record<string, string>; serverTimeZone?: string };
      // 被保活暂停的任务没有 Provider 了；这里统一成数组，卡片直接按数组用。
      setTasks((payload.tasks || []).map((task) => ({ ...task, providerIds: task.providerIds || [] })));
      setRecentRuns(payload.recentRuns || []);
      setProviderQuota(payload.providerQuota || {});
      setOwnerNames(payload.ownerNames || {});
      setServerZone(payload.serverTimeZone || '');
      setError('');
    } catch (cause) {
      if (!silent) setError(`读取定时任务失败：${cause instanceof Error ? cause.message : String(cause)}`);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // 倒计时要秒级跳动，但拉数据不用这么勤：每 15 拍取一次，够把后端写回的
  // nextRunAt 带回来，也不给控制台增加无谓请求。
  useEffect(() => {
    let ticks = 0;
    const timer = window.setInterval(() => {
      ticks += 1;
      setNow(Date.now());
      if (ticks % POLL_TICKS === 0) void load(true);
    }, 1000);
    return () => window.clearInterval(timer);
  }, [load]);

  const runsByTask = useMemo(() => {
    const map: Record<string, ScheduledTaskRun[]> = {};
    recentRuns.forEach((run) => {
      const list = map[run.taskId] || [];
      list.push(run);
      map[run.taskId] = list;
    });
    return map;
  }, [recentRuns]);

  const persist = useCallback(async (draft: ScheduledTask) => {
    const isEdit = Boolean(draft.id);
    const response = await fetch(isEdit ? `${API_BASE}/__quota-tasks/${encodeURIComponent(draft.id)}` : `${API_BASE}/__quota-tasks`, {
      method: isEdit ? 'PATCH' : 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(draft),
    });
    if (!response.ok) throw new Error(await readError(response));
    setEditor(null);
    await load(true);
  }, [load]);

  async function toggle(task: ScheduledTask, enabled: boolean) {
    try {
      await persist({ ...task, enabled });
    } catch (cause) {
      setError(`更新失败：${cause instanceof Error ? cause.message : String(cause)}`);
    }
  }

  async function runNow(task: ScheduledTask) {
    setBusyId(task.id);
    setError('');
    try {
      const response = await fetch(`${API_BASE}/__quota-tasks/${encodeURIComponent(task.id)}/run`, { method: 'POST' });
      if (!response.ok && response.status !== 202) throw new Error(await readError(response));
      // 探测在后台逐个 Provider 跑，稍后自己回来看明细。
      window.setTimeout(() => void load(true), 1200);
    } catch (cause) {
      setError(`立即执行失败：${cause instanceof Error ? cause.message : String(cause)}`);
    } finally {
      setBusyId('');
    }
  }

  async function remove(task: ScheduledTask) {
    if (!(await confirmDialog(`删除定时任务「${task.name}」？执行历史会一并删除。`, {danger: true, confirmLabel: '删除'}))) return;
    const response = await fetch(`${API_BASE}/__quota-tasks/${encodeURIComponent(task.id)}`, { method: 'DELETE' });
    if (!response.ok) {
      setError(`删除失败：${await readError(response)}`);
      return;
    }
    await load(true);
  }

  return (
    <section className="section-full">
      <div className="card panel">
        <div className="panel-header">
          <div>
            <h2 className="panel-title">额度保活</h2>
            <p className="panel-desc">
              按指定时间自动向上游发一条极短的探测对话，把订阅额度窗口钉在固定小时上，让重置时间可预测（例如 09:00 → 14:00 → 19:00）。建议只放订阅类 Provider，探测成本可忽略。
            </p>
            {canManage ? null : <p className="quota-hint">你的角色只有查看权限，任务由有维护权限的账号创建和调整。</p>}
          </div>
          {canManage ? (
            <button type="button" className="btn primary" onClick={() => setEditor({ draft: emptyDraft() })}>
              新建任务
            </button>
          ) : null}
        </div>

        {error ? <div className="quota-error">{error}</div> : null}

        {loading && tasks.length === 0 ? (
          <div className="empty-state">加载定时任务中…</div>
        ) : tasks.length === 0 ? (
          <div className="empty-state">
            {providers.length === 0
              ? '你还没有可用的 Provider：保活任务只能探测已授权给你的 Provider，请先在「输入 Provider」里添加或让管理员分配。'
              : '还没有定时任务。建议只放订阅类 Provider（Claude 套餐、ZCode、Trae 等）：按量计费的 API Key 也会被真的调用一次。'}
          </div>
        ) : (
          <div className="quota-grid">
            {tasks.map((task) => (
              <QuotaTaskCard
                key={task.id}
                task={task}
                providers={providers}
                runs={runsByTask[task.id] || []}
                providerQuota={providerQuota}
                canManage={canManage}
                busy={busyId === task.id}
                expanded={expanded === task.id}
                onToggleExpansion={() => setExpanded(expanded === task.id ? '' : task.id)}
                onToggle={(enabled) => void toggle(task, enabled)}
                onRun={() => void runNow(task)}
                onEdit={() => setEditor({ draft: { ...emptyDraft(), ...task } })}
                onDelete={() => void remove(task)}
                now={now}
                owner={showOwner ? ownerNames[task.ownerUserId || ''] || '' : ''}
                serverZone={serverZone}
              />
            ))}
          </div>
        )}
      </div>

      {editor ? (
        <ScheduledTaskEditor
          draft={editor.draft}
          providers={providers}
          serverZone={serverZone}
          onClose={() => setEditor(null)}
          onSubmit={persist}
        />
      ) : null}
    </section>
  );
}

type QuotaResetWindow = { providerId: string; providerName: string; label: string; usedPercent: number; at: number };

/**
 * 各 Provider 上游给的真实窗口重置时间。原来卡片用「下次触发 + 5 小时」推算，
 * 一旦调度跳过或延迟，推算就与上游对不上；有 resets_at 时以它为准。
 */
function quotaResetWindows(task: ScheduledTask, providers: Provider[], providerQuota: Record<string, MonitorQuota>, now: number): QuotaResetWindow[] {
  const out: QuotaResetWindow[] = [];
  for (const providerId of task.providerIds) {
    const windows = providerQuota[providerId]?.windows ?? [];
    if (windows.length === 0) continue;
    // 保活要钉的是五小时窗口；厂商没给时才退回「用得最紧且带重置时间」的窗口。
    const target = windows.find((window) => window.key === 'five_hour' && window.resetsAt)
      || windows.filter((window) => window.resetsAt).sort((left, right) => right.usedPercent - left.usedPercent)[0];
    if (!target?.resetsAt) continue;
    const at = Date.parse(target.resetsAt);
    // 已经过去的重置时间说明样本过期：这时显示"已重置"只会误导，让卡片退回推算。
    if (Number.isNaN(at) || at <= now) continue;
    out.push({
      providerId,
      providerName: providers.find((provider) => provider.id === providerId)?.name || providerId,
      label: quotaWindowLabel(target.key),
      usedPercent: target.usedPercent,
      at,
    });
  }
  return out.sort((left, right) => left.at - right.at);
}

function QuotaTaskCard({ task, providers, runs, providerQuota, canManage, busy, expanded, onToggleExpansion, onToggle, onRun, onEdit, onDelete, now, owner, serverZone }: {
  task: ScheduledTask;
  providers: Provider[];
  runs: ScheduledTaskRun[];
  providerQuota: Record<string, MonitorQuota>;
  canManage: boolean;
  busy: boolean;
  expanded: boolean;
  onToggleExpansion: () => void;
  onToggle: (enabled: boolean) => void;
  onRun: () => void;
  onEdit: () => void;
  onDelete: () => void;
  now: number;
  owner: string;
  serverZone: string;
}) {
  const latest = runs[0]?.batchId || '';
  const lastBatch = latest ? runs.filter((run) => run.batchId === latest) : [];
  const lastSummary = summarize(lastBatch);
  // 所有时刻都按任务自己的时区显示：芯片上的 09:00 与"下次触发 09:00"必须是
  // 同一个 09:00，否则用户只能看到"9 点的任务在凌晨跑"。
  const zone = task.timeZone || serverZone;
  const zoneDiffersFromBrowser = Boolean(task.timeZone) && task.timeZone !== LOCAL_ZONE;
  const nextReset = task.nextRunAt ? new Date(new Date(task.nextRunAt).getTime() + WINDOW_HOURS * 3600000).toISOString() : '';
  const realResets = quotaResetWindows(task, providers, providerQuota, now);
  const earliestReset = realResets[0];

  return (
    <article className={`quota-card${task.enabled ? '' : ' paused'}`}>
      <header className="quota-card-head">
        <h3 className="quota-card-title" title={task.name}>{task.name}</h3>
        {owner ? <span className="quota-owner" title="该任务由这位用户创建">来自 {owner}</span> : null}
        <Badge tone={task.enabled ? 'blue' : 'slate'}>{MODE_LABELS[task.mode]}</Badge>
        <label className="quota-switch" title={canManage ? undefined : '没有额度保活维护权限，只能查看'}>
          <input type="checkbox" checked={task.enabled} disabled={!canManage} onChange={(event) => onToggle(event.target.checked)} />
          <span>{task.enabled ? '运行中' : '已暂停'}</span>
        </label>
      </header>

      <div className="quota-clock-chips">
        {task.mode === 'daily_times' ? (
          (task.times || []).map((clock) => <span className="quota-clock-chip" key={clock}>{clock}</span>)
        ) : (
          <span className="quota-clock-chip wide">每 {task.interval} {task.mode === 'every_hours' ? '小时' : '分钟'}</span>
        )}
        <span className="quota-clock-note" title={task.timeZone ? undefined : '该任务没有指定时区，按网关所在时区解释'}>
          {task.timeZone || serverZone || '服务器当地'}
        </span>
        {task.mode !== 'daily_times' ? (
          <span className="quota-clock-note">锚点 {dayClockIn(task.anchorAt || task.createdAt, zone) || '创建时刻'}</span>
        ) : null}
      </div>

      <div className="quota-next">
        <span className="quota-next-label">下次触发</span>
        <span className="quota-next-value">{task.enabled ? dayClockIn(task.nextRunAt, zone) || '待计算' : '已暂停'}</span>
        {task.enabled && task.nextRunAt ? <span className="quota-next-countdown">{countdown(task.nextRunAt, now)}</span> : null}
        {task.enabled && earliestReset ? (
          <span
            className="quota-next-reset"
            title={`${realResets.map((item) => `${item.providerName}：${item.label} ${Math.round(item.usedPercent)}% 已用，${dayClockIn(new Date(item.at).toISOString(), zone)} 重置`).join('\n')}\n来自上游 resets_at，不是按触发时间推算`}
          >
            {`${earliestReset.label}窗口 ${dayClockIn(new Date(earliestReset.at).toISOString(), zone)} 重置`}
          </span>
        ) : task.enabled && nextReset ? (
          <span className="quota-next-reset" title="上游还没有给出窗口重置时间，这里按「下次触发 + 5 小时」推算">
            {`窗口预计 ${clockIn(nextReset, zone)} 重置`}
          </span>
        ) : null}
      </div>
      {task.enabled && zoneDiffersFromBrowser && task.nextRunAt ? (
        <p className="quota-hint">按 {zone} 计时；你这里（{LOCAL_ZONE}）是 {dayClockIn(task.nextRunAt, LOCAL_ZONE)}。</p>
      ) : null}

      <div className="quota-providers">
        {task.providerIds.map((id) => {
          const provider = providers.find((item) => item.id === id);
          const model = task.modelOverrides?.[id];
          if (!provider) {
            return <span className="quota-provider missing" key={id} title="该 Provider 已删除，下次调度前会自动移出任务"><ProviderRefLabel providerId={id} providers={providers} size={15} /></span>;
          }
          return (
            <span className="quota-provider" key={id} title={model ? `固定模型 ${model}` : `使用默认模型 ${provider.defaultModel || '（未设置）'}`}>
              <ProviderLabel provider={provider} size={15} />
              {model ? <span className="quota-provider-model">{model}</span> : null}
            </span>
          );
        })}
      </div>

      <div className="quota-facts">
        <span>静默 <b>{task.quietMinutes < 0 ? '关闭' : `${task.quietMinutes || 60} 分钟`}</b></span>
        <span>消息 <b>{task.probePrompt || 'ping'}</b></span>
        <span>上次 <b>{dayClockIn(task.lastRunAt, zone) || '从未执行'}</b></span>
        {lastBatch.length > 0 ? (
          <span className={lastSummary.failed > 0 ? 'quota-facts-warn' : ''}>
            本轮 <b>{lastSummary.fired} 成功 / {lastSummary.skipped} 跳过 / {lastSummary.failed} 失败</b>
          </span>
        ) : null}
      </div>

      <footer className="quota-card-foot">
        {canManage ? (
          <button type="button" className="btn btn-sm" onClick={onRun} disabled={busy || task.providerIds.length === 0}>
            {busy ? '执行中…' : '立即执行'}
          </button>
        ) : null}
        {canManage ? <button type="button" className="btn btn-sm subtle" onClick={onEdit}>编辑</button> : null}
        {canManage ? (
          <MoreMenu
            items={[
              { label: expanded ? '收起明细' : '查看明细', onClick: onToggleExpansion },
              { label: '删除任务', danger: true, onClick: onDelete },
            ]}
          />
        ) : (
          <button type="button" className="btn btn-sm subtle" onClick={onToggleExpansion}>{expanded ? '收起明细' : '查看明细'}</button>
        )}
        <span className="quota-run-hint">{runs.length > 0 ? `近 ${runs.length} 条记录` : '尚无执行记录'}</span>
      </footer>

      {expanded ? (
        <div className="quota-runs">
          {runs.slice(0, 12).map((run) => {
            const detail = run.outcome === 'fired'
              ? `HTTP ${run.status}${run.latencyMs ? ` · ${run.latencyMs}ms` : ''}${run.model ? ` · ${run.model}` : ''}`
              : `${run.reason || '未说明原因'}${run.model ? ` · ${run.model}` : ''}`;
            return (
              <div className={`quota-run-row ${run.outcome}`} key={run.id}>
                <span className="quota-run-time">{clockIn(run.firedAt, zone)}</span>
                <Badge tone={outcomeTone(run.outcome)}>{outcomeLabel(run.outcome)}</Badge>
                <span className="quota-run-provider"><ProviderRefLabel providerId={run.providerId} providers={providers} size={14} /></span>
                <span className="quota-run-detail" title={detail}>{detail}</span>
              </div>
            );
          })}
        </div>
      ) : null}
    </article>
  );
}

function ScheduledTaskEditor({ draft, providers, serverZone, onClose, onSubmit }: {
  draft: ScheduledTask;
  providers: Provider[];
  serverZone: string;
  onClose: () => void;
  onSubmit: (task: ScheduledTask) => Promise<void>;
}) {
  const [task, setTask] = useState<ScheduledTask>(() => ({ ...emptyDraft(), ...draft }));
  const [newClock, setNewClock] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [preview, setPreview] = useState<QuotaPreviewFire[]>([]);
  const previewSeq = useRef(0);

  const patch = (changes: Partial<ScheduledTask>) => setTask((current) => ({ ...current, ...changes }));
  // 时间点必须基于"当前"列表算。直接读渲染闭包里的 task 时，连点两个 × 的第二下
  // 会拿到第一次点击之前的列表，于是一个时间点悄悄留了下来。
  const patchTimes = (update: (times: string[]) => string[]) =>
    setTask((current) => ({ ...current, times: update(current.times || []) }));

  // 老任务可能存着预设里没有的阈值。少了这一条，select 会显示成列表第一项，
  // 用户只是打开看了一眼就被改掉。
  const quietOptions = useMemo(() => {
    const options = [...QUIET_OPTIONS];
    if (task.quietMinutes > 0 && !options.some((option) => option.value === task.quietMinutes)) {
      options.splice(1, 0, { label: `${task.quietMinutes} 分钟`, value: task.quietMinutes });
    }
    return options;
  }, [task.quietMinutes]);

  // 时区列表里必须包含任务已存的值，否则 select 会显示成第一项，
  // 用户打开看一眼再保存就把时区悄悄改掉了（静默窗口踩过同一个坑）。
  const zoneOptions = useMemo(() => {
    const options = [
      { value: LOCAL_ZONE, label: `我这里的时区（${LOCAL_ZONE}）` },
      ...(serverZone && serverZone !== LOCAL_ZONE ? [{ value: serverZone, label: `服务器当地（${serverZone}）` }] : []),
      ...COMMON_ZONES.filter((zone) => zone !== LOCAL_ZONE && zone !== serverZone).map((zone) => ({ value: zone, label: zone })),
      { value: 'UTC', label: 'UTC' },
    ];
    const current = task.timeZone || serverZone;
    if (current && !options.some((option) => option.value === current)) {
      options.unshift({ value: current, label: `当前（${current}）` });
    }
    return options;
  }, [serverZone, task.timeZone]);

  // 触发时间由后端那套调度函数算，前端不复制一份轮次推算，两边才不会漂。
  // 配置一变就重新问一次，400ms 内的连续输入只留最后一次。
  useEffect(() => {
    const timer = window.setTimeout(() => {
      const seq = ++previewSeq.current;
      void (async () => {
        try {
          const response = await fetch(`${API_BASE}/__quota-tasks/preview`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(task),
          });
          if (!response.ok) {
            if (seq === previewSeq.current) setPreview([]);
            return;
          }
          const payload = (await response.json()) as { fires?: QuotaPreviewFire[] };
          if (seq === previewSeq.current) setPreview(payload.fires || []);
        } catch {
          if (seq === previewSeq.current) setPreview([]);
        }
      })();
    }, 400);
    return () => window.clearTimeout(timer);
  }, [task]);

  const providerChoices = useMemo(
    () => providers.map((provider) => ({ id: provider.id, label: provider.name, icon: providerIcon(provider) })),
    [providers],
  );

  function addClock() {
    const value = normalizeClock(newClock);
    if (!value) {
      setError('时间格式应为 HH:MM，例如 09:00');
      return;
    }
    setError('');
    patchTimes((times) => Array.from(new Set([...times, value])).sort());
    setNewClock('');
  }

  async function submit() {
    setSaving(true);
    setError('');
    try {
      await onSubmit(task);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      title={task.id ? '编辑保活任务' : '新建保活任务'}
      description="策略、时间点、要探测的 Provider。保存后由网关的调度循环接管，改完即生效。"
      onClose={onClose}
      size="wide"
    >
      <div className="quota-editor">
        <div className="quota-section">
          <div className="quota-section-head">
            <h4 className="quota-section-title">基本信息</h4>
            <CheckboxField label="启用任务" checked={task.enabled} onChange={(enabled) => patch({ enabled })} />
          </div>
          <Field label="任务名称" value={task.name} onChange={(value) => patch({ name: value })} placeholder="例如 Claude 早九点锚定" fullWidth />
        </div>

        <div className="quota-section">
          <h4 className="quota-section-title">调度策略</h4>
          <div className="quota-modes">
            {(['daily_times', 'every_hours', 'every_minutes'] as ScheduleMode[]).map((mode) => (
              <button key={mode} type="button" className={`quota-mode${task.mode === mode ? ' active' : ''}`} onClick={() => patch({ mode })}>
                {MODE_LABELS[mode]}
              </button>
            ))}
          </div>
          <p className="quota-hint">{MODE_HINTS[task.mode]}</p>

          <div className="field quota-zone-field">
            <label>时间点按哪个时区解释</label>
            <select value={task.timeZone || serverZone} onChange={(event) => patch({ timeZone: event.target.value })}>
              {zoneOptions.map((option) => <option value={option.value} key={option.value}>{option.label}</option>)}
            </select>
          </div>
          {task.mode === 'daily_times' && task.timeZone === LOCAL_ZONE ? (
            <p className="quota-hint">芯片上的小时按你这里的时区（{LOCAL_ZONE}）解释，与网关在哪无关。</p>
          ) : (
            <p className="quota-hint">
              芯片上的小时按 {task.timeZone || serverZone || '服务器当地'} 解释；你这里（{LOCAL_ZONE}）看到的小时不一样。
            </p>
          )}

          {task.mode === 'daily_times' ? (
            <div className="quota-times">
              <div className="field field-inline quota-times-input">
                <label>时间点</label>
                <input
                  value={newClock}
                  onChange={(event) => setNewClock(event.target.value)}
                  onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); addClock(); } }}
                  placeholder="09:00"
                  aria-label="新增时间点"
                />
                <button type="button" className="mini-btn" onClick={addClock}>添加</button>
                <button type="button" className="mini-btn" onClick={() => patchTimes(windowChain)}>
                  按 {WINDOW_HOURS} 小时铺满一天
                </button>
              </div>
              <div className="quota-clock-chips">
                {(task.times || []).map((clock) => (
                  <button
                    type="button"
                    className="quota-clock-chip removable"
                    key={clock}
                    title="点击移除"
                    onClick={() => patchTimes((times) => times.filter((item) => item !== clock))}
                  >
                    {clock}
                    <span className="quota-chip-x">×</span>
                  </button>
                ))}
                {(task.times || []).length === 0 ? <span className="quota-clock-note">还没有时间点</span> : null}
              </div>
            </div>
          ) : (
            <div className="quota-config-grid">
              <div className="field">
                <label>间隔（{task.mode === 'every_hours' ? '小时，1 - 720' : '分钟，1 - 1440'}）</label>
                <input
                  type="number"
                  min={1}
                  max={task.mode === 'every_hours' ? 720 : 1440}
                  value={task.interval || 0}
                  onChange={(event) => patch({ interval: Number(event.target.value) || 0 })}
                />
              </div>
              <div className="field">
                <label>锚点时间（留空 = 创建时刻）</label>
                <input
                  type="datetime-local"
                  value={toInputStamp(task.anchorAt)}
                  onChange={(event) => patch({ anchorAt: fromInputStamp(event.target.value) })}
                />
              </div>
            </div>
          )}
        </div>

        <div className="quota-section">
          <div className="quota-section-head">
            <h4 className="quota-section-title">探测对象</h4>
            <span className="quota-clock-note">{task.providerIds.length} 个 Provider</span>
          </div>
          <MultiSelectFilter
            label="选择 Provider"
            options={providerChoices}
            selected={task.providerIds}
            onChange={(providerIds) => patch({ providerIds })}
            allLabel="尚未选择"
            fieldClassName=""
          />
          {task.providerIds.length > 0 ? (
            <div className="quota-provider-rows">
              {task.providerIds.map((id) => {
                const provider = providers.find((item) => item.id === id);
                if (!provider) {
                  return <div className="quota-provider-row" key={id}><ProviderRefLabel providerId={id} providers={providers} size={15} /><span className="quota-clock-note">已删除，保存后自动移除</span></div>;
                }
                const value = task.modelOverrides?.[id] || '';
                // 模型清单可能还没拉过，或固定的是一个不在清单里的模型；这种
                // 情况下 select 会显示成第一项，用户看一眼就以为没设上。
                const choices = modelsForProbe(provider);
                const options = value && !choices.includes(value) ? [value, ...choices] : choices;
                return (
                  <div className="quota-provider-row" key={id}>
                    <ProviderLabel provider={provider} size={15} protocol={protocolLabel(provider.protocol)} />
                    <div className="field quota-model-field">
                      <select
                        value={value}
                        aria-label={`${provider.name} 的探测模型`}
                        onChange={(event) => {
                          const overrides = { ...(task.modelOverrides || {}) };
                          if (event.target.value) overrides[id] = event.target.value;
                          else delete overrides[id];
                          patch({ modelOverrides: overrides });
                        }}
                      >
                        <option value="">默认模型{provider.defaultModel ? `（${provider.defaultModel}）` : ''}</option>
                        {options.map((model) => <option value={model} key={model}>{model}</option>)}
                      </select>
                    </div>
                  </div>
                );
              })}
            </div>
          ) : null}
        </div>

        <div className="quota-section">
          <h4 className="quota-section-title">保护与消息</h4>
          <div className="quota-config-grid">
            <div className="field">
              <label>静默窗口</label>
              <select value={String(task.quietMinutes)} onChange={(event) => patch({ quietMinutes: Number(event.target.value) })}>
                {quietOptions.map((option) => <option value={String(option.value)} key={option.value}>{option.label}</option>)}
              </select>
            </div>
            <Field label="探测消息" value={task.probePrompt || ''} onChange={(value) => patch({ probePrompt: value })} placeholder="ping" fullWidth />
          </div>
          <p className="quota-hint">
            窗口内同一 Provider 已有保活探测时本轮跳过。每天定点任务到点必发，真实流量不会拦截它；
            滚动模式遇到窗口内的成功请求会跳过，避免把上游重置时间推后。
          </p>
        </div>

        <div className="quota-section">
          <div className="quota-section-head">
            <h4 className="quota-section-title">未来触发时间</h4>
            <span className="quota-clock-note">按 {task.timeZone || serverZone || '服务器当地'}</span>
          </div>
          {preview.length > 0 ? (
            <div className="quota-preview">
              {preview.map((fire) => (
                <div className="quota-preview-row" key={fire.at}>
                  <span className="quota-preview-day">{fire.daily}</span>
                  <span className="quota-preview-clock">{fire.clock}</span>
                  <span className="quota-preview-arrow">→</span>
                  <span className="quota-preview-reset">{shiftClock(fire.clock, WINDOW_HOURS)} 重置</span>
                </div>
              ))}
            </div>
          ) : (
            <p className="quota-hint">填好策略后这里会列出接下来几次触发时间，以及按 {WINDOW_HOURS} 小时窗口推断的重置时刻。</p>
          )}
        </div>

        {error ? <div className="quota-error">{error}</div> : null}

        <div className="quota-editor-foot">
          <button type="button" className="btn" onClick={onClose}>取消</button>
          <button type="button" className="btn success" onClick={submit} disabled={saving || task.providerIds.length === 0}>
            {saving ? '保存中…' : task.id ? '保存修改' : '创建任务'}
          </button>
          {task.providerIds.length === 0 ? <span className="quota-hint">至少选择一个 Provider</span> : null}
        </div>
      </div>
    </Modal>
  );
}

/** datetime-local 要的是不带时区的本地串；存回后端时转成 RFC3339。 */
function toInputStamp(stamp?: string) {
  if (!stamp) return '';
  const date = new Date(stamp);
  if (Number.isNaN(date.getTime())) return '';
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function fromInputStamp(value: string) {
  if (!value.trim()) return '';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? '' : date.toISOString();
}
