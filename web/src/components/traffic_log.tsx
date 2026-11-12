import {useEffect, useRef, useState} from 'react';
import type {APIKey, LogEntry, Provider, Route} from '../types';
import {ProviderLabel} from './provider_label';
import {
    API_BASE,
    actionLabel,
    buildReplayHeaders,
    compactProtocolFlow,
    formatBytes,
    formatCostMicros,
    isTrafficLogCanceled,
    isTrafficLogError,
    providerDisplayLabel,
    replayPlan,
    scheduleModeLabel,
    summarizeReplayBody,
    trafficLogKeyLabel,
    trafficLogKeyTitle,
    trafficLogPeerChain,
    trafficLogProviderLabel,
    trafficLogServing
} from '../lib';
import {Badge} from './ui';

/**
 * API 日志的请求表。版式照 magpie 的日志页抄：一行一条、数字等宽右对齐、
 * 耗时与速度带语义色、状态是一个点加两个字，不塞胶囊也不折第二行。
 *
 * 我们手里没有 magpie 的「费用 / 缓存写入 / 推理强度」，所以不假装：
 * 列只放网关真的记下来的东西，缺的就不占位。
 */

const NUM = 'en-US';

function fmtNum(value: number | undefined) {
  if (value == null || Number.isNaN(value)) return '—';
  return value.toLocaleString(NUM);
}

/** 耗时按量级换说法，颜色按量级分档：一眼能扫出慢请求。 */
function latencyCell(ms: number) {
  if (!ms || ms <= 0) return { text: '—', tone: '' };
  const text = ms < 1000 ? `${ms} ms` : ms < 60000 ? `≈${(ms / 1000).toFixed(1)} 秒` : `≈${Math.round(ms / 60000)} 分`;
  if (ms < 5000) return { text, tone: 'ok' };
  if (ms < 20000) return { text, tone: 'warn' };
  return { text, tone: 'slow' };
}

function speedCell(log: LogEntry) {
  if (!log.latencyMs || log.latencyMs <= 0 || !log.outputTokens) return { text: '—', tone: '' };
  const perSecond = Math.round((log.outputTokens * 1000) / log.latencyMs);
  if (perSecond <= 0) return { text: '—', tone: '' };
  // 短响应配极小耗时会算出六位数的"速度"，那列不该被它撑爆。
  const label = perSecond >= 100000 ? '99k+' : perSecond >= 10000 ? `${Math.round(perSecond / 1000)}k` : String(perSecond);
  return { text: `≥${label} token/秒`, tone: perSecond >= 30 ? 'ok' : '' };
}

function KeyGlyph() {
  return (
    <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="8" cy="12" r="4.2" />
      <path d="M12.2 12H21M17.5 12v3M20.8 12v2.2" />
    </svg>
  );
}

/**
 * 网关把别名映射记成一个字符串 `客户端模型 -> 实际模型`（见 model_alias.go）。
 * 挤在一列里既截断又读不动，所以照 magpie 拆成两列；没有映射时两边同值，
 * 而不是留一个永远为空的格子。
 */
function splitModel(value: string) {
  const index = value.indexOf(' -> ');
  if (index < 0) return { requested: value, effective: value };
  const requested = value.slice(0, index).trim();
  const effective = value.slice(index + 4).trim();
  return { requested: requested || effective, effective };
}

/**
 * `Claude -> OpenAI Chat` 写全要 170px，每行都放一遍既撑爆表格又是噪声：
 * 压成 C→O，完整流向留在 title 里。
 */
/**
 * 调度轨迹：这次请求按什么顺序、因为什么碰过哪些 Provider。
 *
 * 「为什么是它服务」「为什么没用另一个」是排错时被问最多的两件事，日志里原来只有
 * 最终那一条 providerId，看不出前面还试过谁。这里把网关在故障转移循环里记下的
 * 每一步摊开：被丢弃的回答、根本没发出的候选，都有各自的理由。
 * 早于这个功能的请求没有这些字段，直说未记录，不假装有。
 */
const SCHEDULE_REASONS: Record<string, { text: string; tone: 'green' | 'amber' | 'red' | 'slate' | 'cyan' }> = {
  served: { text: '在服务', tone: 'green' },
  upstream_error: { text: '上游报错', tone: 'red' },
  transport_error: { text: '连接失败', tone: 'red' },
  first_packet_timeout: { text: '首包超时', tone: 'amber' },
  saturated: { text: '并发已满', tone: 'cyan' },
  missing_model_override: { text: '未锁定模型', tone: 'slate' },
  disabled: { text: '已禁用', tone: 'slate' },
  not_assigned: { text: '未授权', tone: 'slate' },
  decision_failed: { text: 'Provider 不存在', tone: 'slate' },
  client_gone: { text: '客户端已断开', tone: 'slate' },
  native_passthrough_failed: { text: '直通失败·转转换', tone: 'amber' },
};

export function ScheduleTracePanel({ log, providers }: { log: LogEntry; providers: Provider[] }) {
  const analysis = log.analysis;
  if (!analysis?.captured) return null;
  const attempts = analysis.attempts || [];
  const candidates = analysis.candidates || [];
  if (attempts.length === 0 && candidates.length === 0) {
    return <div className="hint-text">调度轨迹：这条请求早于该功能，未记录尝试过程。</div>;
  }
  const labelFor = (id: string) => {
    const provider = providers.find((item) => item.id === id);
    return provider ? providerDisplayLabel(provider) : id;
  };
  const movedOn = attempts.filter((item) => !item.skipped && item.reason !== 'served').length;
  return (
    <div className="schedule-trace">
      <div className="schedule-trace-head">
        <strong>调度轨迹</strong>
        <span className="mono">{scheduleModeLabel(analysis.scheduleMode)}</span>
        {analysis.sessionPinned ? <span className="schedule-trace-note">会话粘滞：{labelFor(analysis.sessionPinned)}</span> : null}
        {movedOn > 0 ? <span className="schedule-trace-note">换了 {movedOn} 个候选</span> : null}
      </div>
      <ol className="schedule-trace-list">
        {attempts.map((attempt, index) => {
          const reason = SCHEDULE_REASONS[attempt.reason || ''] || { text: attempt.reason || '未知', tone: 'slate' as const };
          return (
            <li key={`${attempt.providerId}-${index}`} className={`schedule-trace-row${attempt.reason === 'served' ? ' served' : ''}`}>
              <span className="schedule-trace-rank">#{index + 1}</span>
              <span className="schedule-trace-provider">{labelFor(attempt.providerId)}</span>
              {attempt.model ? <span className="mono schedule-trace-model">{attempt.model}</span> : null}
              <Badge tone={reason.tone}>{reason.text}</Badge>
              {attempt.status ? <span className="mono">{attempt.status}</span> : null}
              <span className="mono schedule-trace-ms">{attempt.ms ? `${(attempt.ms / 1000).toFixed(2)}s` : '—'}</span>
              {attempt.deferred ? <span className="schedule-trace-note">回答已丢弃</span> : null}
              {attempt.skipped ? <span className="schedule-trace-note">未发出请求</span> : null}
              {attempt.error ? <code className="schedule-trace-error">{attempt.error}</code> : null}
            </li>
          );
        })}
      </ol>
    </div>
  );
}

type ReplayState = {
  phase: 'idle' | 'running' | 'done' | 'error';
  status?: number;
  latencyMs?: number;
  streamed?: boolean;
  bytes?: number;
  text?: string;
  error?: string;
};

/**
 * 一键重放：拿日志里留存的请求体，按它当时进来的协议入口再发一次。
 *
 * 密钥用用户自己选的那一把（日志只记名字，不记原文），请求头只放 Authorization，
 * 界面上任何位置都不显示密钥值。结果按「上游尝试」的样子摊开：状态、耗时、
 * 是否流式、字节数与解包后的回复，和调度轨迹一个读法。
 */
export function ReplayPanel({ log, apiKeys }: { log: LogEntry; apiKeys: APIKey[] }) {
  const plan = replayPlan(log);
  const [keyId, setKeyId] = useState('');
  const [state, setState] = useState<ReplayState>({ phase: 'idle' });
  const abortRef = useRef<AbortController | null>(null);
  const preferred = apiKeys.find((item) => item.id === log.apiKeyId && item.enabled) || apiKeys.find((item) => item.enabled) || apiKeys[0];
  const key = apiKeys.find((item) => item.id === keyId) || preferred;
  useEffect(() => () => abortRef.current?.abort(), []);

  if (!log.requestBody?.trim()) {
    return <div className="hint-text">重放：这条请求没有留存请求正文（旧日志或未开启正文落盘），无法重放。</div>;
  }
  if (!plan) {
    return <div className="hint-text">重放：这条请求走的是「{actionLabel(log.action)}」类调用（{log.path || '无路径'}），不是模型对话入口，暂无对应重放方式。</div>;
  }

  const run = async () => {
    if (!key) {
      setState({ phase: 'error', error: '没有可用的 API 密钥：请先新建一把再重放。' });
      return;
    }
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    const started = performance.now();
    setState({ phase: 'running' });
    try {
      const response = await fetch(`${API_BASE}${plan.path}`, {
        method: 'POST',
        headers: buildReplayHeaders(plan, key.key),
        body: log.requestBody,
        credentials: 'same-origin',
        signal: controller.signal,
      });
      const raw = await response.text();
      const latencyMs = Math.round(performance.now() - started);
      const streamed = (response.headers.get('content-type') || '').includes('event-stream') || raw.includes('data:');
      setState({ phase: 'done', status: response.status, latencyMs, streamed, bytes: raw.length, text: summarizeReplayBody(raw, streamed) });
    } catch (error) {
      if (controller.signal.aborted) return;
      setState({ phase: 'error', error: String(error), latencyMs: Math.round(performance.now() - started) });
    }
  };

  return (
    <div className="replay-panel">
      <div className="replay-panel-controls">
        <label>重放密钥</label>
        <select aria-label="重放密钥" value={key?.id || ''} onChange={(event) => setKeyId(event.target.value)}>
          {apiKeys.map((item) => <option key={item.id} value={item.id} disabled={!item.enabled}>{item.name}{item.enabled ? '' : '（已禁用）'}</option>)}
        </select>
        <span className="replay-panel-target mono" title={`POST ${plan.path}`}>POST {plan.path}</span>
        <button className="mini-btn" type="button" disabled={state.phase === 'running' || !key || !key.enabled} onClick={() => void run()}>
          {state.phase === 'running' ? '重放中…' : '发送重放'}
        </button>
      </div>
      <div className="replay-panel-note">
        请求体取自 {new Date(log.time).toLocaleString()} 的这次调用（{formatBytes(log.requestBody.length)}，落盘时可能已截断）；
        密钥只用于本次 Authorization 头，不会显示或写入日志。重放会真实消耗额度并生成一条新日志。
      </div>
      {state.phase === 'error' ? <div className="replay-attempt error"><Badge tone="red">失败</Badge><span>{state.error}</span></div> : null}
      {state.phase === 'done' ? (
        <div className={`replay-attempt${state.status && state.status >= 400 ? ' error' : ''}`}>
          <span className="replay-rank">#{1}</span>
          <Badge tone={state.status && state.status >= 400 ? 'red' : 'green'}>HTTP {state.status}</Badge>
          <span className="mono">{state.latencyMs} ms</span>
          <span className="replay-note">{state.streamed ? '流式' : '非流式'} · {formatBytes(state.bytes)}</span>
          <pre className="replay-text">{state.text || '（空响应）'}</pre>
        </div>
      ) : null}
    </div>
  );
}

/** 一行日志的稳定标识：增量轮询靠它判断「这条是不是已经在表里」。 */
export function trafficLogRowKey(log: LogEntry): string {
  return `${log.id ?? log.time}-${log.model}-${log.latencyMs}`;
}

export function TrafficLogTable({ logs, providers, routes, apiKeys, onOpen, freshKeys }: {
  logs: LogEntry[];
  providers: Provider[];
  routes?: Route[];
  apiKeys?: APIKey[];
  /** 刚刚由增量轮询插进来的行：挂进场动画，按顺序轻微错开。 */
  freshKeys?: Set<string>;
  onOpen: (log: LogEntry) => void;
}) {
  return (
    <div className="tlog-scroll">
      <div className="tlog">
        <div className="tlog-grid tlog-head">
          <span>时间</span>
          <span>密钥</span>
          <span>请求模型</span>
          <span>实际模型</span>
          <span title="上游实际收到的推理设置">推理强度</span>
          <span title="实际承接这条请求的 Provider 与账号；带「备N」标记的是首选未服务、由备选第 N 位接的">供应商 · 账号</span>
          <span title="这条请求的来路与去向：←下游网关 ID 表示由它送进来，→上游网关 ID 表示本机转给了它；「直连」表示没经过任何对端网关">链路</span>
          <span>协议</span>
          <span className="num">输入</span>
          <span className="num">输出</span>
          <span className="num" title="写入提示缓存的 token（已含在输入里）">缓存写入</span>
          <span className="num" title="命中缓存读取的 token（已含在输入里）">缓存读取</span>
          <span className="num">耗时</span>
          <span className="num">速度</span>
          <span className="num" title="按价格行折算的成本；未计价的请求显示「未计价」而不是 $0">成本</span>
          <span>状态</span>
        </div>
        {logs.map((log, index) => {
          const canceled = isTrafficLogCanceled(log);
          const error = isTrafficLogError(log);
          const ok = log.status >= 200 && log.status < 300;
          const latency = latencyCell(log.latencyMs);
          const speed = speedCell(log);
          const provider = providers.find((item) => item.id === log.providerId);
          const model = splitModel(log.model || '');
          // providerId 记的就是真正服务的那条：与路由首选不一致，说明这条走了备选。
          const serving = trafficLogServing(log, providers, routes || [], apiKeys || []);
          const peerChain = trafficLogPeerChain(log);
          const failoverTitle = serving
            ? `首选「${providerDisplayLabel(serving.primary, '未绑定')}」未承接，本条由${serving.rank ? `备选 ${serving.rank}` : '其他 Provider'}「${providerDisplayLabel(provider, log.providerId || '未知')}」服务`
            : undefined;
          const rowKey = trafficLogRowKey(log);
          const isFresh = freshKeys?.has(rowKey) ?? false;
          return (
            <div
              className={`tlog-grid tlog-row${error ? ' error' : canceled ? ' canceled' : ''}${isFresh ? ' is-new' : ''}`}
              key={rowKey}
              data-log-key={rowKey}
              // 一次来几条时逐条错开，别糊成一片。
              style={isFresh ? { animationDelay: `${Math.min(index * 45, 220)}ms` } : undefined}
              onClick={() => onOpen(log)}
              title="点击查看请求链路 APM 耗时分解与完整报文"
              role="button"
              tabIndex={0}
              onKeyDown={(event) => { if (event.key === 'Enter') onOpen(log); }}
            >
              <span className="tlog-time">{new Date(log.time).toLocaleTimeString('zh-CN', { hour12: false })}</span>
              <span className="tlog-key" title={trafficLogKeyTitle(log)}>
                <KeyGlyph />
                <b>{trafficLogKeyLabel(log)}</b>
              </span>
              <span className="tlog-model" title={model.requested}>{model.requested || '—'}</span>
              <span className="tlog-model effective" title={model.effective}>{model.effective || '—'}</span>
              <span className="tlog-reasoning" title={log.reasoningEffort || '未设置推理强度'}>{log.reasoningEffort || '—'}</span>
              <span className="tlog-provider" title={failoverTitle}>
                {serving ? <i className={`tlog-served${serving.rank ? '' : ' unknown'}`}>{serving.rank ? `备${serving.rank}` : '切换'}</i> : null}
                {provider ? <ProviderLabel provider={provider} size={14} stack /> : trafficLogProviderLabel(log, providers)}
              </span>
              <span className={`tlog-peer${peerChain.peered ? ' peered' : ''}`} title={peerChain.title}>
                {peerChain.text}
              </span>
              <span className="tlog-proto" title={`${actionLabel(log.action)} · ${log.protocolFlow || '未知流向'}（oc=OpenAI Chat，or=OpenAI Responses，cl=Claude）· ${log.path}`}>
                {compactProtocolFlow(log.protocolFlow)}
              </span>              <span className="num">{fmtNum(log.inputTokens)}</span>
              <span className="num">{fmtNum(log.outputTokens)}</span>
              <span className="num" title="写入提示缓存的 token（已计入输入）">{fmtNum(log.cacheWriteTokens)}</span>
              <span className="num" title="命中缓存读取的 token（已计入输入）">{fmtNum(log.cacheTokens)}</span>
              <span className={`num tlog-latency ${latency.tone}`} title={log.ttftMs != null ? `首字 ${log.ttftMs}ms` : undefined}>
                {latency.text}
              </span>
              <span className={`num tlog-speed ${speed.tone}`}>{speed.text}</span>
              <span
                className="num tlog-cost"
                title={log.priced === false ? '这次请求的模型没有价格行，成本未计入' : `成本 ${formatCostMicros(log.costMicros)}`}
              >
                {/* 未计价（没有价格行）不能显示成金额：那会把"没算价"读成"花了这么多"。
                    口径见 types.ts：缺省即未计价，绝不等于 $0。 */}
                {log.costMicros == null ? '—' : log.priced === false ? '未计价' : formatCostMicros(log.costMicros)}
              </span>
              <span
                className={`tlog-status ${ok ? 'ok' : canceled ? 'canceled' : 'bad'}`}
                title={`HTTP ${log.status}${canceled ? ' · 客户端主动取消' : ''}${log.errorDescription ? ` · ${log.errorDescription}` : ''}`}
              >
                <i />
                {ok ? '成功' : canceled ? '已取消' : '失败'}
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
}
