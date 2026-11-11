import React from 'react';
import {PROVIDER_PRESETS} from '../provider_presets';
import {
    protocolLabel,
    peerProviderMemberName,
    peerUpstreamName,
    providerAccountLabel,
    providerConnectKind,
    providerGroupLabel,
    providerOptionLabel,
    reportIfLooksLikeAutofill,
    shortenAccountLabel,
} from '../lib';
import {providerIcon} from './provider_label';
import {BrandIcon} from './ui';
import type {IconSelectOption} from './selects';
import type {Provider, ProviderConnectKind, ProviderPresetSection} from '../types';

/**
 * 每个 Provider 在下拉里给人看的名字：品牌名 + 脱敏账号
 * （WorkBuddy 订阅 · 1815****4947）。同一个服务商每多登一个账号就多一条同名
 * 记录，只读名字分不出是哪条；账号必须在这里，展开选的时候能分清，选完之后
 * 触发器上也得能分清，两处用同一块文字。收起后触发器只有一行，没有分组标题
 * 兜底，所以账号不能被省掉。
 */
export function providerAccountFace(provider: Provider): string {
  return [providerBrandName(provider), providerAccountFaceTail(provider)].filter(Boolean).join(' · ');
}

/**
 * 下拉行里的名字。展开时头顶有分组标题写着品牌名，每行再重复一遍「claude pro」
 * 只会把账号挤进省略号——所以菜单里用「账号」，品牌只留给触发器。
 *
 * 上游网关成员行相反：它的名字（vps1 · DeepSeek Chat）本身就是唯一能认人的那截，
 * 品牌名又恰好是上游名、已经写在分组标题上，所以菜单里用成员自己的名字。
 */
function providerMenuLabel(provider: Provider): string {
  if (providerConnectKind(provider) === 'peer') return peerProviderMemberName(provider);
  const brand = providerBrandName(provider);
  const tail = providerAccountFaceTail(provider);
  return tail || brand;
}

/** 品牌名之后要跟着的那截（账号 / 短 ID）；贴回名字里就是完整标签。 */
function providerAccountFaceTail(provider: Provider): string {
  // 上游网关没有「账号」：成员行自己的名字里已经带着上游名（vps1 · DeepSeek
  // Chat），再补一段只会把名字挤掉。上游名由分组标题说，这里留空。
  if (providerConnectKind(provider) === 'peer') return '';
  const account = providerAccountLabel(provider);
  if (!account || account === provider.name) return '';
  return maskAccountLabel(account);
}

/**
 * 去掉账号、只留品牌的名字：同一个服务商的多条记录共用一个分组标题，
 * 标题取该组里最早的花名（第一条记录的名称去掉账号后缀）。
 */
function providerBrandName(provider: Provider): string {
  const name = provider.name.trim();
  const account = providerAccountLabel(provider);
  if (!account) return name;
  const raw = account.replace(/[\s-]/g, '');
  // 名称里早就并进账号时（\`WorkBuddy 订阅 · 1815****4947\`、\`x · 1815****4947\`）截掉后半截。
  const joined = name.split(' · ');
  const masked = maskAccountLabel(account);
  const stripped = joined.length > 1 && joined.slice(1).some((part) => raw.endsWith(part.replace(/\*/g, '')) || part === masked)
    ? joined[0]
    : name;
  return stripped || name;
}

/**
 * 筛选项 / 弹窗里的「输入 Provider」选项：每个 Provider 一条，带上所属品牌
 * （group / groupKey）——菜单按品牌分成左右两栏，左栏一个品牌，右栏是它的账号。
 *
 * 右栏的名字只写账号（`1815****4947`、`luca`），品牌名由左栏说，不必每行重复；
 * 没有账号可认的（API Key 类）退回完整名称 + 短 ID，同名项之间靠它区分。同品牌
 * 里真撞名时整组退回完整名称（`WorkBuddy 订阅` 和 `WorkBuddy 订阅 · #3adf9871`
 * 是两条记录，后者才是能认人的那个）。
 *
 * 触发器上仍然显示「品牌 · 账号」（providerAccountFace），和菜单里两栏拼起来一致。
 */
export type ProviderBrandGroup = {
  /** providerGroupLabel 的值：同一服务商（API Key 类按 host）共用一个 */
  key: string;
  /** 一级筛选胶囊 / 分组标题上的品牌名 */
  title: string;
  icon: string;
  kind: ProviderConnectKind;
  items: Provider[];
};

/**
 * 按服务商把 Provider 收成组。同一个服务商每多登一个账号就多一条独立记录，
 * 平铺出来全是同名项；模型页的一级筛选和下拉菜单的分组标题都走这一份口径，
 * 免得两处对「算不算同一家」判断不一致。
 *
 * 上游网关的组就是「哪一条上游」，标题写上游名——成员行的名字是「上游 · 成员」，
 * 拿第一条的名字当标题会在标题里重复成员名。
 *
 * 品牌顺序按目录磁贴的排法（订阅登录 → 模型厂商 → 中转 → 本机/自建），目录里
 * 没有的落到「自定义」（排最后）；同段内保持 Provider 出现的顺序。
 */
export function groupProvidersByBrand(providers: Provider[]): ProviderBrandGroup[] {
  const groups = new Map<string, ProviderBrandGroup>();
  providers.forEach((provider) => {
    const key = providerGroupLabel(provider);
    const peer = providerConnectKind(provider) === 'peer';
    const group = groups.get(key);
    if (group) {
      group.items.push(provider);
      return;
    }
    groups.set(key, {
      key,
      title: (peer ? peerUpstreamName(provider) : providerBrandName(provider)) || key,
      icon: providerIcon(provider),
      kind: providerConnectKind(provider),
      items: [provider],
    });
  });
  return [...groups.values()].sort((left, right) => {
    const delta = sectionRank(left.kind) - sectionRank(right.kind);
    if (delta !== 0) return delta;
    return providers.indexOf(left.items[0]) - providers.indexOf(right.items[0]);
  });
}

export function providerSelectOptions(providers: Provider[], options: { withProtocol?: boolean } = {}): IconSelectOption[] {
  const withProtocol = options.withProtocol ?? true;
  return groupProvidersByBrand(providers).flatMap((group) => {
    const faces = group.items.map((provider) => providerMenuLabel(provider));
    const duplicated = group.items.length > 1 && new Set(faces).size !== faces.length;
    const indices = group.items.map((_, index) => index);
    indices.sort((left, right) => faces[left].localeCompare(faces[right], 'zh-Hans-CN'));
    return indices.map((index) => {
      const provider = group.items[index];
      const account = providerAccountLabel(provider);
      const face = faces[index];
      // 有账号的：右栏只写账号；上游成员：成员名（上游名在分组标题上）；
      // 没有账号可认的：完整名称。同组撞名时整组退回完整名称才分得清。
      const label = duplicated ? provider.name.trim() : face;
      return {
        value: provider.id,
        label,
        icon: group.icon,
        description: account ? undefined : provider.id,
        meta: withProtocol ? protocolLabel(provider.protocol) : undefined,
        keywords: `${providerOptionLabel(provider)} ${provider.name} ${account} ${provider.id}`,
        group: group.title,
        groupKey: group.key,
      };
    });
  });
}

/**
 * 多选 Provider 的搜索框 + 常驻勾选列表：给用户授权 Provider 用。
 *
 * 搜索框照 API 日志页的常驻搜索做（放大镜、清空 ×），列表直接铺开、输入即时
 * 过滤，不再用下拉弹层——弹层在弹窗里会盖住角色说明，勾选时也看不到整体都有
 * 哪些 Provider。
 */
export function ProviderChecklist({ providers, selected, onChange }: {
  providers: Provider[];
  selected: string[];
  onChange: (next: string[]) => void;
}) {
  const [query, setQuery] = React.useState('');

  const filtered = React.useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return providers;
    return providers.filter((provider) =>
      [providerOptionLabel(provider), provider.name, providerAccountLabel(provider), provider.id]
        .join(' ')
        .toLowerCase()
        .includes(needle),
    );
  }, [providers, query]);

  const toggle = (id: string) => {
    onChange(selected.includes(id) ? selected.filter((item) => item !== id) : [...selected, id]);
  };

  return (
    <div className="provider-checklist">
      <div className="provider-checklist-search">
        <svg className="provider-checklist-search-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <circle cx="11" cy="11" r="8" />
          <line x1="21" y1="21" x2="16.65" y2="16.65" />
        </svg>
        <input
          type="search"
          value={query}
          // 弹窗里紧挨着用户名 + 密码，Chrome 会把保存的账号灌进来；语义换成
          // one-time-code 才不被当成登录表单，再关掉密码管理器注入。
          autoComplete="one-time-code"
          data-1p-ignore
          data-lpignore="true"
          name="providerCheckQuery"
          placeholder="搜索名称、账号或 ID…"
          aria-label="搜索可用输入 Provider"
          onChange={(event) => {
            reportIfLooksLikeAutofill('provider-checklist-filled', query, event.target.value, event);
            setQuery(event.target.value);
          }}
        />
        {query ? (
          <button type="button" className="provider-checklist-clear" title="清空搜索词" onClick={() => setQuery('')}>
            ×
          </button>
        ) : null}
      </div>
      <div className="provider-checklist-list" role="listbox" aria-multiselectable="true" aria-label="可用输入 Provider">
        {filtered.length === 0 ? (
          <div className="provider-checklist-empty">无匹配项</div>
        ) : filtered.map((provider) => {
          const checked = selected.includes(provider.id);
          const label = providerOptionLabel(provider);
          return (
            <button
              key={provider.id}
              type="button"
              role="option"
              aria-selected={checked}
              className={`provider-checklist-row${checked ? ' selected' : ''}`}
              title={label}
              onClick={() => toggle(provider.id)}
            >
              <span className={`provider-checklist-box${checked ? ' on' : ''}`} aria-hidden="true">{checked ? '✓' : ''}</span>
              <BrandIcon icon={providerIcon(provider)} name={provider.name} size={15} className="filter-option-icon" />
              <span className="provider-checklist-label">{label}</span>
            </button>
          );
        })}
      </div>
      <div className="provider-checklist-count">已选 {selected.length} / 共 {providers.length}</div>
    </div>
  );
}

/** 目录里匹配得上就按磁贴分段，匹配不上（自建反代、换过域名）排到最后的自定义段。 */
function sectionRank(kind: ProviderConnectKind): number {
  const rank = PROVIDER_SECTION_ORDER.indexOf(
    PROVIDER_PRESETS.find((preset) => preset.authType === kind)?.section ?? 'local',
  );
  return rank >= 0 ? rank : PROVIDER_SECTION_ORDER.length;
}

const PROVIDER_SECTION_ORDER: ProviderPresetSection[] = ['subscription', 'vendor', 'relay', 'local'];

/** 手机号只留前 4 后 4（1815****4947）；邮箱砍掉域名；其余原样。 */
function maskAccountLabel(label: string): string {
  const phone = label.replace(/[\s-]/g, '');
  if (/^\+?\d{7,15}$/.test(phone)) {
    return phone.length <= 8 ? phone : `${phone.slice(0, 4)}****${phone.slice(-4)}`;
  }
  return shortenAccountLabel(label);
}
