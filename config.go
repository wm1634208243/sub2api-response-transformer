package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/template"
)

const (
	defaultListen              = ":8888"
	defaultHealthPath          = "/transformer/health"
	defaultStatsPath           = "/transformer/stats"
	defaultAdminPath           = "/transformer/admin"
	defaultMaxInspectBodyBytes = int64(1024 * 1024)
)

type Config struct {
	Listen                 string                  `json:"listen"`
	Upstream               string                  `json:"upstream"`
	HealthPath             string                  `json:"health_path"`
	StatsPath              string                  `json:"stats_path"`
	AdminPath              string                  `json:"admin_path"`
	AdminToken             string                  `json:"admin_token"`
	MaxInspectBodyBytes    int64                   `json:"max_inspect_body_bytes"`
	PreserveHost           bool                    `json:"preserve_host"`
	EmitDebugHeader        bool                    `json:"emit_debug_header"`
	RequestValidationRules []RequestValidationRule `json:"request_validation_rules,omitempty"`
	Rules                  []Rule                  `json:"rules"`
}

type RequestValidationRule struct {
	Name             string   `json:"name"`
	URLPathPrefixes  []string `json:"url_path_prefixes"`
	URLPathContains  []string `json:"url_path_contains"`
	Methods          []string `json:"methods"`
	JSONPath         string   `json:"json_path"`
	AllowedValues    []string `json:"allowed_values"`
	Required         bool     `json:"required"`
	CaseInsensitive  bool     `json:"case_insensitive"`
	DownstreamStatus int      `json:"downstream_status"`
	ResponseBody     string   `json:"response_body"`
}

type Rule struct {
	Name             string   `json:"name"`
	URLPathPrefixes  []string `json:"url_path_prefixes"`
	URLPathContains  []string `json:"url_path_contains"`
	UpstreamStatuses []int    `json:"upstream_statuses"`
	JSONPaths        []string `json:"json_paths"`
	Values           []string `json:"values"`
	MessagePaths     []string `json:"message_paths"`
	MessageContains  []string `json:"message_contains"`
	BodyContains     []string `json:"body_contains"`
	CaseInsensitive  bool     `json:"case_insensitive"`
	DownstreamStatus int      `json:"downstream_status"`
	ResponseTemplate string   `json:"response_template"`
}

type compiledConfig struct {
	Config
	target                 *url.URL
	inspectStatuses        map[int]struct{}
	requestValidationRules []compiledRequestValidationRule
	rules                  []compiledRule
}

type compiledRequestValidationRule struct {
	RequestValidationRule
	methodSet  map[string]struct{}
	allowedSet map[string]struct{}
}

type compiledRule struct {
	Rule
	statusSet    map[int]struct{}
	valueSet     map[string]struct{}
	bodyContains [][]byte
	msgContains  []string
	template     *template.Template
}

func loadConfig(path string) (*compiledConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return compileConfig(cfg)
}

func compileConfig(cfg Config) (*compiledConfig, error) {
	if cfg.Listen == "" {
		cfg.Listen = defaultListen
	}
	if cfg.HealthPath == "" {
		cfg.HealthPath = defaultHealthPath
	}
	if cfg.StatsPath == "" {
		cfg.StatsPath = defaultStatsPath
	}
	if cfg.AdminPath == "" {
		cfg.AdminPath = defaultAdminPath
	}
	if cfg.MaxInspectBodyBytes <= 0 {
		cfg.MaxInspectBodyBytes = defaultMaxInspectBodyBytes
	}
	if cfg.Upstream == "" {
		return nil, fmt.Errorf("upstream is required")
	}

	target, err := url.Parse(cfg.Upstream)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("invalid upstream %q", cfg.Upstream)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, fmt.Errorf("upstream scheme must be http or https")
	}
	if cfg.HealthPath == cfg.StatsPath || cfg.HealthPath == cfg.AdminPath || cfg.StatsPath == cfg.AdminPath {
		return nil, fmt.Errorf("health_path, stats_path and admin_path must differ")
	}

	cc := &compiledConfig{
		Config:                 cfg,
		target:                 target,
		inspectStatuses:        make(map[int]struct{}),
		requestValidationRules: make([]compiledRequestValidationRule, 0, len(cfg.RequestValidationRules)),
		rules:                  make([]compiledRule, 0, len(cfg.Rules)),
	}

	for i, rule := range cfg.RequestValidationRules {
		cr, err := compileRequestValidationRule(rule)
		if err != nil {
			return nil, fmt.Errorf("request validation rule %d: %w", i, err)
		}
		cc.requestValidationRules = append(cc.requestValidationRules, cr)
	}

	for i, rule := range cfg.Rules {
		cr, err := compileRule(rule)
		if err != nil {
			return nil, fmt.Errorf("rule %d: %w", i, err)
		}
		for status := range cr.statusSet {
			cc.inspectStatuses[status] = struct{}{}
		}
		cc.rules = append(cc.rules, cr)
	}

	return cc, nil
}

func compileRequestValidationRule(rule RequestValidationRule) (compiledRequestValidationRule, error) {
	if strings.TrimSpace(rule.Name) == "" {
		return compiledRequestValidationRule{}, fmt.Errorf("name is required")
	}
	if len(rule.URLPathPrefixes) == 0 && len(rule.URLPathContains) == 0 {
		return compiledRequestValidationRule{}, fmt.Errorf("at least one URL match condition is required")
	}
	if strings.TrimSpace(rule.JSONPath) == "" {
		return compiledRequestValidationRule{}, fmt.Errorf("json_path is required")
	}
	if len(rule.AllowedValues) == 0 {
		return compiledRequestValidationRule{}, fmt.Errorf("allowed_values is required")
	}
	if len(rule.Methods) == 0 {
		rule.Methods = []string{"POST"}
	}
	if rule.DownstreamStatus == 0 {
		rule.DownstreamStatus = http.StatusBadRequest
	}
	if rule.DownstreamStatus < 400 || rule.DownstreamStatus > 599 {
		return compiledRequestValidationRule{}, fmt.Errorf("downstream_status must be between 400 and 599")
	}
	if strings.TrimSpace(rule.ResponseBody) == "" {
		rule.ResponseBody = `{"error":{"code":400,"message":"request validation failed","status":"INVALID_ARGUMENT"}}`
	}
	if !json.Valid([]byte(rule.ResponseBody)) {
		return compiledRequestValidationRule{}, fmt.Errorf("response_body must be valid JSON")
	}

	cr := compiledRequestValidationRule{
		RequestValidationRule: rule,
		methodSet:             make(map[string]struct{}, len(rule.Methods)),
		allowedSet:            make(map[string]struct{}, len(rule.AllowedValues)),
	}
	for _, method := range rule.Methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" {
			continue
		}
		cr.methodSet[method] = struct{}{}
	}
	if len(cr.methodSet) == 0 {
		return compiledRequestValidationRule{}, fmt.Errorf("methods must contain at least one HTTP method")
	}
	for _, value := range rule.AllowedValues {
		if rule.CaseInsensitive {
			value = strings.ToLower(value)
		}
		cr.allowedSet[value] = struct{}{}
	}
	if len(cr.allowedSet) == 0 {
		return compiledRequestValidationRule{}, fmt.Errorf("allowed_values must contain at least one value")
	}
	return cr, nil
}

func compileRule(rule Rule) (compiledRule, error) {
	if strings.TrimSpace(rule.Name) == "" {
		return compiledRule{}, fmt.Errorf("name is required")
	}
	if len(rule.UpstreamStatuses) == 0 {
		return compiledRule{}, fmt.Errorf("upstream_statuses is required")
	}
	if rule.DownstreamStatus < 200 || rule.DownstreamStatus > 599 {
		return compiledRule{}, fmt.Errorf("downstream_status must be between 200 and 599")
	}
	if len(rule.JSONPaths) == 0 && len(rule.BodyContains) == 0 && len(rule.MessageContains) == 0 && len(rule.URLPathPrefixes) == 0 && len(rule.URLPathContains) == 0 {
		return compiledRule{}, fmt.Errorf("at least one match condition is required")
	}
	if len(rule.JSONPaths) > 0 && len(rule.Values) == 0 {
		return compiledRule{}, fmt.Errorf("values is required when json_paths is configured")
	}
	if len(rule.MessageContains) > 0 && len(rule.MessagePaths) == 0 {
		rule.MessagePaths = []string{"message", "error.message", "data.message"}
	}

	cr := compiledRule{
		Rule:         rule,
		statusSet:    make(map[int]struct{}, len(rule.UpstreamStatuses)),
		valueSet:     make(map[string]struct{}, len(rule.Values)),
		bodyContains: make([][]byte, 0, len(rule.BodyContains)),
		msgContains:  make([]string, 0, len(rule.MessageContains)),
	}
	for _, status := range rule.UpstreamStatuses {
		if status < 200 || status > 599 {
			return compiledRule{}, fmt.Errorf("invalid upstream status %d", status)
		}
		cr.statusSet[status] = struct{}{}
	}
	for _, value := range rule.Values {
		if rule.CaseInsensitive {
			value = strings.ToLower(value)
		}
		cr.valueSet[value] = struct{}{}
	}
	for _, value := range rule.BodyContains {
		if value == "" {
			continue
		}
		if rule.CaseInsensitive {
			value = strings.ToLower(value)
		}
		cr.bodyContains = append(cr.bodyContains, []byte(value))
	}
	for _, value := range rule.MessageContains {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if rule.CaseInsensitive {
			value = strings.ToLower(value)
		}
		cr.msgContains = append(cr.msgContains, value)
	}
	if strings.TrimSpace(rule.ResponseTemplate) != "" {
		tpl, err := template.New(rule.Name).Parse(rule.ResponseTemplate)
		if err != nil {
			return compiledRule{}, fmt.Errorf("response_template: %w", err)
		}
		cr.template = tpl
	}
	if len(rule.JSONPaths) == 0 && len(cr.bodyContains) == 0 && len(cr.msgContains) == 0 && len(rule.URLPathPrefixes) == 0 && len(rule.URLPathContains) == 0 {
		return compiledRule{}, fmt.Errorf("at least one non-empty match condition is required")
	}
	return cr, nil
}
