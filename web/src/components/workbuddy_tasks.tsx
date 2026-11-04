// WorkBuddy 任务中心面板：一键完成、活跃上报、保活，以及每日自动化的两个开关。
//
// 三个按钮对应三件不同的事，不要混：
//   - 一键完成：按动作表逐项推进成长任务，进度由服务端计数决定，
//     所以「跑过了」不等于「完成了」——结果里 awaiting_progress 就是还没入账；
//   - 活跃上报：只发那一条 chat_request_send（连登天数与 first_buddy 的门槛看它）；
//   - 保活：续期令牌 + 活跃上报 + 签到 + 补领达标未领的奖励。
//
// 执行是异步的：一轮要走完整张动作表，其中小程序对话任务按真人节奏 45s 一条，
// 一轮好几分钟。所以这里 POST 之后只轮询状态，按事件把进度打在面板上。
import React from 'react';
import {
    API_BASE
} from '../lib';
import {
    WorkBuddyAutomation,
    WorkBuddyKeepaliveResult,
    WorkBuddyRun,
    WorkBuddyTaskCenter,
    WorkBuddyTaskItem,
    WorkBuddyTaskResult
} from '../types';

/** 结果状态的中文名。unknown 一律原样显示，方便排障。 */
const TASK_STATUS_LABELS: Record<string, string> = {
  done: '已完成',
  claim_pending: '待领奖',
  awaiting_progress: '待入账',
  skipped: '已跳过',
  error: '失败',
  accepted: '已报名'
};

const TASK_STATUS_TONE: Record<string, string> = {
  done: 'ok',
  claim_pending: 'warn',
  awaiting_progress: 'wait',
  skipped: 'muted',
  error: 'bad'
};

function taskStatusText(status?: string): string {
  if (!status) return '';
  return TASK_STATUS_LABELS[status] || status;
}

function taskPercent(task: WorkBuddyTaskItem): number {
  const target = task.target || 0;
  const current = task.current || 0;
  if (target <= 0) return task.claimed ? 100 : 0;
  return Math.min(100, Math.max(0, Math.round((current / target) * 100)));
}

/** 一个任务在列表里的状态：锁定 / 已领 / 可领 / 进度。 */
function taskRowState(task: WorkBuddyTaskItem): string {
  if (task.claimed) return 'done';
  if (task.locked) return 'locked';
  if (task.claimable) return 'claimable';
  return 'progress';
}

export function WorkBuddyTaskPanel({ providerId, connected, compact }: { providerId: string; connected?: boolean; compact?: boolean }) {
  const [view, setView] = React.useState<WorkBuddyTaskCenter | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [busy, setBusy] = React.useState('');
  const [notice, setNotice] = React.useState('');
  const [error, setError] = React.useState('');
  const [expanded, setExpanded] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/workbuddy-oauth/tasks`);
      const data = await response.json().catch(() => ({}));
      if (!response.ok) {
        throw new Error((data as { error?: { message?: string } }).error?.message || `HTTP ${response.status}`);
      }
      setView(data as WorkBuddyTaskCenter);
      setError('');
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setLoading(false);
    }
  }, [providerId]);

  React.useEffect(() => {
    setView(null);
    setNotice('');
    setError('');
  }, [providerId]);

  React.useEffect(() => {
    if (connected) void load();
  }, [connected, load]);

  // 运行中只轮询 run 状态：这个接口不打上游。整套任务列表一次是两次上游调用，
  // 每 1.5 秒来一遍会让厂商的限流窗口直接打满；跑完再拉一次完整列表即可。
  const running = Boolean(view?.run?.running);
  const runId = view?.run?.id || '';
  const pollRun = React.useCallback(async (id: string) => {
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/workbuddy-oauth/tasks/run?runId=${encodeURIComponent(id)}`);
      if (!response.ok) return;
      const run = await response.json() as WorkBuddyRun;
      setView((current) => (current ? { ...current, run } : current));
      if (!run.running) await load();
    } catch {
      // 轮询失败不打断面板：下一拍再试。
    }
  }, [providerId, load]);

  React.useEffect(() => {
    if (!running || !runId) return;
    const timer = window.setInterval(() => { void pollRun(runId); }, 1200);
    return () => window.clearInterval(timer);
  }, [running, runId, pollRun]);

  async function post(path: string, body?: unknown): Promise<Record<string, unknown> | null> {
    setBusy(path);
    setError('');
    setNotice('');
    try {
      const response = await fetch(`${API_BASE}/__providers/${encodeURIComponent(providerId)}/workbuddy-oauth/${path}`, {
        method: 'POST',
        headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body)
      });
      const data = await response.json().catch(() => ({}));
      if (!response.ok) {
        throw new Error((data as { error?: { message?: string } }).error?.message || `HTTP ${response.status}`);
      }
      return data as Record<string, unknown>;
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
      return null;
    } finally {
      setBusy('');
    }
  }

  async function runTasks(taskCodes?: string[]) {
    const data = await post('tasks/auto', taskCodes && taskCodes.length > 0 ? { taskCodes } : {});
    if (data) {
      setNotice(taskCodes && taskCodes.length === 1 ? `已开始执行 ${taskCodes[0]}` : '已开始一键完成，进度在下方逐项显示');
      await load();
    }
  }

  async function reportActivity() {
    const data = await post('activity');
    if (data) setNotice(String(data.message || '活跃已上报'));
  }

  async function keepalive() {
    const data = await post('keepalive') as WorkBuddyKeepaliveResult | null;
    if (data) setNotice(data.message || (data.error ? `保活失败：${data.error}` : '保活已执行'));
  }

  async function toggleAutomation(patch: { dailyTasks?: boolean; dailyKeepalive?: boolean }) {
    const data = await post('automation', patch) as WorkBuddyAutomation | null;
    if (data) {
      setView((current) => (current ? { ...current, automation: data } : current));
      setNotice('自动化设置已保存');
    }
  }

  if (!connected) return null;

  const tasks = view?.tasks || [];
  const automation = view?.automation;
  const results = view?.run?.results || [];
  const latestByTask = new Map<string, WorkBuddyTaskResult>();
  for (const result of results) {
    if (result.taskCode) latestByTask.set(result.taskCode, result);
  }
  const automatable = tasks.filter((task) => task.automatable).length;
  const done = tasks.filter((task) => task.claimed).length;
  // 卡片态（compact）默认只放按钮和开关：任务列表一张卡十几行会把同排其它卡
  // 撑得一样高。要看明细就展开，展开后列表进流动布局。
  const visibleTasks = expanded ? tasks : compact ? [] : tasks.slice(0, 8);
  const recentEvents = expanded || !compact ? (view?.run?.events || []).slice(-4) : [];

  return (
    <div className="wb-task-panel" data-running={running ? 'true' : 'false'}>
      <div className="wb-task-head">
        <div className="wb-task-headline">
          <span className="wb-task-title">任务中心</span>
          <span className="wb-task-meta" title={tasks.length > 0 ? `共 ${tasks.length} 个任务，其中 ${automatable} 个可以自动完成` : ''}>
            {tasks.length > 0 ? `${done}/${tasks.length} 已领取 · 可自动 ${automatable}` : '—'}
          </span>
        </div>
        {/* 按钮单独成组：卡片只有 300 多像素宽，和标题挤在同一行会把按钮顶出边界。
            整组要么跟在标题后面，要么整体换行，永远不会被裁掉。 */}
        <div className="wb-task-actions">
          <button
            type="button"
            className="wb-task-btn primary"
            disabled={Boolean(busy) || running || view?.readOnly}
            title={view?.readOnly ? '国际版任务只读，请在官方客户端操作' : '按动作表逐项推进所有可自动任务'}
            onClick={() => void runTasks()}
          >
            {running ? '执行中…' : '一键完成'}
          </button>
          <button
            type="button"
            className="wb-task-btn"
            disabled={Boolean(busy)}
            title="发一条 chat_request_send：连登天数与 first_buddy 的门槛都看它"
            onClick={() => void reportActivity()}
          >
            活跃上报
          </button>
          <button
            type="button"
            className="wb-task-btn"
            disabled={Boolean(busy)}
            title="续期令牌 + 活跃上报 + 签到 + 补领达标未领的奖励"
            onClick={() => void keepalive()}
          >
            保活
          </button>
          <button type="button" className="wb-task-btn" disabled={loading} onClick={() => void load()}>
            {loading ? '…' : '刷新'}
          </button>
        </div>
      </div>

      {automation ? (
        <div className="wb-task-auto-row">
          <button
            type="button"
            className={`wb-task-toggle${automation.dailyTasks ? ' on' : ''}`}
            aria-pressed={automation.dailyTasks}
            disabled={Boolean(busy) || view?.readOnly}
            title={`每天 ${automation.hour}:00（北京时间）自动跑一遍任务中心`}
            onClick={() => void toggleAutomation({ dailyTasks: !automation.dailyTasks })}
          >
            每日自动任务
          </button>
          <button
            type="button"
            className={`wb-task-toggle${automation.dailyKeepalive ? ' on' : ''}`}
            aria-pressed={automation.dailyKeepalive}
            disabled={Boolean(busy)}
            title={`每天 ${automation.hour}:00（北京时间）续期令牌并上报活跃、签到、补领`}
            onClick={() => void toggleAutomation({ dailyKeepalive: !automation.dailyKeepalive })}
          >
            每日保活
          </button>
          {automation.lastSummary ? (
            <span className="wb-task-last" title={automation.lastRunAt || ''}>
              上次：{automation.lastSummary}
            </span>
          ) : null}
        </div>
      ) : null}

      {view?.error ? <div className="wb-task-error">{view.error}</div> : null}
      {error ? <div className="wb-task-error">{error}</div> : null}
      {notice ? <div className="wb-task-notice">{notice}</div> : null}

      {recentEvents.length > 0 ? (
        <div className="wb-task-log">
          {recentEvents.map((event, index) => {
            const status = event.result?.status;
            return (
              <div key={`${event.taskCode || event.kind}-${index}`} className="wb-task-log-line" data-tone={TASK_STATUS_TONE[status || ''] || 'muted'}>
                <span className="wb-task-log-code">{event.taskCode || event.kind}</span>
                <span className="wb-task-log-text">
                  {event.kind === 'task_start'
                    ? '开始执行…'
                    : event.message || event.result?.message || ''}
                </span>
              </div>
            );
          })}
        </div>
      ) : null}

      {visibleTasks.length > 0 ? (
        <div className="wb-task-list">
          {visibleTasks.map((task) => {
            const result = latestByTask.get(task.taskCode);
            const percent = taskPercent(task);
            return (
              <div key={task.taskCode} className="wb-task-row" data-state={taskRowState(task)}>
                <div className="wb-task-line">
                  <span className="wb-task-name" title={task.action || task.description || task.title || task.taskCode}>
                    {task.title || task.taskCode}
                    {task.mp ? <i className="wb-task-tag">小程序</i> : null}
                  </span>
                  <span className="wb-task-reward">
                    +{task.credit}{task.energy ? ` +${task.energy}e` : ''}
                  </span>
                  {result ? (
                    <span className="wb-task-badge" data-tone={TASK_STATUS_TONE[result.status || ''] || 'muted'}>
                      {taskStatusText(result.status)}
                    </span>
                  ) : task.claimed ? (
                    <span className="wb-task-badge" data-tone="ok">已领取</span>
                  ) : task.locked ? (
                    <span className="wb-task-badge" data-tone="muted">未解锁</span>
                  ) : null}
                  {task.automatable && !task.claimed && !view?.readOnly ? (
                    <button
                      type="button"
                      className="wb-task-run"
                      disabled={Boolean(busy) || running}
                      title={task.action || '执行这个任务的自动动作'}
                      onClick={() => void runTasks([task.taskCode])}
                    >
                      执行
                    </button>
                  ) : null}
                </div>
                {task.progressKnown && (task.target || 0) > 0 ? (
                  <div className="wb-task-progress">
                    <div className="wb-task-bar">
                      <i style={{ width: `${percent}%` }} />
                    </div>
                    <span className="wb-task-count">{task.current || 0}/{task.target || 0}</span>
                  </div>
                ) : null}
                {result?.message ? (
                  <div className="wb-task-result" title={result.message}>{result.message}</div>
                ) : null}
              </div>
            );
          })}
        </div>
      ) : null}

      {tasks.length > visibleTasks.length || expanded ? (
        <button type="button" className="wb-task-more" onClick={() => setExpanded((value) => !value)}>
          {expanded ? '收起任务列表' : `展开全部 ${tasks.length} 个任务`}
        </button>
      ) : null}    </div>
  );
}
