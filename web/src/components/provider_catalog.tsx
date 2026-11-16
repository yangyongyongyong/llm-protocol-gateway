import React, { useEffect, useMemo, useRef, useState } from 'react';
import type { CustomProvider, Provider, ProviderPreset } from '../types';
import { presetMatchesProvider } from '../lib';
import { customProviderPresets, groupProviderPresets } from '../provider_presets';
import { BrandIcon, InfoTip, Modal } from './ui';

/**
 * 添加 Provider 的目录弹窗：检索 + 分区磁贴，点一个即预填连接方式、协议与接入
 * 地址。走的是设计系统既有的 Modal（Portal 到 body、z-index 与 ESC 由 modalLayers
 * 统一管理），不自己造遮罩与层级。
 */
export function ProviderCatalogDialog({
  open,
  providers,
  customProviders,
  hasProviders,
  onClose,
  onPick,
  onManageCustomProviders,
  onDeleteCustomProvider,
}: {
  open: boolean;
  providers: Provider[];
  customProviders: CustomProvider[];
  hasProviders: boolean;
  onClose: () => void;
  onPick: (preset: ProviderPreset) => void;
  onManageCustomProviders?: () => void;
  onDeleteCustomProvider?: (provider: CustomProvider) => void;
}) {
  const [query, setQuery] = useState('');
  const [openCustomMenuID, setOpenCustomMenuID] = useState('');
  const searchRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    if (!open) return;
    // 每次打开都从完整目录开始，避免上次的检索词把这次"框里没东西"地卡住。
    setQuery('');
    const timer = window.setTimeout(() => searchRef.current?.focus(), 60);
    return () => window.clearTimeout(timer);
  }, [open]);

  useEffect(() => {
    if (!openCustomMenuID) return;
    const close = (event: MouseEvent) => {
      if ((event.target as HTMLElement | null)?.closest('[data-custom-provider-menu]')) return;
      setOpenCustomMenuID('');
    };
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, [openCustomMenuID]);

  const allGroups = useMemo(() => groupProviderPresets(query, customProviders), [query, customProviders]);
  const customGroup = allGroups.find((group) => group.id === 'custom');
  const groups = allGroups.filter((group) => group.id !== 'custom');
  const customPresets = customGroup?.presets || customProviderPresets(customProviders).filter((preset) => {
    const needle = query.trim().toLowerCase();
    return !needle || [preset.name, preset.note, preset.customProviderId].some((value) => (value || '').toLowerCase().includes(needle));
  });
  const addedCount = (preset: ProviderPreset) => providers.filter((provider) => presetMatchesProvider(preset, provider)).length;
  const renderPreset = (preset: ProviderPreset) => {
    const added = addedCount(preset);
    return (
      <button
        key={preset.id}
        type="button"
        className={`provider-tile ${added > 0 ? 'have' : ''}`}
        title={[preset.baseUrl, preset.note, preset.keysUrl ? `密钥：${preset.keysUrl}` : ''].filter(Boolean).join('\n')}
        onClick={() => onPick(preset)}
      >
        <BrandIcon icon={preset.icon} name={preset.name} className="provider-tile-icon" />
        <span className="provider-tile-name">{preset.name}</span>
        {added > 0 ? <span className="provider-tile-count">{added}</span> : null}
      </button>
    );
  };
  const renderCustomPreset = (preset: ProviderPreset) => {
    const custom = customProviders.find((item) => item.id === preset.customProviderId);
    if (!custom) return null;
    const added = addedCount(preset);
    const menuOpen = openCustomMenuID === custom.id;
    return (
      <div className="provider-custom-tile" key={preset.id} data-custom-provider-menu>
        <button
          type="button"
          className={`provider-tile ${added > 0 ? 'have' : ''}`}
          title={[custom.description, custom.scriptDir].filter(Boolean).join('\n')}
          onClick={() => onPick(preset)}
        >
          <BrandIcon icon={preset.icon} name={preset.name} className="provider-tile-icon" />
          <span className="provider-tile-name">{preset.name}</span>
          {added > 0 ? <span className="provider-tile-count">{added}</span> : null}
        </button>
        {onDeleteCustomProvider ? (
          <button
            type="button"
            className="provider-custom-menu-trigger"
            aria-label={`${custom.name} 类型菜单`}
            aria-expanded={menuOpen}
            title="类型操作"
            onClick={() => setOpenCustomMenuID((current) => current === custom.id ? '' : custom.id)}
          >
            ⋮
          </button>
        ) : null}
        {menuOpen ? (
          <div className="provider-custom-menu-popover" role="menu">
            {onDeleteCustomProvider ? <button type="button" role="menuitem" className="danger" onClick={() => { setOpenCustomMenuID(''); onDeleteCustomProvider(custom); }}>删除类型</button> : null}
          </div>
        ) : null}
      </div>
    );
  };
  if (!open) return null;

  return (
    <Modal
      title={hasProviders ? '添加 Provider' : '添加你的第一个 Provider'}
      description="厂商预设会自动填好地址与协议；自定义 Provider 类型选中后只需填写上游 URL 和 Key。"
      size="wide"
      onClose={onClose}
    >
      <div className="provider-catalog-search">
        <input
          ref={searchRef}
          type="text"
          className="models-search-input"
          value={query}
          placeholder="检索厂商、中转站或地址…"
          aria-label="检索 Provider 目录"
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            // 有检索词时 ESC 先清词，再按一次才关弹窗，避免误触丢掉输入条件。
            if (event.key === 'Escape' && query) {
              event.preventDefault();
              event.stopPropagation();
              setQuery('');
            }
          }}
        />
      </div>
      {(customProviders.length > 0 || onManageCustomProviders) ? (
        <div className="provider-catalog-section">
          <div className="provider-catalog-section-head" style={{alignItems: 'center'}}>
            <span className="provider-catalog-section-title">自定义 Provider</span>
            <InfoTip text="自定义类型由 AI 通过 API 创建和编辑；右侧三点菜单只提供类型删除。选择类型创建实例时只需填写上游 URL 和 Key。">
              <span className="info-tip-icon" aria-hidden="true">?</span>
            </InfoTip>
            <span className="provider-catalog-section-desc">先定义协议和脚本，再创建 URL/Key 实例</span>
            {onManageCustomProviders ? (
              <button type="button" className="btn success btn-sm" style={{marginLeft: 'auto'}} onClick={onManageCustomProviders}>＋ 添加自定义类型</button>
            ) : null}
          </div>
          {customPresets.length > 0 ? (
            <div className="provider-tiles">{customPresets.map(renderCustomPreset)}</div>
          ) : (
            <div className="provider-catalog-empty">
              {customProviders.length === 0 ? '还没有自定义 Provider 类型。点击右侧按钮先添加类型。' : `没有匹配「${query.trim()}」的自定义类型。`}
            </div>
          )}
        </div>
      ) : null}
      {groups.length === 0 && customPresets.length === 0 ? (
        <div className="provider-catalog-empty">
          目录里没有「{query.trim()}」。
          <button className="btn-text" type="button" onClick={() => onPick(customEndpointPreset())}>按自定义端点添加</button>
        </div>
      ) : (
        groups.map((group) => (
          <div className="provider-catalog-section" key={group.id}>
            <div className="provider-catalog-section-head">
              <span className="provider-catalog-section-title">{group.title}</span>
              <span className="provider-catalog-section-desc">{group.desc}</span>
            </div>
            <div className="provider-tiles">
              {group.presets.map(renderPreset)}
            </div>
          </div>
        ))
      )}
    </Modal>
  );
}

function customEndpointPreset(): ProviderPreset {
  return {
    id: 'custom',
    name: '自定义端点',
    section: 'local',
    authType: 'api_key',
    protocol: 'openai_chat',
    baseUrl: 'https://example.com/v1/chat/completions',
  };
}
