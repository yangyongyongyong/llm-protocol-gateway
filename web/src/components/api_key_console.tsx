// API 密钥控制中枢的界面骨架：顶部视图切换、左侧密钥队列、右侧详情工作区、
// 全量表格、批量操作浮条。
//
// 每个字段怎么落到 PATCH、转发方案怎么切换，仍然在 apikeys.tsx 里（单一来源）；
// 这里只负责「怎么摆、怎么切、怎么读得快」——同一个 Key 在三种视图里共用同一份
// 服务端状态，不做第二份草稿。
import React from 'react';
import {ProviderLabel} from './provider_label';
import {providerAccountFace} from './provider_select';
import {
  ApiKeyDetailPanel,
  ApiKeyFallbackProvidersModal,
  ApiKeyModelMappingModal,
  ApiKeyQuotaSection,
} from './apikeys';
import {providerSelectOptions} from './provider_select';
import {FilterPopoverSelect, type PopoverFilterOption} from './filter_popover';
import {IconSelect, SearchableModelSelect} from './selects';
import {Badge, BrandIcon, InfoTip, Switch} from './ui';
import {alertDialog, confirmDialog} from '../dialogs';
import {
  API_BASE,
  apiKeyClientBaseURL,
  apiKeyGatewayRoot,
  applyClaudeClientSetup,
  applyCodexClientSetup,
  buildApiKeyCodexModelCatalogJSON,
  buildClaudeCodeSwitcherScript,
  clientConfigsForProtocol,
  codexCatalogPathForHome,
  copyToClipboard,
  localAgentGatewayRoot,
  protocolLabel,
  protocolFromLabel,
  resolveCodexReasoningEffort,
  sanitizeClientConfigID,
  ScheduleModeOptions,
  thinkingDepthSelectOptions,
} from '../lib';
import {
  APIKey,
  ConsoleUser,
  KeyProfile,
  Model,
  OutputEndpoint,
  Protocol,
  Provider,
  Route,
  RouteTestResult,
} from '../types';

export type ApiKeyViewMode = 'split' | 'table';

// 详情工作区要用的回调：直接取 ApikeyDetailPanel 的签名，避免两处漂移。
export type ApiKeyPanelHandlers = Pick<
  React.ComponentProps<typeof ApiKeyDetailPanel>,
  | 'onUpdateField'
  | 'onUpdateBinding'
  | 'onPromoteFallback'
  | 'onUpdateFallbacks'
  | 'onUpdateModelAliases'
  | 'onDelete'
  | 'onClone'
  | 'onRefreshModels'
  | 'onSwitchProfile'
  | 'onCreateProfile'
  | 'onUpdateProfile'
  | 'onDeleteProfile'
>;

// ---------------------------------------------------------------------------
// 纯函数：每一行 / 每一列要展示什么，全从 (key, route, provider, usage) 推出来。
// ---------------------------------------------------------------------------

/** 今日 / 本月请求量（与用量页同口径，来自 /__request-stats 的按密钥聚合）。 */
export type ApiKeyUsage = {
  todayRequests: number;
  monthRequests: number;
};

const ZERO_USAGE: ApiKeyUsage = {todayRequests: 0, monthRequests: 0};

/** 统计快照里只取本页需要的两块：今日 / 本月各自的按密钥聚合。 */
export type ApiKeyUsageSource = {
  today?: {byApiKey?: Array<{apiKeyId?: string; requestCount?: number}>};
  month?: {byApiKey?: Array<{apiKeyId?: string; requestCount?: number}>};
};

export function buildApiKeyUsageMap(snapshot: ApiKeyUsageSource | null | undefined): Record<string, ApiKeyUsage> {
  const map: Record<string, ApiKeyUsage> = {};
  for (const row of snapshot?.today?.byApiKey || []) {
    if (!row?.apiKeyId) continue;
    map[row.apiKeyId] = {...(map[row.apiKeyId] || ZERO_USAGE), todayRequests: row.requestCount || 0};
  }
  for (const row of snapshot?.month?.byApiKey || []) {
    if (!row?.apiKeyId) continue;
    map[row.apiKeyId] = {...(map[row.apiKeyId] || ZERO_USAGE), monthRequests: row.requestCount || 0};
  }
  return map;
}

export type ApiKeyRouteInfo = {
  route?: Route;
  primary?: Provider;
  /** 客户端的协议（输出端点）；没有路由时为空。 */
  outputProtocol?: Protocol;
  /** 入站语种 = 首选 Provider 的母语；与 outputProtocol 不同才发生转换。 */
  inputProtocol?: Protocol;
};

export function apiKeyRouteInfo(key: APIKey, routes: Route[], providers: Provider[]): ApiKeyRouteInfo {
  const route = routes.find((item) => item.id === key.routeId);
  const primary = route ? providers.find((item) => item.id === route.providerId) : undefined;
  return {
    route,
    primary,
    outputProtocol: route?.outputProtocol,
    inputProtocol: primary?.protocol ?? route?.outputProtocol,
  };
}

export type ApiKeyRowModel = ApiKeyRouteInfo & {
  ownerLabel: string;
  ownerIsAdmin: boolean;
  /** 首选上游锁定的模型；空 = 按客户端请求的模型走。 */
  modelLabel: string;
  usage: ApiKeyUsage;
};

export function apiKeyRowModel(
  key: APIKey,
  routes: Route[],
  providers: Provider[],
  usage: ApiKeyUsage | undefined,
  ownerLabel: string,
): ApiKeyRowModel {
  const info = apiKeyRouteInfo(key, routes, providers);
  return {
    ...info,
    ownerLabel,
    ownerIsAdmin: !(key.ownerUserId || '').trim(),
    modelLabel: key.modelOverride?.trim() || info.primary?.defaultModel?.trim() || '按客户端模型',
    usage: usage || ZERO_USAGE,
  };
}

/** 只露头尾、中间打码：截图 / 分享屏幕时不会整串泄露。 */
export function maskKey(key: string) {
  if (key.length <= 14) return key;
  return `${key.slice(0, 8)}${'•'.repeat(6)}${key.slice(-4)}`;
}

// 新建方案 = 完整快照当前顶层转发配置（与 apikeys.tsx 原有实现同口径）。
export function snapshotActiveProfile(keyItem: APIKey, name: string): Partial<KeyProfile> {
  return {
    name,
    routeId: keyItem.routeId,
    modelOverride: keyItem.modelOverride,
    modelAliases: {...(keyItem.modelAliases || {})},
    thinkingDepthOverride: keyItem.thinkingDepthOverride,
    maxOutputTokens: keyItem.maxOutputTokens && keyItem.maxOutputTokens > 0 ? keyItem.maxOutputTokens : 0,
    fallbackProviderIds: [...(keyItem.fallbackProviderIds || [])],
    fallbackModelOverrides: {...(keyItem.fallbackModelOverrides || {})},
    streamEnabled: keyItem.streamEnabled !== false,
    scheduleMode: keyItem.scheduleMode || '',
  };
}

// ---------------------------------------------------------------------------
// 线图标：与侧栏同一套 1.7px 线宽，不额外引图标库。
// ---------------------------------------------------------------------------
function Icon({path, size = 14, className = ''}: {path: React.ReactNode; size?: number; className?: string}) {
  return (
    <svg
      className={`akc-icon ${className}`.trim()}
      viewBox="0 0 24 24"
      width={size}
      height={size}
      fill="none"
      stroke="currentColor"
      strokeWidth={1.7}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {path}
    </svg>
  );
}

const ICONS = {
  key: <><circle cx="8" cy="12" r="4.2" /><path d="M12.2 12H21M17.5 12v3M20.8 12v2.2" /></>,
  copy: <><rect x="9" y="9" width="11" height="11" rx="2.2" /><path d="M15 5.5A2.5 2.5 0 0 0 12.5 3H5.5A2.5 2.5 0 0 0 3 5.5v7A2.5 2.5 0 0 0 5.5 15" /></>,
  check: <path d="m5 12.5 4.5 4.5L19 7" />,
  trash: <><path d="M4 7h16M9.5 7V4.8h5V7" /><path d="m6.5 7 .9 12.2A1.8 1.8 0 0 0 9.2 21h5.6a1.8 1.8 0 0 0 1.8-1.8L17.5 7" /></>,
  layers: <><path d="m12 3 9 5-9 5-9-5 9-5Z" /><path d="m3 13 9 5 9-5" /></>,
  shuffle: <><path d="M3 6.5h3.5l10 11H21M3 17.5h3.5l10-11H21" /><path d="m18.2 3.4 2.8 3.1-2.8 3.1M18.2 14.4l2.8 3.1-2.8 3.1" /></>,
  sliders: <><path d="M5 4v5.5M5 13.5V20M12 4v3M12 11v9M19 4v9M19 17v3" /><circle cx="5" cy="11.5" r="2" /><circle cx="12" cy="9" r="2" /><circle cx="19" cy="15" r="2" /></>,
  code: <><path d="m8.5 8.5-4 3.5 4 3.5M15.5 8.5l4 3.5-4 3.5M13.5 5l-3 14" /></>,
  terminal: <><rect x="2.8" y="4.5" width="18.4" height="15" rx="2.4" /><path d="m7 10 2.5 2.4L7 14.8M12.5 15h4.5" /></>,
  refresh: <><path d="M20 11a8 8 0 1 0-1.8 6.4" /><path d="M20 4.5V11h-6.4" /></>,
  up: <path d="m6 14 6-6 6 6" />,
  down: <path d="m6 10 6 6 6-6" />,
  shield: <path d="M12 3 5 6v5.5c0 4.2 2.9 7.7 7 9.5 4.1-1.8 7-5.3 7-9.5V6l-7-3Z" />,
  shieldCheck: <><path d="M12 3 5 6v5.5c0 4.2 2.9 7.7 7 9.5 4.1-1.8 7-5.3 7-9.5V6l-7-3Z" /><path d="m9 12 2.2 2.2 4-4.2" /></>,
  user: <><circle cx="12" cy="8.2" r="3.6" /><path d="M4.8 20a7.2 7.2 0 0 1 14.4 0" /></>,
  columns: <><rect x="3.5" y="4.5" width="17" height="15" rx="2" /><path d="M10 4.5v15" /></>,
  table: <><rect x="3.5" y="4.5" width="17" height="15" rx="2" /><path d="M3.5 9.5h17M3.5 14.5h17M10 4.5v15" /></>,
  plus: <path d="M12 5v14M5 12h14" />,
  search: <><circle cx="11" cy="11" r="6.5" /><path d="m16 16 4 4" /></>,
  close: <path d="m6 6 12 12M18 6 6 18" />,
  filter: <path d="M4 5.5h16l-6.2 7.4V20l-3.6-2v-5.1L4 5.5Z" />,
  edit: <><path d="M4 20h4.2L20 8.2 15.8 4 4 15.8V20Z" /><path d="m13.6 6.2 4.2 4.2" /></>,
  play: <path d="M8 5.2v13.6L19 12 8 5.2Z" />,
  sort: <><path d="M7 4.5v15M4 16.5l3 3 3-3" /><path d="M13 7h7M13 12h5M13 17h3" /></>,
  pause: <><path d="M9 5.5v13M15 5.5v13" /></>,
  grip: <><circle cx="9" cy="6" r="1.2" /><circle cx="15" cy="6" r="1.2" /><circle cx="9" cy="12" r="1.2" /><circle cx="15" cy="12" r="1.2" /><circle cx="9" cy="18" r="1.2" /><circle cx="15" cy="18" r="1.2" /></>,
};

// ---------------------------------------------------------------------------
// 顶部：标题 + 视图切换 + 主 CTA
// ---------------------------------------------------------------------------
export function ApiKeysTopBar({
  totalKeys,
  enabledKeys,
  viewMode,
  onViewModeChange,
  onCreate,
  createDisabled,
}: {
  totalKeys: number;
  enabledKeys: number;
  viewMode: ApiKeyViewMode;
  onViewModeChange: (mode: ApiKeyViewMode) => void;
  onCreate: () => void;
  createDisabled?: boolean;
}) {
  return (
    <div className="akc-topbar">
      <div className="akc-topbar-title">
        <div className="akc-topbar-heading">
          <h2 className="panel-title">API 密钥控制中枢</h2>
          <span className="akc-count-chip" title={`共 ${totalKeys} 把密钥`}>{totalKeys} 密钥</span>
          {enabledKeys !== totalKeys ? <span className="akc-count-chip muted">{enabledKeys} 启用中</span> : null}
        </div>
        <p className="panel-desc">客户端使用 Bearer 或 x-api-key 携带密钥，网关自动完成双向透传与故障转移。</p>
      </div>
      <div className="akc-topbar-actions">
        <div className="akc-segmented" role="group" aria-label="视图模式">
          <button
            type="button"
            className={`akc-segmented-btn${viewMode === 'split' ? ' active' : ''}`}
            onClick={() => onViewModeChange('split')}
            title="左列选密钥，右侧实时配置"
          >
            <Icon path={ICONS.columns} />
            <span>双栏聚焦</span>
          </button>
          <button
            type="button"
            className={`akc-segmented-btn${viewMode === 'table' ? ' active' : ''}`}
            onClick={() => onViewModeChange('table')}
            title="全宽矩阵，逐列对比所有密钥"
          >
            <Icon path={ICONS.table} />
            <span>全量数据表</span>
          </button>
        </div>
        <button type="button" className="akc-cta" disabled={createDisabled} onClick={onCreate}>
          <Icon path={ICONS.plus} />
          <span>新建 API 密钥</span>
        </button>
      </div>
    </div>
  );
}


/**
 * 「输入 Provider」筛选选项。不能直接用 providerSelectOptions 的 label：那个
 * label 是给「左品牌栏 + 右账号栏」两级菜单用的账号短名（WorkBuddy 的几个订阅
 * 账号会并排显示成同一条「WorkBuddy 订阅」，在平铺菜单里完全分不出来）。这里写
 * 完整名称；同名时把脱敏账号补到第二行，才选得中想要的那个。
 */
export function apiKeyProviderFilterOptions(providers: Provider[]): PopoverFilterOption[] {
  return providerSelectOptions(providers, {withProtocol: false}).map((option) => {
    const provider = providers.find((item) => item.id === option.value);
    const full = provider ? providerAccountFace(provider) : option.label;
    return {
      id: option.value,
      label: full,
      // 第二行只放「名字里没有的账号」：名字本身已经带了账号（或没有账号可认）就省略
      hint: provider && option.label !== full && !full.includes(option.label) ? option.label : undefined,
      icon: option.icon,
    };
  });
}

// ---------------------------------------------------------------------------
// 筛选条：搜索 + 所属用户 / 输入 Provider / 输出协议（两种视图共用）
// ---------------------------------------------------------------------------
export type ApiKeyFiltersProps = {
  keyword: string;
  setKeyword: (value: string) => void;
  ownerOptions: Array<{id: string; label: string}>;
  owner: string[];
  setOwner: (value: string[]) => void;
  /** 普通用户看不到「所属用户」，整列收起而不是置灰。 */
  showOwner: boolean;
  provider: string[];
  setProvider: (value: string[]) => void;
  protocol: string[];
  setProtocol: (value: string[]) => void;
  /** Provider id → 展示名：筛选胶囊上要写人话，而不是内部 id。 */
  providerLabels?: Record<string, string>;
  /** 排序：最近创建 / 名称 / 所属用户（与以前表头的排序按钮同口径）。 */
  sort: string;
  setSort: (value: string) => void;
  reset: () => void;
};

/** 当前生效的筛选，做成可逐个移除的胶囊；没有筛选时整行不占位。 */
function activeFilterChips(filters: ApiKeyFiltersProps) {
  const chips: Array<{key: string; label: string; clear: () => void}> = [];
  const keyword = filters.keyword.trim();
  if (keyword) chips.push({key: 'kw', label: `“${keyword}”`, clear: () => filters.setKeyword('')});
  for (const id of filters.owner) {
    chips.push({key: `owner-${id}`, label: filters.ownerOptions.find((item) => item.id === id)?.label || id, clear: () => filters.setOwner(filters.owner.filter((item) => item !== id))});
  }
  for (const id of filters.provider) {
    chips.push({key: `provider-${id}`, label: filters.providerLabels?.[id] || id, clear: () => filters.setProvider(filters.provider.filter((item) => item !== id))});
  }
  for (const id of filters.protocol) {
    chips.push({key: `protocol-${id}`, label: protocolLabel(id as Protocol), clear: () => filters.setProtocol(filters.protocol.filter((item) => item !== id))});
  }
  return chips;
}

export function hasActiveApiKeyFilters(filters: ApiKeyFiltersProps) {
  return Boolean(filters.keyword.trim())
    || filters.owner.length > 0
    || filters.provider.length > 0
    || filters.protocol.length > 0;
}

export function ApiKeyFilterFields({filters, providers}: {filters: ApiKeyFiltersProps; providers: Provider[]}) {
  return (
    <>
      <div className="akc-search">
        <Icon path={ICONS.search} size={14} />
        <input
          type="search"
          value={filters.keyword}
          placeholder="搜索名称 / 密钥…"
          aria-label="搜索密钥"
          autoComplete="one-time-code"
          data-1p-ignore
          data-lpignore="true"
          name="apiKeyFilterKeyword"
          onChange={(event) => filters.setKeyword(event.target.value)}
        />
      </div>
      <div className={`akc-filter-grid${filters.showOwner ? '' : ' no-owner'}`}>
        {filters.showOwner ? (
          <div className="akc-filter-cell">
            <label>所属用户</label>
            <FilterPopoverSelect
              label="所属用户"
              options={filters.ownerOptions}
              selected={filters.owner}
              onChange={filters.setOwner}
              allLabel="全部用户"
            />
          </div>
        ) : null}
        <div className="akc-filter-cell">
          <label>输入 Provider</label>
          <FilterPopoverSelect
            label="输入 Provider"
            options={apiKeyProviderFilterOptions(providers)}
            selected={filters.provider}
            onChange={filters.setProvider}
            allLabel="全部 Provider"
          />
        </div>
        <div className="akc-filter-cell">
          <label>输出协议</label>
          <FilterPopoverSelect
            label="输出协议"
            options={(['openai_chat', 'openai_responses', 'claude'] as Protocol[]).map((protocol) => ({id: protocol, label: protocolLabel(protocol)}))}
            selected={filters.protocol}
            onChange={filters.setProtocol}
            allLabel="全部协议"
          />
        </div>
      </div>
    </>
  );
}

// ---------------------------------------------------------------------------
// 左侧密钥队列
// ---------------------------------------------------------------------------
export function ApiKeyFleetPane({
  keys,
  totalKeys,
  selectedKeyId,
  checkedIds,
  usage,
  routes,
  providers,
  ownerLabel,
  filters,
  saving,
  onSelect,
  onToggleCheck,
  onSelectAll,
  onClearSelection,
  onBatchDelete,
  onCopyKey,
  copiedKeyId,
  onToggleEnabled,
  createDisabled,
  onCreate,
}: {
  keys: APIKey[];
  totalKeys: number;
  selectedKeyId: string;
  checkedIds: string[];
  usage: Record<string, ApiKeyUsage>;
  routes: Route[];
  providers: Provider[];
  ownerLabel: (key: APIKey) => string;
  filters: ApiKeyFiltersProps;
  saving: boolean;
  onSelect: (key: APIKey) => void;
  onToggleCheck: (keyId: string, index: number, shiftKey: boolean) => void;
  onSelectAll: () => void;
  onClearSelection: () => void;
  onBatchDelete: () => void;
  onCopyKey: (key: APIKey) => void;
  copiedKeyId: string;
  /** 行内一键启用 / 停用：列表里就能直接切换，不必先点进详情。 */
  onToggleEnabled: (key: APIKey) => void;
  createDisabled?: boolean;
  onCreate: () => void;
}) {
  const allSelected = keys.length > 0 && keys.every((key) => checkedIds.includes(key.id));
  const someSelected = checkedIds.length > 0 && !allSelected;
  const chips = activeFilterChips(filters);

  return (
    <section className="akc-list card" aria-label="API 密钥列表">
      <header className="akc-list-head">
        <div className="akc-list-title">
          <Icon path={ICONS.key} size={15} className="accent" />
          <h3>API 密钥</h3>
        </div>
        <span className="akc-list-stat">
          显示 <b>{keys.length}</b> / {totalKeys} 个密钥
          {checkedIds.length > 0 ? <span className="akc-list-stat-checked"> · 已选 {checkedIds.length}</span> : null}
        </span>
      </header>

      <div className="akc-list-filters">
        <ApiKeyFilterFields filters={filters} providers={providers} />
        {checkedIds.length > 0 ? (
          <div className="akc-bulk-inline">
            <button type="button" className="akc-bulk-delete" disabled={saving} onClick={onBatchDelete}>
              <Icon path={ICONS.trash} size={13} />
              <span>删除选中（{checkedIds.length}）</span>
            </button>
            <button type="button" className="akc-bulk-clear" onClick={onClearSelection}>清除选择</button>
          </div>
        ) : null}
      </div>

      <div className={`akc-list-tools${chips.length === 0 ? ' no-chips' : ''}`}>
        <div className="akc-active-filters">
          {chips.map((chip) => (
            <button key={chip.key} type="button" className="akc-filter-chip" title={`移除筛选：${chip.label}`} onClick={chip.clear}>
              <span>{chip.label}</span>
              <Icon path={ICONS.close} size={10} />
            </button>
          ))}
          {chips.length > 1 ? (
            <button type="button" className="akc-link" onClick={filters.reset}>清空</button>
          ) : null}
        </div>
        <label className="akc-sort">
          <Icon path={ICONS.sort} size={12} />
          <select
            value={filters.sort}
            aria-label="排序方式"
            onChange={(event) => filters.setSort(event.target.value)}
          >
            <option value="createdAt">最近创建</option>
            <option value="name">名称 A→Z</option>
            <option value="owner">按所属用户</option>
          </select>
        </label>
      </div>

      <div className="akc-list-columns">
        <label className="akc-check-all" title={allSelected ? '取消全选' : '全选当前筛选'}>
          <input
            type="checkbox"
            checked={allSelected}
            ref={(node) => { if (node) node.indeterminate = someSelected; }}
            disabled={keys.length === 0 || saving}
            aria-label="全选当前列表"
            onChange={(event) => { if (event.target.checked) onSelectAll(); else onClearSelection(); }}
          />
        </label>
        <span>名称 / 协议</span>
        <span className="akc-list-columns-right">所属用户</span>
      </div>

      <div className="akc-list-body" role="listbox" aria-label="密钥">
        {keys.length === 0 ? (
          <div className="akc-list-empty">
            <Icon path={ICONS.filter} size={26} />
            <p>{totalKeys === 0 ? '还没有任何 API 密钥' : '没有符合筛选条件的密钥'}</p>
            {totalKeys === 0 ? (
              <button type="button" className="akc-link" disabled={createDisabled} onClick={onCreate}>新建第一把密钥</button>
            ) : (
              <button type="button" className="akc-link" onClick={filters.reset}>重置筛选条件</button>
            )}
          </div>
        ) : keys.map((key, index) => {
          const model = apiKeyRowModel(key, routes, providers, usage[key.id], ownerLabel(key));
          const isActive = key.id === selectedKeyId;
          const isChecked = checkedIds.includes(key.id);
          const converted = model.inputProtocol && model.outputProtocol && model.inputProtocol !== model.outputProtocol;
          return (
            <div
              key={key.id}
              role="option"
              aria-selected={isActive}
              tabIndex={0}
              className={`akc-key-row${isActive ? ' active' : ''}${isChecked ? ' checked' : ''}${key.enabled ? '' : ' off'}`}
              onClick={() => onSelect(key)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' || event.key === ' ') {
                  event.preventDefault();
                  onSelect(key);
                }
              }}
            >
              <label className="akc-row-check" onClick={(event) => event.stopPropagation()}>
                <input
                  type="checkbox"
                  checked={isChecked}
                  disabled={saving}
                  aria-label={`选择 ${key.name}`}
                  onChange={(event) => onToggleCheck(key.id, index, !!(event.nativeEvent as MouseEvent).shiftKey)}
                />
              </label>
              <div className="akc-key-main">
                <div className="akc-key-name-row">
                  <span className="akc-key-name" title={key.name}>{key.name}</span>
                  <span className={`akc-key-dot${key.enabled ? ' on' : ''}`} title={key.enabled ? '启用中' : '已停用'} />
                </div>
                <div className="akc-key-protocols">
                  {model.outputProtocol ? (
                    converted ? (
                      <>
                        <span className="akc-proto-in" title="首选上游的母语协议">{protocolLabel(model.inputProtocol!)}</span>
                        <span className="akc-proto-arrow" aria-hidden="true">→</span>
                        <span className="akc-proto-out converted" title="客户端使用的协议">{protocolLabel(model.outputProtocol)}</span>
                      </>
                    ) : (
                      <span className="akc-proto-single" title="输入与输出同一协议，网关只做透传">
                        {protocolLabel(model.outputProtocol)}
                        <em>透传</em>
                      </span>
                    )
                  ) : (
                    <span className="akc-proto-missing">路由已失效</span>
                  )}
                  <span className="akc-key-meta">
                    {model.usage.todayRequests > 0 ? `今日 ${model.usage.todayRequests.toLocaleString()} 次` : '今日无请求'}
                  </span>
                </div>
              </div>
              <div className="akc-key-side">
                <span className={`akc-owner-chip${model.ownerIsAdmin ? ' admin' : ''}`} title={`所属用户：${model.ownerLabel}`}>
                  <Icon path={model.ownerIsAdmin ? ICONS.shieldCheck : ICONS.user} size={11} />
                  <span>{model.ownerLabel}</span>
                </span>
                <button
                  type="button"
                  className={`akc-key-copy${copiedKeyId === key.id ? ' copied' : ''}`}
                  title="复制 API 密钥"
                  aria-label={`复制 ${key.name} 的密钥`}
                  onClick={(event) => {
                    event.stopPropagation();
                    onCopyKey(key);
                  }}
                >
                  <Icon path={copiedKeyId === key.id ? ICONS.check : ICONS.copy} size={13} />
                </button>
                <button
                  type="button"
                  className={`akc-key-toggle${key.enabled ? '' : ' enable'}`}
                  disabled={saving}
                  title={key.enabled ? '停用该密钥（客户端将立即收到 401）' : '重新启用该密钥'}
                  aria-label={`${key.enabled ? '停用' : '启用'} ${key.name}`}
                  onClick={(event) => {
                    event.stopPropagation();
                    onToggleEnabled(key);
                  }}
                >
                  <Icon path={key.enabled ? ICONS.pause : ICONS.play} size={12} />
                </button>
              </div>
            </div>
          );
        })}
      </div>

      <footer className="akc-list-foot">
        <span>勾选后可批量管理 · Shift 点选连续多选</span>
        {checkedIds.length > 0 ? <span className="akc-list-foot-count">已勾选 {checkedIds.length} 项</span> : null}
      </footer>
    </section>
  );
}

// ---------------------------------------------------------------------------
// 右侧详情工作区
// ---------------------------------------------------------------------------
export type ApiKeyDetailTab = 'routing' | 'params' | 'client' | 'quota';

export type ApiKeyWorkspaceProps = {
  keyItem: APIKey;
  providers: Provider[];
  models: Model[];
  routes: Route[];
  endpoints: OutputEndpoint[];
  owners?: ConsoleUser[];
  saving: boolean;
  canWriteQuota: boolean;
  // 没有 keys.delete 时不渲染删除按钮（额度编辑由 canWriteQuota 单独控制）
  canDelete?: boolean;
  testingProviderID: string;
  tunnelRunning: boolean;
  livePublicURL: string;
  fixedOutputLabels: string[];
  panel: ApiKeyPanelHandlers;
  onToggleEnabled: (key: APIKey) => void;
  onUpdateOwner?: (key: APIKey, ownerUserId: string) => Promise<void>;
  onToast: (message: string) => void;
  /** 用来拼客户端 Base URL 的公网根地址（管理员开关控制；普通用户固定内网）。 */
  publicBase: string;
  /** 内存视图模式：表格里双击打开抽屉时也复用同一套详情。 */
  onTest?: (key: APIKey, prompt: string) => Promise<RouteTestResult | null>;
};

function useCopiedToken(timeoutMs = 1800) {
  const [token, setToken] = React.useState('');
  const timer = React.useRef<number | null>(null);
  React.useEffect(() => () => { if (timer.current !== null) window.clearTimeout(timer.current); }, []);
  const flash = React.useCallback((next: string) => {
    setToken(next);
    if (timer.current !== null) window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setToken(''), timeoutMs);
  }, [timeoutMs]);
  return [token, flash] as const;
}

export function ApiKeyDetailWorkspace(props: ApiKeyWorkspaceProps) {
  const {
    keyItem, providers, models, routes, endpoints, owners, saving, canWriteQuota,
    testingProviderID, tunnelRunning, livePublicURL, fixedOutputLabels, panel, onToast,
  } = props;
  const canDelete = props.canDelete !== false;
  const { route, primary, outputProtocol } = apiKeyRouteInfo(keyItem, routes, providers);
  const [tab, setTab] = React.useState<ApiKeyDetailTab>('routing');
  // 有公网地址（隧道或域名反代）时默认就按公网给配置：绑回环的部署里内网地址
  // 对浏览器这边根本不通，默认给内网等于默认给一个连不上的 URL。
  const [publicPreview, setPublicPreview] = React.useState(Boolean(props.publicBase));
  const [copied, flashCopied] = useCopiedToken();
  const [fallbackModalOpen, setFallbackModalOpen] = React.useState(false);
  const profiles = keyItem.profiles || [];
  const activeProfile = profiles.find((item) => item.id === (keyItem.activeProfileId || ''));
  const fallbackIds = keyItem.fallbackProviderIds || [];
  // 弹窗里只列还存在的 Provider；悬空引用在保存时顺带清掉。
  const validFallbackIds = fallbackIds.filter((id) => providers.some((item) => item.id === id));
  const staleFallbackCount = fallbackIds.length - validFallbackIds.length;
  const publicAvailable = Boolean(props.publicBase);
  const effectivePublicBase = publicPreview && publicAvailable ? props.publicBase : '';

  React.useEffect(() => {
    if (!publicAvailable && publicPreview) setPublicPreview(false);
  }, [publicAvailable, publicPreview]);

  function copy(text: string, message: string, token = 'plain') {
    if (!text) {
      // 空文本不是复制，而是"动作完成"的反馈：一键写入本机写完配置后走这条
      // 路（它没有要复制的东西）。以前这里直接 return，写入成功什么都不弹。
      flashCopied(token);
      if (message) onToast(message);
      return;
    }
    // 局域网 HTTP 下 navigator.clipboard 不存在，必须走带 execCommand 降级的
    // copyToClipboard；直接调 clipboard 会让点击静默失败。
    void copyToClipboard(text).then((ok) => {
      if (!ok) {
        onToast('复制失败，请手动选中文本复制');
        return;
      }
      flashCopied(token);
      onToast(message);
    });
  }

  const clientURL = route ? apiKeyClientBaseURL(route, endpoints, effectivePublicBase) : '';
  // 三条端点共用一个网关根地址，路径各不相同（见下面 url 菜单）。
  const gatewayURL = apiKeyGatewayRoot(endpoints, effectivePublicBase).replace(/\/$/, '');
  const [urlMenuOpen, setUrlMenuOpen] = React.useState(false);

  const tabs: Array<{id: ApiKeyDetailTab; label: string; icon: React.ReactNode}> = [
    {id: 'routing', label: '转发方案与容灾链路', icon: ICONS.shuffle},
    {id: 'params', label: '基本绑定与模型覆写', icon: ICONS.sliders},
    {id: 'client', label: '客户端配置与调用示例', icon: ICONS.code},
    {id: 'quota', label: '额度与用量治理', icon: ICONS.shield},
  ];

  return (
    <section className="akc-detail card" aria-label="密钥详情">
      <header className="akc-detail-head">
        <div className="akc-detail-identity">
          <Icon path={ICONS.key} size={15} className="accent" />
          <h3 className="akc-detail-name" title={keyItem.name}>{keyItem.name}</h3>
          <button
            type="button"
            className={`akc-status-pill${keyItem.enabled ? ' on' : ''}`}
            disabled={saving}
            title={keyItem.enabled ? '点击停用该密钥' : '点击启用该密钥'}
            onClick={() => props.onToggleEnabled(keyItem)}
          >
            <span className="akc-status-dot" />
            {keyItem.enabled ? '启用中' : '已停用'}
          </button>
          {/* 不再显示「输出协议 → 上游协议」：同一把 Key 的三条端点都能用，
              上游还可能多协议按请求切换，这个箭头会把“默认值”读成“绑定值”。 */}
          {outputProtocol ? null : <Badge tone="red">路由失效</Badge>}
          {keyItem.activeProviderId && keyItem.activeProviderId !== (route?.providerId || '') ? (
            <Badge tone="amber">已切备选</Badge>
          ) : null}
        </div>
        <div className="akc-detail-actions">
          {/* 一把 Key 三条端点都能用，所以「复制 URL」要给三条，而不是按输出
              协议只给一条。用哪条由客户端自己决定——这也正是删掉「输出协议」
              下拉之后用户该看的东西。 */}
          <div className="akc-url-picker">
            <button
              type="button"
              className={`akc-action${copied.startsWith('url-') ? ' copied' : ''}`}
              disabled={!gatewayURL}
              title={gatewayURL ? '选择要复制的端点地址' : '请先为密钥绑定路由'}
              onClick={() => setUrlMenuOpen((open) => !open)}
            >
              <Icon path={copied.startsWith('url-') ? ICONS.check : ICONS.copy} size={13} />
              <span>{copied.startsWith('url-') ? '已复制' : '复制 URL'}</span>
            </button>
            {urlMenuOpen ? (
              <div className="akc-url-menu" role="menu">
                {([
                  {id: 'claude', label: 'Claude（Messages）', path: '/anthropic'},
                  {id: 'chat', label: 'OpenAI Chat', path: '/v1'},
                  {id: 'responses', label: 'OpenAI Responses（Codex）', path: '/openai/v1'},
                ] as const).map((item) => {
                  const full = `${gatewayURL}${item.path}`;
                  return (
                    <button
                      key={item.id}
                      type="button"
                      role="menuitem"
                      className="akc-url-menu-item"
                      title={full}
                      onClick={() => {
                        setUrlMenuOpen(false);
                        copy(full, `已复制 ${item.label} 地址：${full}`, `url-${item.id}`);
                      }}
                    >
                      <span className="akc-url-menu-label">{item.label}</span>
                      <code className="akc-url-menu-path">{item.path}</code>
                    </button>
                  );
                })}
              </div>
            ) : null}
          </div>
          <button
            type="button"
            className={`akc-action${copied === 'key' ? ' copied' : ''}`}
            onClick={() => copy(keyItem.key, '已复制 API Key', 'key')}
          >
            <Icon path={copied === 'key' ? ICONS.check : ICONS.key} size={13} />
            <span>{copied === 'key' ? '已复制' : '复制 Key'}</span>
          </button>
          <button type="button" className="akc-action" disabled={saving} onClick={() => panel.onClone(keyItem)} title="复制配置新建一把密钥">
            <Icon path={ICONS.layers} size={13} />
            <span>克隆</span>
          </button>
          {canDelete ? (
            <button type="button" className="akc-action danger" disabled={saving} onClick={() => void panel.onDelete(keyItem)} title="删除该密钥">
              <Icon path={ICONS.trash} size={13} />
              <span>删除</span>
            </button>
          ) : null}
        </div>
      </header>

      <div className="akc-tabs" role="tablist" aria-label="密钥配置">
        {tabs.map((item) => (
          <button
            key={item.id}
            type="button"
            role="tab"
            aria-selected={tab === item.id}
            className={`akc-tab${tab === item.id ? ' active' : ''}`}
            onClick={() => setTab(item.id)}
          >
            <Icon path={item.icon} size={13} />
            <span>{item.label}</span>
          </button>
        ))}
        <span className="akc-tab-meta">
          生效方案 <b>{activeProfile?.name || '顶层配置'}</b>
        </span>
      </div>

      <div className="akc-detail-body">
        {tab === 'routing' ? (
          <RoutingTab
            keyItem={keyItem}
            providers={providers}
            models={models}
            saving={saving}
            testingProviderID={testingProviderID}
            primaryProviderId={route?.providerId || ''}
            outputProtocol={route?.outputProtocol || 'openai_chat'}
            panel={panel}
            onOpenFallbackModal={() => setFallbackModalOpen(true)}
          />
        ) : null}

        {tab === 'params' ? (
          <ParamsTab
            keyItem={keyItem}
            providers={providers}
            models={models}
            owners={owners}
            saving={saving}
            testingProviderID={testingProviderID}
            fixedOutputLabels={fixedOutputLabels}
            primaryProviderId={route?.providerId || ''}
            primaryProvider={primary}
            bindingOutputProtocol={outputProtocol || 'openai_chat'}
            panel={panel}
            publicPreview={publicPreview}
            publicAvailable={publicAvailable}
            onPublicPreviewChange={setPublicPreview}
          />
        ) : null}

        {tab === 'client' ? (
          <ClientTab
            keyItem={keyItem}
            route={route}
            primary={primary}
            endpoints={endpoints}
            tunnelRunning={tunnelRunning}
            livePublicURL={livePublicURL}
            clientURL={clientURL}
            publicPreview={publicPreview}
            publicAvailable={publicAvailable}
            onPublicPreviewChange={setPublicPreview}
            onCopy={copy}
            copied={copied}
            onTest={props.onTest}
            onUpdateField={panel.onUpdateField}
          />
        ) : null}

        {tab === 'quota' ? (
          <div className="akc-stack">
            <div className="akc-card">
              <div className="akc-card-head">
                <span className="akc-card-title">
                  <Icon path={ICONS.shield} size={13} className="green" />
                  额度窗口与上限
                </span>
                <span className="akc-card-note">按网关本机时区累计；是治理闸门，不是预付费</span>
              </div>
              <ApiKeyQuotaSection
                keyItem={keyItem}
                saving={saving}
                canWrite={canWriteQuota}
                onUpdateField={panel.onUpdateField}
              />
            </div>
            <QuotaUsageCard keyItem={keyItem} />
          </div>
        ) : null}
      </div>

      {fallbackModalOpen ? (
        <ApiKeyFallbackProvidersModal
          preferredProviderId={route?.providerId || ''}
          providers={providers}
          models={models}
          selectedIds={validFallbackIds}
          modelOverrides={keyItem.fallbackModelOverrides || {}}
          staleCount={staleFallbackCount}
          saving={saving}
          testingProviderID={testingProviderID}
          onRefreshModels={panel.onRefreshModels}
          onClose={() => setFallbackModalOpen(false)}
          onSave={async (ids, overrides) => {
            await panel.onUpdateFallbacks(keyItem, ids, overrides);
            setFallbackModalOpen(false);
          }}
        />
      ) : null}
    </section>
  );
}

// ---- Tab 1：转发方案与容灾链路 ------------------------------------------------
function RoutingTab({
  keyItem,
  providers,
  models,
  saving,
  testingProviderID,
  primaryProviderId,
  outputProtocol,
  panel,
  onOpenFallbackModal,
}: {
  keyItem: APIKey;
  providers: Provider[];
  models: Model[];
  saving: boolean;
  testingProviderID: string;
  primaryProviderId: string;
  // 首选接线的输出协议。「设为首选」保持它不变：客户端手里的协议不因为换了一台
  // 上游就变，需要转换由网关做。
  outputProtocol: Protocol;
  panel: ApiKeyPanelHandlers;
  onOpenFallbackModal: () => void;
}) {
  const profiles = keyItem.profiles || [];
  const activeProfileId = keyItem.activeProfileId || '';
  const activeProfile = profiles.find((item) => item.id === activeProfileId);
  // Provider 是软删除（可回收站恢复），删掉后 Key 上的备选引用会被故意保留。
  // 但在这条链路上它们永远不可能被尝试到（没有 Provider 可取），所以不能当成
  // 正常节点排进流水线：占位、可拖、还能「设为首选」，只会误导人。
  const fallbackRefs = keyItem.fallbackProviderIds || [];
  const fallbackIds = fallbackRefs.filter((id) => providers.some((item) => item.id === id));
  // 悬空引用只在「打开配置备选」时随保存一起清理，不在打开页面时自动写回：
  // 万一 Provider 刚从回收站恢复，自动清理会把用户的备选链改坏。
  const staleFallbackCount = fallbackRefs.length - fallbackIds.length;
  const overrides = keyItem.fallbackModelOverrides || {};
  const primary = providers.find((item) => item.id === primaryProviderId);
  // 「在服务」= 后端记录的真实服务节点。会话分散下多台同时服务是正常的：最近
  // 10 分钟内答过请求的每一台都算，只标一台会把其余几台显示成「待命」，看起来
  // 像分散没生效。老后端没有这个字段时退回粘滞位，再退回首选。
  const servingProviderIds = keyItem.servingProviderIds?.length
    ? keyItem.servingProviderIds
    : [keyItem.servingProviderId || keyItem.activeProviderId || primaryProviderId];
  const isServing = (id: string) => Boolean(id) && servingProviderIds.includes(id);
  const [dragState, setDragState] = React.useState<RowDragState>({draggingId: '', overId: '', after: false});
  const drag: RowDragBinding = {
    state: dragState,
    onPointerDown: (event, id) => {
      const list = event.currentTarget.closest('.akc-chain-list');
      if (!list) return;
      const startY = event.clientY;
      let active = false;
      const state: RowDragState = {draggingId: '', overId: '', after: false};

      // 命中哪一行、落在该行上半还是下半：用列表里每一行的中线判断，比逐行绑
      // pointerenter 稳（快速拖动时指针可能跳过整行）。
      const updateTarget = (clientY: number, targetId: string) => {
        const rows = [...list.querySelectorAll<HTMLElement>('.akc-chain-row.draggable')];
        for (const row of rows) {
          const rect = row.getBoundingClientRect();
          if (clientY < rect.top || clientY > rect.bottom) continue;
          const rowId = row.dataset.fallbackId || '';
          if (!rowId || rowId === targetId) { state.overId = ''; return; }
          state.overId = rowId;
          state.after = clientY > rect.top + rect.height / 2;
          return;
        }
        state.overId = '';
      };

      const onMove = (moveEvent: PointerEvent) => {
        if (!active) {
          if (Math.abs(moveEvent.clientY - startY) < 4) return;
          active = true;
          state.draggingId = id;
          document.body.classList.add('akc-dragging');
        }
        moveEvent.preventDefault();
        updateTarget(moveEvent.clientY, id);
        setDragState({...state});
      };

      const finish = () => {
        window.removeEventListener('pointermove', onMove);
        window.removeEventListener('pointerup', onUp);
        window.removeEventListener('pointercancel', onCancel);
        document.body.classList.remove('akc-dragging');
        if (!active) return;
        const {overId: over, after} = state;
        setDragState({draggingId: '', overId: '', after: false});
        if (!over) return;
        const next = moveFallbackId(fallbackIds, id, over, after);
        if (next === fallbackIds) return;
        void panel.onUpdateFallbacks(keyItem, next, pruneOverrides(overrides, next));
      };
      const onUp = () => finish();
      const onCancel = () => {
        state.overId = '';
        active = active ? active : false;
        window.removeEventListener('pointermove', onMove);
        window.removeEventListener('pointerup', onUp);
        window.removeEventListener('pointercancel', onCancel);
        document.body.classList.remove('akc-dragging');
        setDragState({draggingId: '', overId: '', after: false});
      };

      window.addEventListener('pointermove', onMove, {passive: false});
      window.addEventListener('pointerup', onUp);
      window.addEventListener('pointercancel', onCancel);
    },
  };
  return (
    <div className="akc-stack">
      {/* 方案切换条：一行放完，右侧是新建 / 重命名 / 删除 */}
      <div className="akc-bar">
        <div className="akc-bar-main">
          <span className="akc-bar-label">转发方案：</span>
          <div className="akc-chips">
            {profiles.length === 0 ? (
              <span className="akc-scheme-chip active" title="未保存任何方案，直接使用顶层配置">
                当前配置<span className="akc-scheme-mark">生效</span>
              </span>
            ) : profiles.map((profile) => {
              const isActive = profile.id === activeProfileId;
              return (
                <button
                  key={profile.id}
                  type="button"
                  className={`akc-scheme-chip${isActive ? ' active' : ''}`}
                  disabled={saving || isActive}
                  onClick={() => void panel.onSwitchProfile(keyItem, profile.id)}
                  title={isActive ? '当前生效方案' : `切换到「${profile.name}」`}
                >
                  {profile.name}
                  {isActive ? <span className="akc-scheme-mark">生效</span> : null}
                </button>
              );
            })}
          </div>
        </div>
        <div className="akc-bar-actions">
          <SchemeActions
            keyItem={keyItem}
            activeProfile={activeProfile}
            saving={saving}
            onCreate={panel.onCreateProfile}
            onUpdate={panel.onUpdateProfile}
            onDelete={panel.onDeleteProfile}
          />
        </div>
      </div>

      {/* 调度模式：分段控件，一行 */}
      <div className="akc-bar">
        <div className="akc-bar-main">
          <span className="akc-bar-label">
            <Icon path={ICONS.shuffle} size={13} className="accent" />
            调度模式：
          </span>
          <div className="akc-mini-segmented" role="group" aria-label="调度模式">
            {ScheduleModeOptions.map((option) => (
              <button
                key={option.value || 'order'}
                type="button"
                className={(keyItem.scheduleMode || '') === option.value ? 'active' : ''}
                disabled={saving || (fallbackIds.length === 0 && option.value !== '')}
                title={option.hint}
                onClick={() => void panel.onUpdateField(keyItem, 'scheduleMode', option.value)}
              >
                {option.label.replace(/（.*?）/, '')}
                {option.value === '' ? <span className="akc-seg-note">默认</span> : null}
              </button>
            ))}
          </div>
        </div>
        <span className="akc-bar-hint">
          <InfoTip text="同一段对话仍然钉在它已建好缓存的账号上（会话粘性优先于调度模式）；被标记「异常」的候选永远排在最后，但不会被跳过。" />
          {fallbackIds.length === 0 ? '配置备选后才能切换调度模式' : '会话粘性优先于调度模式（命中 Prompt Cache）'}
        </span>
      </div>

      {/* 首选 + 备选流水线 */}
      <div className="akc-chain">
        <div className="akc-chain-head">
          <span className="akc-chain-title">
            <Icon path={ICONS.shield} size={13} className="green" />
            首选节点与备选降级流水线（共 {1 + fallbackIds.length} 个节点{staleFallbackCount > 0 ? ` · 另有 ${staleFallbackCount} 个已删除` : ''}）
          </span>
          <span className="akc-chain-hint">支持一键切换首选、独立锁定模型与探测</span>
        </div>

        <div className="akc-chain-list">
          <PipelineRow
            primary
            rank="首选"
            provider={primary}
            fallbackProviderId={primaryProviderId}
            model={keyItem.modelOverride || primary?.defaultModel || ''}
            modelMuted={!keyItem.modelOverride}
            models={primary ? models.filter((model) => model.providerId === primary.id) : []}
            onPickModel={(model) => void panel.onUpdateField(keyItem, 'modelOverride', model)}
            onClearModel={() => void panel.onUpdateField(keyItem, 'modelOverride', '')}
            serving={isServing(primaryProviderId) || servingProviderIds.length === 0}
            providerModelCount={primary ? models.filter((model) => model.providerId === primary.id).length : 0}
            saving={saving}
            testingProviderID={testingProviderID}
            onRefreshModels={panel.onRefreshModels}
            onRemove={() => undefined}
            onPromote={() => undefined}
          />
          {fallbackIds.map((id, index) => {
            const provider = providers.find((item) => item.id === id);
            const override = overrides[id] || '';
            const inherits = !override && Boolean(primary && provider && provider.protocol === primary.protocol);
            return (
              <PipelineRow
                key={id}
                rank={`备选 ${index + 1}`}
                provider={provider}
                fallbackProviderId={id}
                model={override || (inherits ? '请求模型' : '')}
                modelMuted={!override}
                models={provider ? models.filter((model) => model.providerId === provider.id) : []}
                onPickModel={(model) => void panel.onUpdateFallbacks(
                  keyItem, fallbackIds, {...overrides, [id]: model},
                )}
                onClearModel={() => void panel.onUpdateFallbacks(
                  keyItem, fallbackIds, removeKey(overrides, id),
                )}
                serving={isServing(id)}
                saving={saving}
                testingProviderID={testingProviderID}
                drag={drag}
                onRefreshModels={panel.onRefreshModels}
                onRemove={() => void panel.onUpdateFallbacks(keyItem, fallbackIds.filter((item) => item !== id), removeKey(overrides, id))}
                onPromote={() => void panel.onPromoteFallback(keyItem, id)}
              />
            );
          })}
        </div>

        <div className="akc-chain-foot">
          <button
            type="button"
            className="akc-soft-btn"
            // 首选还在时要两台以上才有「备选」可选；首选已被删除时只要还有一台
            // 就能选——那时候这一台既是备选也是把这个密钥救回来的唯一办法。
            disabled={saving || providers.length === 0 || (!!primaryProviderId && providers.length < 2)}
            onClick={onOpenFallbackModal}
          >
            <Icon path={ICONS.plus} size={13} />
            <span>配置备选 Provider</span>
          </button>
          <span className="akc-chain-foot-note">
            {staleFallbackCount > 0 ? (
              <span className="akc-chain-foot-stale">
                {`有 ${staleFallbackCount} 个备选指向已删除的 Provider，不会参与调度。`}
                <button
                  type="button"
                  className="akc-link"
                  disabled={saving}
                  onClick={() => void panel.onUpdateFallbacks(keyItem, fallbackIds, pruneOverrides(overrides, fallbackIds))}
                >
                  清理掉
                </button>
              </span>
            ) : null}
            {!primaryProviderId ? (
              <span className="akc-chain-foot-stale">
                首选 Provider 已不存在：这个密钥当前没有首选节点，流量会落到下面的备选或直接失败。点「配置备选 Provider」选一台，再用「设为首选」把它提上来。
              </span>
            ) : fallbackIds.length === 0 ? (
              '未配置备选：首选额度耗尽后，这个密钥不会自动换到别的 Provider。'
            ) : (
              '按住备选行拖动即可调整顺序，松手后保存；「设为首选」会交换绑定，其余备选顺序不变。'
            )}
          </span>
        </div>
      </div>
    </div>
  );
}

/** 只保留仍有效备选的模型覆写，避免给已删除的 Provider 留着垃圾键。 */
function pruneOverrides(overrides: Record<string, string>, ids: string[]) {
  const next: Record<string, string> = {};
  for (const id of ids) {
    if (overrides[id]) next[id] = overrides[id];
  }
  return next;
}

function removeKey(source: Record<string, string>, key: string) {
  const next = {...source};
  delete next[key];
  return next;
}


function PipelineRow({
  rank,
  primary,
  provider,
  fallbackProviderId,
  model,
  modelMuted,
  models,
  onPickModel,
  onClearModel,
  serving,
  saving,
  testingProviderID,
  providerModelCount,
  drag,
  onRefreshModels,
  onRemove,
  onPromote,
}: {
  rank: string;
  primary?: boolean;
  provider?: Provider;
  fallbackProviderId: string;
  model: string;
  modelMuted?: boolean;
  /** 该节点自己的模型目录：备选行的模型下拉就从这里取，不用跑去弹窗里配。 */
  models?: Model[];
  onPickModel?: (model: string) => void;
  onClearModel?: () => void;
  serving: boolean;
  saving: boolean;
  testingProviderID: string;
  providerModelCount?: number;
  /** 备选行的拖拽状态与事件；首选行不传。 */
  drag?: RowDragBinding;
  onRefreshModels: (providerId: string, providerName: string) => Promise<void>;
  onRemove: () => void;
  onPromote: () => void;
}) {
  const health = provider?.healthStatus;
  const unhealthy = health === 'unavailable';
  const className = [
    'akc-chain-row',
    primary ? 'primary' : '',
    serving ? ' serving' : '',
    unhealthy ? ' unhealthy' : '',
    drag ? ' draggable' : '',
    drag?.state.draggingId === fallbackProviderId ? ' dragging' : '',
    drag?.state.overId === fallbackProviderId ? (drag.state.after ? ' drop-after' : ' drop-before') : '',
  ].filter(Boolean).join(' ');
  // 指针拖拽：鼠标与触屏同一套事件。按住行内任意位置都可拖，但按钮和文字选择不
  // 抢手势；按下后没有移动 4px 就当作普通点击，不会误触发排序。
  const dragProps = drag ? {
    onPointerDown: (event: React.PointerEvent<HTMLDivElement>) => {
      if (saving || event.button !== 0) return;
      // 点模型下拉是在配置这一行，不是在排序；交给下拉自己处理。
      if ((event.target as HTMLElement).closest('button, input, .searchable-select')) return;
      drag.onPointerDown(event, fallbackProviderId);
    },
  } : {};
  return (
    <div className={className} data-fallback-id={drag ? fallbackProviderId : undefined} {...dragProps}>
      {drag ? (
        <span className="akc-drag-handle" aria-hidden="true" title="按住拖动可调整备选顺序">
          <Icon path={ICONS.grip} size={13} />
        </span>
      ) : (
        <span className="akc-drag-spacer" />
      )}
      <span className={`akc-chain-rank${primary ? ' primary' : ''}`}>{rank}</span>
      <div className="akc-chain-identity">
        {provider ? (
          <ProviderLabel provider={provider} size={15} />
        ) : (
          <span className="akc-chain-missing" title={fallbackProviderId}>Provider 不存在</span>
        )}
      </div>
      <div className="akc-chain-middle">
        {/* 备选行的模型以前只能去「配置备选 Provider」弹窗里改，可它本来就摆在
            这一行上——能看见就该能改，所以直接做成下拉。首选行的模型仍在上面
            「模型替换与推理深度」里改（那是同一条链路的首选位）。 */}
        {provider && onPickModel ? (
          <SearchableModelSelect
            value={modelMuted ? '' : model}
            models={models || []}
            disabled={saving}
            emptyLabel="沿用请求模型"
            onChange={(next) => (next ? onPickModel(next) : onClearModel?.())}
          />
        ) : provider ? (
          <span className={`akc-model-chip${modelMuted || !model ? ' muted' : ''}`} title={modelMuted ? '沿用请求里的模型名' : '该节点锁定的模型'}>
            {model || '未锁定模型'}
          </span>
        ) : null}
        <span className="akc-serving">
          <span className={`akc-serving-dot${serving ? ' on' : ''}${unhealthy ? ' bad' : ''}`} />
          {unhealthy && !serving ? '上游异常' : serving ? '在服务' : '待命'}
        </span>
      </div>
      <div className="akc-chain-actions">
        {provider ? (
          <button
            type="button"
            className="akc-row-icon"
            title="刷新该 Provider 的模型列表"
            disabled={saving || testingProviderID === provider.id}
            onClick={() => void onRefreshModels(provider.id, provider.name)}
          >
            <Icon path={ICONS.refresh} size={13} className={testingProviderID === provider.id ? 'spin' : ''} />
          </button>
        ) : null}
        {!primary ? (
          <button type="button" className="akc-row-text-btn" disabled={saving || !provider} onClick={onPromote} title="把这台上游提为首选">
            设为首选
          </button>
        ) : (
          <span className="akc-row-model-note">{providerModelCount ? `${providerModelCount} 个模型` : ''}</span>
        )}
        {!primary ? (
          <button type="button" className="akc-row-text-btn danger" disabled={saving} title="从备选链移除" onClick={onRemove}>
            移除
          </button>
        ) : null}
      </div>
    </div>
  );
}

/**
 * 备选链的拖拽排序：HTML5 原生拖拽，拖到某一行上半 = 插到它前面，下半 = 插到它后面。
 * 松手才落库（一次 PATCH），失败时由下方回弹到服务端顺序——不在拖拽过程中逐格提交，
 * 否则每划过一行就发一次请求，撤销一次要多点几次。
 */
type RowDragState = {draggingId: string; overId: string; after: boolean};
type RowDragBinding = {
  state: RowDragState;
  onPointerDown: (event: React.PointerEvent<HTMLElement>, id: string) => void;
};

export function moveFallbackId(ids: string[], fromId: string, toId: string, after: boolean) {
  if (fromId === toId) return ids;
  const fromIndex = ids.indexOf(fromId);
  if (fromIndex < 0 || !ids.includes(toId)) return ids;
  const next = ids.filter((id) => id !== fromId);
  const targetIndex = next.indexOf(toId);
  next.splice(after ? targetIndex + 1 : targetIndex, 0, fromId);
  return next.every((id, index) => id === ids[index]) ? ids : next;
}

function SchemeActions({
  keyItem,
  activeProfile,
  saving,
  onCreate,
  onUpdate,
  onDelete,
}: {
  keyItem: APIKey;
  activeProfile?: KeyProfile;
  saving: boolean;
  onCreate: ApiKeyPanelHandlers['onCreateProfile'];
  onUpdate: ApiKeyPanelHandlers['onUpdateProfile'];
  onDelete: ApiKeyPanelHandlers['onDeleteProfile'];
}) {
  const [dialog, setDialog] = React.useState<'create' | 'rename' | null>(null);
  const [name, setName] = React.useState('');
  const inputRef = React.useRef<HTMLInputElement | null>(null);

  React.useEffect(() => {
    if (!dialog) return;
    inputRef.current?.focus();
    inputRef.current?.select();
  }, [dialog]);

  const open = (mode: 'create' | 'rename') => {
    setName(mode === 'rename' ? activeProfile?.name || '' : '');
    setDialog(mode);
  };
  const closeDialog = () => setDialog(null);

  const submit = async () => {
    const trimmed = name.trim();
    if (!trimmed) return;
    try {
      if (dialog === 'create') {
        await onCreate(keyItem, snapshotActiveProfile(keyItem, trimmed), true);
      } else if (dialog === 'rename' && activeProfile) {
        await onUpdate(keyItem, activeProfile.id, {...activeProfile, name: trimmed});
      }
      setDialog(null);
    } catch {
      // 失败时保留输入框供修改重试；提示由数据层给出
    }
  };

  if (dialog) {
    return (
      <div className="akc-inline-dialog" role="dialog" aria-label={dialog === 'create' ? '新建转发方案' : '重命名转发方案'}>
        <input
          ref={inputRef}
          value={name}
          disabled={saving}
          placeholder={dialog === 'create' ? '方案名称…' : '新名称'}
          onChange={(event) => setName(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') { event.preventDefault(); void submit(); }
            if (event.key === 'Escape') { event.stopPropagation(); setDialog(null); }
          }}
        />
        <button type="button" className="akc-inline-confirm" disabled={saving || !name.trim()} onClick={() => void submit()}>
          {saving ? '保存中…' : '确认'}
        </button>
        <button type="button" className="akc-soft-btn" onClick={closeDialog}>取消</button>
      </div>
    );
  }

  return (
    <>
      <button type="button" className="akc-soft-btn" disabled={saving} onClick={() => open('create')}>
        <Icon path={ICONS.plus} size={13} />
        <span>新建方案</span>
      </button>
      {activeProfile ? (
        <button type="button" className="akc-soft-btn" disabled={saving} onClick={() => open('rename')}>
          <Icon path={ICONS.edit} size={13} />
          <span>重命名</span>
        </button>
      ) : null}
      {activeProfile && (keyItem.profiles || []).length > 1 ? (
        <button
          type="button"
          className="akc-soft-btn danger"
          disabled={saving}
          onClick={() => {
            void confirmDialog(`删除方案「${activeProfile.name}」？删除后将切换到其余方案之一。`, {danger: true, confirmLabel: '删除'}).then((ok) => {
              if (ok) void onDelete(keyItem, activeProfile.id);
            });
          }}
        >
          <Icon path={ICONS.trash} size={13} />
          <span>删除当前</span>
        </button>
      ) : null}
    </>
  );
}

// ---- Tab 2：基本绑定与模型覆写 ------------------------------------------------
function ParamsTab({
  keyItem,
  providers,
  models,
  owners,
  saving,
  testingProviderID,
  fixedOutputLabels,
  primaryProviderId,
  primaryProvider,
  bindingOutputProtocol,
  panel,
  publicPreview,
  publicAvailable,
  onPublicPreviewChange,
}: {
  keyItem: APIKey;
  providers: Provider[];
  models: Model[];
  owners?: ConsoleUser[];
  saving: boolean;
  testingProviderID: string;
  fixedOutputLabels: string[];
  primaryProviderId: string;
  primaryProvider?: Provider;
  bindingOutputProtocol: Protocol;
  panel: ApiKeyPanelHandlers & Pick<React.ComponentProps<typeof ApiKeyDetailPanel>, 'onUpdateOwner'>;
  publicPreview: boolean;
  publicAvailable: boolean;
  onPublicPreviewChange: (value: boolean) => void;
}) {
  const providerModels = primaryProvider ? models.filter((model) => model.providerId === primaryProvider.id) : [];
  const [aliasModalOpen, setAliasModalOpen] = React.useState(false);
  const aliasCount = Object.keys(keyItem.modelAliases || {}).length;

  return (
    <div className="akc-grid-2">
      <div className="akc-card">
        <div className="akc-card-head">
          <span className="akc-card-title">
            <Icon path={ICONS.sliders} size={13} className="accent" />
            基本绑定参数
          </span>
        </div>
        <div className="akc-form">
          <ApiKeyNameField
            name={keyItem.name}
            disabled={saving}
            onSave={(name) => panel.onUpdateField(keyItem, 'name', name)}
          />
          <IconSelect
            label="输入 Provider（首选）"
            value={primaryProviderId}
            disabled={saving}
            options={providerSelectOptions(providers)}
            onChange={(providerId) => void panel.onUpdateBinding(keyItem, providerId, bindingOutputProtocol)}
          />
          {owners ? (
            <div className="akc-field">
              <label>所属用户</label>
              <select
                value={keyItem.ownerUserId || ''}
                disabled={saving}
                onChange={(event) => void panel.onUpdateOwner?.(keyItem, event.target.value)}
              >
                <option value="">内置 admin（无归属）</option>
                {owners.map((user) => <option key={user.id} value={user.id}>{user.username}</option>)}
              </select>
            </div>
          ) : null}
          {/* 「输出协议」已经删掉：一把 Key 本来就能同时服务 Claude Code、Codex
              和任意 OpenAI SDK，每条端点自带协议，网关按本次请求实际走的那条
              端点决定直通还是转换。留一个下拉只会让人以为选它就限制了 —— 以前
              确实限制（另一条端点会被拒），现在不会了。
              下面的「协议接入一览」列出三条可用地址，用哪条由客户端自己决定。 */}
          <div className="akc-field">
            <div className="akc-label-row">
              <label>公网 Base URL 预览</label>
              <InfoTip text="打开后「复制 URL」与客户端配置使用公网隧道域名；关闭则用内网地址。仅影响复制出来的配置，不影响转发。" />
            </div>
            <Switch
              checked={publicPreview && publicAvailable}
              disabled={!publicAvailable}
              label={publicAvailable ? (publicPreview ? '使用公网域名' : '使用内网地址') : '没有公网地址，只能使用内网地址'}
              onChange={onPublicPreviewChange}
            />
          </div>
        </div>
      </div>

      <div className="akc-card">
        <div className="akc-card-head">
          <span className="akc-card-title">
            <Icon path={ICONS.sliders} size={13} className="purple" />
            模型替换与推理深度
          </span>
        </div>
        <div className="akc-form">
          {/* 「固定模型替换」已经挪到「转发方案与容灾链路」——首选和备选各有自己
              的模型覆盖，放在那边才能一眼看见整条链路用的是哪个模型。这里只留
              刷新按钮：模型清单是链路配模型的前提，放在哪一页都要够得着。 */}
          <div className="akc-field">
            <div className="akc-label-row">
              <label>模型清单</label>
              <InfoTip text="模型替换在「转发方案与容灾链路」里按节点配置：首选和每个备选各有自己的模型。这里只负责把这个 Provider 的模型列表拉新。" />
            </div>
            <div className="akc-field-inline">
              <span className="akc-muted-inline">
                {providerModels.length > 0 ? `${providerModels.length} 个模型` : '还没有模型清单'}
              </span>
              <button
                type="button"
                className="akc-soft-btn"
                disabled={saving || !primaryProvider || testingProviderID === primaryProvider.id}
                onClick={() => primaryProvider ? void panel.onRefreshModels(primaryProvider.id, primaryProvider.name) : undefined}
              >
                <Icon path={ICONS.refresh} size={13} className={primaryProvider && testingProviderID === primaryProvider.id ? 'spin' : ''} />
                <span>{primaryProvider && testingProviderID === primaryProvider.id ? '刷新中…' : '刷新模型'}</span>
              </button>
            </div>
          </div>

          <div className="akc-form-row">
            <div className="akc-field">
              <div className="akc-label-row">
                <label>思考深度</label>
                {/* 这是 Key 级设置：不论这一轮落到首选还是哪个备选、走哪个模型
                    都按它发。放在「基本绑定」里最容易被误读成只对首选生效，
                    所以把范围写在问号里。 */}
                <InfoTip text="这个 Key 的全局思考深度：无论请求最终落在首选、哪个备选，还是哪个模型，都按这里发。留空表示尊重客户端自己带的值（例如 reasoning_effort / thinking）。" />
              </div>
              <select
                value={keyItem.thinkingDepthOverride || ''}
                disabled={saving}
                onChange={(event) => void panel.onUpdateField(keyItem, 'thinkingDepthOverride', event.target.value)}
              >
                {thinkingDepthSelectOptions({value: '', label: '（不覆盖）'})}
              </select>
            </div>
            <MaxOutputTokensField
              value={keyItem.maxOutputTokens && keyItem.maxOutputTokens > 0 ? keyItem.maxOutputTokens : 0}
              disabled={saving}
              onSave={(value) => panel.onUpdateField(keyItem, 'maxOutputTokens', value)}
            />
          </div>

          <div className="akc-form-row">
            <FirstPacketTimeoutField
              value={keyItem.firstPacketTimeoutSec && keyItem.firstPacketTimeoutSec > 0 ? keyItem.firstPacketTimeoutSec : 0}
              disabled={saving}
              onSave={(value) => panel.onUpdateField(keyItem, 'firstPacketTimeoutSec', value)}
            />
            <SameProviderRetriesField
              value={keyItem.sameProviderRetries && keyItem.sameProviderRetries > 0 ? keyItem.sameProviderRetries : 0}
              disabled={saving}
              onSave={(value) => panel.onUpdateField(keyItem, 'sameProviderRetries', value)}
            />
          </div>

          <div className="akc-form-row">
            <div className="akc-field">
              <div className="akc-label-row">
                <label>流式响应（SSE）</label>
                <InfoTip text="关闭后该 Key 的 stream:true 请求将被拒绝。" />
              </div>
              <Switch
                checked={keyItem.streamEnabled !== false}
                label="允许 stream:true"
                disabled={saving}
                onChange={(value) => void panel.onUpdateField(keyItem, 'streamEnabled', value)}
              />
            </div>
            <div className="akc-field">
              <div className="akc-label-row">
                <label>Codex Fast 档（强制）</label>
                <InfoTip text="开启后，该 Key 的请求只要落到 ChatGPT OAuth（Codex）Provider，就强制以 fast 档转发（service_tier=priority，速度约 1.5~2.5x，额度消耗 2~2.5x）。其他类型 Provider 不受影响。" />
              </div>
              <Switch
                checked={keyItem.codexForceFast === true}
                label="强制 fast 档"
                disabled={saving}
                onChange={(value) => void panel.onUpdateField(keyItem, 'codexForceFast', value)}
              />
            </div>
          </div>

          <div className="akc-field">
            <div className="akc-label-row">
              <label>模型映射</label>
              <InfoTip text="把客户端请求的 model 名改写成上游真实模型名（按 Key 生效）。" />
              <span className="akc-muted-inline">{aliasCount > 0 ? `已配置 ${aliasCount} 个别名` : '未配置别名'}</span>
            </div>
            <div className="akc-field-inline">
              <button type="button" className="akc-soft-btn" disabled={saving || !primaryProvider} onClick={() => setAliasModalOpen(true)}>
                <Icon path={ICONS.edit} size={13} />
                <span>配置映射</span>
              </button>
            </div>
          </div>
        </div>
      </div>

      <ApiKeyModelMappingModal
        open={aliasModalOpen}
        providerName={primaryProvider?.name || 'Provider'}
        models={providerModels}
        aliases={keyItem.modelAliases || {}}
        saving={saving}
        onClose={() => setAliasModalOpen(false)}
        onSave={(aliases) => panel.onUpdateModelAliases(keyItem, aliases)}
      />
    </div>
  );
}

function MaxOutputTokensField({value, disabled, onSave}: {value: number; disabled: boolean; onSave: (value: number) => Promise<void>}) {
  const [draft, setDraft] = React.useState(value > 0 ? String(value) : '');
  React.useEffect(() => { setDraft(value > 0 ? String(value) : ''); }, [value]);
  const commit = () => {
    const raw = draft.trim();
    const parsed = raw === '' ? 0 : Number.parseInt(raw, 10);
    const next = Number.isFinite(parsed) && parsed > 0 ? Math.min(parsed, 200000) : 0;
    setDraft(next > 0 ? String(next) : '');
    if (next === value) return;
    void onSave(next);
  };
  return (
    <div className="akc-field">
      <div className="akc-label-row">
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
        onBlur={commit}
        onKeyDown={(event) => { if (event.key === 'Enter') event.currentTarget.blur(); }}
      />
    </div>
  );
}

/**
 * 这个 Key 等上游「响应头」的上限。
 *
 * 只覆盖「请求已发出、上游还没开口」这一段：一旦响应头到了，长回答、慢 stream
 * 都不受影响。默认 0 表示按各 Provider 自己的设置走（再没有就是网关的 600s
 * 响应头等待）——同一个 Provider 可能既跑秒回的聊天又跑先想两分钟的模型，所以这
 * 个值放在 Key 上才答得了「我这把钥匙要等多久」。
 */
function FirstPacketTimeoutField({value, disabled, onSave}: {value: number; disabled: boolean; onSave: (value: number) => Promise<void>}) {
  const [draft, setDraft] = React.useState(value > 0 ? String(value) : '');
  React.useEffect(() => { setDraft(value > 0 ? String(value) : ''); }, [value]);
  const commit = () => {
    const raw = draft.trim();
    const parsed = raw === '' ? 0 : Number.parseInt(raw, 10);
    const next = Number.isFinite(parsed) && parsed > 0 ? Math.min(parsed, 600) : 0;
    setDraft(next > 0 ? String(next) : '');
    if (next === value) return;
    void onSave(next);
  };
  return (
    <div className="akc-field">
      <div className="akc-label-row">
        <label>等待响应头（秒）</label>
        <InfoTip text="从请求发出到上游返回响应头的上限。只影响「还没开口」这一段，一旦开始返回就不会被打断。留空或 0 = 按 Provider 的设置走（网关默认 600 秒）。" />
      </div>
      <input
        type="number"
        min={0}
        max={600}
        placeholder="0 = 按 Provider 设置"
        disabled={disabled}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={commit}
        onKeyDown={(event) => { if (event.key === 'Enter') event.currentTarget.blur(); }}
      />
    </div>
  );
}

/**
 * 同一个 Provider 上最多再试几次。
 *
 * 传输错误和 5xx/429 多半是瞬时抖动，换账号解决不了问题，反而把整条链路白推
 * 一格——「第一个明明还能用，偶尔出错就切走」就是这么来的。设成 1~3 之后先在
 * 原地重试，真不行才换节点。0 = 一失败就换（历史行为）。
 */
function SameProviderRetriesField({value, disabled, onSave}: {value: number; disabled: boolean; onSave: (value: number) => Promise<void>}) {
  const [draft, setDraft] = React.useState(value > 0 ? String(value) : '');
  React.useEffect(() => { setDraft(value > 0 ? String(value) : ''); }, [value]);
  const commit = () => {
    const raw = draft.trim();
    const parsed = raw === '' ? 0 : Number.parseInt(raw, 10);
    const next = Number.isFinite(parsed) && parsed > 0 ? Math.min(parsed, 3) : 0;
    setDraft(next > 0 ? String(next) : '');
    if (next === value) return;
    void onSave(next);
  };
  return (
    <div className="akc-field">
      <div className="akc-label-row">
        <label>同节点重试次数</label>
        <InfoTip text="同一个 Provider 失败后先原地重试几次再换下一个节点。只对可重试的失败生效（连接中断、5xx、429）；参数错误、认证失败不重试——重试多少次结果都一样。0 = 一失败就切走（默认）。" />
      </div>
      <input
        type="number"
        min={0}
        max={3}
        placeholder="0 = 失败即切"
        disabled={disabled}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={commit}
        onKeyDown={(event) => { if (event.key === 'Enter') event.currentTarget.blur(); }}
      />
    </div>
  );
}

function ApiKeyNameField({name, disabled, onSave}: {name: string; disabled: boolean; onSave: (name: string) => Promise<void>}) {  const [draft, setDraft] = React.useState(name);
  React.useEffect(() => { setDraft(name); }, [name]);
  return (
    <div className="akc-field">
      <label>名称</label>
      <input
        value={draft}
        disabled={disabled}
        placeholder="例如 key1"
        onChange={(event) => setDraft(event.target.value)}
        onBlur={() => {
          const trimmed = draft.trim();
          if (!trimmed || trimmed === name) { setDraft(name); return; }
          void onSave(trimmed);
        }}
        onKeyDown={(event) => { if (event.key === 'Enter') event.currentTarget.blur(); }}
      />
    </div>
  );
}

// ---- Tab 3：客户端配置与调用示例 ----------------------------------------------
function ClientTab({
  keyItem,
  route,
  primary,
  endpoints,
  tunnelRunning,
  livePublicURL,
  clientURL,
  publicPreview,
  publicAvailable,
  onPublicPreviewChange,
  onCopy,
  copied,
  onTest,
  onUpdateField,
}: {
  keyItem: APIKey;
  route?: Route;
  primary?: Provider;
  endpoints: OutputEndpoint[];
  tunnelRunning: boolean;
  livePublicURL: string;
  clientURL: string;
  publicPreview: boolean;
  publicAvailable: boolean;
  onPublicPreviewChange: (value: boolean) => void;
  onCopy: (text: string, message: string, token?: string) => void;
  copied: string;
  onTest?: (key: APIKey, prompt: string) => Promise<RouteTestResult | null>;
  onUpdateField: ApiKeyPanelHandlers['onUpdateField'];
}) {
  const [snippet, setSnippet] = React.useState<'claude' | 'openai' | 'codex'>('claude');
  const [applyingClients, setApplyingClients] = React.useState(false);
  const gatewayURL = apiKeyGatewayRoot(endpoints, publicPreview && publicAvailable ? livePublicURL : '').replace(/\/$/, '');
  const publicBase = publicPreview && publicAvailable ? livePublicURL : '';
  // 「一键写入本机」写的是跑网关这台机器上的配置：CLI 与桌面端共用同一个地址。
  // 本机走 127.0.0.1，选了公网 https 域名才用公网。以前 CLI 那半写的是网卡
  // IP，换网络、换网卡、拔掉 Wi-Fi 都会失效，而 127.0.0.1 永远通。
  // （Desktop 的 3p 配置还额外要求 https 或回环 http，这条规则同时满足它。）
  const localAgentURL = localAgentGatewayRoot(endpoints, publicBase);

  // 直接调后端把本机配置写好：CLI 与桌面端读的是两套文件，后端一次都写。
  const applyLocally = async () => {
    if (!route) return;
    setApplyingClients(true);
    try {
      const outcome = await applyClaudeClientSetup({
        baseURL: `${localAgentURL}/anthropic`,
        gatewayURL: localAgentURL,
        token: keyItem.key,
        model: 'default[1m]',
      });
      if (!outcome.ok) {
        void alertDialog(outcome.error || '写入失败');
        return;
      }
      // 没有文本可复制，这里要的是"写入成功"的反馈：copy() 对空文本只弹
      // 提示、不碰剪贴板（早期它直接 return，导致写入成功后毫无反应）。
      onCopy('', '已写入本机 Claude Code CLI 与 Claude Desktop 配置', 'claude-local');
      const notes = (outcome.result?.notes || []).join('\n');
      void alertDialog(`已写入本机配置：\n${outcome.result?.cliPath || ''}\n${outcome.result?.desktopPath || ''}${notes ? `\n\n${notes}` : ''}`);
    } catch (error) {
      void alertDialog(String(error));
    } finally {
      setApplyingClients(false);
    }
  };

  // Codex 的一键写入：CLI 与 Codex Desktop 读的是同一份 ~/.codex/config.toml，
  // 所以写一次两端都生效。config.toml 里只替换本工具管理的那一段，其余配置不动；
  // 模型目录（model_catalog_json）也一起写，否则 Codex 会报 "Model metadata
  // not found"，见 buildApiKeyCodexConfig 的注释。
  //
  // auth.json：Codex 判定"已登录"看的是它，不是 config.toml。不写就会在启动时
  // 弹登录页——哪怕 provider 已经指向自带凭据的网关。默认写；用户本机已有
  // ChatGPT 登录时可以用下面的开关保住它。
  const applyCodexLocally = async () => {
    if (!route) return;
    setApplyingClients(true);
    try {
      // Codex 的 base_url 是网关的 /openai/v1；本机地址与公网地址的取舍和
      // Claude 那条一样：本机走回环，选了公网域名才用公网。
      const outcome = await applyCodexClientSetup({
        baseURL: `${localAgentURL}/openai/v1`,
        token: keyItem.key,
        providerID: sanitizeClientConfigID(keyItem.name),
        providerName: keyItem.name || 'LLM Gateway',
        model: 'default[1m]',
        reasoningEffort: resolveCodexReasoningEffort(keyItem),
        catalogPath: codexCatalogPathForHome(),
        catalog: buildApiKeyCodexModelCatalogJSON(keyItem, primary),
        keepOfficialLogin: keyItem.codexKeepOfficialLogin === true,
      });
      if (!outcome.ok) {
        void alertDialog(outcome.error || '写入失败');
        return;
      }
      onCopy('', '已写入本机 Codex CLI 与 Desktop 配置', 'codex-local');
      const notes = (outcome.result?.notes || []).join('\n');
      const authLine = outcome.result?.authPath ? `\n${outcome.result.authPath}` : '';
      void alertDialog(`已写入本机 Codex 配置（CLI 与 Desktop 共用）：\n${outcome.result?.configPath || ''}${authLine}${outcome.result?.catalogPath ? `\n${outcome.result.catalogPath}` : ''}${notes ? `\n\n${notes}` : ''}`);
    } catch (error) {
      void alertDialog(String(error));
    } finally {
      setApplyingClients(false);
    }
  };

  const snippets = {
    claude: {
      label: 'Claude Code',
      base: `${gatewayURL}/anthropic`,
      auth: 'x-api-key',
      code: `export ANTHROPIC_BASE_URL=${shellArg(`${gatewayURL}/anthropic`)}\nexport ANTHROPIC_AUTH_TOKEN=${shellArg(keyItem.key)}`,
    },
    openai: {
      label: 'OpenAI SDK',
      base: `${gatewayURL}/v1`,
      auth: 'Authorization: Bearer',
      code: `curl ${shellArg(`${gatewayURL}/v1/chat/completions`)} \\\n  -H ${shellArg(`Authorization: Bearer ${keyItem.key}`)} \\\n  -H 'Content-Type: application/json' \\\n  -d '{"model":${shellArg(keyItem.modelOverride || primary?.defaultModel || 'auto')},"messages":[{"role":"user","content":"你好"}]}'`,
    },
    codex: {
      label: 'Codex',
      base: `${gatewayURL}/openai/v1`,
      auth: 'Authorization: Bearer',
      code: `export OPENAI_BASE_URL=${shellArg(`${gatewayURL}/openai/v1`)}\nexport OPENAI_API_KEY=${shellArg(keyItem.key)}`,
    },
  } as const;
  const active = snippets[snippet];

  return (
    <div className="akc-stack">
      <div className="akc-bar">
        <div className="akc-bar-main">
          <span className="akc-bar-label">网关地址：</span>
          <div className="akc-mini-segmented" role="group" aria-label="网关地址类型">
            <button type="button" className={!publicPreview ? 'active' : ''} onClick={() => onPublicPreviewChange(false)}>内网</button>
            <button
              type="button"
              className={publicPreview ? 'active' : ''}
              disabled={!publicAvailable}
              title={publicAvailable ? livePublicURL : '未配置公网域名 / 隧道'}
              onClick={() => onPublicPreviewChange(true)}
            >
              公网域名
            </button>
          </div>
          <code className="akc-inline-code" title={clientURL}>{clientURL || gatewayURL}</code>
        </div>
        <span className="akc-bar-hint">
          {livePublicURL ? `公网地址${tunnelRunning ? '（隧道）' : ''} · ${livePublicURL}` : '未配置公网地址，仅内网可用'}
        </span>
      </div>

      <div className="akc-grid-2">
        <div className="akc-card">
          <div className="akc-card-head">
            <span className="akc-card-title">
              <Icon path={ICONS.code} size={13} className="accent" />
              一键复制客户端配置
            </span>
            <span className="akc-card-note">按当前输出协议匹配可用的客户端</span>
          </div>
          <div className="akc-client-buttons">
            {clientConfigsForProtocol(route?.outputProtocol).map((client) => (
              <button
                key={client}
                type="button"
                className={`akc-client-btn${copied === client ? ' copied' : ''}`}
                disabled={!route}
                onClick={() => onCopy(
                  buildClientConfigText(client, keyItem, route!, gatewayURL, primary),
                  `已复制 ${clientConfigButtonLabel(client)} 配置`,
                  client,
                )}
              >
                <BrandIcon icon={client === 'codex' ? 'codex-color' : client === 'claude' ? 'claude-color' : 'opencode'} name={client} size={15} />
                <span>{copied === client ? '已复制' : `复制 ${clientConfigButtonLabel(client)} 配置`}</span>
              </button>
            ))}
            {/* 第四种按钮不走 clientConfigsForProtocol：它是「一次配置、平台换模型」的
                固定入口，模型名恒为 default[1m]，因此不依赖当前 Provider 选了哪个
                模型，也不受输出协议变化影响（协议在平台上换，客户端不用重配）。 */}
            <button
              type="button"
              className={`akc-client-btn${copied === 'claude-switcher' ? ' copied' : ''}`}
              disabled={!route}
              title="把内置模型名固定为 default[1m]，模型与 Provider 之后全在平台上切"
              onClick={() => onCopy(
                buildClaudeCodeSwitcherScript(keyItem, endpoints, publicBase),
                '已复制 Claude Code 一键接入脚本（模型固定为 default[1m]，粘贴到终端执行）',
                'claude-switcher',
              )}
            >
              <BrandIcon icon="claude-color" name="claude" size={15} />
              <span>{copied === 'claude-switcher' ? '已复制' : '复制 Claude Code 一键接入'}</span>
            </button>
            {/* 桌面 App 里的会话不读 ~/.claude/settings.json，走自己的 3p 配置。
                这条按钮直接在「运行网关的这台机器」上把两端都写好，省掉粘贴终端
                这一步；后端只接受本机请求，公网管理员点了会被拒。 */}
            <button
              type="button"
              className={`akc-client-btn${copied === 'claude-local' ? ' copied' : ''}`}
              disabled={!route || applyingClients}
              title="在本机直接写入 Claude Code CLI 与桌面端的配置（模型固定为 default[1m]）；走 /anthropic 端点，与 Codex 的配置互不影响"
              onClick={() => void applyLocally()}
            >
              <BrandIcon icon="claude-color" name="claude" size={15} />
              <span>{copied === 'claude-local' ? '已写入本机' : applyingClients ? '写入中…' : '一键写入本机（CLI + 桌面端）'}</span>
            </button>
            {/* Codex：CLI 与 Codex Desktop 读的是同一份 ~/.codex 目录（config.toml
                + auth.json），所以写一次两端都配好。需要输出协议为 Responses。 */}
            <button
              type="button"
              className={`akc-client-btn${copied === 'codex-local' ? ' copied' : ''}`}
              disabled={!route || applyingClients}
              title="在本机写入 ~/.codex（config.toml + auth.json + 模型目录，模型固定为 default[1m]），Codex CLI 与 Desktop 同时生效；走 /openai/v1 端点，与 Claude 的配置互不影响"
              onClick={() => void applyCodexLocally()}
            >
              <BrandIcon icon="codex-color" name="codex" size={15} />
              <span>{copied === 'codex-local' ? '已写入本机' : applyingClients ? '写入中…' : '一键写入本机（Codex CLI + Desktop）'}</span>
            </button>
          </div>
          <div className="akc-hint">
            {route ? '三条端点（OpenAI Chat / OpenAI Responses / Claude）均可调用；网关按请求进来的端点自动直通或转换，不再由输出协议钉死。' : '请先在「基本绑定」里为该密钥选择 Provider。'}
          </div>
          <ol className="akc-steps">
            <li><b>先选网关地址</b>：本机或同一局域网用「内网」，外部网络用「公网域名」。</li>
            <li><b>点上面的复制按钮</b>：会按选中地址生成本工具托管的配置片段（含 Base URL 与密钥）。</li>
            <li><b>粘贴到客户端配置</b>：OpenCode → <code>opencode.json</code>，Codex → <code>config.toml</code>，Claude → 环境变量。</li>
            <li><b>发一条消息验证</b>：在「在线调试沙箱」里点发送，能看到状态、时延和协议转换结果。</li>
          </ol>
          <div className="akc-hint">
            <b>Claude Code 一键接入</b>：默认模型名固定成 <code>default[1m]</code>（客户端按 1M 上下文计算；
            需要 200K 档时可改用 <code>default</code>），并把 opus / sonnet / haiku / fable 四个内置档位与子 agent
            一起钉到保留别名。
            之后换 Provider、换模型、换上游协议都在平台完成，客户端不用再改配置，也不用像改配置工具那样重启。
            脚本会先备份、只改 <code>env</code> 里这几个键，其余配置原样保留；环境变量只在启动时读取，
            所以第一次执行后重开一次客户端。
          </div>
        </div>

        <div className="akc-card">
          <div className="akc-card-head">
            <span className="akc-card-title">
              <Icon path={ICONS.code} size={13} className="purple" />
              协议接入一览
            </span>
          </div>
          <div className="akc-mini-segmented">
            {(Object.keys(snippets) as Array<keyof typeof snippets>).map((id) => (
              <button key={id} type="button" className={snippet === id ? 'active' : ''} onClick={() => setSnippet(id)}>
                {snippets[id].label}
              </button>
            ))}
          </div>
          <div className="akc-kv">
            <span className="akc-kv-key">Base URL</span>
            <code className="akc-kv-value" title={active.base}>{active.base}</code>
            <button type="button" className="akc-row-text-btn" onClick={() => onCopy(active.base, '已复制 Base URL')}>复制</button>
          </div>
          <div className="akc-kv">
            <span className="akc-kv-key">鉴权头</span>
            <code className="akc-kv-value">{active.auth}</code>
            <button type="button" className="akc-row-text-btn" onClick={() => onCopy(`${active.auth}: ${keyItem.key}`, '已复制鉴权头')}>复制</button>
          </div>
          <div className="akc-kv">
            <span className="akc-kv-key">密钥</span>
            <code className="akc-kv-value mono" title={keyItem.key}>{maskKey(keyItem.key)}</code>
            <button type="button" className="akc-row-text-btn" onClick={() => onCopy(keyItem.key, '已复制 API Key')}>复制</button>
          </div>
          <div className="akc-code-block">
            <button type="button" className="akc-row-text-btn akc-code-copy" onClick={() => onCopy(active.code, `已复制 ${active.label} 配置`)}>
              <Icon path={ICONS.copy} size={12} /> 复制
            </button>
            <pre>{active.code}</pre>
          </div>
          <ul className="akc-notes">
            <li>密钥通过 <code>{active.auth}</code> 发送，内网、公网同一把。</li>
            <li>请求里的模型名默认按原样转发；要固定模型请到「基本绑定与模型覆写」设置。</li>
            <li>客户端用哪条端点由客户端自己决定；网关只按实际入口做直通或转换，三条端点共用同一把 Key。</li>
          </ul>
        </div>
      </div>

      {route ? (
        <div className="akc-card">
          <div className="akc-card-head">
            <span className="akc-card-title">
              <Icon path={ICONS.code} size={13} />
              {tunnelRunning ? '公网调用示例' : '局域网调用示例'}（cURL）
            </span>
            <button
              type="button"
              className="akc-soft-btn"
              onClick={() => onCopy(routeTestCurl(route, endpoints, keyItem, publicBase, primary), '已复制 curl')}
            >
              <Icon path={ICONS.copy} size={13} />
              <span>复制 curl</span>
            </button>
          </div>
          <div className="akc-code-block plain"><pre>{routeTestCurl(route, endpoints, keyItem, publicBase, primary)}</pre></div>
        </div>
      ) : null}

      {onTest && route ? <SandboxCard keyItem={keyItem} onTest={onTest} onUpdateField={onUpdateField} /> : null}
    </div>
  );
}

function clientConfigButtonLabel(client: 'opencode' | 'codex' | 'claude') {
  return client === 'opencode' ? 'OpenCode' : client === 'codex' ? 'Codex' : 'Claude';
}

/** POSIX 单引号转义：密钥里理论上不会有单引号，但拼命令行时不能假设。 */
function shellArg(value: string) {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

export function routeTestCurl(route: Route, endpoints: OutputEndpoint[], keyItem: APIKey, publicBase: string, provider?: Provider) {
  const endpoint = endpoints.find((item) => item.protocol === route.outputProtocol);
  const root = `${apiKeyGatewayRoot(endpoints, publicBase)}${endpoint?.basePath || ''}`;
  const model = keyItem.modelOverride?.trim() || provider?.defaultModel?.trim() || 'your-model';
  const url = route.outputProtocol === 'claude' ? `${root}/v1/messages`
    : route.outputProtocol === 'openai_responses' ? `${root}/responses`
    : route.outputProtocol === 'gemini' ? `${root}/models/${model}:generateContent`
    : `${root}/chat/completions`;
  const payload: Record<string, unknown> = route.outputProtocol === 'claude'
    ? {model, max_tokens: 1024, messages: [{role: 'user', content: '你好'}]}
    : route.outputProtocol === 'gemini'
      ? {contents: [{parts: [{text: '你好'}]}]}
      : {model, messages: [{role: 'user', content: '你好'}]};
  const auth = route.outputProtocol === 'claude'
    ? `  -H ${shellArg(`x-api-key: ${keyItem.key}`)} \\\n`
    : route.outputProtocol === 'gemini'
      ? `  -H ${shellArg(`x-goog-api-key: ${keyItem.key}`)} \\\n`
      : `  -H ${shellArg(`Authorization: Bearer ${keyItem.key}`)} \\\n`;
  return `curl -s ${shellArg(url)} \\\n  -H 'Content-Type: application/json' \\\n${auth}  -d '${JSON.stringify(payload).replace(/'/g, `'\\''`)}'`;
}

/**
 * 极简客户端配置片段（贴到对应配置文件里即可）。
 * 完整可执行的安装脚本（含备份与增量合并）仍在「客户端配置」弹窗里生成，
 * 两边职责不重叠：这里给的是「看一眼就知道该填什么」。
 */
export function buildClientConfigText(
  client: 'opencode' | 'codex' | 'claude',
  keyItem: APIKey,
  route: Route,
  gatewayURL: string,
  provider?: Provider,
) {
  const base = `${gatewayURL}${route.outputProtocol === 'claude' ? '/anthropic' : route.outputProtocol === 'openai_responses' ? '/openai/v1' : '/v1'}`;
  const realModel = keyItem.modelOverride?.trim() || provider?.defaultModel?.trim() || 'auto';
  // Codex 与 Claude Code 都使用网关的保留别名，默认选择 1M 窗口；真实模型由
  // API Key 的模型覆写或 Provider 默认模型决定。OpenCode 仍需要具体模型名。
  const model = client === 'opencode' ? realModel : 'default[1m]';
  if (client === 'claude') {
    return JSON.stringify({env: {ANTHROPIC_BASE_URL: base, ANTHROPIC_AUTH_TOKEN: keyItem.key, ANTHROPIC_MODEL: model}}, null, 2);
  }
  if (client === 'codex') {
    return JSON.stringify({
      model,
      model_provider: 'llm-gateway',
      model_providers: {'llm-gateway': {name: 'LLM Gateway', base_url: base, env_key: 'OPENAI_API_KEY', api_key: keyItem.key}},
    }, null, 2);
  }
  return JSON.stringify({
    provider: {'llm-gateway': {npm: '@ai-sdk/openai-compatible', options: {baseURL: base, apiKey: keyItem.key}, models: {[model]: {}}}},
    model: `llm-gateway/${model}`,
  }, null, 2);
}

// 在线调试沙箱：真的用这把 Key 打一次网关，答案就是「这条链现在通不通」。
function SandboxCard({keyItem, onTest, onUpdateField}: {
  keyItem: APIKey;
  onTest: (key: APIKey, prompt: string) => Promise<RouteTestResult | null>;
  onUpdateField: ApiKeyPanelHandlers['onUpdateField'];
}) {
  const [prompt, setPrompt] = React.useState('用一句话解释什么是量子纠缠。');
  const [result, setResult] = React.useState<RouteTestResult | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [serverError, setServerError] = React.useState('');

  const run = async () => {
    setBusy(true);
    setServerError('');
    setResult(null);
    try {
      const response = await onTest(keyItem, prompt);
      if (response) setResult(response);
      else setServerError('网关没有返回结果');
    } catch (error) {
      setServerError(String(error));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="akc-card">
      <div className="akc-card-head">
        <span className="akc-card-title">
          <Icon path={ICONS.terminal} size={13} className="purple" />
          在线调试沙箱
        </span>
        <span className="akc-card-note">用本密钥直连网关，真实走一遍协议转换与故障转移</span>
      </div>
      <div className="akc-sandbox-input">
        <input
          value={prompt}
          placeholder="输入测试提示词…"
          disabled={busy}
          onChange={(event) => setPrompt(event.target.value)}
          onKeyDown={(event) => { if (event.key === 'Enter' && !busy) { event.preventDefault(); void run(); } }}
        />
        <button type="button" className="akc-cta" disabled={busy || !prompt.trim()} onClick={() => void run()}>
          <Icon path={ICONS.play} size={12} />
          <span>{busy ? '透传中…' : '发送测试'}</span>
        </button>
      </div>
      {result ? (
        <>
          <div className="akc-stats-4">
            <div>
              <span>状态</span>
              <b className={result.success ? 'ok' : 'bad'}>{result.status || (result.success ? '200' : '失败')}</b>
            </div>
            <div>
              <span>时延</span>
              <b>{result.latencyMs != null ? `${result.latencyMs}ms` : '—'}</b>
            </div>
            <div>
              <span>协议转换</span>
              <b className="accent">{result.protocolFlow || '—'}</b>
            </div>
            <div>
              <span>模型</span>
              <b>{result.model || '—'}</b>
            </div>
          </div>
          <div className={`akc-code-block plain${result.success ? '' : ' error'}`}>
            <pre>{result.preview || result.error || '（无内容）'}</pre>
          </div>
        </>
      ) : null}
      {serverError ? <div className="akc-hint error">{serverError}</div> : null}
      <div className="akc-sandbox-foot">
        <span>{keyItem.enabled ? '该密钥已启用，测试会走真实调度' : '该密钥已停用，测试可能被网关拒绝'}</span>
        <Switch checked={keyItem.enabled} label={keyItem.enabled ? '启用' : '停用'} onChange={(value) => void onUpdateField(keyItem, 'enabled', value)} />
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 额度页补充读数：今日 / 本月请求数，与用量页同源（/__request-stats）。
// ---------------------------------------------------------------------------
function QuotaUsageCard({keyItem}: {keyItem: APIKey}) {
  const [stats, setStats] = React.useState<ApiKeyUsageSource | null>(null);
  const [error, setError] = React.useState('');
  React.useEffect(() => {
    let cancelled = false;
    void fetch(`${API_BASE}/__request-stats`, {credentials: 'same-origin'})
      .then((response) => (response.ok ? response.json() : Promise.reject(new Error(`HTTP ${response.status}`))))
      .then((data: ApiKeyUsageSource) => { if (!cancelled) { setStats(data); setError(''); } })
      .catch((reason: unknown) => { if (!cancelled) setError(String(reason)); });
    return () => { cancelled = true; };
  }, [keyItem.id]);
  const usage = buildApiKeyUsageMap(stats)[keyItem.id] || ZERO_USAGE;
  return (
    <div className="akc-card">
      <div className="akc-card-head">
        <span className="akc-card-title">
          <Icon path={ICONS.shield} size={13} />
          窗口内已用请求数
        </span>
        <span className="akc-card-note">只统计已完成请求，与用量页同口径</span>
      </div>
      <div className="akc-stats-2">
        <div>
          <span>今日</span>
          <b>{usage.todayRequests.toLocaleString()}</b>
        </div>
        <div>
          <span>本月</span>
          <b>{usage.monthRequests.toLocaleString()}</b>
        </div>
      </div>
      {error ? <div className="akc-hint error">用量读取失败：{error}</div> : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// 全量数据表
// ---------------------------------------------------------------------------
export function ApiKeyFullTable({
  keys,
  totalKeys,
  checkedIds,
  selectedKeyId,
  usage,
  routes,
  providers,
  ownerLabel,
  saving,
  onToggleCheck,
  onSelectAll,
  onClearSelection,
  onSelect,
  onOpenDrawer,
  onCopyKey,
  copiedKeyId,
  onDelete,
  onToggleEnabled,
  onBatchDelete,
  filters,
  providersForFilters,
  pagination,
}: {
  keys: APIKey[];
  totalKeys: number;
  checkedIds: string[];
  selectedKeyId: string;
  usage: Record<string, ApiKeyUsage>;
  routes: Route[];
  providers: Provider[];
  ownerLabel: (key: APIKey) => string;
  saving: boolean;
  onToggleCheck: (keyId: string, index: number, shiftKey: boolean) => void;
  onSelectAll: () => void;
  onClearSelection: () => void;
  onSelect: (key: APIKey) => void;
  onOpenDrawer: (key: APIKey) => void;
  onCopyKey: (key: APIKey) => void;
  copiedKeyId: string;
  onDelete: (key: APIKey) => void;
  onToggleEnabled: (key: APIKey) => void;
  onBatchDelete: () => void;
  /** 表格视图的筛选行：与左侧队列同一套控件。 */
  filters?: ApiKeyFiltersProps;
  providersForFilters?: Provider[];
  pagination?: {page: number; total: number; pageSize: number; onPrev: () => void; onNext: () => void};
}) {
  const allSelected = keys.length > 0 && keys.every((key) => checkedIds.includes(key.id));
  return (
    <section className="akc-tablecard card">
      <header className="akc-table-head">
        <div className="akc-list-title">
          <Icon path={ICONS.table} size={15} className="accent" />
          <h3>全量密钥矩阵</h3>
          <span className="akc-list-stat">
            显示 <b>{keys.length}</b> / {totalKeys}
            {checkedIds.length > 0 ? <span className="akc-list-stat-checked"> · 已选 {checkedIds.length}</span> : null}
          </span>
        </div>
        <div className="akc-table-actions">
          {checkedIds.length > 0 ? (
            <>
              <button type="button" className="akc-bulk-delete" disabled={saving} onClick={onBatchDelete}>
                <Icon path={ICONS.trash} size={13} />
                删除选中（{checkedIds.length}）
              </button>
              <button type="button" className="akc-bulk-clear" onClick={onClearSelection}>清除选择</button>
            </>
          ) : null}
        </div>
      </header>
      {filters ? (
        <div className="akc-table-filters">
          <ApiKeyFilterFields filters={filters} providers={providersForFilters || providers} />
        </div>
      ) : null}
      <div className="akc-table-scroll">
        <table className="akc-table">
          <thead>
            <tr>
              <th className="akc-th-check">
                <input
                  type="checkbox"
                  checked={allSelected}
                  ref={(node) => { if (node) node.indeterminate = checkedIds.length > 0 && !allSelected; }}
                  aria-label="全选"
                  onChange={(event) => { if (event.target.checked) onSelectAll(); else onClearSelection(); }}
                />
              </th>
              <th>名称与密钥</th>
              <th>所属用户</th>
              <th>输入 Provider</th>
              <th>协议</th>
              <th>首选模型</th>
              <th className="num">今日</th>
              <th className="num">本月</th>
              <th>状态</th>
              <th className="akc-th-actions">操作</th>
            </tr>
          </thead>
          <tbody>
            {keys.length === 0 ? (
              <tr>
                <td colSpan={10} className="akc-table-empty">当前筛选条件下没有匹配的密钥。</td>
              </tr>
            ) : keys.map((key, index) => {
              const model = apiKeyRowModel(key, routes, providers, usage[key.id], ownerLabel(key));
              const isChecked = checkedIds.includes(key.id);
              const converted = Boolean(model.inputProtocol && model.outputProtocol && model.inputProtocol !== model.outputProtocol);
              return (
                <tr
                  key={key.id}
                  className={`${isChecked ? 'checked ' : ''}${selectedKeyId === key.id ? 'active' : ''}`}
                  onClick={() => onSelect(key)}
                  onDoubleClick={() => onOpenDrawer(key)}
                >
                  <td className="akc-th-check" onClick={(event) => event.stopPropagation()}>
                    <input
                      type="checkbox"
                      checked={isChecked}
                      disabled={saving}
                      aria-label={`选择 ${key.name}`}
                      onChange={(event) => onToggleCheck(key.id, index, !!(event.nativeEvent as MouseEvent).shiftKey)}
                    />
                  </td>
                  <td>
                    <div className="akc-table-name">
                      <span className={`akc-key-dot${key.enabled ? ' on' : ''}`} />
                      <span title={key.name}>{key.name}</span>
                    </div>
                    <div className="akc-table-sub mono">{maskKey(key.key)}</div>
                  </td>
                  <td>
                    <span className={`akc-owner-chip${model.ownerIsAdmin ? ' admin' : ''}`}>
                      <Icon path={model.ownerIsAdmin ? ICONS.shieldCheck : ICONS.user} size={11} />
                      {model.ownerLabel}
                    </span>
                  </td>
                  <td>
                    {model.primary ? <ProviderLabel provider={model.primary} size={15} /> : <span className="akc-chain-missing">未绑定</span>}
                  </td>
                  <td>
                    <div className="akc-table-protocols">
                      {model.outputProtocol ? (
                        converted ? (
                          <>
                            <span>{protocolLabel(model.inputProtocol!)}</span>
                            <span className="akc-proto-arrow">→</span>
                            <span className="out">{protocolLabel(model.outputProtocol)}</span>
                          </>
                        ) : (
                          <>
                            <span className="out">{protocolLabel(model.outputProtocol)}</span>
                            <span className="akc-proto-same">透传</span>
                          </>
                        )
                      ) : <Badge tone="red">失效</Badge>}
                    </div>
                  </td>
                  <td>
                    <div className={`akc-table-sub strong${key.modelOverride ? '' : ' muted'}`} title={model.modelLabel}>{model.modelLabel}</div>
                  </td>
                  <td className="num mono">{model.usage.todayRequests.toLocaleString()}</td>
                  <td className="num mono">{model.usage.monthRequests.toLocaleString()}</td>
                  <td>
                    <button
                      type="button"
                      className={`akc-status-pill compact${key.enabled ? ' on' : ''}`}
                      disabled={saving}
                      title={key.enabled ? '点击停用' : '点击启用'}
                      onClick={(event) => { event.stopPropagation(); onToggleEnabled(key); }}
                    >
                      <span className="akc-status-dot" />
                      {key.enabled ? '启用中' : '已停用'}
                    </button>
                  </td>
                  <td className="akc-th-actions" onClick={(event) => event.stopPropagation()}>
                    <div className="akc-table-row-actions">
                      <button type="button" className="akc-row-icon" title="打开配置" onClick={() => onOpenDrawer(key)}>
                        <Icon path={ICONS.sliders} size={13} />
                      </button>
                      <button type="button" className="akc-row-icon" title="复制密钥" onClick={() => onCopyKey(key)}>
                        <Icon path={copiedKeyId === key.id ? ICONS.check : ICONS.copy} size={13} />
                      </button>
                      <button type="button" className="akc-row-icon danger" title="删除" disabled={saving} onClick={() => onDelete(key)}>
                        <Icon path={ICONS.trash} size={13} />
                      </button>
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {/* 底部条同时说「这一页多少」和「一共多少」：只显示当前页条数时，翻页后
          数字不变，看着像总条数；翻页控件也始终可见（只有一页时禁用），否则页脚
          比一行表格还高，短屏幕上会被挤到视口外。 */}
      <footer className="akc-list-foot">
        <span>单击选中 · 双击打开配置抽屉 · Shift 连续多选</span>
        <span className="akc-pager-actions">
          <span>
            共 {totalKeys} 把密钥
            {pagination ? ` · 第 ${pagination.page} / ${pagination.total} 页 · 每页 ${pagination.pageSize} 个` : ''}
          </span>
          {pagination ? (
            <>
              <button type="button" className="akc-soft-btn" disabled={pagination.page <= 1} onClick={pagination.onPrev}>上一页</button>
              <button type="button" className="akc-soft-btn" disabled={pagination.page >= pagination.total} onClick={pagination.onNext}>下一页</button>
            </>
          ) : null}
        </span>
      </footer>
    </section>
  );
}

// ---------------------------------------------------------------------------
// 侧滑抽屉（全量表格里双击行打开）：复用同一个详情工作区
// ---------------------------------------------------------------------------
export function ApiKeyDrawer({open, title, onClose, children}: {open: boolean; title: string; onClose: () => void; children: React.ReactNode}) {
  React.useEffect(() => {
    if (!open) return undefined;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') { event.stopPropagation(); onClose(); }
    };
    document.addEventListener('keydown', onKeyDown, true);
    return () => document.removeEventListener('keydown', onKeyDown, true);
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="akc-drawer-backdrop" onMouseDown={onClose}>
      <aside className="akc-drawer" onMouseDown={(event) => event.stopPropagation()}>
        <div className="akc-drawer-bar">
          <span>密钥配置 · {title}</span>
          <button type="button" className="akc-row-icon" aria-label="关闭" onClick={onClose}>
            <Icon path={ICONS.close} size={15} />
          </button>
        </div>
        <div className="akc-drawer-body">{children}</div>
      </aside>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 底部批量操作浮条
// ---------------------------------------------------------------------------
export function ApiKeyBatchBar({
  count,
  saving,
  onClear,
  onEnable,
  onDisable,
  onDelete,
}: {
  count: number;
  saving: boolean;
  onClear: () => void;
  onEnable: () => void;
  onDisable: () => void;
  onDelete: () => void;
}) {
  if (count === 0) return null;
  return (
    <div className="akc-batchbar" role="toolbar" aria-label="批量操作">
      <span className="akc-batchbar-count">
        已选择 <b>{count}</b> 把密钥
      </span>
      <div className="akc-batchbar-actions">
        <button type="button" disabled={saving} onClick={onEnable}>批量启用</button>
        <button type="button" disabled={saving} onClick={onDisable}>批量停用</button>
        <button type="button" className="danger" disabled={saving} onClick={onDelete}>
          <Icon path={ICONS.trash} size={13} />
          批量删除
        </button>
      </div>
      <button type="button" className="akc-batchbar-close" aria-label="取消选择" onClick={onClear}>
        <Icon path={ICONS.close} size={14} />
      </button>
    </div>
  );
}
