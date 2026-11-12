// @generated-from main.tsx — 由重构脚本拆分生成，请直接维护本文件。
import React, {useEffect, useState} from 'react';
import {
    API_BASE,
    apiKeyClientBaseURL,
    apiKeyGatewayRoot,
    buildApiKeyClientConfig,
    buildApiKeyClientConfigExtras,
    buildApiKeyClientConfigInstallScript,
    buildApiKeyCodexRestoreOfficialScript,
    buildApiKeyPublicCurl,
    clientConfigFilePath,
    clientConfigProtocolHint,
    clientConfigScriptNoun,
    clientConfigsForProtocol,
    clientConfigTitle,
    copyToClipboard,
    formatCostMicros,
    formatTokenCount,
    getApiKeyBinding,
    localGatewayRoot,
    modelAlias,
    protocolFromLabel,
    protocolLabel,
    QuotaWindowOptions,
    scheduleModeLabel,
    ScheduleModeOptions,
    thinkingDepthSelectOptions,
    usageStatsForKey
} from '../lib';
import {IconSelect, SearchableModelSelect} from './selects';
import {
    APIKey,
    ConsoleUser,
    KeyProfile,
    Model,
    OutputEndpoint,
    Protocol,
    Provider,
    RequestStatsSnapshot,
    Route
} from '../types';
import {Badge, CopyButton, InfoTip, Modal, Switch} from './ui';
import {ProviderLabel} from './provider_label';
import {confirmDialog} from '../dialogs';
import {providerSelectOptions} from './provider_select';
import {FallbackProviderPicker} from './fallback_picker';

/**
 * 密钥面板可以单独 PATCH 的字段。这里只有一处定义：以前三个地方各抄一遍，
 * 加一个字段就得同步改三处，漏一处表现为「下拉框选了就报类型错」这种难查的问题。
 */
export type ApiKeyEditableField =
  | 'name' | 'routeId' | 'modelOverride' | 'thinkingDepthOverride' | 'maxOutputTokens'
  | 'firstPacketTimeoutSec' | 'sameProviderRetries'
  | 'streamEnabled' | 'codexKeepOfficialLogin' | 'codexForceFast' | 'scheduleMode' | 'enabled'
  | 'quotaWindow' | 'quotaTokens' | 'quotaCostMicros';

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
  canWriteQuota,
  testingProviderID,
  tunnelRunning,
  livePublicURL,
  fixedOutputLabels,
  onUpdateField,
  onUpdateBinding,
  onPromoteFallback,
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
  // 额度只能管理员写（后端也会拒）；非管理员传 false，只读展示已用量。
  canWriteQuota: boolean;
  testingProviderID: string;
  tunnelRunning: boolean;
  livePublicURL: string;
  fixedOutputLabels: string[];
  onUpdateField: (key: APIKey, field: ApiKeyEditableField, value: string | boolean | number) => Promise<void>;
  onUpdateBinding: (key: APIKey, providerId: string, outputProtocol: Protocol) => Promise<void>;
  // 备选「设为首选」：绑定与备选列表一次提交（交换，不删节点）。
  onPromoteFallback: (key: APIKey, providerId: string) => Promise<void>;
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
  // 隧道域名或控制台自身的域名都算「公网可用」：反代部署时网关自己不知道外面叫什么。
  const publicAvailable = Boolean(livePublicURL);
  const defaultPublicBase = publicAvailable ? livePublicURL : '';
  const apiKeyClientURL = route ? apiKeyClientBaseURL(route, endpoints, defaultPublicBase) : '';
  const [clientConfigModal, setClientConfigModal] = React.useState<'opencode' | 'codex' | 'claude' | null>(null);
  const [fallbackModalOpen, setFallbackModalOpen] = React.useState(false);
  const profiles = keyItem.profiles || [];
  const activeProfileId = keyItem.activeProfileId || '';
  const activeProfile = profiles.find((item) => item.id === activeProfileId);
  const primaryProvider = providers.find((item) => item.id === binding.providerId);
  const fallbackIds = keyItem.fallbackProviderIds || [];
  const fallbackModelOverrides = keyItem.fallbackModelOverrides || {};
  const activeProviderId = keyItem.activeProviderId || binding.providerId;
  const activeProvider = providers.find((item) => item.id === activeProviderId);
  const usingFallback = Boolean(keyItem.activeProviderId && keyItem.activeProviderId !== binding.providerId);
  // 「在服务」的多节点口径（见后端 ServingProviderIDs）：最近 10 分钟答过请求的
  // 每一台都在服务。老后端只给单个 id 时退回那一个，再退回粘滞位。
  const servingIds = keyItem.servingProviderIds?.length
    ? keyItem.servingProviderIds
    : [keyItem.servingProviderId || activeProviderId].filter(Boolean);
  const activeFallbackModel = usingFallback ? (fallbackModelOverrides[keyItem.activeProviderId || ''] || '') : '';
  // 备选没有固定模型时由网关自动解析：优先沿用请求模型，模型不在备选目录时用
  // 备选自己的默认模型；协议不同则由网关负责转换，不需要用户额外锁定模型。
  const activeFallbackUsesAutoModel = usingFallback && !activeFallbackModel;
  const sameProtocolAsPrimary = (provider?: Provider) => Boolean(primaryProvider && provider && provider.protocol === primaryProvider.protocol);

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
      scheduleMode: keyItem.scheduleMode || '',
    };
  }

  // 方案创建/重命名走应用内弹窗：window.prompt 在部分嵌入式浏览器会被静默拦截，
  // 表现为按钮点击毫无反应（“假按钮”）。
  const [profileDialog, setProfileDialog] = React.useState<'create' | 'rename' | null>(null);
  const [detailTab, setDetailTab] = React.useState<'connect' | 'routing' | 'params' | 'all'>('connect');

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
    if (!(await confirmDialog(`删除方案「${activeProfile.name}」？删除后将切换到其余方案之一。`, {danger: true, confirmLabel: '删除'}))) return;
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
          {(keyItem.scheduleMode || '') !== '' ? <Badge tone="cyan">{scheduleModeLabel(keyItem.scheduleMode)}</Badge> : null}
          {keyItem.quotaWindow === 'day' ? <Badge tone="amber">日额度</Badge> : null}
          {keyItem.quotaWindow === 'month' ? <Badge tone="amber">月额度</Badge> : null}
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

      <div className="api-key-detail-tabs" role="tablist">
        <button
          type="button"
          className={`api-key-detail-tab ${detailTab === 'connect' ? 'active' : ''}`}
          onClick={() => setDetailTab('connect')}
        >
          接入指南
        </button>
        <button
          type="button"
          className={`api-key-detail-tab ${detailTab === 'routing' ? 'active' : ''}`}
          onClick={() => setDetailTab('routing')}
        >
          路由与方案
        </button>
        <button
          type="button"
          className={`api-key-detail-tab ${detailTab === 'params' ? 'active' : ''}`}
          onClick={() => setDetailTab('params')}
        >
          模型与参数
        </button>
        <button
          type="button"
          className={`api-key-detail-tab ${detailTab === 'all' ? 'active' : ''}`}
          onClick={() => setDetailTab('all')}
        >
          全部
        </button>
      </div>

      {(detailTab === 'connect' || detailTab === 'all') && (
        <div className="detail-section">
          <div className="detail-section-title">客户端快速接入指南</div>
          <div className="api-key-client-configs">
            <div className="field-label-row">
              <label>一键复制客户端配置</label>
              <InfoTip text="仅显示与当前输出协议匹配的客户端；点击复制并预览配置，弹窗内可切换内网 / 公网域名。" />
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
                  {client === 'opencode' ? '复制 OpenCode' : client === 'codex' ? '复制 Codex' : '复制 Claude'}配置
                </button>
              ))}
            </div>
            {route ? (
              <div className="hint-text">{`当前输出协议：${protocolLabel(route.outputProtocol)}${route.outputProtocol === 'openai_responses' ? ' · 适配 Codex / OpenCode。' : route.outputProtocol === 'claude' ? ' · 适配 Claude Code / OpenCode（Messages）；不走 OpenAI Responses。' : ' · 适配 OpenCode（Chat Completions）。'}`}</div>
            ) : null}
          </div>

          {route ? (
            <div className="api-key-call-example">
              <div className="field-label-row">
                <label>{publicAvailable ? '公网调用示例' : '内网调用示例'}</label>
                <CopyButton value={buildApiKeyPublicCurl(keyItem, route, endpoints, livePublicURL, routeProvider)} label="复制 curl" />
              </div>
              <div className="hint-text">{`协议：${protocolLabel(route.outputProtocol)} · 客户端 Base URL：${apiKeyClientURL}${route.outputProtocol === 'openai_chat' ? ' · 完整路径：/v1/chat/completions' : route.outputProtocol === 'claude' ? ' · 完整路径：/anthropic/v1/messages（Base URL 不要带 /v1）' : ' · 完整路径：/openai/v1/responses'}${keyItem.modelOverride ? ` · 固定模型：${keyItem.modelOverride}` : routeProvider?.defaultModel ? ` · 默认模型：${routeProvider.defaultModel}` : ''}`}</div>
              <pre className="curl-preview">{buildApiKeyPublicCurl(keyItem, route, endpoints, livePublicURL, routeProvider)}</pre>
            </div>
          ) : null}
        </div>
      )}

      {(detailTab === 'routing' || detailTab === 'all') && (
        <>
          <div className="api-key-profile-selector">
            <div className="field-label-row">
              <label>转发方案</label>
              <InfoTip text="同一 Key（token 不变）可切换多套完整转发配置。下方整页表单即当前生效方案（Provider / 备选 / 模型映射等）；切换方案后新请求立即走新配置。" />
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
              <IconSelect
                label="输入 Provider（首选）"
                value={binding.providerId}
                disabled={saving}
                options={providerSelectOptions(providers)}
                onChange={(providerId) => {
                  void onUpdateBinding(keyItem, providerId, binding.outputProtocol);
                }}
              />
              {owners && onUpdateOwner ? (
                <div className="field">
                  <label>所属用户</label>
                  <select
                    value={keyItem.ownerUserId || ''}
                    disabled={saving}
                    onChange={(event) => void onUpdateOwner(keyItem, event.target.value)}
                  >
                    <option value="">内置 admin（无归属）</option>
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
            <div className="detail-section-head">
              <div className="detail-section-title">备选与故障转移</div>
              <button className="mini-btn" type="button" disabled={saving || !binding.providerId || providers.length < 2} onClick={() => setFallbackModalOpen(true)}>
                配置备选
              </button>
            </div>
            {/* 故障转移是一条链，就按链排：一行一个候选，谁在服务一眼看到底。
                原来把整条链拼成一句「1. A → m ； 2. B → m」的纯文本，三个备选就换行成糊。
                会话分散下多台同时服务是正常的，所以「在服务」按后端给的窗口内节点集合判，
                而不是只看粘滞位。 */}
            <div className="fallback-chain">
              <FallbackChainRow
                rank="首选"
                provider={primaryProvider}
                missingLabel="未绑定"
                modelLabel={keyItem.modelOverride || '按客户端模型'}
                serving={servingIds.includes(binding.providerId)}
                stateLabel={servingIds.includes(binding.providerId) ? '在服务' : (usingFallback ? '已切走' : undefined)}
              />
              {fallbackIds.map((id, index) => {
                const serving = servingIds.includes(id);
                const fallbackProvider = providers.find((item) => item.id === id);
                const modelOverride = fallbackModelOverrides[id];
                const inheritsModel = !modelOverride && sameProtocolAsPrimary(fallbackProvider);
                return (
                  <FallbackChainRow
                    key={id}
                    rank={`备选 ${index + 1}`}
                    provider={fallbackProvider}
                    missingLabel={id}
                    modelLabel={modelOverride || (fallbackProvider ? (inheritsModel ? '按请求模型' : '自动解析模型') : '未锁定模型')}
                    modelUnset={!fallbackProvider}
                    serving={serving}
                    stateLabel={serving ? '在服务' : undefined}
                  />
                );
              })}
            </div>
            {fallbackIds.length === 0 ? (
              <div className="fallback-empty">未配置备选：首选额度耗尽后，这个密钥不会自动换到别的 Provider。</div>
            ) : null}
            {usingFallback && activeFallbackModel ? (
              <div className="fallback-active-note">
                当前由备选承接，模型锁定为 <b>{activeFallbackModel}</b>
              </div>
            ) : null}
            {activeFallbackUsesAutoModel ? (
              <div className="fallback-active-note">当前由备选承接，模型由网关自动解析</div>
            ) : null}
            {/* 调度只在有多个候选时才有意义，所以跟着备选链放在同一节里。 */}
            {fallbackIds.length > 0 ? (
              <div className="field" style={{ marginTop: 10 }}>
                <div className="field-label-row">
                  <label>调度模式</label>
                  <InfoTip text={ScheduleModeOptions.find((option) => option.value === (keyItem.scheduleMode || ''))?.hint ?? ''} />
                </div>
                <select
                  value={keyItem.scheduleMode || ''}
                  disabled={saving}
                  onChange={(event) => void onUpdateField(keyItem, 'scheduleMode', event.target.value)}
                >
                  {ScheduleModeOptions.map((option) => (
                    <option key={option.value} value={option.value}>{option.label}</option>
                  ))}
                </select>
                <div className="hint-text">同一段对话仍然钉在它已建好缓存的账号上（会话粘性优先于调度模式）；被标记「异常」的候选永远排在最后，但不会被跳过。</div>
              </div>
            ) : null}
            {/* 额度也是「这把 Key 能干什么」的治理项，和调度放同一节。 */}
            <ApiKeyQuotaSection
              keyItem={keyItem}
              saving={saving}
              canWrite={canWriteQuota}
              onUpdateField={onUpdateField}
            />
          </div>
        </>
      )}

      {(detailTab === 'params' || detailTab === 'all') && (
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
              <div className="field-label-row">
                <label>流式响应（SSE）</label>
                <InfoTip text="允许流式响应（关闭后该 Key 的 stream:true 请求将被拒绝）" />
              </div>
              <Switch checked={keyItem.streamEnabled !== false} label="允许 stream:true" disabled={saving} onChange={(value) => void onUpdateField(keyItem, 'streamEnabled', value)} />
            </div>
            <div className="field">
              <div className="field-label-row">
                <label>Codex Fast 档（强制）</label>
                <InfoTip text="开启后，该 Key 的请求只要落到 ChatGPT OAuth（Codex）Provider，就强制以 fast 档转发（service_tier=priority，速度约 1.5~2.5x，额度消耗 2~2.5x），与客户端传参无关；其他类型 Provider 不受影响。默认关闭。" />
              </div>
              <Switch checked={keyItem.codexForceFast === true} label="强制 fast 档" disabled={saving} onChange={(value) => void onUpdateField(keyItem, 'codexForceFast', value)} />
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
      )}

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

/**
 * 故障转移链上的一行：排队位次、哪个 Provider、锁什么模型、现在是不是它在服务。
 * 与「配置备选」弹窗里的条目用同一套视觉，两处看起来是同一个东西。
 */
function FallbackChainRow({ rank, provider, missingLabel, modelLabel, modelUnset, serving, stateLabel }: {
  rank: string;
  provider?: Provider;
  missingLabel: string;
  modelLabel: string;
  modelUnset?: boolean;
  serving: boolean;
  stateLabel?: string;
}) {
  return (
    <div className={`fallback-row${serving ? ' serving' : ''}`}>
      <span className="fallback-rank">{rank}</span>
      {provider
        ? <ProviderLabel provider={provider} size={16} className="fallback-identity" />
        : <span className="fallback-name missing" title={missingLabel}>{missingLabel}</span>}
      {provider ? <Badge tone="slate">{protocolLabel(provider.protocol)}</Badge> : null}
      <span className={`fallback-model${modelUnset ? ' unset' : ''}`}>{modelLabel}</span>
      {stateLabel ? <span className={`fallback-state${serving ? ' serving' : ''}`}>{stateLabel}</span> : null}
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
  staleCount = 0,
}: {
  preferredProviderId: string;
  providers: Provider[];
  models: Model[];
  selectedIds: string[];
  modelOverrides: Record<string, string>;
  // 备选里指向已删除 Provider 的条数：它们不在 selectedIds 里，保存时会被一并清掉。
  // 只在提示里说明数量，不必把每个孤儿 id 都列出来。
  staleCount?: number;
  saving: boolean;
  testingProviderID: string;
  onRefreshModels: (providerId: string, providerName: string) => Promise<void>;
  onClose: () => void;
  onSave: (ids: string[], overrides: Record<string, string>) => Promise<void>;
}) {
  const candidates = providers.filter((item) => item.id !== preferredProviderId);
  const primaryProvider = providers.find((item) => item.id === preferredProviderId);
  // 同协议 = 上游说同一种 API 方言；跨协议由网关自动转换。两种备选都不要求
  // 用户锁定模型，固定模型只用于明确希望该节点始终使用某个模型时。
  const sameProtocol = (provider: Provider) => Boolean(primaryProvider && provider.protocol === primaryProvider.protocol);
  // 备选链就是一个有序 id 列表；增删、排序都只改它，保存时再落盘。
  const [orderedIds, setOrderedIds] = useState<string[]>(() => (
    selectedIds.filter((id) => id !== preferredProviderId && providers.some((item) => item.id === id))
  ));
  const [overrides, setOverrides] = useState<Record<string, string>>(() => {
    const next: Record<string, string> = {};
    for (const id of selectedIds) {
      const model = (modelOverrides[id] || '').trim();
      if (model) next[id] = model;
    }
    return next;
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const selectedProviders = orderedIds
    .map((id) => candidates.find((item) => item.id === id))
    .filter((item): item is Provider => Boolean(item));
  const crossProtocolProviders = selectedProviders.filter((item) => !sameProtocol(item));

  // 链变短时顺手清掉被移除 Provider 的模型覆盖，免得它下次被加回来时带着旧值。
  const applyChain = (next: string[]) => {
    setError('');
    setOrderedIds(next);
    setOverrides((current) => {
      const kept: Record<string, string> = {};
      for (const id of next) {
        const model = (current[id] || '').trim();
        if (model) kept[id] = model;
      }
      return kept;
    });
  };

  function setProviderModel(id: string, model: string) {
    setError('');
    setOverrides((current) => ({ ...current, [id]: model }));
  }

  // 批量设置固定模型。
  //
  // 这块以前只能一个个点：备选一多（同一家的多个账号就是 N 行），要设成同一个模型就得
  // 点 N 次下拉、每次还要在同一个清单里翻一遍。下面这条批量栏把「选一次，落一片」做掉：
  //   · 候选模型取所选备选清单的并集（同厂商多账号共用一个清单，这是最常见的情况）；
  //   · 应用时只写给「模型清单里确实有它」的 Provider，跳过的条数如实报出来 ——
  //     写一个该 Provider 不认的模型，等于把那一跳彻底弄坏，比不设更糟。
  const [batchModel, setBatchModel] = useState('');
  const [batchNotice, setBatchNotice] = useState('');
  const batchModels = (() => {
    const seen = new Set<string>();
    const out: Model[] = [];
    for (const provider of selectedProviders) {
      for (const model of models.filter((item) => item.providerId === provider.id)) {
        const id = (model.id || '').trim();
        if (!id || seen.has(id)) continue;
        seen.add(id);
        out.push(model);
      }
    }
    return out.sort((a, b) => (a.id || '').localeCompare(b.id || ''));
  })();

  const applyBatchModel = () => {
    const target = batchModel.trim();
    if (!target) {
      setBatchNotice('先选一个模型，或用「全部留空」清掉所有固定模型。');
      return;
    }
    setError('');
    let applied = 0;
    const skipped: string[] = [];
    setOverrides((current) => {
      const next = { ...current };
      for (const provider of selectedProviders) {
        const has = models.some((model) => model.providerId === provider.id && (model.id || '').trim() === target);
        if (has) {
          next[provider.id] = target;
          applied += 1;
        } else {
          skipped.push(provider.name);
        }
      }
      return next;
    });
    setBatchNotice(
      skipped.length === 0
        ? `已把 ${applied} 个备选设为 ${target}`
        : `已设置 ${applied} 个；${skipped.length} 个没有该模型，已跳过（${skipped.slice(0, 3).join('、')}${skipped.length > 3 ? '…' : ''}）`,
    );
  };

  const clearAllOverrides = () => {
    setError('');
    setOverrides({});
    setBatchNotice('已清空全部固定模型：每个备选都沿用请求模型或各自默认模型。');
  };

  return (
    <Modal
      title="配置备选 Provider"
      description="左边挑 Provider，右边是降级顺序（#1 最先试）。同协议直接转发，跨协议由网关自动转换；固定模型是可选项，留空时自动沿用请求模型或使用该 Provider 的默认模型。"
      onClose={onClose}
      size="wide"
    >
      <div className="api-key-fallback-modal">
        {staleCount > 0 ? (
          <div className="hint-text">
            另有 {staleCount} 个备选指向已删除的 Provider：它们不会出现在列表里，点击保存后会被一并移除。
          </div>
        ) : null}
        {candidates.length === 0 ? (
          <div className="empty-state compact">没有可配置的备选 Provider，请先添加更多输入 Provider。</div>
        ) : (
          <>
            <FallbackProviderPicker
              providers={candidates}
              selectedIds={orderedIds}
              sameProtocol={sameProtocol}
              onChange={applyChain}
              thirdColumn={selectedProviders.length > 0 ? (
                // 固定模型替换作为第三列，和「可选 / 备选链」并排。
                // 它原来是双栏下面另起的一段（还带折叠 + 自己的滚动），于是页面上出现
                // 两个上下堆着的大下拉，再加第二个滚轮。并排之后：同一行读「选谁 →
                // 谁先 → 每台用哪个模型」，是同一件事的三个面，不用来回上下找。
                <section className="fpp-col" aria-label="固定模型替换">
                  <header className="fpp-col-head">
                    <span className="fpp-col-title">固定模型替换</span>
                    <span className="fpp-col-count">{selectedProviders.length}</span>
                    <span className="api-key-fallback-models-note">
                      {crossProtocolProviders.length > 0
                        ? `${crossProtocolProviders.length} 个跨协议会自动转换`
                        : '留空 = 自动'}
                    </span>
                  </header>
                  {selectedProviders.length > 1 ? (
                    <div className="api-key-fallback-models-bulk">
                      <SearchableModelSelect
                        value={batchModel}
                        models={batchModels}
                        disabled={busy || saving}
                        emptyLabel="批量设为…"
                        onChange={(value) => { setBatchModel(value); setBatchNotice(''); }}
                      />
                      <button
                        className="mini-btn"
                        type="button"
                        disabled={busy || saving || !batchModel.trim()}
                        onClick={applyBatchModel}
                        title="把选中的模型写给每一个有此模型的备选"
                      >
                        应用到全部
                      </button>
                      <button
                        className="mini-btn"
                        type="button"
                        disabled={busy || saving}
                        onClick={clearAllOverrides}
                        title="清掉所有固定模型，全部改为自动"
                      >
                        留空
                      </button>
                    </div>
                  ) : null}
                  {batchNotice ? <div className="api-key-fallback-models-notice">{batchNotice}</div> : null}
                  <div className="api-key-fallback-models-list">
                    {selectedProviders.map((provider, index) => {
                      const providerModels = models.filter((model) => model.providerId === provider.id);
                      const crossProtocol = !sameProtocol(provider);
                      const chosen = (overrides[provider.id] || '').trim();
                      const rowHint = crossProtocol
                        ? (chosen ? '跨协议备选：网关会自动转换协议' : '跨协议备选：不固定模型时使用请求模型或该 Provider 默认模型（网关自动转换协议）')
                        : (chosen ? `固定使用 ${chosen}` : '留空时沿用请求模型或该 Provider 默认模型');
                      return (
                        <div className="api-key-fallback-models-row" key={provider.id} title={`#${index + 1} ${provider.name} — ${rowHint}`}>
                          {/* 用编号当这一列的行标识，而不是重复 Provider 名：
                              名字在左边的「备选链」列已经写着，两列靠 #N 对齐即可。
                              267px 的列宽里如果既放名字又放下拉，两者都会被压成省略号
                              （实测：名字只剩「Qoder 账…」、下拉只剩「Qwen」）。 */}
                          <span className="fpp-rank" title={provider.name}>#{index + 1}</span>
                          <SearchableModelSelect
                            value={overrides[provider.id] || ''}
                            models={providerModels}
                            disabled={busy || saving}
                            emptyLabel="自动"
                            onChange={(value) => setProviderModel(provider.id, value)}
                          />
                          <button
                            className="icon-btn"
                            type="button"
                            disabled={busy || saving || testingProviderID === provider.id}
                            onClick={() => void onRefreshModels(provider.id, provider.name)}
                            title="刷新该 Provider 的模型列表"
                            aria-label="刷新该 Provider 的模型列表"
                          >
                            {testingProviderID === provider.id ? '…' : '↻'}
                          </button>
                        </div>
                      );
                    })}
                  </div>
                </section>
              ) : null}
            />
          </>
        )}
        {error ? <div className="hint-line error">{error}</div> : null}
      </div>
      <div className="actions modal-actions">
        <span className="api-key-fallback-summary-text">
          {orderedIds.length === 0
            ? '未配置备选：首选额度耗尽后，这个密钥不会自动换到别的 Provider'
            : `共 ${orderedIds.length} 个备选`}
        </span>
        <button className="btn" type="button" disabled={busy || saving} onClick={onClose}>取消</button>
        <button
          className="btn primary"
          type="button"
          disabled={busy || saving}
          onClick={() => {
            const cleaned: Record<string, string> = {};
            for (const id of orderedIds) {
              const model = (overrides[id] || '').trim();
              if (model) cleaned[id] = model;
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
  onUpdateField?: (key: APIKey, field: ApiKeyEditableField, value: string | boolean | number) => Promise<void>;
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
    void copyToClipboard(text).then((ok) => {
      if (ok) {
        onToast?.(message);
      }
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
            <div className="hint-text">{`同时写入：${configExtras.map((item) => item.display).join('、')}（Codex 模型元数据，消除 Model metadata not found）`}</div>
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
          <div className="hint-text">{`当前根地址：${gatewayRoot}${!publicAvailable ? ' · 公网域名不可用（通过域名打开控制台，或先在「公网访问」开启隧道）' : ''}`}</div>
        </div>

        {client === 'codex' ? (
          <div className="field">
            <div className="field-label-row">
              <label>保持账号登录</label>
              <InfoTip text={'开启后 provider 表会对齐 Codex 官方 provider 形状（name = "OpenAI"），尽量保留 Codex 官方插件市场 / 移动端远程控制；不写入也不影响 ~/.codex/auth.json，实际模型流量仍走本网关。默认关闭；若不需要这些官方能力可保持关闭。（`supports_websockets` 固定为 false，不受此开关影响——见下方生成脚本注释。）'} />
            </div>
            <label style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
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
            </label>
          </div>
        ) : null}

        {protocolHint ? <div className="hint-line error">{protocolHint}</div> : null}

        <div className="field">
          <label>配置修改脚本（Python）</label>
          <div className="hint-text">{client === 'codex'
            ? `终端粘贴执行后会增量合并进 ${filePath}：只替换本工具用一对多个 # 号分界线包起来的那一段（provider 相关配置，其中包含 approval_policy = "never" / sandbox_mode = "danger-full-access"，即关闭沙箱、跳过审批确认）；文件里其他任何区块（比如 [features]、[memories]、personality 等你自己的配置）原样保留、不会被改动或挪动位置——对 Codex App 做最小改动。${configExtras.length > 0 ? ` 同时整份覆盖 ${configExtras.map((item) => item.display).join('、')}（本工具独占的模型元数据文件，不影响其他配置）。` : ''} 执行前会先备份为同目录 .bak.时间戳。脚本为 Python（macOS 自带 python3），不再依赖 bash/awk。`
            : `终端粘贴执行后会增量合并进 ${filePath}${configExtras.length > 0 ? `，并写入 ${configExtras.map((item) => item.display).join('、')}` : ''}：只更新本工具管理的键，保留你其它配置；若文件已存在，会先备份为同目录 .bak.时间戳。脚本为 Python（macOS 自带 python3）。`}</div>
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
                <div
                  className="model-mapping-model-id model-id-clickable"
                  title={`点击复制模型 ID：${model.id}`}
                  onClick={() => {
                    void copyToClipboard(model.id).then((ok) => {
                      if (ok) {
                        const toast = document.getElementById('toast');
                        if (toast) {
                          toast.textContent = `已复制模型 ID：${model.id}`;
                          toast.classList.add('show');
                          window.setTimeout(() => toast.classList.remove('show'), 2000);
                        }
                      }
                    });
                  }}
                >
                  <span>{model.id}</span>
                  <span className="model-id-copy-hint" aria-hidden="true">📋</span>
                </div>
                {modelAlias(model) ? <div className="model-id-alias">{modelAlias(model)}</div> : null}
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
        <div className="field-label-row">
          <label>模型映射</label>
          <InfoTip text="把客户端请求的 model 名改写成上游真实模型名（按 Key 生效）；打开后按上游模型逐个配置。" />
          <span className="model-mapping-summary">{count > 0 ? `已配置 ${count} 个别名` : '未配置别名'}</span>
        </div>
        <div className="model-mapping-trigger">
          <button className="btn model-mapping-btn" type="button" disabled={disabled} onClick={() => setOpen(true)}>配置映射</button>
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
      <div className="field-label-row">
        <label>固定模型替换</label>
        <InfoTip text={`设置后将忽略请求体中的 model，统一替换为所选模型。${models.length > 0 ? ` · 共 ${models.length} 个，点开后输入关键字筛选；选项含网关解析的 max output` : ''}`} />
      </div>
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
      <div className="field-label-row">
        <label>最大输出 Token</label>
        <InfoTip text="留空或 0 按模型自动解析；填写后覆盖上游 max_tokens（上限 200000）。" />
      </div>
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

/**
 * 密钥额度区：重置窗口 + token / 成本上限 + 当前窗口已用量。
 *
 * 已用量直接从 /__request-stats 的按密钥聚合里取（今天 / 本月），与用量页同源同口径，
 * 不在这里另算一份——两处算法不一致是这类面板最难查的 bug。数字只包含**已完成**的请求：
 * 正在飞的请求按 prompt 预扣，界面如实说明，不做假精确。
 */
export function ApiKeyQuotaSection({ keyItem, saving, canWrite, onUpdateField }: {
  keyItem: APIKey;
  saving: boolean;
  canWrite: boolean;
  onUpdateField: (key: APIKey, field: ApiKeyEditableField, value: string | boolean | number) => Promise<void>;
}) {
  const windowValue = keyItem.quotaWindow || '';
  const [snapshot, setSnapshot] = useState<RequestStatsSnapshot | null>(null);
  const [tokensDraft, setTokensDraft] = useState('');
  const [usdDraft, setUsdDraft] = useState('');
  const [error, setError] = useState('');

  useEffect(() => {
    setTokensDraft(keyItem.quotaTokens ? String(keyItem.quotaTokens) : '');
    setUsdDraft(keyItem.quotaCostMicros ? (keyItem.quotaCostMicros / 1_000_000).toFixed(2) : '');
  }, [keyItem.id, keyItem.quotaTokens, keyItem.quotaCostMicros]);

  useEffect(() => {
    let cancelled = false;
    // 窗口变了就必须重取：今天和本月是两个不同的桶。
    void fetch(`${API_BASE}/__request-stats`, { credentials: 'same-origin' })
      .then((response) => (response.ok ? response.json() : Promise.reject(new Error(`HTTP ${response.status}`))))
      .then((data: RequestStatsSnapshot) => { if (!cancelled) { setSnapshot(data); setError(''); } })
      .catch((reason: unknown) => { if (!cancelled) setError(String(reason)); });
    return () => { cancelled = true; };
  }, [keyItem.id, windowValue]);

  const buckets = usageStatsForKey(snapshot, keyItem.id);
  const bucket = windowValue === 'month' ? buckets.month : windowValue === 'day' ? buckets.today : undefined;
  const usedTokens = bucket ? bucket.inputTokens + bucket.outputTokens : 0;
  const usedCost = bucket?.costMicros || 0;
  const tokenLimit = keyItem.quotaTokens || 0;
  const costLimit = keyItem.quotaCostMicros || 0;

  const percent = (used: number, limit: number) => (limit > 0 ? Math.min(100, Math.round((used / limit) * 100)) : 0);
  const toneFor = (pct: number) => (pct >= 100 ? 'full' : pct >= 80 ? 'warn' : 'ok');

  const saveTokens = () => {
    const next = Math.max(0, Math.round(Number(tokensDraft.replace(/[,\s]/g, '')) || 0));
    if (next === tokenLimit) return;
    void onUpdateField(keyItem, 'quotaTokens', next);
  };
  const saveCost = () => {
    const usd = Math.max(0, Number(usdDraft.replace(/[$\s,]/g, '')) || 0);
    const next = Math.round(usd * 1_000_000);
    if (next === costLimit) return;
    void onUpdateField(keyItem, 'quotaCostMicros', next);
  };

  return (
    <div className="field" style={{ marginTop: 10 }}>
      <div className="field-label-row">
        <label>额度窗口</label>
        <InfoTip text="按网关进程的本机时区累计并重置（生产固定 Asia/Shanghai，即北京时间 0 点 / 每月 1 号 0 点）。它是治理闸门，不是预付费：并发的在途请求只按 prompt 预扣，可能小幅超出上限，之后的下一个请求即被拒。" />
      </div>
      <select
        value={windowValue}
        disabled={saving || !canWrite}
        onChange={(event) => void onUpdateField(keyItem, 'quotaWindow', event.target.value)}
      >
        {QuotaWindowOptions.map((option) => (
          <option key={option.value} value={option.value}>{option.label}</option>
        ))}
      </select>

      {!windowValue ? (
        <div className="key-quota-none">该密钥不受额度门约束。</div>
      ) : (
        <div className="key-quota-grid">
          <div className="key-quota-cell">
            <div className="key-quota-head">
              <span>Token</span>
              <b className="font-mono">{formatTokenCount(usedTokens)}</b>
              <small>{tokenLimit ? ` / ${formatTokenCount(tokenLimit)}` : ' / 不限'}</small>
            </div>
            {tokenLimit > 0 ? (
              <div className={`key-quota-track key-quota-track--${toneFor(percent(usedTokens, tokenLimit))}`}>
                <i style={{ width: `${percent(usedTokens, tokenLimit)}%` }} />
              </div>
            ) : null}
            {canWrite ? (
              <input
                type="text"
                inputMode="numeric"
                placeholder="0 = 不限"
                value={tokensDraft}
                disabled={saving}
                onChange={(event) => setTokensDraft(event.target.value)}
                onBlur={saveTokens}
                onKeyDown={(event) => { if (event.key === 'Enter') { event.currentTarget.blur(); } }}
              />
            ) : null}
          </div>
          <div className="key-quota-cell">
            <div className="key-quota-head">
              <span>成本</span>
              <b className="font-mono">{formatCostMicros(usedCost)}</b>
              <small>{costLimit ? ` / ${formatCostMicros(costLimit)}` : ' / 不限'}</small>
            </div>
            {costLimit > 0 ? (
              <div className={`key-quota-track key-quota-track--${toneFor(percent(usedCost, costLimit))}`}>
                <i style={{ width: `${percent(usedCost, costLimit)}%` }} />
              </div>
            ) : null}
            {canWrite ? (
              <input
                type="text"
                inputMode="decimal"
                placeholder="0 = 不限（USD）"
                value={usdDraft}
                disabled={saving}
                onChange={(event) => setUsdDraft(event.target.value)}
                onBlur={saveCost}
                onKeyDown={(event) => { if (event.key === 'Enter') { event.currentTarget.blur(); } }}
              />
            ) : null}
          </div>
        </div>
      )}

      {canWrite && (tokenLimit > 0 || costLimit > 0) ? (
        <div className="key-quota-note">
          已用只含已完成的请求；成本上限只统计「价格表」里设过价的请求，未计价流量不受它约束——
          要卡成本请同时给一个 Token 上限。
        </div>
      ) : null}
      {!canWrite ? <div className="key-quota-note">额度只能由管理员设置。</div> : null}
      {error ? <div className="hint-line error">额度用量读取失败：{error}</div> : null}
    </div>
  );
}
