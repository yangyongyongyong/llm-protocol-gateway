import React from 'react';
import {BrandIcon} from './ui';

export type GroupPickState = 'none' | 'some' | 'all';

/** 分组行：整组勾选/取消，部分命中时显示 indeterminate。 */
export function ProviderGroupHeader({
  title,
  count,
  picked,
  onToggle,
  icon,
  hint,
}: {
  title: string;
  count: number;
  picked: GroupPickState;
  onToggle: (checked: boolean) => void;
  icon?: string;
  hint?: string;
}) {
  const ref = React.useRef<HTMLInputElement | null>(null);
  React.useEffect(() => {
    if (ref.current) ref.current.indeterminate = picked === 'some';
  }, [picked]);
  return (
    <label className={`provider-group-check${picked === 'all' ? ' all' : picked === 'some' ? ' some' : ''}`} title={hint || `整组${picked === 'all' ? '取消' : '勾选'}：${title}`}>
      <input
        ref={ref}
        type="checkbox"
        checked={picked === 'all'}
        onChange={(event) => onToggle(event.target.checked)}
      />
      {icon ? <BrandIcon icon={icon} name={title} size={13} className="filter-option-icon" /> : null}
      <span className="provider-group-check-title">{title}</span>
      <span className="provider-group-check-count">{count}</span>
    </label>
  );
}
