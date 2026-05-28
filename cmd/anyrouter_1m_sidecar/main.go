package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	defaultConfigPath = "anyrouter-1m-sidecar.json"
	defaultListenAddr = ":8787"
	defaultUpstream   = "https://anyrouter.top"
	defaultBaseModel  = "claude-opus-4-7"
	defaultBeta       = "context-1m-2025-08-07"
)

var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

type Config struct {
	ListenAddr          string            `json:"listen_addr"`
	UpstreamBaseURL     string            `json:"upstream_base_url"`
	BaseModel           string            `json:"base_model"`
	OneMillionBeta      string            `json:"one_million_beta"`
	ForceOneMillion     bool              `json:"force_one_million"`
	RejectEmptyMessages bool              `json:"reject_empty_messages"`
	EnsureMetadata      bool              `json:"ensure_metadata"`
	DefaultHeaders      map[string]string `json:"default_headers"`
}

type App struct {
	mu             sync.RWMutex
	configPath     string
	adminToken     string
	upstreamAPIKey string
	client         *http.Client
	cfg            Config
}

func main() {
	configPath := getenv("ANYROUTER_1M_CONFIG", defaultConfigPath)
	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	applyEnv(&cfg)

	app := &App{
		configPath:     configPath,
		adminToken:     os.Getenv("ANYROUTER_1M_ADMIN_TOKEN"),
		upstreamAPIKey: os.Getenv("ANYROUTER_API_KEY"),
		client: &http.Client{
			Timeout: 0,
		},
		cfg: cfg,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleIndex)
	mux.HandleFunc("/healthz", app.handleHealthz)
	mux.HandleFunc("/api/config", app.handleConfig)
	mux.HandleFunc("/v1/", app.handleProxy)

	log.Printf("anyrouter 1m sidecar listening on %s, upstream=%s", cfg.ListenAddr, cfg.UpstreamBaseURL)
	if err := http.ListenAndServe(cfg.ListenAddr, mux); err != nil {
		log.Fatal(err)
	}
}

func defaultConfig() Config {
	return Config{
		ListenAddr:          defaultListenAddr,
		UpstreamBaseURL:     defaultUpstream,
		BaseModel:           defaultBaseModel,
		OneMillionBeta:      defaultBeta,
		ForceOneMillion:     true,
		RejectEmptyMessages: true,
		EnsureMetadata:      true,
		DefaultHeaders: map[string]string{
			"Anthropic-Dangerous-Direct-Browser-Access": "true",
			"Anthropic-Version":                         "2023-06-01",
			"User-Agent":                                "claude-cli/2.1.126 (external, claude-vscode, agent-sdk/0.2.126)",
			"X-App":                                     "cli",
			"X-Stainless-Arch":                          "x64",
			"X-Stainless-Lang":                          "js",
			"X-Stainless-Os":                            "Windows",
			"X-Stainless-Package-Version":               "0.81.0",
			"X-Stainless-Retry-Count":                   "0",
			"X-Stainless-Runtime":                       "node",
			"X-Stainless-Runtime-Version":               "v24.3.0",
			"X-Stainless-Timeout":                       "600",
		},
	}
}

func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	normalizeConfig(&cfg)
	return cfg, nil
}

func saveConfig(path string, cfg Config) error {
	normalizeConfig(&cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func normalizeConfig(cfg *Config) {
	def := defaultConfig()
	if strings.TrimSpace(cfg.ListenAddr) == "" {
		cfg.ListenAddr = def.ListenAddr
	}
	if strings.TrimSpace(cfg.UpstreamBaseURL) == "" {
		cfg.UpstreamBaseURL = def.UpstreamBaseURL
	}
	cfg.UpstreamBaseURL = strings.TrimRight(strings.TrimSpace(cfg.UpstreamBaseURL), "/")
	if strings.TrimSpace(cfg.BaseModel) == "" {
		cfg.BaseModel = def.BaseModel
	}
	if strings.TrimSpace(cfg.OneMillionBeta) == "" {
		cfg.OneMillionBeta = def.OneMillionBeta
	}
	if cfg.DefaultHeaders == nil {
		cfg.DefaultHeaders = def.DefaultHeaders
	}
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("ANYROUTER_1M_LISTEN"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("ANYROUTER_UPSTREAM"); v != "" {
		cfg.UpstreamBaseURL = v
	}
	if v := os.Getenv("ANYROUTER_1M_MODEL"); v != "" {
		cfg.BaseModel = v
	}
	normalizeConfig(cfg)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (a *App) getConfig() Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

func (a *App) setConfig(cfg Config) error {
	normalizeConfig(&cfg)
	if err := saveConfig(a.configPath, cfg); err != nil {
		return err
	}
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
	return nil
}

func (a *App) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		a.handleProxy(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTemplate.Execute(w, map[string]any{
		"AdminRequired": a.adminToken != "",
	})
}

func (a *App) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.getConfig())
	case http.MethodPost:
		if !a.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin token required"})
			return
		}
		var cfg Config
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := a.setConfig(cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, cfg)
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) authorized(r *http.Request) bool {
	if a.adminToken == "" {
		return true
	}
	token := strings.TrimSpace(r.Header.Get("X-Admin-Token"))
	if token == "" {
		token = strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	}
	return token == a.adminToken
}

func (a *App) handleProxy(w http.ResponseWriter, r *http.Request) {
	cfg := a.getConfig()
	target, err := buildTargetURL(cfg.UpstreamBaseURL, r.URL)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if shouldPatchClaudeBody(r.URL.Path, r.Method, body) {
		var extraBetas []string
		body, extraBetas, err = patchClaudeBody(body, cfg, authFingerprint(r))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if cfg.RejectEmptyMessages && messagesEmpty(body) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "messages must not be empty for AnyRouter 1M"})
			return
		}
		r.Header.Set("Anthropic-Beta", ensureCSV(r.Header.Get("Anthropic-Beta"), append(extraBetas, cfg.OneMillionBeta)...))
	}

	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	copyHeaders(upReq.Header, r.Header)
	applyConfiguredHeaders(upReq.Header, cfg.DefaultHeaders)
	upReq.Header.Set("Anthropic-Beta", ensureCSV(upReq.Header.Get("Anthropic-Beta"), cfg.OneMillionBeta))
	upReq.Header.Set("Content-Type", firstNonEmpty(upReq.Header.Get("Content-Type"), "application/json"))
	upReq.Host = ""
	if a.upstreamAPIKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+a.upstreamAPIKey)
		upReq.Header.Del("x-api-key")
	}

	resp, err := a.client.Do(upReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func shouldPatchClaudeBody(path, method string, body []byte) bool {
	if method != http.MethodPost || len(bytes.TrimSpace(body)) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	return path == "/v1/messages" || path == "/v1/messages/count_tokens"
}

func patchClaudeBody(body []byte, cfg Config, authSeed string) ([]byte, []string, error) {
	if !gjson.ValidBytes(body) {
		return body, nil, nil
	}
	var betas []string
	betas, body = extractAndRemoveBetas(body)
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	baseModel := strings.TrimSpace(cfg.BaseModel)
	if baseModel == "" {
		baseModel, _ = stripOneMillionSuffix(model)
	}
	if cfg.ForceOneMillion || strings.Contains(strings.ToLower(model), "[1m]") {
		if baseModel == "" {
			baseModel = defaultBaseModel
		}
		var err error
		body, err = sjson.SetBytes(body, "model", baseModel)
		if err != nil {
			return body, betas, err
		}
		betas = append(betas, cfg.OneMillionBeta)
		if cfg.EnsureMetadata {
			body = ensureClaudeCodeMetadata(body, authSeed)
		}
	}
	return body, betas, nil
}

func stripOneMillionSuffix(model string) (string, bool) {
	trimmed := strings.TrimSpace(model)
	lower := strings.ToLower(trimmed)
	const suffix = "[1m]"
	if !strings.Contains(lower, suffix) {
		return trimmed, false
	}
	idx := strings.Index(lower, suffix)
	return strings.TrimSpace(trimmed[:idx] + trimmed[idx+len(suffix):]), true
}

func extractAndRemoveBetas(body []byte) ([]string, []byte) {
	var betas []string
	for _, field := range []string{"betas", "anthropic_beta"} {
		result := gjson.GetBytes(body, field)
		if !result.Exists() {
			continue
		}
		betas = appendBetaResult(betas, result)
		body, _ = sjson.DeleteBytes(body, field)
	}
	return betas, body
}

func appendBetaResult(betas []string, result gjson.Result) []string {
	if result.IsArray() {
		result.ForEach(func(_, item gjson.Result) bool {
			return appendBetaString(&betas, item.String())
		})
		return betas
	}
	_ = appendBetaString(&betas, result.String())
	return betas
}

func appendBetaString(betas *[]string, raw string) bool {
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*betas = append(*betas, part)
		}
	}
	return true
}

func ensureClaudeCodeMetadata(body []byte, seed string) []byte {
	userID := gjson.GetBytes(body, "metadata.user_id").String()
	if looksLikeClaudeCodeMetadata(userID) {
		return body
	}
	body, _ = sjson.SetBytes(body, "metadata.user_id", generateMetadataUserID(seed))
	return body
}

func looksLikeClaudeCodeMetadata(userID string) bool {
	if !gjson.Valid(userID) {
		return false
	}
	return gjson.Get(userID, "device_id").String() != ""
}

func generateMetadataUserID(seed string) string {
	sum := sha256.Sum256([]byte(firstNonEmpty(seed, "anyrouter-1m-sidecar")))
	device := hex.EncodeToString(sum[:])[:64]
	session := deterministicUUID(sum[16:])
	raw, _ := json.Marshal(map[string]string{
		"device_id":    device,
		"account_uuid": "",
		"session_id":   session,
	})
	return string(raw)
}

func deterministicUUID(seed []byte) string {
	if len(seed) < 16 {
		sum := sha256.Sum256(seed)
		seed = sum[:]
	}
	b := append([]byte(nil), seed[:16]...)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

func messagesEmpty(body []byte) bool {
	msgs := gjson.GetBytes(body, "messages")
	return msgs.Exists() && msgs.IsArray() && len(msgs.Array()) == 0
}

func authFingerprint(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		auth = r.Header.Get("x-api-key")
	}
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	return auth + "|" + remoteIP
}

func buildTargetURL(base string, in *url.URL) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid upstream base url %q", base)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + in.EscapedPath()
	parsed.RawQuery = in.RawQuery
	return parsed.String(), nil
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		if _, skip := hopByHopHeaders[http.CanonicalHeaderKey(key)]; skip {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func applyConfiguredHeaders(headers http.Header, defaults map[string]string) {
	for key, value := range defaults {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		headers.Set(key, value)
	}
}

func ensureCSV(existing string, required ...string) string {
	seen := map[string]bool{}
	var parts []string
	for _, raw := range append([]string{existing}, required...) {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			key := strings.ToLower(part)
			if seen[key] {
				continue
			}
			seen[key] = true
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, ",")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var indexTemplate = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>AnyRouter 1M Sidecar</title>
  <style>
    :root {
      color-scheme: light;
      --ink: #171717;
      --muted: #626262;
      --line: #d7d0c5;
      --paper: #f7f2e8;
      --panel: #fffdf7;
      --accent: #0d766f;
      --accent-2: #b42318;
      --field: #ffffff;
      --shadow: 0 18px 50px rgba(41, 31, 18, .10);
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      font: 14px/1.45 ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
      color: var(--ink);
      background:
        linear-gradient(90deg, rgba(23,23,23,.035) 1px, transparent 1px),
        linear-gradient(rgba(23,23,23,.035) 1px, transparent 1px),
        var(--paper);
      background-size: 22px 22px;
    }
    main { max-width: 1180px; margin: 0 auto; padding: 28px; }
    header { display: flex; justify-content: space-between; gap: 16px; align-items: end; border-bottom: 2px solid var(--ink); padding-bottom: 18px; }
    h1 { margin: 0; font-size: clamp(28px, 4vw, 54px); line-height: .95; letter-spacing: 0; }
    .status { display: inline-flex; align-items: center; min-height: 34px; padding: 0 12px; border: 1px solid var(--ink); background: var(--panel); box-shadow: 4px 4px 0 var(--ink); white-space: nowrap; }
    form { margin-top: 24px; display: grid; grid-template-columns: 1.05fr .95fr; gap: 18px; }
    section { background: var(--panel); border: 1px solid var(--line); box-shadow: var(--shadow); padding: 18px; }
    h2 { margin: 0 0 14px; font-size: 15px; text-transform: uppercase; letter-spacing: .08em; }
    label { display: block; margin: 12px 0; color: var(--muted); }
    label span { display: block; margin-bottom: 6px; color: var(--ink); }
    input, textarea {
      width: 100%;
      border: 1px solid var(--line);
      background: var(--field);
      color: var(--ink);
      padding: 11px 12px;
      min-height: 40px;
      font: inherit;
      outline: none;
    }
    textarea { min-height: 318px; resize: vertical; }
    input:focus, textarea:focus { border-color: var(--accent); box-shadow: 0 0 0 3px rgba(13,118,111,.14); }
    .checks { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; margin-top: 10px; }
    .check { display: flex; gap: 8px; align-items: center; border: 1px solid var(--line); padding: 10px; background: #faf7ef; color: var(--ink); }
    .check input { width: 18px; min-height: 18px; }
    .actions { grid-column: 1 / -1; display: flex; gap: 10px; justify-content: flex-end; align-items: center; }
    button {
      border: 1px solid var(--ink);
      background: var(--ink);
      color: white;
      padding: 11px 16px;
      font: inherit;
      cursor: pointer;
      min-height: 42px;
    }
    button.secondary { background: var(--panel); color: var(--ink); }
    .token { max-width: 320px; }
    .danger { color: var(--accent-2); }
    @media (max-width: 820px) {
      main { padding: 18px; }
      header, form { display: block; }
      .status { margin-top: 14px; }
      section { margin-top: 14px; }
      .checks { grid-template-columns: 1fr; }
      .actions { margin-top: 14px; justify-content: stretch; flex-direction: column; }
      button, .token { width: 100%; max-width: none; }
    }
  </style>
</head>
<body>
<main>
  <header>
    <div>
      <h1>AnyRouter<br>1M Sidecar</h1>
    </div>
    <div class="status" id="status">loading</div>
  </header>
  <form id="form">
    <section>
      <h2>Route</h2>
      <label><span>Listen</span><input id="listen_addr" autocomplete="off"></label>
      <label><span>Upstream</span><input id="upstream_base_url" autocomplete="off"></label>
      <label><span>Base model</span><input id="base_model" autocomplete="off"></label>
      <label><span>1M beta</span><input id="one_million_beta" autocomplete="off"></label>
      <div class="checks">
        <label class="check"><input id="force_one_million" type="checkbox">force 1M</label>
        <label class="check"><input id="reject_empty_messages" type="checkbox">block empty</label>
        <label class="check"><input id="ensure_metadata" type="checkbox">metadata</label>
      </div>
    </section>
    <section>
      <h2>Headers</h2>
      <textarea id="headers" spellcheck="false"></textarea>
    </section>
    <div class="actions">
      <input class="token" id="admin_token" type="password" placeholder="admin token">
      <button type="button" class="secondary" id="reload">Reload</button>
      <button type="submit">Save</button>
    </div>
  </form>
</main>
<script>
const adminRequired = {{.AdminRequired}};
const $ = id => document.getElementById(id);
function setStatus(text, danger=false) {
  const el = $("status");
  el.textContent = text;
  el.className = danger ? "status danger" : "status";
}
function toHeaderText(headers) {
  return Object.entries(headers || {}).map(([k,v]) => k + ": " + v).join("\n");
}
function fromHeaderText(text) {
  const out = {};
  for (const line of text.split(/\r?\n/)) {
    const idx = line.indexOf(":");
    if (idx < 1) continue;
    const key = line.slice(0, idx).trim();
    const val = line.slice(idx + 1).trim();
    if (key && val) out[key] = val;
  }
  return out;
}
function fill(cfg) {
  $("listen_addr").value = cfg.listen_addr || "";
  $("upstream_base_url").value = cfg.upstream_base_url || "";
  $("base_model").value = cfg.base_model || "";
  $("one_million_beta").value = cfg.one_million_beta || "";
  $("force_one_million").checked = !!cfg.force_one_million;
  $("reject_empty_messages").checked = !!cfg.reject_empty_messages;
  $("ensure_metadata").checked = !!cfg.ensure_metadata;
  $("headers").value = toHeaderText(cfg.default_headers);
}
function collect() {
  return {
    listen_addr: $("listen_addr").value.trim(),
    upstream_base_url: $("upstream_base_url").value.trim(),
    base_model: $("base_model").value.trim(),
    one_million_beta: $("one_million_beta").value.trim(),
    force_one_million: $("force_one_million").checked,
    reject_empty_messages: $("reject_empty_messages").checked,
    ensure_metadata: $("ensure_metadata").checked,
    default_headers: fromHeaderText($("headers").value)
  };
}
async function loadConfig() {
  setStatus("loading");
  const res = await fetch("/api/config");
  if (!res.ok) throw new Error(await res.text());
  fill(await res.json());
  setStatus(adminRequired ? "admin token required" : "ready");
}
async function saveConfig(e) {
  e.preventDefault();
  const headers = {"Content-Type": "application/json"};
  if ($("admin_token").value) headers["X-Admin-Token"] = $("admin_token").value;
  setStatus("saving");
  const res = await fetch("/api/config", {method:"POST", headers, body: JSON.stringify(collect())});
  if (!res.ok) {
    setStatus(await res.text(), true);
    return;
  }
  fill(await res.json());
  setStatus("saved");
}
$("form").addEventListener("submit", saveConfig);
$("reload").addEventListener("click", () => loadConfig().catch(err => setStatus(err.message, true)));
loadConfig().catch(err => setStatus(err.message, true));
</script>
</body>
</html>`))
