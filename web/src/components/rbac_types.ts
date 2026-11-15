// RBAC（角色 / 用户 / 授权目标）的前端类型。与后端 /__roles、/__roles/catalog、
// /__roles/grant-targets、/__users 的响应形状一一对应；放在组件目录里，避免与
// 同时改动的其它模块在 types.ts 上产生冲突。
import type {ConsoleUser} from '../types';

export type {ConsoleUser};

export type RoleScope = 'own' | 'granted' | 'all';

export type Role = {
  id: string;
  name: string;
  description: string;
  builtin: boolean;
  scope: RoleScope;
  permissions: string[];
  userCount: number;
  createdAt?: string;
  updatedAt?: string;
};

export type RoleOption = {
  id: string;
  name: string;
  description: string;
  builtin: boolean;
  scope: RoleScope;
};

export type RolePermission = {
  key: string;
  label: string;
};

export type RolePermissionModule = {
  id: string;
  label: string;
  description: string;
  permissions: RolePermission[];
};

export type RoleScopeOption = {
  id: RoleScope;
  label: string;
  description: string;
};

export type RoleCatalog = {
  modules: RolePermissionModule[];
  scopes: RoleScopeOption[];
};

export type GrantTarget = {
  id: string;
  name: string;
  ownerName: string;
  ownerId?: string;
  protocol?: string;
  disabled?: boolean;
  /** 管理员 Provider 分组；用于在授权选择器里按组浏览/勾选。 */
  groupId?: string;
  groupName?: string;
  /** 非敏感账号标识与已脱敏手机号。 */
  accountLabel?: string;
  phone?: string;
};

/** 编辑角色的表单：新建与编辑共用。 */
export type RoleDraft = {
  id: string | null;
  name: string;
  description: string;
  scope: RoleScope;
  permissions: string[];
};

/** 编辑用户的表单。password 只在新建时提交。 */
export type UserDraft = {
  id: string | null;
  username: string;
  displayName: string;
  password: string;
  roleId: string;
  enabled: boolean;
  avatarUrl: string;
  grantedProviderIds: string[];
};
