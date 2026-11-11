import React from 'react';
import type { Model, Provider } from '../types';
import { modelContextLabel } from '../lib';
import { Badge, Modal } from './ui';

/** 常见窗口的快捷按钮：填的是「多少个 token」，不是「多少 K」。 */
const WINDOW_PRESETS: Array<{ label: string; value: number }> = [
  { label: '200K', value: 200_000 },
  { label: '400K', value: 400_000 },
  { label: '1M', value: 1_000_000 },
  { label: '2M', value: 2_000_000 },
];

/**
 * ModelContextDialog 给一个模型单独定上下文窗口，替代不了批量编辑——窗口是
 * 「这个上游的这个模型能装多少」，一次只说得清一个模型。
 *
 * 为什么需要它：网关只有两条腿判断窗口，厂商已知的那几个公开规格，和上游
 * /models 自己报的数。中转站两条都不占的时候，一个真的能跑 1M 的模型在网关
 * 眼里就是默认的 128K，于是 Claude Desktop / Claude Code 那边也永远按 128K
 * 压缩上下文，而界面上看不出任何异常。
 *
 * 用应用内 Modal 而不是 window.prompt：桌面端（Wails WebView）里原生对话框
 * 根本弹不出来（见 dialogs.tsx 顶部）。
 */
export function ModelContextDialog({
  provider,
  model,
  busy,
  onSave,
  onClose,
}: {
  provider?: Provider;
  model: Model;
  busy: boolean;
  onSave: (contextLength: number) => void;
  onClose: () => void;
}) {
  const current = model.contextOverride || 0;
  const [value, setValue] = React.useState(current > 0 ? String(current) : '');
  const inputRef = React.useRef<HTMLInputElement | null>(null);

  React.useEffect(() => {
    inputRef.current?.focus();
    inputRef.current?.select();
  }, []);

  const trimmed = value.trim().replace(/[,_\s]/g, '');
  const parsed = trimmed === '' ? 0 : Number(trimmed);
  const valid = Number.isFinite(parsed) && Number.isInteger(parsed) && parsed >= 0 && parsed <= 10_000_000;
  // 空串 = 退回自动（按厂商/上游算），不是「窗口是 0」。
  const next = trimmed === '' ? 0 : parsed;
  const dirty = valid && next !== current;

  const submit = () => {
    if (busy || !valid) return;
    onSave(next);
  };

  return (
    <Modal
      title="模型上下文窗口"
      description={`${model.name && model.name !== model.id ? `${model.name} · ` : ''}${model.id}`}
      onClose={onClose}
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <div className="form-grid modal-form">
          <div className="field field-full">
            <label>上下文窗口（token 数）</label>
            <input
              ref={inputRef}
              type="number"
              min={0}
              max={10_000_000}
              step={1000}
              value={value}
              disabled={busy}
              placeholder="留空 = 自动"
              onChange={(event) => setValue(event.target.value)}
            />
            <div className="models-filter-group" style={{ marginTop: 8 }}>
              {WINDOW_PRESETS.map((preset) => (
                <button
                  key={preset.value}
                  type="button"
                  className={`models-filter-chip ${next === preset.value ? 'active' : ''}`}
                  disabled={busy}
                  onClick={() => setValue(String(preset.value))}
                >
                  {preset.label}
                </button>
              ))}
              <button
                type="button"
                className={`models-filter-chip ${trimmed === '' ? 'active' : ''}`}
                disabled={busy}
                onClick={() => setValue('')}
              >
                自动
              </button>
            </div>
            {!valid ? <div className="hint-line error">请填 0–10000000 之间的整数（token 数）。</div> : null}
            <div className="hint-text" style={{ marginTop: 8 }}>
              网关按这个数决定「这个模型能装多少」，并在 /v1/models 里同时按
              context_length、context_window、max_input_tokens 报给客户端；到了 1M
              及以上，Claude Desktop 与 Claude Code 会把它当成 1M 窗口的模型列出来。
              留空则按厂商公开规格或上游自己报的数算（当前自动值
              {model.contextLength ? ` ${model.contextLength.toLocaleString()}` : '未知'}）。
              设置和上游清单分开存，重新同步模型不会把它冲掉。
            </div>
            <div className="hint-text">
              这一项不影响请求本身：网关不按窗口截断内容，超长的部分仍然由上游
              自己拒绝。{provider ? `所属：${provider.name}` : ''}
            </div>
          </div>
        </div>
        <div className="actions modal-actions">
          <button type="button" className="btn" onClick={onClose}>取消</button>
          <button type="submit" className="btn primary" disabled={busy || !valid || !dirty}>
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

/**
 * ModelContextCell 是模型列表里「上下文长度」那一格：数字 + 是不是运维设的。
 *
 * 数字永远是对外宣称的那个值（设过就是设的值），「手动」标记不能省——设过之后
 * 上游再报什么都会被它盖住，运维排查「为什么这个模型说 1M」时得一眼看出该去
 * 哪里改。contextLength 在设过的时候仍是自动值，悬浮提示里一并给出来。
 */
export function ModelContextCell({ model, onEdit }: { model: Model; onEdit?: () => void }) {
  const override = model.contextOverride || 0;
  const effective = override > 0 ? override : (model.contextLength || 0);
  const text = effective ? effective.toLocaleString() : '—';
  const badge = modelContextLabel({ ...model, contextLength: effective });
  return (
    <span className="model-context-cell">
      <span className="model-context-value">{text}</span>
      {badge ? <Badge tone="slate">{badge}</Badge> : null}
      {override > 0 ? (
        <span
          className="model-context-manual"
          title={`手动设置，重新同步模型也不会覆盖。自动值（厂商规格 / 上游声明）：${(model.contextLength || 0).toLocaleString()}`}
        >
          手动
        </span>
      ) : null}
      {onEdit ? (
        <button type="button" className="mini-btn" onClick={onEdit} title="设置这个模型的上下文窗口">设置</button>
      ) : null}
    </span>
  );
}
