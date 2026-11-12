import React, {useCallback, useEffect, useMemo, useRef, useState} from 'react';
import {API_BASE, protocolLabel} from '../lib';
import {Badge, Field, Modal} from './ui';
import {confirmDialog} from '../dialogs';
import {IconSelect, IconSelectOption} from './selects';
import type {GrantTarget} from './rbac_types';
import {ProviderGrantPicker} from './provider_grant_picker';
import type {PeerGateway, PeerUpstream, PeerUpstreamsResponse, PeersResponse, Protocol} from '../types';

async function errorTextFrom(response: Response): Promise<string> {
  try {
    const raw = (await response.text()).trim();
    if (!raw) return `HTTP ${response.status}`;
    try {
      const parsed = JSON.parse(raw) as { error?: {message?: string} | string; message?: string };
      if (typeof parsed.error === 'string') return parsed.error;
      const message = parsed.error?.message || parsed.message;
      if (message) return message;
    } catch {
      // 非 JSON：原样返回
    }
    return raw;
  } catch {
    return `HTTP ${response.status}`;
  }
}

async function jsonRequest<T>(url: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(url, { credentials: 'same-origin', ...init });
  if (!response.ok) throw new Error(await errorTextFrom(response));
  if (response.status === 204) return undefined as T;
  const text = await response.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

function formatTime(value: string | undefined): string {
  if (!value) return '从未';
  const ts = Date.parse(value);
  if (Number.isNaN(ts)) return '—';
  const diff = Date.now() - ts;
  if (diff >= 0 && diff < 60_000) return '刚刚';
  if (diff >= 0 && diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
  if (diff >= 0 && diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`;
  return new Date(ts).toLocaleString();
}

/**
 * 授权清单的悬浮提示：这条上游授权了哪些 Provider，名字与上游那边的写法一致。
 * 「授权 N 个 Provider」这几个字本身答不出"到底授权了哪一个"，而这一栏正是
 * 运维核对的地方。
 */
function grantedProviderTitles(upstream: PeerUpstream): string {
  const granted = (upstream.grantedProviders || []).map((item) => item.name?.trim() || item.id).filter(Boolean);
  if (granted.length === 0) return '';
  return `上游授权的 Provider：\n${granted.join('\n')}`;
}

/** 本机身份：注册时发给上游的就是它，运维靠它核对"上游那一行是不是我"。 */
function GatewayIdChip({ gatewayId }: { gatewayId: string }) {
  if (!gatewayId) {
    return <span className="hint-text">网关身份还没就绪</span>;
  }
  return (
    <span className="peer-gateway-id" title="本机网关身份，重装 app 不变（由机器标识派生）">
      <span className="peer-gateway-id-label">本机身份</span>
      <code>{gatewayId}</code>
    </span>
  );
}

const PROTOCOL_OPTIONS: Array<{value: Protocol; label: string}> = [
  { value: 'openai_chat', label: 'OpenAI Chat' },
  { value: 'openai_responses', label: 'OpenAI Responses' },
  { value: 'claude', label: 'Claude' },
];

type UpstreamDraft = {
  /** 已有上游的 provider ID；null = 新建。 */
  providerId: string | null;
  parentUrl: string;
  username: string;
  password: string;
  name: string;
  protocol: Protocol;
  requestedProviderIds: string[];
};

const emptyUpstreamDraft: UpstreamDraft = {
  providerId: null,
  parentUrl: '',
  username: '',
  password: '',
  name: '',
  protocol: 'openai_chat',
  requestedProviderIds: [],
};

/**
 * 网关互联：一张页面的两半。
 *
 * 上半是「我连到谁」——本机作为下游注册到上游网关，注册回来就是一条
 * authType=peer 的 provider，用的是本机在上游的凭据。
 * 下半是「谁连到我」——本机作为上游，列着注册上来的下游网关，可以调授权、
 * 吊销、设置注册来源白名单。
 *
 * 两半放在一起是因为它们是同一件事的两侧：只做上半，运维不知道谁在用自己；
 * 只做下半，他没地方把本机接到别人那里去。
 */
export function PeersPage({
  canView,
  canManage,
  onChanged,
}: {
  canView: boolean;
  canManage: boolean;
  /** 上游注册会新建/改动 provider 与路由，通知外层刷新一次状态。 */
  onChanged?: () => void;
}) {
  const [upstreams, setUpstreams] = useState<PeerUpstream[]>([]);
  const [upstreamGatewayId, setUpstreamGatewayId] = useState('');
  const [upstreamsLoading, setUpstreamsLoading] = useState(true);
  const [upstreamsError, setUpstreamsError] = useState('');

  const [peers, setPeers] = useState<PeerGateway[]>([]);
  const [localGatewayId, setLocalGatewayId] = useState('');
  const [maxHops, setMaxHops] = useState(0);
  const [maxHopsDraft, setMaxHopsDraft] = useState('');
  const [maxHopsSaving, setMaxHopsSaving] = useState(false);
  const [allowCidrs, setAllowCidrs] = useState('');
  const [registrationOpen, setRegistrationOpen] = useState(true);
  const [peersLoading, setPeersLoading] = useState(true);
  const [peersError, setPeersError] = useState('');
  /** 正在调授权的下游网关；null = 关闭。 */
  const [grantsPeer, setGrantsPeer] = useState<PeerGateway | null>(null);

  const [draft, setDraft] = useState<UpstreamDraft | null>(null);
  const [draftError, setDraftError] = useState('');
  const [draftSaving, setDraftSaving] = useState(false);
  const [busyId, setBusyId] = useState('');
  // 「测试连接」的结果：按上游 providerId 记，成功也留一条——运维点完按钮要的
  // 是「连上了、同步到了什么」，只有失败才冒出来会让人反复点。
  const [testResults, setTestResults] = useState<Record<string, { ok: boolean; text: string }>>({});

  const loadUpstreams = useCallback(async () => {
    setUpstreamsLoading(true);
    try {
      const payload = await jsonRequest<PeerUpstreamsResponse>(`${API_BASE}/__peer-upstreams`);
      setUpstreams(payload.upstreams || []);
      setUpstreamGatewayId(payload.gatewayId || '');
      setUpstreamsError('');
    } catch (error) {
      setUpstreamsError((error as Error).message);
    } finally {
      setUpstreamsLoading(false);
    }
  }, []);

  const loadPeers = useCallback(async () => {
    if (!canView) {
      setPeersLoading(false);
      return;
    }
    setPeersLoading(true);
    try {
      const payload = await jsonRequest<PeersResponse>(`${API_BASE}/__peers`);
      setPeers(payload.peers || []);
      setLocalGatewayId(payload.gatewayId || '');
      setMaxHops(payload.maxHops || 0);
      setMaxHopsDraft(String(payload.maxHops || 0));
      setAllowCidrs(payload.registrationAllowCidrs || '');
      setRegistrationOpen(payload.registrationOpen ?? true);
      setPeersError('');
    } catch (error) {
      setPeersError((error as Error).message);
    } finally {
      setPeersLoading(false);
    }
  }, [canView]);

  useEffect(() => {
    void loadUpstreams();
    void loadPeers();
  }, [loadUpstreams, loadPeers]);

  // 删掉上游（普通 provider 删除）之后视觉上应当立刻消失，所以两个列表一起刷。
  const refreshAll = useCallback(async () => {
    await Promise.all([loadUpstreams(), loadPeers()]);
    onChanged?.();
  }, [loadUpstreams, loadPeers, onChanged]);

  const submitDraft = async () => {
    if (!draft) return;
    const parentUrl = draft.parentUrl.trim();
    if (!parentUrl) {
      setDraftError('请填写上游网关地址');
      return;
    }
    if (!draft.username.trim() || !draft.password.trim()) {
      setDraftError('用户名和密码都要填：第一次由你自己设定，之后每次注册填同一个');
      return;
    }
    setDraftSaving(true);
    setDraftError('');
    try {
      if (draft.providerId) {
        await jsonRequest(`${API_BASE}/__peer-upstreams/${encodeURIComponent(draft.providerId)}/reconnect`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            username: draft.username.trim(),
            password: draft.password,
            requestedProviderIds: draft.requestedProviderIds,
          }),
        });
      } else {
        await jsonRequest(`${API_BASE}/__peer-upstreams`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            parentUrl,
            username: draft.username.trim(),
            password: draft.password,
            name: draft.name.trim(),
            protocol: draft.protocol,
            requestedProviderIds: draft.requestedProviderIds,
          }),
        });
      }
      setDraft(null);
      await refreshAll();
    } catch (error) {
      setDraftError((error as Error).message);
    } finally {
      setDraftSaving(false);
    }
  };

  // 「测试连接」走 refresh：连得上就把上游最新的授权清单同步回来，连不上要说清
  // 是解析不到、被拒、还是凭据失效——只留一行红字等于让运维去翻服务端日志。
  const refreshUpstream = async (upstream: PeerUpstream) => {
    const id = upstream.providerId;
    setBusyId(id);
    setTestResults((current) => {
      const next = {...current};
      delete next[id];
      return next;
    });
    try {
      const payload = await jsonRequest<{ upstream?: PeerUpstream }>(
        `${API_BASE}/__peer-upstreams/${encodeURIComponent(id)}/refresh`,
        { method: 'POST' },
      );
      const synced = payload.upstream;
      const parts: string[] = ['连接正常'];
      if (synced?.parentGatewayId) parts.push(`上游身份 ${synced.parentGatewayId}`);
      if (synced?.grantsSynced) parts.push(`已同步 ${synced.grantCount} 个授权 Provider`);
      if (synced && synced.models.length > 0) parts.push(`${synced.models.length} 个可用模型`);
      setTestResults((current) => ({...current, [id]: {ok: true, text: `已同步来源授权清单；${parts.join('；')}`}}));
      await refreshAll();
    } catch (error) {
      // 上游拒绝时 jsonRequest 抛的是它那句错误正文，直接显示——比自己再翻译一遍准。
      setTestResults((current) => ({...current, [id]: {ok: false, text: `测试连接失败：${(error as Error).message}`}}));
      setUpstreamsError('');
      await loadUpstreams();
    } finally {
      setBusyId('');
    }
  };

  const revokePeer = async (peer: PeerGateway) => {
    const label = peer.name?.trim() || peer.username;
    const ok = await confirmDialog(
      `吊销「${label}」？\n它的对端凭据会立即失效，送进来的请求当场被拒。\n账号会保留为禁用状态、授权清单也还在——它重新注册一次就能恢复，不需要重新分配 Provider。`,
      { confirmLabel: '吊销', danger: true },
    );
    if (!ok) return;
    setBusyId(peer.userId);
    try {
      await jsonRequest(`${API_BASE}/__peers/${encodeURIComponent(peer.userId)}`, { method: 'DELETE' });
      await loadPeers();
    } catch (error) {
      setPeersError((error as Error).message);
    } finally {
      setBusyId('');
    }
  };

  // 断开一条上游：删的是「这一组」，不是一个协议的那一行。一次注册会落下多条
  // 成员行（每个协议一条），只删一条会留下半条链路——界面还连着、模型却少一半。
  // 本地断开不会让上游忘记这台下游，那要在上游自己的「网关互联」页里吊销，所以
  // 确认文案里把这层说清楚，免得以为删完就两边都干净了。
  const disconnectUpstream = async (upstream: PeerUpstream) => {
    const members = upstream.memberCount || 1;
    const ok = await confirmDialog(
      `断开「${upstream.name}」？\n本机会删掉这条上游的 ${members} 条成员行（可恢复），绑在它们上面的 API 密钥绑定会重置为空。\n上游那边的注册记录不会消失——要让上游也忘掉这台下游，请在上游的「网关互联」页吊销。`,
      { confirmLabel: '断开', danger: true },
    );
    if (!ok) return;
    setBusyId(upstream.providerId);
    try {
      await jsonRequest(`${API_BASE}/__peer-upstreams/${encodeURIComponent(upstream.providerId)}`, { method: 'DELETE' });
      await refreshAll();
    } catch (error) {
      setUpstreamsError((error as Error).message);
    } finally {
      setBusyId('');
    }
  };

  const saveCidrs = async (value: string) => {    try {
      const payload = await jsonRequest<{ cidrs: string }>(`${API_BASE}/__settings/peer-registration-cidrs`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ cidrs: value }),
      });
      setAllowCidrs(payload.cidrs || '');
      setRegistrationOpen(!(payload.cidrs || '').trim());
      setPeersError('');
    } catch (error) {
      setPeersError((error as Error).message);
    }
  };

  // 跳数上限：0（空）= 恢复默认 3。后端 clamp 到 1..8，界面上不需要再拦一遍。
  const saveMaxHops = async () => {
    const trimmed = maxHopsDraft.trim();
    const hops = trimmed === '' ? 0 : Number(trimmed);
    if (Number.isNaN(hops) || hops < 0) {
      setPeersError('跳数上限要是一个数字（1–8，留空恢复默认 3）');
      return;
    }
    setMaxHopsSaving(true);
    try {
      const payload = await jsonRequest<{ hops: number }>(`${API_BASE}/__settings/peer-max-hops`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ hops }),
      });
      setMaxHops(payload.hops || 0);
      setMaxHopsDraft(String(payload.hops || 0));
      setPeersError('');
    } catch (error) {
      setPeersError((error as Error).message);
    } finally {
      setMaxHopsSaving(false);
    }
  };

  const busy = (id: string) => busyId === id;

  return (
    <section className="section-full peers-page">
      <div className="card panel">
        <div className="panel-header">
          <div>
            <h2 className="panel-title">网关互联</h2>
            <p className="panel-desc">
              把本机接到另一个网关，或管理注册到本机的下游网关。透传的请求由上游用自己的凭据转发、记在它的账上，本机不接触上游凭据。
            </p>
          </div>
          <div className="panel-header-actions">
            {canManage ? (
              <button className="btn primary" type="button" onClick={() => { setDraftError(''); setDraft({ ...emptyUpstreamDraft }); }}>
                连接到上游网关
              </button>
            ) : null}
          </div>
        </div>
        <div className="peers-gateway-id-row">
          <GatewayIdChip gatewayId={localGatewayId || upstreamGatewayId} />
          <span className="hint-text">
            同一台机器上重装 app 身份不变，所以上游那边认的还是同一个下游。
          </span>
        </div>

        {upstreamsError ? <div className="rbac-form-error">{upstreamsError}</div> : null}
        {upstreamsLoading && upstreams.length === 0 ? <div className="empty-state">加载中…</div> : null}
        {!upstreamsLoading && upstreams.length === 0 && !upstreamsError ? (
          <div className="empty-state">
            还没有连接任何上游网关。{canManage ? '点「连接到上游网关」，填上游地址加一组你自己定的用户名和密码即可。' : ''}
          </div>
        ) : null}

        {upstreams.length > 0 ? (
          <div className="peer-upstream-list">
            <div className="peer-upstream-row peer-upstream-row-head">
              <span>上游</span>
              <span>协议</span>
              <span>可用模型</span>
              <span>最近状态</span>
              <span />
            </div>
            {upstreams.map((upstream) => (
              <div className="peer-upstream-row" key={upstream.providerId}>
                <div className="peer-upstream-main">
                  <div className="peer-upstream-name">
                    {upstream.name}
                    {upstream.enabled ? null : <Badge tone="slate">已禁用</Badge>}
                  </div>
                  <div className="peer-upstream-url" title={upstream.parentUrl}>{upstream.parentUrl}</div>
                  <div className="peer-upstream-meta">
                    {upstream.memberCount && upstream.memberCount > 1 ? (
                      <span title="一次注册按协议落下多条本机 Provider 行；「断开」删的是整组">
                        {upstream.memberCount} 条成员
                      </span>
                    ) : null}
                    {upstream.peerUsername ? <span>上游账号 {upstream.peerUsername}</span> : null}
                    {upstream.autoRenew ? <span title="已保存续签凭据；重启或 token 轮换后会自动恢复">自动续签已开启</span> : null}
                    {upstream.grantsSynced ? (
                      <span title={grantedProviderTitles(upstream)}>上游授权 {upstream.grantCount} 个 Provider</span>
                    ) : (
                      <span title="点「测试连接」会重新向上游查询授权清单">授权清单还没同步</span>
                    )}
                    {upstream.grantsSyncedAt ? <span>同步于 {formatTime(upstream.grantsSyncedAt)}</span> : null}
                  </div>
                </div>
                <div><Badge tone="blue">{protocolLabel(upstream.protocol)}</Badge></div>
                <div className="peer-upstream-models">
                  {upstream.models.length === 0 ? (
                    <span className="hint-text">
                      {!upstream.grantsSynced
                        ? '还没同步到上游的授权清单'
                        : upstream.grantCount > 0
                          // 授权了、但都不是这条链路的协议：说清楚，否则"没模型"看起来像故障。
                          ? `上游授权了 ${upstream.grantCount} 个 Provider，但其中没有 ${protocolLabel(upstream.protocol)} 的模型`
                          : '上游还没授权任何模型'}
                    </span>
                  ) : (
                    <>
                      <span>{upstream.models.slice(0, 3).join('、')}</span>
                      {upstream.models.length > 3 ? <span className="hint-text"> 等 {upstream.models.length} 个</span> : null}
                    </>
                  )}
                </div>
                <div>
                  <Badge tone={upstream.healthStatus === 'healthy' ? 'green' : upstream.healthStatus === 'failed' ? 'red' : 'slate'}>
                    {upstream.healthStatus === 'healthy' ? '正常' : upstream.healthStatus === 'failed' ? '异常' : '未探测'}
                  </Badge>
                </div>
                <div className="peer-upstream-actions">
                  {canManage ? (
                    <>
                      <button
                        className="mini-btn"
                        type="button"
                        disabled={busy(upstream.providerId)}
                        onClick={() => void refreshUpstream(upstream)}
                      >
                        {busy(upstream.providerId) ? '测试中…' : '测试连接'}
                      </button>
                      <button
                        className="mini-btn"
                        type="button"
                        onClick={() => {
                          setDraftError('');
                          setDraft({
                            providerId: upstream.providerId,
                            parentUrl: upstream.parentUrl,
                            username: '',
                            password: '',
                            name: upstream.name,
                            protocol: upstream.protocol,
                            requestedProviderIds: [],
                          });
                        }}
                      >
                        {upstream.autoRenew ? '更新注册凭据' : '启用自动续签'}
                      </button>
                      <button
                        className="mini-btn danger"
                        type="button"
                        disabled={busy(upstream.providerId)}
                        title="删掉这条上游在本机的全部成员行（可恢复）；上游那边的注册记录需在上游吊销"
                        onClick={() => void disconnectUpstream(upstream)}
                      >
                        断开
                      </button>
                    </>
                  ) : null}
                </div>
                {testResults[upstream.providerId] ? (
                  <div className={`peer-upstream-test-result ${testResults[upstream.providerId].ok ? 'ok' : 'bad'}`}>
                    {testResults[upstream.providerId].text}
                  </div>
                ) : null}
              </div>
            ))}
          </div>
        ) : null}
      </div>

      {canView ? (
        <div className="card panel">
          <div className="panel-header">
            <div>
              <h2 className="panel-title">注册到本机的下游网关</h2>
              <p className="panel-desc">
                下游用「本机地址 + 用户名 + 密码」注册上来，它的账号会自动出现在用户管理页里。
                授权哪些 Provider 决定它能透传什么；吊销会立刻停掉它的流量，账号与授权保留。
              </p>
            </div>
          </div>

          {peersError ? <div className="rbac-form-error">{peersError}</div> : null}
          {peersLoading && peers.length === 0 ? <div className="empty-state">加载中…</div> : null}
          {!peersLoading && peers.length === 0 && !peersError ? (
            <div className="empty-state">还没有下游网关注册到本机。</div>
          ) : null}

          {peers.length > 0 ? (
            <div className="peer-upstream-list">
              <div className="peer-upstream-row peer-upstream-row-head peer-downstream-row">
                <span>下游</span>
                <span>网关身份</span>
                <span>授权 Provider</span>
                <span>最近送进来的请求</span>
                <span />
              </div>
              {peers.map((peer) => (
                <div className="peer-upstream-row peer-downstream-row" key={peer.userId}>
                  <div className="peer-upstream-main">
                    <div className="peer-upstream-name">
                      {peer.name?.trim() || peer.username}
                      {peer.enabled ? null : <Badge tone="slate">已吊销</Badge>}
                      {peer.keyEnabled ? <Badge tone="green">凭据有效</Badge> : null}
                    </div>
                    <div className="peer-upstream-url" title={peer.username}>{peer.username}</div>
                  </div>
                  <div><code className="peer-gateway-code">{peer.gatewayId || '—'}</code></div>
                  <div className="peer-upstream-models">
                    {peer.grantMode === 'all' ? (
                      <div className="peer-grant-all-summary">
                        <Badge tone="green">全部 Provider · 动态</Badge>
                        <span className="hint-text">当前 {peer.grantedProviderIds.length} 个，以后新增自动包含</span>
                      </div>
                    ) : peer.grantedProviderIds.length === 0 ? (
                      <span className="hint-text">未授权</span>
                    ) : (
                      <span title={peer.grantedProviderIds.join('、')}>
                        {peer.grantedProviderIds.slice(0, 3).join('、')}
                        {peer.grantedProviderIds.length > 3 ? ` 等 ${peer.grantedProviderIds.length} 个` : ''}
                      </span>
                    )}
                  </div>
                  <div>
                    <span>{peer.ingressRequests} 次</span>
                    <div className="hint-text">最近 {formatTime(peer.lastSeenAt)}</div>
                  </div>
                  <div className="peer-upstream-actions">
                    {canManage ? (
                      <button
                        className="mini-btn"
                        type="button"
                        onClick={() => setGrantsPeer(peer)}
                      >
                        调授权
                      </button>
                    ) : null}
                    {canManage && peer.keyEnabled ? (
                      <button
                        className="mini-btn danger"
                        type="button"
                        disabled={busy(peer.userId)}
                        onClick={() => void revokePeer(peer)}
                      >
                        吊销
                      </button>
                    ) : null}
                  </div>
                </div>
              ))}
            </div>
          ) : null}

          {canManage ? (
            <div className="peer-cidr-editor">
              <Field
                fullWidth
                label="允许注册的来源"
                hint="留空 = 不限制来源（默认，公网下游可以直接注册）。填了网段就只放行这些来源，其它一律拒绝。注册本身靠的是下游自己定的用户名和密码，这里只是额外的收紧手段。多个网段用逗号或换行分隔。"
                value={allowCidrs}
                placeholder={registrationOpen ? '留空：不限制来源' : '例如 203.0.113.0/24'}
                multiline
                rows={2}
                onChange={(value) => setAllowCidrs(value)}
              />
              <Field
                fullWidth
                label="链路跳数上限"
                hint={`透传请求最多经过多少台网关（1–8，留空恢复默认 ${maxHops || 3}）。真实的多级链路（内网 → 公网 → 公网）走不通时可以调大；调小则在本机这一段更早拦住被伪造或误配的长链。`}
                value={maxHopsDraft}
                placeholder="3"
                onChange={(value) => setMaxHopsDraft(value.replace(/[^\d]/g, ''))}
              />
              <div className="peer-cidr-actions">
                <span className="hint-text">链路最多经过 {maxHops || 3} 跳；再有下一跳会被拒绝，避免父子互相绕成环。</span>
                <button className="btn" type="button" onClick={() => void saveCidrs(allowCidrs)}>保存来源限制</button>
                <button
                  className="btn"
                  type="button"
                  disabled={maxHopsSaving || maxHopsDraft.trim() === String(maxHops || 0)}
                  onClick={() => void saveMaxHops()}
                >
                  {maxHopsSaving ? '保存中…' : '保存跳数上限'}
                </button>
              </div>
            </div>
          ) : null}
        </div>
      ) : null}

      {grantsPeer ? (
        <PeerGrantsModal
          peer={grantsPeer}
          onClose={() => setGrantsPeer(null)}
          onSaved={async () => {
            setGrantsPeer(null);
            await loadPeers();
          }}
          onToast={(text) => setPeersError(text)}
        />
      ) : null}

      {draft ? (
        <UpstreamDraftModal
          draft={draft}
          error={draftError}
          saving={draftSaving}
          onChange={(next) => setDraft(next)}
          onClose={() => { if (!draftSaving) { setDraft(null); setDraftError(''); } }}
          onSubmit={() => void submitDraft()}
        />
      ) : null}
    </section>
  );
}

function UpstreamDraftModal({
  draft,
  error,
  saving,
  onChange,
  onClose,
  onSubmit,
}: {
  draft: UpstreamDraft;
  error: string;
  saving: boolean;
  onChange: (draft: UpstreamDraft) => void;
  onClose: () => void;
  onSubmit: () => void;
}) {
  const [protocolChoices] = useState(() =>
    PROTOCOL_OPTIONS.map((option): IconSelectOption => ({ value: option.value, label: option.label })));
  const protocolValue = useMemo(() => draft.protocol, [draft.protocol]);
  const editing = Boolean(draft.providerId);
  // 「测试连接」：只问上游能不能连通、允不允许本机注册，不建账号、不换凭据。
  const [probing, setProbing] = useState(false);
  const [probeResult, setProbeResult] = useState<{ ok: boolean; text: string } | null>(null);
  // 每次探测递增；回来的结果不是最新那一次就丢弃，避免改了地址后旧结果盖上来。
  const probeSeq = useRef(0);
  // 弹窗关闭（含测试进行中）之后结果不能再写进已经卸载的组件。
  const mounted = useRef(true);
  useEffect(() => () => { mounted.current = false; }, []);

  const parentUrl = draft.parentUrl.trim();
  const busy = saving || probing;

  const runProbe = async () => {
    if (!parentUrl) {
      setProbeResult({ ok: false, text: '请先填写上游网关地址' });
      return;
    }
    const seq = ++probeSeq.current;
    setProbing(true);
    setProbeResult(null);
    try {
      const payload = await jsonRequest<{
        parentUrl?: string;
        gatewayId?: string;
        registrationAllowed?: boolean;
        message?: string;
      }>(`${API_BASE}/__peer-upstreams/probe`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ parentUrl }),
      });
      if (!mounted.current || seq !== probeSeq.current) return;
      const parts = [payload.message || '连接正常'];
      if (payload.gatewayId) parts.push(`上游身份 ${payload.gatewayId}`);
      setProbeResult({
        ok: payload.registrationAllowed !== false,
        text: parts.join('；'),
      });
    } catch (probeError) {
      if (!mounted.current || seq !== probeSeq.current) return;
      setProbeResult({ ok: false, text: (probeError as Error).message });
    } finally {
      if (mounted.current && seq === probeSeq.current) setProbing(false);
    }
  };

  return (
    <Modal
      title={editing ? '重新注册上游网关' : '连接到上游网关'}
      description={editing
        ? '用第一次注册时填的那组用户名和密码重新注册。成功后会自动更新续签凭据，之后重启无需再次登录。'
        : '用户名和密码由你自己定，上游会照此给本机建一个账号。注册成功后本机会保存独立续签凭据，后续重启自动恢复。'}
      onClose={busy ? () => {} : onClose}
    >
      <div className="form-grid">
        {editing ? null : (
          <Field
            fullWidth
            label="上游网关地址"
            hint="公网地址直接填即可（例如网关的独立 API 域名）：只填主机名时默认按 https。粘进来的 /__peer/... 路径会被去掉。"
            value={draft.parentUrl}
            placeholder="vps1.example.com"
            onChange={(value) => {
              // 换了地址，上一次的探测结果就不再对应这个地址了。
              probeSeq.current += 1;
              setProbing(false);
              setProbeResult(null);
              onChange({ ...draft, parentUrl: value });
            }}
          />
        )}
        <div className="field field-full">
          <div className="field-label-row">
            <label>{editing ? '连通性' : '先测一下'}</label>
          </div>
          <div className="peer-probe-row">
            <button className="btn" type="button" disabled={busy} onClick={() => void runProbe()}>
              {probing ? '测试中…' : '测试连接'}
            </button>
            <span className="hint-text peer-probe-text">
              {editing
                ? draft.parentUrl
                : '只检查能否连通、上游是否允许本机注册；不创建账号、不换凭据。可以填完地址先测，再填用户名密码。'}
            </span>
          </div>
          {probeResult ? (
            <div className={probeResult.ok ? 'peer-probe-ok' : 'rbac-form-error'}>{probeResult.text}</div>
          ) : null}
        </div>
        <Field
          label="用户名"
          hint="第一次注册时自己填一个（例如 peer-<本机身份后几位>），上游照此建账号；之后每次重新注册都要填同一个。"
          value={draft.username}
          placeholder="自己定一个用户名"
          onChange={(value) => onChange({ ...draft, username: value })}
        />
        <Field
          label="密码"
          hint="同样是第一次注册时自己设定（至少 8 位），之后重新注册必须一字不差。上游只存摘要，本机不保存密码。"
          value={draft.password}
          placeholder="自己设定一个密码"
          onChange={(value) => onChange({ ...draft, password: value })}
        />
        {editing ? null : (
          <>
            <Field
              label="显示名称"
              hint="留空就用上游主机名。这条也是本机新建的 Provider 名字。"
              value={draft.name}
              placeholder="vps1-远端"
              onChange={(value) => onChange({ ...draft, name: value })}
            />
            <IconSelect
              label="协议"
              value={protocolValue}
              options={protocolChoices}
              onChange={(value) => onChange({ ...draft, protocol: value as Protocol })}
              hint="一条 Provider 只能说一种协议。想同时用上游的 OpenAI 和 Claude，就按两个协议各连一次。"
            />
          </>
        )}
      </div>
      {error ? <div className="rbac-form-error">{error}</div> : null}
      <div className="modal-actions">
        <button className="btn" type="button" onClick={onClose} disabled={busy}>取消</button>
        <button className="btn primary" type="button" onClick={onSubmit} disabled={busy}>
          {saving ? '注册中…' : editing ? '重新注册' : '注册'}
        </button>
      </div>
    </Modal>
  );
}

/**
 * 调整一个已注册下游网关的授权（按层授权的编辑入口）。
 *
 * 注册时的预置授权只是一次快照；上游随时可以新增或收回，立即生效、不需要
 * 下游重新注册。清单与用户管理页的「分配 Provider」同一份来源
 * （/__roles/grant-targets），交互样式也保持一致，管理员两边看到的是同一件事。
 * 空列表 = 收回全部授权，下游下一次请求就拿不到任何 provider，弹窗里明说。
 */
function PeerGrantsModal({ peer, onClose, onSaved, onToast }: {
  peer: PeerGateway;
  onClose: () => void;
  onSaved: () => Promise<void>;
  onToast: (text: string) => void;
}) {
  const [granted, setGranted] = useState<string[]>(peer.grantedProviderIds || []);
  const [grantMode, setGrantMode] = useState<'selected' | 'all'>(peer.grantMode === 'all' ? 'all' : 'selected');
  const [targets, setTargets] = useState<GrantTarget[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [saving, setSaving] = useState(false);
  const label = peer.name?.trim() || peer.username;

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setLoadError('');
    jsonRequest<GrantTarget[]>(`${API_BASE}/__roles/grant-targets`)
      .then((list) => { if (!cancelled) setTargets(list || []); })
      .catch((error) => { if (!cancelled) setLoadError((error as Error).message); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, []);

  // 搜索、全选、勾选全部收进 ProviderGrantPicker：与用户管理页同一个控件。

  async function submit() {
    setSaving(true);
    try {
      await jsonRequest(`${API_BASE}/__peers/${encodeURIComponent(peer.userId)}/grants`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(grantMode === 'all'
          ? { grantMode: 'all' }
          : { grantMode: 'selected', providerIds: granted }),
      });
      await onSaved();
    } catch (error) {
      onToast((error as Error).message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      title={`调整下游网关授权：${label}`}
      description="选择下游能透传的 Provider。动态全部模式会自动包含以后新增的 Provider，不需要重新调节授权。"
      size="xwide"
      onClose={onClose}
    >
      <div className="peer-grant-mode-grid" role="radiogroup" aria-label="Provider 授权模式">
        <button
          type="button"
          role="radio"
          aria-checked={grantMode === 'selected'}
          className={`peer-grant-mode-card${grantMode === 'selected' ? ' active' : ''}`}
          onClick={() => setGrantMode('selected')}
        >
          <span className="peer-grant-mode-icon" aria-hidden="true">☷</span>
          <span className="peer-grant-mode-copy">
            <b>指定 Provider</b>
            <small>只授权当前勾选的对象，后续新增需要再次配置</small>
          </span>
          <span className="peer-grant-mode-check" aria-hidden="true">{grantMode === 'selected' ? '✓' : ''}</span>
        </button>
        <button
          type="button"
          role="radio"
          aria-checked={grantMode === 'all'}
          className={`peer-grant-mode-card dynamic${grantMode === 'all' ? ' active' : ''}`}
          onClick={() => setGrantMode('all')}
        >
          <span className="peer-grant-mode-icon" aria-hidden="true">∞</span>
          <span className="peer-grant-mode-copy">
            <b>全部 Provider <span className="peer-grant-recommended">推荐</span></b>
            <small>动态包含当前与未来新增，父网关新增后自动对下游可见</small>
          </span>
          <span className="peer-grant-mode-check" aria-hidden="true">{grantMode === 'all' ? '✓' : ''}</span>
        </button>
      </div>
      {grantMode === 'all' ? (
        <div className="peer-grant-dynamic-panel">
          <div className="peer-grant-dynamic-orbit" aria-hidden="true">∞</div>
          <div>
            <strong>动态全量授权已启用</strong>
            <p>
              当前覆盖 {targets.length} 个 Provider。之后在父网关新增的 Provider 也会自动纳入，无需重新保存本页授权；
              下游会周期同步清单，并即时影响新请求可用的 Provider 集合。
            </p>
          </div>
        </div>
      ) : null}
      {loading ? <div className="empty-state compact">加载中…</div> : null}
      {loadError ? <div className="rbac-form-error">{loadError}</div> : null}
      {grantMode === 'selected' && !loading && !loadError && targets.length === 0 ? (
        <div className="empty-state compact">本机还没有可授权的输入 Provider。</div>
      ) : null}
      {grantMode === 'selected' && targets.length > 0 ? (
        <>
          {/* 与用户管理页同一个授权选择器：左「可选」右「已授权」。
              这里原来是一片三列复选框网格，和用户管理页各写一遍、措辞还不一样；
              统一之后两处的交互、计数、批量动作完全一致。 */}
          <ProviderGrantPicker
            targets={targets}
            granted={granted}
            onChange={(next) => setGranted(next)}
            loading={loading}
            error={loadError}
            emptyText="本机还没有可授权的输入 Provider。"
          />
          {granted.length === 0 ? (
            <div className="hint-text">不授权任何 Provider 并保存 = 收回全部授权：它送来的透传请求将拿不到任何 Provider。</div>
          ) : null}
        </>
      ) : null}
      <div className="actions modal-actions">
        <button type="button" className="btn" disabled={saving} onClick={onClose}>取消</button>
        <button type="button" className="btn primary" disabled={saving || loading} onClick={() => void submit()}>
          {saving ? '保存中…' : grantMode === 'all' ? '保存为全部可见' : '保存授权'}
        </button>
      </div>
    </Modal>
  );
}
