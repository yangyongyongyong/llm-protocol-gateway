package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// summarizeUpstreamHTTPError builds a client/log-facing message from an upstream
// HTTP status and body. Prefers structured error.message when present.
func summarizeUpstreamHTTPError(status int, body []byte) string {
	if msg := extractResponseErrorMessage(body); msg != "" {
		if status > 0 {
			return fmt.Sprintf("HTTP %d: %s", status, msg)
		}
		return msg
	}
	snippet := strings.TrimSpace(string(body))
	if snippet == "" {
		if status > 0 {
			return fmt.Sprintf("HTTP %d", status)
		}
		return "upstream request failed"
	}
	if !utf8.ValidString(snippet) {
		snippet = string([]rune(snippet))
	}
	const maxLen = 500
	if len(snippet) > maxLen {
		snippet = snippet[:maxLen] + "…"
	}
	if status > 0 {
		return fmt.Sprintf("HTTP %d: %s", status, snippet)
	}
	return snippet
}

// errorMessageFromValue extracts a human-readable message from a protocol error
// object, falling back to a JSON/string dump instead of a generic placeholder.
func errorMessageFromValue(errorValue any, fallback string) (message string, errorType string) {
	errorType = "api_error"
	message = strings.TrimSpace(fallback)
	if message == "" {
		message = "upstream request failed"
	}
	if item, ok := errorValue.(map[string]any); ok {
		if value := stringValue(item["message"]); value != "" {
			message = value
		} else if value := stringValue(item["msg"]); value != "" {
			message = value
		} else if value := stringValue(item["detail"]); value != "" {
			message = value
		} else if value := stringValue(item["error"]); value != "" {
			message = value
		} else if raw, err := json.Marshal(item); err == nil && len(raw) > 0 && string(raw) != "{}" {
			message = string(raw)
		}
		if value := stringValue(item["type"]); value != "" {
			errorType = value
		} else if value := stringValue(item["code"]); value != "" {
			errorType = value
		}
		return message, errorType
	}
	if errorValue != nil {
		if text := strings.TrimSpace(stringValue(errorValue)); text != "" {
			message = text
		}
	}
	return message, errorType
}

func errorValueOrBody(payload map[string]any, status int, body []byte) any {
	if payload != nil {
		if errorValue, ok := payload["error"]; ok && errorValue != nil {
			return errorValue
		}
		if detail := strings.TrimSpace(stringValue(payload["detail"])); detail != "" {
			return map[string]any{
				"type":    "api_error",
				"message": detail,
			}
		}
	}
	return map[string]any{
		"type":    "api_error",
		"message": summarizeUpstreamHTTPError(status, body),
	}
}
