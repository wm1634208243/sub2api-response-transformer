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
  <title>Response Transformer</title>
  <link rel="icon" href="data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><rect width='100' height='100' rx='20' fill='%23112c36'/><rect x='10' y='10' width='80' height='80' rx='16' fill='none' stroke='%232b9da7' stroke-width='2'/><text x='50' y='68' text-anchor='middle' font-family='system-ui,sans-serif' font-size='48' font-weight='800' fill='%2333c7cf'>RT</text></svg>">
  <style>
    :root { color-scheme: dark; font-family: Inter, "Microsoft YaHei", ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; --bg: #0b111b; --surface: #121b29; --surface-2: #192538; --line: #28384d; --text: #edf3fa; --muted: #9cb0c8; --accent: #33c7cf; --accent-strong: #17aab5; --danger: #ff7373; --danger-bg: #402128; --ok: #58d68d; }
    * { box-sizing: border-box; }
    body { margin: 0; min-width: 320px; background: var(--bg); color: var(--text); }
    header { min-height: 72px; display: flex; align-items: center; gap: 16px; padding: 14px max(20px, calc((100vw - 1280px) / 2)); border-bottom: 1px solid var(--line); background: #0e1724; position: sticky; top: 0; z-index: 10; }
    .brand { display: flex; align-items: center; gap: 11px; margin-right: auto; min-width: 0; }
    .brand-mark { width: 34px; height: 34px; border: 1px solid #2b9da7; border-radius: 8px; display: grid; place-items: center; color: var(--accent); font-weight: 800; font-size: 15px; background: #112c36; }
    .brand h1 { margin: 0; font-size: 16px; letter-spacing: 0; white-space: nowrap; }
    .brand p { color: var(--muted); font-size: 12px; margin: 2px 0 0; }
    main { width: min(1280px, calc(100% - 40px)); margin: 28px auto 56px; }
    .page-title { display: flex; justify-content: space-between; align-items: end; gap: 20px; margin-bottom: 22px; }
    .page-title h2 { margin: 0; font-size: 23px; letter-spacing: 0; }
    .page-title p { color: var(--muted); margin: 6px 0 0; font-size: 13px; }
    .status { min-height: 22px; color: var(--muted); font-size: 13px; text-align: right; }
    .panel { background: var(--surface); border: 1px solid var(--line); border-radius: 8px; margin-bottom: 16px; overflow: hidden; }
    .panel-header { display: flex; align-items: center; justify-content: space-between; gap: 14px; padding: 15px 18px; border-bottom: 1px solid var(--line); background: rgba(255,255,255,.015); }
    .panel-title { margin: 0; font-size: 14px; font-weight: 700; }
    .panel-note { color: var(--muted); font-size: 12px; }
    .panel-body { padding: 18px; }
    .grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 14px; }
    .rule-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 14px; }
    label { display: grid; gap: 7px; color: var(--muted); font-size: 12px; }
    input, textarea, select { appearance: none; width: 100%; border: 1px solid #34465e; border-radius: 6px; background: #0d1521; color: var(--text); padding: 10px 11px; font: inherit; font-size: 13px; outline: none; transition: border-color .15s, box-shadow .15s; }
    input:focus, textarea:focus, select:focus { border-color: var(--accent); box-shadow: 0 0 0 3px rgba(51,199,207,.13); }
    input::placeholder, textarea::placeholder { color: #64758a; }
    textarea { min-height: 86px; resize: vertical; line-height: 1.45; font-family: ui-monospace, SFMono-Regular, Consolas, monospace; }
    select { background-image: linear-gradient(45deg, transparent 50%, #a9bacd 50%), linear-gradient(135deg, #a9bacd 50%, transparent 50%); background-position: calc(100% - 16px) 50%, calc(100% - 11px) 50%; background-size: 5px 5px, 5px 5px; background-repeat: no-repeat; padding-right: 32px; }
    .wide { grid-column: 1 / -1; }
    .two { grid-column: span 2; }
    .actions { display: flex; align-items: center; gap: 9px; flex-wrap: wrap; }
    button { border: 1px solid transparent; border-radius: 6px; padding: 9px 13px; background: #243247; color: var(--text); font: inherit; font-size: 13px; font-weight: 650; cursor: pointer; transition: background .15s, border-color .15s, transform .15s; }
    button:hover { background: #30415a; }
    button:active { transform: translateY(1px); }
    button.primary { background: var(--accent); color: #062127; }
    button.primary:hover { background: #63dde1; }
    button.outline { border-color: #3c526d; background: transparent; }
    button.danger { color: #ffb4b4; border-color: #73404a; background: var(--danger-bg); }
    button.danger:hover { background: #552a33; }
    .token { width: 230px; max-width: 28vw; }
    .rule { border-left: 3px solid var(--accent); }
    .rule + .rule { margin-top: 16px; }
    .rule-meta { display: flex; align-items: center; gap: 9px; min-width: 0; }
    .rule-index { color: var(--accent); font-weight: 800; font-size: 12px; }
    .hint { color: var(--muted); font-size: 12px; line-height: 1.5; }
    .json-view { min-height: 240px; background: #09111b; color: #c9e3f4; }
    .empty { padding: 34px 18px; text-align: center; color: var(--muted); border: 1px dashed #36506c; border-radius: 6px; }
    @media (max-width: 900px) { header { padding: 12px 18px; flex-wrap: wrap; } .token { order: 3; width: 100%; max-width: none; } main { width: min(100% - 28px, 700px); margin-top: 20px; } .grid, .rule-grid { grid-template-columns: 1fr 1fr; } .two { grid-column: span 2; } }
    @media (max-width: 600px) { .brand p { display: none; } .page-title { display: block; } .status { text-align: left; margin-top: 10px; } .grid, .rule-grid { grid-template-columns: 1fr; } .two, .wide { grid-column: auto; } .panel-body { padding: 14px; } header .actions { width: 100%; } header .actions button { flex: 1; } }
  </style>
</head>
<body>
  <header>
    <div class="brand"><div class="brand-mark">RT</div><div><h1>Response Transformer</h1><p>Sub2API 响应状态转换代理</p></div></div>
    <input id="token" class="token" type="password" placeholder="管理令牌">
    <div class="actions"><button class="outline" type="button" onclick="loadConfig()">加载配置</button><button class="primary" type="button" onclick="saveConfig()">保存并热加载</button></div>
  </header>
  <main>
    <div class="page-title"><div><h2>代理配置</h2><p>配置上游连接与响应状态转换规则。</p></div><div id="status" class="status" aria-live="polite"></div></div>
    <section class="panel">
      <div class="panel-header"><h3 class="panel-title">服务设置</h3><span class="panel-note">修改监听地址后需要重启服务</span></div>
      <div class="panel-body"><div class="grid">
        <label>监听地址<input id="listen" placeholder="127.0.0.1:8888"></label>
        <label>上游 Sub2API<input id="upstream" placeholder="http://127.0.0.1:1203"></label>
        <label>最大检查 Body 字节<input id="max_inspect_body_bytes" type="number" min="1" placeholder="1048576"></label>
        <label>健康检查路径<input id="health_path"></label>
        <label>统计路径<input id="stats_path"></label>
        <label>管理页路径<input id="admin_path"></label>
        <label>管理令牌<input id="admin_token" type="password" placeholder="留空表示不鉴权"></label>
        <label>透传 Host<select id="preserve_host"><option value="false">否</option><option value="true">是</option></select></label>
        <label>返回调试响应头<select id="emit_debug_header"><option value="false">否</option><option value="true">是</option></select></label>
      </div></div>
    </section>
    <section class="panel">
      <div class="panel-header"><div><h3 class="panel-title">转换规则</h3><div class="panel-note">按顺序匹配，第一条命中后停止。</div></div><div class="actions"><button class="outline" type="button" onclick="showJSON()">查看 JSON</button><button class="primary" type="button" onclick="addRule()">新增规则</button></div></div>
      <div class="panel-body"><div id="rules"></div></div>
    </section>
    <section class="panel">
      <div class="panel-header"><h3 class="panel-title">当前配置 JSON</h3><span class="panel-note">保存前可在此核对</span></div>
      <div class="panel-body"><textarea id="jsonView" class="json-view" spellcheck="false" readonly></textarea></div>
    </section>
  </main>
<script>
let config = null;
const $ = (id) => document.getElementById(id);
const lines = (value) => String(value || '').split(/\r?\n/).map((s) => s.trim()).filter(Boolean);
const csvNums = (value) => String(value || '').split(/[,\s]+/).map((s) => s.trim()).filter(Boolean).map(Number).filter(Number.isFinite);
const bool = (id) => $(id).value === 'true';
const token = () => $('token').value || localStorage.getItem('transformer_admin_token') || '';
function headers() { const h = {'Content-Type': 'application/json'}; if (token()) h['X-Admin-Token'] = token(); return h; }
function setStatus(message, bad) { $('status').textContent = message; $('status').style.color = bad ? '#ff9b9b' : '#9cb0c8'; }
function endpoint() { return location.pathname.replace(/\/$/, '') + '/api/config'; }
async function loadConfig() {
  if ($('token').value) localStorage.setItem('transformer_admin_token', $('token').value);
  setStatus('正在加载配置...', false);
  const res = await fetch(endpoint(), {headers: headers()});
  if (!res.ok) { setStatus(await res.text(), true); return; }
  config = await res.json(); fillForm(); setStatus('配置已加载', false);
}
function fillForm() {
  for (const key of ['listen','upstream','health_path','stats_path','admin_path','admin_token','max_inspect_body_bytes']) $(key).value = config[key] ?? '';
  $('preserve_host').value = String(!!config.preserve_host); $('emit_debug_header').value = String(!!config.emit_debug_header);
  renderRules(); showJSON();
}
function readForm() {
  config = config || defaultConfig();
  config.listen = $('listen').value; config.upstream = $('upstream').value; config.health_path = $('health_path').value; config.stats_path = $('stats_path').value; config.admin_path = $('admin_path').value; config.admin_token = $('admin_token').value;
  config.max_inspect_body_bytes = Number($('max_inspect_body_bytes').value || 1048576); config.preserve_host = bool('preserve_host'); config.emit_debug_header = bool('emit_debug_header');
  config.rules = [...document.querySelectorAll('.rule')].map(readRule); return config;
}
function renderRules() {
  const target = $('rules'); target.innerHTML = '';
  if (!(config.rules || []).length) { target.innerHTML = '<div class="empty">尚未设置转换规则。新增规则后，代理才会改写匹配响应的状态码。</div>'; return; }
  config.rules.forEach((rule, index) => target.appendChild(ruleNode(rule, index)));
}
function ruleNode(rule, index) {
  const div = document.createElement('section'); div.className = 'panel rule';
  div.innerHTML = '<div class="panel-header"><div class="rule-meta"><span class="rule-index">RULE ' + String(index + 1).padStart(2, '0') + '</span><span class="panel-note">命中后返回下游状态码</span></div><button class="danger" type="button" data-remove-rule="1">删除规则</button></div><div class="panel-body"><div class="rule-grid">' +
    field('名称', 'name', rule.name || '', 'input') + field('上游状态码', 'upstream_statuses', (rule.upstream_statuses || []).join(','), 'input', '500') + field('下游状态码', 'downstream_status', rule.downstream_status || 400, 'input', '', 'number') +
    field('URL 前缀，每行一个', 'url_path_prefixes', (rule.url_path_prefixes || []).join('\n'), 'textarea') + field('URL 包含，每行一个', 'url_path_contains', (rule.url_path_contains || []).join('\n'), 'textarea') + selectField('忽略大小写', 'case_insensitive', !!rule.case_insensitive) +
    field('Code JSON 路径，每行一个', 'json_paths', (rule.json_paths || []).join('\n'), 'textarea', 'error.code') + field('Code 值，每行一个', 'values', (rule.values || []).join('\n'), 'textarea', '451') + field('Message JSON 路径，每行一个', 'message_paths', (rule.message_paths || []).join('\n'), 'textarea') +
    field('Message 包含，每行一个', 'message_contains', (rule.message_contains || []).join('\n'), 'textarea') + field('Body 包含，每行一个', 'body_contains', (rule.body_contains || []).join('\n'), 'textarea', '', '', 'two') +
    field('响应模板，留空则原样透传上游 Body', 'response_template', rule.response_template || '', 'textarea', '', '', 'wide') +
    '</div></div>';
  div.querySelector('[data-k="case_insensitive"]').value = String(!!rule.case_insensitive);
  div.querySelector('[data-remove-rule]').addEventListener('click', () => { const i = [...document.querySelectorAll('.rule')].indexOf(div); config.rules.splice(i, 1); renderRules(); showJSON(); });
  div.addEventListener('input', showJSON); div.addEventListener('change', showJSON); return div;
}
function field(label, key, value, tag, placeholder, type, className) { const cls = className ? ' class="' + className + '"' : ''; const ph = placeholder ? ' placeholder="' + esc(placeholder) + '"' : ''; const ty = type ? ' type="' + type + '"' : ''; if (tag === 'textarea') return '<label' + cls + '>' + label + '<textarea data-k="' + key + '"' + ph + '>' + esc(value) + '</textarea></label>'; return '<label' + cls + '>' + label + '<input data-k="' + key + '" value="' + esc(value) + '"' + ph + ty + '></label>'; }
function selectField(label, key, value) { return '<label>' + label + '<select data-k="' + key + '"><option value="false">否</option><option value="true">是</option></select></label>'; }
function readRule(el) { const v = (k) => el.querySelector('[data-k="' + k + '"]').value; return {name:v('name'),url_path_prefixes:lines(v('url_path_prefixes')),url_path_contains:lines(v('url_path_contains')),upstream_statuses:csvNums(v('upstream_statuses')),json_paths:lines(v('json_paths')),values:lines(v('values')),message_paths:lines(v('message_paths')),message_contains:lines(v('message_contains')),body_contains:lines(v('body_contains')),case_insensitive:v('case_insensitive') === 'true',downstream_status:Number(v('downstream_status') || 500),response_template:v('response_template')}; }
function addRule() { config = readForm(); config.rules.push({name:'restore-451',url_path_prefixes:['/v1/','/v1beta/'],url_path_contains:[],upstream_statuses:[500],json_paths:['error.code'],values:['451'],message_paths:[],message_contains:[],body_contains:[],case_insensitive:false,downstream_status:451,response_template:''}); renderRules(); showJSON(); }
function showJSON() { if (!config) return; $('jsonView').value = JSON.stringify(readForm(), null, 2); }
async function saveConfig() { if (!config) { await loadConfig(); return; } if ($('token').value) localStorage.setItem('transformer_admin_token', $('token').value); setStatus('正在保存配置...', false); const res = await fetch(endpoint(), {method:'POST',headers:headers(),body:JSON.stringify(readForm())}); const text = await res.text(); if (!res.ok) { setStatus(text, true); return; } const result = JSON.parse(text); setStatus(result.listen_change_restart ? '已保存。监听地址变更后需重启服务。' : '已保存并热加载。', false); }
function defaultConfig() { return {listen:'127.0.0.1:8888',upstream:'http://127.0.0.1:1203',health_path:'/transformer/health',stats_path:'/transformer/stats',admin_path:'/transformer/admin',max_inspect_body_bytes:1048576,preserve_host:false,emit_debug_header:false,rules:[]}; }
function esc(value) { return String(value).replace(/[&<>"']/g, (char) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char])); }
loadConfig().catch((err) => setStatus(String(err), true));
</script>
</body>
</html>`
