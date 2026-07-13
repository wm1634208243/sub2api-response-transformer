package main

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

type adminHandler struct {
	store      *configStore
	configPath string
	logger     *slog.Logger
}

func newAdminHandler(store *configStore, configPath string, logger *slog.Logger) http.Handler {
	return &adminHandler{store: store, configPath: configPath, logger: logger}
}

func (h *adminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := h.store.Load()
	suffix := strings.TrimPrefix(r.URL.Path, cfg.AdminPath)
	if suffix == "" || suffix == "/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, adminHTML)
		return
	}
	if suffix != "/api/config" {
		http.NotFound(w, r)
		return
	}
	if !h.authorized(r, cfg.AdminToken) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="sub2api-response-transformer"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cfg.Config)
	case http.MethodPost:
		h.saveConfig(w, r, cfg.Listen)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *adminHandler) authorized(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	got := r.Header.Get("X-Admin-Token")
	if got == "" {
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			got = strings.TrimSpace(auth[7:])
		}
	}
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func (h *adminHandler) saveConfig(w http.ResponseWriter, r *http.Request, currentListen string) {
	var cfg Config
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	compiled, err := compileConfig(cfg)
	if err != nil {
		http.Error(w, "invalid config: "+err.Error(), http.StatusBadRequest)
		return
	}
	toStore := compiled
	if compiled.Listen != currentListen {
		copyCfg := compiled.Config
		copyCfg.Listen = currentListen
		toStore, err = compileConfig(copyCfg)
		if err != nil {
			http.Error(w, "invalid runtime config: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	raw, err := json.MarshalIndent(compiled.Config, "", "  ")
	if err != nil {
		http.Error(w, "marshal config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(h.configPath, raw, 0o600); err != nil {
		http.Error(w, "write config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.store.Store(toStore)
	h.logger.Info("config_saved_from_admin", "rules", len(compiled.Rules), "listen_changed", compiled.Listen != currentListen)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":                  "ok",
		"rules":                   len(compiled.Rules),
		"listen_change_restart":   compiled.Listen != currentListen,
		"effective_listen":        toStore.Listen,
		"configured_listen":       compiled.Listen,
		"configured_admin_path":   compiled.AdminPath,
		"configured_health_path":  compiled.HealthPath,
		"configured_stats_path":   compiled.StatsPath,
		"configured_upstream_url": compiled.Upstream,
	})
}

const adminHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Sub2API Response Transformer</title>
  <style>
    :root { color-scheme: light dark; font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    body { margin: 0; background: #f6f7f9; color: #171a1f; }
    header { position: sticky; top: 0; z-index: 3; background: #111827; color: #fff; padding: 14px 22px; display: flex; align-items: center; gap: 14px; box-shadow: 0 2px 10px rgba(0,0,0,.14); }
    header h1 { margin: 0; font-size: 17px; font-weight: 700; }
    header input { width: 220px; max-width: 32vw; padding: 8px 10px; border: 1px solid #4b5563; border-radius: 6px; background: #1f2937; color: #fff; }
    header button { padding: 8px 12px; border: 0; border-radius: 6px; background: #e5e7eb; color: #111827; cursor: pointer; }
    header button.primary { background: #38bdf8; color: #082f49; font-weight: 700; }
    main { max-width: 1180px; margin: 20px auto 48px; padding: 0 18px; }
    .panel { background: #fff; border: 1px solid #e5e7eb; border-radius: 8px; padding: 16px; margin-bottom: 14px; box-shadow: 0 1px 2px rgba(0,0,0,.03); }
    .grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; }
    label { display: grid; gap: 6px; font-size: 12px; color: #4b5563; }
    input, textarea, select { box-sizing: border-box; width: 100%; padding: 9px 10px; border: 1px solid #d1d5db; border-radius: 6px; background: #fff; color: #111827; font: inherit; font-size: 13px; }
    textarea { min-height: 72px; resize: vertical; font-family: ui-monospace, SFMono-Regular, Consolas, monospace; }
    .rule { border-left: 4px solid #38bdf8; }
    .rule-title { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 12px; }
    .rule-title strong { font-size: 15px; }
    .actions { display: flex; gap: 8px; flex-wrap: wrap; }
    .danger { background: #fee2e2; color: #991b1b; border: 1px solid #fecaca; }
    .muted { color: #6b7280; font-size: 12px; }
    .status { min-height: 20px; color: #0369a1; font-size: 13px; }
    .wide { grid-column: 1 / -1; }
    .two { grid-column: span 2; }
    @media (max-width: 820px) { .grid { grid-template-columns: 1fr; } .two { grid-column: auto; } header { flex-wrap: wrap; } }
    @media (prefers-color-scheme: dark) {
      body { background: #0f172a; color: #e5e7eb; }
      .panel { background: #111827; border-color: #263244; }
      input, textarea, select { background: #0b1220; color: #e5e7eb; border-color: #374151; }
      label { color: #cbd5e1; }
      .muted { color: #94a3b8; }
    }
  </style>
</head>
<body>
  <header>
    <h1>Response Transformer</h1>
    <input id="token" type="password" placeholder="Admin token">
    <button onclick="loadConfig()">加载</button>
    <button class="primary" onclick="saveConfig()">保存并热加载</button>
    <span id="status" class="status"></span>
  </header>
  <main>
    <section class="panel">
      <div class="grid">
        <label>监听地址 <input id="listen"></label>
        <label>上游 Sub2API <input id="upstream"></label>
        <label>最大检�?Body 字节 <input id="max_inspect_body_bytes" type="number"></label>
        <label>健康检查路�?<input id="health_path"></label>
        <label>统计路径 <input id="stats_path"></label>
        <label>管理页路�?<input id="admin_path"></label>
        <label>Admin Token <input id="admin_token" type="password" placeholder="留空则不鉴权"></label>
        <label>透传 Host
          <select id="preserve_host"><option value="false">false</option><option value="true">true</option></select>
        </label>
        <label>返回调试响应�?          <select id="emit_debug_header"><option value="false">false</option><option value="true">true</option></select>
        </label>
      </div>
    </section>
    <div class="actions panel">
      <button onclick="addRule()">新增规则</button>
      <button onclick="showJSON()">查看 JSON</button>
      <span class="muted">规则从上到下匹配，第一条命中就停止�?/span>
    </div>
    <section id="rules"></section>
    <section class="panel">
      <label>当前 JSON
        <textarea id="jsonView" class="wide" style="min-height:180px"></textarea>
      </label>
    </section>
  </main>
<script>
let config = null;
const $ = (id) => document.getElementById(id);
const lines = (value) => String(value || '').split(/\r?\n/).map(s => s.trim()).filter(Boolean);
const csvNums = (value) => String(value || '').split(/[,\s]+/).map(s => s.trim()).filter(Boolean).map(Number).filter(n => Number.isFinite(n));
const bool = (id) => $(id).value === 'true';
const token = () => $('token').value || localStorage.getItem('transformer_admin_token') || '';
function headers() {
  const h = {'Content-Type': 'application/json'};
  if (token()) h['X-Admin-Token'] = token();
  return h;
}
function setStatus(text, bad) {
  $('status').textContent = text;
  $('status').style.color = bad ? '#dc2626' : '#0369a1';
}
async function loadConfig() {
  if ($('token').value) localStorage.setItem('transformer_admin_token', $('token').value);
  setStatus('加载�?..');
  const res = await fetch(location.pathname.replace(/\/$/, '') + '/api/config', {headers: headers()});
  if (!res.ok) { setStatus(await res.text(), true); return; }
  config = await res.json();
  fillForm();
  setStatus('已加�?);
}
function fillForm() {
  for (const key of ['listen','upstream','health_path','stats_path','admin_path','admin_token','max_inspect_body_bytes']) $(key).value = config[key] ?? '';
  $('preserve_host').value = String(!!config.preserve_host);
  $('emit_debug_header').value = String(!!config.emit_debug_header);
  renderRules();
  showJSON();
}
function readForm() {
  config.listen = $('listen').value;
  config.upstream = $('upstream').value;
  config.health_path = $('health_path').value;
  config.stats_path = $('stats_path').value;
  config.admin_path = $('admin_path').value;
  config.admin_token = $('admin_token').value;
  config.max_inspect_body_bytes = Number($('max_inspect_body_bytes').value || 1048576);
  config.preserve_host = bool('preserve_host');
  config.emit_debug_header = bool('emit_debug_header');
  config.rules = [...document.querySelectorAll('.rule')].map(readRule);
  return config;
}
function renderRules() {
  $('rules').innerHTML = '';
  (config.rules || []).forEach((rule, index) => $('rules').appendChild(ruleNode(rule, index)));
}
function ruleNode(rule, index) {
  const div = document.createElement('section');
  div.className = 'panel rule';
  div.innerHTML =
    '<div class="rule-title">' +
    '<strong>规则 ' + (index + 1) + '</strong>' +
		'<button class="danger" type="button" data-remove-rule="1">删除</button>' +
    '</div>' +
    '<div class="grid">' +
    '<label>名称 <input data-k="name" value="' + esc(rule.name || '') + '"></label>' +
    '<label>上游状态码 <input data-k="upstream_statuses" value="' + esc((rule.upstream_statuses || []).join(',')) + '" placeholder="500"></label>' +
    '<label>下游状态码 <input data-k="downstream_status" type="number" value="' + esc(rule.downstream_status || 400) + '"></label>' +
    '<label>URL 前缀，一行一�?<textarea data-k="url_path_prefixes">' + esc((rule.url_path_prefixes || []).join('\n')) + '</textarea></label>' +
    '<label>URL 包含，一行一�?<textarea data-k="url_path_contains">' + esc((rule.url_path_contains || []).join('\n')) + '</textarea></label>' +
    '<label>忽略大小�?<select data-k="case_insensitive"><option value="false">false</option><option value="true">true</option></select></label>' +
    '<label>Code JSON 路径 <textarea data-k="json_paths">' + esc((rule.json_paths || []).join('\n')) + '</textarea></label>' +
    '<label>Code �?<textarea data-k="values">' + esc((rule.values || []).join('\n')) + '</textarea></label>' +
    '<label>Message JSON 路径 <textarea data-k="message_paths">' + esc((rule.message_paths || []).join('\n')) + '</textarea></label>' +
    '<label>Message 包含 <textarea data-k="message_contains">' + esc((rule.message_contains || []).join('\n')) + '</textarea></label>' +
    '<label class="two">Body 包含 <textarea data-k="body_contains">' + esc((rule.body_contains || []).join('\n')) + '</textarea></label>' +
    '<label class="wide">响应模板，可留空表示原样返回 Body <textarea data-k="response_template" style="min-height:110px">' + esc(rule.response_template || '') + '</textarea></label>' +
    '</div>';
  div.querySelector('[data-k="case_insensitive"]').value = String(!!rule.case_insensitive);
  div.addEventListener('input', showJSON);
  div.addEventListener('change', showJSON);
  return div;
}
function readRule(el) {
  const v = (k) => el.querySelector('[data-k="' + k + '"]').value;
  return {
    name: v('name'),
    url_path_prefixes: lines(v('url_path_prefixes')),
    url_path_contains: lines(v('url_path_contains')),
    upstream_statuses: csvNums(v('upstream_statuses')),
    json_paths: lines(v('json_paths')),
    values: lines(v('values')),
    message_paths: lines(v('message_paths')),
    message_contains: lines(v('message_contains')),
    body_contains: lines(v('body_contains')),
    case_insensitive: v('case_insensitive') === 'true',
    downstream_status: Number(v('downstream_status') || 500),
    response_template: v('response_template')
  };
}
function addRule() {
  if (!config) config = defaultConfig();
  const rule = {name:'restore-code', upstream_statuses:[500], json_paths:['code','error.code','data.code'], values:['400'], downstream_status:400, case_insensitive:false};
  $('rules').appendChild(ruleNode(rule, document.querySelectorAll('.rule').length));
  showJSON();
}
function showJSON() {
  if (!config) return;
  $('jsonView').value = JSON.stringify(readForm(), null, 2);
}
async function saveConfig() {
  if (!config) return loadConfig();
  if ($('token').value) localStorage.setItem('transformer_admin_token', $('token').value);
  const body = JSON.stringify(readForm());
  setStatus('保存�?..');
  const res = await fetch(location.pathname.replace(/\/$/, '') + '/api/config', {method:'POST', headers: headers(), body});
  const text = await res.text();
  if (!res.ok) { setStatus(text, true); return; }
  setStatus(JSON.parse(text).listen_change_restart ? '已保存，监听地址变更需重启容器' : '已保存并热加�?);
}
function defaultConfig() {
  return {listen:'127.0.0.1:8888', upstream:'http://127.0.0.1:1203', health_path:'/transformer/health', stats_path:'/transformer/stats', admin_path:'/transformer/admin', max_inspect_body_bytes:1048576, preserve_host:false, emit_debug_header:false, rules:[]};
}
function esc(s) {
  return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}
loadConfig().catch(err => setStatus(String(err), true));
</script>
</body>
</html>`
