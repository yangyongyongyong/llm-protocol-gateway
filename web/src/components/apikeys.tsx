// @generated-from main.tsx — 由重构脚本拆分生成，请直接维护本文件。
import React from 'react';
import { apiKeyClientBaseURL, apiKeyGatewayRoot, buildApiKeyClientConfig, buildApiKeyClientConfigExtras, buildApiKeyClientConfigInstallScript, buildApiKeyCodexRestoreOfficialScript, buildApiKeyPublicCurl, clientConfigFilePath, clientConfigProtocolHint, clientConfigScriptNoun, clientConfigTitle, clientConfigsForProtocol, fixedOutputLabels, getApiKeyBinding, localGatewayRoot, protocolFromLabel, protocolLabel, providerOptionLabel, thinkingDepthSelectOptions } from '../lib';
import { SearchableModelSelect } from './selects';
import { APIKey, ConsoleUser, KeyProfile, Model, OutputEndpoint, Protocol, Provider, Route } from '../types';
import { Badge, CopyButton, Modal } from './ui';
/** 方案命名弹窗：替代 window.prompt——嵌入式浏览器/被浏览器拦截时 prompt 会静默
 *  返回 null，表现为按钮点了没反应；应用内弹窗不依赖原生对话框。 */
export function ProfileNameDialog({ mode, initial, busy, onSubmit, onClose }: { mode: 'create' | 'rename'; initial: string; busy: boolean; onSubmit: (name: string) => void; onClose: () => void }) {
  const [value, setValue] = React.useState(initial);
  const inputRef = React.useRef<HTMLInputElement | null>(null);
  React.useEffect(() => {
    inputRef.current?.focus();
    inputRef.current?.select();
  }, []);
  const trimmed = value.trim();
  return (
    <Modal
      title={mode === 'create' ? '新建转发方案' : '重命名转发方案'}
      description={mode === 'create'
        ? '将完整复制当前生效方案的转发配置（Provider / 备选 / 模型映射等），创建后立即生效。'
        : '仅修改方案名称，Key 的 token 与转发配置不受影响。'}
      onClose={onClose}
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (busy || !trimmed) return;
          onSubmit(trimmed);
        }}
      >
        <div className="form-grid modal-form">
          <div className="field field-full">
            <label>方案名称</label>
            <input
              ref={inputRef}
              value={value}
              disabled={busy}
              placeholder={mode === 'create' ? '例如：GPT-5.6 高质量' : ''}
              onChange={(event) => setValue(event.target.value)}
            />
          </div>
        </div>
        <div className="actions modal-actions">
          <button type="button" className="btn" onClick={onClose}>取消</button>
          <button type="submit" className="btn primary" disabled={busy || !trimmed}>{busy ? '保存中…' : mode === 'create' ? '创建方案' : '保存'}</button>
        </div>
      </form>
    </Modal>
  );
}

export function ApiKeyDetailPanel({
  keyItem,
  providers,
  routes,
  models,
  endpoints,
  saving,
  testingProviderID,
  tunnelRunning,
  livePublicURL,
  fixedOutputLabels,
  onUpdateField,
  onUpdateBinding,
  onUpdateModelAliases,
  onUpdateFallbacks,
  onDelete,
  onClone,
  onRefreshModels,
  onToast,
  owners,
  onUpdateOwner,
  onSwitchProfile,
  onCreateProfile,
  onUpdateProfile,
  onDeleteProfile,
}: {
  keyItem: APIKey;
  providers: Provider[];
  routes: Route[];
  models: Model[];
  endpoints: OutputEndpoint[];
  saving: boolean;
  testingProviderID: string;
  tunnelRunning: boolean;
  livePublicURL: string;
  fixedOutputLabels: string[];
  onUpdateField: (key: APIKey, field: 'name' | 'routeId' | 'modelOverride' | 'thinkingDepthOverride' | 'maxOutputTokens' | 'streamEnabled' | 'codexKeepOfficialLogin' | 'enabled', value: string | boolean | number) => Promise<void>;
  onUpdateBinding: (key: APIKey, providerId: string, outputProtocol: Protocol) => Promise<void>;
  onUpdateModelAliases: (key: APIKey, modelAliases: Record<string, string>) => Promise<void>;
  onUpdateFallbacks: (key: APIKey, fallbackProviderIds: string[], fallbackModelOverrides: Record<string, string>) => Promise<void>;
  onDelete: (key: APIKey) => Promise<void>;
  onClone: (key: APIKey) => void;
  onRefreshModels: (providerId: string, providerName: string) => Promise<void>;
  onToast?: (message: string) => void;
  // 用户归属（仅管理员可见/可改）；owners 为空时不渲染
  owners?: ConsoleUser[];
  onUpdateOwner?: (key: APIKey, ownerUserId: string) => Promise<void>;
  // 转发方案（多套配置 + 一键切换）
  onSwitchProfile: (key: APIKey, profileId: string) => Promise<void>;
  onCreateProfile: (key: APIKey, profile: Partial<KeyProfile>, activate: boolean) => Promise<void>;
  onUpdateProfile: (key: APIKey, profileId: string, profile: Partial<KeyProfile>) => Promise<void>;
  onDeleteProfile: (key: APIKey, profileId: string) => Promise<void>;
}) {
  const { route, binding, routeProvider, bindingAction } = getApiKeyBinding(keyItem, routes, providers);
  const modelOptions = routeProvider ? models.filter((model) => model.providerId === routeProvider.id) : [];
  const publicAvailable = Boolean(tunnelRunning && livePublicURL);
  const defaultPublicBase = publicAvailable ? livePublicURL : '';
  const apiKeyClientURL = route ? apiKeyClientBaseURL(route, endpoints, defaultPublicBase) : '';
  const [clientConfigModal, setClientConfigModal] = React.useState<'opencode' | 'codex' | 'claude' | null>(null);
  const [fallbackModalOpen, setFallbackModalOpen] = React.useState(false);
  const profiles = keyItem.profiles || [];
  const activeProfileId = keyItem.activeProfileId || '';
  const activeProfile = profiles.find((item) => item.id === activeProfileId);
  const fallbackIds = keyItem.fallbackProviderIds || [];
  const fallbackModelOverrides = keyItem.fallbackModelOverrides || {};
  const activeProviderId = keyItem.activeProviderId || binding.providerId;
  const activeProvider = providers.find((item) => item.id === activeProviderId);
  const usingFallback = Boolean(keyItem.activeProviderId && keyItem.activeProviderId !== binding.providerId);
  const activeFallbackModel = usingFallback ? (fallbackModelOverrides[keyItem.activeProviderId || ''] || '') : '';

  function openClientConfigModal(client: 'opencode' | 'codex' | 'claude') {
    setClientConfigModal(client);
  }

  // 新建方案 = 完整克隆当前 Key 顶层转发配置（备选 / 模型映射等一并带上）。
  function snapshotCurrentProfile(name: string): Partial<KeyProfile> {
    return {
      name,
      routeId: keyItem.routeId,
      modelOverride: keyItem.modelOverride,
      modelAliases: { ...(keyItem.modelAliases || {}) },
      thinkingDepthOverride: keyItem.thinkingDepthOverride,
      maxOutputTokens: keyItem.maxOutputTokens && keyItem.maxOutputTokens > 0 ? keyItem.maxOutputTokens : 0,
      fallbackProviderIds: [...(keyItem.fallbackProviderIds || [])],
      fallbackModelOverrides: { ...(keyItem.fallbackModelOverrides || {}) },
      streamEnabled: keyItem.streamEnabled !== false,
    };
  }

  // 方案创建/重命名走应用内弹窗：window.prompt 在部分嵌入式浏览器会被静默拦截，
  // 表现为按钮点击毫无反应（“假按钮”）。
  const [profileDialog, setProfileDialog] = React.useState<'create' | 'rename' | null>(null);

  async function submitProfileName(name: string) {
    if (!profileDialog) return;
    try {
      if (profileDialog === 'create') {
        await onCreateProfile(keyItem, snapshotCurrentProfile(name), true);
      } else if (activeProfile) {
        await onUpdateProfile(keyItem, activeProfile.id, { ...activeProfile, name });
      }
      setProfileDialog(null);
    } catch {
      // 失败时保留弹窗供修改重试；错误提示由数据层的 toast 负责
    }
  }

  async function handleDeleteProfile() {
    if (!activeProfile) return;
    if (profiles.length <= 1) {
      onToast?.('至少保留一套方案，或先新建再删除');
      return;
    }
    if (!window.confirm(`删除方案「${activeProfile.name}」？删除后将切换到其余方案之一。`)) return;
    await onDeleteProfile(keyItem, activeProfile.id);
  }

  return (
    <div className="api-keys-detail card">
      <div className="route-top api-keys-detail-head">
        <div className="route-name">{keyItem.name}</div>
        <div className="route-actions">
          <Badge tone={keyItem.enabled ? 'green' : 'slate'}>{keyItem.enabled ? '启用' : '禁用'}</Badge>
          <Badge tone={bindingAction === '透传' ? 'green' : 'cyan'}>{bindingAction}</Badge>
          {usingFallback ? <Badge tone="amber">已切备选</Badge> : null}
          {route ? <CopyButton value={apiKeyClientURL} label="复制 URL" toastContent={`已复制 URL：${apiKeyClientURL}`} /> : null}
          <CopyButton value={keyItem.key} label="复制 Key" toastContent={`已复制 Key：${keyItem.key}`} />
          <button className="icon-btn" onClick={() => onClone(keyItem)} title="克隆为新 API 密钥">克隆</button>
          <button className="icon-btn danger" onClick={() => void onDelete(keyItem)} title="删除">删除</button>
        </div>
      </div>

      {profileDialog ? (
        <ProfileNameDialog
          mode={profileDialog}
          initial={profileDialog === 'rename' ? activeProfile?.name || '' : ''}
          busy={saving}
          onSubmit={(name) => void submitProfileName(name)}
          onClose={() => setProfileDialog(null)}
        />
      ) : null}

      <div className="api-key-profile-selector">
        <div className="field-label-row">
          <label>转发方案</label>
          <div className="api-key-profile-toolbar">
            <button className="btn" type="button" disabled={saving} onClick={() => setProfileDialog('create')}>
              新建方案
            </button>
            {activeProfile ? (
              <button className="btn" type="button" disabled={saving} onClick={() => setProfileDialog('rename')}>
                重命名
              </button>
            ) : null}
            {activeProfile && profiles.length > 1 ? (
              <button className="btn danger" type="button" disabled={saving} onClick={() => void handleDeleteProfile()}>
                删除当前
              </button>
            ) : null}
          </div>
        </div>
        <div className="hint-line">
          同一 Key（token 不变）可切换多套完整转发配置。下方整页表单即当前生效方案（Provider / 备选 / 模型映射等）；切换方案后新请求立即走新配置。
        </div>
        <div className="api-key-profile-tabs" role="tablist" aria-label="转发方案">
          {profiles.length === 0 ? (
            <button className="api-key-profile-tab active" type="button" role="tab" aria-selected="true" disabled>
              当前配置
            </button>
          ) : (
            profiles.map((profile) => {
              const isActive = profile.id === activeProfileId;
              return (
                <button
                  key={profile.id}
                  className={`api-key-profile-tab${isActive ? ' active' : ''}`}
                  type="button"
                  role="tab"
                  aria-selected={isActive}
                  disabled={saving || isActive}
                  onClick={() => void onSwitchProfile(keyItem, profile.id)}
                  title={isActive ? '当前生效方案' : `切换到「${profile.name}」`}
                >
                  {profile.name}
                  {isActive ? <span className="api-key-profile-tab-mark">生效</span> : null}
                </button>
              );
            })
          )}
        </div>
      </div>

      <div className="detail-section">
        <div className="detail-section-title">基本绑定</div>
        <div className="form-grid compact">
          <ApiKeyNameField
            name={keyItem.name}
            disabled={saving}
            onSave={(name) => onUpdateField(keyItem, 'name', name)}
          />
          <div className="field">
            <label>输入 Provider（首选）</label>
            <select
              value={binding.providerId}
              disabled={saving}
              onChange={(event) => {
                void onUpdateBinding(keyItem, event.target.value, binding.outputProtocol);
              }}
            >
              {providers.map((item) => <option key={item.id} value={item.id}>{providerOptionLabel(item)}</option>)}
            </select>
            <div className="hint-line">
              {routeProvider ? `输入协议：${protocolLabel(routeProvider.protocol)}（由 Provider 决定，不可单独修改）` : '未绑定'}
            </div>
          </div>
          {owners && onUpdateOwner ? (
            <div className="field">
              <label>所属用户</label>
              <select
                value={keyItem.ownerUserId || ''}
                disabled={saving}
                onChange={(event) => void onUpdateOwner(keyItem, event.target.value)}
              >
                <option value="">管理员</option>
                {owners.map((user) => <option key={user.id} value={user.id}>{user.username}</option>)}
              </select>
            </div>
          ) : null}
          <div className="field">
            <label>输出协议</label>
            <select
              value={protocolLabel(binding.outputProtocol)}
              disabled={saving || !binding.providerId}
              onChange={(event) => {
                void onUpdateBinding(keyItem, binding.providerId, protocolFromLabel(event.target.value));
              }}
            >
              {fixedOutputLabels.map((label) => <option key={label} value={label}>{label}</option>)}
            </select>
          </div>
        </div>
      </div>

      <div className="detail-section">
        <div className="detail-section-title">备选与故障转移</div>
        <div className="api-key-fallback-summary">
          <div className="hint-line">
            当前在用：{activeProvider ? providerOptionLabel(activeProvider) : '未绑定'}
            {usingFallback ? `（已从首选故障转移${activeFallbackModel ? ` · 模型 ${activeFallbackModel}` : ''}）` : ''}
          </div>
          <div className="hint-line">
            {fallbackIds.length === 0
              ? '未配置备选。首选额度耗尽后将无法自动切换。'
              : `备选顺序：${fallbackIds.map((id, index) => {
                const provider = providers.find((item) => item.id === id);
                const model = fallbackModelOverrides[id] || '未选模型';
                return `${index + 1}. ${provider ? providerOptionLabel(provider) : id} → ${model}`;
              }).join(' ； ')}`}
          </div>
          <div>
            <button className="btn" type="button" disabled={saving || !binding.providerId || providers.length < 2} onClick={() => setFallbackModalOpen(true)}>
              配置备选
            </button>
          </div>
        </div>
      </div>

      <div className="detail-section">
        <div className="detail-section-title">模型与生成参数</div>
        <div className="form-grid compact">
          <ApiKeyFixedModelField
            value={keyItem.modelOverride || ''}
            models={modelOptions}
            disabled={saving || !routeProvider}
            refreshing={routeProvider ? testingProviderID === routeProvider.id : false}
            onChange={(value) => void onUpdateField(keyItem, 'modelOverride', value)}
            onRefresh={() => routeProvider ? void onRefreshModels(routeProvider.id, routeProvider.name) : undefined}
          />
          <div className="field">
            <label>思考深度</label>
            <select value={keyItem.thinkingDepthOverride || ''} disabled={saving} onChange={(event) => void onUpdateField(keyItem, 'thinkingDepthOverride', event.target.value)}>
              {thinkingDepthSelectOptions({ value: '', label: '（不覆盖）' })}
            </select>
          </div>
          <ApiKeyMaxOutputTokensField
            value={keyItem.maxOutputTokens && keyItem.maxOutputTokens > 0 ? keyItem.maxOutputTokens : 0}
            disabled={saving}
            onSave={(n) => onUpdateField(keyItem, 'maxOutputTokens', n)}
          />
          <div className="field">
            <label>流式响应（SSE）</label>
            <label className="hint-line" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <input
                type="checkbox"
                checked={keyItem.streamEnabled !== false}
                disabled={saving}
                onChange={(event) => void onUpdateField(keyItem, 'streamEnabled', event.target.checked)}
              />
              允许流式响应（关闭后该 Key 的 stream:true 请求将被拒绝）
            </label>
          </div>
          <ApiKeyModelMappingControl
            aliases={keyItem.modelAliases || {}}
            models={modelOptions}
            providerName={routeProvider?.name || 'Provider'}
            disabled={saving || !routeProvider}
            saving={saving}
            refreshing={routeProvider ? testingProviderID === routeProvider.id : false}
            onRefresh={() => routeProvider ? void onRefreshModels(routeProvider.id, routeProvider.name) : undefined}
            onSave={(modelAliases) => onUpdateModelAliases(keyItem, modelAliases)}
          />
        </div>
      </div>

      <div className="detail-section">
        <div className="detail-section-title">客户端接入</div>
        <div className="api-key-client-configs">
          <div className="field-label-row">
            <label>一键复制客户端配置</label>
          </div>
          <div className="hint-line">
            仅显示与当前输出协议匹配的客户端。点击后复制到剪贴板，并弹窗预览配置路径与内容；可在弹窗内切换内网 / 公网域名。
          </div>
          <div className="api-key-client-config-actions">
            {clientConfigsForProtocol(route?.outputProtocol).map((client) => (
              <button
                key={client}
                className="btn client-config-btn"
                type="button"
                disabled={!route}
                onClick={() => openClientConfigModal(client)}
              >
                {client === 'opencode' ? '复制 OpenCode 配置' : client === 'codex' ? '复制 Codex 配置' : '复制 Claude 配置'}
              </button>
            ))}
          </div>
          {route ? (
            <div className="hint-line">
              当前输出协议：{protocolLabel(route.outputProtocol)}
              {route.outputProtocol === 'openai_responses'
                ? ' · 适配 Codex / OpenCode。'
                : route.outputProtocol === 'claude'
                  ? ' · 适配 Claude Code / OpenCode（Messages）；不走 OpenAI Responses。'
                  : ' · 适配 OpenCode（Chat Completions）。'}
            </div>
          ) : null}
        </div>

        {route ? (
          <div className="api-key-call-example">
            <div className="field-label-row">
              <label>{tunnelRunning ? '公网调用示例' : '局域网调用示例'}</label>
              <CopyButton value={buildApiKeyPublicCurl(keyItem, route, endpoints, livePublicURL, routeProvider)} label="复制 curl" />
            </div>
            <div className="hint-line">
              协议：{protocolLabel(route.outputProtocol)} · 客户端 Base URL：{apiKeyClientURL}
              {route.outputProtocol === 'openai_chat' ? ' · 完整路径：/v1/chat/completions' : route.outputProtocol === 'claude' ? ' · 完整路径：/anthropic/v1/messages（Base URL 不要带 /v1）' : ' · 完整路径：/openai/v1/responses'}
              {keyItem.modelOverride ? ` · 固定模型：${keyItem.modelOverride}` : routeProvider?.defaultModel ? ` · 默认模型：${routeProvider.defaultModel}` : ''}
            </div>
            <pre className="curl-preview">{buildApiKeyPublicCurl(keyItem, route, endpoints, livePublicURL, routeProvider)}</pre>
          </div>
        ) : null}
      </div>

      {clientConfigModal && route ? (
        <ApiKeyClientConfigModal
          client={clientConfigModal}
          keyItem={keyItem}
          route={route}
          provider={routeProvider}
          endpoints={endpoints}
          lanRoot={localGatewayRoot(endpoints)}
          publicBase={livePublicURL}
          publicAvailable={publicAvailable}
          onClose={() => setClientConfigModal(null)}
          onToast={onToast}
          onUpdateField={onUpdateField}
        />
      ) : null}

      {fallbackModalOpen ? (
        <ApiKeyFallbackProvidersModal
          preferredProviderId={binding.providerId}
          providers={providers}
          models={models}
          selectedIds={fallbackIds}
          modelOverrides={fallbackModelOverrides}
          saving={saving}
          testingProviderID={testingProviderID}
          onRefreshModels={onRefreshModels}
          onClose={() => setFallbackModalOpen(false)}
          onSave={async (ids, overrides) => {
            await onUpdateFallbacks(keyItem, ids, overrides);
            setFallbackModalOpen(false);
          }}
        />
      ) : null}
    </div>
  );
}

export function ApiKeyFallbackProvidersModal({
  preferredProviderId,
  providers,
  models,
  selectedIds,
  modelOverrides,
  saving,
  testingProviderID,
  onRefreshModels,
  onClose,
  onSave,
}: {
  preferredProviderId: string;
  providers: Provider[];
  models: Model[];
  selectedIds: string[];
  modelOverrides: Record<string, string>;
  saving: boolean;
  testingProviderID: string;
  onRefreshModels: (providerId: string, providerName: string) => Promise<void>;
  onClose: () => void;
  onSave: (ids: string[], overrides: Record<string, string>) => Promise<void>;
}) {
  const candidates = providers.filter((item) => item.id !== preferredProviderId);
  const [orderedIds, setOrderedIds] = React.useState<string[]>(() => (
    selectedIds.filter((id) => id !== preferredProviderId && providers.some((item) => item.id === id))
  ));
  const [overrides, setOverrides] = React.useState<Record<string, string>>(() => {
    const next: Record<string, string> = {};
    for (const id of selectedIds) {
      const model = (modelOverrides[id] || '').trim();
      if (model) next[id] = model;
    }
    return next;
  });
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState('');
  const selectedProviders = orderedIds
    .map((id) => candidates.find((item) => item.id === id))
    .filter((item): item is Provider => Boolean(item));
  const unselectedProviders = candidates.filter((item) => !orderedIds.includes(item.id));
  const missingModelIds = orderedIds.filter((id) => !(overrides[id] || '').trim());

  function toggleProvider(id: string) {
    setError('');
    setOrderedIds((current) => {
      if (current.includes(id)) {
        setOverrides((prev) => {
          const next = { ...prev };
          delete next[id];
          return next;
        });
        return current.filter((item) => item !== id);
      }
      return [...current, id];
    });
  }

  function moveProvider(id: string, delta: number) {
    setOrderedIds((current) => {
      const index = current.indexOf(id);
      if (index < 0) return current;
      const next = index + delta;
      if (next < 0 || next >= current.length) return current;
      const copy = [...current];
      const [item] = copy.splice(index, 1);
      copy.splice(next, 0, item);
      return copy;
    });
  }

  function setProviderModel(id: string, model: string) {
    setError('');
    setOverrides((current) => ({ ...current, [id]: model }));
  }

  return (
    <Modal
      title="配置备选 Provider"
      description="按优先级排序（#1 在最上）。每个备选必须选择该 Provider 的固定模型替换。"
      onClose={onClose}
      size="wide"
    >
      <div className="api-key-fallback-modal">
        <div className="hint-line">首选 Provider 不在此列表中。已选按优先级从上到下排列；保存前每个备选都要选好固定模型。</div>
        {candidates.length === 0 ? (
          <div className="empty-state compact">没有可配置的备选 Provider，请先添加更多输入 Provider。</div>
        ) : (
          <div className="api-key-fallback-list">
            {selectedProviders.map((provider, orderIndex) => {
              const providerModels = models.filter((model) => model.providerId === provider.id);
              return (
                <div className="api-key-fallback-item selected" key={provider.id}>
                  <div className="api-key-fallback-item-main">
                    <label className="checkbox-field">
                      <input
                        type="checkbox"
                        checked
                        disabled={busy || saving}
                        onChange={() => toggleProvider(provider.id)}
                      />
                      <span>{providerOptionLabel(provider)}</span>
                    </label>
                    <div className="api-key-fallback-order">
                      <span className="api-key-fallback-rank">#{orderIndex + 1}</span>
                      <button className="mini-btn" type="button" disabled={busy || saving || orderIndex <= 0} onClick={() => moveProvider(provider.id, -1)}>上移</button>
                      <button className="mini-btn" type="button" disabled={busy || saving || orderIndex >= selectedProviders.length - 1} onClick={() => moveProvider(provider.id, 1)}>下移</button>
                    </div>
                  </div>
                  <div className="api-key-fallback-model">
                    <label>固定模型替换（必选）</label>
                    <div className="field-inline">
                      <SearchableModelSelect
                        value={overrides[provider.id] || ''}
                        models={providerModels}
                        disabled={busy || saving}
                        emptyLabel="请选择该 Provider 的固定模型"
                        onChange={(value) => setProviderModel(provider.id, value)}
                      />
                      <button
                        className="mini-btn"
                        type="button"
                        disabled={busy || saving || testingProviderID === provider.id}
                        onClick={() => void onRefreshModels(provider.id, provider.name)}
                        title="刷新该 Provider 模型列表"
                      >
                        {testingProviderID === provider.id ? '刷新中…' : '刷新模型'}
                      </button>
                    </div>
                    {!(overrides[provider.id] || '').trim() ? (
                      <div className="hint-line error">必须为该备选选择固定模型</div>
                    ) : (
                      <div className="hint-line">切换到此备选时，将强制使用该模型</div>
                    )}
                  </div>
                </div>
              );
            })}
            {unselectedProviders.map((provider) => (
              <div className="api-key-fallback-item" key={provider.id}>
                <label className="checkbox-field">
                  <input
                    type="checkbox"
                    checked={false}
                    disabled={busy || saving}
                    onChange={() => toggleProvider(provider.id)}
                  />
                  <span>{providerOptionLabel(provider)}</span>
                </label>
              </div>
            ))}
          </div>
        )}
        <div className="hint-line">
          当前顺序：{orderedIds.length === 0 ? '（无）' : orderedIds.map((id, index) => {
            const provider = providers.find((item) => item.id === id);
            const model = (overrides[id] || '').trim() || '未选模型';
            return `${index + 1}. ${provider ? providerOptionLabel(provider) : id} → ${model}`;
          }).join(' ； ')}
        </div>
        {error ? <div className="hint-line error">{error}</div> : null}
      </div>
      <div className="actions modal-actions">
        <button className="btn" type="button" disabled={busy || saving} onClick={onClose}>取消</button>
        <button
          className="btn primary"
          type="button"
          disabled={busy || saving || missingModelIds.length > 0}
          onClick={() => {
            if (missingModelIds.length > 0) {
              setError('每个已选备选 Provider 都必须选择固定模型替换');
              return;
            }
            const cleaned: Record<string, string> = {};
            for (const id of orderedIds) {
              cleaned[id] = (overrides[id] || '').trim();
            }
            setBusy(true);
            void onSave(orderedIds, cleaned).finally(() => setBusy(false));
          }}
        >
          {busy || saving ? '保存中…' : '保存备选'}
        </button>
      </div>
    </Modal>
  );
}

export function ApiKeyClientConfigModal({
  client,
  keyItem,
  route,
  provider,
  endpoints,
  lanRoot,
  publicBase,
  publicAvailable,
  onClose,
  onToast,
  onUpdateField,
}: {
  client: 'opencode' | 'codex' | 'claude';
  keyItem: APIKey;
  route: Route;
  provider?: Provider;
  endpoints: OutputEndpoint[];
  lanRoot: string;
  publicBase: string;
  publicAvailable: boolean;
  onClose: () => void;
  onToast?: (message: string) => void;
  onUpdateField?: (key: APIKey, field: 'name' | 'routeId' | 'modelOverride' | 'thinkingDepthOverride' | 'maxOutputTokens' | 'streamEnabled' | 'codexKeepOfficialLogin' | 'enabled', value: string | boolean | number) => Promise<void>;
}) {
  const [networkMode, setNetworkMode] = React.useState<'lan' | 'public'>(publicAvailable ? 'public' : 'lan');
  // 绑定到具体 key（而非只在本次弹窗会话内），跨次打开保留上次选择。
  const [keepOfficialLogin, setKeepOfficialLogin] = React.useState(() => keyItem.codexKeepOfficialLogin ?? false);
  const effectivePublicBase = networkMode === 'public' && publicAvailable ? publicBase : '';
  const configText = React.useMemo(
    () => buildApiKeyClientConfig(client, keyItem, route, endpoints, effectivePublicBase, provider, keepOfficialLogin),
    [client, keyItem, route, endpoints, effectivePublicBase, provider, keepOfficialLogin],
  );
  const configExtras = React.useMemo(
    () => buildApiKeyClientConfigExtras(client, keyItem, provider),
    [client, keyItem, provider],
  );
  const installScript = React.useMemo(
    () => buildApiKeyClientConfigInstallScript(client, configText, configExtras),
    [client, configText, configExtras],
  );
  // 静态脚本（不依赖当前 key/provider），只用来把本工具此前写入的那一段摘掉。
  const codexRestoreScript = React.useMemo(() => buildApiKeyCodexRestoreOfficialScript(), []);
  const filePath = clientConfigFilePath(client);
  const gatewayRoot = apiKeyGatewayRoot(endpoints, effectivePublicBase);
  const protocolHint = clientConfigProtocolHint(client, route);

  const copyConfig = React.useCallback((text: string, message: string) => {
    void navigator.clipboard.writeText(text).then(() => {
      onToast?.(message);
    });
  }, [onToast]);

  React.useEffect(() => {
    const modeLabel = networkMode === 'public' ? '公网域名' : '内网';
    copyConfig(installScript, `已复制${clientConfigScriptNoun(client)}（${clientConfigTitle(client)} · ${modeLabel}），粘贴到终端执行即可`);
    // 仅打开弹窗时自动复制一次；切换网络时由按钮自行复制
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  React.useEffect(() => {
    if (!publicAvailable && networkMode === 'public') {
      setNetworkMode('lan');
    }
  }, [publicAvailable, networkMode]);

  return (
    <Modal
      title={clientConfigTitle(client)}
      description={
        client === 'codex'
          ? '已复制 Python 修改脚本到剪贴板。粘贴到终端执行即可增量合并进 config.toml（只替换本工具管理的一段，不动你其他配置；会先备份；依赖 macOS 自带 python3）。'
          : '已复制 Python 修改脚本到剪贴板。粘贴到终端执行即可增量合并进 JSON 配置（只更新本工具管理的键，保留你其它配置；会先备份；依赖 macOS 自带 python3）。'
      }
      onClose={onClose}
      size="wide"
    >
      <div className="api-key-client-config-modal">
        <div className="field">
          <label>目标配置文件</label>
          <div className="field-inline">
            <div className="field-readonly code">{filePath}</div>
            <CopyButton value={filePath} label="复制路径" />
          </div>
          {configExtras.length > 0 ? (
            <div className="hint-line">
              同时写入：{configExtras.map((item) => item.display).join('、')}（Codex 模型元数据，消除 Model metadata not found）
            </div>
          ) : null}
        </div>

        <div className="field">
          <label>网关地址</label>
          <div className="api-key-network-toggle" role="group" aria-label="网关地址类型">
            <button
              className={`mini-btn${networkMode === 'lan' ? ' active' : ''}`}
              type="button"
              onClick={() => {
                setNetworkMode('lan');
                const nextConfig = buildApiKeyClientConfig(client, keyItem, route, endpoints, '', provider, keepOfficialLogin);
                const nextScript = buildApiKeyClientConfigInstallScript(client, nextConfig, buildApiKeyClientConfigExtras(client, keyItem, provider));
                copyConfig(nextScript, `已复制${clientConfigScriptNoun(client)}（${clientConfigTitle(client)} · 内网）`);
              }}
            >
              内网
            </button>
            <button
              className={`mini-btn${networkMode === 'public' ? ' active' : ''}`}
              type="button"
              disabled={!publicAvailable}
              title={publicAvailable ? publicBase : '未开启公网域名 / 隧道'}
              onClick={() => {
                if (!publicAvailable) return;
                setNetworkMode('public');
                const nextConfig = buildApiKeyClientConfig(client, keyItem, route, endpoints, publicBase, provider, keepOfficialLogin);
                const nextScript = buildApiKeyClientConfigInstallScript(client, nextConfig, buildApiKeyClientConfigExtras(client, keyItem, provider));
                copyConfig(nextScript, `已复制${clientConfigScriptNoun(client)}（${clientConfigTitle(client)} · 公网域名）`);
              }}
            >
              公网域名
            </button>
          </div>
          <div className="hint-line">
            当前根地址：{gatewayRoot}
            {!publicAvailable ? ' · 公网域名不可用（请先在「公网访问」开启隧道/域名）' : ''}
          </div>
        </div>

        {client === 'codex' ? (
          <div className="field">
            <label>保持账号登录</label>
            <label className="hint-line" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <input
                type="checkbox"
                checked={keepOfficialLogin}
                onChange={(event) => {
                  const next = event.target.checked;
                  setKeepOfficialLogin(next);
                  const nextConfig = buildApiKeyClientConfig(client, keyItem, route, endpoints, effectivePublicBase, provider, next);
                  const nextScript = buildApiKeyClientConfigInstallScript(client, nextConfig, buildApiKeyClientConfigExtras(client, keyItem, provider));
                  copyConfig(nextScript, `已复制${clientConfigScriptNoun(client)}（${clientConfigTitle(client)} · ${next ? '保持账号登录' : '不保留'}）`);
                  // 持久化到该 key，下次打开弹窗（甚至换设备/刷新页面）自动恢复这次的选择。
                  void onUpdateField?.(keyItem, 'codexKeepOfficialLogin', next);
                }}
              />
              开启后 provider 表会对齐 Codex 官方 provider 形状（name = "OpenAI"），
              尽量保留 Codex 官方插件市场 / 移动端远程控制；不写入也不影响 ~/.codex/auth.json，实际模型流量仍走本网关。
              默认关闭；若不需要这些官方能力可保持关闭。（`supports_websockets` 固定为 false，不受此开关影响——见下方生成脚本注释。）
            </label>
          </div>
        ) : null}

        {protocolHint ? <div className="hint-line error">{protocolHint}</div> : null}

        <div className="field">
          <label>配置修改脚本（Python）</label>
          <div className="hint-line">
            {client === 'codex' ? (
              <>
                终端粘贴执行后会增量合并进 {filePath}：只替换本工具用一对多个 # 号分界线包起来的那一段
                （provider 相关配置，其中包含 approval_policy = "never" / sandbox_mode =
                "danger-full-access"，即关闭沙箱、跳过审批确认）；文件里其他任何区块（比如
                [features]、[memories]、personality 等你自己的配置）原样保留、不会被改动或
                挪动位置——对 Codex App 做最小改动。
                {configExtras.length > 0 ? ` 同时整份覆盖 ${configExtras.map((item) => item.display).join('、')}（本工具独占的模型元数据文件，不影响其他配置）。` : ''}
                {' '}执行前会先备份为同目录 <code>.bak.时间戳</code>。脚本为 Python（macOS 自带 python3），不再依赖 bash/awk。
              </>
            ) : (
              <>
                终端粘贴执行后会增量合并进 {filePath}
                {configExtras.length > 0 ? `，并写入 ${configExtras.map((item) => item.display).join('、')}` : ''}
                ：只更新本工具管理的键，保留你其它配置；若文件已存在，会先备份为同目录 <code>.bak.时间戳</code>。
                脚本为 Python（macOS 自带 python3）。
              </>
            )}
          </div>
          <pre className="curl-preview api-key-client-config-preview">{installScript}</pre>
        </div>
      </div>
      <div className="actions modal-actions">
        <button className="btn" type="button" onClick={onClose}>关闭</button>
        <button
          className="btn"
          type="button"
          onClick={() => copyConfig(configText, `已复制纯配置内容（${clientConfigTitle(client)}）`)}
        >
          仅复制配置内容
        </button>
        {client === 'codex' ? (
          <button
            className="btn"
            type="button"
            title="只移除本工具此前写入的那一段，其余配置不受影响；没有该区块时是无害的空操作"
            onClick={() => copyConfig(codexRestoreScript, '已复制"还原为官方 provider"脚本，粘贴到终端执行即可')}
          >
            还原为官方 provider
          </button>
        ) : null}
        <button
          className="btn primary"
          type="button"
          onClick={() => copyConfig(installScript, `已复制${clientConfigScriptNoun(client)}（${clientConfigTitle(client)}）`)}
        >
          复制{clientConfigScriptNoun(client)}
        </button>
      </div>
    </Modal>
  );
}

export function countModelAliases(aliases: Record<string, string>) {
  return Object.keys(aliases || {}).length;
}

export function aliasForModel(aliases: Record<string, string>, modelId: string) {
  for (const [alias, target] of Object.entries(aliases || {})) {
    if (target === modelId) {
      return alias;
    }
  }
  return '';
}

export function buildModelAliases(models: Model[], aliasByModelId: Record<string, string>) {
  const aliases: Record<string, string> = {};
  const seen = new Set<string>();
  for (const model of models) {
    const alias = (aliasByModelId[model.id] || '').trim();
    if (!alias || alias === model.id) {
      continue;
    }
    if (seen.has(alias)) {
      continue;
    }
    aliases[alias] = model.id;
    seen.add(alias);
  }
  return aliases;
}

export function applyModelAliasPrefix(modelId: string, prefix: string) {
  const trimmed = prefix.trim();
  if (!trimmed) {
    return '';
  }
  const alias = `${trimmed}${modelId}`;
  return alias === modelId ? '' : alias;
}

export function inferCommonModelAliasPrefix(models: Model[], aliases: Record<string, string>) {
  let prefix: string | null = null;
  let matched = 0;
  for (const model of models) {
    const alias = aliasForModel(aliases, model.id);
    if (!alias || alias === model.id || !alias.endsWith(model.id)) {
      continue;
    }
    const candidate = alias.slice(0, alias.length - model.id.length);
    if (prefix === null) {
      prefix = candidate;
    } else if (prefix !== candidate) {
      return '';
    }
    matched += 1;
  }
  return matched > 0 ? (prefix || '') : '';
}

export function ApiKeyModelMappingModal({
  open,
  providerName,
  models,
  aliases,
  saving,
  refreshing,
  onClose,
  onSave,
  onRefresh,
}: {
  open: boolean;
  providerName: string;
  models: Model[];
  aliases: Record<string, string>;
  saving?: boolean;
  refreshing?: boolean;
  onClose: () => void;
  onSave: (aliases: Record<string, string>) => void | Promise<void>;
  onRefresh?: () => void;
}) {
  const [draft, setDraft] = React.useState<Record<string, string>>({});
  const [prefix, setPrefix] = React.useState('');

  React.useEffect(() => {
    if (!open) {
      return;
    }
    const next: Record<string, string> = {};
    for (const model of models) {
      next[model.id] = aliasForModel(aliases, model.id);
    }
    setDraft(next);
    setPrefix(inferCommonModelAliasPrefix(models, aliases));
  }, [open]);

  React.useEffect(() => {
    if (!open) {
      return;
    }
    setDraft((current) => {
      const next = { ...current };
      let changed = false;
      for (const model of models) {
        if (!(model.id in next)) {
          next[model.id] = aliasForModel(aliases, model.id);
          changed = true;
        }
      }
      return changed ? next : current;
    });
  }, [open, models]);

  function applyPrefixToAll() {
    const trimmed = prefix.trim();
    if (!trimmed) {
      return;
    }
    const next: Record<string, string> = {};
    for (const model of models) {
      next[model.id] = applyModelAliasPrefix(model.id, trimmed);
    }
    setDraft(next);
  }

  function clearAllAliases() {
    const next: Record<string, string> = {};
    for (const model of models) {
      next[model.id] = '';
    }
    setDraft(next);
  }

  const prefixPreview = models[0] ? applyModelAliasPrefix(models[0].id, prefix) : '';

  if (!open) {
    return null;
  }

  return (
    <Modal
      size="wide"
      title="模型映射"
      description={`为 ${providerName} 的模型配置客户端别名，避免 Cursor 等客户端的自定义模型名与内置名称冲突。可统一加前缀批量生成，也可单独调整某个模型。`}
      onClose={onClose}
    >
      <div className="modal-toolbar">
        <div className="model-mapping-toolbar-meta">共 {models.length} 个模型 · 已配置 {countModelAliases(aliases)} 个别名</div>
        {onRefresh ? (
          <button className="mini-btn" type="button" disabled={refreshing} onClick={onRefresh}>
            {refreshing ? '刷新中…' : '刷新模型列表'}
          </button>
        ) : null}
      </div>
      {models.length > 0 ? (
        <div className="model-mapping-prefix-bar">
          <div className="field model-mapping-prefix-field">
            <label>统一前缀</label>
            <input
              value={prefix}
              placeholder="例如 gw- 或 custom-"
              onChange={(event) => setPrefix(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter') {
                  event.preventDefault();
                  applyPrefixToAll();
                }
              }}
            />
          </div>
          <button className="btn model-mapping-btn" type="button" disabled={!prefix.trim()} onClick={applyPrefixToAll}>应用到全部</button>
          <button className="btn" type="button" onClick={clearAllAliases}>清空全部</button>
          {prefixPreview ? (
            <div className="model-mapping-prefix-preview">示例：{models[0].id} → {prefixPreview}</div>
          ) : null}
        </div>
      ) : null}
      {models.length === 0 ? (
        <div className="empty-state">暂无模型。请先刷新 Provider 模型列表，再配置别名。</div>
      ) : (
        <div className="model-mapping-list">
          <div className="model-mapping-head">
            <span>真实模型</span>
            <span>客户端别名</span>
          </div>
          {models.map((model) => (
            <div className="model-mapping-row" key={model.id}>
              <div className="model-mapping-model">
                <div className="model-mapping-model-id">{model.id}</div>
                {model.contextLength ? <div className="model-mapping-model-meta">{model.contextLength.toLocaleString()} tokens</div> : null}
              </div>
              <input
                value={draft[model.id] || ''}
                placeholder="留空表示不映射"
                onChange={(event) => setDraft((current) => ({ ...current, [model.id]: event.target.value }))}
              />
            </div>
          ))}
        </div>
      )}
      <div className="actions modal-actions">
        <button className="btn" type="button" onClick={onClose}>取消</button>
        <button
          className="btn primary"
          type="button"
          disabled={saving || models.length === 0}
          onClick={() => void Promise.resolve(onSave(buildModelAliases(models, draft))).then(() => onClose())}
        >
          {saving ? '保存中…' : '保存映射'}
        </button>
      </div>
    </Modal>
  );
}

export function ApiKeyModelMappingControl({
  aliases,
  models,
  providerName,
  disabled,
  saving,
  refreshing,
  onSave,
  onRefresh,
}: {
  aliases: Record<string, string>;
  models: Model[];
  providerName: string;
  disabled?: boolean;
  saving?: boolean;
  refreshing?: boolean;
  onSave: (aliases: Record<string, string>) => void | Promise<void>;
  onRefresh?: () => void;
}) {
  const [open, setOpen] = React.useState(false);
  const count = countModelAliases(aliases);

  return (
    <>
      <div className="field">
        <label>模型映射</label>
        <div className="model-mapping-trigger">
          <button className="btn model-mapping-btn" type="button" disabled={disabled} onClick={() => setOpen(true)}>模型映射</button>
          <span className="model-mapping-summary">{count > 0 ? `已配置 ${count} 个别名` : '未配置别名'}</span>
        </div>
      </div>
      <ApiKeyModelMappingModal
        open={open}
        providerName={providerName}
        models={models}
        aliases={aliases}
        saving={saving}
        refreshing={refreshing}
        onClose={() => setOpen(false)}
        onRefresh={onRefresh}
        onSave={onSave}
      />
    </>
  );
}

export function ApiKeyFixedModelField({
  value,
  models,
  disabled,
  refreshing,
  onChange,
  onRefresh,
}: {
  value: string;
  models: Model[];
  disabled?: boolean;
  refreshing?: boolean;
  onChange: (value: string) => void;
  onRefresh: () => void;
}) {
  return (
    <div className="field">
      <label>固定模型替换</label>
      <div className="field-inline">
        <SearchableModelSelect
          value={value}
          models={models}
          disabled={disabled}
          emptyLabel="（不替换，使用请求体 model）"
          onChange={onChange}
        />
        <button className="mini-btn" type="button" disabled={disabled || refreshing} onClick={onRefresh} title="从绑定路由的 Provider 重新获取模型列表">
          {refreshing ? '刷新中…' : '刷新模型'}
        </button>
      </div>
      <div className="hint-line">
        设置后将忽略请求体中的 model，统一替换为所选模型。
        {models.length > 0 ? ` · 共 ${models.length} 个，点开后输入关键字筛选；选项含网关解析的 max output` : ''}
      </div>
    </div>
  );
}

export function ApiKeyMaxOutputTokensField({
  value,
  disabled,
  onSave,
}: {
  value: number;
  disabled?: boolean;
  onSave: (value: number) => Promise<void> | void;
}) {
  const [draft, setDraft] = React.useState(value > 0 ? String(value) : '');
  React.useEffect(() => {
    setDraft(value > 0 ? String(value) : '');
  }, [value]);

  function commit() {
    const raw = draft.trim();
    const n = raw === '' ? 0 : Number.parseInt(raw, 10);
    const next = Number.isFinite(n) && n > 0 ? Math.min(n, 200000) : 0;
    setDraft(next > 0 ? String(next) : '');
    if (next === (value > 0 ? value : 0)) return;
    void onSave(next);
  }

  return (
    <div className="field">
      <label>最大输出 Token</label>
      <input
        type="number"
        min={0}
        max={200000}
        placeholder="0 = 自动（按模型）"
        disabled={disabled}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={() => commit()}
        onKeyDown={(event) => {
          if (event.key === 'Enter') {
            event.preventDefault();
            (event.target as HTMLInputElement).blur();
          }
        }}
      />
      <div className="hint-line">留空或 0 按模型自动解析；填写后覆盖上游 max_tokens（上限 200000）。</div>
    </div>
  );
}

export function ApiKeyNameField({ name, disabled, onSave }: { name: string; disabled?: boolean; onSave: (name: string) => Promise<void> | void }) {
  const [draft, setDraft] = React.useState(name);
  React.useEffect(() => {
    setDraft(name);
  }, [name]);
  return (
    <div className="field">
      <label>名称</label>
      <input
        value={draft}
        disabled={disabled}
        placeholder="例如 key1"
        onChange={(event) => setDraft(event.target.value)}
        onBlur={() => {
          const trimmed = draft.trim();
          if (!trimmed || trimmed === name) {
            setDraft(name);
            return;
          }
          void onSave(trimmed);
        }}
        onKeyDown={(event) => {
          if (event.key === 'Enter') {
            event.currentTarget.blur();
          }
        }}
      />
    </div>
  );
}
