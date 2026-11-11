import {peerProviderMemberName, peerUpstreamName, providerAccountLabel, providerConnectKind, providerOptionLabel, shortenAccountLabel} from '../lib';
import {resolveProviderIcon} from '../provider_presets';
import type {Provider} from '../types';
import {BrandIcon} from './ui';

/**
 * Provider 的统一署名：品牌图标 + 名称 +（可选）账号标识。
 *
 * 「输入 Provider」卡片早就带图标了，但模型列表的筛选项、密钥的路由与方案、
 * API 日志的 Provider 列都还是光秃秃一行字——同一个服务商在七个地方长得都不
 * 一样，认厂商只能靠读字。这里收一个组件，各处都用它。
 *
 * 账号标识解决的是另一半问题：同一个服务商每多登一个账号就多一条同名记录，
 * 只读名字分不出是哪条，所以在名字后面补一段昵称 / 邮箱 / 账号 id 尾段。
 */
export function ProviderLabel({ provider, size = 16, protocol, account = true, stack = false, className = '' }: {
  provider: Provider;
  size?: number;
  /** 需要时把协议跟在名字后面，省得每处自己拼 Badge。 */
  protocol?: string;
  /** 关掉账号标识：宽度实在挤不动的场合（窄胶囊）用。 */
  account?: boolean;
  /** 名字与账号分两行：API 日志那种一列要装两件事的窄格子。 */
  stack?: boolean;
  className?: string;
}) {
  // 上游网关成员行：名字那格写成员名，第二格写它属于哪条上游（与账号同位置）。
  const peer = providerConnectKind(provider) === 'peer';
  const accountLabel = account
    ? (peer ? peerUpstreamName(provider) : providerAccountLabel(provider))
    : '';
  const showAccount = Boolean(accountLabel) && accountLabel !== provider.name;
  const name = peer ? peerProviderMemberName(provider) : provider.name;
  return (
    <span
      className={`provider-label${stack ? ' stacked' : ''}${className ? ` ${className}` : ''}`}
      title={showAccount ? `${providerOptionLabel(provider)} · ${accountLabel}` : providerOptionLabel(provider)}
    >
      <BrandIcon icon={resolveProviderIcon(provider)} name={provider.name} size={size} />
      <span className="provider-label-name">{name}</span>
      {showAccount ? <span className="provider-label-account">{shortenAccountLabel(accountLabel)}</span> : null}
      {protocol ? <span className="provider-label-protocol">{protocol}</span> : null}
    </span>
  );
}

/**
 * 只有 providerId 的场合（日志、用量这类按 id 聚合的行）：找不到对应 Provider
 * 时退回显示原始 id，图标位用首字占位，不留空洞也不假装知道是谁。
 */
export function ProviderRefLabel({ providerId, providers, size = 16 }: {
  providerId?: string;
  providers: Provider[];
  size?: number;
}) {
  const provider = providers.find((item) => item.id === providerId);
  if (!provider) {
    return <span className="provider-label missing" title={providerId || '未知 Provider'}>{providerId || '未知'}</span>;
  }
  return <ProviderLabel provider={provider} size={size} />;
}

/** 给筛选下拉之类的纯数据选项用的图标字段：与 ProviderLabel 同一张图标表。 */
export function providerIcon(provider: Provider): string {
  return resolveProviderIcon(provider);
}
