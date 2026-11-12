import {useEffect, useMemo, useState} from 'react';
import type {ProviderGroup} from '../types';
import {confirmDialog} from '../dialogs';
import {Modal} from './ui';

/**
 * Provider 分组管理只对管理员开放。分组是纯归类维度：删除分组只会把 Provider
 * 移回「未分组」，不会删除 Provider，也不会改变用户在用户管理里的授权。
 */
export function ProviderGroupsDialog({
  open,
  groups,
  providerCounts,
  busy,
  onClose,
  onCreate,
  onRename,
  onDelete,
  onMove,
}: {
  open: boolean;
  groups: ProviderGroup[];
  providerCounts: Record<string, number>;
  busy: boolean;
  onClose: () => void;
  onCreate: (name: string) => Promise<void>;
  onRename: (groupID: string, name: string) => Promise<void>;
  onDelete: (groupID: string) => Promise<void>;
  onMove: (groupID: string, direction: -1 | 1) => Promise<void>;
}) {
  const [newName, setNewName] = useState('');
  const [names, setNames] = useState<Record<string, string>>({});
  const ordered = useMemo(() => [...groups].sort((a, b) => a.position - b.position), [groups]);

  // 外部刷新分组后同步编辑框；保留用户尚未提交的改名草稿会导致删组/重排时串行号。
  useEffect(() => {
    const next: Record<string, string> = {};
    for (const group of ordered) next[group.id] = group.name;
    setNames(next);
  }, [ordered]);

  if (!open) return null;

  // 创建分组后清空输入；父组件负责刷新 GatewayState。
  const create = async () => {
    const name = newName.trim();
    if (!name || busy) return;
    await onCreate(name);
    setNewName('');
  };

  // 只有名称真的变化时才提交，避免无意义的保存请求。
  const rename = async (group: ProviderGroup) => {
    const name = (names[group.id] || '').trim();
    if (!name || busy || name === group.name) return;
    await onRename(group.id, name);
  };

  // 删组前明确告知组内 Provider 会回到未分组，但不会被删除。
  const remove = async (group: ProviderGroup) => {
    if (busy) return;
    const count = providerCounts[group.id] || 0;
    const suffix = count > 0 ? `\n\n组内 ${count} 个 Provider 会变成「未分组」，不会被删除，也不影响用户授权。` : '';
    if (!(await confirmDialog(`确定删除 Provider 分组「${group.name}」？${suffix}`, {danger: true, confirmLabel: '删除分组'}))) return;
    await onDelete(group.id);
  };

  return (
    <Modal
      title="Provider 分组"
      description="只给管理员归类、筛选和批量操作用。分组不会改变 Provider 权限；给普通用户使用 Provider 仍然去用户管理里分配。"
      onClose={onClose}
    >
      <form
        className="provider-group-create"
        onSubmit={(event) => {
          event.preventDefault();
          void create();
        }}
      >
        <input
          value={newName}
          placeholder="新分组名称，例如 OpenAI、Claude、WorkBuddy"
          disabled={busy}
          onChange={(event) => setNewName(event.target.value)}
        />
        <button className="btn primary" type="submit" disabled={busy || !newName.trim()}>创建分组</button>
      </form>

      <div className="provider-group-manage-list">
        {ordered.length === 0 ? (
          <div className="empty-state">暂无分组。创建后可以把 Provider 批量移入。</div>
        ) : ordered.map((group, index) => {
          const count = providerCounts[group.id] || 0;
          const changed = (names[group.id] || '').trim() !== group.name;
          return (
            <div className="provider-group-manage-row" key={group.id}>
              <span className="provider-group-manage-index">{String(index + 1).padStart(2, '0')}</span>
              <input
                value={names[group.id] ?? ''}
                disabled={busy}
                onChange={(event) => setNames((current) => ({...current, [group.id]: event.target.value}))}
                onKeyDown={(event) => {
                  if (event.key === 'Enter') {
                    event.preventDefault();
                    void rename(group);
                  }
                }}
              />
              <span className="provider-group-manage-count">{count} 个 Provider</span>
              <span className="provider-group-manage-actions">
                <button className="mini-btn" type="button" disabled={busy || index === 0} onClick={() => void onMove(group.id, -1)} title="上移分组">上移</button>
                <button className="mini-btn" type="button" disabled={busy || index === ordered.length - 1} onClick={() => void onMove(group.id, 1)} title="下移分组">下移</button>
                <button className="mini-btn" type="button" disabled={busy || !changed} onClick={() => void rename(group)}>保存名称</button>
                <button className="mini-btn danger" type="button" disabled={busy} onClick={() => void remove(group)}>删除</button>
              </span>
            </div>
          );
        })}
      </div>

      <div className="provider-group-manage-hint">
        删除分组不会删除 Provider；组内 Provider 会自动回到「未分组」。
      </div>
    </Modal>
  );
}
