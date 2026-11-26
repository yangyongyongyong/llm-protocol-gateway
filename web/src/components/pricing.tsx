import {useEffect, useMemo, useRef, useState} from 'react';
import type {Model, ModelUSDPrice, Provider} from '../types';
import {
  API_BASE,
  formatCostCoverage,
  formatCostMicros,
  formatLocalISODate,
  formatUSDPerMTok,
  providerUsageLabel,
  upstreamModelForPricing,
} from '../lib';
import {providerAccountFace} from './provider_select';
import {Modal} from './ui';
import './usage.css';

/** 价格行的空表单初值：单价留空＝未计价，不是 0。 */
const EMPTY_RATE = '';

/** 未设价清单一次最多列几个，其余只报个数：面板不是用来翻页的。 */
const MISSING_PREVIEW = 8;

/** 全局范围：所有没有自己价格行的 Provider 共用这一套价。 */
const GLOBAL_SCOPE = '*';

/** 四档单价的字段名：收窄成数字键，读出来就不用再判类型。 */
type RateKey = 'inputUsdPerMTok' | 'outputUsdPerMTok' | 'cacheReadUsdPerMTok' | 'cacheWriteUsdPerMTok';
type RateField = { key: RateKey; label: string; hint: string };

// 四档单价的顺序与含义要和后端 costOf 的算法一致：input 只算未命中缓存的那部分，
// 否则缓存 token 会被按全价重复计一次。
const RATE_FIELDS: RateField[] = [
  { key: 'inputUsdPerMTok', label: '输入（未缓存）', hint: '每百万 Token 的美元价。只算没有命中缓存的 prompt，缓存读取/写入另有单价。' },
  { key: 'outputUsdPerMTok', label: '输出', hint: '每百万生成 Token 的美元价。' },
  { key: 'cacheReadUsdPerMTok', label: '缓存读取', hint: '每百万缓存命中 Token 的美元价，通常是输入价的 1/10。' },
  { key: 'cacheWriteUsdPerMTok', label: '缓存写入', hint: '每百万缓存创建 Token 的美元价，一般比输入价略高。' },
];

/** 把已有价格行中的某个单价转换成表单字符串，保留完整有效小数。 */
function rateValue(price: ModelUSDPrice | null, field: RateField): string {
  if (!price) return EMPTY_RATE;
  const value = price[field.key];
  return typeof value === 'number' && value !== 0 ? formatUSDPerMTok(value) : EMPTY_RATE;
}

/** 读取网关持久化的模型价格表。 */
export async function readPrices(): Promise<ModelUSDPrice[]> {
  const response = await fetch(`${API_BASE}/__prices`, { credentials: 'same-origin' });
  if (!response.ok) throw new Error(`读取价格表失败：HTTP ${response.status}`);
  const data = (await response.json()) as { prices?: ModelUSDPrice[] };
  return data.prices || [];
}

/**
 * 价格表面板：网关成本账本的录入、精确展示与未计价历史补算面板。
 *
 * 这里的单价是运维按厂商价目表手工填的，网关不会替我们猜：猜错的价格会被悄悄加进
 * 总额，比留空（显示「未计价」）更危险。改价只影响之后的请求，已计价历史不重写；
 * 若此前存在 priced=0 的未计价请求，可通过面板底部的补算入口按当前价格补记成本。
 */
export function PriceTablePanel({
  providers,
  models,
  modelCoverage,
  initialTarget,
  defaultFrom,
  defaultTo,
  onBackfilled,
  onPricesChanged,
  onClose,
}: {
  providers: Provider[];
  models: Model[];
  /** 区间内按模型汇总的请求数与已计价请求数，用来说「还有多少没设价」。 */
  modelCoverage: Array<{ model: string; requestCount: number; pricedCount: number }>;
  /** 可选：从成本报表或模型列表点击设价时预填的目标 Provider 与模型。 */
  initialTarget?: { providerId?: string; modelId: string } | null;
  /** 可选：补算区间默认起始日期（YYYY-MM-DD）。 */
  defaultFrom?: string;
  /** 可选：补算区间默认结束日期（YYYY-MM-DD）。 */
  defaultTo?: string;
  /** 补算完成后触发外部刷新统计数据。 */
  onBackfilled?: () => void;
  /** 价格行新增/删除后通知外部同步刷新。 */
  onPricesChanged?: () => void;
  onClose: () => void;
}) {
  const [prices, setPrices] = useState<ModelUSDPrice[] | null>(null);
  const [error, setError] = useState('');
  const todayISO = useMemo(() => formatLocalISODate(new Date()), []);
  // 默认落在第一个具体 Provider 上，而不是「全局 + *」：全局行是所有 Provider 的
  // 最后一道兜底，误存全 0 的全局行会把整个账本未计价模型变成「已计价 $0」。
  const [providerId, setProviderId] = useState(() => providers[0]?.id || GLOBAL_SCOPE);
  const [modelId, setModelId] = useState('*');
  const [rates, setRates] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState(false);
  // 正在编辑的行键（provider/model）；为空表示新增。
  const [editingKey, setEditingKey] = useState('');

  // 补算未计价请求状态
  const [backfillFrom, setBackfillFrom] = useState(() => defaultFrom || todayISO);
  const [backfillTo, setBackfillTo] = useState(() => defaultTo || todayISO);
  const [backfilling, setBackfilling] = useState(false);
  const [backfillMessage, setBackfillMessage] = useState('');
  const [backfillError, setBackfillError] = useState('');
  const appliedInitialRef = useRef(false);

  const reload = async () => {
    try {
      setPrices(await readPrices());
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  useEffect(() => {
    void reload();
  }, []);

  // 从外部跳入时，在首次加载价格表后智能回显已有覆盖行或预填目标模型。
  useEffect(() => {
    if (!prices || !initialTarget || appliedInitialRef.current) return;
    appliedInitialRef.current = true;
    const normalizedModel = upstreamModelForPricing(initialTarget.modelId);
    const targetProvider = (initialTarget.providerId || '').trim();
    const providerExact = targetProvider
      ? prices.find(item => item.providerId === targetProvider && item.modelId === normalizedModel)
      : undefined;
    const globalExact = prices.find(item => item.providerId === GLOBAL_SCOPE && item.modelId === normalizedModel);
    if (providerExact) {
      loadIntoForm(providerExact);
    } else if (globalExact) {
      loadIntoForm(globalExact);
    } else {
      setProviderId(GLOBAL_SCOPE);
      setModelId(normalizedModel || '*');
      setRates({});
      setEditingKey('');
    }
  }, [prices, initialTarget]);

  const scopeModels = useMemo(() => {
    if (providerId === GLOBAL_SCOPE) {
      const seen = new Set<string>();
      return models.map(model => model.id).filter(id => {
        if (seen.has(id)) return false;
        seen.add(id);
        return true;
      });
    }
    return models.filter(model => model.providerId === providerId).map(model => model.id);
  }, [models, providerId]);

  const rows = useMemo(() => [...(prices || [])].sort((a, b) => {
    if ((a.providerId === GLOBAL_SCOPE) !== (b.providerId === GLOBAL_SCOPE)) {
      return a.providerId === GLOBAL_SCOPE ? -1 : 1;
    }
    if (a.providerId !== b.providerId) return a.providerId.localeCompare(b.providerId);
    return a.modelId.localeCompare(b.modelId);
  }), [prices]);

  // 未设价清单按「上游模型」聚合：别名和真名指同一个模型，不能各报一条。
  const missing = useMemo(() => {
    const byModel = new Map<string, { model: string; requestCount: number; pricedCount: number }>();
    for (const item of modelCoverage) {
      const model = upstreamModelForPricing(item.model || '');
      if (!model) continue;
      const current = byModel.get(model) || { model, requestCount: 0, pricedCount: 0 };
      current.requestCount += item.requestCount || 0;
      current.pricedCount += item.pricedCount || 0;
      byModel.set(model, current);
    }
    return [...byModel.values()]
      .filter(item => item.requestCount > item.pricedCount)
      .sort((a, b) => (b.requestCount - b.pricedCount) - (a.requestCount - a.pricedCount));
  }, [modelCoverage]);

  function scopeKnown(id: string) {
    return id === GLOBAL_SCOPE || providers.some(provider => provider.id === id);
  }

  function scopeLabel(id: string) {
    if (id === GLOBAL_SCOPE) return '全局';
    return scopeKnown(id) ? providerUsageLabel(id, providers) : `${id}（已删除）`;
  }

  function loadIntoForm(price: ModelUSDPrice) {
    setProviderId(price.providerId);
    setModelId(price.modelId || '*');
    setRates(Object.fromEntries(RATE_FIELDS.map(field => [field.key, rateValue(price, field)])));
    setEditingKey(`${price.providerId}/${price.modelId}`);
  }

  function clearForm() {
    setRates({});
    setEditingKey('');
    setModelId('*');
  }

  function startMissing(model: string) {
    setProviderId(GLOBAL_SCOPE);
    setModelId(model);
    setRates({});
    setEditingKey('');
  }

  const scoped = providerId === GLOBAL_SCOPE;
  const globalZeroRow = scoped && RATE_FIELDS.every(field => Number(rates[field.key] || 0) === 0);

  async function save() {
    if (!providerId) {
      setError('先选择范围再保存价格。');
      return;
    }
    const payload: ModelUSDPrice = {
      providerId,
      modelId: modelId.trim() || '*',
      inputUsdPerMTok: Number(rates.inputUsdPerMTok || 0),
      outputUsdPerMTok: Number(rates.outputUsdPerMTok || 0),
      cacheReadUsdPerMTok: Number(rates.cacheReadUsdPerMTok || 0),
      cacheWriteUsdPerMTok: Number(rates.cacheWriteUsdPerMTok || 0),
    };
    if (RATE_FIELDS.some(field => Number.isNaN(Number(rates[field.key] || 0)) || Number(rates[field.key] || 0) < 0)) {
      setError('单价必须是不小于 0 的数字；不设价就留空或删掉这一行。');
      return;
    }
    if (globalZeroRow) {
      setError('全局行至少要填一个非 0 单价。全 0 的全局行会把所有未计价的模型算成「已计价 $0」，掩盖「未计价」提示；如果本来就不想计价，请不要保存这一行。');
      return;
    }
    setSaving(true);
    setError('');
    try {
      const response = await fetch(`${API_BASE}/__prices`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
      if (!response.ok) throw new Error(`保存失败：${(await response.text()).slice(0, 160)}`);
      await reload();
      clearForm();
      onPricesChanged?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  }

  async function remove(price: ModelUSDPrice) {
    setError('');
    try {
      const response = await fetch(
        `${API_BASE}/__prices/${encodeURIComponent(price.providerId)}/${encodeURIComponent(price.modelId || '*')}`,
        { method: 'DELETE', credentials: 'same-origin' },
      );
      if (!response.ok) throw new Error(`删除失败：HTTP ${response.status}`);
      await reload();
      if (editingKey === `${price.providerId}/${price.modelId}`) clearForm();
      onPricesChanged?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  /** 按当前价格表补算指定日期区间内 priced=0 的历史请求。 */
  async function runBackfill() {
    setBackfillError('');
    setBackfillMessage('');
    if (!backfillFrom || !backfillTo || backfillFrom > backfillTo) {
      setBackfillError('请选择有效的起止日期（开始日期不能晚于结束日期）。');
      return;
    }
    setBackfilling(true);
    try {
      const response = await fetch(`${API_BASE}/__prices/backfill`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ from: backfillFrom, to: backfillTo }),
      });
      if (!response.ok) {
        throw new Error(`补算失败：${(await response.text()).slice(0, 200)}`);
      }
      const data = (await response.json()) as { updated?: number; costMicros?: number; from?: string; to?: string };
      const updated = data.updated || 0;
      const costText = formatCostMicros(data.costMicros || 0);
      if (updated > 0) {
        setBackfillMessage(`已按当前价格补算 ${updated.toLocaleString()} 次未计价请求，新增记账 ${costText}（已刷新统计）。`);
        onBackfilled?.();
      } else {
        setBackfillMessage('所选区间内没有符合当前价格表的未计价（priced=0）请求。');
      }
    } catch (err) {
      setBackfillError(err instanceof Error ? err.message : String(err));
    } finally {
      setBackfilling(false);
    }
  }

  return (
    <Modal title="价格表 · 成本账本" size="wide" onClose={onClose}
      description="按厂商价目表填美元/百万 Token 单价。全局价对所有没有自己价格行的 Provider 生效，官方 Claude 的多个账号只需维护一套。没有价格行的 Provider 与模型一律显示「未计价」，不会算成 $0；订阅制（按积分计费）的上游建议留空。改价只影响之后的请求，已计价历史不重写。">
      <div className="price-panel">
        {!!missing.length && (
          <div className="price-missing">
            <div className="price-missing-title">本区间有请求但未计价（按模型汇总）</div>
            {missing.slice(0, MISSING_PREVIEW).map(item => (
              <div className="price-missing-row" key={item.model}>
                <span className="font-mono">{item.model}</span>
                <small>{(item.requestCount - item.pricedCount).toLocaleString()} 次请求未计价</small>
                <button type="button" className="mini-btn" onClick={() => startMissing(item.model)}>设全局价</button>
              </div>
            ))}
            {missing.length > MISSING_PREVIEW ? (
              <div className="price-missing-more">另有 {missing.length - MISSING_PREVIEW} 个模型在本区间也没有价格行。</div>
            ) : null}
          </div>
        )}

        <div className="price-editor">
          <div className="field">
            <label>范围</label>
            <select value={providerId} onChange={event => { setProviderId(event.target.value); setModelId('*'); }}>
              <option value={GLOBAL_SCOPE}>全局 · 所有 Provider 共用</option>
              {providers.map(provider => <option key={provider.id} value={provider.id}>{providerAccountFace(provider)}</option>)}
            </select>
          </div>
          <div className="field">
            <label>模型</label>
            <input list="price-model-options" value={modelId} placeholder="*（该范围的默认价）"
              onChange={event => { setModelId(event.target.value); }} />
            <datalist id="price-model-options">
              <option value="*" />
              {scopeModels.map(id => <option key={id} value={id} />)}
            </datalist>
          </div>
          {RATE_FIELDS.map(field => (
            <div className="field" key={field.key}>
              <label>{field.label} USD/MTok</label>
              <input type="number" min="0" step="any"
                value={rates[field.key] || EMPTY_RATE} title={field.hint}
                onChange={event => setRates(current => ({ ...current, [field.key]: event.target.value }))} />
            </div>
          ))}
          {globalZeroRow ? (
            <div className="hint-line error">全局行至少要填一个非 0 单价；全 0 的全局行会把所有未计价模型算成「已计价 $0」。</div>
          ) : null}
          <div className="price-editor-actions">
            <button type="button" className="btn primary" disabled={saving || globalZeroRow} onClick={() => void save()}>
              {saving ? '保存中…' : editingKey ? '更新这一行' : '保存价格'}
            </button>
            {editingKey ? <button type="button" className="btn" onClick={clearForm}>取消编辑</button> : null}
          </div>
        </div>

        {error ? <div className="hint-line error">{error}</div> : null}

        <div className="price-list">
          <div className="price-row price-row-head">
            <span>范围</span><span>模型</span><span>输入</span><span>输出</span><span>缓存读</span><span>缓存写</span><span>操作</span>
          </div>
          {!prices ? <div className="empty-state compact">正在读取价格表…</div> : !rows.length
            ? <div className="empty-state compact">还没有任何价格行，所有请求都会记为未计价。</div>
            : rows.map(price => (
              <div className="price-row" key={`${price.providerId}/${price.modelId}`}>
                <span title={price.providerId} className={price.providerId === GLOBAL_SCOPE ? 'price-scope-global' : undefined}>
                  {scopeLabel(price.providerId)}
                </span>
                <span className="font-mono">{price.modelId === '*' ? '默认（*）' : price.modelId}</span>
                {RATE_FIELDS.map(field => (
                  <span className="num" key={field.key} title={price[field.key] ? `${price[field.key]} USD/MTok` : undefined}>
                    {formatUSDPerMTok(price[field.key])}
                  </span>
                ))}
                <span className="price-row-actions">
                  {scopeKnown(price.providerId)
                    ? <button type="button" className="mini-btn" onClick={() => loadIntoForm(price)}>编辑</button>
                    : null}
                  <button type="button" className="mini-btn danger" onClick={() => void remove(price)}>删除</button>
                </span>
              </div>
            ))}
        </div>

        <div className="price-backfill-bar">
          <div className="price-backfill-copy">
            <strong>按当前价格补算未计价请求</strong>
            <small>只补算保留日志里尚未计价（priced=0）的请求并同步刷新日聚合；已计价的历史请求保持不变。</small>
          </div>
          <div className="price-backfill-controls">
            <input type="date" aria-label="补算开始日期" value={backfillFrom} max={todayISO}
              onChange={event => setBackfillFrom(event.target.value)} />
            <span>至</span>
            <input type="date" aria-label="补算结束日期" value={backfillTo} max={todayISO}
              onChange={event => setBackfillTo(event.target.value)} />
            <button type="button" className="btn" disabled={backfilling} onClick={() => void runBackfill()}>
              {backfilling ? '补算中…' : '补算区间未计价'}
            </button>
          </div>
          {backfillMessage ? <div className="hint-line ok">{backfillMessage}</div> : null}
          {backfillError ? <div className="hint-line error">{backfillError}</div> : null}
        </div>

        <p className="usage-note">
          查找顺序：该 Provider 的精确模型 → 该 Provider 的默认（*）→ 全局精确模型 → 全局默认（*）。
          想给官方 Claude 的所有账号用同一套价，就在「全局」下按模型各存一行；某个中转有
          自己的价格时，再给它单独存一行覆盖。
        </p>
        <p className="usage-note">
          输入价只作用于未命中缓存的 prompt：缓存读取与缓存写入的 Token 已经算在输入总量里，
          再按输入价计一次就翻倍了。成本以微美元整数逐请求落库，日聚合等于请求之和。
        </p>
        {rows.length ? (
          <p className="usage-note">
            共 {rows.length} 行价格 ·{' '}
            {formatCostCoverage(
              modelCoverage.reduce((sum, item) => sum + (item.pricedCount || 0), 0),
              modelCoverage.reduce((sum, item) => sum + (item.requestCount || 0), 0),
            )}
          </p>
        ) : null}
      </div>
    </Modal>
  );
}
