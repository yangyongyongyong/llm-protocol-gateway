import React from 'react';
import type {Model, ModelUSDPrice, Provider} from '../types';
import {
  API_BASE,
  type EffectivePriceSource,
  formatUSDPerMTok,
  modelContextLabel,
  resolveEffectiveModelPrice,
  upstreamModelForPricing,
} from '../lib';
import {readPrices} from './pricing';
import {Badge, Modal} from './ui';

/** 常见窗口的快捷按钮：填的是「多少个 token」，不是「多少 K」。 */
const WINDOW_PRESETS: Array<{ label: string; value: number }> = [
  { label: '200K', value: 200_000 },
  { label: '400K', value: 400_000 },
  { label: '1M', value: 1_000_000 },
  { label: '2M', value: 2_000_000 },
];

const GLOBAL_SCOPE = '*';

type RateKey = 'inputUsdPerMTok' | 'outputUsdPerMTok' | 'cacheReadUsdPerMTok' | 'cacheWriteUsdPerMTok';

const PRICE_FIELDS: Array<{ key: RateKey; label: string; hint: string }> = [
  { key: 'inputUsdPerMTok', label: '输入（未缓存）', hint: '每百万非缓存 prompt Token 的美元单价' },
  { key: 'outputUsdPerMTok', label: '输出', hint: '每百万输出 Token 的美元单价' },
  { key: 'cacheReadUsdPerMTok', label: '缓存读取', hint: '每百万缓存读取 Token 的美元单价' },
  { key: 'cacheWriteUsdPerMTok', label: '缓存写入', hint: '每百万缓存写入 Token 的美元单价' },
];

/** 把单价数值转成表单展示字符串（0 视为留空）。 */
function formatRateInput(price: ModelUSDPrice | undefined, key: RateKey): string {
  if (!price) return '';
  const val = price[key];
  return typeof val === 'number' && val !== 0 ? formatUSDPerMTok(val) : '';
}

/** 生成生效价格来源的中文说明标签。 */
function effectiveSourceText(source: EffectivePriceSource): string {
  switch (source) {
    case 'provider_exact':
      return '当前 Provider 专属价';
    case 'provider_wildcard':
      return '当前 Provider 默认 (*)';
    case 'global_exact':
      return '全局精确价 (*)';
    case 'global_wildcard':
      return '全局默认 (*)';
    default:
      return '未设价（统计显示「未计价」）';
  }
}

/**
 * ModelContextDialog 是模型列表点击「设置」后的综合设置弹窗：
 * 1) 上下文窗口（token 数），支持快捷档位或退回自动；
 * 2) 成本账本单价（仅管理员可见），支持切换「全局 *」与「仅当前 Provider」作用域并配置 USD/MTok 单价。
 */
export function ModelContextDialog({
  provider,
  model,
  isAdmin = false,
  mergedCount = 1,
  busy,
  onSave,
  onPriceSaved,
  onClose,
}: {
  provider?: Provider;
  model: Model;
  /** 仅管理员允许读取并修改 /__prices 价格表。 */
  isAdmin?: boolean;
  /** 跨账号合并展示时，这个模型覆盖的账号数。 */
  mergedCount?: number;
  busy: boolean;
  /** 保存上下文窗口覆盖值；返回 true 表示保存成功。 */
  onSave: (contextLength: number) => Promise<boolean> | boolean;
  /** 价格保存或删除成功后触发刷新统计或提示。 */
  onPriceSaved?: (message: string) => void;
  onClose: () => void;
}) {
  const current = model.contextOverride || 0;
  const [value, setValue] = React.useState(current > 0 ? String(current) : '');
  const inputRef = React.useRef<HTMLInputElement | null>(null);

  const normalizedModel = React.useMemo(() => upstreamModelForPricing(model.id), [model.id]);
  const [prices, setPrices] = React.useState<ModelUSDPrice[] | null>(null);
  const [priceLoadError, setPriceLoadError] = React.useState('');
  const [priceSaveError, setPriceSaveError] = React.useState('');
  const [priceScope, setPriceScope] = React.useState<string>(GLOBAL_SCOPE);
  const [rates, setRates] = React.useState<Record<RateKey, string>>({
    inputUsdPerMTok: '',
    outputUsdPerMTok: '',
    cacheReadUsdPerMTok: '',
    cacheWriteUsdPerMTok: '',
  });
  const [priceBusy, setPriceBusy] = React.useState(false);
  const scopeInitializedRef = React.useRef(false);

  React.useEffect(() => {
    inputRef.current?.focus();
    inputRef.current?.select();
  }, []);

  // 读取当前价格表用于回显有效单价与作用域。
  const reloadPrices = React.useCallback(async () => {
    if (!isAdmin) return;
    try {
      const list = await readPrices();
      setPrices(list);
      setPriceLoadError('');
      return list;
    } catch (err) {
      setPriceLoadError(err instanceof Error ? err.message : String(err));
      return null;
    }
  }, [isAdmin]);

  React.useEffect(() => {
    if (!isAdmin) return;
    let active = true;
    void (async () => {
      try {
        const list = await readPrices();
        if (!active) return;
        setPrices(list);
        setPriceLoadError('');
        if (!scopeInitializedRef.current) {
          scopeInitializedRef.current = true;
          const providerExact = model.providerId
            ? list.find(item => item.providerId === model.providerId && item.modelId === normalizedModel)
            : undefined;
          // 单账号且已有当前 Provider 专属覆盖行时默认选中该 Provider；其余（含跨账号合并）默认全局 *。
          const defaultScope = mergedCount <= 1 && providerExact ? model.providerId : GLOBAL_SCOPE;
          setPriceScope(defaultScope);
          const row = list.find(item => item.providerId === defaultScope && item.modelId === normalizedModel);
          setRates({
            inputUsdPerMTok: formatRateInput(row, 'inputUsdPerMTok'),
            outputUsdPerMTok: formatRateInput(row, 'outputUsdPerMTok'),
            cacheReadUsdPerMTok: formatRateInput(row, 'cacheReadUsdPerMTok'),
            cacheWriteUsdPerMTok: formatRateInput(row, 'cacheWriteUsdPerMTok'),
          });
        }
      } catch (err) {
        if (!active) return;
        setPriceLoadError(err instanceof Error ? err.message : String(err));
      }
    })();
    return () => { active = false; };
  }, [isAdmin, mergedCount, model.providerId, normalizedModel]);

  const effective = React.useMemo(
    () => resolveEffectiveModelPrice(prices || [], model.providerId, model.id),
    [prices, model.providerId, model.id],
  );

  const scopedExistingRow = React.useMemo(
    () => (prices || []).find(item => item.providerId === priceScope && item.modelId === normalizedModel),
    [prices, priceScope, normalizedModel],
  );

  /** 切换价格作用域并回显该作用域下已保存的行。 */
  const handleScopeChange = (nextScope: string) => {
    setPriceScope(nextScope);
    setPriceSaveError('');
    const row = (prices || []).find(item => item.providerId === nextScope && item.modelId === normalizedModel);
    setRates({
      inputUsdPerMTok: formatRateInput(row, 'inputUsdPerMTok'),
      outputUsdPerMTok: formatRateInput(row, 'outputUsdPerMTok'),
      cacheReadUsdPerMTok: formatRateInput(row, 'cacheReadUsdPerMTok'),
      cacheWriteUsdPerMTok: formatRateInput(row, 'cacheWriteUsdPerMTok'),
    });
  };

  // 上下文窗口校验
  const trimmed = value.trim().replace(/[,_\s]/g, '');
  const parsed = trimmed === '' ? 0 : Number(trimmed);
  const contextValid = Number.isFinite(parsed) && Number.isInteger(parsed) && parsed >= 0 && parsed <= 10_000_000;
  const nextContext = trimmed === '' ? 0 : parsed;
  const contextDirty = contextValid && nextContext !== current;

  // 价格输入校验
  const parsedRates = React.useMemo(() => {
    const out: Record<RateKey, number> = {
      inputUsdPerMTok: 0,
      outputUsdPerMTok: 0,
      cacheReadUsdPerMTok: 0,
      cacheWriteUsdPerMTok: 0,
    };
    let valid = true;
    for (const field of PRICE_FIELDS) {
      const raw = (rates[field.key] || '').trim();
      const num = raw === '' ? 0 : Number(raw);
      if (!Number.isFinite(num) || num < 0) {
        valid = false;
      }
      out[field.key] = num;
    }
    return { values: out, valid };
  }, [rates]);

  const priceAllZero = PRICE_FIELDS.every(field => parsedRates.values[field.key] === 0);
  const priceHasInput = PRICE_FIELDS.some(field => (rates[field.key] || '').trim() !== '');
  const priceDirty = React.useMemo(() => {
    if (!isAdmin || !prices) return false;
    return PRICE_FIELDS.some(field => {
      const existingVal = scopedExistingRow ? scopedExistingRow[field.key] : 0;
      return parsedRates.values[field.key] !== existingVal;
    });
  }, [isAdmin, prices, scopedExistingRow, parsedRates.values]);

  const globalZeroInvalid = priceScope === GLOBAL_SCOPE && priceDirty && priceAllZero && priceHasInput;
  const clearingByEmpty = Boolean(scopedExistingRow) && priceDirty && !priceHasInput;
  const submitting = busy || priceBusy;
  const canSubmit = !submitting
    && (contextDirty || priceDirty)
    && (!contextDirty || contextValid)
    && (!priceDirty || (parsedRates.valid && !globalZeroInvalid));

  /** 删除当前选中作用域的显式单价行。 */
  const handleDeleteScopedPrice = async () => {
    if (!scopedExistingRow || submitting) return;
    setPriceBusy(true);
    setPriceSaveError('');
    try {
      const response = await fetch(
        `${API_BASE}/__prices/${encodeURIComponent(priceScope)}/${encodeURIComponent(normalizedModel || '*')}`,
        { method: 'DELETE', credentials: 'same-origin' },
      );
      if (!response.ok) throw new Error(`清除价格失败：HTTP ${response.status}`);
      await reloadPrices();
      setRates({
        inputUsdPerMTok: '',
        outputUsdPerMTok: '',
        cacheReadUsdPerMTok: '',
        cacheWriteUsdPerMTok: '',
      });
      onPriceSaved?.(`已清除 ${normalizedModel}（${priceScope === GLOBAL_SCOPE ? '全局' : (provider?.name || priceScope)}）的显式单价`);
    } catch (err) {
      setPriceSaveError(err instanceof Error ? err.message : String(err));
    } finally {
      setPriceBusy(false);
    }
  };

  /** 提交保存已修改的上下文窗口与单价设置。 */
  const submit = async () => {
    if (!canSubmit) return;
    setPriceSaveError('');

    if (contextDirty) {
      const ok = await onSave(nextContext);
      if (!ok) return;
    }

    if (isAdmin && priceDirty) {
      setPriceBusy(true);
      try {
        if (clearingByEmpty) {
          const response = await fetch(
            `${API_BASE}/__prices/${encodeURIComponent(priceScope)}/${encodeURIComponent(normalizedModel || '*')}`,
            { method: 'DELETE', credentials: 'same-origin' },
          );
          if (!response.ok) throw new Error(`清除价格失败：HTTP ${response.status}`);
          onPriceSaved?.(`已清除 ${normalizedModel}（${priceScope === GLOBAL_SCOPE ? '全局' : (provider?.name || priceScope)}）的单价设置`);
        } else {
          if (priceScope === GLOBAL_SCOPE && priceAllZero) {
            setPriceSaveError('全局单价至少需要一个非 0 值；若不想计价请留空或点清除。');
            return;
          }
          const payload: ModelUSDPrice = {
            providerId: priceScope,
            modelId: normalizedModel || '*',
            inputUsdPerMTok: parsedRates.values.inputUsdPerMTok,
            outputUsdPerMTok: parsedRates.values.outputUsdPerMTok,
            cacheReadUsdPerMTok: parsedRates.values.cacheReadUsdPerMTok,
            cacheWriteUsdPerMTok: parsedRates.values.cacheWriteUsdPerMTok,
          };
          const response = await fetch(`${API_BASE}/__prices`, {
            method: 'POST',
            credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload),
          });
          if (!response.ok) throw new Error(`保存价格失败：${(await response.text()).slice(0, 160)}`);
          onPriceSaved?.(`已保存 ${normalizedModel}（${priceScope === GLOBAL_SCOPE ? '全局 *' : (provider?.name || priceScope)}）的美元单价`);
        }
      } catch (err) {
        setPriceSaveError(err instanceof Error ? err.message : String(err));
        return;
      } finally {
        setPriceBusy(false);
      }
    }

    onClose();
  };

  return (
    <Modal
      title="模型设置"
      size={isAdmin ? 'wide' : 'default'}
      description={`${model.name && model.name !== model.id ? `${model.name} · ` : ''}${model.id}`}
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
            <label>上下文窗口（token 数）</label>
            <input
              ref={inputRef}
              type="number"
              min={0}
              max={10_000_000}
              step={1000}
              value={value}
              disabled={submitting}
              placeholder="留空 = 自动"
              onChange={(event) => setValue(event.target.value)}
            />
            <div className="models-filter-group" style={{ marginTop: 8 }}>
              {WINDOW_PRESETS.map((preset) => (
                <button
                  key={preset.value}
                  type="button"
                  className={`models-filter-chip ${nextContext === preset.value ? 'active' : ''}`}
                  disabled={submitting}
                  onClick={() => setValue(String(preset.value))}
                >
                  {preset.label}
                </button>
              ))}
              <button
                type="button"
                className={`models-filter-chip ${trimmed === '' ? 'active' : ''}`}
                disabled={submitting}
                onClick={() => setValue('')}
              >
                自动
              </button>
            </div>
            {!contextValid ? <div className="hint-line error">请填 0–10000000 之间的整数（token 数）。</div> : null}
            <div className="hint-text" style={{ marginTop: 8 }}>
              网关按这个数在 /v1/models 里报给客户端；到了 1M 及以上，Claude Desktop 与 Claude Code 会按 1M 窗口识别。
              留空则按厂商规格或上游声明（当前自动值 {model.contextLength ? model.contextLength.toLocaleString() : '未知'}）。
              {mergedCount > 1
                ? ` 当前为跨 ${mergedCount} 个账号的合并展示，上下文覆盖仅保存至代表账号（${provider?.name || model.providerId}）。`
                : provider ? ` 所属：${provider.name}` : ''}
            </div>
          </div>

          {isAdmin ? (
            <div className="field field-full model-price-section">
              <div className="model-price-head">
                <div>
                  <label>成本账本单价（USD / 百万 Token）</label>
                  <div className="hint-text" style={{ marginTop: 2 }}>
                    当前生效：{effectiveSourceText(effective.source)}
                    {effective.price
                      ? `（输入 ${formatUSDPerMTok(effective.price.inputUsdPerMTok)} / 输出 ${formatUSDPerMTok(effective.price.outputUsdPerMTok)} / 缓存读 ${formatUSDPerMTok(effective.price.cacheReadUsdPerMTok)} / 缓存写 ${formatUSDPerMTok(effective.price.cacheWriteUsdPerMTok)}）`
                      : ''}
                  </div>
                </div>
                {scopedExistingRow ? (
                  <button
                    type="button"
                    className="mini-btn danger"
                    disabled={submitting}
                    onClick={() => void handleDeleteScopedPrice()}
                  >
                    清除此范围单价
                  </button>
                ) : null}
              </div>

              <div className="models-filter-group" style={{ marginTop: 8 }}>
                <button
                  type="button"
                  className={`models-filter-chip ${priceScope === GLOBAL_SCOPE ? 'active' : ''}`}
                  disabled={submitting}
                  onClick={() => handleScopeChange(GLOBAL_SCOPE)}
                >
                  全局（* · 所有 Provider 共用）
                </button>
                <button
                  type="button"
                  className={`models-filter-chip ${priceScope === model.providerId ? 'active' : ''}`}
                  disabled={submitting}
                  onClick={() => handleScopeChange(model.providerId)}
                >
                  仅当前 Provider（{provider?.name || model.providerId}）
                </button>
              </div>

              {priceLoadError ? <div className="hint-line error" style={{ marginTop: 8 }}>{priceLoadError}</div> : null}

              <div className="model-price-grid">
                {PRICE_FIELDS.map(field => (
                  <div className="field" key={field.key}>
                    <label>{field.label}</label>
                    <input
                      type="number"
                      min="0"
                      step="any"
                      disabled={submitting || !prices}
                      placeholder={effective.price ? `继承 ${formatUSDPerMTok(effective.price[field.key])}` : '留空 = 未计价'}
                      title={field.hint}
                      value={rates[field.key]}
                      onChange={event => {
                        const nextVal = event.target.value;
                        setRates(prev => ({ ...prev, [field.key]: nextVal }));
                      }}
                    />
                  </div>
                ))}
              </div>

              {!parsedRates.valid ? <div className="hint-line error">单价必须是不小于 0 的数字。</div> : null}
              {globalZeroInvalid ? <div className="hint-line error">全局单价至少要填一个非 0 值；不想计价请留空或点清除。</div> : null}
              {priceSaveError ? <div className="hint-line error">{priceSaveError}</div> : null}

              <div className="hint-text" style={{ marginTop: 6 }}>
                按模型真实上游 ID「<span className="font-mono">{normalizedModel}</span>」匹配。
                {mergedCount > 1 ? ' 多账号合并视图下建议维护「全局 *」单价，使所有账号自动共用同一套价格。' : ''}
                新配置仅对后续请求生效；如需把今日或历史未计价请求补算入账，可在「用量统计 → 价格表」点击补算。
              </div>
            </div>
          ) : null}
        </div>
        <div className="actions modal-actions">
          <button type="button" className="btn" onClick={onClose}>取消</button>
          <button type="submit" className="btn primary" disabled={!canSubmit}>
            {submitting ? '保存中…' : '保存设置'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

/**
 * ModelContextCell 是模型列表里「上下文长度」那一格：数字 + 是不是运维设的。
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
        <button type="button" className="mini-btn" onClick={onEdit} title="设置上下文窗口与美元单价">设置</button>
      ) : null}
    </span>
  );
}
