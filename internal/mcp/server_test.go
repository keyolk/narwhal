package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runRPC feeds newline-delimited requests through a server and returns the
// decoded responses.
func runRPC(t *testing.T, daemonURL func() (string, error), lines ...string) []map[string]any {
	t.Helper()
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out bytes.Buffer
	s := New(in, &out, daemonURL)
	if err := s.Serve(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		responses = append(responses, m)
	}
	return responses
}

func noDaemon() (string, error) { return "", fmt.Errorf("daemon not running") }

func TestInitializeReportsProtocolAndTools(t *testing.T) {
	resps := runRPC(t, noDaemon,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)

	if len(resps) != 1 {
		t.Fatalf("responses = %d, want 1", len(resps))
	}
	result, ok := resps[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", resps[0])
	}
	if result["protocolVersion"] != protocolVersion {
		t.Fatalf("protocolVersion = %v, want %s", result["protocolVersion"], protocolVersion)
	}
	caps, _ := result["capabilities"].(map[string]any)
	if _, hasTools := caps["tools"]; !hasTools {
		t.Fatalf("capabilities missing tools: %v", caps)
	}
}

func TestToolsListExposesEveryTool(t *testing.T) {
	resps := runRPC(t, noDaemon,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	result := resps[0]["result"].(map[string]any)
	tools := result["tools"].([]any)

	got := map[string]bool{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		got[tool["name"].(string)] = true
		// Every tool must carry a schema, or Claude Code cannot call it.
		if _, ok := tool["inputSchema"].(map[string]any); !ok {
			t.Fatalf("tool %v has no inputSchema", tool["name"])
		}
		if desc, _ := tool["description"].(string); desc == "" {
			t.Fatalf("tool %v has no description", tool["name"])
		}
	}
	for _, want := range []string{
		"narwhal_spawn", "narwhal_drain", "narwhal_status",
		"narwhal_send", "narwhal_cancel",
	} {
		if !got[want] {
			t.Fatalf("tools/list missing %s (got %v)", want, got)
		}
	}
}

func TestNotificationsGetNoResponse(t *testing.T) {
	// A notification has no id and must not be answered, or the client
	// sees a response to a request it never made.
	resps := runRPC(t, noDaemon,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if len(resps) != 0 {
		t.Fatalf("notification produced %d responses: %v", len(resps), resps)
	}
}

func TestUnknownMethodReturnsError(t *testing.T) {
	resps := runRPC(t, noDaemon,
		`{"jsonrpc":"2.0","id":7,"method":"does/not/exist"}`)
	errObj, ok := resps[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error, got %v", resps[0])
	}
	if code := errObj["code"].(float64); code != -32601 {
		t.Fatalf("code = %v, want -32601", code)
	}
}

func TestToolCallWithoutDaemonIsInBandError(t *testing.T) {
	// A missing daemon must surface as an isError tool result, not a
	// protocol error: the model needs to see it and can act on it.
	resps := runRPC(t, noDaemon,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"narwhal_status","arguments":{}}}`)

	result, ok := resps[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result, got %v", resps[0])
	}
	if result["isError"] != true {
		t.Fatalf("expected isError=true, got %v", result)
	}
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "narwhal daemon start") {
		t.Fatalf("error text should tell the user how to recover, got %q", text)
	}
}

func TestToolCallForwardsToDaemon(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.String()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"run_id":"s1-1","workers":[{"task_id":"auth","state":"dispatched"}]}`)
	}))
	defer srv.Close()

	daemonURL := func() (string, error) { return srv.URL, nil }
	resps := runRPC(t, daemonURL,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"narwhal_spawn","arguments":{"cwd":"/tmp/x","workers":[{"name":"auth","assignment":"audit auth"}]}}}`)

	if gotPath != "/api/v1/control/spawn" {
		t.Fatalf("forwarded to %q, want /api/v1/control/spawn", gotPath)
	}
	if !strings.Contains(gotBody, `"cwd":"/tmp/x"`) {
		t.Fatalf("body lost cwd: %s", gotBody)
	}

	result := resps[0]["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("unexpected error result: %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "s1-1") {
		t.Fatalf("response text lost the run id: %q", text)
	}
	// Output should be indented so the model reads structure, not one line.
	if !strings.Contains(text, "\n") {
		t.Fatalf("expected indented JSON, got %q", text)
	}
}

func TestStatusPassesRunIDAsQuery(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		fmt.Fprint(w, `{"runs":[]}`)
	}))
	defer srv.Close()

	runRPC(t, func() (string, error) { return srv.URL, nil },
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"narwhal_status","arguments":{"run_id":"abc"}}}`)

	if !strings.Contains(gotURL, "run_id=abc") {
		t.Fatalf("status URL = %q, want run_id=abc", gotURL)
	}
}

func TestDaemonErrorStatusSurfacesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"run not found"}`)
	}))
	defer srv.Close()

	resps := runRPC(t, func() (string, error) { return srv.URL, nil },
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"narwhal_drain","arguments":{"run_id":"nope"}}}`)

	result := resps[0]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("expected isError, got %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "run not found") {
		t.Fatalf("error text should carry the daemon's message, got %q", text)
	}
}
