package monitor

import (
	"strings"
	"time"
)

// Key dimensions survive request-log retention and can be safely combined only
// after selecting the keys visible to the authenticated user.
func recordKeyUsage(day *usageDayStats, keyID string, event UsageEvent, input, cache int64) {
	child := day.byKey[keyID]
	if child == nil {
		child = newUsageDayStats()
		day.byKey[keyID] = child
	}
	child.total.RequestCount++
	child.total.InputTokens += input
	child.total.OutputTokens += event.OutputTokens
	child.total.CacheTokens += cache
	child.total.CacheWriteTokens += event.CacheWriteTokens
	child.total.CostMicros += event.CostMicros
	if event.Priced {
		child.total.PricedCount++
	}
	child.total.RxBytes += event.RxBytes
	child.total.TxBytes += event.TxBytes
	child.latencySum += event.LatencyMs
	if event.TTFTMs > 0 {
		child.ttftSum += event.TTFTMs
		child.ttftCount++
	}
	switch statusBucket(event.Status) {
	case "2xx":
		child.status2xx++
	case "4xx":
		child.status4xx++
	case "5xx":
		child.status5xx++
	default:
		child.statusOther++
	}
	provider := normalizeProviderForStats(event.ProviderID)
	if child.byProvider[provider] == nil {
		child.byProvider[provider] = &ProviderDayStats{ProviderID: provider}
	}
	p := child.byProvider[provider]
	p.RequestCount++
	p.InputTokens += input
	p.OutputTokens += event.OutputTokens
	p.CacheTokens += cache
	p.CacheWriteTokens += event.CacheWriteTokens
	p.CostMicros += event.CostMicros
	if event.Priced {
		p.PricedCount++
	}
	if model, ok := NormalizeModelForStats(event.Model); ok {
		if child.byModel[model] == nil {
			child.byModel[model] = &ModelDayStats{Model: model}
		}
		m := child.byModel[model]
		m.RequestCount++
		m.InputTokens += input
		m.OutputTokens += event.OutputTokens
		m.CacheTokens += cache
		m.CacheWriteTokens += event.CacheWriteTokens
		m.CostMicros += event.CostMicros
		if event.Priced {
			m.PricedCount++
		}
	}
	if protocol := event.OutputProtocol; protocol != "" {
		if child.byProtocol[protocol] == nil {
			child.byProtocol[protocol] = &ProtocolDayStats{Protocol: protocol}
		}
		p := child.byProtocol[protocol]
		p.RequestCount++
		p.InputTokens += input
		p.OutputTokens += event.OutputTokens
		p.CacheTokens += cache
		p.CacheWriteTokens += event.CacheWriteTokens
		p.CostMicros += event.CostMicros
		if event.Priced {
			p.PricedCount++
		}
	}
	// 下游网关流量也要挂到 Key 维度下，普通用户按自己名下 Key 统计时才能
	// 看到“我的哪个下游网关用了多少”，同时不会串到别人的 Key。
	if gatewayID := strings.TrimSpace(event.PeerGatewayID); gatewayID != "" && strings.EqualFold(strings.TrimSpace(event.PeerDirection), "ingress") {
		if child.byDownstream[gatewayID] == nil {
			child.byDownstream[gatewayID] = &DownstreamGatewayDayStats{GatewayID: gatewayID}
		}
		d := child.byDownstream[gatewayID]
		d.RequestCount++
		d.InputTokens += input
		d.OutputTokens += event.OutputTokens
		d.CacheTokens += cache
		d.CacheWriteTokens += event.CacheWriteTokens
		d.CostMicros += event.CostMicros
		if event.Priced {
			d.PricedCount++
		}
	}
}

func usageDayFromBuckets(b UsageDayBuckets) *usageDayStats {
	day := newUsageDayStats()
	day.total = b.Total
	day.status2xx = b.Status2xx
	day.status4xx = b.Status4xx
	day.status5xx = b.Status5xx
	day.statusOther = b.StatusOther
	day.latencySum = b.LatencySum
	day.ttftSum = b.TTFTSum
	day.ttftCount = b.TTFTCount
	for id, v := range b.ByProvider {
		copied := v
		day.byProvider[id] = &copied
	}
	for id, v := range b.ByModel {
		copied := v
		day.byModel[id] = &copied
	}
	for id, v := range b.ByProtocol {
		copied := v
		day.byProtocol[id] = &copied
	}
	for id, v := range b.ByDownstreamGateway {
		copied := v
		day.byDownstream[id] = &copied
	}
	return day
}

func keyDimensionDays(days map[string]*usageDayStats, keys map[string]struct{}) map[string]*usageDayStats {
	selected := make(map[string]*usageDayStats, len(days))
	for date, day := range days {
		merged := newUsageDayStats()
		for key := range keys {
			child := day.byKey[key]
			if child == nil {
				continue
			}
			merged.status2xx += child.status2xx
			merged.status4xx += child.status4xx
			merged.status5xx += child.status5xx
			merged.statusOther += child.statusOther
			merged.latencySum += child.latencySum
			merged.ttftSum += child.ttftSum
			merged.ttftCount += child.ttftCount
			for id, v := range child.byProvider {
				if merged.byProvider[id] == nil {
					merged.byProvider[id] = &ProviderDayStats{ProviderID: id}
				}
				p := merged.byProvider[id]
				p.RequestCount += v.RequestCount
				p.InputTokens += v.InputTokens
				p.OutputTokens += v.OutputTokens
				p.CacheTokens += v.CacheTokens
				p.CacheWriteTokens += v.CacheWriteTokens
				p.CostMicros += v.CostMicros
				p.PricedCount += v.PricedCount
			}
			for id, v := range child.byModel {
				if merged.byModel[id] == nil {
					merged.byModel[id] = &ModelDayStats{Model: id}
				}
				m := merged.byModel[id]
				m.RequestCount += v.RequestCount
				m.InputTokens += v.InputTokens
				m.OutputTokens += v.OutputTokens
				m.CacheTokens += v.CacheTokens
				m.CacheWriteTokens += v.CacheWriteTokens
				m.CostMicros += v.CostMicros
				m.PricedCount += v.PricedCount
			}
			for id, v := range child.byProtocol {
				if merged.byProtocol[id] == nil {
					merged.byProtocol[id] = &ProtocolDayStats{Protocol: id}
				}
				p := merged.byProtocol[id]
				p.RequestCount += v.RequestCount
				p.InputTokens += v.InputTokens
				p.OutputTokens += v.OutputTokens
				p.CacheTokens += v.CacheTokens
				p.CacheWriteTokens += v.CacheWriteTokens
				p.CostMicros += v.CostMicros
				p.PricedCount += v.PricedCount
			}
			for id, v := range child.byDownstream {
				if merged.byDownstream[id] == nil {
					merged.byDownstream[id] = &DownstreamGatewayDayStats{GatewayID: id}
				}
				d := merged.byDownstream[id]
				d.RequestCount += v.RequestCount
				d.InputTokens += v.InputTokens
				d.OutputTokens += v.OutputTokens
				d.CacheTokens += v.CacheTokens
				d.CacheWriteTokens += v.CacheWriteTokens
				d.CostMicros += v.CostMicros
				d.PricedCount += v.PricedCount
			}
		}
		selected[date] = merged
	}
	return selected
}

// TodayProviderStatsForKeys returns one provider breakdown for the selected
// keys. It reads the already-persisted key×provider dimensions, so callers do
// not need to scan request logs or maintain a second aggregate.
func (s *Store) TodayProviderStatsForKeys(now time.Time, keyIDs []string) []ProviderDayStats {
	localNow := now.Local()
	dayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, localNow.Location())
	keySet := make(map[string]struct{}, len(keyIDs))
	for _, id := range keyIDs {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			keySet[trimmed] = struct{}{}
		}
	}
	if len(keySet) == 0 {
		return nil
	}
	s.mu.RLock()
	selectedDays := keyDimensionDays(s.usageByDay, keySet)
	_, _, providers, _, _ := mergeUsageDays(selectedDays, dayStart, dayStart.AddDate(0, 0, 1))
	s.mu.RUnlock()
	return sortProviderStats(providers)
}

func keyPeriodDimensions(days map[string]*usageDayStats, from, to time.Time, period *PeriodStatsSnapshot) {
	_, _, providers, models, _ := mergeUsageDays(days, from, to)
	period.ByProvider = sortProviderStats(providers)
	period.ByModel = sortModelDayStats(models)
	period.ByProtocol = sortProtocolStats(mergeProtocolDays(days, from, to))
	period.ByDownstreamGateway = sortDownstreamGatewayStats(mergeDownstreamGatewayDays(days, from, to))
}
