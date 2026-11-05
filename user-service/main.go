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
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// 默认 DefBuckets 从 5ms 起、间距过大，毫秒级内部调用的 P99 会严重失真。
var latencyBuckets = []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

var (
	httpRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Number of HTTP requests",
		},
		// handler 存路由模板（GET /user/{id}），不是原始 URL，避免标签基数爆炸。
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

// 把「管理端点」与「业务链路」分开：/metrics 和 /health 不经过埋点与访问日志，
// 从根上消除 Prometheus 抓取和 livenessProbe 对 QPS/P99 的污染。
func newHandler() http.Handler {
	business := http.NewServeMux()
	business.HandleFunc("GET /user/{id}", withHandler("GET /user/{id}", userHandler))

	root := http.NewServeMux()
	root.Handle("/metrics", promhttp.Handler())
	root.HandleFunc("GET /health", healthHandler)
	root.Handle("/", withRequestID(observe(business)))
	return root
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set(requestIDHeader, id)
		ctx := context.WithValue(r.Context(), metaKey{}, &requestMeta{requestID: id, handler: "unmatched"})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withHandler 显式给出路由模板名，既做低基数标签，也让日志能按接口聚合。
func withHandler(name string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		metaFrom(r.Context()).handler = name
		next(w, r)
	}
}

// 自己实现埋点，才能加 handler 标签并精确控制哪些路径计入。
func observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(ww, r)

		m := metaFrom(r.Context())
		labels := []string{strconv.Itoa(ww.statusCode), r.Method, m.handler}
		httpRequests.WithLabelValues(labels...).Inc()
		httpDuration.WithLabelValues(labels...).Observe(time.Since(start).Seconds())

		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"handler", m.handler,
			"status", ww.statusCode,
			"duration_ms", float64(time.Since(start).Microseconds())/1000,
			"request_id", m.requestID,
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
	// 只检查自身，绝不依赖下游；否则下游抖动会引发级联重启。
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func userHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id":    id,
		"name":  "User" + id,
		"email": "user" + id + "@example.com",
	})
}

func main() {
	// 结构化 JSON 日志，Loki 才能用 | json 与 unwrap duration_ms 做数值聚合。
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	registerMetrics()

	srv := &http.Server{
		Addr:              ":8080",
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

	slog.Info("user-service starting", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}
