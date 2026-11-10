import React, {useCallback, useEffect, useMemo, useState} from 'react';
import {API_BASE, protocolLabel} from '../lib';
import {Badge, Field, Modal} from './ui';
import {confirmDialog} from '../dialogs';
import {IconSelect, IconSelectOption} from './selects';
import type {GrantTarget} from './rbac_types';
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
  const [allowsPrivate, setAllowsPrivate] = useState(true);
  const [peersLoading, setPeersLoading] = useState(true);
  const [peersError, setPeersError] = useState('');
  /** 正在调授权的下游网关；null = 关闭。 */
  const [grantsPeer, setGrantsPeer] = useState<PeerGateway | null>(null);

  const [draft, setDraft] = useState<UpstreamDraft | null>(null);
  const [draftError, setDraftError] = useState('');
  const [draftSaving, setDraftSaving] = useState(false);
  const [busyId, setBusyId] = useState('');

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
      setAllowsPrivate(payload.registrationAllowsPrivate ?? true);
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
      setDraftError('请填写上游给本机开的用户名和密码');
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

  const refreshUpstream = async (upstream: PeerUpstream) => {
    setBusyId(upstream.providerId);
    try {
      await jsonRequest(`${API_BASE}/__peer-upstreams/${encodeURIComponent(upstream.providerId)}/refresh`, {
        method: 'POST',
      });
      await refreshAll();
    } catch (error) {
      setUpstreamsError((error as Error).message);
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

  const saveCidrs = async (value: string) => {
    try {
      const payload = await jsonRequest<{ cidrs: string }>(`${API_BASE}/__settings/peer-registration-cidrs`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ cidrs: value }),
      });
      setAllowCidrs(payload.cidrs || '');
      setAllowsPrivate(!(payload.cidrs || '').trim());
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
              把本机接到另一个网关，或者管理注册到本机的下游网关。透传的请求由上游用它自己的凭据转发、记在它的账上；
              本机从不接触上游的凭据，也不复制它的 Provider。
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
            还没有连接任何上游网关。{canManage ? '点「连接到上游网关」，用上游那边给本机开的用户名和密码注册即可。' : ''}
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
                </div>
                <div><Badge tone="blue">{protocolLabel(upstream.protocol)}</Badge></div>
                <div className="peer-upstream-models">
                  {upstream.models.length === 0 ? (
                    <span className="hint-text">上游还没授权任何模型</span>
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
                        重新注册
                      </button>
                    </>
                  ) : null}
                </div>
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
                    {peer.grantedProviderIds.length === 0 ? (
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
                hint="留空 = 只允许本机与内网（默认）。公网部署时必须在这里写出允许的来源网段，否则注册会被拒。多个网段用逗号或换行分隔。"
                value={allowCidrs}
                placeholder={allowsPrivate ? '留空：仅本机 / 内网' : '例如 203.0.113.0/24'}
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
  return (
    <Modal
      title={editing ? '重新注册上游网关' : '连接到上游网关'}
      description={editing
        ? '用上游给本机开的用户名和密码重新注册。会换发一把新凭据，上游那边旧的立刻失效；模型清单按上游最新授权更新。'
        : '用上游那边给本机开的用户名和密码注册。注册成功后本机会多一条 Provider（authType=peer）和一条同协议的路由，密钥绑定到那条路由就能用。'}
      onClose={onClose}
    >
      <div className="form-grid">
        {editing ? null : (
          <Field
            fullWidth
            label="上游网关地址"
            hint="只填主机名也可以，默认按 https。粘进来的 /__peer/... 路径会被去掉。"
            value={draft.parentUrl}
            placeholder="vps1.example.com"
            onChange={(value) => onChange({ ...draft, parentUrl: value })}
          />
        )}
        <Field
          label="上游给的用户名"
          value={draft.username}
          placeholder="peer-xxxxxxxx"
          onChange={(value) => onChange({ ...draft, username: value })}
        />
        <Field
          label="密码"
          value={draft.password}
          placeholder="上游设置的那个密码"
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
        <button className="btn" type="button" onClick={onClose} disabled={saving}>取消</button>
        <button className="btn primary" type="button" onClick={onSubmit} disabled={saving}>
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
  const [targets, setTargets] = useState<GrantTarget[]>([]);
  const [keyword, setKeyword] = useState('');
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

  const grantedSet = useMemo(() => new Set(granted), [granted]);
  const visibleTargets = useMemo(() => {
    const word = keyword.trim().toLowerCase();
    if (!word) return targets;
    return targets.filter((target) =>
      target.name.toLowerCase().includes(word) || target.ownerName.toLowerCase().includes(word));
  }, [targets, keyword]);
  const allVisibleSelected = visibleTargets.length > 0 && visibleTargets.every((target) => grantedSet.has(target.id));

  function toggleGrant(id: string, on: boolean) {
    setGranted((current) => on
      ? Array.from(new Set([...current, id]))
      : current.filter((item) => item !== id));
  }

  function toggleAllVisible(on: boolean) {
    const ids = visibleTargets.map((target) => target.id);
    setGranted((current) => (on
      ? Array.from(new Set([...current, ...ids]))
      : current.filter((id) => !ids.includes(id))));
  }

  async function submit() {
    setSaving(true);
    try {
      await jsonRequest(`${API_BASE}/__peers/${encodeURIComponent(peer.userId)}/grants`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ providerIds: granted }),
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
      description="勾选它能透传到哪些 Provider，取消勾选立即收回。改动立即生效，不需要它重新注册。"
      size="wide"
      onClose={onClose}
    >
      {loading ? <div className="empty-state compact">加载中…</div> : null}
      {loadError ? <div className="rbac-form-error">{loadError}</div> : null}
      {!loading && !loadError && targets.length === 0 ? (
        <div className="empty-state compact">本机还没有可授权的输入 Provider。</div>
      ) : null}
      {targets.length > 0 ? (
        <>
          <div className="rbac-grant-toolbar">
            <div className="rbac-search">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <circle cx="11" cy="11" r="8" />
                <line x1="21" y1="21" x2="16.65" y2="16.65" />
              </svg>
              <input
                type="search"
                value={keyword}
                placeholder="搜索名称或归属账号…"
                aria-label="搜索可授权的输入 Provider"
                autoComplete="off"
                onChange={(event) => setKeyword(event.target.value)}
              />
              {keyword ? (
                <button type="button" className="rbac-search-clear" title="清空搜索" onClick={() => setKeyword('')}>×</button>
              ) : null}
            </div>
            <button
              type="button"
              className="btn secondary rbac-grant-all"
              disabled={visibleTargets.length === 0}
              title={keyword.trim() ? '只作用于当前搜索到的结果' : '勾选全部可授权的 Provider'}
              onClick={() => toggleAllVisible(!allVisibleSelected)}
            >
              {allVisibleSelected ? '取消全选' : '全选'}
              {keyword.trim() ? `（${visibleTargets.length}）` : ''}
            </button>
          </div>
          {visibleTargets.length === 0 ? (
            <div className="empty-state compact">没有匹配「{keyword.trim()}」的 Provider。</div>
          ) : (
            <div className="rbac-grant-list">
              {visibleTargets.map((target) => {
                const checked = grantedSet.has(target.id);
                return (
                  <button
                    key={target.id}
                    type="button"
                    role="checkbox"
                    aria-checked={checked}
                    className={`rbac-grant-item${checked ? ' checked' : ''}`}
                    onClick={() => toggleGrant(target.id, !checked)}
                  >
                    <span className={`provider-checklist-box${checked ? ' on' : ''}`} aria-hidden="true">{checked ? '✓' : ''}</span>
                    <span className="rbac-grant-name" title={target.name}>{target.name}</span>
                    <span className="rbac-grant-owner" title={`归属：${target.ownerName}`}>归属 {target.ownerName}</span>
                  </button>
                );
              })}
            </div>
          )}
          {granted.length === 0 ? (
            <div className="hint-text">不勾选任何 Provider 并保存 = 收回全部授权：它送来的透传请求将拿不到任何 Provider。</div>
          ) : null}
        </>
      ) : null}
      <div className="actions modal-actions">
        <button type="button" className="btn" disabled={saving} onClick={onClose}>取消</button>
        <button type="button" className="btn primary" disabled={saving || loading} onClick={() => void submit()}>
          {saving ? '保存中…' : '保存授权'}
        </button>
      </div>
    </Modal>
  );
}
