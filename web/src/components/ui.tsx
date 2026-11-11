// @generated-from main.tsx — 由重构脚本拆分生成，请直接维护本文件。
import React, {useEffect, useRef, useState} from 'react';
import {createPortal} from 'react-dom';
import {copyToClipboard, isFreeModel, modelContextLabel, modelRateLabel} from '../lib';
import {BadgeTone, ModalLayer, Model, NavItemID, ThemeMode} from '../types';

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

export function Badge({ tone, children, title }: { tone: BadgeTone; children: React.ReactNode; title?: string }) {
  return <span className={`badge ${tone}`} title={title}>{children}</span>;
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
/**
 * ModelPriceTags 是模型的价格标签组：免费（唯一用绿色）、积分倍率（被划线的是
 * 打折前的原价）、以及 1M 以上的窗口。上游没给价格的厂商一个都不渲染 —— 宁缺
 * 毋造，避免把 0× 当成"免费"读。
 */
export function ModelPriceTags({ model }: { model: Model }) {
  const rate = modelRateLabel(model);
  const context = modelContextLabel(model);
  const free = isFreeModel(model);
  if (!free && !rate && !context) return null;
  return (
    <>
      {free ? <span className="model-tag free">免费</span> : null}
      {rate ? (
        <span className="model-tag rate" title={`每次请求消耗 ${rate.rate} 倍积分${rate.was ? `（原价 ${rate.was}）` : ''}`}>
          {rate.was ? <s>{rate.was}</s> : null}
          {rate.rate}
        </span>
      ) : null}
      {context ? <span className="model-tag ctx" title={`${(model.contextLength || 0).toLocaleString()} tokens`}>{context}</span> : null}
    </>
  );
}

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
    case 'quota-keeper':
      return (<svg viewBox="0 0 24 24" {...common}><circle cx="12" cy="12" r="8.5" /><path d="M12 7.2V12l3.2 2" /><path d="M18.6 4.6 20.4 6.4M19.5 4.1v2.3h-2.3" /></svg>);
    case 'users':
      return (<svg viewBox="0 0 24 24" {...common}><circle cx="9.5" cy="8.5" r="3.5" /><path d="M3.5 19.5a6 6 0 0 1 12 0" /><path d="M16 5.6a3.5 3.5 0 0 1 0 5.8M17.8 14.3a6 6 0 0 1 2.7 5.2" /></svg>);
    case 'machine':
      return (<svg viewBox="0 0 24 24" {...common}><rect x="6" y="6" width="12" height="12" rx="2" /><rect x="10" y="10" width="4" height="4" rx="1" /><path d="M9 3v3M15 3v3M9 18v3M15 18v3M3 9h3M3 15h3M18 9h3M18 15h3" /></svg>);
    case 'settings':
      return (<svg viewBox="0 0 24 24" {...common}><circle cx="12" cy="12" r="3" /><path d="M12 2.8v2.4M12 18.8v2.4M2.8 12h2.4M18.8 12h2.4M5.5 5.5l1.7 1.7M16.8 16.8l1.7 1.7M5.5 18.5l1.7-1.7M16.8 7.2l1.7-1.7" /></svg>);
    case 'monitoring':
      // 监控中心：示波器式的健康曲线 + 一个告警点。
      return (<svg viewBox="0 0 24 24" {...common}><path d="M3 13.5h3.4l2-5.5 3 10 2.2-6.5 1.4 2H21" /><circle cx="18.4" cy="6.2" r="1.6" /></svg>);
    default:
      return (<svg viewBox="0 0 24 24" {...common}><circle cx="12" cy="12" r="8.5" /></svg>);
  }
}

/**
 * BrandIcon：品牌图标。web/public/icons/ 里 -color 结尾的是彩色版，按 <img> 原样
 * 显示；裸名版是 currentColor 单色，只能走 CSS mask 才会跟着主题着色（当图片渲染
 * 会在深色下变成一团黑）。没有匹配到图标时退回名称首字。
 */
export function BrandIcon({
  icon,
  name,
  size = 18,
  className = '',
}: {
  icon?: string;
  name: string;
  size?: number;
  className?: string;
}) {
  if (!icon) {
    return (
      <span className={`brand-icon fallback ${className}`} aria-hidden="true" style={{ width: size, height: size }}>
        {brandMonogram(name)}
      </span>
    );
  }
  // 图标默认是同名 SVG，用 currentColor 单色描出来；厂商只给了位图时（ZCode 只有
  // 一张彩色 PNG）在名字里写清后缀，按彩色图直接显示——遮罩上色对位图没有意义。
  const bitmap = icon.includes('.');
  const source = bitmap ? `/icons/${icon}` : `/icons/${icon}.svg`;
  if (bitmap || icon.endsWith('-color')) {
    return <img className={`brand-icon ${className}`} src={source} alt="" width={size} height={size} />;
  }
  return (
    <span
      className={`brand-icon mask ${className}`}
      aria-hidden="true"
      style={{ width: size, height: size, WebkitMaskImage: `url(${source})`, maskImage: `url(${source})` }}
    />
  );
}

// brandMonogram：中文名只取一个字——两个汉字放不进小方块，实测会把"阶跃"挤成两行。
export function brandMonogram(name: string): string {
  const source = (name || '').replace(/[（(].*$/, '').trim();
  const letters = source.match(/[A-Za-z]/g);
  if (letters && letters.length >= 2) return letters.slice(0, 2).join('').toUpperCase();
  return source.slice(0, 1) || '?';
}

/**
 * InfoTip：字段说明悬浮提示。在标签旁放一个 ⊙ 图标，鼠标悬浮弹出说明文字，
 * 替代原来始终占据垂直空间的 hint-line，让页面更紧凑。
 * 纯 CSS 实现，不需要 JS 状态。错误提示（error variant）不应使用此组件。
 */
export function InfoTip({ text, children }: { text: string; children?: React.ReactNode }) {
  const anchorRef = useRef<HTMLSpanElement>(null);
  const hideTimer = useRef<number | null>(null);
  const [pos, setPos] = useState<{ left: number; top: number; below: boolean } | null>(null);
  useEffect(() => () => { if (hideTimer.current !== null) window.clearTimeout(hideTimer.current); }, []);
  if (!text) return null;
  // 气泡挂到 body 上：字段可能处在带 overflow 的弹窗/面板里，就地绝对定位会被裁掉。
  const show = () => {
    if (hideTimer.current !== null) window.clearTimeout(hideTimer.current);
    const anchor = anchorRef.current;
    if (!anchor) return;
    const rect = anchor.getBoundingClientRect();
    const below = rect.top < 120;
    const half = Math.min(160, window.innerWidth / 2 - 12);
    const left = Math.min(Math.max(rect.left + rect.width / 2, half), window.innerWidth - half);
    setPos({ left, top: below ? rect.bottom + 8 : rect.top - 8, below });
  };
  const hide = () => { hideTimer.current = window.setTimeout(() => setPos(null), 120); };
  return (
    <span
      ref={anchorRef}
      className="info-tip"
      aria-label={text}
      tabIndex={0}
      onMouseEnter={show}
      onMouseLeave={hide}
      onFocus={show}
      onBlur={hide}
    >
      {children ?? <span className="info-tip-icon" aria-hidden="true">⊙</span>}
      {pos && createPortal(
        <span
          className={`info-tip-popup${pos.below ? ' tip-below' : ''}`}
          role="tooltip"
          style={{ left: pos.left, top: pos.top }}
          onMouseEnter={() => { if (hideTimer.current !== null) window.clearTimeout(hideTimer.current); }}
          onMouseLeave={hide}
        >
          {text}
        </span>,
        document.body,
      )}
    </span>
  );
}

// Switch：表单里的布尔项。原生 checkbox 只有 13px、在深色主题下几乎看不见，
// 开关做成 34×20 的轨道，右侧带一句状态文案，和输入框同一行高。
export function Switch({ checked, label, onChange, disabled }: { checked: boolean; label?: string; onChange: (value: boolean) => void; disabled?: boolean }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      className={`switch-control${checked ? ' on' : ''}`}
      disabled={disabled}
      onClick={() => onChange(!checked)}
    >
      <span className="switch-track" aria-hidden="true"><span className="switch-thumb" /></span>
      {label ? <span className="switch-label">{label}</span> : null}
    </button>
  );
}

export function Field({ label, hint, value, onChange, placeholder, fullWidth, multiline, rows = 2 }: { label: string; hint?: string; value: string; onChange: (value: string) => void; placeholder?: string; fullWidth?: boolean; multiline?: boolean; rows?: number }) {
  // 多行字段（提示词一类）沿用 .field 的控件外观，只放开高度与纵向拉伸。
  const control = multiline
    ? <textarea value={value} placeholder={placeholder} rows={rows} onChange={(event) => onChange(event.target.value)} />
    : <input value={value} placeholder={placeholder} onChange={(event) => onChange(event.target.value)} />;
  // hint：字段说明。给了就收进标签旁的 InfoTip，不再占一行纵向空间。
  const labelRow = hint
    ? <div className="field-label-row"><label>{label}</label><InfoTip text={hint} /></div>
    : <label>{label}</label>;
  return <div className={`field${fullWidth ? ' field-full' : ''}`}>{labelRow}{control}</div>;
}

export function CopyButton({ value, label = '复制', toastContent, className = '' }: { value: string; label?: string; toastContent?: string; className?: string }) {
  const [copied, setCopied] = React.useState(false);
  return (
    <button
      className={`mini-btn${className ? ` ${className}` : ''}${copied ? ' copied' : ''}`}
      type="button"
      onClick={() => {
        void copyToClipboard(value).then((ok) => {
          if (ok) {
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

// 背景滚动锁按"还开着几个弹窗"计数，而不是每个弹窗各自记住上一次的 overflow：
// 叠开两个弹窗时，内层捕获到的就是 'hidden'，它关闭时把 'hidden' 原样写回去，
// 于是全部弹窗关掉后页面再也滚不动。计数归零才解锁。
let modalScrollLocks = 0;
let modalScrollRestore = '';

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
    modalScrollLocks += 1;
    if (modalScrollLocks === 1) {
      modalScrollRestore = document.body.style.overflow;
      document.body.style.overflow = 'hidden';
    }
    return () => {
      const index = modalLayers.findIndex((item) => item.id === layer.id);
      if (index >= 0) modalLayers.splice(index, 1);
      modalScrollLocks = Math.max(0, modalScrollLocks - 1);
      if (modalScrollLocks === 0) document.body.style.overflow = modalScrollRestore;
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
