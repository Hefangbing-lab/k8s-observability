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

// 下游地址抽成变量，单元测试可以用 httptest.Server 替换。
var downstreamBaseURL = "http://user-service"

var validUserID = regexp.MustCompile(`^[0-9]{1,18}$`)

// 显式配置超时与连接池：DefaultClient 没有超时，DefaultTransport 每 host 只留 2 条 idle 连接。
var downstreamClient = &http.Client{
	Timeout: 3 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 2 * time.Second,
	},
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

// 管理端点（/metrics、/health）与业务链路分开，避免抓取与探针对指标/日志的污染。
func newHandler() http.Handler {
	business := http.NewServeMux()
	business.HandleFunc("GET /order/{id}", withHandler("GET /order/{id}", orderHandler))

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

	slog.Info("order-service starting", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}
