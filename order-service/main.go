package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// 默认 DefBuckets 从 5ms 起、间距过大，毫秒级内部调用的 P99 会严重失真。
var latencyBuckets = []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

var (
	httpRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Number of HTTP requests",
		},
		// handler 存路由模板，不是原始 URL，避免标签基数爆炸。
		[]string{"code", "method", "handler"},
	)
	httpDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency",
			Buckets: latencyBuckets,
		},
		[]string{"code", "method", "handler"},
	)
)

const requestIDHeader = "X-Request-ID"

// 下游地址抽成变量（可用 USER_SERVICE_URL 覆盖），单元测试用 httptest.Server 替换。
var downstreamBaseURL = getenv("USER_SERVICE_URL", "http://user-service")

var validUserID = regexp.MustCompile(`^[0-9]{1,18}$`)

// 显式配置超时与连接池：DefaultClient 没有超时，DefaultTransport 每 host 只留 2 条 idle 连接。
// Transport 再包一层 otelhttp：出站请求自动注入 traceparent，生成 HTTP client span。
var downstreamClient = &http.Client{
	Timeout: 3 * time.Second,
	Transport: otelhttp.NewTransport(&http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 2 * time.Second,
	}),
}

// 请求级元数据用指针在中间件之间共享，避免依赖 r.Pattern 是否被 ServeMux 写回原请求。
type requestMeta struct {
	requestID string
	handler   string
}

type metaKey struct{}

var registerOnce sync.Once

func registerMetrics() {
	registerOnce.Do(func() { prometheus.MustRegister(httpRequests, httpDuration) })
}

func metaFrom(ctx context.Context) *requestMeta {
	if m, ok := ctx.Value(metaKey{}).(*requestMeta); ok {
		return m
	}
	return &requestMeta{handler: "unmatched"}
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// 管理端点（/metrics、/health）与业务链路分开，避免抓取与探针对指标/日志/trace 的污染。
func newHandler() http.Handler {
	business := http.NewServeMux()
	business.HandleFunc("GET /order/{id}", withHandler("GET /order/{id}", orderHandler))

	root := http.NewServeMux()
	// 必须显式打开 OpenMetrics：exemplar 只存在于 OpenMetrics 文本格式中，
	// promhttp.Handler() 默认只协商 text/plain;version=0.0.4，会把 exemplar 全部丢掉，
	// 表现就是 Grafana 直方图上「指标点 -> trace」永远点不出来（静默失效）。
	root.Handle("/metrics", promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))
	root.HandleFunc("GET /health", healthHandler)
	// otelhttp 必须包住业务链路：它从 traceparent 头提取上游上下文并创建 server span，
	// 内层中间件（requestID / observe）才能拿到 span，出站请求也才能继续这条链路。
	// withMeta 放在它外层：span 命名要靠内层写入的 meta（见 spanNameFromMeta）。
	root.Handle("/", withMeta(otelhttp.NewHandler(
		withRequestID(observe(business)),
		"http.server",
		otelhttp.WithSpanNameFormatter(spanNameFromMeta),
	)))
	return root
}

// withMeta 在 otelhttp 之前建立请求级元数据指针：
// 内外层共享同一个 *requestMeta，withHandler 写路由模板，otelhttp 收尾时据此给 span 命名。
func withMeta(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), metaKey{}, &requestMeta{handler: "unmatched"})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// otelhttp 在 handler 返回后回调本函数（此时 r.Pattern 是外层 mux 的 "/"，
// 所以不能用它命名），改为读 withHandler 写入路由模板。
func spanNameFromMeta(_ string, r *http.Request) string {
	if m, ok := r.Context().Value(metaKey{}).(*requestMeta); ok && m.handler != "" && m.handler != "unmatched" {
		return m.handler
	}
	return r.Method
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			// request ID 与 trace ID 对齐：日志、指标 exemplar、trace 用同一个标识，
			// Loki 里点一下 trace_id 就能跳到 Tempo 的瀑布图。
			if sc := oteltrace.SpanContextFromContext(r.Context()); sc.IsValid() {
				id = sc.TraceID().String()
			} else {
				id = newRequestID()
			}
		}
		w.Header().Set(requestIDHeader, id)
		metaFrom(r.Context()).requestID = id
		next.ServeHTTP(w, r)
	})
}

func withHandler(name string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		metaFrom(r.Context()).handler = name
		next(w, r)
	}
}

func observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(ww, r)

		m := metaFrom(r.Context())
		sc := oteltrace.SpanContextFromContext(r.Context())
		labels := []string{strconv.Itoa(ww.statusCode), r.Method, m.handler}
		httpRequests.WithLabelValues(labels...).Inc()
		elapsed := time.Since(start).Seconds()

		if sc.IsValid() {
			// exemplar 是「指标 → trace」的钩子：Grafana 直方图上每个点可以点开直接跳到该次请求的 trace。
			if eo, ok := httpDuration.WithLabelValues(labels...).(prometheus.ExemplarObserver); ok {
				eo.ObserveWithExemplar(elapsed, prometheus.Labels{"trace_id": sc.TraceID().String()})
			} else {
				httpDuration.WithLabelValues(labels...).Observe(elapsed)
			}
			// 路由模板同时作为 span 属性，Tempo 里可按 http.route 聚合。
			span := oteltrace.SpanFromContext(r.Context())
			span.SetAttributes(attribute.String("http.route", m.handler))
		} else {
			httpDuration.WithLabelValues(labels...).Observe(elapsed)
		}

		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"handler", m.handler,
			"status", ww.statusCode,
			"duration_ms", float64(time.Since(start).Microseconds())/1000,
			"request_id", m.requestID,
			"trace_id", sc.TraceID().String(),
			"span_id", sc.SpanID().String(),
		)
	})
}

// statusWriter 只捕获状态码；Unwrap + Flush/Hijack/Push 保证包装后可选接口不丢失。
type statusWriter struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

func (ww *statusWriter) WriteHeader(code int) {
	if ww.wroteHeader {
		return
	}
	ww.wroteHeader = true
	ww.statusCode = code
	ww.ResponseWriter.WriteHeader(code)
}

func (ww *statusWriter) Write(p []byte) (int, error) {
	if !ww.wroteHeader {
		ww.WriteHeader(http.StatusOK)
	}
	return ww.ResponseWriter.Write(p)
}

func (ww *statusWriter) Unwrap() http.ResponseWriter { return ww.ResponseWriter }

func (ww *statusWriter) Flush() {
	if f, ok := ww.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (ww *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := ww.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijack not supported")
}

func (ww *statusWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := ww.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return errors.New("push not supported")
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func orderHandler(w http.ResponseWriter, r *http.Request) {
	orderID := r.PathValue("id")
	userID := r.URL.Query().Get("userId")
	if userID == "" {
		userID = "1"
	}
	// 输入校验：防止 ../、?、# 等改变下游 URL 结构。
	if !validUserID.MatchString(userID) {
		http.Error(w, "invalid userId", http.StatusBadRequest)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		downstreamBaseURL+"/user/"+url.PathEscape(userID), nil)
	if err != nil {
		slog.Error("build downstream request failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// 透传 requestID，让两个服务的日志能用同一个 ID 串起来。
	req.Header.Set(requestIDHeader, metaFrom(r.Context()).requestID)

	resp, err := downstreamClient.Do(req)
	if err != nil {
		// 内部错误只写日志，不外泄给客户端；区分超时(504)与连接失败(502)。
		slog.Error("downstream call failed", "err", err,
			"request_id", metaFrom(r.Context()).requestID)
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			http.Error(w, "downstream timeout", http.StatusGatewayTimeout)
		} else {
			http.Error(w, "downstream unavailable", http.StatusBadGateway)
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("downstream non-200", "status", resp.StatusCode,
			"request_id", metaFrom(r.Context()).requestID)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}

	var user map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		slog.Error("decode downstream response failed", "err", err,
			"request_id", metaFrom(r.Context()).requestID)
		http.Error(w, "invalid upstream response", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"orderId":  orderID,
		"userName": user["name"],
		"email":    user["email"],
		"product":  "云服务器ECS",
	})
}

func isTimeout(err error) bool {
	var nerr net.Error
	return errors.As(err, &nerr) && nerr.Timeout()
}

func main() {
	// 结构化 JSON 日志，Loki 才能用 | json 与 unwrap duration_ms 做数值聚合。
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	registerMetrics()

	// 追踪初始化失败只降级，不阻塞启动。
	if shutdownTracing, err := setupTracing(context.Background()); err != nil {
		slog.Warn("tracing disabled", "err", err)
	} else {
		defer func() {
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := shutdownTracing(flushCtx); err != nil {
				slog.Warn("flush traces failed", "err", err)
			}
		}()
	}

	srv := &http.Server{
		Addr:              ":" + getenv("PORT", "8080"),
		Handler:           newHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		slog.Info("shutting down", "grace_period", "20s")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("order-service starting", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}
