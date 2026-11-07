package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	// 走 Collector 而不是直连 Tempo：采样、批处理、多后端扇出都在 Collector 做，
	// 业务进程只认一个 OTLP 端点，换后端不用改代码。
	defaultOTLPEndpoint = "http://otel-collector.monitoring.svc.cluster.local:4318"
	otelServiceName     = "user-service"
)

// setupTracing 建立 OTLP/HTTP 导出链路。
// 返回 error 时调用方只降级（打一条 warn 继续跑）：
// 可观测性组件故障不该拖垮业务进程，这是与 metrics 一致的设计原则。
func setupTracing(ctx context.Context) (func(context.Context) error, error) {
	endpoint := getenv("OTEL_EXPORTER_OTLP_ENDPOINT", defaultOTLPEndpoint)

	// 显式拼上 /v1/traces：WithEndpointURL 是「完整 URL」语义，不会像 base endpoint 那样
	// 自动补信号路径，少一段就会打到 Collector 的根路径并被 404 丢弃（静默丢 span）。
	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(strings.TrimSuffix(endpoint, "/")+"/v1/traces"),
		otlptracehttp.WithTimeout(3*time.Second),
	)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", otelServiceName),
			attribute.String("service.version", getenv("SERVICE_VERSION", "dev")),
			attribute.String("deployment.environment", getenv("DEPLOY_ENV", "dev")),
		),
		resource.WithHost(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exp,
			sdktrace.WithBatchTimeout(2*time.Second),
			sdktrace.WithMaxExportBatchSize(512),
		),
		// 父级已采样则沿用父级决定（保证一条链路要么全采要么全不采），
		// 无父级时按比例采样，生产通过 OTEL_TRACES_SAMPLER_ARG 调低。
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRatio()))),
	)
	otel.SetTracerProvider(tp)
	// W3C Trace Context（traceparent）+ Baggage：跨服务透传的标准头。
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	return tp.Shutdown, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func sampleRatio() float64 {
	v := os.Getenv("OTEL_TRACES_SAMPLER_ARG")
	if v == "" {
		return 1.0
	}
	r, err := strconv.ParseFloat(v, 64)
	if err != nil || r < 0 || r > 1 {
		slog.Warn("invalid OTEL_TRACES_SAMPLER_ARG, fallback to 1.0", "value", v)
		return 1.0
	}
	return r
}
