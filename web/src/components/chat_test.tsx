import {useState} from 'react';
import {
    chatTestAnswer,
    defaultThinkingValueForField,
    formatChatTestResponse,
    formatProviderCacheTestDetail,
    formatProviderThinkingTestDetail,
    httpStatusLabel,
    statusTone,
    testResultBadge,
    thinkingPresetsForProtocol,
} from '../lib';
import type {
    Model,
    Protocol,
    ProviderCacheTestResult,
    ProviderChatTestOptions,
    ProviderThinkingTestResult,
    RouteTestResult,
} from '../types';
import {Badge, CopyButton, Field, Modal} from './ui';

/** 一种协议下可选的 thinking 字段与其枚举值。 */
export type ThinkingFieldPreset = ReturnType<typeof thinkingPresetsForProtocol>['fields'][number];

/**
 * 对话测试弹窗。
 *
 * 原来一次运行会叠出三个弹窗（主对话 / Cache / Thinking 各一个），关掉一个又露出
 * 一个，遮罩与滚动互相打架。这里合成一个：配置在上，结果用分段切换看，原始报文收进
 * 折叠区——先给结论（模型回了什么、多少 token），要看细节再展开。
 */

export type ChatTestTab = 'main' | 'cache' | 'thinking';

export type ChatTestResults = {
  main: RouteTestResult | null;
  cache: ProviderCacheTestResult | null;
  thinking: ProviderThinkingTestResult | null;
};

export type ChatTestDialogProps = {
  title: string;
  description: string;
  kind: 'provider' | 'route';
  /** thinking 字段与默认值随协议不同，这里只把协议传进来。 */
  protocol: Protocol;
  /** 该 Provider 是否支持 Cache / Thinking 两项附加测试（Responses 与 OAuth 类不支持）。 */
  supportsExtraTests: boolean;
  models: Model[];
  model: string;
  onModelChange: (value: string) => void;
  refreshingModels: boolean;
  onRefreshModels: () => void;
  options: ProviderChatTestOptions;
  onOptionsChange: (patch: Partial<ProviderChatTestOptions>) => void;
  thinkingFields: ThinkingFieldPreset[];
  message: string;
  onMessageChange: (value: string) => void;
  curl: string;
  curlLabel: string;
  endpointURL: string;
  hintLine?: string;
  /** 鉴权没解析出来时的提醒，空表示一切正常。 */
  authWarning?: string;
  /** 三项测试各自独立跑，所以忙也是分别忙的。 */
  running: Record<ChatTestTab, boolean>;
  onRun: (test: ChatTestTab) => void;
  onClose: () => void;
  results: ChatTestResults;
};

export function ChatTestDialog(props: ChatTestDialogProps) {
  const [tab, setTab] = useState<ChatTestTab>('main');
  const { results } = props;
  const noModel = !props.model && props.models.length === 0;

  const tabs: Array<{ id: ChatTestTab; label: string; state: 'idle' | 'ok' | 'fail' | 'running'; hidden?: boolean }> = [
    { id: 'main', label: '主对话', state: resultState(results.main, props.running.main) },
    { id: 'cache', label: 'Cache', state: tabState(extraState(results.cache), props.running.cache), hidden: !props.supportsExtraTests },
    { id: 'thinking', label: 'Thinking', state: tabState(extraState(results.thinking), props.running.thinking), hidden: !props.supportsExtraTests },
  ];

  return (
    <Modal title={props.title} description={props.description} blocking={!props.running} onClose={props.onClose} size="wide">
      <div className="chat-test">
        <div className="chat-test-config">
          <div className="field">
            <label>测试模型</label>
            <div className="field-inline">
              <select value={props.model} onChange={(event) => props.onModelChange(event.target.value)}>
                <option value="">（使用 Provider 兜底模型）</option>
                {props.models.map((model) => <option key={model.id} value={model.id}>{model.id}</option>)}
              </select>
              <button className="mini-btn" type="button" disabled={props.refreshingModels} onClick={() => void props.onRefreshModels()}>
                {props.refreshingModels ? '刷新中…' : '刷新模型'}
              </button>
            </div>
            {noModel ? (
              <div className="hint-line error">该 Provider 还没有模型列表，也没有兜底默认模型：先点「刷新模型」，否则请求会带着空模型名发出。</div>
            ) : null}
          </div>

          {props.kind === 'provider' ? (
            <>
              <Field multiline label="系统提示词" value={props.options.systemPrompt} onChange={(value) => props.onOptionsChange({ systemPrompt: value })} />
              <Field multiline label="用户提示词" value={props.options.userPrompt} onChange={(value) => props.onOptionsChange({ userPrompt: value })} />
              {props.thinkingFields.length ? (
                <>
                  <div className="field">
                    <label>Thinking 字段</label>
                    <select
                      value={props.options.thinkingField}
                      onChange={(event) => {
                        const next = props.thinkingFields.find((field) => field.key === event.target.value);
                        props.onOptionsChange({
                          thinkingField: event.target.value,
                          thinkingValue: defaultThinkingValueForField(props.protocol, next?.key || event.target.value),
                        });
                      }}
                    >
                      {props.thinkingFields.map((field) => <option key={field.key} value={field.key}>{field.label}</option>)}
                    </select>
                  </div>
                  <div className="field">
                    <label>Thinking 值</label>
                    <div className="field-inline grow">
                      <select
                        className="chat-test-preset"
                        value={currentPreset(props) ? props.options.thinkingValue : ''}
                        onChange={(event) => { if (event.target.value) props.onOptionsChange({ thinkingValue: event.target.value }); }}
                      >
                        <option value="">自定义…</option>
                        {(currentField(props)?.presets || []).map((preset) => <option key={preset} value={preset}>{preset}</option>)}
                      </select>
                      <input
                        className="chat-test-value"
                        placeholder="枚举值或自定义内容"
                        value={props.options.thinkingValue}
                        onChange={(event) => props.onOptionsChange({ thinkingValue: event.target.value })}
                      />
                    </div>
                  </div>
                </>
              ) : null}
            </>
          ) : (
            <Field label="测试消息" value={props.message} onChange={props.onMessageChange} />
          )}
        </div>

        <div className="chat-test-run">
          <button className="btn primary" disabled={props.running.main} onClick={() => props.onRun('main')}>
            {props.running.main ? '测试中…' : results.main ? '重新运行' : '运行测试'}
          </button>
          <span className="chat-test-run-note">
            {props.kind !== 'provider'
              ? '走网关转发后的输出接口，也就是客户端实际调用的 URL。'
              : '发一条真实对话给上游，验证这个 Provider 本身能不能用。'}
          </span>
        </div>

        {props.authWarning ? (
          <div className="hint-line error">{props.authWarning}</div>
        ) : null}
        {props.hintLine ? (
          <div className="hint-text">{props.hintLine}</div>
        ) : null}

        {props.kind === 'provider' && props.supportsExtraTests ? (
          <div className="chat-test-tabs" role="tablist">
            {tabs.filter((item) => !item.hidden).map((item) => (
              <button
                key={item.id}
                role="tab"
                aria-selected={tab === item.id}
                className={`chat-test-tab${tab === item.id ? ' active' : ''}`}
                onClick={() => setTab(item.id)}
              >
                <span className="chat-test-tab-label">{item.label}</span>
                <span className={`chat-test-dot ${item.state}`} aria-hidden="true" />
              </button>
            ))}
          </div>
        ) : null}

        <div className="chat-test-body">
          {tab === 'main' || props.kind === 'route' ? <MainResult result={results.main} running={props.running.main} /> : null}
          {tab === 'cache' && props.kind === 'provider' ? (
            <CacheResult result={results.cache} running={props.running.cache} onRun={() => props.onRun('cache')} />
          ) : null}
          {tab === 'thinking' && props.kind === 'provider' ? (
            <ThinkingResult result={results.thinking} running={props.running.thinking} onRun={() => props.onRun('thinking')} />
          ) : null}
        </div>

        <details className="chat-test-raw">
          <summary>
            <span>{props.curlLabel}</span>
            <span className="chat-test-raw-target" title={props.endpointURL}>{props.endpointURL}</span>
            <CopyButton value={props.curl} label="复制 curl" className="mini-btn" />
          </summary>
          <pre className="curl-preview">{props.curl}</pre>
        </details>
      </div>
    </Modal>
  );
}


function resultState(result: RouteTestResult | null, running: boolean): 'idle' | 'ok' | 'fail' | 'running' {
  if (!result) return running ? 'running' : 'idle';
  return result.success ? 'ok' : 'fail';
}

// 附加测试在主对话之后跑：结果还没回来时点要转起来，而不是继续显示"没跑过"。
function tabState(state: 'idle' | 'ok' | 'fail', running: boolean): 'idle' | 'ok' | 'fail' | 'running' {
  return state === 'idle' && running ? 'running' : state;
}

function extraState(result: ProviderCacheTestResult | ProviderThinkingTestResult | null): 'idle' | 'ok' | 'fail' {
  if (!result) return 'idle';
  if ('skipped' in result && result.skipped) return 'idle';
  return result.success ? 'ok' : 'fail';
}

function currentField(props: ChatTestDialogProps): ThinkingFieldPreset | undefined {
  return props.thinkingFields.find((field) => field.key === props.options.thinkingField) || props.thinkingFields[0];
}

function currentPreset(props: ChatTestDialogProps): boolean {
  return (currentField(props)?.presets || []).includes(props.options.thinkingValue);
}

/** 一次测试的结论卡：状态、耗时、模型、token，然后是模型说的话。 */
function ResultHead({ success, status, latencyMs, tone }: { success: boolean; status?: number; latencyMs?: number; tone?: 'idle' | 'ok' | 'fail' }) {
  return (
    <div className="chat-test-head">
      <Badge tone={success ? 'green' : statusTone(status)}>{testResultBadge(success)}</Badge>
      <span className="chat-test-head-meta">
        {httpStatusLabel(status)}
        {latencyMs != null ? ` · ${latencyMs}ms` : ''}
      </span>
      {tone ? <span className={`chat-test-dot ${tone}`} aria-hidden="true" /> : null}
    </div>
  );
}

function MainResult({ result, running }: { result: RouteTestResult | null; running: boolean }) {
  if (!result) {
    return <div className="chat-test-empty">{running ? '正在请求上游…' : '还没运行。配置好参数点上面的「运行测试」。'}</div>;
  }
  const answer = chatTestAnswer(result);
  const raw = formatChatTestResponse(result);
  return (
    <div className="chat-test-card">
      <ResultHead success={result.success} status={result.status} latencyMs={result.latencyMs} />
      <div className="chat-test-facts">
        <span>模型 <b>{answer?.model || result.model || '-'}</b></span>
        {answer?.usage ? <span>{answer.usage}</span> : null}
        {result.targetUrl || result.upstreamUrl ? <span title={result.targetUrl || result.upstreamUrl}>上游 <b>{shortHost(result.targetUrl || result.upstreamUrl || '')}</b></span> : null}
      </div>
      {(() => {
        // 有结构化诊断时优先给 transportError / 上游 errorMessage；这比
        // 只有一句 HTTP 状态更能说明到底是地址不通、超时还是上游拒绝。
        const diagnosticError = result.diagnostics?.transportError
          || result.diagnostics?.errorMessage
          || result.error
          || answer?.error
          || '';
        return diagnosticError ? <div className="chat-test-error">{diagnosticError}</div> : null;
      })()}
      {answer?.reasoning ? (
        <details className="chat-test-reasoning">
          <summary>思考内容（{answer.reasoning.length} 字符）</summary>
          <div className="chat-test-text subtle">{answer.reasoning}</div>
        </details>
      ) : null}
      {answer?.text ? (
        <div className="chat-test-text" data-testid="chat-test-answer">{answer.text}</div>
      ) : result.success ? (
        <div className="chat-test-text subtle">响应里没有可读的正文，看下面的原始报文。</div>
      ) : null}
      <details className="chat-test-details" open={!result.success}>
        <summary>原始请求与响应</summary>
        <div className="chat-test-details-head">
          <span>{result.success ? '响应报文' : '诊断信息'}</span>
          <CopyButton value={raw} label="复制" className="mini-btn" />
        </div>
        <pre className={`json-preview${result.success ? ' ok' : ''}`}>{raw}</pre>
      </details>
    </div>
  );
}

function CacheResult({ result, running, onRun }: { result: ProviderCacheTestResult | null; running: boolean; onRun: () => void }) {
  if (!result) return <ExtraTestPrompt label="Cache" running={running} onRun={onRun} note="跑两轮同样的对话，看第二轮的 usage 里有没有缓存命中。上游慢时这一步要几十秒。" />;
  if ('skipped' in result && result.skipped) {
    return <div className="chat-test-empty">{result.summary || '该 Provider 不适用 Cache 测试'}</div>;
  }
  const detail = formatProviderCacheTestDetail(result);
  return (
    <div className="chat-test-card">
      <ResultHead success={result.success} status={result.status} latencyMs={result.latencyMs} />
      <div className="chat-test-facts">
        <span>缓存命中 <b>{result.cacheHitTokens != null ? result.cacheHitTokens : '未上报'}</b></span>
        <span title="prompt_cache_hit_tokens / cache_read_input_tokens">取自第二轮</span>
      </div>
      <div className="chat-test-text">{result.summary || 'Cache 测试完成'}</div>
      <details className="chat-test-details">
        <summary>两轮 usage 详情</summary>
        <div className="chat-test-details-head">
          <span>Cache 报文</span>
          <CopyButton value={detail} label="复制" className="mini-btn" />
        </div>
        <pre className="json-preview">{detail}</pre>
      </details>
    </div>
  );
}

function ThinkingResult({ result, running, onRun }: { result: ProviderThinkingTestResult | null; running: boolean; onRun: () => void }) {
  if (!result) return <ExtraTestPrompt label="Thinking" running={running} onRun={onRun} note="按上面选的字段与值发一次请求，看上游是否真的开始思考。" />;
  if ('skipped' in result && result.skipped) {
    return <div className="chat-test-empty">{result.summary || '该 Provider 不适用 Thinking 测试'}</div>;
  }
  const answer = chatTestAnswer(result as unknown as RouteTestResult);
  const detail = formatProviderThinkingTestDetail(result);
  return (
    <div className="chat-test-card">
      <ResultHead success={result.success} status={result.status} latencyMs={result.latencyMs} />
      <div className="chat-test-facts">
        <span>字段 <b>{result.thinkingField || '-'}</b></span>
        <span>值 <b>{result.thinkingValue || '-'}</b></span>
        <span>思考 <b>{answer?.reasoning ? `${answer.reasoning.length} 字符` : '无'}</b></span>
      </div>
      {answer?.reasoning ? <div className="chat-test-text subtle">{answer.reasoning.slice(0, 2000)}</div> : null}
      {answer?.text ? <div className="chat-test-text">{answer.text}</div> : null}
      <details className="chat-test-details">
        <summary>请求与响应</summary>
        <div className="chat-test-details-head">
          <span>Thinking 报文</span>
          <CopyButton value={detail} label="复制" className="mini-btn" />
        </div>
        <pre className="json-preview">{detail}</pre>
      </details>
    </div>
  );
}

/** 附加测试没跑过时的入口：说明它测什么，并给一个只跑这一项的按钮。 */
function ExtraTestPrompt({ label, running, onRun, note }: { label: string; running: boolean; onRun: () => void; note: string }) {
  return (
    <div className="chat-test-prompt">
      <div className="chat-test-empty">{note}</div>
      <button className="btn" disabled={running} onClick={onRun}>{running ? '测试中…' : `运行 ${label} 测试`}</button>
    </div>
  );
}

function shortHost(raw: string): string {
  try {
    return new URL(raw).host;
  } catch {
    return raw.slice(0, 40);
  }
}
