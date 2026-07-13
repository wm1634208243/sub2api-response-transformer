package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testConfig(t *testing.T, rules []Rule) *compiledConfig {
	t.Helper()
	cfg, err := compileConfig(Config{
		Listen:              ":0",
		Upstream:            "http://127.0.0.1:9999",
		MaxInspectBodyBytes: 1024,
		Rules:               rules,
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func testTransformer(t *testing.T, cfg *compiledConfig) *transformer {
	t.Helper()
	store := &configStore{}
	store.Store(cfg)
	return &transformer{
		configs: store,
		stats:   &proxyStats{},
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestModifyResponseTransformsNestedCodeAndPreservesBody(t *testing.T) {
	body := []byte(`{"error":{"code":451,"message":"blocked"},"data":{"region":"x"}}`)
	cfg := testConfig(t, []Rule{{
		Name:             "legal",
		UpstreamStatuses: []int{500},
		JSONPaths:        []string{"code", "error.code", "/data/code"},
		Values:           []string{"451"},
		DownstreamStatus: 451,
	}})
	cfg.EmitDebugHeader = true
	tr := testTransformer(t, cfg)
	resp := &http.Response{
		StatusCode:    500,
		Status:        "500 Internal Server Error",
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       httptest.NewRequest(http.MethodPost, "http://proxy/v1/responses", nil),
	}

	if err := tr.modifyResponse(resp); err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 451 {
		t.Fatalf("status = %d, want 451", resp.StatusCode)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body changed: %q", got)
	}
	if resp.Header.Get("X-Response-Transformed") != "legal" {
		t.Fatal("missing transformation header")
	}
}

func TestModifyResponseBodyContains(t *testing.T) {
	body := []byte(`{"message":"policy result CODE=400"}`)
	cfg := testConfig(t, []Rule{{
		Name:             "bad-request",
		UpstreamStatuses: []int{500},
		BodyContains:     []string{"code=400"},
		CaseInsensitive:  true,
		DownstreamStatus: 400,
	}})
	tr := testTransformer(t, cfg)
	resp := &http.Response{
		StatusCode: 500,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    httptest.NewRequest(http.MethodPost, "http://proxy/v1/chat/completions", nil),
	}

	if err := tr.modifyResponse(resp); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestModifyResponseURLAndMessageMatch(t *testing.T) {
	body := []byte(`{"error":{"message":"location blocked by policy"}}`)
	cfg := testConfig(t, []Rule{{
		Name:             "message-policy",
		URLPathPrefixes:  []string{"/v1/"},
		UpstreamStatuses: []int{500},
		MessagePaths:     []string{"error.message"},
		MessageContains:  []string{"blocked"},
		DownstreamStatus: 451,
	}})
	tr := testTransformer(t, cfg)
	resp := &http.Response{
		StatusCode: 500,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    httptest.NewRequest(http.MethodPost, "http://proxy/v1/chat/completions", nil),
	}

	if err := tr.modifyResponse(resp); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 451 {
		t.Fatalf("status = %d, want 451", resp.StatusCode)
	}
}

func TestModifyResponseURLMismatchDoesNotTransform(t *testing.T) {
	body := []byte(`{"code":451,"message":"blocked"}`)
	cfg := testConfig(t, []Rule{{
		Name:             "only-v1",
		URLPathPrefixes:  []string{"/v1/"},
		UpstreamStatuses: []int{500},
		JSONPaths:        []string{"code"},
		Values:           []string{"451"},
		DownstreamStatus: 451,
	}})
	tr := testTransformer(t, cfg)
	resp := &http.Response{
		StatusCode: 500,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    httptest.NewRequest(http.MethodPost, "http://proxy/dashboard", nil),
	}

	if err := tr.modifyResponse(resp); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}

func TestModifyResponseTemplate(t *testing.T) {
	body := []byte(`{"code":400,"message":"bad model","data":{"field":"model"}}`)
	cfg := testConfig(t, []Rule{{
		Name:             "template",
		UpstreamStatuses: []int{500},
		JSONPaths:        []string{"code"},
		Values:           []string{"400"},
		DownstreamStatus: 400,
		ResponseTemplate: `{"code":400,"message":"{{call .JSON "message"}}","path":"{{.Path}}"}`,
	}})
	tr := testTransformer(t, cfg)
	resp := &http.Response{
		StatusCode: 500,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    httptest.NewRequest(http.MethodPost, "http://proxy/v1/responses", nil),
	}

	if err := tr.modifyResponse(resp); err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	want := `{"code":400,"message":"bad model","path":"/v1/responses"}`
	if string(got) != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestNormalResponseIsNotRead(t *testing.T) {
	reader := &countingReadCloser{Reader: strings.NewReader("stream")}
	cfg := testConfig(t, []Rule{{
		Name:             "legal",
		UpstreamStatuses: []int{500},
		JSONPaths:        []string{"code"},
		Values:           []string{"451"},
		DownstreamStatus: 451,
	}})
	tr := testTransformer(t, cfg)
	resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: reader}

	if err := tr.modifyResponse(resp); err != nil {
		t.Fatal(err)
	}
	if reader.reads != 0 {
		t.Fatalf("normal response body was read %d times", reader.reads)
	}
}

func TestOversizedBodyIsReplayedWithoutLoss(t *testing.T) {
	body := []byte("0123456789")
	cfg := testConfig(t, []Rule{{
		Name:             "large",
		UpstreamStatuses: []int{500},
		BodyContains:     []string{"451"},
		DownstreamStatus: 451,
	}})
	cfg.MaxInspectBodyBytes = 4
	tr := testTransformer(t, cfg)
	resp := &http.Response{
		StatusCode:    500,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: -1,
	}

	if err := tr.modifyResponse(resp); err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("replayed body = %q", got)
	}
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}

func TestGzipBodyCanMatchWithoutChangingCompressedBytes(t *testing.T) {
	plain := []byte(`{"code":451,"message":"blocked"}`)
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	_, _ = zw.Write(plain)
	_ = zw.Close()
	raw := append([]byte(nil), compressed.Bytes()...)

	cfg := testConfig(t, []Rule{{
		Name:             "gzip-legal",
		UpstreamStatuses: []int{500},
		JSONPaths:        []string{"code"},
		Values:           []string{"451"},
		DownstreamStatus: 451,
	}})
	tr := testTransformer(t, cfg)
	resp := &http.Response{
		StatusCode: 500,
		Header:     http.Header{"Content-Encoding": []string{"gzip"}},
		Body:       io.NopCloser(bytes.NewReader(raw)),
		Request:    httptest.NewRequest(http.MethodPost, "http://proxy/v1/responses", nil),
	}

	if err := tr.modifyResponse(resp); err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 451 {
		t.Fatalf("status = %d, want 451", resp.StatusCode)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("gzip body bytes changed")
	}
}

func TestProxyIntegration(t *testing.T) {
	body := []byte(`{"code":400,"message":"invalid parameter","data":{"field":"model"}}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	cfg, err := compileConfig(Config{
		Listen:              ":0",
		Upstream:            upstream.URL,
		MaxInspectBodyBytes: 1024,
		Rules: []Rule{{
			Name:             "bad-request",
			UpstreamStatuses: []int{500},
			JSONPaths:        []string{"code"},
			Values:           []string{"400"},
			DownstreamStatus: 400,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &configStore{}
	store.Store(cfg)
	stats := &proxyStats{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	proxy := newProxy(store, stats, logger)
	server := httptest.NewServer(newHandler(store, proxy, stats, t.TempDir()+"/config.json", logger))
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body changed: %s", got)
	}
}

type countingReadCloser struct {
	io.Reader
	reads int
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	r.reads++
	return r.Reader.Read(p)
}

func (r *countingReadCloser) Close() error { return nil }

func BenchmarkNormalResponseBypass(b *testing.B) {
	cfg, _ := compileConfig(Config{
		Upstream: "http://127.0.0.1:1203",
		Rules: []Rule{{
			Name:             "legal",
			UpstreamStatuses: []int{500},
			JSONPaths:        []string{"code"},
			Values:           []string{"451"},
			DownstreamStatus: 451,
		}},
	})
	store := &configStore{}
	store.Store(cfg)
	tr := &transformer{configs: store, stats: &proxyStats{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	resp := &http.Response{StatusCode: 200}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = tr.modifyResponse(resp)
	}
}
