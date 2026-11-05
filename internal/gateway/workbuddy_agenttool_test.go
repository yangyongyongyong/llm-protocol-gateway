package gateway

import (
	"encoding/json"
	"testing"
)

// The request body is rebuilt, never forwarded: `provider` is a response field and
// sending it is refused outright (code 15003). A client that echoes a previous
// response back must not be able to poison its next request.
func TestWorkBuddySearchRequestDropsProvider(t *testing.T) {
	body := []byte(`{"query":"golang sse","provider":"tavily","type":"TEXT2TEXT","max_results":3,"extra":true}`)
	request, ok := workBuddySearchRequest(body)
	if !ok {
		t.Fatal("a valid query was refused")
	}
	if _, has := request["provider"]; has {
		t.Fatalf("provider survived: %v", request)
	}
	if _, has := request["extra"]; has {
		t.Fatalf("an unknown field survived: %v", request)
	}
	// `type` is only legal as lower-case text2text, and the client's casing is ignored.
	if request["type"] != "text2text" {
		t.Fatalf("type = %v", request["type"])
	}
	if request["query"] != "golang sse" {
		t.Fatalf("query = %v", request["query"])
	}
	if request["max_results"] != int64(3) {
		t.Fatalf("max_results = %v", request["max_results"])
	}
}

// The official client asks for 5, and the server caps around 10: absent means 5, and a
// larger value is clamped rather than sent.
func TestWorkBuddySearchRequestBounds(t *testing.T) {
	request, ok := workBuddySearchRequest([]byte(`{"query":"x"}`))
	if !ok {
		t.Fatal("a bare query was refused")
	}
	if request["max_results"] != int64(workBuddySearchOfficialResults) {
		t.Fatalf("default = %v, want %d", request["max_results"], workBuddySearchOfficialResults)
	}

	request, _ = workBuddySearchRequest([]byte(`{"query":"x","max_results":500}`))
	if request["max_results"] != int64(workBuddySearchMaxResultsCap) {
		t.Fatalf("clamped = %v, want %d", request["max_results"], workBuddySearchMaxResultsCap)
	}

	// A string number is read the same way, because clients differ.
	request, _ = workBuddySearchRequest([]byte(`{"query":"x","max_results":"7"}`))
	if request["max_results"] != int64(7) {
		t.Fatalf("string max_results = %v", request["max_results"])
	}
}

// A search without a query is the client's mistake, and it is answered here rather than
// spending an upstream call to be told so.
func TestWorkBuddySearchRequestRequiresQuery(t *testing.T) {
	for _, body := range []string{``, `{}`, `{"query":"   "}`, `not json`} {
		if _, ok := workBuddySearchRequest([]byte(body)); ok {
			t.Fatalf("%q was accepted", body)
		}
	}
}

// A fetch carries one url and nothing else.
func TestWorkBuddyWebFetchRequest(t *testing.T) {
	request, ok := workBuddyWebFetchRequest([]byte(`{"url":" https://example.com/a ","other":1}`))
	if !ok {
		t.Fatal("a valid url was refused")
	}
	if request["url"] != "https://example.com/a" {
		t.Fatalf("url = %v", request["url"])
	}
	if len(request) != 1 {
		t.Fatalf("extra fields survived: %v", request)
	}
	for _, body := range []string{``, `{}`, `{"url":"  "}`, `not json`} {
		if _, ok := workBuddyWebFetchRequest([]byte(body)); ok {
			t.Fatalf("%q was accepted", body)
		}
	}
}

// The built body is exactly the three legal fields.
func TestWorkBuddySearchRequestEncodesCleanly(t *testing.T) {
	request, ok := workBuddySearchRequest([]byte(`{"query":"q","provider":"x"}`))
	if !ok {
		t.Fatal("refused")
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("fields = %v, want exactly query/type/max_results", decoded)
	}
}
