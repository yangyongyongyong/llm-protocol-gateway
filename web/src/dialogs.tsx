// 应用内确认 / 提示弹窗：替代 window.confirm 与 window.alert。
//
// 桌面 App 的窗口是 Wails 的 WKWebView，而 Wails 没有实现 WKUIDelegate 的
// runJavaScriptConfirmPanel / runJavaScriptAlertPanel —— macOS 在 delegate 缺
// 这两个方法时直接以 NO 回调、alert 则静默丢弃。结果是：
//
//	window.confirm(...) 立即返回 false
//	window.alert(...)   什么都不弹
//
// 落在它们上面的按钮就成了「假按钮」：点了没反应、不报错、也发不出请求。
// 删除 Provider 正是被 deleteProvider() 第一行的 confirm 挡住的——所以在桌面端
// 只有删除失效，而克隆（应用内弹窗）和禁用（无确认、直接请求）都正常。
//
// 这里不依赖原生对话框，用应用内 Modal 自己渲染，返回 Promise，浏览器与桌面端
// 行为一致。apikeys.tsx 的 ProfileNameDialog 当初为 window.prompt 做过同样的事。
import React from 'react';
import {createRoot, type Root} from 'react-dom/client';
import {Modal} from './components/ui';

export type ConfirmOptions = {
  /** 确认按钮文案，默认「确定」。 */
  confirmLabel?: string;
  /** 取消按钮文案，默认「取消」。 */
  cancelLabel?: string;
  /** 危险动作（删除类）：确认按钮标红。 */
  danger?: boolean;
};

type PendingDialog = {
  kind: 'confirm' | 'alert';
  /** 第一行当标题，其余行当正文；多行提示因此能自然分段。 */
  lines: string[];
  confirmLabel: string;
  cancelLabel: string;
  danger: boolean;
  resolve: (ok: boolean) => void;
};

// 同一时刻只显示一个：连续触发（例如批量删除里连着报错）的请求排队，前一个关掉
// 再显示下一个，不会两个遮罩叠在一起。
const pending: PendingDialog[] = [];
const subscribers = new Set<() => void>();
let hostRoot: Root | null = null;

function emit() {
  for (const notify of subscribers) notify();
}

function ensureHost() {
  if (hostRoot || typeof document === 'undefined') return;
  const container = document.createElement('div');
  container.dataset.dialogHost = '';
  document.body.appendChild(container);
  hostRoot = createRoot(container);
  hostRoot.render(<DialogHost />);
}

// splitMessage 把提示拆成标题 + 正文。沿用 confirm 的原文，调用方只需要把
// window.confirm 换成 confirmDialog，文案不用重写。
function splitMessage(message: string): string[] {
  const lines = String(message ?? '').split('\n');
  while (lines.length > 1 && lines[lines.length - 1].trim() === '') lines.pop();
  return lines.length > 0 && lines[0].trim() === '' ? lines.slice(1) : lines;
}

function push(
  kind: PendingDialog['kind'],
  message: string,
  confirmLabel: string,
  cancelLabel: string,
  danger: boolean,
): Promise<boolean> {
  if (typeof document === 'undefined') {
    // 没有 DOM（SSR / 单测）时才退回原生对话框。
    if (kind === 'alert') {
      window.alert(message);
      return Promise.resolve(true);
    }
    return Promise.resolve(window.confirm(message));
  }
  return new Promise<boolean>((resolve) => {
    // 先入队再挂载：DialogHost 的首帧就能看到这条请求，不依赖 effect 订阅时序。
    pending.push({kind, lines: splitMessage(message), confirmLabel, cancelLabel, danger, resolve});
    ensureHost();
    emit();
  });
}

/** confirmDialog 询问用户是否继续，确认返回 true，取消 / 关闭 / Esc 返回 false。 */
export function confirmDialog(message: string, options: ConfirmOptions = {}): Promise<boolean> {
  return push('confirm', message, options.confirmLabel || '确定', options.cancelLabel || '取消', options.danger === true);
}

/** alertDialog 显示一段提示，用户点按钮后 resolve。 */
export function alertDialog(message: string, options: {confirmLabel?: string} = {}): Promise<boolean> {
  return push('alert', message, options.confirmLabel || '知道了', '', false);
}

export function DialogHost() {
  const [, forceRender] = React.useState(0);

  React.useEffect(() => {
    const notify = () => forceRender((n) => n + 1);
    subscribers.add(notify);
    return () => {
      subscribers.delete(notify);
    };
  }, []);

  const head = pending[0];
  if (!head) return null;

  const settle = (ok: boolean) => {
    const index = pending.indexOf(head);
    if (index < 0) return;
    pending.splice(index, 1);
    emit();
    head.resolve(ok);
  };

  const [title, ...body] = head.lines;
  return (
    <Modal title={title || '提示'} description="" onClose={() => settle(false)}>
      {body.length > 0 ? <p className="dialog-message">{body.join('\n')}</p> : null}
      <div className="actions modal-actions">
        {/* 焦点落在「取消」而不是确认按钮上。
            原先确认按钮带 autoFocus —— 危险操作（删除 Provider / 吊销对端）时，弹窗
            一出现光标就压在红色按钮上，一次误按回车就是不可逆动作。取消放前面并
            接收初始焦点：确认仍只有一步，但需要主动移过去。 */}
        {head.kind === 'confirm' ? (
          <button className="btn" onClick={() => settle(false)} autoFocus>{head.cancelLabel}</button>
        ) : null}
        <button
          className={`btn ${head.danger ? 'danger' : 'primary'}`}
          onClick={() => settle(true)}
          autoFocus={head.kind !== 'confirm'}
        >
          {head.confirmLabel}
        </button>
      </div>
    </Modal>
  );
}
