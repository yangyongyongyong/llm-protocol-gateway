import React from 'react';
import {
  fallbackAdd,
  fallbackMove,
  fallbackRemove,
  peerProviderMemberName,
  peerUpstreamName,
  providerAccountLabel,
  providerConnectKind,
  providerGroupLabel,
  providerMatchesQuery,
  providerOptionLabel,
} from '../lib';
import {providerIcon} from './provider_label';
import {BrandIcon} from './ui';
import {groupProvidersByBrand} from './provider_select';
import type {Provider} from '../types';

/**
 * BrandFilterChips 是一条「全部 / 品牌…」的筛选胶囊。两侧列表共用它，胶囊上带
 * 该品牌在当前搜索结果里的条数，没有命中的品牌不出现，免得点进去全是空的。
 */
function BrandFilterChips({groups, value, onChange}: {
  groups: Array<{key: string; title: string; icon: string; count: number}>;
  value: string;
  onChange: (key: string) => void;
}) {
  if (groups.length < 2) return null;
  return (
    <div className="fpp-chips" role="group" aria-label="按 Provider 品牌筛选">
      <button
        type="button"
        className={`fpp-chip${value === '' ? ' active' : ''}`}
        aria-pressed={value === ''}
        onClick={() => onChange('')}
      >
        全部
      </button>
      {groups.map((group) => (
        <button
          key={group.key}
          type="button"
          className={`fpp-chip${value === group.key ? ' active' : ''}`}
          aria-pressed={value === group.key}
          title={group.title}
          onClick={() => onChange(group.key)}
        >
          <BrandIcon icon={group.icon} name={group.title} size={12} className="fpp-chip-icon" />
          <span className="fpp-chip-label">{group.title}</span>
          <span className="fpp-chip-count">{group.count}</span>
        </button>
      ))}
    </div>
  );
}

/**
 * ProviderRowFace 是两侧列表里的一行：名字 + 账号（第二行）+ 协议。
 *
 * 上游网关的成员行拆成「成员名 + 上游名」两行：名字那一格写「vps1 · DeepSeek
 * Chat」会自己把自己挤进省略号，而真正要知道的「它属于哪条上游」正好可以放在
 * 第二行——和账号的位置一样。
 */
function ProviderRowFace({provider}: {provider: Provider}) {
  const peer = providerConnectKind(provider) === 'peer';
  const name = peer ? peerProviderMemberName(provider) : provider.name;
  const account = peer ? peerUpstreamName(provider) : providerAccountLabel(provider);
  const showAccount = Boolean(account) && account !== provider.name && account !== name;
  return (
    <span className="fpp-face">
      <BrandIcon icon={providerIcon(provider)} name={provider.name} size={15} className="filter-option-icon" />
      <span className="fpp-face-text">
        <span className="fpp-face-name" title={peer ? provider.name : providerOptionLabel(provider)}>{name}</span>
        {showAccount ? <span className="fpp-face-account" title={peer ? `来自上游网关 ${account}` : account}>{account}</span> : null}
      </span>
      <span className="fpp-face-proto">{provider.protocol}</span>
    </span>
  );
}

/**
 * FallbackProviderPicker 是「配置备选 Provider」的双栏选择器：左边是还没进链的
 * Provider，右边是按降级顺序排好的备选链。单击一行就挪到对面，勾选后批量挪。
 *
 * 为什么不用单列勾选列表：备选链是有序的，单列列表既看不出「哪个已经在链上」，
 * 也没法在好几台之间排先后；双栏把「选谁」和「谁先谁后」拆成两个动作。
 *
 * 排序用指针拖拽（与备选流水线同一套手感：按下后移动超过 4px 才算拖），另外
 * 保留 ↑/↓ 键盘路径——拖动对触屏和键盘都不友好。
 */
export function FallbackProviderPicker({providers, selectedIds, sameProtocol, onChange, thirdColumn}: {
  providers: Provider[];
  selectedIds: string[];
  /** 判断备选与首选是否同协议；跨协议由网关自动转换，不强制锁定模型。 */
  sameProtocol: (provider: Provider) => boolean;
  onChange: (next: string[]) => void;
  /**
   * 第三列（可选）。调用方用它把「固定模型替换」放进同一行，而不是在双栏下面再叠
   * 一大块——上下叠起来时页面上会出现两个一模一样的大下拉，还得再附一个滚轮。
   */
  thirdColumn?: React.ReactNode;
}) {
  const [query, setQuery] = React.useState('');
  const [brand, setBrand] = React.useState('');
  const [pickedLeft, setPickedLeft] = React.useState<string[]>([]);
  const [pickedRight, setPickedRight] = React.useState<string[]>([]);
  // 刚变化的行：加个短暂的入场动画提示「它刚挪过来」，避免视线在两侧跳。
  const [justMoved, setJustMoved] = React.useState<Record<string, boolean>>({});
  const [drag, setDrag] = React.useState<{id: string; overId: string; after: boolean; active: boolean} | null>(null);

  const selectedSet = React.useMemo(() => new Set(selectedIds), [selectedIds]);
  const selected = React.useMemo(
    () => selectedIds.map((id) => providers.find((item) => item.id === id)).filter((item): item is Provider => Boolean(item)),
    [selectedIds, providers],
  );
  const available = React.useMemo(
    () => providers.filter((item) => !selectedSet.has(item.id)),
    [providers, selectedSet],
  );

  // 两侧各自按搜索词过滤，再按品牌收窄；品牌胶囊的条数按过滤后的结果算。
  const narrow = React.useCallback((list: Provider[]) => {
    const matched = list.filter((item) => providerMatchesQuery(item, query));
    const groups = groupProvidersByBrand(matched).map((group) => ({
      key: group.key,
      title: group.title,
      icon: group.icon,
      count: group.items.length,
    }));
    const shown = brand ? matched.filter((item) => providerGroupLabel(item) === brand) : matched;
    return {matched, shown, groups};
  }, [query, brand]);
  const left = narrow(available);
  const right = narrow(selected);

  // 筛选把已选项藏起来时，隐藏行的勾选状态要清掉，否则「移出」会挪走看不见的项。
  React.useEffect(() => {
    const visible = new Set(right.shown.map((item) => item.id));
    setPickedRight((current) => {
      const next = current.filter((id) => visible.has(id));
      return next.length === current.length ? current : next;
    });
  }, [right.shown]);
  React.useEffect(() => {
    const visible = new Set(left.shown.map((item) => item.id));
    setPickedLeft((current) => {
      const next = current.filter((id) => visible.has(id));
      return next.length === current.length ? current : next;
    });
  }, [left.shown]);

  const flash = React.useCallback((ids: string[]) => {
    if (ids.length === 0) return;
    setJustMoved((current) => {
      const next = {...current};
      for (const id of ids) next[id] = true;
      return next;
    });
    window.setTimeout(() => {
      setJustMoved((current) => {
        const next = {...current};
        for (const id of ids) delete next[id];
        return next;
      });
    }, 450);
  }, []);

  const add = (ids: string[]) => {
    if (ids.length === 0) return;
    onChange(fallbackAdd(selectedIds, ids));
    setPickedLeft((current) => current.filter((id) => !ids.includes(id)));
    flash(ids);
  };
  const remove = (ids: string[]) => {
    if (ids.length === 0) return;
    onChange(fallbackRemove(selectedIds, ids));
    setPickedRight((current) => current.filter((id) => !ids.includes(id)));
    flash(ids);
  };
  const move = (id: string, delta: number) => {
    const index = selectedIds.indexOf(id);
    if (index < 0) return;
    const target = index + delta;
    if (target < 0 || target >= selectedIds.length) return;
    onChange(fallbackMove(selectedIds, id, selectedIds[target], delta > 0));
  };

  // 指针拖拽：与备选流水线同一套手感，松手才重置顺序。
  const beginDrag = (event: React.PointerEvent<HTMLElement>, id: string) => {
    if (event.button !== 0) return;
    if ((event.target as HTMLElement).closest('button, input')) return;
    const list = event.currentTarget.closest('.fpp-list');
    if (!list) return;
    const startY = event.clientY;
    let active = false;
    const state = {id, overId: '', after: false, active: false};

    const updateTarget = (clientY: number) => {
      const rows = [...list.querySelectorAll<HTMLElement>('.fpp-row')];
      for (const row of rows) {
        const rect = row.getBoundingClientRect();
        if (clientY < rect.top || clientY > rect.bottom) continue;
        const rowId = row.dataset.providerId || '';
        state.overId = rowId && rowId !== id ? rowId : '';
        state.after = clientY > rect.top + rect.height / 2;
        return;
      }
      state.overId = '';
    };
    const onMove = (moveEvent: PointerEvent) => {
      if (!active) {
        if (Math.abs(moveEvent.clientY - startY) < 4) return;
        active = true;
        state.active = true;
        document.body.classList.add('fpp-dragging');
      }
      moveEvent.preventDefault();
      updateTarget(moveEvent.clientY);
      setDrag({...state});
    };
    const finish = () => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', finish);
      window.removeEventListener('pointercancel', finish);
      document.body.classList.remove('fpp-dragging');
      setDrag(null);
      if (!active || !state.overId) return;
      const next = fallbackMove(selectedIds, id, state.overId, state.after);
      if (next !== selectedIds) onChange(next);
    };
    window.addEventListener('pointermove', onMove, {passive: false});
    window.addEventListener('pointerup', finish);
    window.addEventListener('pointercancel', finish);
  };

  const allLeftShownPicked = left.shown.length > 0 && left.shown.every((item) => pickedLeft.includes(item.id));
  const allRightShownPicked = right.shown.length > 0 && right.shown.every((item) => pickedRight.includes(item.id));

  return (
    <div className="fpp">
      <div className="fpp-search">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
          <circle cx="11" cy="11" r="8" />
          <line x1="21" y1="21" x2="16.65" y2="16.65" />
        </svg>
        <input
          type="search"
          value={query}
          autoComplete="one-time-code"
          data-1p-ignore
          data-lpignore="true"
          name="fallbackProviderQuery"
          placeholder="搜索名称、账号或 ID（空格分词）"
          aria-label="搜索 Provider"
          onChange={(event) => setQuery(event.target.value)}
        />
        {query ? (
          <button type="button" className="fpp-search-clear" title="清空搜索词" onClick={() => setQuery('')}>×</button>
        ) : null}
      </div>
      <BrandFilterChips groups={left.groups.concat(right.groups.filter((group) => !left.groups.some((item) => item.key === group.key)))} value={brand} onChange={setBrand} />

      <div className="fpp-cols">
        <section className="fpp-col" aria-label="可选 Provider">
          <header className="fpp-col-head">
            <span className="fpp-col-title">可选 Provider</span>
            <span className="fpp-col-count">{available.length}</span>
            <button
              type="button"
              className="fpp-link"
              disabled={left.shown.length === 0}
              onClick={() => (allLeftShownPicked ? setPickedLeft([]) : setPickedLeft(left.shown.map((item) => item.id)))}
            >
              {allLeftShownPicked ? '取消全选' : '全选'}
            </button>
          </header>
          <div className="fpp-list" role="listbox" aria-multiselectable="true">
            {left.shown.length === 0 ? (
              <div className="fpp-empty">{available.length === 0 ? '全部 Provider 都已在备选链上' : '无匹配项'}</div>
            ) : left.shown.map((provider) => {
              const picked = pickedLeft.includes(provider.id);
              return (
                <div
                  key={provider.id}
                  className={`fpp-row${picked ? ' picked' : ''}${justMoved[provider.id] ? ' arrived' : ''}`}
                  data-provider-id={provider.id}
                >
                  <input
                    type="checkbox"
                    className="fpp-check"
                    checked={picked}
                    aria-label={`勾选 ${provider.name}`}
                    onChange={() => setPickedLeft((current) => (picked ? current.filter((id) => id !== provider.id) : [...current, provider.id]))}
                  />
                  <button type="button" className="fpp-row-main" onClick={() => add([provider.id])}>
                    <ProviderRowFace provider={provider} />
                  </button>
                  <button type="button" className="fpp-row-act add" title="加入备选链末尾" onClick={() => add([provider.id])}>›</button>
                </div>
              );
            })}
          </div>
          <footer className="fpp-col-foot">
            <span className="fpp-hint">
              {pickedLeft.length > 0 ? `已勾选 ${pickedLeft.length} 项` : '单击加入备选；勾选后批量加入'}
            </span>
            <button
              type="button"
              className="fpp-batch"
              disabled={pickedLeft.length === 0}
              onClick={() => add(pickedLeft)}
            >
              加入备选 {pickedLeft.length > 0 ? `(${pickedLeft.length})` : ''}
            </button>
          </footer>
        </section>

        <section className="fpp-col" aria-label="备选链（按降级顺序）">
          <header className="fpp-col-head">
            <span className="fpp-col-title">备选链</span>
            <span className="fpp-col-count">{selected.length}</span>
            <button
              type="button"
              className="fpp-link"
              disabled={right.shown.length === 0}
              onClick={() => (allRightShownPicked ? setPickedRight([]) : setPickedRight(right.shown.map((item) => item.id)))}
            >
              {allRightShownPicked ? '取消全选' : '全选'}
            </button>
          </header>
          <div className="fpp-list" role="listbox" aria-multiselectable="true">
            {right.shown.length === 0 ? (
              <div className="fpp-empty">{selected.length === 0 ? '还没有备选：首选额度耗尽后不会自动换到别的 Provider' : '无匹配项'}</div>
            ) : right.shown.map((provider) => {
              const picked = pickedRight.includes(provider.id);
              const position = selected.findIndex((item) => item.id === provider.id);
              const crossProtocol = !sameProtocol(provider);
              const dragging = drag?.active && drag.id === provider.id;
              const overHere = drag?.active && drag.overId === provider.id;
              return (
                <div
                  key={provider.id}
                  className={[
                    'fpp-row',
                    'chain',
                    picked ? 'picked' : '',
                    justMoved[provider.id] ? ' arrived' : '',
                    crossProtocol ? ' cross' : '',
                    dragging ? ' dragging' : '',
                    overHere ? (drag.after ? ' drop-after' : ' drop-before') : '',
                  ].filter(Boolean).join(' ')}
                  data-provider-id={provider.id}
                  onPointerDown={(event) => beginDrag(event, provider.id)}
                >
                  <input
                    type="checkbox"
                    className="fpp-check"
                    checked={picked}
                    aria-label={`勾选 ${provider.name}`}
                    onChange={() => setPickedRight((current) => (picked ? current.filter((id) => id !== provider.id) : [...current, provider.id]))}
                  />
                  <span className="fpp-rank" title="降级顺序：数字越小越先试">#{position + 1}</span>
                  <ProviderRowFace provider={provider} />
                  {crossProtocol ? <span className="fpp-flag" title="网关会自动做协议转换；固定模型是可选项">跨协议自动转换</span> : null}
                  <span className="fpp-order">
                    <button type="button" className="fpp-order-btn" title="上移" disabled={position <= 0} onClick={() => move(provider.id, -1)}>↑</button>
                    <button type="button" className="fpp-order-btn" title="下移" disabled={position >= selected.length - 1} onClick={() => move(provider.id, 1)}>↓</button>
                  </span>
                  <span className="fpp-row-actions">
                    <button type="button" className="fpp-row-act" title="移回可选" onClick={() => remove([provider.id])} aria-label={`移出 ${provider.name}`}>‹</button>
                  </span>
                </div>
              );
            })}
          </div>
          <footer className="fpp-col-foot">
            <span className="fpp-hint">
              {pickedRight.length > 0 ? `已勾选 ${pickedRight.length} 项` : '拖动行可调整顺序；↑/↓ 也行'}
            </span>
            <button
              type="button"
              className="fpp-batch danger"
              disabled={pickedRight.length === 0}
              onClick={() => remove(pickedRight)}
            >
              移出 {pickedRight.length > 0 ? `(${pickedRight.length})` : ''}
            </button>
          </footer>
        </section>
        {thirdColumn}
      </div>
    </div>
  );
}
