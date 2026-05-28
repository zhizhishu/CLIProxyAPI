package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestPatchClaudeBodyForcesOneMillion(t *testing.T) {
	cfg := defaultConfig()
	body := []byte(`{
		"model":"claude-opus-4-7[1m]",
		"betas":["custom-beta"],
		"messages":[{"role":"user","content":"hello"}]
	}`)

	out, betas, err := patchClaudeBody(body, cfg, "test-key")
	if err != nil {
		t.Fatalf("patchClaudeBody error: %v", err)
	}

	if got := gjson.GetBytes(out, "model").String(); got != "claude-opus-4-7" {
		t.Fatalf("model = %q, want claude-opus-4-7. body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "betas").Exists() {
		t.Fatalf("betas should be removed from body: %s", string(out))
	}
	if got := gjson.GetBytes(out, "metadata.user_id").String(); !gjson.Valid(got) {
		t.Fatalf("metadata.user_id should be JSON string, got %q", got)
	}
	if got := gjson.Get(gjson.GetBytes(out, "metadata.user_id").String(), "device_id").String(); got == "" {
		t.Fatalf("metadata.user_id should include device_id: %s", string(out))
	}
	if len(betas) != 2 || betas[0] != "custom-beta" || betas[1] != cfg.OneMillionBeta {
		t.Fatalf("betas = %#v, want custom beta and 1m beta", betas)
	}
}

func TestEnsureCSVDeduplicates(t *testing.T) {
	got := ensureCSV("custom,context-1m-2025-08-07", "context-1m-2025-08-07", "other")
	want := "custom,context-1m-2025-08-07,other"
	if got != want {
		t.Fatalf("ensureCSV = %q, want %q", got, want)
	}
}

func TestBuildTargetURLPreservesPathAndQuery(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://sidecar.local/v1/messages?beta=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := buildTargetURL("https://anyrouter.top/api", req.URL)
	if err != nil {
		t.Fatalf("buildTargetURL error: %v", err)
	}
	want := "https://anyrouter.top/api/v1/messages?beta=true"
	if got != want {
		t.Fatalf("target = %q, want %q", got, want)
	}
}

func TestMessagesEmpty(t *testing.T) {
	if !messagesEmpty([]byte(`{"messages":[]}`)) {
		t.Fatal("messagesEmpty should detect empty messages")
	}
	if messagesEmpty([]byte(`{"messages":[{"role":"user","content":"hello"}]}`)) {
		t.Fatal("messagesEmpty should not reject non-empty messages")
	}
}

func TestHandleProxyPatchesClaudeMessagesRequest(t *testing.T) {
	var upstreamBody []byte
	var upstreamBeta string
	var upstreamUserAgent string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("upstream path = %q, want /v1/messages", r.URL.Path)
		}
		upstreamBeta = r.Header.Get("Anthropic-Beta")
		upstreamUserAgent = r.Header.Get("User-Agent")
		var err error
		upstreamBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer upstream.Close()

	cfg := defaultConfig()
	cfg.UpstreamBaseURL = upstream.URL
	app := &App{cfg: cfg, client: upstream.Client()}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"claude-opus-4-7[1m]",
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	app.handleProxy(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := gjson.GetBytes(upstreamBody, "model").String(); got != "claude-opus-4-7" {
		t.Fatalf("upstream model = %q, want claude-opus-4-7. body=%s", got, string(upstreamBody))
	}
	if !strings.Contains(upstreamBeta, defaultBeta) {
		t.Fatalf("Anthropic-Beta = %q, want %q", upstreamBeta, defaultBeta)
	}
	if upstreamUserAgent != cfg.DefaultHeaders["User-Agent"] {
		t.Fatalf("User-Agent = %q, want configured Claude CLI UA", upstreamUserAgent)
	}
	userID := gjson.GetBytes(upstreamBody, "metadata.user_id").String()
	if !gjson.Valid(userID) || gjson.Get(userID, "device_id").String() == "" {
		t.Fatalf("metadata.user_id should be Claude Code JSON, got %q", userID)
	}
}
