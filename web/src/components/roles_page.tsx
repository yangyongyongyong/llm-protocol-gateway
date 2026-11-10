import React, {useCallback, useEffect, useMemo, useState} from 'react';
import {API_BASE} from '../lib';
import {Badge, Modal} from './ui';
import {confirmDialog} from '../dialogs';
import type {Role, RoleCatalog, RoleDraft, RoleOption, RoleScope} from './rbac_types';

/** 错误文案：后端的 409 / 400 都是纯文本里带一句中文原因，直接透出给用户看。 */
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

export function scopeLabel(scope: RoleScope | undefined, catalog?: RoleCatalog | null): string {
  return catalog?.scopes.find((item) => item.id === scope)?.label
    || ({ own: '自己的资源', granted: '被授权资源', all: '全部资源' } as Record<string, string>)[scope || 'own']
    || scope
    || '';
}

function scopeTone(scope: RoleScope | undefined): 'purple' | 'amber' | 'blue' {
  if (scope === 'all') return 'purple';
  if (scope === 'granted') return 'amber';
  return 'blue';
}

export function emptyRoleDraft(): RoleDraft {
  return { id: null, name: '', description: '', scope: 'own', permissions: [] };
}

/** 角色管理页的数据加载 + 增删改。页面块与其它弹窗共用，单独收成 hook。 */
export function useRoles(enabled = true) {
  const [roles, setRoles] = useState<Role[]>([]);
  const [options, setOptions] = useState<RoleOption[]>([]);
  const [catalog, setCatalog] = useState<RoleCatalog | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const refresh = useCallback(async () => {
    if (!enabled) return;
    setLoading(true);
    setError('');
    try {
      const [roleList, roleOptions, roleCatalog] = await Promise.all([
        jsonRequest<Role[]>(`${API_BASE}/__roles`),
        jsonRequest<RoleOption[]>(`${API_BASE}/__roles/options`),
        jsonRequest<RoleCatalog>(`${API_BASE}/__roles/catalog`),
      ]);
      setRoles(roleList || []);
      setOptions(roleOptions || []);
      setCatalog(roleCatalog || null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, [enabled]);

  // 首次挂载即拉取；角色目录很少变，后续靠操作后的 refresh 更新。
  useEffect(() => {
    void refresh();
  }, [refresh]);

  return { roles, options, catalog, loading, error, refresh };
}

export function RolesPage({ model, canManage, onToast }: {
  model: ReturnType<typeof useRoles>;
  canManage: boolean;
  onToast: (text: string) => void;
}) {
  const { roles, catalog, loading, error, refresh } = model;
  const [draft, setDraft] = useState<RoleDraft | null>(null);
  const [busyId, setBusyId] = useState('');

  // 权限数按目录里的 key 计数，目录里已下线的 key 不计入（避免数字虚高）。
  const catalogKeys = useMemo(() => {
    const keys = new Set<string>();
    (catalog?.modules || []).forEach((module) => module.permissions.forEach((permission) => keys.add(permission.key)));
    return keys;
  }, [catalog]);

  function countPermissions(role: Role): number {
    if (!catalogKeys.size) return role.permissions.length;
    return role.permissions.filter((key) => catalogKeys.has(key)).length;
  }

  async function copyRole(role: Role) {
    setBusyId(role.id);
    try {
      await jsonRequest(`${API_BASE}/__roles/${encodeURIComponent(role.id)}/copy`, { method: 'POST' });
      await refresh();
      onToast(`已复制角色「${role.name}」`);
    } catch (err) {
      onToast(`复制失败：${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setBusyId('');
    }
  }

  async function deleteRole(role: Role) {
    if (!(await confirmDialog(`确定删除角色「${role.name}」？该操作不可撤销。`, {danger: true, confirmLabel: '删除'}))) return;
    setBusyId(role.id);
    try {
      await jsonRequest(`${API_BASE}/__roles/${encodeURIComponent(role.id)}`, { method: 'DELETE' });
      await refresh();
      onToast(`已删除角色「${role.name}」`);
    } catch (err) {
      // 409：仍有用户持有该角色，后端会带上原因，直接透出。
      onToast(`删除失败：${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setBusyId('');
    }
  }

  return (
    <section className="section-full">
      <div className="card panel">
        <div className="panel-header">
          <div>
            <h2 className="panel-title">角色管理</h2>
            <p className="panel-desc">
              角色决定成员能进入哪些页面、能操作哪些资源。内置角色不可修改或删除；需要调整时先「复制」成自定义角色再改。角色变更立即生效，无需对方重新登录。
            </p>
          </div>
          <div className="panel-header-actions">
            {canManage ? (
              <button className="btn primary" type="button" onClick={() => setDraft(emptyRoleDraft())}>新建角色</button>
            ) : null}
          </div>
        </div>

        {error ? <div className="rbac-form-error">{error}</div> : null}
        {loading && roles.length === 0 ? <div className="empty-state">加载中…</div> : null}
        {!loading && !error && roles.length === 0 ? <div className="empty-state">暂无角色。</div> : null}

        {roles.length > 0 ? (
          <div className="rbac-role-grid">
            {roles.map((role) => {
              const count = countPermissions(role);
              // 删不掉时不再渲染成灰色的红色按钮：改成一段不可点的说明，原因直接写在卡片里。
              const deleteBlocked = !canManage || role.builtin || role.userCount > 0;
              const deleteReason = !canManage
                ? '没有角色管理权限'
                : role.builtin
                  ? '内置角色不可删除'
                  : role.userCount > 0
                    ? `仍有 ${role.userCount} 个用户持有该角色`
                    : '';
              return (
                <div className={`card rbac-role-card${role.builtin ? ' builtin' : ''}`} key={role.id}>
                  <div className="rbac-role-card-head">
                    <div className="rbac-role-card-title">
                      <b>{role.name}</b>
                      <Badge tone={role.builtin ? 'slate' : 'blue'}>{role.builtin ? '内置' : '自定义'}</Badge>
                      <Badge tone={scopeTone(role.scope)}>{scopeLabel(role.scope, catalog)}</Badge>
                    </div>
                    <div className="rbac-role-card-id">{role.id}</div>
                  </div>
                  <p className="rbac-role-card-desc">{role.description || '暂无描述'}</p>
                  <div className="rbac-role-card-stats">
                    <div className="rbac-stat">
                      <span>权限</span>
                      <b>{count} 项</b>
                    </div>
                    <div className="rbac-stat">
                      <span>在用人数</span>
                      <b>{role.userCount}</b>
                    </div>
                  </div>
                  <div className="rbac-role-card-actions">
                    <button
                      type="button"
                      className="mini-btn"
                      disabled={!canManage}
                      onClick={() => setDraft({
                        id: role.id,
                        name: role.name,
                        description: role.description,
                        scope: role.scope,
                        permissions: [...role.permissions],
                      })}
                    >
                      {role.builtin ? '查看' : '编辑'}
                    </button>
                    <button
                      type="button"
                      className="mini-btn"
                      disabled={!canManage || busyId === role.id}
                      onClick={() => void copyRole(role)}
                    >
                      复制
                    </button>
                    {deleteBlocked ? (
                      <span className="rbac-role-lock" title={deleteReason}>
                        {role.builtin ? '内置不可删除' : role.userCount > 0 ? `在用 ${role.userCount} 人` : '不可删除'}
                      </span>
                    ) : (
                      <button
                        type="button"
                        className="mini-btn danger"
                        disabled={busyId === role.id}
                        onClick={() => void deleteRole(role)}
                      >
                        删除
                      </button>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        ) : null}
      </div>

      {draft && catalog ? (
        <RoleEditorModal
          role={draft}
          catalog={catalog}
          readOnly={!canManage || Boolean(roles.find((role) => role.id === draft.id)?.builtin)}
          onClose={() => setDraft(null)}
          onSaved={async (message) => {
            setDraft(null);
            await refresh();
            onToast(message);
          }}
          onToast={onToast}
        />
      ) : null}
    </section>
  );
}

function RoleEditorModal({ role, catalog, readOnly, onClose, onSaved, onToast }: {
  role: RoleDraft;
  catalog: RoleCatalog;
  readOnly: boolean;
  onClose: () => void;
  onSaved: (message: string) => Promise<void>;
  onToast: (text: string) => void;
}) {
  const [name, setName] = useState(role.name);
  const [description, setDescription] = useState(role.description);
  const [scope, setScope] = useState<RoleScope>(role.scope);
  const [permissions, setPermissions] = useState<string[]>(role.permissions);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState('');

  const selected = useMemo(() => new Set(permissions), [permissions]);
  const moduleCount = catalog.modules.filter((module) => module.permissions.some((permission) => selected.has(permission.key))).length;
  const scopeInfo = catalog.scopes.find((item) => item.id === scope);

  function toggle(keys: string[], on: boolean) {
    setPermissions((current) => {
      const set = new Set(current);
      keys.forEach((key) => { if (on) set.add(key); else set.delete(key); });
      // 按目录顺序输出，便于后端与展示比对。
      return catalog.modules.flatMap((module) => module.permissions.map((permission) => permission.key)).filter((key) => set.has(key));
    });
  }

  async function submit() {
    const trimmed = name.trim();
    if (!trimmed) {
      setFormError('角色名称不能为空');
      return;
    }
    setFormError('');
    setBusy(true);
    try {
      const body = JSON.stringify({ name: trimmed, description: description.trim(), scope, permissions });
      if (role.id) {
        await jsonRequest(`${API_BASE}/__roles/${encodeURIComponent(role.id)}`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body,
        });
      } else {
        await jsonRequest(`${API_BASE}/__roles`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body,
        });
      }
      await onSaved(role.id ? '角色已更新' : '角色已创建');
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title={role.id ? (readOnly ? '查看角色' : '编辑角色') : '新建角色'}
      description={readOnly
        ? '内置角色不可修改。如需调整权限，请关闭后「复制」成自定义角色再编辑。'
        : '设置角色名称、资源范围与页面权限。保存后立即对持有该角色的成员生效。'}
      size="wide"
      onClose={onClose}
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (!readOnly) void submit();
        }}
      >
        <div className="form-grid modal-form">
          <div className="field">
            <label>角色名称</label>
            <input value={name} disabled={readOnly} placeholder="例如：运维成员" onChange={(event) => setName(event.target.value)} />
          </div>
          <div className="field">
            <label>资源范围</label>
            <select value={scope} disabled={readOnly} onChange={(event) => setScope(event.target.value as RoleScope)}>
              {catalog.scopes.map((item) => <option key={item.id} value={item.id}>{item.label}</option>)}
            </select>
          </div>
          <div className="field field-full">
            <label>角色说明</label>
            <input value={description} disabled={readOnly} placeholder="一句话说明这个角色适合谁" onChange={(event) => setDescription(event.target.value)} />
            {scopeInfo ? <div className="hint-text">{scopeInfo.description}</div> : null}
          </div>
        </div>

        {formError ? <div className="rbac-form-error">保存失败：{formError}</div> : null}

        <div className="rbac-perm-summary">
          <b>{moduleCount} 个模块 · {permissions.length} 项权限</b>
          <span>勾选模块标题可整组开关；资源范围决定这些操作作用在谁的数据上。</span>
        </div>

        <div className="rbac-perm-modules">
          {catalog.modules.map((module) => {
            const keys = module.permissions.map((permission) => permission.key);
            const checkedCount = keys.filter((key) => selected.has(key)).length;
            const allChecked = keys.length > 0 && checkedCount === keys.length;
            const partial = checkedCount > 0 && !allChecked;
            return (
              <div className="rbac-perm-module" key={module.id}>
                <label className="rbac-perm-module-head">
                  <input
                    type="checkbox"
                    checked={allChecked}
                    disabled={readOnly}
                    ref={(node) => {
                      // 半选态只能用 DOM 属性表达。
                      if (node) node.indeterminate = partial;
                    }}
                    onChange={(event) => toggle(keys, event.target.checked)}
                  />
                  <span className="rbac-perm-module-title">{module.label}</span>
                  <span className="rbac-perm-module-count">{checkedCount}/{keys.length}</span>
                </label>
                {module.description ? <p className="rbac-perm-module-desc">{module.description}</p> : null}
                <div className="rbac-perm-list">
                  {module.permissions.map((permission) => (
                    <label className="rbac-perm-item" key={permission.key}>
                      <input
                        type="checkbox"
                        checked={selected.has(permission.key)}
                        disabled={readOnly}
                        onChange={(event) => toggle([permission.key], event.target.checked)}
                      />
                      <span className="rbac-perm-item-label">{permission.label}</span>
                      <code className="rbac-perm-item-key">{permission.key}</code>
                    </label>
                  ))}
                </div>
              </div>
            );
          })}
        </div>

        <div className="actions modal-actions">
          <button type="button" className="btn" onClick={onClose}>{readOnly ? '关闭' : '取消'}</button>
          {readOnly ? null : (
            <button type="submit" className="btn primary" disabled={busy}>
              {busy ? '保存中…' : role.id ? '保存修改' : '创建角色'}
            </button>
          )}
        </div>
      </form>
    </Modal>
  );
}
