package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type configStore struct {
	value atomic.Pointer[compiledConfig]
}

func (s *configStore) Load() *compiledConfig {
	return s.value.Load()
}

func (s *configStore) Store(cfg *compiledConfig) {
	s.value.Store(cfg)
}

type proxyStats struct {
	requests          atomic.Uint64
	inspectedRequests atomic.Uint64
	rejectedRequests  atomic.Uint64
	requestTooLarge   atomic.Uint64
	inspected         atomic.Uint64
	transformed       atomic.Uint64
	tooLarge          atomic.Uint64
	unsupportedCoding atomic.Uint64
	proxyErrors       atomic.Uint64
}

func (s *proxyStats) snapshot() map[string]uint64 {
	return map[string]uint64{
		"requests":                  s.requests.Load(),
		"inspected_requests":        s.inspectedRequests.Load(),
		"rejected_requests":         s.rejectedRequests.Load(),
		"skipped_request_too_large": s.requestTooLarge.Load(),
		"inspected_responses":       s.inspected.Load(),
		"transformed_responses":     s.transformed.Load(),
		"skipped_body_too_large":    s.tooLarge.Load(),
		"skipped_content_encoding":  s.unsupportedCoding.Load(),
		"proxy_errors":              s.proxyErrors.Load(),
	}
}

type transformer struct {
	configs *configStore
	stats   *proxyStats
	logger  *slog.Logger
}

func newProxy(store *configStore, stats *proxyStats, logger *slog.Logger) *httputil.ReverseProxy {
	t := &transformer{configs: store, stats: stats, logger: logger}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          2048,
		MaxIdleConnsPerHost:   1024,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}

	return &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			cfg := store.Load()
			req.SetURL(cfg.target)
			req.SetXForwarded()
			if !cfg.PreserveHost {
				req.Out.Host = cfg.target.Host
			}
		},
		Transport:      transport,
		ModifyResponse: t.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			stats.proxyErrors.Add(1)
			logger.Error("upstream_proxy_error", "method", r.Method, "path", r.URL.Path, "error", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":{"type":"proxy_error","message":"upstream unavailable"}}`)
		},
	}
}

func (t *transformer) modifyResponse(resp *http.Response) error {
	cfg := t.configs.Load()
	if _, ok := cfg.inspectStatuses[resp.StatusCode]; !ok {
		return nil
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return nil
	}
	if resp.ContentLength > cfg.MaxInspectBodyBytes {
		t.stats.tooLarge.Add(1)
		return nil
	}

	raw, complete, err := readBodyPrefix(resp.Body, cfg.MaxInspectBodyBytes)
	if err != nil {
		return fmt.Errorf("read inspect body: %w", err)
	}
	if !complete {
		t.stats.tooLarge.Add(1)
		resp.Body = &replayReadCloser{Reader: io.MultiReader(bytes.NewReader(raw), resp.Body), closer: resp.Body}
		return nil
	}
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	resp.Header.Set("Content-Length", strconv.Itoa(len(raw)))

	inspectBody, ok := decodeBodyForInspection(resp.Header.Get("Content-Encoding"), raw, cfg.MaxInspectBodyBytes)
	if !ok {
		t.stats.unsupportedCoding.Add(1)
		return nil
	}
	t.stats.inspected.Add(1)

	rule := cfg.match(resp.StatusCode, resp.Request.URL.Path, inspectBody)
	if rule == nil {
		return nil
	}

	oldStatus := resp.StatusCode
	resp.StatusCode = rule.DownstreamStatus
	resp.Status = fmt.Sprintf("%d %s", rule.DownstreamStatus, http.StatusText(rule.DownstreamStatus))
	if rule.template != nil {
		rendered, err := renderResponseTemplate(rule, resp, oldStatus, inspectBody)
		if err != nil {
			return fmt.Errorf("render response template: %w", err)
		}
		resp.Body = io.NopCloser(bytes.NewReader(rendered))
		resp.ContentLength = int64(len(rendered))
		resp.Header.Set("Content-Length", strconv.Itoa(len(rendered)))
		resp.Header.Del("Content-Encoding")
		if resp.Header.Get("Content-Type") == "" {
			resp.Header.Set("Content-Type", "application/json; charset=utf-8")
		}
	}
	if cfg.EmitDebugHeader {
		resp.Header.Set("X-Response-Transformed", rule.Name)
	}
	t.stats.transformed.Add(1)
	t.logger.Info("response_status_transformed",
		"rule", rule.Name,
		"method", resp.Request.Method,
		"path", resp.Request.URL.Path,
		"from", oldStatus,
		"to", rule.DownstreamStatus,
	)
	return nil
}

type responseTemplateContext struct {
	RuleName         string
	Method           string
	Path             string
	UpstreamStatus   int
	DownstreamStatus int
	RawBody          string
	JSON             func(string) string
}

func renderResponseTemplate(rule *compiledRule, resp *http.Response, upstreamStatus int, body []byte) ([]byte, error) {
	decoded := decodeJSONBody(body)
	ctx := responseTemplateContext{
		RuleName:         rule.Name,
		Method:           resp.Request.Method,
		Path:             resp.Request.URL.Path,
		UpstreamStatus:   upstreamStatus,
		DownstreamStatus: rule.DownstreamStatus,
		RawBody:          string(body),
		JSON: func(path string) string {
			value, ok := lookupJSONPath(decoded, path)
			if !ok {
				return ""
			}
			return scalarString(value)
		},
	}
	var out bytes.Buffer
	if err := rule.template.Execute(&out, ctx); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func readBodyPrefix(body io.Reader, max int64) ([]byte, bool, error) {
	limited := io.LimitReader(body, max+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if int64(len(raw)) > max {
		return raw, false, nil
	}
	return raw, true, nil
}

func decodeBodyForInspection(contentEncoding string, raw []byte, max int64) ([]byte, bool) {
	encoding := strings.ToLower(strings.TrimSpace(contentEncoding))
	switch encoding {
	case "", "identity":
		return raw, true
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, false
		}
		defer reader.Close()
		decoded, err := io.ReadAll(io.LimitReader(reader, max+1))
		if err != nil || int64(len(decoded)) > max {
			return nil, false
		}
		return decoded, true
	default:
		return nil, false
	}
}

type replayReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r *replayReadCloser) Close() error {
	return r.closer.Close()
}

func validateRequest(cfg *compiledConfig, stats *proxyStats, logger *slog.Logger, w http.ResponseWriter, r *http.Request) bool {
	if len(cfg.requestValidationRules) == 0 || r.Body == nil {
		return false
	}

	candidates := make([]*compiledRequestValidationRule, 0, len(cfg.requestValidationRules))
	for i := range cfg.requestValidationRules {
		rule := &cfg.requestValidationRules[i]
		if _, ok := rule.methodSet[strings.ToUpper(r.Method)]; !ok {
			continue
		}
		if !rule.matchesRequestPath(r.URL.Path) {
			continue
		}
		candidates = append(candidates, rule)
	}
	if len(candidates) == 0 {
		return false
	}

	raw, complete, err := readBodyPrefix(r.Body, cfg.MaxInspectBodyBytes)
	if err != nil {
		logger.Warn("request_validation_read_failed", "method", r.Method, "path", r.URL.Path, "error", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return true
	}
	if !complete {
		stats.requestTooLarge.Add(1)
		r.Body = &replayReadCloser{Reader: io.MultiReader(bytes.NewReader(raw), r.Body), closer: r.Body}
		return false
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))

	decoded := decodeJSONBody(raw)
	if decoded == nil {
		return false
	}
	stats.inspectedRequests.Add(1)

	for _, rule := range candidates {
		value, exists := lookupJSONPath(decoded, rule.JSONPath)
		if !exists && !rule.Required {
			continue
		}

		allowed := false
		if exists {
			text := scalarString(value)
			if rule.CaseInsensitive {
				text = strings.ToLower(text)
			}
			_, allowed = rule.allowedSet[text]
		}
		if allowed {
			continue
		}

		if cfg.EmitDebugHeader {
			w.Header().Set("X-Request-Rejected", rule.Name)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(rule.DownstreamStatus)
		_, _ = io.WriteString(w, rule.ResponseBody)
		stats.rejectedRequests.Add(1)
		logger.Info("request_rejected",
			"rule", rule.Name,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rule.DownstreamStatus,
		)
		return true
	}
	return false
}

func (r *compiledRequestValidationRule) matchesRequestPath(path string) bool {
	for _, prefix := range r.URLPathPrefixes {
		if prefix != "" && strings.HasPrefix(path, prefix) {
			return true
		}
	}
	for _, value := range r.URLPathContains {
		if value != "" && strings.Contains(path, value) {
			return true
		}
	}
	return false
}

func newHandler(store *configStore, proxy *httputil.ReverseProxy, stats *proxyStats, configPath string, logger *slog.Logger) http.Handler {
	admin := newAdminHandler(store, configPath, logger)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := store.Load()
		switch r.URL.Path {
		case cfg.HealthPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"ok"}`)
			return
		case cfg.StatsPath:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(stats.snapshot())
			return
		case cfg.AdminPath:
			admin.ServeHTTP(w, r)
			return
		default:
			if strings.HasPrefix(r.URL.Path, cfg.AdminPath+"/") {
				admin.ServeHTTP(w, r)
				return
			}
			stats.requests.Add(1)
			if validateRequest(cfg, stats, logger, w, r) {
				return
			}
			proxy.ServeHTTP(w, r)
		}
	})
}
