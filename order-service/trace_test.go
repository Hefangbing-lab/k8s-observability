package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// 上游传入的 traceparent：trace-id 固定，便于断言链路没有被「重新起一条」。
const upstreamTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
const upstreamTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

func withTestTracer(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	oldTP := otel.GetTracerProvider()
	oldProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(oldTP)
		otel.SetTextMapPropagator(oldProp)
	})
	return exp
}

// 核心用例：入站 traceparent 被延续，出站请求继续携带同一个 trace-id，
// 并且 server span 与 client span 构成父子关系（这是瀑布图能连起来的前提）。
func TestTracePropagationToDownstream(t *testing.T) {
	registerMetrics()
	exp := withTestTracer(t)

	var gotTraceparent string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"User7","email":"u7@example.com"}`))
	}))
	defer upstream.Close()

	oldURL := downstreamBaseURL
	downstreamBaseURL = upstream.URL
	defer func() { downstreamBaseURL = oldURL }()

	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/order/100?userId=7", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("traceparent", upstreamTraceparent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if !strings.Contains(gotTraceparent, upstreamTraceID) {
		t.Fatalf("downstream traceparent = %q, want trace-id %s", gotTraceparent, upstreamTraceID)
	}

	var serverSpan, clientSpan *tracetest.SpanStub
	for i := range exp.GetSpans() {
		s := exp.GetSpans()[i]
		switch {
		case s.SpanKind == oteltrace.SpanKindServer:
			serverSpan = &s
		case s.SpanKind == oteltrace.SpanKindClient:
			clientSpan = &s
		}
	}
	if serverSpan == nil || clientSpan == nil {
		t.Fatalf("spans not found: server=%v client=%v", serverSpan, clientSpan)
	}
	if serverSpan.Name != "GET /order/{id}" {
		t.Errorf("server span name = %q, want route template", serverSpan.Name)
	}
	if serverSpan.SpanContext.TraceID().String() != upstreamTraceID {
		t.Errorf("server trace id = %s, want inherited %s",
			serverSpan.SpanContext.TraceID(), upstreamTraceID)
	}
	if clientSpan.Parent.SpanID() != serverSpan.SpanContext.SpanID() {
		t.Errorf("client span is not a child of server span")
	}
	foundRoute := false
	for _, kv := range serverSpan.Attributes {
		if string(kv.Key) == "http.route" && kv.Value.AsString() == "GET /order/{id}" {
			foundRoute = true
		}
	}
	if !foundRoute {
		t.Errorf("http.route attribute missing on server span: %v", serverSpan.Attributes)
	}
}

// 没有入站 traceparent 时：request ID 直接用新生成的 trace ID，
// 保证「日志里的 request_id」和「Tempo 里的 traceID」是同一个字符串。
func TestRequestIDFallsBackToTraceID(t *testing.T) {
	registerMetrics()
	withTestTracer(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"U","email":"u@example.com"}`))
	}))
	defer upstream.Close()

	oldURL := downstreamBaseURL
	downstreamBaseURL = upstream.URL
	defer func() { downstreamBaseURL = oldURL }()

	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/order/1?userId=3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	id := resp.Header.Get(requestIDHeader)
	if len(id) != 32 {
		t.Fatalf("request id = %q, want 32-hex trace id", id)
	}
}

// 端到端验证导出器：起一个假的 OTLP/HTTP 接收端，确认真的发出了 protobuf 且有 service.name。
func TestOTLPExporterDeliversSpans(t *testing.T) {
	var body []byte
	var path string
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", recv.URL)
	shutdown, err := setupTracing(context.Background())
	if err != nil {
		t.Fatalf("setupTracing: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	tr := otel.Tracer("test")
	_, span := tr.Start(context.Background(), "unit-test-span")
	span.End()

	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(flushCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	if path != "/v1/traces" {
		t.Errorf("export path = %q, want /v1/traces", path)
	}
	if len(body) == 0 {
		t.Fatal("no protobuf body received")
	}
	var req collectortrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal OTLP payload: %v", err)
	}
	if len(req.ResourceSpans) == 0 {
		t.Fatal("no ResourceSpans in payload")
	}
	var svc string
	for _, kv := range req.ResourceSpans[0].Resource.Attributes {
		if kv.Key == "service.name" {
			svc = kv.Value.GetStringValue()
		}
	}
	if svc != "order-service" {
		t.Errorf("service.name = %q, want order-service", svc)
	}
	if n := len(req.ResourceSpans[0].ScopeSpans); n == 0 {
		t.Fatal("no ScopeSpans in payload")
	}
}

// exemplar 只能通过 OpenMetrics 文本格式导出：promhttp.Handler() 默认只协商
// text/plain;version=0.0.4，会把 exemplar 全丢掉，表现为 Grafana 直方图上
// 「指标点 -> trace」永远点不出来（静默失效，看日志也发现不了）。
// 这里同时锁住「协商结果」和「exemplar 真实存在」两件事。
func TestMetricsExposesExemplar(t *testing.T) {
	registerMetrics()
	withTestTracer(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"User1","email":"u1@example.com"}`))
	}))
	defer upstream.Close()
	oldURL := downstreamBaseURL
	downstreamBaseURL = upstream.URL
	defer func() { downstreamBaseURL = oldURL }()

	handler := newHandler()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/order/1", nil))

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/openmetrics-text") {
		t.Fatalf("content-type = %q, want application/openmetrics-text", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, `trace_id="`) {
		t.Fatal("exemplar 未导出：Grafana 无法从指标点跳到 trace")
	}
}
