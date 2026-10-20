package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Number of HTTP requests",
		},
		[]string{"code", "method"},
	)
	httpDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"code", "method"},
	)
)

func main() {
	prometheus.MustRegister(httpRequests, httpDuration)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler)
	mux.HandleFunc("GET /order/{id}", orderHandler)
	mux.Handle("/metrics", promhttp.Handler())

	var handler http.Handler = mux
	handler = loggingMiddleware(handler)
	handler = promhttp.InstrumentHandlerDuration(httpDuration, handler)
	handler = promhttp.InstrumentHandlerCounter(httpRequests, handler)

	log.Println("order-service starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", handler))
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

	// 通过 K8s Service 的 DNS 名调 user-service
	resp, err := http.Get("http://user-service/user/" + userID)
	if err != nil {
		http.Error(w, "call user-service failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close() // 必须关，否则连接泄漏——面试点

	var user map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&user)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"orderId":  orderID,
		"userName": user["name"],
		"email":    user["email"],
		"product":  "云服务器ECS",
	})
}

// loggingMiddleware 打印每个请求的方法、路径、状态码和耗时
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusWriter{ResponseWriter: w, statusCode: 200}
		next.ServeHTTP(ww, r)
		log.Printf("request method=%s path=%s status=%d duration=%s",
			r.Method, r.URL.Path, ww.statusCode, time.Since(start))
	})
}

// statusWriter 包装 ResponseWriter 以捕获状态码
type statusWriter struct {
	http.ResponseWriter
	statusCode int
}

func (ww *statusWriter) WriteHeader(code int) {
	ww.statusCode = code
	ww.ResponseWriter.WriteHeader(code)
}
