package gateway

import (
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// normalizeUsageModelIdentities folds client-side provider-pinned model ids
// ("qoder-cn/dfmodel") onto the id the provider catalog carries. Rows keep
// their provider icon and merge with the bare id instead of ranking as a
// separate, icon-less model.
func (s *Server) normalizeUsageModelIdentities(snapshot *monitor.UsageStatsSnapshot) {
	if snapshot == nil {
		return
	}
	catalog := s.usageCatalogModelIDs()
	if len(catalog) == 0 {
		return
	}
	canonical := func(model string) string {
		model = strings.TrimSpace(model)
		if model == "" {
			return model
		}
		if _, ok := catalog[strings.ToLower(model)]; ok {
			return model
		}
		i := strings.LastIndex(model, "/")
		if i <= 0 || i == len(model)-1 {
			return model
		}
		tail := strings.TrimSpace(model[i+1:])
		if id, ok := catalog[strings.ToLower(tail)]; ok {
			return id
		}
		return model
	}
	snapshot.Today.ByModel = monitor.MergeModelDayStats(snapshot.Today.ByModel, canonical)
	snapshot.Today.ByProviderModel = monitor.MergeProviderModelDayStats(snapshot.Today.ByProviderModel, canonical)
	snapshot.Month.ByModel = monitor.MergeModelDayStats(snapshot.Month.ByModel, canonical)
	snapshot.Month.ByProviderModel = monitor.MergeProviderModelDayStats(snapshot.Month.ByProviderModel, canonical)
	if snapshot.Range != nil {
		snapshot.Range.ByModel = monitor.MergeModelDayStats(snapshot.Range.ByModel, canonical)
		snapshot.Range.ByProviderModel = monitor.MergeProviderModelDayStats(snapshot.Range.ByProviderModel, canonical)
	}
}

func (s *Server) usageCatalogModelIDs() map[string]string {
	ids := map[string]string{}
	for _, provider := range s.router.State().Providers {
		for _, model := range provider.Models {
			id := strings.TrimSpace(model.ID)
			if id == "" {
				continue
			}
			lower := strings.ToLower(id)
			if _, ok := ids[lower]; !ok {
				ids[lower] = id
			}
		}
	}
	return ids
}
