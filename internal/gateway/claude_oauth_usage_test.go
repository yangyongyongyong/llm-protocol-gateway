package gateway

import "testing"

func TestParseClaudeOAuthUsageBucket(t *testing.T) {
	bucket := parseClaudeOAuthUsageBucket(map[string]any{
		"utilization": 42.5,
		"resets_at":   "2026-07-07T12:00:00Z",
	})
	if bucket == nil || bucket.Utilization != 42.5 || bucket.ResetsAt != "2026-07-07T12:00:00Z" {
		t.Fatalf("unexpected bucket: %#v", bucket)
	}
	if parseClaudeOAuthUsageBucket(nil) != nil {
		t.Fatalf("expected nil for missing bucket")
	}
}
