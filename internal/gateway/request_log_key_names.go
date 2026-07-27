package gateway

import (
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
)

// request_logs / usage buckets persist the API key *name* as a snapshot taken
// when the request was served, so renaming a key used to leave stale names in
// the console. Key IDs are immutable, so names are re-resolved from the live
// router state on read — same contract as fillUsageUserNames does for user
// renames: whatever the API keys page shows is what the logs show.
//
// The stored snapshot survives only as a fallback for IDs with no live key
// (deleted keys, self-check pseudo keys), which have no current name to show.
func currentAPIKeyNamesByID(keys []domain.APIKey) map[string]string {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if name := strings.TrimSpace(key.Name); name != "" {
			out[key.ID] = name
		}
	}
	return out
}

// fillRequestLogKeyNames rewrites each log row's key name to the key's current name.
func (s *Server) fillRequestLogKeyNames(logs []monitor.RequestLog) {
	if len(logs) == 0 || s.router == nil {
		return
	}
	names := currentAPIKeyNamesByID(s.router.State().APIKeys)
	if len(names) == 0 {
		return
	}
	for i := range logs {
		if name, ok := names[strings.TrimSpace(logs[i].APIKeyID)]; ok {
			logs[i].APIKeyName = name
		}
	}
}

// fillUsageKeyNames does the same for every byApiKey breakdown of a usage snapshot.
func (s *Server) fillUsageKeyNames(snapshot *monitor.UsageStatsSnapshot) {
	if snapshot == nil || s.router == nil {
		return
	}
	names := currentAPIKeyNamesByID(s.router.State().APIKeys)
	if len(names) == 0 {
		return
	}
	rename := func(rows []monitor.APIKeyDayStats) {
		for i := range rows {
			if name, ok := names[strings.TrimSpace(rows[i].APIKeyID)]; ok {
				rows[i].APIKeyName = name
			}
		}
	}
	rename(snapshot.Today.ByAPIKey)
	rename(snapshot.Month.ByAPIKey)
	if snapshot.Range != nil {
		rename(snapshot.Range.ByAPIKey)
	}
}

// resolveLogKeyNameFilter turns the console's by-name log filter into a by-ID
// filter. The "密钥名称" dropdown is populated with current names, but stored
// rows carry the name they had at request time, so a renamed key would only
// match its post-rename history. Matching on the immutable ID fixes that.
//
// Falls back to the raw substring match when no live key matches the text, so
// filtering on a deleted key's historical name still works.
func resolveLogKeyNameFilter(keys []domain.APIKey, query monitor.RequestLogQuery) monitor.RequestLogQuery {
	needle := strings.ToLower(strings.TrimSpace(query.APIKeyName))
	if needle == "" {
		return query
	}
	matched := make([]string, 0, 1)
	for _, key := range keys {
		if strings.Contains(strings.ToLower(strings.TrimSpace(key.Name)), needle) {
			matched = append(matched, key.ID)
		}
	}
	if len(matched) == 0 {
		return query
	}
	query.APIKeyName = ""
	query.APIKeyIDs = intersectKeyIDs(query.APIKeyIDs, matched)
	return query
}

// intersectKeyIDs preserves RequestLogQuery.APIKeyIDs semantics: nil means
// "unrestricted", a non-nil empty slice means "match nothing" (user isolation).
func intersectKeyIDs(current, add []string) []string {
	if current == nil {
		return add
	}
	allowed := make(map[string]struct{}, len(current))
	for _, id := range current {
		allowed[id] = struct{}{}
	}
	out := make([]string, 0, len(add))
	for _, id := range add {
		if _, ok := allowed[id]; ok {
			out = append(out, id)
		}
	}
	return out
}
