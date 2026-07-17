package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dumpNearEmptyConvertedResponse writes a full diagnostic bundle whenever a
// Claude->Responses conversion produces a suspiciously small output array
// (<=1 items). Gated by LPG_DEBUG_NEAR_EMPTY_DUMP (a directory path); a
// complete no-op with zero overhead when unset.
//
// Background: a 200 status with a short output array can still be a hard
// client-side failure. Codex's "remote compaction v2" requires exactly one
// usable ("compaction") output item and hard-fails otherwise — e.g. "expected
// exactly one compaction output item, got 0 from 1 output items" — even when
// the gateway itself sees nothing wrong (status 200, non-empty usage). The
// gateway's own retry heuristics (isThinkingOnlyEmptyOutput) only catch the
// "all-reasoning" shape; this dump exists to capture ground truth for
// whatever shape actually triggers the next such client-side failure, since
// request_logs only stores a truncated request body and no response body at
// all for successful (2xx) requests.
func dumpNearEmptyConvertedResponse(kind, model string, upstreamBody []byte, summary map[string]any) {
	dir := strings.TrimSpace(os.Getenv("LPG_DEBUG_NEAR_EMPTY_DUMP"))
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	bundle := map[string]any{
		"time":          time.Now().Format(time.RFC3339Nano),
		"kind":          kind,
		"model":         model,
		"summary":       summary,
		"upstream_body": json.RawMessage(nonEmptyOrNullJSON(upstreamBody)),
	}
	encoded, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return
	}
	name := filepath.Join(dir, fmt.Sprintf("near-empty-%s-%d.json", kind, time.Now().UnixNano()))
	_ = os.WriteFile(name, encoded, 0o644)
}

func nonEmptyOrNullJSON(b []byte) []byte {
	if len(b) == 0 {
		return []byte("null")
	}
	return b
}

func outputItemTypes(output []map[string]any) []string {
	types := make([]string, 0, len(output))
	for _, item := range output {
		types = append(types, stringValue(item["type"]))
	}
	return types
}
