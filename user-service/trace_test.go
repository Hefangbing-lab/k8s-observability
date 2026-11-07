package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// 上游（order-service）传来的 traceparent 必须被延续，而不是新起一条链路。
func TestServerSpanContinuesUpstreamTrace(t *testing.T) {
	registerMetrics()

	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	oldTP, oldProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(oldTP)
		otel.SetTextMapPropagator(oldProp)
	})

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	const traceparent = "00-" + traceID + "-00f067aa0ba902b7-01"

	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/user/7", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("traceparent", traceparent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	// 没有入站 X-Request-ID 时，响应头里的 request id 就是 trace id（日志与 trace 共用标识）。
	if got := resp.Header.Get(requestIDHeader); got != traceID {
		t.Errorf("request id = %q, want trace id %q", got, traceID)
	}

	var span *tracetest.SpanStub
	for i := range exp.GetSpans() {
		if s := exp.GetSpans()[i]; s.SpanKind == oteltrace.SpanKindServer {
			span = &s
		}
	}
	if span == nil {
		t.Fatal("server span not recorded")
	}
	if span.Name != "GET /user/{id}" {
		t.Errorf("span name = %q, want route template", span.Name)
	}
	if span.SpanContext.TraceID().String() != traceID {
		t.Errorf("trace id = %s, want %s", span.SpanContext.TraceID(), traceID)
	}
	if span.Parent.SpanID().String() != "00f067aa0ba902b7" {
		t.Errorf("parent span id = %s, want upstream span id", span.Parent.SpanID())
	}
}

// 探针与抓取不能产生 span，否则 Tempo 里会被 /health 淹没。
func TestHealthAndMetricsProduceNoSpans(t *testing.T) {
	registerMetrics()

	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	oldTP := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(oldTP)
	})

	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	for _, p := range []string{"/health", "/metrics"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	if n := len(exp.GetSpans()); n != 0 {
		t.Fatalf("management endpoints produced %d spans, want 0", n)
	}
}
