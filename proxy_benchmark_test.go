package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func benchmarkTransformer(b *testing.B, ruleCount int, matchLast bool) (*transformer, []byte) {
	b.Helper()

	rules := make([]Rule, 0, ruleCount)
	for i := 0; i < ruleCount; i++ {
		value := "451"
		if matchLast && i < ruleCount-1 {
			value = fmt.Sprintf("no-match-%d", i)
		}
		rules = append(rules, Rule{
			Name:             fmt.Sprintf("restore-%d", i+1),
			URLPathPrefixes:  []string{"/v1/"},
			UpstreamStatuses: []int{500},
			JSONPaths:        []string{"code", "error.code", "data.code"},
			Values:           []string{value},
			DownstreamStatus: 451,
		})
	}

	cfg, err := compileConfig(Config{
		Upstream:            "http://127.0.0.1:1203",
		MaxInspectBodyBytes: 1024 * 1024,
		Rules:               rules,
	})
	if err != nil {
		b.Fatal(err)
	}

	store := &configStore{}
	store.Store(cfg)
	return &transformer{
		configs: store,
		stats:   &proxyStats{},
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, []byte(`{"error":{"code":451,"message":"blocked by upstream policy"},"data":{"region":"test"}}`)
}

func BenchmarkTransformSmallJSONOneRule(b *testing.B) {
	tr, body := benchmarkTransformer(b, 1, false)
	req := httptest.NewRequest(http.MethodPost, "http://proxy/v1/chat/completions", nil)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp := &http.Response{
			StatusCode:    500,
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
			Request:       req,
		}
		if err := tr.modifyResponse(resp); err != nil {
			b.Fatal(err)
		}
		if resp.StatusCode != 451 {
			b.Fatalf("status = %d, want 451", resp.StatusCode)
		}
	}
}

func BenchmarkTransformSmallJSONTenRulesWorstCase(b *testing.B) {
	tr, body := benchmarkTransformer(b, 10, true)
	req := httptest.NewRequest(http.MethodPost, "http://proxy/v1/chat/completions", nil)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp := &http.Response{
			StatusCode:    500,
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
			Request:       req,
		}
		if err := tr.modifyResponse(resp); err != nil {
			b.Fatal(err)
		}
		if resp.StatusCode != 451 {
			b.Fatalf("status = %d, want 451", resp.StatusCode)
		}
	}
}
