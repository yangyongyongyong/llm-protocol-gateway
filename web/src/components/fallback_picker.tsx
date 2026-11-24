import React from 'react';
import {
  fallbackAdd,
  peerProviderMemberName,
  peerUpstreamName,
  providerAccountDetail,
  providerAccountLabel,
  providerConnectKind,
  providerGroupLabel,
  providerMatchesQuery,
  providerOptionLabel,
  providerSelectionGroup,
  selectionToggleGroup,
} from '../lib';
import {providerIcon} from './provider_label';
import {BrandIcon} from './ui';
import {groupProvidersByBrand} from './provider_select';
import {ProviderGroupHeader, type GroupPickState} from './provider_group_header';
import type {Provider, ProviderGroup} from '../types';

/**
 * ProviderGroupChips 是一条「全部 / 分组…」的筛选胶囊。管理员分组优先，
 * 没分组的 Provider 仍按品牌聚合。两侧列表共用它。
 */
function ProviderGroupChips({groups, value, onChange}: {
  groups: Array<{key: string; title: string; icon: string; count: number}>;
  value: string;
  onChange: (key: string) => void;
}) {
  if (groups.length < 2) return null;
  return (
    <div className="fpp-chips" role="group" aria-label="按 Provider 分组筛选">
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
  const account = peer
    ? [providerAccountDetail(provider), peerUpstreamName(provider)].filter(Boolean).join(' · ')
    : (providerAccountDetail(provider) || providerAccountLabel(provider));
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
export function FallbackProviderPicker({providers, providerGroups = [], selectedIds, sameProtocol, onChange, thirdColumn}: {
  providers: Provider[];
  /** 管理员分组的显示元数据；有 groupId 的 Provider 优先按它收组。 */
  providerGroups?: ProviderGroup[];
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
  const [groupKey, setGroupKey] = React.useState('');
  const [pickedLeft, setPickedLeft] = React.useState<string[]>([]);
  const [pickedRight, setPickedRight] = React.useState<string[]>([]);
  // 刚变化的行：加个短暂的入场动画提示「它刚挪过来」，避免视线在两侧跳。
  const [justMoved, setJustMoved] = React.useState<Record<string, boolean>>({});
  const [drag, setDrag] = React.useState<{index: number; overIndex: number; after: boolean; active: boolean} | null>(null);

  const countById = React.useMemo(() => {
    const counts: Record<string, number> = {};
    for (const id of selectedIds) counts[id] = (counts[id] || 0) + 1;
    return counts;
  }, [selectedIds]);
  const selectedRows = React.useMemo(
    () => selectedIds.map((id, index) => ({id, index, provider: providers.find((item) => item.id === id)})),
    [selectedIds, providers],
  );

  // 左侧永远显示全部 Provider：同一行可以反复加入，形成 p1/p2/... 的独立节点。
  const narrow = React.useCallback((list: Provider[]) => {
    const allGroups = groupProvidersByBrand(list, providerGroups);
    const groupTitleByKey = new Map(allGroups.map((group) => [group.key, group.title]));
    const matched = list.filter((item) => {
      const key = providerSelectionGroup(item, providerGroups)?.key || providerGroupLabel(item);
      return providerMatchesQuery(item, query, groupTitleByKey.get(key) || '');
    });
    const groups = groupProvidersByBrand(matched, providerGroups).map((group) => ({
      key: group.key,
      title: group.title,
      icon: group.icon,
      count: group.items.length,
    }));
    const sections = groupProvidersByBrand(matched, providerGroups).filter((group) => !groupKey || group.key === groupKey);
    const shown = sections.flatMap((group) => group.items);
    return {matched, shown, groups, sections};
  }, [query, groupKey, providerGroups]);
  const left = narrow(providers);
  const allGroups = narrow(providers).groups;
  const rightRows = selectedRows.filter((row) => {
    if (!row.provider) return false;
    const group = providerSelectionGroup(row.provider, providerGroups)?.key || providerGroupLabel(row.provider);
    return (!groupKey || group === groupKey) && providerMatchesQuery(row.provider, query);
  });

  const rowToken = (id: string, index: number) => `${id}\u0000${index}`;

  React.useEffect(() => {
    const visible = new Set(left.shown.map((item) => item.id));
    setPickedLeft((current) => {
      const next = current.filter((id) => visible.has(id));
      return next.length === current.length ? current : next;
    });
  }, [left.shown]);
  React.useEffect(() => {
    const visible = new Set(rightRows.map((row) => rowToken(row.id, row.index)));
    setPickedRight((current) => {
      const next = current.filter((token) => visible.has(token));
      return next.length === current.length ? current : next;
    });
  }, [rightRows]);

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
  const removeIndexes = (indexes: number[]) => {
    if (indexes.length === 0) return;
    const drop = new Set(indexes);
    const removed = selectedIds.filter((_, index) => drop.has(index));
    onChange(selectedIds.filter((_, index) => !drop.has(index)));
    setPickedRight([]);
    flash(removed);
  };
  const moveIndex = (fromIndex: number, toIndex: number, after: boolean) => {
    if (fromIndex < 0 || toIndex < 0 || fromIndex >= selectedIds.length || toIndex >= selectedIds.length || fromIndex === toIndex) return;
    const next = selectedIds.slice();
    const [id] = next.splice(fromIndex, 1);
    let target = toIndex;
    if (fromIndex < toIndex) target -= 1;
    next.splice(after ? target + 1 : target, 0, id);
    onChange(next);
    setPickedRight([]);
  };

  const beginDrag = (event: React.PointerEvent<HTMLElement>, index: number) => {
    if (event.button !== 0) return;
    if ((event.target as HTMLElement).closest('button, input, .searchable-select')) return;
    const list = event.currentTarget.closest('.fpp-list');
    if (!list) return;
    const startY = event.clientY;
    let active = false;
    const state = {index, overIndex: -1, after: false, active: false};

    const updateTarget = (clientY: number) => {
      const rows = [...list.querySelectorAll<HTMLElement>('.fpp-row')];
      for (const row of rows) {
        const rect = row.getBoundingClientRect();
        if (clientY < rect.top || clientY > rect.bottom) continue;
        const rowIndex = Number.parseInt(row.dataset.rowIndex || '', 10);
        state.overIndex = Number.isFinite(rowIndex) && rowIndex !== index ? rowIndex : -1;
        state.after = clientY > rect.top + rect.height / 2;
        return;
      }
      state.overIndex = -1;
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
      if (!active || state.overIndex < 0) return;
      moveIndex(index, state.overIndex, state.after);
    };
    window.addEventListener('pointermove', onMove, {passive: false});
    window.addEventListener('pointerup', finish);
    window.addEventListener('pointercancel', finish);
  };

  const allLeftShownPicked = left.shown.length > 0 && left.shown.every((item) => pickedLeft.includes(item.id));
  const allRightShownPicked = rightRows.length > 0 && rightRows.every((row) => pickedRight.includes(rowToken(row.id, row.index)));
  const groupPickState = (items: Provider[], picked: string[]): GroupPickState => {
    if (items.length === 0) return 'none';
    const count = items.filter((item) => picked.includes(item.id)).length;
    return count === 0 ? 'none' : count === items.length ? 'all' : 'some';
  };

  React.useEffect(() => {
    if (groupKey && !allGroups.some((group) => group.key === groupKey)) setGroupKey('');
  }, [groupKey, allGroups]);

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
          placeholder="搜索名称、手机号、分组或 ID（空格分词）"
          aria-label="搜索 Provider"
          onChange={(event) => setQuery(event.target.value)}
        />
        {query ? (
          <button type="button" className="fpp-search-clear" title="清空搜索词" onClick={() => setQuery('')}>×</button>
        ) : null}
      </div>
      <ProviderGroupChips groups={allGroups} value={groupKey} onChange={setGroupKey} />

      <div className="fpp-cols">
        <section className="fpp-col" aria-label="可选 Provider">
          <header className="fpp-col-head">
            <span className="fpp-col-title">可选 Provider</span>
            <span className="fpp-col-count">{providers.length}</span>
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
              <div className="fpp-empty">{providers.length === 0 ? '还没有 Provider' : '无匹配项'}</div>
            ) : left.sections.map((group) => {
              const groupIds = group.items.map((item) => item.id);
              return (
                <div className="fpp-group" key={group.key}>
                  <ProviderGroupHeader
                    title={group.title}
                    count={group.items.length}
                    icon={group.icon}
                    picked={groupPickState(group.items, pickedLeft)}
                    hint="勾选整组后，再点下方「加入备选」"
                    onToggle={(checked) => setPickedLeft((current) => selectionToggleGroup(current, groupIds, checked))}
                  />
                  {group.items.map((provider) => {
                    const picked = pickedLeft.includes(provider.id);
                    const used = countById[provider.id] || 0;
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
                          aria-label={`勾选 ${provider.name}${providerAccountDetail(provider) ? ` ${providerAccountDetail(provider)}` : ''}`}
                          onChange={() => setPickedLeft((current) => (picked ? current.filter((id) => id !== provider.id) : [...current, provider.id]))}
                        />
                        <button type="button" className="fpp-row-main" onClick={() => add([provider.id])}>
                          <ProviderRowFace provider={provider} />
                        </button>
                        {used > 0 ? <span className="fpp-flag" title="已在链路中出现的次数">已选 {used} 次</span> : null}
                        <button type="button" className="fpp-row-act add" title="再加入一行" onClick={() => add([provider.id])}>›</button>
                      </div>
                    );
                  })}
                </div>
              );
            })}
          </div>
          <footer className="fpp-col-foot">
            <span className="fpp-hint">
              {pickedLeft.length > 0 ? `已勾选 ${pickedLeft.length} 项` : '单击加入；同一 Provider 可重复加入'}
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
            <span className="fpp-col-title">链路行</span>
            <span className="fpp-col-count">{selectedIds.length}</span>
            <button
              type="button"
              className="fpp-link"
              disabled={rightRows.length === 0}
              onClick={() => (allRightShownPicked ? setPickedRight([]) : setPickedRight(rightRows.map((row) => rowToken(row.id, row.index))))}
            >
              {allRightShownPicked ? '取消全选' : '全选'}
            </button>
          </header>
          <div className="fpp-list" role="listbox" aria-multiselectable="true">
            {rightRows.length === 0 ? (
              <div className="fpp-empty">{selectedIds.length === 0 ? '还没有备选：首选额度耗尽后不会自动换到别的 Provider' : '无匹配项'}</div>
            ) : rightRows.map((row) => {
              const provider = row.provider!;
              const token = rowToken(row.id, row.index);
              const picked = pickedRight.includes(token);
              const crossProtocol = !sameProtocol(provider);
              const dragging = drag?.active && drag.index === row.index;
              const overHere = drag?.active && drag.overIndex === row.index;
              return (
                <div
                  key={token}
                  className={[
                    'fpp-row',
                    'chain',
                    picked ? ' picked' : '',
                    justMoved[row.id] ? ' arrived' : '',
                    crossProtocol ? ' cross' : '',
                    dragging ? ' dragging' : '',
                    overHere ? (drag.after ? ' drop-after' : ' drop-before') : '',
                  ].filter(Boolean).join(' ')}
                  data-provider-id={row.id}
                  data-row-index={row.index}
                  onPointerDown={(event) => beginDrag(event, row.index)}
                >
                  <input
                    type="checkbox"
                    className="fpp-check"
                    checked={picked}
                    aria-label={`勾选第 ${row.index + 1} 行 ${provider.name}`}
                    onChange={() => setPickedRight((current) => (picked ? current.filter((item) => item !== token) : [...current, token]))}
                  />
                  <span className="fpp-rank" title="固定行号：provider_index 模式下客户端用 pN 选择">#{row.index + 1}</span>
                  <ProviderRowFace provider={provider} />
                  {crossProtocol ? <span className="fpp-flag" title="网关会自动做协议转换；固定模型是可选项">跨协议自动转换</span> : null}
                  <span className="fpp-order">
                    <button type="button" className="fpp-order-btn" title="上移" disabled={row.index <= 0} onClick={() => moveIndex(row.index, row.index - 1, false)}>↑</button>
                    <button type="button" className="fpp-order-btn" title="下移" disabled={row.index >= selectedIds.length - 1} onClick={() => moveIndex(row.index, row.index + 1, true)}>↓</button>
                  </span>
                  <span className="fpp-row-actions">
                    <button type="button" className="fpp-row-act" title="移出这一行" onClick={() => removeIndexes([row.index])} aria-label={`移出第 ${row.index + 1} 行 ${provider.name}`}>‹</button>
                  </span>
                </div>
              );
            })}
          </div>
          <footer className="fpp-col-foot">
            <span className="fpp-hint">
              {pickedRight.length > 0 ? `已勾选 ${pickedRight.length} 行` : '拖动行可调整顺序；同一 Provider 可重复出现'}
            </span>
            <button
              type="button"
              className="fpp-batch danger"
              disabled={pickedRight.length === 0}
              onClick={() => {
                const indexes = pickedRight
                  .map((token) => Number.parseInt(token.split('\u0000').pop() || '', 10))
                  .filter(Number.isFinite);
                removeIndexes(indexes);
              }}
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
