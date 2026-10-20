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
	mux.HandleFunc("GET /user/{id}", userHandler)
	mux.Handle("/metrics", promhttp.Handler())

	var handler http.Handler = mux
	handler = loggingMiddleware(handler)
	handler = promhttp.InstrumentHandlerDuration(httpDuration, handler)
	handler = promhttp.InstrumentHandlerCounter(httpRequests, handler)

	log.Println("user-service starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", handler))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func userHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userData := map[string]string{
		"id":    id,
		"name":  "User" + id,
		"email": "user" + id + "@example.com",
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(userData)
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
