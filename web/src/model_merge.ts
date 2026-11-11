// 模型页「同名模型跨账号合并」的纯函数单测：页面逻辑抽成 mergeModelsForDisplay，
// 这里直接喂数据验证——同一服务商多个账号列出同一批模型时只留一条，并记住来源账号。
import type {Model, Provider} from './types';

export type MergedModel = Model & {providers?: Provider[]};

/**
 * 把同名（大小写不敏感）模型合并成一条：保留第一条的位置与价格信息，上下文取
 * 各账号里的最大值，来源账号按出现顺序累积。未合并时原样返回。
 *
 * 比的是「对外宣称的窗口」（设过就是设的值）：否则一个手动标成 1M 的账号会被
 * 另一个报 128K 的账号盖掉，合并出来的那条还显示 128K。
 */
export function mergeModelsAcrossAccounts(models: Model[], providersById: Map<string, Provider>): MergedModel[] {
  const merged = new Map<string, MergedModel>();
  const declaredWindow = (model: Model) => model.contextOverride || model.contextLength || 0;
  for (const model of models) {
    const key = model.id.trim().toLowerCase();
    const provider = providersById.get(model.providerId);
    const existing = merged.get(key);
    if (!existing) {
      merged.set(key, {...model, providers: provider ? [provider] : []});
      continue;
    }
    if (provider && !(existing.providers || []).some((item) => item.id === provider.id)) {
      existing.providers = [...(existing.providers || []), provider];
    }
    if (declaredWindow(model) > declaredWindow(existing)) {
      existing.contextLength = model.contextLength || 0;
      existing.contextOverride = model.contextOverride || 0;
    }
    if (!existing.rate && !existing.free) {
      existing.free = model.free;
      existing.rate = model.rate;
      existing.rateWas = model.rateWas;
    }
  }
  return [...merged.values()];
}
