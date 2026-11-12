package gateway

import "testing"

// OpenAI Responses 的成功响应带 "error": null；它不是错误，日志不能因此变红。
func TestExtractResponseErrorMessageIgnoresNilError(t *testing.T) {
	cases := []string{
		`{"id":"resp_1","object":"response","status":"completed","error":null}`,
		`{"id":"resp_1","status":"incomplete","error":null,"incomplete_details":{"reason":"max_output_tokens"}}`,
	}
	for _, body := range cases {
		if got := extractResponseErrorMessage([]byte(body)); got != "" {
			t.Fatalf("error = %q, want empty for %s", got, body)
		}
	}
	if got := extractResponseErrorMessage([]byte(`{"error":{"message":"boom","type":"api_error"}}`)); got != "boom" {
		t.Fatalf("real error = %q, want boom", got)
	}
}
