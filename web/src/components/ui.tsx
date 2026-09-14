// @generated-from main.tsx — 由重构脚本拆分生成，请直接维护本文件。
import React, { useEffect, useRef } from 'react';
import { createPortal } from 'react-dom';
import { BadgeTone, ModalLayer, NavItemID, ThemeMode } from '../types';
export const THEME_STORAGE_KEY = 'llm-gateway-theme';

export function systemPrefersDark() {
  return typeof window !== 'undefined'
    && typeof window.matchMedia === 'function'
    && window.matchMedia('(prefers-color-scheme: dark)').matches;
}

export function resolveTheme(mode: ThemeMode): 'light' | 'dark' {
  if (mode === 'system') return systemPrefersDark() ? 'dark' : 'light';
  return mode;
}

export function readStoredTheme(): ThemeMode {
  try {
    const raw = localStorage.getItem(THEME_STORAGE_KEY);
    if (raw === 'light' || raw === 'dark' || raw === 'system') return raw;
  } catch {
    // ignore
  }
  return 'system';
}

let themeAnimArmed = false;
let themeAnimTimer: number | undefined;

export function applyThemeMode(mode: ThemeMode) {
  const resolved = resolveTheme(mode);
  const root = document.documentElement;
  // 初次应用（页面加载）直接落定；此后用户/系统切换主题时短暂挂上
  // html.theme-anim，让背景/文字/边框颜色平滑过渡而不是瞬间跳变。
  if (themeAnimArmed) {
    root.classList.add('theme-anim');
    if (themeAnimTimer != null) window.clearTimeout(themeAnimTimer);
    themeAnimTimer = window.setTimeout(() => root.classList.remove('theme-anim'), 380);
  }
  themeAnimArmed = true;
  root.dataset.theme = resolved;
  root.style.colorScheme = resolved;
  return resolved;
}

export function ThemeIcon({ kind }: { kind: ThemeMode }) {
  if (kind === 'light') {
    return (
      <svg className="theme-icon" viewBox="0 0 24 24" aria-hidden="true">
        <circle cx="12" cy="12" r="4.2" fill="currentColor" />
        <g stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
          <path d="M12 2.5v2.2M12 19.3v2.2M2.5 12h2.2M19.3 12h2.2M5.1 5.1l1.6 1.6M17.3 17.3l1.6 1.6M5.1 18.9l1.6-1.6M17.3 6.7l1.6-1.6" />
        </g>
      </svg>
    );
  }
  if (kind === 'dark') {
    return (
      <svg className="theme-icon" viewBox="0 0 24 24" aria-hidden="true">
        <path
          fill="currentColor"
          d="M15.2 3.1a8.8 8.8 0 1 0 5.7 15.5A8.2 8.2 0 0 1 15.2 3.1Z"
        />
      </svg>
    );
  }
  return (
    <svg className="theme-icon" viewBox="0 0 24 24" aria-hidden="true">
      <rect x="3.5" y="4.5" width="17" height="12" rx="2.2" fill="none" stroke="currentColor" strokeWidth="1.8" />
      <path d="M8 20.2h8M12 16.5v3.7" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
    </svg>
  );
}

export function ThemeSwitch({
  value,
  onChange,
  size = 'default',
}: {
  value: ThemeMode;
  onChange: (mode: ThemeMode) => void;
  size?: 'default' | 'compact';
}) {
  const options: Array<{ id: ThemeMode; label: string }> = [
    { id: 'light', label: '白天' },
    { id: 'dark', label: '夜晚' },
    { id: 'system', label: '跟随系统' },
  ];
  return (
    <div className={`theme-switch ${size === 'compact' ? 'compact' : ''}`} role="group" aria-label="主题模式">
      {options.map((option) => (
        <button
          key={option.id}
          type="button"
          className={`theme-switch-btn ${value === option.id ? 'active' : ''}`}
          onClick={() => onChange(option.id)}
          title={option.label}
          aria-label={option.label}
          aria-pressed={value === option.id}
        >
          <ThemeIcon kind={option.id} />
        </button>
      ))}
    </div>
  );
}

export function Badge({ tone, children }: { tone: BadgeTone; children: React.ReactNode }) {
  return <span className={`badge ${tone}`}>{children}</span>;
}

export interface MoreMenuItem {
  label: string;
  danger?: boolean;
  disabled?: boolean;
  onClick: () => void;
}

/** “更多操作”下拉菜单：低频操作（克隆/禁用/删除）收纳入口，避免卡片页脚按钮堆砌。
 *  菜单层 portal 到 body 并用 fixed 坐标定位——卡片容器有 overflow:hidden，
 *  卡内绝对定位会被裁剪；portal 同时天然脱离卡片点击冒泡路径。 */
export function MoreMenu({ items, label = '更多操作' }: { items: MoreMenuItem[]; label?: string }) {
  const [open, setOpen] = React.useState(false);
  const [pos, setPos] = React.useState<{ top: number; left: number } | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);

  // 视口下方放不下时向上翻；水平右对齐触发按钮并夹在视口内
  const place = React.useCallback(() => {
    const trigger = triggerRef.current;
    if (!trigger) return;
    const rect = trigger.getBoundingClientRect();
    const width = 132;
    const estimatedHeight = items.length * 33 + 12;
    const below = window.innerHeight - rect.bottom;
    const top = below < estimatedHeight + 12 ? Math.max(8, rect.top - estimatedHeight - 8) : rect.bottom + 6;
    const left = Math.max(8, Math.min(rect.right - width, window.innerWidth - width - 8));
    setPos({ top, left });
  }, [items.length]);

  React.useEffect(() => {
    if (!open) return undefined;
    const onDocMouseDown = (event: MouseEvent) => {
      const target = event.target as Node;
      if (menuRef.current?.contains(target) || triggerRef.current?.contains(target)) return;
      setOpen(false);
    };
    // stopPropagation：菜单开着时 Escape 只关菜单，不穿透到下层弹窗
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.stopPropagation();
        setOpen(false);
      }
    };
    const onClose = () => setOpen(false);
    document.addEventListener('mousedown', onDocMouseDown);
    document.addEventListener('keydown', onKeyDown);
    window.addEventListener('scroll', onClose, true);
    window.addEventListener('resize', onClose);
    return () => {
      document.removeEventListener('mousedown', onDocMouseDown);
      document.removeEventListener('keydown', onKeyDown);
      window.removeEventListener('scroll', onClose, true);
      window.removeEventListener('resize', onClose);
    };
  }, [open]);

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        className={`icon-btn more-menu-trigger${open ? ' open' : ''}`}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={label}
        title={label}
        onClick={() => {
          if (!open) place();
          setOpen((value) => !value);
        }}
      >
        ⋯
      </button>
      {open && pos ? createPortal(
        <div
          ref={menuRef}
          className="more-menu-list"
          role="menu"
          style={{ top: pos.top, left: pos.left }}
          onClick={(event) => event.stopPropagation()}
        >
          {items.map((item) => (
            <button
              key={item.label}
              type="button"
              role="menuitem"
              className={`more-menu-item${item.danger ? ' danger' : ''}`}
              disabled={item.disabled}
              onClick={(event) => {
                event.stopPropagation();
                setOpen(false);
                item.onClick();
              }}
            >
              {item.label}
            </button>
          ))}
        </div>,
        document.body,
      ) : null}
    </>
  );
}

/**
 * 侧边栏导航图标：统一的 16px 线性 SVG 图标集（stroke 风格），
 * 替换原来的 emoji / 字符画，保证跨平台渲染一致、风格统一。
 */
export function NavIcon({ id }: { id: NavItemID }) {
  const common = {
    fill: 'none',
    stroke: 'currentColor',
    strokeWidth: 1.7,
    strokeLinecap: 'round' as const,
    strokeLinejoin: 'round' as const,
  };
  switch (id) {
    case 'input-providers':
      return (<svg viewBox="0 0 24 24" {...common}><path d="M9 3v5M15 3v5" /><path d="M7 8h10v3.5a5 5 0 0 1-10 0V8Z" /><path d="M12 16.5V21" /></svg>);
    case 'models-menu':
      return (<svg viewBox="0 0 24 24" {...common}><rect x="3.5" y="3.5" width="7" height="7" rx="1.6" /><rect x="13.5" y="3.5" width="7" height="7" rx="1.6" /><rect x="3.5" y="13.5" width="7" height="7" rx="1.6" /><rect x="13.5" y="13.5" width="7" height="7" rx="1.6" /></svg>);
    case 'api-keys':
      return (<svg viewBox="0 0 24 24" {...common}><circle cx="8" cy="12" r="4.2" /><path d="M12.2 12H21M17.5 12v3M20.8 12v2.2" /></svg>);
    case 'output-providers':
      return (<svg viewBox="0 0 24 24" {...common}><path d="M12 3v10M12 3 8.5 6.5M12 3l3.5 3.5" /><path d="M5 13.5v4A2.5 2.5 0 0 0 7.5 20h9a2.5 2.5 0 0 0 2.5-2.5v-4" /></svg>);
    case 'usage-stats':
      return (<svg viewBox="0 0 24 24" {...common}><path d="M4 20h16" /><path d="M7 16.5v-5M12 16.5V8M17 16.5v-3" /></svg>);
    case 'public-access':
      return (<svg viewBox="0 0 24 24" {...common}><circle cx="12" cy="12" r="8.5" /><path d="M3.5 12h17M12 3.5c2.4 2.3 3.6 5.1 3.6 8.5s-1.2 6.2-3.6 8.5c-2.4-2.3-3.6-5.1-3.6-8.5s1.2-6.2 3.6-8.5Z" /></svg>);
    case 'traffic-tokens':
      return (<svg viewBox="0 0 24 24" {...common}><path d="M8 6h13M8 12h13M8 18h13" /><path d="M3.5 6h.01M3.5 12h.01M3.5 18h.01" strokeWidth="2.6" /></svg>);
    case 'alerts':
      return (<svg viewBox="0 0 24 24" {...common}><path d="M6.2 9.5a5.8 5.8 0 0 1 11.6 0c0 4 1.7 5.6 1.7 5.6H4.5s1.7-1.6 1.7-5.6" /><path d="M10.4 19a1.8 1.8 0 0 0 3.2 0" /></svg>);
    case 'users':
      return (<svg viewBox="0 0 24 24" {...common}><circle cx="9.5" cy="8.5" r="3.5" /><path d="M3.5 19.5a6 6 0 0 1 12 0" /><path d="M16 5.6a3.5 3.5 0 0 1 0 5.8M17.8 14.3a6 6 0 0 1 2.7 5.2" /></svg>);
    case 'self-check':
      return (<svg viewBox="0 0 24 24" {...common}><circle cx="12" cy="12" r="8.5" /><path d="m8.5 12.2 2.4 2.4 4.6-4.8" /></svg>);
    case 'machine':
      return (<svg viewBox="0 0 24 24" {...common}><rect x="6" y="6" width="12" height="12" rx="2" /><rect x="10" y="10" width="4" height="4" rx="1" /><path d="M9 3v3M15 3v3M9 18v3M15 18v3M3 9h3M3 15h3M18 9h3M18 15h3" /></svg>);
    case 'settings':
      return (<svg viewBox="0 0 24 24" {...common}><circle cx="12" cy="12" r="3" /><path d="M12 2.8v2.4M12 18.8v2.4M2.8 12h2.4M18.8 12h2.4M5.5 5.5l1.7 1.7M16.8 16.8l1.7 1.7M5.5 18.5l1.7-1.7M16.8 7.2l1.7-1.7" /></svg>);
    default:
      return (<svg viewBox="0 0 24 24" {...common}><circle cx="12" cy="12" r="8.5" /></svg>);
  }
}

export function Field({ label, value, onChange, placeholder, fullWidth }: { label: string; value: string; onChange: (value: string) => void; placeholder?: string; fullWidth?: boolean }) {
  return <div className={`field${fullWidth ? ' field-full' : ''}`}><label>{label}</label><input value={value} placeholder={placeholder} onChange={(event) => onChange(event.target.value)} /></div>;
}

export function CopyButton({ value, label = '复制', toastContent }: { value: string; label?: string; toastContent?: string }) {
  const [copied, setCopied] = React.useState(false);
  return (
    <button
      className={`mini-btn${copied ? ' copied' : ''}`}
      type="button"
      onClick={() => {
        void navigator.clipboard.writeText(value).then(() => {
          setCopied(true);
          window.setTimeout(() => setCopied(false), 2000);
          // 仅当调用方显式传入 toastContent 时，才在底部全局 toast 里展示复制的具体内容
          // （持续 3s）；不传时保持原有行为（只有按钮自身文案变化），避免大段文本
          // （比如“复制全部”/“复制 curl”）把底部 toast 撑爆。
          if (toastContent !== undefined) {
            const toast = document.getElementById('toast');
            if (toast) {
              toast.textContent = toastContent;
              toast.classList.add('show');
              window.setTimeout(() => toast.classList.remove('show'), 3000);
            }
          }
        });
      }}
    >
      {copied ? '复制成功' : label}
    </button>
  );
}

export function CheckboxField({ label, checked, onChange }: { label: string; checked: boolean; onChange: (value: boolean) => void }) {
  return <label className="field checkbox-field"><input type="checkbox" checked={checked} onChange={(event) => onChange(event.target.checked)} /><span>{label}</span></label>;
}

export function SelectField({ label, values, value, onChange, disabled, fullWidth }: { label: string; values: string[]; value?: string; onChange: (value: string) => void; disabled?: boolean; fullWidth?: boolean }) {
  return <div className={`field${fullWidth ? ' field-full' : ''}`}><label>{label}</label><select value={value || values[0] || ''} disabled={disabled} onChange={(event) => onChange(event.target.value)}>{values.map((item) => <option key={item}>{item}</option>)}</select></div>;
}

export function URLRow({ label, value, onCopy }: { label: string; value: string; onCopy?: () => void }) {
  return <div className="url-row"><div className="url-label">{label}</div><div className="code">{value}</div><button className="mini-btn" disabled={!onCopy} onClick={onCopy}>{onCopy ? '复制' : '不可用'}</button></div>;
}

export const modalLayers: ModalLayer[] = [];

export let nextModalLayerID = 0;

// 弹窗层级的单调递增计数器：在打开瞬间分配，避免按 modalLayers.length 推算时
// 同一批次渲染的两个弹窗拿到相同 z-index（length 要等 useEffect 入栈后才更新）。
export let nextModalZIndex = 1100;

export let modalEscapeListenerReady = false;

export function ensureModalEscapeListener() {
  if (modalEscapeListenerReady) return;
  modalEscapeListenerReady = true;
  window.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape' || modalLayers.length === 0) return;
    const top = modalLayers[modalLayers.length - 1];
    event.preventDefault();
    event.stopPropagation();
    top.onClose();
  });
}

export function Modal({ title, description, children, onClose, size = 'default', blocking = true }: { title: string; description: string; children: React.ReactNode; onClose: () => void; size?: 'default' | 'wide'; blocking?: boolean }) {
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const layerRef = useRef<{ id: number; zIndex: number } | null>(null);

  if (!layerRef.current) {
    ensureModalEscapeListener();
    const id = ++nextModalLayerID;
    nextModalZIndex += 2;
    layerRef.current = { id, zIndex: nextModalZIndex };
  }

  useEffect(() => {
    const layer = layerRef.current!;
    const entry: ModalLayer = {
      id: layer.id,
      onClose: () => onCloseRef.current(),
    };
    modalLayers.push(entry);
    // 打开期间锁定背景滚动，避免鼠标在弹窗边缘滚动时把后面的页面一起滚走。
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      const index = modalLayers.findIndex((item) => item.id === layer.id);
      if (index >= 0) modalLayers.splice(index, 1);
      document.body.style.overflow = previousOverflow;
    };
  }, []);

  // Portal 到 body：.card / .sidebar 上的 backdrop-filter 会让它们成为 fixed
  // 定位的包含块，弹窗若留在卡片内部渲染，遮罩会被卡片裁剪并错位。
  return createPortal(
    <div
      className={`modal-backdrop${blocking ? '' : ' non-blocking'}`}
      style={{ zIndex: layerRef.current.zIndex }}
      onMouseDown={blocking ? () => onCloseRef.current() : undefined}
    >
      <div className={`modal-card${size === 'wide' ? ' wide' : ''}`} onMouseDown={(event) => event.stopPropagation()}>
        <div className="modal-header">
          <div>
            <h2>{title}</h2>
            <p>{description}</p>
          </div>
          <button className="icon-btn modal-close" onClick={() => onCloseRef.current()} aria-label="关闭弹窗">✕</button>
        </div>
        {children}
      </div>
    </div>,
    document.body,
  );
}
