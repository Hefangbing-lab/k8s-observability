package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestHealthAndMetricsAreNotInstrumented(t *testing.T) {
	registerMetrics()
	before := counterTotal(t)

	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	// 探针与 Prometheus 抓取都不应让业务计数器 +1。
	for _, path := range []string{"/health", "/metrics", "/health"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: got status %d", path, resp.StatusCode)
		}
	}
	if got := counterTotal(t); got != before {
		t.Fatalf("health/metrics polluted counter: before=%v after=%v", before, got)
	}
}

func TestUserHandlerIsInstrumentedAndHasRequestID(t *testing.T) {
	registerMetrics()
	before := counterTotal(t)

	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/user/3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d", resp.StatusCode)
	}
	if resp.Header.Get(requestIDHeader) == "" {
		t.Fatal("missing X-Request-ID response header")
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["name"] != "User3" {
		t.Fatalf("unexpected body: %v", body)
	}
	if got := counterTotal(t); got <= before {
		t.Fatalf("business request not counted: %v -> %v", before, got)
	}
}

func TestStatusWriterKeepsFirstStatusAndOptionalInterfaces(t *testing.T) {
	rec := httptest.NewRecorder()
	ww := &statusWriter{ResponseWriter: rec, statusCode: http.StatusOK}

	ww.WriteHeader(http.StatusInternalServerError)
	ww.WriteHeader(http.StatusOK)
	if _, err := ww.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}

	if ww.statusCode != http.StatusInternalServerError {
		t.Fatalf("status overwritten: got %d", ww.statusCode)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("recorder got %d", rec.Code)
	}
	if _, ok := interface{}(ww).(http.Flusher); !ok {
		t.Fatal("http.Flusher lost after wrapping")
	}
	if _, ok := interface{}(ww).(http.Hijacker); !ok {
		t.Fatal("http.Hijacker lost after wrapping")
	}
}

func counterTotal(t *testing.T) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "http_requests_total" {
			var total float64
			for _, m := range mf.GetMetric() {
				total += m.GetCounter().GetValue()
			}
			return total
		}
	}
	return 0
}
