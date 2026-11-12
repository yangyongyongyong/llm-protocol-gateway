// 授权选择器：把「额外可用的输入 Provider」「网关授权清单」这类"从一堆里挑若干"的操作
// 收成一套统一的双栏组件。
//
// 它替换掉的是原来那个复选框网格（一行三个 checkbox，勾选状态只能靠打勾与否辨认）：
//   · 可选和已选混在同一片网格里，"我到底给了几项"要靠数勾；
//   · 没有"移出"的独立动作，取消勾选和"从未授权"看起来一模一样；
//   · 用户管理页与网关授权页各写了一遍同样的网格，两处的间距、分栏、按钮措辞还不一样。
//
// 现在统一成与「配置备选 Provider」同一套语言：左边候选、右边已选、单击挪过去、
// 勾选可批量、栏头显示计数、两栏各自滚动（长短不一时互不拖累）。
import React, {useCallback, useMemo, useState} from 'react';
import type {GrantTarget} from './rbac_types';

export function ProviderGrantPicker({
  targets,
  granted,
  onChange,
  denied,
  loading = false,
  error = '',
  emptyText = '暂无可授权的输入 Provider。',
}: {
  targets: GrantTarget[];
  granted: string[];
  onChange: (next: string[]) => void;
  /** 已被其它规则让渡、无论如何都不在候选里的 id（目前没有用到，留作扩展）。 */
  denied?: string[];
  loading?: boolean;
  error?: string;
  emptyText?: string;
}) {
  const [query, setQuery] = useState('');
  const [pickedLeft, setPickedLeft] = useState<string[]>([]);
  const [pickedRight, setPickedRight] = useState<string[]>([]);

  const grantedSet = useMemo(() => new Set(granted), [granted]);
  const deniedSet = useMemo(() => new Set(denied || []), [denied]);

  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase();
    const list = targets.filter((target) => !deniedSet.has(target.id));
    if (!needle) return list;
    // 空格分词、全部命中：与备选选择器同一套搜索口径，避免两处手感不同。
    const words = needle.split(/\s+/).filter(Boolean);
    return list.filter((target) => {
      const haystack = `${target.name} ${target.ownerName || ''}`.toLowerCase();
      return words.every((word) => haystack.includes(word));
    });
  }, [targets, query, deniedSet]);

  const left = useMemo(() => visible.filter((target) => !grantedSet.has(target.id)), [visible, grantedSet]);
  // 右侧按「授权的先后顺序」排，并跟随搜索过滤：授权是有先后的意图（先给谁用），
  // 按 targets 的目录顺序重排会把这个意图抹掉。
  const right = useMemo(() => {
    const byId = new Map(visible.map((target) => [target.id, target]));
    return granted
      .map((id) => byId.get(id))
      .filter((target): target is GrantTarget => Boolean(target));
  }, [visible, granted]);

  const add = useCallback((ids: string[]) => {
    if (ids.length === 0) return;
    const next = [...granted];
    for (const id of ids) if (!next.includes(id)) next.push(id);
    onChange(next);
    setPickedLeft((current) => current.filter((id) => !ids.includes(id)));
  }, [granted, onChange]);

  const remove = useCallback((ids: string[]) => {
    if (ids.length === 0) return;
    onChange(granted.filter((id) => !ids.includes(id)));
    setPickedRight((current) => current.filter((id) => !ids.includes(id)));
  }, [granted, onChange]);

  const allLeftPicked = left.length > 0 && left.every((target) => pickedLeft.includes(target.id));
  const allRightPicked = right.length > 0 && right.every((target) => pickedRight.includes(target.id));

  if (loading) return <div className="empty-state compact">加载中…</div>;
  if (error) return <div className="rbac-form-error">{error}</div>;
  if (targets.length === 0) return <div className="empty-state compact">{emptyText}</div>;

  const row = (target: GrantTarget, picked: boolean, side: 'left' | 'right') => {
    const setPicked = side === 'left' ? setPickedLeft : setPickedRight;
    const act = side === 'left' ? () => add([target.id]) : () => remove([target.id]);
    return (
      <div key={target.id} className={`fpp-row${picked ? ' picked' : ''}`} data-provider-id={target.id}>
        <input
          type="checkbox"
          className="fpp-check"
          checked={picked}
          aria-label={`勾选 ${target.name}`}
          onChange={() => setPicked((current) => (picked ? current.filter((id) => id !== target.id) : [...current, target.id]))}
        />
        <button type="button" className="fpp-row-main" onClick={act} title={side === 'left' ? '授权这一项' : '收回这一项'}>
          <span className="fpp-face">
            <span className="fpp-face-text">
              <span className="fpp-face-name" title={target.name}>{target.name}</span>
              {target.ownerName ? <span className="fpp-face-account" title={`归属：${target.ownerName}`}>归属 {target.ownerName}</span> : null}
            </span>
          </span>
        </button>
        <button
          type="button"
          className={`fpp-row-act${side === 'left' ? ' add' : ''}`}
          title={side === 'left' ? '授权' : '收回'}
          aria-label={side === 'left' ? '授权' : '收回'}
          onClick={act}
        >
          {side === 'left' ? '›' : '‹'}
        </button>
      </div>
    );
  };

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
          name="grantProviderQuery"
          placeholder="搜索名称或归属账号（空格分词）"
          aria-label="搜索可授权的输入 Provider"
          onChange={(event) => setQuery(event.target.value)}
        />
        {query ? (
          <button type="button" className="fpp-search-clear" title="清空搜索词" onClick={() => setQuery('')}>×</button>
        ) : null}
      </div>

      <div className="fpp-cols fpp-cols-2">
        <section className="fpp-col" aria-label="可选 Provider">
          <header className="fpp-col-head">
            <span className="fpp-col-title">可选 Provider</span>
            <span className="fpp-col-count">{left.length}</span>
            <button
              type="button"
              className="fpp-link"
              disabled={left.length === 0}
              onClick={() => setPickedLeft(allLeftPicked ? [] : left.map((target) => target.id))}
            >
              {allLeftPicked ? '取消全选' : '全选'}
            </button>
          </header>
          <div className="fpp-list" role="listbox" aria-multiselectable="true">
            {left.length === 0 ? (
              <div className="fpp-empty">{query.trim() ? '无匹配项' : '全部 Provider 都已授权'}</div>
            ) : left.map((target) => row(target, pickedLeft.includes(target.id), 'left'))}
          </div>
          <footer className="fpp-col-foot">
            <span className="fpp-hint">{pickedLeft.length > 0 ? `已勾选 ${pickedLeft.length} 项` : '单击即可授权；勾选后批量授权'}</span>
            <button type="button" className="fpp-batch" disabled={pickedLeft.length === 0} onClick={() => add(pickedLeft)}>
              授权 {pickedLeft.length > 0 ? `(${pickedLeft.length})` : ''}
            </button>
          </footer>
        </section>

        <section className="fpp-col" aria-label="已授权">
          <header className="fpp-col-head">
            <span className="fpp-col-title">已授权</span>
            <span className="fpp-col-count">{right.length}</span>
            <button
              type="button"
              className="fpp-link"
              disabled={right.length === 0}
              onClick={() => setPickedRight(allRightPicked ? [] : right.map((target) => target.id))}
            >
              {allRightPicked ? '取消全选' : '全选'}
            </button>
          </header>
          <div className="fpp-list" role="listbox" aria-multiselectable="true">
            {right.length === 0 ? (
              <div className="fpp-empty">还没有授权任何 Provider</div>
            ) : right.map((target) => row(target, pickedRight.includes(target.id), 'right'))}
          </div>
          <footer className="fpp-col-foot">
            <span className="fpp-hint">{pickedRight.length > 0 ? `已勾选 ${pickedRight.length} 项` : '单击即可收回；勾选后批量收回'}</span>
            <button type="button" className="fpp-batch danger" disabled={pickedRight.length === 0} onClick={() => remove(pickedRight)}>
              收回 {pickedRight.length > 0 ? `(${pickedRight.length})` : ''}
            </button>
          </footer>
        </section>
      </div>
    </div>
  );
}
