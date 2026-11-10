import React, {useCallback, useEffect, useMemo, useState} from 'react';
import {API_BASE} from '../lib';
import {Avatar, AvatarUploader} from './avatar';
import {Badge, Modal, Switch} from './ui';
import {confirmDialog} from '../dialogs';
import {scopeLabel} from './roles_page';
import type {ConsoleUser, GrantTarget, RoleOption, UserDraft} from './rbac_types';

async function errorTextFrom(response: Response): Promise<string> {
  try {
    const raw = (await response.text()).trim();
    if (!raw) return `HTTP ${response.status}`;
    try {
      const parsed = JSON.parse(raw) as { error?: string; message?: string };
      const message = parsed.error || parsed.message;
      if (message) return message;
    } catch {
      // 非 JSON：原样返回
    }
    return raw;
  } catch {
    return `HTTP ${response.status}`;
  }
}

async function jsonRequest<T>(url: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(url, { credentials: 'same-origin', ...init });
  if (!response.ok) throw new Error(await errorTextFrom(response));
  if (response.status === 204) return undefined as T;
  const text = await response.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

function scopeTone(scope: string | undefined): 'purple' | 'amber' | 'blue' | 'slate' {
  if (scope === 'all') return 'purple';
  if (scope === 'granted') return 'amber';
  if (scope === 'own') return 'blue';
  return 'slate';
}

function formatTime(value: string | undefined): string {
  if (!value) return '—';
  const ts = Date.parse(value);
  if (Number.isNaN(ts)) return '—';
  const date = new Date(ts);
  const diff = Date.now() - ts;
  if (diff >= 0 && diff < 60_000) return '刚刚';
  if (diff >= 0 && diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
  if (diff >= 0 && diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`;
  return date.toLocaleString();
}

export function emptyUserDraft(roleId: string): UserDraft {
  return {
    id: null,
    username: '',
    displayName: '',
    password: '',
    roleId,
    enabled: true,
    avatarUrl: '',
    grantedProviderIds: [],
  };
}

export function UsersPage({ canManage, canView, onToast, onChanged, roleOptions, refreshRoleOptions }: {
  canManage: boolean;
  canView: boolean;
  onToast: (text: string) => void;
  /** 用户增删改后通知外层刷新（部分页面按 ownerUserId 展示用户名 / 统计）。 */
  onChanged: (deletedUser?: ConsoleUser) => Promise<void> | void;
  roleOptions: RoleOption[];
  refreshRoleOptions: () => Promise<void>;
}) {
  const [users, setUsers] = useState<ConsoleUser[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [keyword, setKeyword] = useState('');
  const [roleFilter, setRoleFilter] = useState('');
  const [enabledFilter, setEnabledFilter] = useState<'all' | 'on' | 'off'>('all');
  const [draft, setDraft] = useState<UserDraft | null>(null);
  const [resetUser, setResetUser] = useState<ConsoleUser | null>(null);
  const [busyId, setBusyId] = useState('');

  const refresh = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      setUsers(await jsonRequest<ConsoleUser[]>(`${API_BASE}/__users`) || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (canView) void refresh();
  }, [refresh, canView]);

  // 角色筛选按 roleId；老账号没有 roleId 时退回 role（内置角色 id 与 role 同名）。
  const roleKey = (user: ConsoleUser) => user.roleId || user.role || '';

  const filtered = useMemo(() => {
    const needle = keyword.trim().toLowerCase();
    return users.filter((user) => {
      if (roleFilter && roleKey(user) !== roleFilter) return false;
      if (enabledFilter === 'on' && !user.enabled) return false;
      if (enabledFilter === 'off' && user.enabled) return false;
      if (!needle) return true;
      return [user.username, user.displayName, user.roleName]
        .filter(Boolean)
        .join(' ')
        .toLowerCase()
        .includes(needle);
    });
  }, [users, keyword, roleFilter, enabledFilter]);

  async function toggleEnabled(user: ConsoleUser) {
    setBusyId(user.id);
    try {
      await jsonRequest(`${API_BASE}/__users/${encodeURIComponent(user.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled: !user.enabled }),
      });
      await refresh();
      await onChanged();
      onToast(user.enabled ? '用户已禁用' : '用户已启用');
    } catch (err) {
      onToast(`操作失败：${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setBusyId('');
    }
  }

  async function deleteUser(user: ConsoleUser) {
    if (!(await confirmDialog(`确定删除用户「${user.displayName || user.username}」？其名下 Key 将归还管理员，该操作不可撤销。`, {danger: true, confirmLabel: '删除'}))) return;
    setBusyId(user.id);
    try {
      await jsonRequest(`${API_BASE}/__users/${encodeURIComponent(user.id)}`, { method: 'DELETE' });
      await refresh();
      await onChanged(user);
      onToast('用户已删除');
    } catch (err) {
      onToast(`删除失败：${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setBusyId('');
    }
  }

  function editUser(user: ConsoleUser) {
    setDraft({
      id: user.id,
      username: user.username,
      displayName: user.displayName || '',
      password: '',
      roleId: roleKey(user),
      enabled: user.enabled,
      avatarUrl: user.avatarUrl || '',
      grantedProviderIds: [...(user.grantedProviderIds || [])],
    });
  }

  const defaultRoleId = roleOptions.find((role) => !role.builtin)?.id || roleOptions.find((role) => role.id === 'user')?.id || roleOptions[0]?.id || '';
  const hasFilter = Boolean(keyword.trim() || roleFilter || enabledFilter !== 'all');

  return (
    <section className="section-full">
      <div className="card panel">
        <div className="panel-header">
          <div>
            <h2 className="panel-title">用户管理</h2>
            <p className="panel-desc">
              控制台账号与角色分配。角色决定成员可进入的页面与可操作的资源；点用户卡上的「分配 Provider」可以额外开放输入 Provider，叠加在自己的资源之上。角色变更、禁用、删除都立即生效。
            </p>
          </div>
          <div className="panel-header-actions">
            {canManage ? (
              <button className="btn primary" type="button" onClick={() => setDraft(emptyUserDraft(defaultRoleId))}>新建用户</button>
            ) : null}
          </div>
        </div>

        <div className="rbac-toolbar">
          <div className="rbac-search">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <circle cx="11" cy="11" r="8" />
              <line x1="21" y1="21" x2="16.65" y2="16.65" />
            </svg>
            <input
              type="search"
              value={keyword}
              placeholder="搜索名称或账号…"
              aria-label="搜索用户"
              autoComplete="off"
              onChange={(event) => setKeyword(event.target.value)}
            />
            {keyword ? (
              <button type="button" className="rbac-search-clear" title="清空搜索" onClick={() => setKeyword('')}>×</button>
            ) : null}
          </div>
          <select className="rbac-filter" value={roleFilter} aria-label="按角色筛选" onChange={(event) => setRoleFilter(event.target.value)}>
            <option value="">全部角色</option>
            {roleOptions.map((role) => <option key={role.id} value={role.id}>{role.name}</option>)}
          </select>
          <select
            className="rbac-filter"
            value={enabledFilter}
            aria-label="按状态筛选"
            onChange={(event) => setEnabledFilter(event.target.value as 'all' | 'on' | 'off')}
          >
            <option value="all">全部状态</option>
            <option value="on">仅启用</option>
            <option value="off">仅禁用</option>
          </select>
          <span className="rbac-toolbar-count">{filtered.length} / {users.length}</span>
        </div>

        {error ? <div className="rbac-form-error">{error}</div> : null}
        {loading && users.length === 0 ? <div className="empty-state">加载中…</div> : null}
        {!loading && users.length === 0 && !error ? (
          <div className="empty-state">
            暂无用户。{canManage ? '点击「新建用户」创建第一个控制台账号。' : ''}
          </div>
        ) : null}
        {!loading && users.length > 0 && filtered.length === 0 ? (
          <div className="empty-state">
            没有符合条件的用户。
            {hasFilter ? (
              <button
                type="button"
                className="mini-btn"
                style={{ marginLeft: 8 }}
                onClick={() => { setKeyword(''); setRoleFilter(''); setEnabledFilter('all'); }}
              >
                清除筛选
              </button>
            ) : null}
          </div>
        ) : null}

        {filtered.length > 0 ? (
          <div className="rbac-user-grid">
            {filtered.map((user) => {
              const display = user.displayName?.trim() || user.username;
              const grantedCount = (user.grantedProviderIds || []).length;
              const lastSeen = user.lastActiveAt || user.lastLoginAt;
              return (
                <div className={`card rbac-user-card${user.enabled ? '' : ' disabled'}`} key={user.id}>
                  <div className="rbac-user-head">
                    <Avatar src={user.avatarUrl} name={display} size={40} />
                    <div className="rbac-user-identity">
                      <b title={display}>{display}</b>
                      <span title={`@${user.username}`}>@{user.username}</span>
                    </div>
                    <Switch
                      checked={user.enabled}
                      disabled={!canManage || busyId === user.id}
                      onChange={() => void toggleEnabled(user)}
                      label={user.enabled ? '已启用' : '已禁用'}
                    />
                  </div>

                  <div className="rbac-user-badges">
                    <Badge tone={user.roleScope === 'all' || user.role === 'admin' ? 'purple' : 'blue'}>
                      {user.roleName || user.role}
                    </Badge>
                    <Badge tone={scopeTone(user.roleScope)}>{scopeLabel(user.roleScope)}</Badge>
                    {user.role !== 'admin' && user.roleScope !== 'all' ? (
                      <button
                        type="button"
                        className={`rbac-grant-chip${grantedCount > 0 ? '' : ' empty'}`}
                        disabled={!canManage}
                        title="点击分配该用户可用的输入 Provider（叠加在自己的 Provider 之上）"
                        onClick={() => editUser(user)}
                      >
                        {grantedCount > 0 ? `可用 Provider ${grantedCount}` : '分配 Provider'}
                      </button>
                    ) : null}
                  </div>

                  <div className="rbac-user-meta">
                    <div>
                      <span>最近登录</span>
                      <b title={user.lastLoginAt ? new Date(user.lastLoginAt).toLocaleString() : '从未登录'}>{formatTime(user.lastLoginAt)}</b>
                    </div>
                    <div>
                      <span>最近活跃</span>
                      <b title={lastSeen ? new Date(lastSeen).toLocaleString() : '从未活跃'}>{lastSeen ? formatTime(lastSeen) : '从未活跃'}</b>
                    </div>
                    <div>
                      <span>创建时间</span>
                      <b title={user.createdAt ? new Date(user.createdAt).toLocaleString() : ''}>{formatTime(user.createdAt)}</b>
                    </div>
                  </div>

                  <div className="rbac-user-actions">
                    <button
                      type="button"
                      className="mini-btn"
                      disabled={!canManage || busyId === user.id}
                      onClick={() => editUser(user)}
                    >
                      编辑
                    </button>
                    <button
                      type="button"
                      className="mini-btn"
                      disabled={!canManage || busyId === user.id}
                      onClick={() => setResetUser(user)}
                    >
                      重置密码
                    </button>
                    <button
                      type="button"
                      className="mini-btn danger"
                      disabled={!canManage || busyId === user.id}
                      onClick={() => void deleteUser(user)}
                    >
                      删除
                    </button>
                  </div>
                </div>
              );
            })}
          </div>
        ) : null}
      </div>

      {draft ? (
        <UserEditorModal
          user={draft}
          roleOptions={roleOptions}
          onClose={() => setDraft(null)}
          onSaved={async (message) => {
            setDraft(null);
            await refresh();
            await refreshRoleOptions();
            await onChanged();
            onToast(message);
          }}
          onToast={onToast}
        />
      ) : null}

      {resetUser ? (
        <PasswordResetModal
          user={resetUser}
          onClose={() => setResetUser(null)}
          onDone={() => {
            setResetUser(null);
            onToast('密码已重置');
          }}
          onToast={onToast}
        />
      ) : null}
    </section>
  );
}

export function UserEditorModal({ user, roleOptions, onClose, onSaved, onToast }: {
  user: UserDraft;
  roleOptions: RoleOption[];
  onClose: () => void;
  onSaved: (message: string) => Promise<void>;
  onToast: (text: string) => void;
}) {
  const [username, setUsername] = useState(user.username);
  const [displayName, setDisplayName] = useState(user.displayName);
  const [password, setPassword] = useState('');
  const [roleId, setRoleId] = useState(user.roleId || roleOptions[0]?.id || '');
  const [enabled, setEnabled] = useState(user.enabled);
  const [avatarUrl, setAvatarUrl] = useState(user.avatarUrl);
  const [granted, setGranted] = useState<string[]>(user.grantedProviderIds);
  const [grantTargets, setGrantTargets] = useState<GrantTarget[]>([]);
  const [grantKeyword, setGrantKeyword] = useState('');
  const [grantLoading, setGrantLoading] = useState(false);
  const [grantError, setGrantError] = useState('');
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState('');

  const creating = !user.id;
  const role = roleOptions.find((item) => item.id === roleId);
  // 管理员本身看得见全部资源，不需要再分配；其余角色都可以额外分配 Provider，
  // 分配只扩大可用范围，不会改角色也不会收回自己已有的资源。
  const canAssignProviders = Boolean(role) && role?.scope !== 'all';

  useEffect(() => {
    if (!canAssignProviders) return;
    let cancelled = false;
    setGrantLoading(true);
    setGrantError('');
    void jsonRequest<GrantTarget[]>(`${API_BASE}/__roles/grant-targets`)
      .then((list) => { if (!cancelled) setGrantTargets(list || []); })
      .catch((err) => { if (!cancelled) setGrantError(err instanceof Error ? err.message : String(err)); })
      .finally(() => { if (!cancelled) setGrantLoading(false); });
    return () => { cancelled = true; };
  }, [canAssignProviders]);

  const grantedSet = useMemo(() => new Set(granted), [granted]);

  // 搜索按名字和归属账号匹配；「全选」只作用于搜出来的这批，
  // 否则一搜一勾就把看不见的几百个也勾上了。
  const visibleGrantTargets = useMemo(() => {
    const word = grantKeyword.trim().toLowerCase();
    if (!word) return grantTargets;
    return grantTargets.filter((target) =>
      target.name.toLowerCase().includes(word) || target.ownerName.toLowerCase().includes(word));
  }, [grantTargets, grantKeyword]);
  const visibleGrantedCount = visibleGrantTargets.filter((target) => grantedSet.has(target.id)).length;
  const allVisibleSelected = visibleGrantTargets.length > 0 && visibleGrantedCount === visibleGrantTargets.length;

  function toggleGrant(id: string, on: boolean) {
    setGranted((current) => on
      ? Array.from(new Set([...current, id]))
      : current.filter((item) => item !== id));
  }

  function toggleAllVisible(on: boolean) {
    const ids = visibleGrantTargets.map((target) => target.id);
    setGranted((current) => (on
      ? Array.from(new Set([...current, ...ids]))
      : current.filter((id) => !ids.includes(id))));
  }

  async function submit() {
    const name = username.trim();
    if (!name) {
      setFormError('用户名不能为空');
      return;
    }
    if (!roleId) {
      setFormError('请选择角色');
      return;
    }
    if (creating && password.trim().length < 8) {
      setFormError('初始密码至少 8 位');
      return;
    }
    setFormError('');
    setBusy(true);
    try {
      if (creating) {
        const body: Record<string, unknown> = {
          username: name,
          password,
          roleId,
          enabled,
          grantedProviderIds: canAssignProviders ? granted : [],
        };
        if (displayName.trim()) body.displayName = displayName.trim();
        if (avatarUrl) body.avatarUrl = avatarUrl;
        await jsonRequest(`${API_BASE}/__users`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
      } else {
        // 只提交发生变化的字段；管理员范围不收不发，避免把已有分配静默清空。
        const patch: Record<string, unknown> = {};
        if (name !== user.username) patch.username = name;
        if (displayName.trim() !== user.displayName) patch.displayName = displayName.trim();
        if (avatarUrl !== user.avatarUrl) patch.avatarUrl = avatarUrl;
        if (roleId !== user.roleId) patch.roleId = roleId;
        if (enabled !== user.enabled) patch.enabled = enabled;
        if (canAssignProviders) {
          const before = [...user.grantedProviderIds].sort().join('\n');
          const after = [...granted].sort().join('\n');
          if (before !== after) patch.grantedProviderIds = granted;
        }
        if (Object.keys(patch).length === 0) {
          await onSaved('没有需要保存的修改');
          return;
        }
        await jsonRequest(`${API_BASE}/__users/${encodeURIComponent(user.id!)}`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(patch),
        });
      }
      await onSaved(creating ? '用户已创建' : '用户已更新');
    } catch (err) {
      onToast(`保存用户失败：${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title={creating ? '新建用户' : '编辑用户'}
      description={creating
        ? '创建控制台账号：设置用户名、初始密码并分配角色。'
        : '修改账号资料、角色与启停状态；角色变更立即生效，无需对方重新登录。'}
      size="wide"
      onClose={onClose}
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <div className="form-grid modal-form">
          <div className="field">
            <label>用户名</label>
            <input value={username} placeholder="登录账号" autoComplete="off" onChange={(event) => setUsername(event.target.value)} />
          </div>
          {creating ? (
            <div className="field">
              <label>初始密码（至少 8 位）</label>
              <input
                type="password"
                autoComplete="new-password"
                value={password}
                placeholder="用户登录后可自行修改"
                onChange={(event) => setPassword(event.target.value)}
              />
            </div>
          ) : (
            <div className="field">
              <label>显示名称</label>
              <input value={displayName} placeholder="留空则显示用户名" onChange={(event) => setDisplayName(event.target.value)} />
            </div>
          )}
          {creating ? (
            <div className="field field-full">
              <label>显示名称</label>
              <input value={displayName} placeholder="留空则显示用户名" onChange={(event) => setDisplayName(event.target.value)} />
            </div>
          ) : null}
          <div className="field">
            <label>角色</label>
            <select value={roleId} onChange={(event) => setRoleId(event.target.value)}>
              {roleOptions.length === 0 ? <option value="">（无可用角色）</option> : null}
              {roleOptions.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.name}{item.builtin ? '（内置）' : '（自定义）'} · {scopeLabel(item.scope)}
                </option>
              ))}
            </select>
            {role?.description ? <div className="hint-text">{role.description}</div> : null}
          </div>
          <div className="field">
            <label>账号状态</label>
            <div className="rbac-switch-row">
              <Switch checked={enabled} onChange={setEnabled} label={enabled ? '启用（可登录）' : '禁用（禁止登录）'} />
            </div>
          </div>
          <div className="field field-full">
            <label>头像</label>
            <AvatarUploader value={avatarUrl} name={displayName || username} onChange={setAvatarUrl} disabled={busy} />
          </div>
        </div>

        {formError ? <div className="rbac-form-error">保存失败：{formError}</div> : null}

        {canAssignProviders ? (
          <div className="rbac-grant-block">
            <div className="rbac-grant-head">
              <b>额外可用的输入 Provider</b>
              <span>已选 {granted.length} / 共 {grantTargets.length}</span>
            </div>
            <p className="hint-text">
              勾选后，该用户除了自己的 Provider，还能使用下面这些；取消勾选立即收回。角色与权限不变。
            </p>
            {grantLoading ? <div className="empty-state compact">加载中…</div> : null}
            {grantError ? <div className="rbac-form-error">{grantError}</div> : null}
            {!grantLoading && !grantError && grantTargets.length === 0 ? (
              <div className="empty-state compact">暂无可授权的输入 Provider。</div>
            ) : null}
            {grantTargets.length > 0 ? (
              <>
                <div className="rbac-grant-toolbar">
                  <div className="rbac-search">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                      <circle cx="11" cy="11" r="8" />
                      <line x1="21" y1="21" x2="16.65" y2="16.65" />
                    </svg>
                    <input
                      type="search"
                      value={grantKeyword}
                      placeholder="搜索名称或归属账号…"
                      aria-label="搜索可授权的输入 Provider"
                      autoComplete="off"
                      onChange={(event) => setGrantKeyword(event.target.value)}
                    />
                    {grantKeyword ? (
                      <button type="button" className="rbac-search-clear" title="清空搜索" onClick={() => setGrantKeyword('')}>×</button>
                    ) : null}
                  </div>
                  <button
                    type="button"
                    className="btn secondary rbac-grant-all"
                    disabled={visibleGrantTargets.length === 0}
                    title={grantKeyword.trim() ? '只作用于当前搜索到的结果' : '勾选全部可授权的 Provider'}
                    onClick={() => toggleAllVisible(!allVisibleSelected)}
                  >
                    {allVisibleSelected ? '取消全选' : '全选'}
                    {grantKeyword.trim() ? `（${visibleGrantTargets.length}）` : ''}
                  </button>
                </div>
                {visibleGrantTargets.length === 0 ? (
                  <div className="empty-state compact">没有匹配「{grantKeyword.trim()}」的 Provider。</div>
                ) : (
                  <div className="rbac-grant-list">
                    {visibleGrantTargets.map((target) => {
                      const checked = grantedSet.has(target.id);
                      return (
                        <button
                          key={target.id}
                          type="button"
                          role="checkbox"
                          aria-checked={checked}
                          className={`rbac-grant-item${checked ? ' checked' : ''}`}
                          onClick={() => toggleGrant(target.id, !checked)}
                        >
                          <span className={`provider-checklist-box${checked ? ' on' : ''}`} aria-hidden="true">{checked ? '✓' : ''}</span>
                          <span className="rbac-grant-name" title={target.name}>{target.name}</span>
                          <span className="rbac-grant-owner" title={`归属：${target.ownerName}`}>归属 {target.ownerName}</span>
                        </button>
                      );
                    })}
                  </div>
                )}
              </>
            ) : null}
          </div>
        ) : null}

        <div className="actions modal-actions">
          <button type="button" className="btn" disabled={busy} onClick={onClose}>取消</button>
          <button type="submit" className="btn primary" disabled={busy || roleOptions.length === 0}>
            {busy ? '保存中…' : creating ? '创建用户' : '保存修改'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function PasswordResetModal({ user, onClose, onDone, onToast }: {
  user: ConsoleUser;
  onClose: () => void;
  onDone: () => void;
  onToast: (text: string) => void;
}) {
  const [value, setValue] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (value.trim().length < 8) {
      onToast('密码至少 8 位');
      return;
    }
    setBusy(true);
    try {
      await jsonRequest(`${API_BASE}/__users/${encodeURIComponent(user.id)}/reset-password`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: value.trim() }),
      });
      onDone();
    } catch (err) {
      onToast(`重置失败：${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title="重置用户密码"
      description={`为用户「${user.displayName || user.username}」设置新密码，至少 8 位。该用户当前会话不受影响，新密码立即生效。`}
      onClose={onClose}
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <div className="form-grid modal-form">
          <div className="field field-full">
            <label>新密码（至少 8 位）</label>
            <input
              type="text"
              autoComplete="new-password"
              value={value}
              disabled={busy}
              placeholder="至少 8 位"
              onChange={(event) => setValue(event.target.value)}
            />
          </div>
        </div>
        <div className="actions modal-actions">
          <button type="button" className="btn" disabled={busy} onClick={onClose}>取消</button>
          <button type="submit" className="btn primary" disabled={busy || value.trim().length < 8}>{busy ? '重置中…' : '重置密码'}</button>
        </div>
      </form>
    </Modal>
  );
}

/**
 * 个人资料弹窗（侧栏用户卡片入口）：修改自己的显示名称与头像。
 * 走 PATCH /__profile，成功后外层刷新 auth 状态让头像/名字立刻反映在页面上。
 */
export function ProfileModal({ currentName, currentDisplayName, currentAvatar, onClose, onSaved, onToast }: {
  currentName: string;
  currentDisplayName?: string;
  currentAvatar?: string;
  onClose: () => void;
  onSaved: (displayName: string, avatarUrl: string) => Promise<void> | void;
  onToast: (text: string) => void;
}) {
  const [displayName, setDisplayName] = useState(currentDisplayName || '');
  const [avatarUrl, setAvatarUrl] = useState(currentAvatar || '');
  const [busy, setBusy] = useState(false);

  async function submit() {
    setBusy(true);
    try {
      const response = await fetch(`${API_BASE}/__profile`, {
        method: 'PATCH',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ displayName: displayName.trim(), avatarUrl }),
      });
      if (!response.ok) throw new Error(await errorTextFrom(response));
      await onSaved(displayName.trim(), avatarUrl);
      onToast('个人资料已更新');
    } catch (err) {
      onToast(`保存失败：${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title="个人资料"
      description="修改自己的显示名称与头像，只影响控制台里的展示，不影响登录账号。"
      onClose={onClose}
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <div className="form-grid modal-form">
          <div className="field field-full">
            <label>登录账号</label>
            <div className="static-field">{currentName}</div>
          </div>
          <div className="field field-full">
            <label>显示名称</label>
            <input
              value={displayName}
              placeholder={`留空则显示 ${currentName}`}
              onChange={(event) => setDisplayName(event.target.value)}
            />
          </div>
          <div className="field field-full">
            <label>头像</label>
            <AvatarUploader value={avatarUrl} name={displayName || currentName} onChange={setAvatarUrl} disabled={busy} />
          </div>
        </div>
        <div className="actions modal-actions">
          <button type="button" className="btn" disabled={busy} onClick={onClose}>取消</button>
          <button type="submit" className="btn primary" disabled={busy}>{busy ? '保存中…' : '保存'}</button>
        </div>
      </form>
    </Modal>
  );
}
